package analysis

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
	"time"
)

// Simulated canaries: a rollout is driven tick by tick with request counts drawn from a
// traffic model, exactly what the controller feeds the analysis from Prometheus. Only the
// decision is under test; no cluster, mesh or metric store is involved.

const (
	simTick       = 15 * time.Second
	simTotalRPS   = 200.0
	simRuns       = 1000
	simMaxTicks   = 400
	simOnsetTick  = 4
	simSpikeTick  = 6
	simFPRCeiling = 0.05 // alpha in testConfig
	simMissCeil   = 0.10 // beta in testConfig
)

// window yields the failure and slow probabilities for the tick with the given index.
type trafficModel func(rng *rand.Rand, tick int) (pErr, pSlow float64)

func steady(pErr, pSlow float64) trafficModel {
	return func(*rand.Rand, int) (float64, float64) { return pErr, pSlow }
}

func stepRegression(pBefore, pAfter float64) trafficModel {
	return func(_ *rand.Rand, tick int) (float64, float64) {
		if tick >= simOnsetTick {
			return pAfter, 0.002
		}
		return pBefore, 0.002
	}
}

func rampRegression(pStart, pEnd float64, over int) trafficModel {
	return func(_ *rand.Rand, tick int) (float64, float64) {
		f := math.Min(1, math.Max(0, float64(tick-simOnsetTick)/float64(over)))
		return pStart + f*(pEnd-pStart), 0.002
	}
}

func latencyRegression(pSlowAfter float64) trafficModel {
	return func(_ *rand.Rand, tick int) (float64, float64) {
		if tick >= simOnsetTick {
			return 0.003, pSlowAfter
		}
		return 0.003, 0.002
	}
}

// bursty draws each window's error rate from a Beta distribution with the given mean and a
// coefficient of variation of one, so errors cluster: most windows are quiet, some run at
// several times the long-run rate while the long-run rate stays under the limit.
func bursty(mean float64) trafficModel {
	a := (1 - mean) / mean * mean
	b := a * (1 - mean) / mean
	return func(rng *rand.Rand, _ int) (float64, float64) {
		return betaSample(rng, a, b), 0.002
	}
}

func spike(base, spikeRate float64) trafficModel {
	return func(_ *rand.Rand, tick int) (float64, float64) {
		if tick == simSpikeTick {
			return spikeRate, 0.002
		}
		return base, 0.002
	}
}

func betaSample(rng *rand.Rand, a, b float64) float64 {
	x := gammaSample(rng, a)
	y := gammaSample(rng, b)
	return x / (x + y)
}

// Marsaglia-Tsang, with the boost for shape < 1.
func gammaSample(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		return gammaSample(rng, shape+1) * math.Pow(rng.Float64(), 1/shape)
	}
	d := shape - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

func binomial(rng *rand.Rand, n int64, p float64) int64 {
	var k int64
	for range n {
		if rng.Float64() < p {
			k++
		}
	}
	return k
}

func poisson(rng *rand.Rand, mean float64) int64 {
	l := math.Exp(-mean)
	var k int64
	p := 1.0
	for {
		p *= rng.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

type runResult struct {
	final     Action
	reason    Reason
	ticks     int
	anomalies int
}

func simulate(cfg Config, model trafficModel, seed uint64) runResult {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	st := Start(cfg, t0)
	now := t0
	for tick := 1; tick <= simMaxTicks; tick++ {
		now = now.Add(simTick)
		pErr, pSlow := model(rng, tick)
		n := poisson(rng, simTotalRPS*float64(st.Weight)/100*simTick.Seconds())
		s := Sample{Requests: n, Errors: binomial(rng, n, pErr), Slow: binomial(rng, n, pSlow)}
		d := Tick(cfg, &st, now, s, true)
		if d.Action == Promote || d.Action == Rollback {
			return runResult{final: d.Action, reason: d.Reason, ticks: tick, anomalies: st.Anomalies}
		}
	}
	return runResult{final: Hold, ticks: simMaxTicks, anomalies: st.Anomalies}
}

type classStats struct {
	rollbacks, promotes, undecided int
	ticks                          []int
}

func runClass(cfg Config, model trafficModel) classStats {
	var cs classStats
	for i := range simRuns {
		r := simulate(cfg, model, uint64(i+1))
		switch r.final {
		case Rollback:
			cs.rollbacks++
		case Promote:
			cs.promotes++
		default:
			cs.undecided++
		}
		cs.ticks = append(cs.ticks, r.ticks)
	}
	sort.Ints(cs.ticks)
	return cs
}

func (cs classStats) rate(n int) float64 { return float64(n) / float64(simRuns) }

func (cs classStats) percentile(p float64) time.Duration {
	i := int(math.Ceil(p*float64(len(cs.ticks)))) - 1
	return time.Duration(cs.ticks[max(0, i)]) * simTick
}

func (cs classStats) String() string {
	return fmt.Sprintf("rollback %.1f%%  promote %.1f%%  undecided %.1f%%  decision p50 %v p95 %v",
		100*cs.rate(cs.rollbacks), 100*cs.rate(cs.promotes), 100*cs.rate(cs.undecided), cs.percentile(0.5), cs.percentile(0.95))
}

func TestSimulatedHealthyCanariesArePromoted(t *testing.T) {
	cfg := testConfig()
	for _, tc := range []struct {
		name  string
		model trafficModel
	}{
		{"clean", steady(0.001, 0.001)},
		{"third of the limit", steady(0.003, 0.003)},
		{"half of the limit", steady(0.005, 0.005)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := runClass(cfg, tc.model)
			t.Logf("healthy/%s: %s", tc.name, cs)
			if cs.rate(cs.rollbacks) > simFPRCeiling {
				t.Fatalf("false rollback rate %.3f exceeds %.2f", cs.rate(cs.rollbacks), simFPRCeiling)
			}
			if cs.undecided > 0 {
				t.Fatalf("%d healthy rollouts never decided", cs.undecided)
			}
		})
	}
}

func TestSimulatedDegradingCanariesAreRolledBack(t *testing.T) {
	cfg := testConfig()
	for _, tc := range []struct {
		name  string
		model trafficModel
	}{
		{"flagrant step to 30%", stepRegression(0.003, 0.30)},
		{"step to the H1 rate 2%", stepRegression(0.003, 0.02)},
		{"ramp 0.5% to 3% over 8 windows", rampRegression(0.005, 0.03, 8)},
		{"latency tail 5%", latencyRegression(0.05)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := runClass(cfg, tc.model)
			onset := time.Duration(simOnsetTick) * simTick
			t.Logf("degrading/%s: %s  detection after onset p50 %v p95 %v", tc.name, cs, cs.percentile(0.5)-onset, cs.percentile(0.95)-onset)
			if cs.rate(cs.rollbacks) < 1-simMissCeil {
				t.Fatalf("rollback rate %.3f is under the %.2f power floor", cs.rate(cs.rollbacks), 1-simMissCeil)
			}
		})
	}
}

// The exit criterion's second half: noise that stays under the limit must not trigger.
func TestSimulatedNoisyHealthyCanariesStayUnderTheFalseRollbackCeiling(t *testing.T) {
	cfg := testConfig()
	for _, tc := range []struct {
		name  string
		model trafficModel
	}{
		{"steady at 70% of the limit", steady(0.007, 0.007)},
		{"steady at 90% of the limit", steady(0.009, 0.009)},
		{"bursty, long-run 80% of the limit", bursty(0.008)},
		{"one window at 10x the limit", spike(0.005, 0.10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := runClass(cfg, tc.model)
			t.Logf("noisy-healthy/%s: %s", tc.name, cs)
			if cs.rate(cs.rollbacks) > simFPRCeiling {
				t.Fatalf("false rollback rate %.3f exceeds the %.2f ceiling", cs.rate(cs.rollbacks), simFPRCeiling)
			}
		})
	}
}
