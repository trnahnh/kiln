package analysis

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// Held-out detector evaluation. Round one tuned the threshold and burn-rate rules on the
// very classes they were scored on. Here the class pool is widened with traffic shapes no
// rule was tuned on (diurnal load, retry storms, heavy-tailed latency, low traffic), split
// into two stratified halves, and each alternative is tuned on one half and scored on the
// other, both ways round. The sequential test runs untuned at its defaults and is also
// swept over alpha so its own trade-off curve can be drawn next to the alternatives'.

const (
	holdoutFoldSeed  = 7
	holdoutBootstrap = 200
	diurnalPeriod    = 96 // ticks: 24 minutes, a full cycle inside a long rollout
)

// diurnal offers a sinusoidal load between 10% and 100% of the suite's rate, starting at
// the given phase of the cycle, with an error rate that rises with load as a saturating
// backend's does.
func diurnal(phase int, pErrPerLoad, pErrAfter float64) (trafficModel, rateModel) {
	load := func(tick int) float64 {
		return 0.55 + 0.45*math.Sin(2*math.Pi*float64(tick+phase)/diurnalPeriod)
	}
	model := func(_ *rand.Rand, tick int) (float64, float64) {
		p := pErrPerLoad
		if pErrAfter > 0 && tick >= simOnsetTick {
			p = pErrAfter
		}
		return p * (0.5 + load(tick)), 0.002
	}
	return model, func(tick int) float64 { return simTotalRPS * load(tick) }
}

// retryStorm clusters failures: a two-state chain switches a burst on with pOn per window
// and off with pOff; inside a burst the error rate is burst and callers' retries amplify
// the request volume. A regression raises pOn and burst from the onset on.
func retryStorm(base, burst, pOn, pOff, amplification float64, storm bool) (trafficModel, rateModel) {
	on := false
	model := func(rng *rand.Rand, tick int) (float64, float64) {
		if tick == 1 {
			on = false
		}
		pOn, pOff, burst := pOn, pOff, burst
		if storm && tick >= simOnsetTick {
			pOn, pOff, burst = 0.5, 0.3, 0.08
		}
		if on {
			on = rng.Float64() >= pOff
		} else {
			on = rng.Float64() < pOn
		}
		if on {
			return burst, 0.002
		}
		return base, 0.002
	}
	rps := func(int) float64 {
		if on {
			return simTotalRPS * amplification
		}
		return simTotalRPS
	}
	return model, rps
}

// heavyTailLatency draws each window's slow fraction from a log-normal with sigma one
// around the mean, so most windows are quiet and a few carry several times the mean.
func heavyTailLatency(meanBefore, meanAfter float64) trafficModel {
	const sigma = 1.0
	return func(rng *rand.Rand, tick int) (float64, float64) {
		mean := meanBefore
		if meanAfter > 0 && tick >= simOnsetTick {
			mean = meanAfter
		}
		mu := math.Log(mean) - sigma*sigma/2
		return 0.002, math.Min(0.5, math.Exp(mu+sigma*rng.NormFloat64()))
	}
}

func lowTraffic(rps float64) rateModel { return func(int) float64 { return rps } }

type holdoutClass struct {
	cmpClass
	source string
}

func holdoutClasses() []holdoutClass {
	var out []holdoutClass
	for _, cl := range comparisonClasses() {
		out = append(out, holdoutClass{cmpClass: cl, source: "round 1"})
	}
	add := func(name string, healthy bool, model trafficModel, rps rateModel) {
		cl := cmpClass{name: name, model: model, healthy: healthy, rps: rps}
		if !healthy {
			cl.onset = simOnsetTick
		}
		out = append(out, holdoutClass{cmpClass: cl, source: "round 2"})
	}
	m, r := diurnal(diurnalPeriod*3/4, 0.004, 0)
	add("diurnal, rollout starts in the trough, errors track load", true, m, r)
	m, r = diurnal(diurnalPeriod/4, 0.004, 0)
	add("diurnal, rollout starts at the peak, errors track load", true, m, r)
	m, r = retryStorm(0.003, 0.05, 0.03, 0.5, 1.5, false)
	add("retry storms, rare bursts at 5x the limit with 1.5x retry volume", true, m, r)
	add("heavy-tailed latency, mean 0.5%", true, heavyTailLatency(0.005, 0), nil)
	add("low traffic, 20 rps at 0.5%", true, steady(0.005, 0.002), lowTraffic(20))
	add("very low traffic, 5 rps at 0.5%", true, steady(0.005, 0.002), lowTraffic(5))

	m, r = diurnal(diurnalPeriod*3/4, 0.004, 0.02)
	add("diurnal, step to 2% x load from the trough", false, m, r)
	m, r = diurnal(diurnalPeriod/4, 0.004, 0.02)
	add("diurnal, step to 2% x load from the peak", false, m, r)
	m, r = retryStorm(0.003, 0.05, 0.03, 0.5, 1.5, true)
	add("retry storm from onset, bursts at 8% half the time", false, m, r)
	add("heavy-tailed latency, mean 0.5% to 2%", false, heavyTailLatency(0.005, 0.02), nil)
	add("low traffic, 20 rps, step to 3%", false, stepRegression(0.005, 0.03), lowTraffic(20))
	add("very low traffic, 5 rps, step to 5%", false, stepRegression(0.005, 0.05), lowTraffic(5))
	return out
}

// folds splits the classes into two stratified halves with a fixed shuffle, so each
// half has the same share of healthy and regression classes.
func folds(classes []holdoutClass) (a, b []int) {
	rng := rand.New(rand.NewPCG(holdoutFoldSeed, holdoutFoldSeed))
	for _, healthy := range []bool{true, false} {
		var idx []int
		for i, cl := range classes {
			if cl.healthy == healthy {
				idx = append(idx, i)
			}
		}
		rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
		for k, i := range idx {
			if k%2 == 0 {
				a = append(a, i)
			} else {
				b = append(b, i)
			}
		}
	}
	slices.Sort(a)
	slices.Sort(b)
	return a, b
}

// summary scores one tuning point on a subset of the classes.
type summary struct {
	worstFPR, worstMiss float64
	fprLow, fprHigh     float64
	meanP50             time.Duration
	p50Low, p50High     time.Duration
	regressions         int
}

func summarize(classes []holdoutClass, p pointResult, idx []int, rng *rand.Rand) summary {
	var s summary
	var medians [][]time.Duration
	for _, i := range idx {
		o := p.outcomes[i]
		if classes[i].healthy {
			if o.rollbackRate >= s.worstFPR {
				s.worstFPR = o.rollbackRate
				s.fprLow, s.fprHigh = wilson(o.rollbackRate, simRuns)
			}
			continue
		}
		s.worstMiss = math.Max(s.worstMiss, 1-o.rollbackRate)
		s.regressions++
		if len(o.delays) > 0 {
			medians = append(medians, o.delays)
		}
	}
	if len(medians) == 0 {
		return s
	}
	s.meanP50 = meanOfMedians(medians, nil)
	boots := make([]time.Duration, holdoutBootstrap)
	for b := range boots {
		boots[b] = meanOfMedians(medians, rng)
	}
	slices.Sort(boots)
	s.p50Low, s.p50High = boots[len(boots)*25/1000], boots[len(boots)*975/1000]
	return s
}

// meanOfMedians averages each class's median delay; with rng it resamples every class's
// delays with replacement first, one bootstrap replicate.
func meanOfMedians(classes [][]time.Duration, rng *rand.Rand) time.Duration {
	var sum time.Duration
	for _, delays := range classes {
		sample := delays
		if rng != nil {
			sample = make([]time.Duration, len(delays))
			for i := range sample {
				sample[i] = delays[rng.IntN(len(delays))]
			}
		}
		sorted := slices.Clone(sample)
		slices.Sort(sorted)
		sum += sorted[(len(sorted)-1)/2]
	}
	return sum / time.Duration(len(classes))
}

// wilson is the 95% score interval for a binomial proportion.
func wilson(p float64, n int) (float64, float64) {
	const z = 1.96
	nf := float64(n)
	centre := (p + z*z/(2*nf)) / (1 + z*z/nf)
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / (1 + z*z/nf)
	return math.Max(0, centre-half), math.Min(1, centre+half)
}

func sprtFamily() detectorFamily {
	return detectorFamily{name: "sequential test + CUSUM (this controller)"}
}

func sprtAlphas() []float64 { return []float64{0.01, 0.02, 0.05, 0.1, 0.2} }

type evaluated struct {
	family string
	points []pointResult
}

// evaluateAll scores every tuning point of every family on every class once; folds and
// curves are then subsets of these outcomes.
func evaluateAll(cfg Config, classes []holdoutClass) []evaluated {
	plain := make([]cmpClass, len(classes))
	for i, cl := range classes {
		plain[i] = cl.cmpClass
	}
	var out []evaluated
	var sprt evaluated
	sprt.family = sprtFamily().name
	for _, alpha := range sprtAlphas() {
		c := cfg
		c.Alpha = alpha
		label := fmt.Sprintf("alpha %.2f", alpha)
		if alpha == cfg.Alpha {
			label += " (ADR-0014 default)"
		}
		sprt.points = append(sprt.points, runPoint(plain, sprt.family, label, func(cl cmpClass, seed uint64) runResult {
			return simulateWith(c, cl.model, cl.rate(), seed)
		}))
	}
	out = append(out, sprt)
	for _, fam := range []detectorFamily{thresholdFamily(), burnRateFamily()} {
		e := evaluated{family: fam.name}
		for _, pt := range fam.grid(cfg) {
			e.points = append(e.points, runPoint(plain, fam.name, pt.label, func(cl cmpClass, seed uint64) runResult {
				return simulateFixed(cfg, cl.model, cl.rate(), seed, pt.build())
			}))
		}
		out = append(out, e)
	}
	return out
}

// choose picks, on the tuning half, the fastest point under both ceilings, or failing
// that the point whose worst false-rollback rate is nearest the reference.
func choose(classes []holdoutClass, points []pointResult, tuning []int, matchFPR float64, rng *rand.Rand) (int, bool) {
	best, matched := -1, -1
	var bestP50 time.Duration
	var matchedGap float64
	for i, p := range points {
		s := summarize(classes, p, tuning, rng)
		if s.worstFPR <= simFPRCeiling && s.worstMiss <= simMissCeil && (best < 0 || s.meanP50 < bestP50) {
			best, bestP50 = i, s.meanP50
		}
		if gap := math.Abs(s.worstFPR - matchFPR); matched < 0 || gap < matchedGap {
			matched, matchedGap = i, gap
		}
	}
	if best >= 0 {
		return best, true
	}
	return matched, false
}

type foldRow struct {
	family, label string
	underCeilings bool
	point         pointResult
	held          summary
}

// The very-low-traffic pair sits outside every rule's operating envelope at this window
// and step configuration, and a single such class fixes the worst-case columns for every
// detector; the folds are therefore reported with it and without it.
const veryLowTraffic = "very low traffic"

func TestDetectorHoldout(t *testing.T) {
	reportPath := os.Getenv("KILN_HOLDOUT_REPORT")
	if reportPath == "" {
		t.Skip("set KILN_HOLDOUT_REPORT to run the held-out evaluation; it sweeps 52 tuning points over 21 classes and takes minutes")
	}
	cfg := testConfig()
	classes := holdoutClasses()
	a, b := folds(classes)
	rng := rand.New(rand.NewPCG(holdoutFoldSeed, 11))
	all := evaluateAll(cfg, classes)

	var report strings.Builder
	fmt.Fprintf(&report, "Classes: %d, of which %d from round 1 and %d new. Fold A holds %s; fold B holds %s.\n\n",
		len(classes), countSource(classes, "round 1"), countSource(classes, "round 2"), nameList(classes, a), nameList(classes, b))

	defaultIdx := 0
	for i, p := range all[0].points {
		if strings.Contains(p.label, "default") {
			defaultIdx = i
		}
	}
	for _, view := range []struct {
		title string
		keep  func(holdoutClass) bool
	}{
		{"all classes", func(holdoutClass) bool { return true }},
		{"without the very-low-traffic pair", func(cl holdoutClass) bool { return !strings.HasPrefix(cl.name, veryLowTraffic) }},
	} {
		fmt.Fprintf(&report, "# %s\n\n", view.title)
		var foldRows [][]foldRow
		var helds [][]int
		for f, fold := range [][2][]int{{a, b}, {b, a}} {
			tuning, held := filterIdx(classes, fold[0], view.keep), filterIdx(classes, fold[1], view.keep)
			sprtDefault := all[0].points[defaultIdx]
			sprtTuning := summarize(classes, sprtDefault, tuning, rng)
			rows := []foldRow{{family: all[0].family, label: sprtDefault.label, underCeilings: sprtTuning.worstFPR <= simFPRCeiling && sprtTuning.worstMiss <= simMissCeil, point: sprtDefault, held: summarize(classes, sprtDefault, held, rng)}}
			for _, e := range all[1:] {
				i, ok := choose(classes, e.points, tuning, sprtTuning.worstFPR, rng)
				rows = append(rows, foldRow{family: e.family, label: e.points[i].label, underCeilings: ok, point: e.points[i], held: summarize(classes, e.points[i], held, rng)})
			}
			foldRows = append(foldRows, rows)
			helds = append(helds, held)
			fmt.Fprintf(&report, "## Fold %c: tuned on %s, scored on %s\n\n", 'A'+f, foldName(f, false), foldName(f, true))
			report.WriteString(renderFold(classes, rows, held))
		}
		report.WriteString(renderLosses(classes, foldRows, helds))
	}

	newOnly := indexesOf(classes, "round 2")
	plotted := filterIdx(classes, newOnly, func(cl holdoutClass) bool { return !strings.HasPrefix(cl.name, veryLowTraffic) })
	report.WriteString("\n# Trade-off on the new classes only\n\nEvery tuning point of every family, scored on the ten round-2 classes no rule was tuned on in round 1, without the 5 rps pair (on which the sequential test rolls back 99.3% of healthy rollouts at every alpha; see the folds). Worst false rollback is the highest healthy-class rate with its 95% Wilson interval; detection is the mean of the regression classes' median delays with a 95% bootstrap interval.\n\n")
	report.WriteString(renderCurveTable(classes, all, plotted, rng))

	t.Log("\n" + report.String())
	if err := os.WriteFile(reportPath, []byte(report.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("KILN_HOLDOUT_SVG"); path != "" {
		if err := os.WriteFile(path, []byte(renderCurveSVG(classes, all, plotted, rng)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func filterIdx(classes []holdoutClass, idx []int, keep func(holdoutClass) bool) []int {
	var out []int
	for _, i := range idx {
		if keep(classes[i]) {
			out = append(out, i)
		}
	}
	return out
}

func countSource(classes []holdoutClass, source string) int { return len(indexesOf(classes, source)) }

func indexesOf(classes []holdoutClass, source string) []int {
	var idx []int
	for i, cl := range classes {
		if cl.source == source {
			idx = append(idx, i)
		}
	}
	return idx
}

func nameList(classes []holdoutClass, idx []int) string {
	names := make([]string, len(idx))
	for i, k := range idx {
		names[i] = classes[k].name
	}
	return strings.Join(names, "; ")
}

func foldName(f int, held bool) string {
	if (f == 0) != held {
		return "fold A"
	}
	return "fold B"
}

func renderFold(classes []holdoutClass, rows []foldRow, held []int) string {
	var b strings.Builder
	b.WriteString("| Detector | Tuning chosen on the other half | Under both ceilings there? | Worst false rollback (held out) | Worst miss (held out) | Mean of median delays (held out) |")
	for _, i := range held {
		if !classes[i].healthy {
			fmt.Fprintf(&b, " %s: p50, missed |", classes[i].name)
		}
	}
	b.WriteString("\n|---|---|---|---|---|---|")
	for _, i := range held {
		if !classes[i].healthy {
			b.WriteString("---|")
		}
	}
	b.WriteString("\n")
	for _, r := range rows {
		ok := "yes"
		if !r.underCeilings {
			ok = "no, nearest false-rollback rate"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.1f%% (%.1f to %.1f) | %.1f%% | %v (%v to %v) |", r.family, r.label, ok, 100*r.held.worstFPR, 100*r.held.fprLow, 100*r.held.fprHigh, 100*r.held.worstMiss, r.held.meanP50, r.held.p50Low, r.held.p50High)
		for _, i := range held {
			if classes[i].healthy {
				continue
			}
			o := r.point.outcomes[i]
			if len(o.delays) == 0 {
				fmt.Fprintf(&b, " never, %.1f%% |", 100*(1-o.rollbackRate))
				continue
			}
			fmt.Fprintf(&b, " %v, %.1f%% |", o.percentile(0.5), 100*(1-o.rollbackRate))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nFalse rollback per held-out healthy class:\n\n| Detector |")
	for _, i := range held {
		if classes[i].healthy {
			fmt.Fprintf(&b, " %s |", classes[i].name)
		}
	}
	b.WriteString("\n|---|")
	for _, i := range held {
		if classes[i].healthy {
			b.WriteString("---|")
		}
	}
	b.WriteString("\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s, %s |", r.family, r.label)
		for _, i := range held {
			if classes[i].healthy {
				fmt.Fprintf(&b, " %.1f%% |", 100*r.point.outcomes[i].rollbackRate)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderLosses names every held-out class on which an alternative, at its chosen tuning,
// beat the sequential test: a faster median on a regression it also caught at least as
// often, or a lower false-rollback rate on a healthy class. An alternative whose own
// held-out false-rollback rate is above both the ceiling and the sequential test's does
// not count; it would be trading rollouts for speed.
func renderLosses(classes []holdoutClass, foldRows [][]foldRow, helds [][]int) string {
	var b strings.Builder
	b.WriteString("## Where the sequential test loses\n\n")
	n := 0
	for f, rows := range foldRows {
		sprt := rows[0]
		for _, i := range helds[f] {
			cl := classes[i]
			s := sprt.point.outcomes[i]
			for _, r := range rows[1:] {
				if r.held.worstFPR > math.Max(simFPRCeiling, sprt.held.worstFPR) {
					continue
				}
				o := r.point.outcomes[i]
				switch {
				case cl.healthy && o.rollbackRate < s.rollbackRate && s.rollbackRate > 0.01:
					fmt.Fprintf(&b, "- fold %c, %s: %.1f%% false rollback against %.1f%% for %s (%s)\n", 'A'+f, cl.name, 100*s.rollbackRate, 100*o.rollbackRate, r.family, r.label)
					n++
				case !cl.healthy && len(o.delays) > 0 && len(s.delays) > 0 && o.percentile(0.5) < s.percentile(0.5) && o.rollbackRate >= s.rollbackRate:
					fmt.Fprintf(&b, "- fold %c, %s: p50 %v against %v for %s (%s), both catching %.1f%% or more\n", 'A'+f, cl.name, s.percentile(0.5), o.percentile(0.5), r.family, r.label, 100*s.rollbackRate)
					n++
				case !cl.healthy && len(s.delays) == 0 && len(o.delays) > 0:
					fmt.Fprintf(&b, "- fold %c, %s: never caught, against p50 %v for %s (%s)\n", 'A'+f, cl.name, o.percentile(0.5), r.family, r.label)
					n++
				}
			}
			if s.rollbackRate >= 0.5 && cl.healthy {
				fmt.Fprintf(&b, "- fold %c, %s: the sequential test itself rolls back %.1f%% of healthy rollouts (%s)\n", 'A'+f, cl.name, 100*s.rollbackRate, reasonMix(classes[i], sprt.point))
				n++
			}
		}
	}
	if n == 0 {
		b.WriteString("- none on these folds\n")
	}
	b.WriteString("\n")
	return b.String()
}

// reasonMix reruns the sequential test on a class to name why it rolled back, since the
// outcomes keep only the counts.
func reasonMix(cl holdoutClass, p pointResult) string {
	cfg := testConfig()
	if strings.HasPrefix(p.label, "alpha ") {
		fmt.Sscanf(p.label, "alpha %f", &cfg.Alpha)
	}
	reasons := map[Reason]int{}
	for i := range simRuns {
		r := simulateWith(cfg, cl.model, cl.rate(), uint64(i+1))
		if r.final == Rollback {
			reasons[r.reason]++
		}
	}
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, reasons[Reason(k)])
	}
	return strings.Join(parts, ", ")
}

type curvePoint struct {
	family, label string
	s             summary
}

func curvePoints(classes []holdoutClass, all []evaluated, idx []int, rng *rand.Rand) []curvePoint {
	var pts []curvePoint
	for _, e := range all {
		for _, p := range e.points {
			pts = append(pts, curvePoint{family: e.family, label: p.label, s: summarize(classes, p, idx, rng)})
		}
	}
	return pts
}

func renderCurveTable(classes []holdoutClass, all []evaluated, idx []int, rng *rand.Rand) string {
	var b strings.Builder
	b.WriteString("| Detector | Tuning | Worst false rollback | 95% interval | Worst miss | Mean of median delays | 95% interval |\n|---|---|---|---|---|---|---|\n")
	for _, p := range curvePoints(classes, all, idx, rng) {
		fmt.Fprintf(&b, "| %s | %s | %.1f%% | %.1f to %.1f | %.1f%% | %v | %v to %v |\n", p.family, p.label, 100*p.s.worstFPR, 100*p.s.fprLow, 100*p.s.fprHigh, 100*p.s.worstMiss, p.s.meanP50, p.s.p50Low, p.s.p50High)
	}
	return b.String()
}

// renderCurveSVG draws detection delay against false-rollback rate for every tuning point,
// with the 95% intervals as error bars; a hollow marker misses more than the ceiling.
func renderCurveSVG(classes []holdoutClass, all []evaluated, idx []int, rng *rand.Rand) string {
	const (
		w, h               = 960, 560
		left, right        = 80, 300
		top, bottom        = 40, 70
		xMax               = 0.30
		yMaxMinutes        = 6.0
		plotW, plotH       = w - left - right, h - top - bottom
		colourSPRT         = "#1b6ac9"
		colourThreshold    = "#d2691e"
		colourBurn         = "#2e8b57"
		markerRadius       = 5
		defaultMarkerExtra = 4
	)
	colour := map[string]string{}
	for i, e := range all {
		colour[e.family] = []string{colourSPRT, colourThreshold, colourBurn}[i%3]
	}
	x := func(fpr float64) float64 { return left + plotW*math.Min(fpr, xMax)/xMax }
	y := func(d time.Duration) float64 { return top + plotH*(1-math.Min(d.Minutes(), yMaxMinutes)/yMaxMinutes) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="sans-serif" font-size="12">`, w, h, w, h)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="white"/>`, w, h)
	for i := 0; i <= 6; i++ {
		fpr := xMax * float64(i) / 6
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="#ddd"/>`, x(fpr), top, x(fpr), h-bottom)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" text-anchor="middle">%.0f%%</text>`, x(fpr), h-bottom+18, 100*fpr)
	}
	for i := 0; i <= 6; i++ {
		d := time.Duration(float64(i) * yMaxMinutes / 6 * float64(time.Minute))
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#ddd"/>`, left, y(d), w-right, y(d))
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" text-anchor="end" dominant-baseline="middle">%v</text>`, left-8, y(d), d)
	}
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="#999" stroke-dasharray="4 3"/>`, x(simFPRCeiling), top, x(simFPRCeiling), h-bottom)
	fmt.Fprintf(&b, `<text x="%.1f" y="%d" fill="#666">5%% false-rollback ceiling</text>`, x(simFPRCeiling)+4, top+12)
	fmt.Fprintf(&b, `<text x="%d" y="%d" text-anchor="middle" font-size="13">worst false rollback across the held-out healthy classes (95%% Wilson interval); points beyond %.0f%% are drawn at the edge</text>`, left+plotW/2, h-bottom+42, 100*xMax)
	fmt.Fprintf(&b, `<text transform="translate(18 %d) rotate(-90)" text-anchor="middle" font-size="13">mean of the regression classes' median detection delays (95%% bootstrap interval)</text>`, top+plotH/2)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="14" font-weight="bold">Detection delay against false rollback on %d new traffic classes; the 5 rps pair, where every rule fails as configured, is left out</text>`, left, top-16, len(idx))

	legendY := top + 10
	for _, e := range all {
		fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="%d" fill="%s"/><text x="%d" y="%d" dominant-baseline="middle">%s</text>`, w-right+20, legendY, markerRadius, colour[e.family], w-right+32, legendY, e.family)
		legendY += 20
	}
	fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="%d" fill="white" stroke="#333"/><text x="%d" y="%d" dominant-baseline="middle">hollow: misses more than %.0f%% of a regression class</text>`, w-right+20, legendY, markerRadius, w-right+32, legendY, 100*simMissCeil)
	legendY += 20
	fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="%d" fill="none" stroke="%s" stroke-width="2"/><text x="%d" y="%d" dominant-baseline="middle">ring: the controller's ADR-0014 default</text>`, w-right+20, legendY, markerRadius+defaultMarkerExtra, colourSPRT, w-right+32, legendY)

	for _, p := range curvePoints(classes, all, idx, rng) {
		if p.s.regressions == 0 {
			continue
		}
		cx, cy := x(p.s.worstFPR), y(p.s.meanP50)
		c := colour[p.family]
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-opacity="0.5"/>`, x(p.s.fprLow), cy, x(p.s.fprHigh), cy, c)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-opacity="0.5"/>`, cx, y(p.s.p50Low), cx, y(p.s.p50High), c)
		fill := c
		if p.s.worstMiss > simMissCeil {
			fill = "white"
		}
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%d" fill="%s" stroke="%s" stroke-width="1.5"><title>%s, %s: worst false rollback %.1f%%, worst miss %.1f%%, mean p50 %v</title></circle>`, cx, cy, markerRadius, fill, c, p.family, p.label, 100*p.s.worstFPR, 100*p.s.worstMiss, p.s.meanP50)
		if strings.Contains(p.label, "default") {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%d" fill="none" stroke="%s" stroke-width="2"/>`, cx, cy, markerRadius+defaultMarkerExtra, c)
		}
	}
	b.WriteString("</svg>\n")
	return b.String()
}
