//go:build trace

// Replay of a public workload through the placement model: the first 93 minutes of the
// Google 2011 cluster trace (clusterdata-2011-2, task_events part 0 of 500, 12,477 machines)
// placed by the pure scoring function the plugin calls and by a model of the default
// scheduler's resource scoring, billed by the node-occupancy rule of METRICS.md. The trace
// carries no prices, so spot and on-demand are assigned to its machines synthetically and
// declared as such; the trace's own machine for every task is accounted alongside, as the
// placement Borg actually made. Nothing here reaches a cluster.
package trace

import (
	"compress/gzip"
	"container/heap"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trnahnh/kiln/scheduler-plugin/internal/scoring"
)

const (
	machineEventsFile = "machine_events/part-00000-of-00001.csv.gz"
	taskEventsFile    = "task_events/part-00000-of-00500.csv.gz"

	// Task events: SUBMIT, SCHEDULE, EVICT, FAIL, FINISH, KILL, LOST.
	evSchedule = 1
	evEvict    = 2
	evLost     = 6

	// Trace resource units are normalised to the largest machine; scaled to integers for
	// the scoring model's milli-CPU and bytes.
	cpuScale = 1_000_000
	memScale = 1_000_000_000_000

	// Synthetic economics: a machine with the trace's full CPU capacity bills this per hour
	// on demand; spot bills the kind cluster's ratio of it (0.03 to 0.10) with the kind
	// cluster's preemption risk.
	onDemandPerCPUHour = 0.20
	spotDiscount       = 0.3
	spotRisk           = 0.2
)

type machine struct {
	id   int64
	node scoring.Node
	// running counts tasks on the machine; occupiedSince is set while it is non-zero.
	running       int
	occupiedSince int64
}

type placement struct {
	key      string
	start    int64
	end      int64
	class    scoring.WorkloadClass
	priority int
	req      scoring.Resources
	// borgMachine is the machine the trace recorded the task on.
	borgMachine int64
}

type cluster struct {
	machines []*machine
	byID     map[int64]*machine
	cursor   int
	rng      *rand.Rand
	spotFrac float64
}

func TestGoogleTraceReplay(t *testing.T) {
	dir := os.Getenv("KILN_TRACE_DIR")
	if dir == "" {
		t.Skip("KILN_TRACE_DIR unset; run hack/google-trace.sh to fetch the trace sample")
	}
	spotFrac := envFloat(t, "KILN_TRACE_SPOT_FRACTION", 0.5)
	seed := uint64(envFloat(t, "KILN_TRACE_SEED", 1))

	machines, windowEnd := readMachines(t, filepath.Join(dir, machineEventsFile))
	snapshot, arrivals, windowEnd := readTasks(t, filepath.Join(dir, taskEventsFile), windowEnd)
	t.Logf("trace: %d machines, %d tasks running at the start, %d placements in %s", len(machines), len(snapshot), len(arrivals), time.Duration(windowEnd)*time.Microsecond)
	t.Logf("economics (synthetic): spot fraction %.2f, on-demand %.2f $/h per unit CPU, spot discount %.1f, seed %d", spotFrac, onDemandPerCPUHour, spotDiscount, seed)
	t.Log(classMix(arrivals))

	var rows []string
	for _, warm := range []bool{false, true} {
		scenario := "empty cluster"
		if warm {
			scenario = "cluster carrying the trace's running tasks"
		}
		var baseline float64
		for _, strat := range []strategy{defaultStrategy{}, kilnStrategy{}, borgStrategy{}} {
			c := newCluster(machines, spotFrac, seed)
			var base []placement
			if warm {
				base = snapshot
			}
			r := replay(c, base, arrivals, windowEnd, strat)
			if _, isDefault := strat.(defaultStrategy); isDefault {
				baseline = r.dollars
			}
			rate := fmt.Sprintf("%.0f", r.perSecond)
			if _, isBorg := strat.(borgStrategy); isBorg {
				rate = "n/a"
			}
			rows = append(rows, fmt.Sprintf("| %s | %s | %.2f | %+.1f%% | %.1f | %.1f | %d | %d | %d | %.1f%% | %s |",
				scenario, strat.name(), r.dollars, 100*(r.dollars-baseline)/baseline, r.hoursOnDemand, r.hoursSpot, r.machinesTouched, r.violations, r.unplaced, 100*r.meanUtil, rate))
			t.Logf("%s / %s: %+v", scenario, strat.name(), r)
		}
	}
	table := "| Scenario | Placement | Billed $ (node-occupancy) | vs default | On-demand node-h | Spot node-h | Machines touched | Latency-class on spot | Unplaced | Mean request util of occupied | Placements/s |\n|---|---|---|---|---|---|---|---|---|---|---|\n" + strings.Join(rows, "\n") + "\n"
	t.Log("\n" + table)
	if path := os.Getenv("KILN_TRACE_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(table), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func envFloat(t *testing.T, name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

func openCSV(t *testing.T, path string) (*csv.Reader, func()) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	r := csv.NewReader(gz)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	return r, func() { _ = gz.Close(); _ = f.Close() }
}

// readMachines keeps the machines present when the trace starts: ADD events at time 0 with
// both capacities recorded.
func readMachines(t *testing.T, path string) ([]machine, int64) {
	r, done := openCSV(t, path)
	defer done()
	var out []machine
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if rec[0] != "0" || rec[2] != "0" || rec[4] == "" || rec[5] == "" {
			continue
		}
		id, _ := strconv.ParseInt(rec[1], 10, 64)
		cpu, _ := strconv.ParseFloat(rec[4], 64)
		mem, _ := strconv.ParseFloat(rec[5], 64)
		out = append(out, machine{id: id, node: scoring.Node{
			Name:        rec[1],
			Allocatable: scoring.Resources{MilliCPU: int64(cpu * cpuScale), MemoryBytes: int64(mem * memScale)},
		}})
	}
	return out, 0
}

type taskEvent struct {
	time    int64
	kind    int
	machine int64
	prio    int
	cpu     float64
	mem     float64
	hasReq  bool
}

// readTasks turns the file's events into placements: every SCHEDULE opens one, the next
// EVICT/FAIL/FINISH/KILL/LOST of the same task closes it, and one still open at the end of
// the file runs to the window's end. Those scheduled at time 0 were running when the trace
// began and form the snapshot; the rest are the arrivals to place.
func readTasks(t *testing.T, path string, windowEnd int64) (snapshot, arrivals []placement, end int64) {
	r, done := openCSV(t, path)
	defer done()
	events := map[string][]taskEvent{}
	var keys []string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ts, _ := strconv.ParseInt(rec[0], 10, 64)
		kind, _ := strconv.Atoi(rec[5])
		key := rec[2] + "/" + rec[3]
		ev := taskEvent{time: ts, kind: kind}
		ev.machine, _ = strconv.ParseInt(rec[4], 10, 64)
		ev.prio, _ = strconv.Atoi(rec[8])
		if rec[9] != "" && rec[10] != "" {
			ev.cpu, _ = strconv.ParseFloat(rec[9], 64)
			ev.mem, _ = strconv.ParseFloat(rec[10], 64)
			ev.hasReq = true
		}
		if _, seen := events[key]; !seen {
			keys = append(keys, key)
		}
		events[key] = append(events[key], ev)
		windowEnd = max(windowEnd, ts)
	}
	for _, key := range keys {
		evs := events[key]
		slices.SortStableFunc(evs, func(a, b taskEvent) int { return int(a.time - b.time) })
		var open *placement
		var lastReq taskEvent
		for _, ev := range evs {
			if ev.hasReq {
				lastReq = ev
			}
			switch {
			case ev.kind == evSchedule:
				if open != nil || !lastReq.hasReq {
					continue
				}
				open = &placement{
					key: key, start: ev.time, end: windowEnd, priority: ev.prio, class: classOf(ev.prio), borgMachine: ev.machine,
					req: scoring.Resources{MilliCPU: int64(lastReq.cpu * cpuScale), MemoryBytes: int64(lastReq.mem * memScale)},
				}
			case ev.kind >= evEvict && ev.kind <= evLost:
				if open == nil {
					continue
				}
				open.end = ev.time
				if open.start == 0 {
					snapshot = append(snapshot, *open)
				} else {
					arrivals = append(arrivals, *open)
				}
				open = nil
			}
		}
		if open != nil {
			if open.start == 0 {
				snapshot = append(snapshot, *open)
			} else {
				arrivals = append(arrivals, *open)
			}
		}
	}
	slices.SortStableFunc(arrivals, func(a, b placement) int { return int(a.start - b.start) })
	return snapshot, arrivals, windowEnd
}

// classOf maps the trace's priority bands, as its format document names them, onto the
// workload classes: production (9 and above) is latency-sensitive, free (0 and 1) is batch,
// the middle band is standard.
func classOf(prio int) scoring.WorkloadClass {
	switch {
	case prio >= 9:
		return scoring.ClassLatencySensitive
	case prio <= 1:
		return scoring.ClassBatch
	default:
		return scoring.ClassStandard
	}
}

func classMix(ps []placement) string {
	n := map[scoring.WorkloadClass]int{}
	for _, p := range ps {
		n[p.class]++
	}
	return fmt.Sprintf("arrivals by class: latency-sensitive %d, standard %d, batch %d", n[scoring.ClassLatencySensitive], n[scoring.ClassStandard], n[scoring.ClassBatch])
}

func newCluster(machines []machine, spotFrac float64, seed uint64) *cluster {
	c := &cluster{byID: map[int64]*machine{}, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), spotFrac: spotFrac}
	for i := range machines {
		m := machines[i]
		cpuUnits := float64(m.node.Allocatable.MilliCPU) / cpuScale
		m.node.Economics = scoring.Economics{HourlyCost: onDemandPerCPUHour * cpuUnits}
		if c.rng.Float64() < spotFrac {
			m.node.Economics = scoring.Economics{HourlyCost: onDemandPerCPUHour * cpuUnits * spotDiscount, Spot: true, PreemptionRisk: spotRisk}
		}
		c.machines = append(c.machines, &m)
		c.byID[m.id] = &m
	}
	return c
}

// candidates mirrors the scheduler's percentageOfNodesToScore: at this size it scans from
// a rotating cursor until 5% of the machines are found feasible, so neither strategy sees
// the whole cluster per pod.
func (c *cluster) candidates(fits func(*machine) bool) []*machine {
	want := max(100, len(c.machines)*5/100)
	var out []*machine
	for scanned := 0; scanned < len(c.machines) && len(out) < want; scanned++ {
		m := c.machines[c.cursor]
		c.cursor = (c.cursor + 1) % len(c.machines)
		if fits(m) {
			out = append(out, m)
		}
	}
	return out
}

func fits(p placement, m *machine) bool {
	return m.node.Requested.MilliCPU+p.req.MilliCPU <= m.node.Allocatable.MilliCPU &&
		m.node.Requested.MemoryBytes+p.req.MemoryBytes <= m.node.Allocatable.MemoryBytes
}

type strategy interface {
	name() string
	// place returns the chosen machine or nil when none fits among the candidates.
	place(c *cluster, p placement) *machine
}

// defaultStrategy is the default profile's resource scoring: NodeResourcesFit's
// LeastAllocated plus NodeResourcesBalancedAllocation, equal weight, random among ties,
// with no economics and no class filter.
type defaultStrategy struct{}

func (defaultStrategy) name() string { return "default scheduler (least-allocated + balanced)" }

func (defaultStrategy) place(c *cluster, p placement) *machine {
	cands := c.candidates(func(m *machine) bool { return fits(p, m) })
	return pickBest(c.rng, cands, func(m *machine) float64 {
		cpuAfter := float64(m.node.Requested.MilliCPU+p.req.MilliCPU) / float64(m.node.Allocatable.MilliCPU)
		memAfter := float64(m.node.Requested.MemoryBytes+p.req.MemoryBytes) / float64(m.node.Allocatable.MemoryBytes)
		least := ((1 - cpuAfter) + (1 - memAfter)) / 2
		balanced := 1 - math.Abs(cpuAfter-memAfter)
		return math.Round(least*100) + math.Round(balanced*100)
	})
}

// kilnStrategy is the plugin's path: the class filter before scoring, the cost range taken
// over the candidate set as PreScore does, then scoring.Score with the default weights.
type kilnStrategy struct{}

func (kilnStrategy) name() string { return "kiln-scheduler (cost-aware scoring)" }

func (kilnStrategy) place(c *cluster, p placement) *machine {
	pod := scoring.Pod{Class: p.class, Requests: p.req}
	cands := c.candidates(func(m *machine) bool { return fits(p, m) && scoring.Feasible(pod, m.node) })
	nodes := make([]scoring.Node, len(cands))
	for i, m := range cands {
		nodes[i] = m.node
	}
	costs := scoring.CostRangeOf(nodes)
	w := scoring.DefaultWeights()
	return pickBest(c.rng, cands, func(m *machine) float64 {
		return float64(scoring.Score(pod, m.node, costs, w))
	})
}

// borgStrategy accounts the trace's own placement without a fit check: Borg schedules on
// usage with overcommit, so its machine may not have the request's room under this model.
type borgStrategy struct{}

func (borgStrategy) name() string { return "trace's own placement (Borg)" }

func (borgStrategy) place(c *cluster, p placement) *machine { return c.byID[p.borgMachine] }

func pickBest(rng *rand.Rand, cands []*machine, score func(*machine) float64) *machine {
	var best *machine
	bestScore := math.Inf(-1)
	ties := 0
	for _, m := range cands {
		s := score(m)
		switch {
		case s > bestScore:
			best, bestScore, ties = m, s, 1
		case s == bestScore:
			ties++
			if rng.IntN(ties) == 0 {
				best = m
			}
		}
	}
	return best
}

type departure struct {
	at int64
	m  *machine
	p  placement
}

type departures []departure

func (d departures) Len() int           { return len(d) }
func (d departures) Less(i, j int) bool { return d[i].at < d[j].at }
func (d departures) Swap(i, j int)      { d[i], d[j] = d[j], d[i] }
func (d *departures) Push(x any)        { *d = append(*d, x.(departure)) }
func (d *departures) Pop() any          { old := *d; x := old[len(old)-1]; *d = old[:len(old)-1]; return x }

type result struct {
	dollars         float64
	hoursOnDemand   float64
	hoursSpot       float64
	machinesTouched int
	violations      int
	unplaced        int
	placed          int
	meanUtil        float64
	perSecond       float64
}

// replay runs the arrivals through the strategy in time order, billing every machine for
// the seconds it hosts at least one task. Tasks with no room are retried every minute of
// trace time and counted unplaced if the window ends first.
func replay(c *cluster, snapshot, arrivals []placement, windowEnd int64, strat strategy) result {
	var res result
	var deps departures
	touched := map[int64]bool{}
	var utilSum float64
	var utilN int

	admit := func(now int64, m *machine, p placement) {
		if m.running == 0 {
			m.occupiedSince = now
		}
		m.running++
		m.node.Requested.MilliCPU += p.req.MilliCPU
		m.node.Requested.MemoryBytes += p.req.MemoryBytes
		heap.Push(&deps, departure{at: p.end, m: m, p: p})
	}
	release := func(now int64, m *machine, p placement) {
		m.running--
		m.node.Requested.MilliCPU -= p.req.MilliCPU
		m.node.Requested.MemoryBytes -= p.req.MemoryBytes
		if m.running == 0 {
			bill(&res, m, now-m.occupiedSince)
		}
	}
	drain := func(now int64) {
		for len(deps) > 0 && deps[0].at <= now {
			d := heap.Pop(&deps).(departure)
			release(d.at, d.m, d.p)
		}
	}

	for _, p := range snapshot {
		if m := c.byID[p.borgMachine]; m != nil {
			admit(0, m, p)
		}
	}

	var pending []placement
	nextRetry := int64(60 * 1e6)
	wall := time.Now()
	place := func(now int64, p placement) bool {
		m := strat.place(c, p)
		if m == nil {
			return false
		}
		if p.class == scoring.ClassLatencySensitive && m.node.Economics.Spot {
			res.violations++
		}
		touched[m.id] = true
		p.end = max(p.end, now+(p.end-p.start))
		if p.end > windowEnd {
			p.end = windowEnd
		}
		admit(now, m, p)
		res.placed++
		utilSum += float64(m.node.Requested.MilliCPU) / float64(m.node.Allocatable.MilliCPU)
		utilN++
		return true
	}
	for _, p := range arrivals {
		drain(p.start)
		if p.start >= nextRetry && len(pending) > 0 {
			kept := pending[:0]
			for _, q := range pending {
				if !place(p.start, q) {
					kept = append(kept, q)
				}
			}
			pending = kept
			nextRetry = p.start + 60*1e6
		}
		if !place(p.start, p) {
			pending = append(pending, p)
		}
	}
	res.perSecond = float64(res.placed) / time.Since(wall).Seconds()
	res.unplaced = len(pending)
	drain(windowEnd)
	for _, m := range c.machines {
		if m.running > 0 {
			bill(&res, m, windowEnd-m.occupiedSince)
		}
	}
	res.machinesTouched = len(touched)
	if utilN > 0 {
		res.meanUtil = utilSum / float64(utilN)
	}
	return res
}

func bill(res *result, m *machine, micros int64) {
	hours := float64(micros) / 1e6 / 3600
	res.dollars += hours * m.node.Economics.HourlyCost
	if m.node.Economics.Spot {
		res.hoursSpot += hours
	} else {
		res.hoursOnDemand += hours
	}
}
