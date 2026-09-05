// Package aws resolves node economics for EKS/Karpenter nodes from the EC2 spot price
// history and the public Spot Advisor interruption data (ADR-0010).
package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	corev1 "k8s.io/api/core/v1"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/scoring"
)

const (
	LabelEKSCapacityType       = "eks.amazonaws.com/capacityType"
	LabelKarpenterCapacityType = "karpenter.sh/capacity-type"
	LabelInstanceType          = "node.kubernetes.io/instance-type"
	LabelZone                  = "topology.kubernetes.io/zone"
	LabelRegion                = "topology.kubernetes.io/region"

	SpotAdvisorURL = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"

	cacheTTL = 10 * time.Minute
)

// EC2API is the slice of the EC2 client the source needs; tests supply a fake.
type EC2API interface {
	DescribeSpotPriceHistory(ctx context.Context, in *ec2.DescribeSpotPriceHistoryInput, opts ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error)
}

// Advisor is the Spot Advisor document, reduced to what the source reads.
type Advisor struct {
	Ranges      []AdvisorRange                                `json:"ranges"`
	SpotAdvisor map[string]map[string]map[string]AdvisorEntry `json:"spot_advisor"`
}

type AdvisorRange struct {
	Index int     `json:"index"`
	Label string  `json:"label"`
	Max   float64 `json:"max"`
}

type AdvisorEntry struct {
	R int `json:"r"`
	S int `json:"s"`
}

// AdvisorFetcher returns the Spot Advisor document; tests supply a fake.
type AdvisorFetcher interface {
	Fetch(ctx context.Context) (*Advisor, error)
}

type HTTPAdvisor struct {
	Client *http.Client
	URL    string
}

func (h HTTPAdvisor) Fetch(ctx context.Context) (*Advisor, error) {
	url := h.URL
	if url == "" {
		url = SpotAdvisorURL
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch spot advisor: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch spot advisor: HTTP %d", resp.StatusCode)
	}
	return ParseAdvisor(resp.Body)
}

func ParseAdvisor(r io.Reader) (*Advisor, error) {
	var a Advisor
	if err := json.NewDecoder(r).Decode(&a); err != nil {
		return nil, fmt.Errorf("decode spot advisor: %w", err)
	}
	if len(a.Ranges) == 0 || len(a.SpotAdvisor) == 0 {
		return nil, fmt.Errorf("spot advisor document has no ranges or regions")
	}
	return &a, nil
}

// Source prices spot nodes from the latest spot price in their zone and on-demand nodes
// from OnDemand; reclaim risk comes from the Spot Advisor interruption bucket.
type Source struct {
	EC2      EC2API
	Advisor  AdvisorFetcher
	OnDemand map[string]float64
	Now      func() time.Time

	mu        sync.Mutex
	spotCache map[string]cached
	advisor   *Advisor
	advisorAt time.Time
}

type cached struct {
	price float64
	at    time.Time
}

func (s *Source) Economics(node *corev1.Node) (scoring.Economics, bool) {
	labels := node.GetLabels()
	instanceType := labels[LabelInstanceType]
	if instanceType == "" {
		return scoring.Economics{}, false
	}
	spot, ok := capacityType(labels)
	if !ok {
		return scoring.Economics{}, false
	}
	if !spot {
		price, ok := s.OnDemand[instanceType]
		if !ok {
			return scoring.Economics{}, false
		}
		return scoring.Economics{HourlyCost: price}, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	price, err := s.spotPrice(ctx, instanceType, labels[LabelZone])
	if err != nil {
		return scoring.Economics{}, false
	}
	risk, err := s.interruptionRisk(ctx, labels[LabelRegion], instanceType)
	if err != nil {
		return scoring.Economics{}, false
	}
	return scoring.Economics{HourlyCost: price, Spot: true, PreemptionRisk: risk}, true
}

func capacityType(labels map[string]string) (spot bool, ok bool) {
	switch labels[LabelEKSCapacityType] {
	case "SPOT":
		return true, true
	case "ON_DEMAND":
		return false, true
	}
	switch labels[LabelKarpenterCapacityType] {
	case "spot":
		return true, true
	case "on-demand":
		return false, true
	}
	return false, false
}

func (s *Source) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Source) spotPrice(ctx context.Context, instanceType, zone string) (float64, error) {
	key := instanceType + "/" + zone
	s.mu.Lock()
	if c, ok := s.spotCache[key]; ok && s.now().Sub(c.at) < cacheTTL {
		s.mu.Unlock()
		return c.price, nil
	}
	s.mu.Unlock()

	in := &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []types.InstanceType{types.InstanceType(instanceType)},
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:           aws.Time(s.now()),
		MaxResults:          aws.Int32(20),
	}
	if zone != "" {
		in.AvailabilityZone = aws.String(zone)
	}
	out, err := s.EC2.DescribeSpotPriceHistory(ctx, in)
	if err != nil {
		return 0, fmt.Errorf("describe spot price history: %w", err)
	}
	price, err := LatestSpotPrice(out.SpotPriceHistory)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if s.spotCache == nil {
		s.spotCache = map[string]cached{}
	}
	s.spotCache[key] = cached{price: price, at: s.now()}
	s.mu.Unlock()
	return price, nil
}

// LatestSpotPrice picks the newest entry; the API returns history, not a single quote.
func LatestSpotPrice(history []types.SpotPrice) (float64, error) {
	var latest *types.SpotPrice
	for i := range history {
		h := &history[i]
		if h.SpotPrice == nil {
			continue
		}
		if latest == nil || (h.Timestamp != nil && latest.Timestamp != nil && h.Timestamp.After(*latest.Timestamp)) {
			latest = h
		}
	}
	if latest == nil {
		return 0, fmt.Errorf("spot price history is empty")
	}
	price, err := strconv.ParseFloat(aws.ToString(latest.SpotPrice), 64)
	if err != nil || price <= 0 {
		return 0, fmt.Errorf("spot price %q is not a positive number", aws.ToString(latest.SpotPrice))
	}
	return price, nil
}

func (s *Source) interruptionRisk(ctx context.Context, region, instanceType string) (float64, error) {
	s.mu.Lock()
	adv := s.advisor
	stale := adv == nil || s.now().Sub(s.advisorAt) >= cacheTTL
	s.mu.Unlock()
	if stale {
		fresh, err := s.Advisor.Fetch(ctx)
		if err != nil {
			return 0, err
		}
		s.mu.Lock()
		s.advisor, s.advisorAt = fresh, s.now()
		adv = fresh
		s.mu.Unlock()
	}
	return adv.Risk(region, instanceType)
}

// Risk maps the advisor's interruption-frequency bucket to its upper bound as a fraction,
// so ">20%" becomes 1.0 and "<5%" becomes 0.05.
func (a *Advisor) Risk(region, instanceType string) (float64, error) {
	entry, ok := a.SpotAdvisor[region]["Linux"][instanceType]
	if !ok {
		return 0, fmt.Errorf("spot advisor has no entry for %s in %s", instanceType, region)
	}
	for _, r := range a.Ranges {
		if r.Index == entry.R {
			return r.Max / 100, nil
		}
	}
	return 0, fmt.Errorf("spot advisor bucket %d has no range", entry.R)
}
