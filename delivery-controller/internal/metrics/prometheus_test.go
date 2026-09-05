package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trnahnh/kiln/delivery-controller/internal/analysis"
)

func TestCountAtOrBelowInterpolatesInsideTheBucket(t *testing.T) {
	buckets := []Bucket{{Le: 500, Count: 1000}, {Le: 250, Count: 900}, {Le: math.Inf(1), Count: 1000}, {Le: 100, Count: 800}}
	cases := []struct {
		threshold float64
		want      float64
	}{
		{250, 900},
		{500, 1000},
		{300, 900 + 100*50.0/250},
		{50, 400},
		{10000, 1000},
	}
	for _, tc := range cases {
		if got := CountAtOrBelow(buckets, tc.threshold); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("threshold %v: got %v want %v", tc.threshold, got, tc.want)
		}
	}
	if got := CountAtOrBelow(nil, 300); got != 0 {
		t.Errorf("no buckets should count zero, got %v", got)
	}
}

func TestDeltaHandlesCounterResets(t *testing.T) {
	prev := Counters{Requests: 1000, Errors: 10, Slow: 5}
	got := Delta(prev, Counters{Requests: 1600, Errors: 16, Slow: 8})
	if got != (analysis.Sample{Requests: 600, Errors: 6, Slow: 3}) {
		t.Fatalf("plain delta: %+v", got)
	}
	got = Delta(prev, Counters{Requests: 200, Errors: 3, Slow: 1})
	if got != (analysis.Sample{Requests: 200, Errors: 3, Slow: 1}) {
		t.Fatalf("reset should use the current value: %+v", got)
	}
	got = Delta(prev, Counters{Requests: 1001, Errors: 40, Slow: 40})
	if got.Errors != 1 || got.Slow != 1 {
		t.Fatalf("failures never exceed requests: %+v", got)
	}
}

func vector(rows ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": rows}})
	return string(b)
}

func row(value float64, labels map[string]string) map[string]any {
	if labels == nil {
		labels = map[string]string{}
	}
	return map[string]any{"metric": labels, "value": []any{1700000000.0, fmt.Sprintf("%v", value)}}
}

func fakePrometheus(t *testing.T, handler func(q string) (int, string)) *Prometheus {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		code, body := handler(r.URL.Query().Get("query"))
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewPrometheus(srv.URL)
}

func TestCountersQueriesTheDestinationSidecarAndInterpolatesSlow(t *testing.T) {
	var queries []string
	p := fakePrometheus(t, func(q string) (int, string) {
		queries = append(queries, q)
		switch {
		case strings.HasPrefix(q, "sum(istio_request_duration_milliseconds_count"):
			return 200, vector(row(1000, nil))
		case strings.HasPrefix(q, "sum(istio_requests_total"):
			return 200, vector(row(12, nil))
		case strings.HasPrefix(q, "sum by (le) (istio_request_duration_milliseconds_bucket"):
			return 200, vector(row(800, map[string]string{"le": "100"}), row(900, map[string]string{"le": "250"}), row(1000, map[string]string{"le": "500"}), row(1000, map[string]string{"le": "+Inf"}))
		}
		return 400, `{"status":"error","error":"unexpected query"}`
	})
	c, err := p.Counters(context.Background(), Target{Namespace: "shop", Workload: "checkout", LatencyMaxMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	if c.Requests != 1000 || c.Errors != 12 || math.Abs(c.Slow-80) > 1e-9 {
		t.Fatalf("got %+v", c)
	}
	for _, q := range queries {
		for _, want := range []string{`reporter="destination"`, `destination_workload="checkout"`, `destination_workload_namespace="shop"`} {
			if !strings.Contains(q, want) {
				t.Errorf("query %q lacks %s", q, want)
			}
		}
	}
	if !strings.Contains(queries[1], `response_code=~"5.."`) {
		t.Errorf("error query does not select 5xx: %q", queries[1])
	}
}

func TestCountersWithNoSeriesYetIsZeroNotAnError(t *testing.T) {
	p := fakePrometheus(t, func(string) (int, string) { return 200, vector() })
	c, err := p.Counters(context.Background(), Target{Namespace: "shop", Workload: "checkout", LatencyMaxMs: 300})
	if err != nil || c != (Counters{}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestCountersReportsPrometheusFailures(t *testing.T) {
	p := fakePrometheus(t, func(string) (int, string) { return 503, "down" })
	if _, err := p.Counters(context.Background(), Target{Workload: "x"}); err == nil {
		t.Fatal("expected an error")
	}
	p = fakePrometheus(t, func(string) (int, string) { return 200, `{"status":"error","error":"bad query"}` })
	if _, err := p.Counters(context.Background(), Target{Workload: "x"}); err == nil || !strings.Contains(err.Error(), "bad query") {
		t.Fatalf("expected the prometheus error, got %v", err)
	}
}
