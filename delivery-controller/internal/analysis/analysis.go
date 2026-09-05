// Package analysis decides, from request counts alone, whether a canary keeps receiving
// traffic, gets more, is promoted, or is rolled back. It knows nothing about Kubernetes,
// Istio or Prometheus so the decision can be exercised against simulated traffic.
package analysis

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type Config struct {
	// Null-hypothesis rates: the highest acceptable fraction of failed and of slow requests.
	ErrorRateMax   float64
	LatencyTailMax float64
	// Requests the canary must serve at the current checkpoint before any decision fires.
	MinSampleSize int64
	// Ceilings on false rollback (alpha) and missed regression (beta).
	Alpha, Beta float64
	// The alternative hypothesis is RegressionFactor times the null rate.
	RegressionFactor float64
	// Ascending canary traffic checkpoints ending at 100.
	Checkpoints []int
	// A checkpoint that reaches neither bound within this window rolls back.
	MaxStepDuration time.Duration
}

// Tick evidence is capped at this fraction of the rollback bound in either direction, so a
// single burst of errors or a single quiet window cannot decide a rollout by itself.
const tickEvidenceCap = 1.0 / 3

const DefaultLatencyTailMax = 0.01

func (c Config) Validate() error {
	var errs []error
	if c.ErrorRateMax <= 0 || c.ErrorRateMax >= 1 {
		errs = append(errs, fmt.Errorf("errorRateMax %v must be in (0,1)", c.ErrorRateMax))
	}
	if c.LatencyTailMax <= 0 || c.LatencyTailMax >= 1 {
		errs = append(errs, fmt.Errorf("latencyTailMax %v must be in (0,1)", c.LatencyTailMax))
	}
	if c.MinSampleSize < 1 {
		errs = append(errs, fmt.Errorf("minSampleSize %d must be at least 1", c.MinSampleSize))
	}
	if c.Alpha <= 0 || c.Alpha >= 0.5 || c.Beta <= 0 || c.Beta >= 0.5 {
		errs = append(errs, fmt.Errorf("alpha %v and beta %v must be in (0,0.5)", c.Alpha, c.Beta))
	}
	if c.RegressionFactor <= 1 {
		errs = append(errs, fmt.Errorf("regressionFactor %v must exceed 1", c.RegressionFactor))
	}
	if len(c.Checkpoints) == 0 || c.Checkpoints[len(c.Checkpoints)-1] != 100 {
		errs = append(errs, errors.New("checkpoints must end at 100"))
	}
	for i, p := range c.Checkpoints {
		if p < 1 || p > 100 || (i > 0 && p <= c.Checkpoints[i-1]) {
			errs = append(errs, fmt.Errorf("checkpoints must be ascending percentages in [1,100], got %v", c.Checkpoints))
			break
		}
	}
	if c.MaxStepDuration <= 0 {
		errs = append(errs, errors.New("maxStepDuration must be positive"))
	}
	return errors.Join(errs...)
}

// Two criteria share the configured alpha (Bonferroni), so the rollout-level false-rollback
// rate stays under the ceiling whichever one fires.
func (c Config) perCriterionAlpha() float64 { return c.Alpha / 2 }

// RollbackBound is Wald's upper threshold A = ln((1-beta)/alpha).
func (c Config) RollbackBound() float64 {
	return math.Log((1 - c.Beta) / c.perCriterionAlpha())
}

// AcceptMargin is the magnitude of Wald's lower threshold B = ln(beta/(1-alpha)).
func (c Config) AcceptMargin() float64 {
	return -math.Log(c.Beta / (1 - c.perCriterionAlpha()))
}

// Criterion is one Bernoulli sequential test. Cumulative is a CUSUM: it floors at zero so a
// long healthy stretch cannot bank credit that later hides a slow-building regression.
// SinceCheckpoint is the plain log-likelihood ratio accumulated since the last checkpoint
// was reached; its descent below zero is what earns acceptance and sizes sub-steps.
type Criterion struct {
	Cumulative      float64
	SinceCheckpoint float64
}

type State struct {
	Checkpoint             int
	Weight                 int
	Errors                 Criterion
	Latency                Criterion
	SamplesSinceCheckpoint int64
	TotalSamples           int64
	Shrink                 int
	Anomalies              int
	CheckpointStartedAt    time.Time
}

type Sample struct {
	Requests int64
	Errors   int64
	Slow     int64
}

type Action int

const (
	// Hold keeps the current weight.
	Hold Action = iota
	// Shift moves the canary weight to Decision.Weight.
	Shift
	// Promote sends all traffic to the new version.
	Promote
	// Rollback removes the canary.
	Rollback
)

func (a Action) String() string {
	switch a {
	case Hold:
		return "Hold"
	case Shift:
		return "Shift"
	case Promote:
		return "Promote"
	case Rollback:
		return "Rollback"
	}
	return fmt.Sprintf("Action(%d)", int(a))
}

type Reason string

const (
	ReasonRegressionDetected Reason = "RegressionDetected"
	ReasonInconclusive       Reason = "Inconclusive"
	ReasonMetricsUnavailable Reason = "MetricsUnavailable"
	ReasonAccepted           Reason = "Accepted"
)

type Decision struct {
	Action     Action
	Weight     int
	Reason     Reason
	Criterion  string
	Confidence float64
	Anomaly    bool
}

// Start enters the first checkpoint outright: with no traffic there is no evidence, so the
// first step is the only one taken without earning it.
func Start(cfg Config, now time.Time) State {
	return State{Weight: cfg.Checkpoints[0], CheckpointStartedAt: now}
}

// Tick folds one window of canary traffic into the state and returns what to do next.
// metricsOK is false when the window could not be read at all; the sample is then ignored
// and only the checkpoint clock advances.
func Tick(cfg Config, st *State, now time.Time, s Sample, metricsOK bool) Decision {
	anomaly := false
	if metricsOK && s.Requests > 0 {
		errInc := cfg.increment(cfg.ErrorRateMax, s.Requests, s.Errors)
		latInc := cfg.increment(cfg.LatencyTailMax, s.Requests, s.Slow)
		st.Errors.add(errInc)
		st.Latency.add(latInc)
		st.SamplesSinceCheckpoint += s.Requests
		st.TotalSamples += s.Requests
		anomaly = errInc > 0 || latInc > 0
	}

	bound := cfg.RollbackBound()
	if st.Errors.Cumulative >= bound {
		return Decision{Action: Rollback, Weight: 0, Reason: ReasonRegressionDetected, Criterion: "errorRate", Anomaly: anomaly}
	}
	if st.Latency.Cumulative >= bound {
		return Decision{Action: Rollback, Weight: 0, Reason: ReasonRegressionDetected, Criterion: "latencyP99", Anomaly: anomaly}
	}
	if now.Sub(st.CheckpointStartedAt) > cfg.MaxStepDuration {
		reason := ReasonInconclusive
		if !metricsOK || st.SamplesSinceCheckpoint == 0 {
			reason = ReasonMetricsUnavailable
		}
		return Decision{Action: Rollback, Weight: 0, Reason: reason, Anomaly: anomaly}
	}

	confidence := st.confidence(cfg.AcceptMargin())
	if st.SamplesSinceCheckpoint < cfg.MinSampleSize {
		return Decision{Action: Hold, Weight: st.Weight, Confidence: confidence, Anomaly: anomaly}
	}
	if anomaly {
		st.Shrink++
		st.Anomalies++
		return Decision{Action: Hold, Weight: st.Weight, Confidence: confidence, Anomaly: true}
	}

	last := len(cfg.Checkpoints) - 1
	if st.Checkpoint == last {
		if confidence >= 1 {
			return Decision{Action: Promote, Weight: 100, Reason: ReasonAccepted, Confidence: 1}
		}
		return Decision{Action: Hold, Weight: st.Weight, Confidence: confidence}
	}

	from, to := cfg.Checkpoints[st.Checkpoint], cfg.Checkpoints[st.Checkpoint+1]
	desired := from + int(math.Round(confidence*float64(to-from)))
	if desired <= st.Weight {
		return Decision{Action: Hold, Weight: st.Weight, Confidence: confidence}
	}
	step := max(1, (desired-st.Weight)>>uint(st.Shrink))
	st.Shrink = 0
	st.Weight += step
	if st.Weight >= to {
		st.Weight = to
		st.Checkpoint++
		st.Errors.SinceCheckpoint = 0
		st.Latency.SinceCheckpoint = 0
		st.SamplesSinceCheckpoint = 0
		st.CheckpointStartedAt = now
	}
	return Decision{Action: Shift, Weight: st.Weight, Confidence: confidence}
}

// Confidence is how far both criteria have descended toward acceptance since the checkpoint,
// as a fraction of the accept margin, clamped to [0,1].
func (st *State) confidence(margin float64) float64 {
	c := math.Min(-st.Errors.SinceCheckpoint, -st.Latency.SinceCheckpoint) / margin
	return math.Max(0, math.Min(1, c))
}

func (c *Criterion) add(inc float64) {
	c.Cumulative = math.Max(0, c.Cumulative+inc)
	c.SinceCheckpoint += inc
}

// increment is the log-likelihood ratio of x failures in n trials under p1 = k*p0 versus p0,
// capped per tick.
func (cfg Config) increment(p0 float64, n, x int64) float64 {
	p1 := math.Min(cfg.RegressionFactor*p0, 0.999)
	llr := float64(x)*math.Log(p1/p0) + float64(n-x)*math.Log((1-p1)/(1-p0))
	cap := tickEvidenceCap * cfg.RollbackBound()
	return math.Max(-cap, math.Min(cap, llr))
}
