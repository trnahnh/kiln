// Package slo reads a workload's request counters from Istio's standard metrics in
// Prometheus. Controllers turn two snapshots into a window and decide; nothing here decides.
package slo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Reporter selects which sidecar's view of a request is read. The destination sidecar
// counts every request that reached the workload; the source sidecar counts what the
// caller experienced, including network delay and connections that never got a response.
type Reporter string

const (
	ReporterDestination Reporter = "destination"
	ReporterSource      Reporter = "source"
)

type Target struct {
	Namespace    string
	Workload     string
	LatencyMaxMs float64
	// Empty means destination.
	Reporter Reporter
}

// Counters are cumulative since the reporting sidecars started, summed over their pods.
type Counters struct {
	Requests float64
	Errors   float64
	Slow     float64
}

type Source interface {
	Counters(ctx context.Context, t Target) (Counters, error)
}

// Delta turns two cumulative snapshots into one window. A counter that went backwards was
// reset by a pod restart; the current value is then the best estimate of what happened
// since. Failures never exceed requests.
func Delta(prev, cur Counters) Counters {
	d := func(p, c float64) float64 {
		if c < p {
			return math.Round(c)
		}
		return math.Round(c - p)
	}
	w := Counters{Requests: d(prev.Requests, cur.Requests), Errors: d(prev.Errors, cur.Errors), Slow: d(prev.Slow, cur.Slow)}
	w.Errors = math.Min(w.Errors, w.Requests)
	w.Slow = math.Min(w.Slow, w.Requests)
	return w
}

type Prometheus struct {
	BaseURL string
	Client  *http.Client
}

func NewPrometheus(baseURL string) *Prometheus {
	return &Prometheus{BaseURL: baseURL, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (p *Prometheus) Counters(ctx context.Context, t Target) (Counters, error) {
	reporter := t.Reporter
	if reporter == "" {
		reporter = ReporterDestination
	}
	sel := fmt.Sprintf(`reporter=%q,destination_workload=%q,destination_workload_namespace=%q`, reporter, t.Workload, t.Namespace)
	total, err := p.scalar(ctx, fmt.Sprintf(`sum(istio_request_duration_milliseconds_count{%s})`, sel))
	if err != nil {
		return Counters{}, fmt.Errorf("requests: %w", err)
	}
	errs, err := p.scalar(ctx, fmt.Sprintf(`sum(istio_requests_total{%s,response_code=~%q})`, sel, errorCodes(reporter)))
	if err != nil {
		return Counters{}, fmt.Errorf("errors: %w", err)
	}
	buckets, err := p.buckets(ctx, fmt.Sprintf(`sum by (le) (istio_request_duration_milliseconds_bucket{%s})`, sel))
	if err != nil {
		return Counters{}, fmt.Errorf("latency buckets: %w", err)
	}
	return Counters{Requests: total, Errors: errs, Slow: math.Max(0, total-CountAtOrBelow(buckets, t.LatencyMaxMs))}, nil
}

// A caller whose request got no response at all (its own timeout, a reset connection) is
// reported by its sidecar as response code 0; from the caller's side that is a failure.
func errorCodes(r Reporter) string {
	if r == ReporterSource {
		return "5..|0"
	}
	return "5.."
}

type Bucket struct {
	Le    float64
	Count float64
}

// CountAtOrBelow interpolates the cumulative histogram linearly inside the bucket that
// contains the threshold, the same assumption histogram_quantile makes, so a threshold that
// is not a bucket boundary still yields a usable count.
func CountAtOrBelow(buckets []Bucket, threshold float64) float64 {
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Le < buckets[j].Le })
	loLe, loCount := 0.0, 0.0
	for _, b := range buckets {
		switch {
		case b.Le == threshold:
			return b.Count
		case b.Le < threshold:
			loLe, loCount = b.Le, b.Count
		case math.IsInf(b.Le, 1):
			return loCount
		default:
			return loCount + (b.Count-loCount)*(threshold-loLe)/(b.Le-loLe)
		}
	}
	return loCount
}

type response struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (p *Prometheus) query(ctx context.Context, q string) (*response, error) {
	u := p.BaseURL + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned HTTP %d", resp.StatusCode)
	}
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus status %q: %s", r.Status, r.Error)
	}
	return &r, nil
}

func (p *Prometheus) scalar(ctx context.Context, q string) (float64, error) {
	r, err := p.query(ctx, q)
	if err != nil {
		return 0, err
	}
	if len(r.Data.Result) == 0 {
		return 0, nil
	}
	return sampleValue(r.Data.Result[0].Value)
}

func (p *Prometheus) buckets(ctx context.Context, q string) ([]Bucket, error) {
	r, err := p.query(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(r.Data.Result))
	for _, item := range r.Data.Result {
		le, err := strconv.ParseFloat(item.Metric["le"], 64)
		if err != nil {
			return nil, fmt.Errorf("bucket le %q: %w", item.Metric["le"], err)
		}
		v, err := sampleValue(item.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, Bucket{Le: le, Count: v})
	}
	return out, nil
}

func sampleValue(v []json.RawMessage) (float64, error) {
	if len(v) != 2 {
		return 0, fmt.Errorf("malformed sample %s", v)
	}
	var s string
	if err := json.Unmarshal(v[1], &s); err != nil {
		return 0, fmt.Errorf("malformed sample value %s: %w", v[1], err)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("sample value %q: %w", s, err)
	}
	if math.IsNaN(f) {
		return 0, nil
	}
	return f, nil
}
