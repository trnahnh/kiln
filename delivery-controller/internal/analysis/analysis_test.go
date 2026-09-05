package analysis

import (
	"math"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		ErrorRateMax:     0.01,
		LatencyTailMax:   0.01,
		MinSampleSize:    500,
		Alpha:            0.05,
		Beta:             0.10,
		RegressionFactor: 2,
		Checkpoints:      []int{5, 20, 50, 100},
		MaxStepDuration:  30 * time.Minute,
	}
}

var t0 = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func healthy(n int64) Sample { return Sample{Requests: n} }

func TestValidateRejectsBadConfigs(t *testing.T) {
	cases := map[string]func(*Config){
		"error rate zero":        func(c *Config) { c.ErrorRateMax = 0 },
		"latency tail one":       func(c *Config) { c.LatencyTailMax = 1 },
		"min sample zero":        func(c *Config) { c.MinSampleSize = 0 },
		"alpha half":             func(c *Config) { c.Alpha = 0.5 },
		"beta zero":              func(c *Config) { c.Beta = 0 },
		"factor one":             func(c *Config) { c.RegressionFactor = 1 },
		"no checkpoints":         func(c *Config) { c.Checkpoints = nil },
		"not ending at 100":      func(c *Config) { c.Checkpoints = []int{5, 50} },
		"not ascending":          func(c *Config) { c.Checkpoints = []int{50, 20, 100} },
		"zero max step duration": func(c *Config) { c.MaxStepDuration = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected an error")
			}
		})
	}
	if err := testConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestBoundsFollowWald(t *testing.T) {
	cfg := testConfig()
	wantA := math.Log((1 - 0.10) / 0.025)
	wantB := -math.Log(0.10 / (1 - 0.025))
	if math.Abs(cfg.RollbackBound()-wantA) > 1e-9 || math.Abs(cfg.AcceptMargin()-wantB) > 1e-9 {
		t.Fatalf("A=%v B=%v, want %v %v", cfg.RollbackBound(), cfg.AcceptMargin(), wantA, wantB)
	}
}

func TestStartEntersFirstCheckpointOutright(t *testing.T) {
	st := Start(testConfig(), t0)
	if st.Weight != 5 || st.Checkpoint != 0 || !st.CheckpointStartedAt.Equal(t0) {
		t.Fatalf("unexpected start state %+v", st)
	}
}

func TestNothingFiresBeforeMinSampleSize(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	// 40% errors is a flagrant regression, but 100 requests are under the gate; the evidence
	// is still banked so the very next window can fire.
	d := Tick(cfg, &st, t0.Add(time.Minute), Sample{Requests: 100, Errors: 40}, true)
	if d.Action != Hold || d.Weight != 5 {
		t.Fatalf("got %+v", d)
	}
	if st.Errors.Cumulative <= 0 {
		t.Fatalf("evidence was not banked: %+v", st)
	}
}

func TestRollbackNeedsRepeatedEvidenceBecauseTicksAreCapped(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	now := t0
	var d Decision
	ticks := 0
	for ; ticks < 10; ticks++ {
		now = now.Add(15 * time.Second)
		d = Tick(cfg, &st, now, Sample{Requests: 600, Errors: 200}, true)
		if d.Action == Rollback {
			break
		}
	}
	if d.Action != Rollback || d.Reason != ReasonRegressionDetected || d.Criterion != "errorRate" {
		t.Fatalf("got %+v after %d ticks", d, ticks)
	}
	if ticks != 2 {
		t.Fatalf("a flagrant regression should take exactly three capped ticks, took %d", ticks+1)
	}
}

func TestLatencyRegressionRollsBackOnItsOwnCriterion(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	var d Decision
	for i := 0; i < 5 && d.Action != Rollback; i++ {
		d = Tick(cfg, &st, t0.Add(time.Duration(i+1)*15*time.Second), Sample{Requests: 600, Slow: 120}, true)
	}
	if d.Action != Rollback || d.Criterion != "latencyP99" {
		t.Fatalf("got %+v", d)
	}
}

func TestCumulativeEvidenceFloorsAtZero(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	for i := range 50 {
		Tick(cfg, &st, t0.Add(time.Duration(i+1)*15*time.Second), healthy(1000), true)
	}
	if st.Errors.Cumulative != 0 || st.Latency.Cumulative != 0 {
		t.Fatalf("healthy traffic banked credit: %+v", st)
	}
}

func TestAcceptanceMovesTrafficAndResetsCheckpoint(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	now := t0
	// Two clean windows of 600 clear the gate and the accept margin (each capped tick is a
	// third of the rollback bound, the margin is under two thirds of it).
	now = now.Add(15 * time.Second)
	d := Tick(cfg, &st, now, healthy(600), true)
	if d.Action != Shift || d.Weight <= 5 || d.Weight > 20 {
		t.Fatalf("first clean window should move toward 20: %+v", d)
	}
	now = now.Add(15 * time.Second)
	d = Tick(cfg, &st, now, healthy(600), true)
	if d.Action != Shift || d.Weight != 20 || st.Checkpoint != 1 {
		t.Fatalf("second clean window should arrive at 20: %+v state %+v", d, st)
	}
	if st.SamplesSinceCheckpoint != 0 || st.Errors.SinceCheckpoint != 0 || !st.CheckpointStartedAt.Equal(now) {
		t.Fatalf("checkpoint arrival did not reset acceptance state: %+v", st)
	}
	now = now.Add(15 * time.Second)
	d = Tick(cfg, &st, now, healthy(100), true)
	if d.Action != Hold || d.Weight != 20 {
		t.Fatalf("fresh checkpoint must re-earn its samples: %+v", d)
	}
}

func TestAnomalyHoldsAndHalvesNextSubStep(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	now := t0.Add(15 * time.Second)
	Tick(cfg, &st, now, healthy(600), true)
	before := st.Weight
	now = now.Add(15 * time.Second)
	d := Tick(cfg, &st, now, Sample{Requests: 600, Errors: 9}, true)
	if d.Action != Hold || !d.Anomaly || st.Shrink != 1 || st.Weight != before {
		t.Fatalf("anomaly should hold: %+v state %+v", d, st)
	}
	confidenceBefore := st.confidence(cfg.AcceptMargin())
	now = now.Add(15 * time.Second)
	d = Tick(cfg, &st, now, healthy(600), true)
	if d.Action != Shift {
		t.Fatalf("clean window after anomaly should move: %+v", d)
	}
	full := int(math.Round(confidenceBefore*15)) + 5 - before
	if full > 1 && d.Weight-before > full/2+1 {
		t.Fatalf("sub-step %d was not halved from %d", d.Weight-before, full)
	}
	if st.Shrink != 0 {
		t.Fatalf("shrink should reset once a step is taken: %+v", st)
	}
}

func TestPromoteOnlyAfterLastCheckpointIsAccepted(t *testing.T) {
	cfg := testConfig()
	cfg.Checkpoints = []int{100}
	st := Start(cfg, t0)
	d := Tick(cfg, &st, t0.Add(15*time.Second), healthy(600), true)
	if d.Action != Hold {
		t.Fatalf("one clean window is not the full margin: %+v", d)
	}
	d = Tick(cfg, &st, t0.Add(30*time.Second), healthy(600), true)
	if d.Action != Promote || d.Weight != 100 || d.Reason != ReasonAccepted {
		t.Fatalf("got %+v", d)
	}
}

func TestTimeoutRollsBackInconclusive(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	now := t0
	var d Decision
	for i := 0; i < 200 && d.Action != Rollback; i++ {
		now = now.Add(15 * time.Second)
		// Exactly at the limit: expected evidence per window is slightly negative but the
		// CUSUM never accepts, so the checkpoint never earns its margin.
		d = Tick(cfg, &st, now, Sample{Requests: 300, Errors: 4, Slow: 4}, true)
	}
	if d.Action != Rollback || d.Reason != ReasonInconclusive {
		t.Fatalf("got %+v at %v", d, now.Sub(t0))
	}
}

func TestNoMetricsRollsBackAsUnavailable(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	d := Tick(cfg, &st, t0.Add(31*time.Minute), Sample{}, false)
	if d.Action != Rollback || d.Reason != ReasonMetricsUnavailable {
		t.Fatalf("got %+v", d)
	}
}

func TestUnreadableWindowIsIgnoredNotCounted(t *testing.T) {
	cfg := testConfig()
	st := Start(cfg, t0)
	Tick(cfg, &st, t0.Add(15*time.Second), Sample{Requests: 1000, Errors: 900}, false)
	if st.TotalSamples != 0 || st.Errors.Cumulative != 0 {
		t.Fatalf("unreadable window changed state: %+v", st)
	}
}
