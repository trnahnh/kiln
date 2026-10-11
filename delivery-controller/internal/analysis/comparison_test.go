package analysis

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Detector comparison: the sequential test is run against the two decision rules a canary
// controller usually ships with, a per-window threshold that rolls back after N consecutive
// breaches (the Flagger / Argo Rollouts analysis shape) and a multi-window, multi-burn-rate
// alert on the error budget (the SRE Workbook alerting shape), on the traffic models and
// seeds of the simulation suite. Each alternative is swept over its tuning grid; the table
// reports its operating points under the same false-rollback ceiling the sequential test is
// held to, so time to detect is compared at equal false-rollback cost, not at defaults.

const (
	// The alternatives drive a fixed schedule: every checkpoint is held this many windows
	// and the canary is promoted after the last, so a regression that outlives the
	// schedule is a miss.
	cmpStepTicks = 4
)

type detector interface {
	observe(s Sample) bool
}

type detectorFamily struct {
	name string
	// grid yields every tuning point of the family with a short label.
	grid func(cfg Config) []detectorPoint
}

type detectorPoint struct {
	label string
	build func() detector
}

// thresholdDetector rolls back once either rate has exceeded its limit times `multiplier`
// in `consecutive` windows in a row, the shape of a metric check with a failure threshold.
type thresholdDetector struct {
	cfg         Config
	multiplier  float64
	consecutive int
	failed      int
}

func (d *thresholdDetector) observe(s Sample) bool {
	if s.Requests == 0 {
		return false
	}
	errRate := float64(s.Errors) / float64(s.Requests)
	slowRate := float64(s.Slow) / float64(s.Requests)
	if errRate > d.multiplier*d.cfg.ErrorRateMax || slowRate > d.multiplier*d.cfg.LatencyTailMax {
		d.failed++
	} else {
		d.failed = 0
	}
	return d.failed >= d.consecutive
}

// burnWindow pairs a long window with a short one, both in ticks, and the burn rate both
// must reach: the SRE Workbook's multi-window rule, where burn is the observed error rate
// over the rate the objective permits.
type burnWindow struct {
	long, short int
	burn        float64
}

type burnRateDetector struct {
	cfg     Config
	windows []burnWindow
	history []Sample
}

func (d *burnRateDetector) observe(s Sample) bool {
	d.history = append(d.history, s)
	for _, w := range d.windows {
		if len(d.history) < w.long {
			continue
		}
		if d.burn(w.long, true) >= w.burn && d.burn(w.short, true) >= w.burn {
			return true
		}
		if d.burn(w.long, false) >= w.burn && d.burn(w.short, false) >= w.burn {
			return true
		}
	}
	return false
}

func (d *burnRateDetector) burn(ticks int, errors bool) float64 {
	var n, x int64
	for _, s := range d.history[len(d.history)-ticks:] {
		n += s.Requests
		if errors {
			x += s.Errors
		} else {
			x += s.Slow
		}
	}
	if n == 0 {
		return 0
	}
	limit := d.cfg.ErrorRateMax
	if !errors {
		limit = d.cfg.LatencyTailMax
	}
	return float64(x) / float64(n) / limit
}

// The workbook's windows are 1h/5m, 6h/30m and 3d/6h at burn rates 14.4, 6 and 1, sized to
// a 30-day budget. A canary lives minutes, so the windows are 1, 2 and 4 minutes with the
// same long-to-short ratio where a tick allows, and the three burn rates are scaled
// together by one factor that the sweep varies.
func burnRateWindows(scale float64) []burnWindow {
	return []burnWindow{
		{long: 4, short: 1, burn: 14.4 * scale},
		{long: 8, short: 1, burn: 6 * scale},
		{long: 16, short: 2, burn: 1 * scale},
	}
}

func thresholdFamily() detectorFamily {
	return detectorFamily{name: "threshold, N consecutive breaches", grid: func(cfg Config) []detectorPoint {
		var pts []detectorPoint
		for _, m := range []float64{1, 1.5, 2, 3, 5} {
			for _, n := range []int{1, 2, 3, 4, 5, 6, 8} {
				m, n := m, n
				pts = append(pts, detectorPoint{
					label: fmt.Sprintf("limit x%.1f, %d consecutive", m, n),
					build: func() detector { return &thresholdDetector{cfg: cfg, multiplier: m, consecutive: n} },
				})
			}
		}
		return pts
	}}
}

func burnRateFamily() detectorFamily {
	return detectorFamily{name: "multi-window multi-burn-rate", grid: func(cfg Config) []detectorPoint {
		var pts []detectorPoint
		for _, f := range []float64{0.5, 0.75, 1, 1.25, 1.5, 2, 2.5, 3, 4, 5, 6, 8} {
			f := f
			pts = append(pts, detectorPoint{
				label: fmt.Sprintf("burn x%.2f (%.1f/%.1f/%.1f)", f, 14.4*f, 6*f, 1*f),
				build: func() detector { return &burnRateDetector{cfg: cfg, windows: burnRateWindows(f)} },
			})
		}
		return pts
	}}
}

// simulateFixed drives a fixed-schedule rollout through a detector on the same traffic
// model and seed as simulate.
func simulateFixed(cfg Config, model trafficModel, seed uint64, d detector) runResult {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	horizon := cmpStepTicks * len(cfg.Checkpoints)
	for tick := 1; tick <= horizon; tick++ {
		weight := cfg.Checkpoints[min((tick-1)/cmpStepTicks, len(cfg.Checkpoints)-1)]
		pErr, pSlow := model(rng, tick)
		n := poisson(rng, simTotalRPS*float64(weight)/100*simTick.Seconds())
		s := Sample{Requests: n, Errors: binomial(rng, n, pErr), Slow: binomial(rng, n, pSlow)}
		if d.observe(s) {
			return runResult{final: Rollback, reason: ReasonRegressionDetected, ticks: tick}
		}
	}
	return runResult{final: Promote, reason: ReasonAccepted, ticks: horizon}
}

type cmpClass struct {
	name     string
	model    trafficModel
	healthy  bool
	onset    int
	flagrant bool
}

func comparisonClasses() []cmpClass {
	return []cmpClass{
		{name: "healthy clean", model: steady(0.001, 0.001), healthy: true},
		{name: "healthy half of the limit", model: steady(0.005, 0.005), healthy: true},
		{name: "noisy steady at 90% of the limit", model: steady(0.009, 0.009), healthy: true},
		{name: "noisy bursty, long-run 80% of the limit", model: bursty(0.008), healthy: true},
		{name: "noisy one window at 10x the limit", model: spike(0.005, 0.10), healthy: true},
		{name: "step to 30%", model: stepRegression(0.003, 0.30), onset: simOnsetTick, flagrant: true},
		{name: "step to 2% (the H1 rate)", model: stepRegression(0.003, 0.02), onset: simOnsetTick},
		{name: "ramp 0.5% to 3% over 8 windows", model: rampRegression(0.005, 0.03, 8), onset: simOnsetTick},
		{name: "latency tail 5%", model: latencyRegression(0.05), onset: simOnsetTick},
	}
}

type classOutcome struct {
	rollbackRate float64
	// detection delays after onset, rollbacks only
	delays []time.Duration
}

func (o classOutcome) percentile(p float64) time.Duration {
	if len(o.delays) == 0 {
		return 0
	}
	sort.Slice(o.delays, func(i, j int) bool { return o.delays[i] < o.delays[j] })
	i := int(math.Ceil(p*float64(len(o.delays)))) - 1
	return o.delays[max(0, i)]
}

type pointResult struct {
	family, label string
	outcomes      []classOutcome
	worstFPR      float64
	worstMiss     float64
	meanP50       time.Duration
}

func runPoint(cfg Config, classes []cmpClass, family, label string, run func(model trafficModel, seed uint64) runResult) pointResult {
	res := pointResult{family: family, label: label}
	var p50Sum time.Duration
	var regressions int
	for _, cl := range classes {
		var o classOutcome
		rollbacks := 0
		for i := range simRuns {
			r := run(cl.model, uint64(i+1))
			if r.final != Rollback {
				continue
			}
			rollbacks++
			if !cl.healthy {
				o.delays = append(o.delays, time.Duration(r.ticks-cl.onset)*simTick)
			}
		}
		o.rollbackRate = float64(rollbacks) / float64(simRuns)
		if cl.healthy {
			res.worstFPR = math.Max(res.worstFPR, o.rollbackRate)
		} else {
			res.worstMiss = math.Max(res.worstMiss, 1-o.rollbackRate)
			p50Sum += o.percentile(0.5)
			regressions++
		}
		res.outcomes = append(res.outcomes, o)
	}
	res.meanP50 = p50Sum / time.Duration(regressions)
	return res
}

// operatingPoints picks, per family, the tuning that detects fastest among those under the
// false-rollback ceiling and the miss ceiling, and the tuning whose worst false-rollback
// rate is nearest the sequential test's own, so the reader sees both a fair-ceiling and a
// matched-rate comparison.
func operatingPoints(points []pointResult, matchFPR float64) (fastest, matched *pointResult) {
	for i := range points {
		p := &points[i]
		if p.worstFPR <= simFPRCeiling && p.worstMiss <= simMissCeil {
			if fastest == nil || p.meanP50 < fastest.meanP50 {
				fastest = p
			}
		}
		if matched == nil || math.Abs(p.worstFPR-matchFPR) < math.Abs(matched.worstFPR-matchFPR) {
			matched = p
		}
	}
	return fastest, matched
}

func TestDetectorComparison(t *testing.T) {
	cfg := testConfig()
	classes := comparisonClasses()

	sprt := runPoint(cfg, classes, "sequential test + CUSUM (this controller)", "ADR-0014 defaults", func(model trafficModel, seed uint64) runResult {
		return simulate(cfg, model, seed)
	})

	var rows []pointResult
	rows = append(rows, sprt)
	for _, fam := range []detectorFamily{thresholdFamily(), burnRateFamily()} {
		var points []pointResult
		for _, pt := range fam.grid(cfg) {
			pt := pt
			points = append(points, runPoint(cfg, classes, fam.name, pt.label, func(model trafficModel, seed uint64) runResult {
				return simulateFixed(cfg, model, seed, pt.build())
			}))
		}
		fastest, matched := operatingPoints(points, sprt.worstFPR)
		if fastest == nil {
			t.Logf("%s: no tuning point meets both the %.0f%% false-rollback ceiling and the %.0f%% miss ceiling", fam.name, 100*simFPRCeiling, 100*simMissCeil)
			rows = append(rows, *matched)
			continue
		}
		rows = append(rows, *fastest)
		if matched != fastest {
			rows = append(rows, *matched)
		}
		if os.Getenv("KILN_COMPARISON_SWEEP") != "" {
			for _, p := range points {
				t.Logf("sweep %s | %s | worst FPR %.1f%% | worst miss %.1f%% | mean p50 %v", p.family, p.label, 100*p.worstFPR, 100*p.worstMiss, p.meanP50)
			}
		}
	}

	table := renderComparison(classes, rows)
	t.Log("\n" + table)
	if path := os.Getenv("KILN_COMPARISON_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(table), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
	}
	if sprt.worstFPR > simFPRCeiling {
		t.Fatalf("sequential test worst false-rollback rate %.3f exceeds %.2f", sprt.worstFPR, simFPRCeiling)
	}
}

func renderComparison(classes []cmpClass, rows []pointResult) string {
	var b strings.Builder
	b.WriteString("| Detector | Tuning | Worst false rollback (healthy/noisy) |")
	for _, cl := range classes {
		if !cl.healthy {
			fmt.Fprintf(&b, " %s: detect p50 / p95, missed |", cl.name)
		}
	}
	b.WriteString("\n|---|---|---|")
	for _, cl := range classes {
		if !cl.healthy {
			b.WriteString("---|")
		}
	}
	b.WriteString("\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %.1f%% |", r.family, r.label, 100*r.worstFPR)
		for i, cl := range classes {
			if cl.healthy {
				continue
			}
			o := r.outcomes[i]
			if len(o.delays) == 0 {
				fmt.Fprintf(&b, " never, %.1f%% |", 100*(1-o.rollbackRate))
				continue
			}
			fmt.Fprintf(&b, " %v / %v, %.1f%% |", o.percentile(0.5), o.percentile(0.95), 100*(1-o.rollbackRate))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nPer-class false rollback for each row:\n\n| Detector | Tuning |")
	for _, cl := range classes {
		if cl.healthy {
			fmt.Fprintf(&b, " %s |", cl.name)
		}
	}
	b.WriteString("\n|---|---|")
	for _, cl := range classes {
		if cl.healthy {
			b.WriteString("---|")
		}
	}
	b.WriteString("\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s |", r.family, r.label)
		for i, cl := range classes {
			if cl.healthy {
				fmt.Fprintf(&b, " %.1f%% |", 100*r.outcomes[i].rollbackRate)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
