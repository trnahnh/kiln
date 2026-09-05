//go:build liveaws

// Manual validation only (TESTING.md): confirms the fakes in aws_test.go mirror the real
// DescribeSpotPriceHistory and Spot Advisor responses. Uses the standard AWS credential
// chain on the developer's machine, never CI, and makes a handful of read-only calls.
//
//	go test -tags=liveaws ./internal/pricing/aws/ -run Live -v
package aws

import (
	"context"
	"net/http"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestLiveSpotPriceHistoryMatchesFakeShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatalf("load AWS config (credentials are required for this manual check): %v", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	client := ec2.NewFromConfig(cfg)
	out, err := client.DescribeSpotPriceHistory(ctx, &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []types.InstanceType{types.InstanceTypeM5Large},
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:           awssdk.Time(time.Now()),
		MaxResults:          awssdk.Int32(20),
	})
	if err != nil {
		t.Fatalf("DescribeSpotPriceHistory: %v", err)
	}
	if len(out.SpotPriceHistory) == 0 {
		t.Fatal("live history is empty; the fake assumes at least one entry")
	}
	for _, h := range out.SpotPriceHistory {
		if h.SpotPrice == nil || h.Timestamp == nil || h.AvailabilityZone == nil {
			t.Fatalf("live entry missing a field the fake assumes present: %+v", h)
		}
	}
	price, err := LatestSpotPrice(out.SpotPriceHistory)
	if err != nil {
		t.Fatalf("LatestSpotPrice on live data: %v", err)
	}
	t.Logf("live m5.large spot price in %s: %.4f USD/h from %d entries", cfg.Region, price, len(out.SpotPriceHistory))
}

func TestLiveSpotAdvisorMatchesFakeShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	adv, err := HTTPAdvisor{Client: &http.Client{Timeout: 45 * time.Second}}.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(adv.Ranges) != 5 {
		t.Fatalf("fake assumes 5 interruption buckets, live has %d", len(adv.Ranges))
	}
	risk, err := adv.Risk("us-east-1", "m5.large")
	if err != nil {
		t.Fatal(err)
	}
	if risk <= 0 || risk > 1 {
		t.Fatalf("live risk %v outside (0,1]", risk)
	}
	t.Logf("live m5.large us-east-1 interruption risk bucket: %.2f", risk)
}
