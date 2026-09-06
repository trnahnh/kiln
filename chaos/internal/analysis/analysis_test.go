package analysis

import (
	"math"
	"testing"
	"time"
)

var cfg = Config{ErrorRateMax: 0.05, MinSampleSize: 20, RecoveryWindows: 4, MetricsTimeout: 30 * time.Second}

// A synthetic scrape: what the counters grew by since the previous scrape. ok=false plays
// an unreachable Prometheus.
type scrape struct {
	requests, errors, slow float64
	ok                     bool
}

func healthy(n int) []scrape {
	out := make([]scrape, n)
	for i := range out {
		out[i] = scrape{requests: 100, errors: 1, slow: 0, ok: true}
	}
	return out
}

type run struct {
	decisions []Decision
	state     State
	ticks     int
}

// feed plays a stream of scrapes 5 s apart through Tick the way the controller does: the
// snapshot only advances once a window has been judged, so small windows merge.
func feed(cfg Config, stage Stage, stream []scrape) run {
	start := time.Unix(1_700_000_000, 0)
	st := NewState(start)
	var cumulative, judged Window
	r := run{}
	for i, s := range stream {
		now := start.Add(time.Duration(i+1) * 5 * time.Second)
		if s.ok {
			cumulative.Requests += s.requests
			cumulative.Errors += s.errors
			cumulative.Slow += s.slow
		}
		w := Window{Requests: cumulative.Requests - judged.Requests, Errors: cumulative.Errors - judged.Errors, Slow: cumulative.Slow - judged.Slow}
		d := Tick(cfg, &st, now, stage, w, s.ok)
		if d.Judged {
			judged = cumulative
		}
		r.decisions = append(r.decisions, d)
		r.ticks++
		if d.Action != Continue {
			break
		}
	}
	r.state = st
	return r
}

func TestHealthyStreamNeverAborts(t *testing.T) {
	r := feed(cfg, StageFault, healthy(40))
	for i, d := range r.decisions {
		if d.Action != Continue {
			t.Fatalf("tick %d: %v %s", i, d.Action, d.Reason)
		}
	}
	if r.state.FaultWindows != 40 || r.state.WorstErrorRate != 0.01 {
		t.Fatalf("state %+v", r.state)
	}
}

func TestAbortsOnTheFirstWindowOverTheErrorBound(t *testing.T) {
	stream := append(healthy(5), scrape{requests: 100, errors: 6, ok: true})
	stream = append(stream, healthy(5)...)
	r := feed(cfg, StageFault, stream)
	last := r.decisions[len(r.decisions)-1]
	if r.ticks != 6 || last.Action != Abort || last.Reason != ReasonSLOBreach {
		t.Fatalf("expected abort at tick 6, got %d ticks ending %v %s", r.ticks, last.Action, last.Reason)
	}
	if !last.Judgement.Breached || last.Judgement.ErrorRate != 0.06 || last.Judgement.Degradation != 1 {
		t.Fatalf("judgement %+v", last.Judgement)
	}
}

func TestAbortsOnTheLatencyTailAlone(t *testing.T) {
	stream := append(healthy(3), scrape{requests: 1000, errors: 0, slow: 11, ok: true})
	r := feed(cfg, StageFault, stream)
	last := r.decisions[len(r.decisions)-1]
	if r.ticks != 4 || last.Action != Abort || last.Reason != ReasonSLOBreach {
		t.Fatalf("expected a latency abort at tick 4, got %d ticks ending %v %s", r.ticks, last.Action, last.Reason)
	}
	if last.Judgement.SlowFraction != 0.011 {
		t.Fatalf("judgement %+v", last.Judgement)
	}
}

func TestAtTheBoundIsNotABreach(t *testing.T) {
	r := feed(cfg, StageFault, []scrape{{requests: 100, errors: 5, slow: 1, ok: true}})
	if d := r.decisions[0]; d.Action != Continue || d.Judgement.Breached || d.Judgement.Degradation != 1 {
		t.Fatalf("decision %+v", d)
	}
}

func TestSmallWindowsMergeUntilTheSampleFloor(t *testing.T) {
	stream := []scrape{{requests: 5, errors: 5, ok: true}, {requests: 5, errors: 0, ok: true}, {requests: 5, errors: 0, ok: true}, {requests: 5, errors: 0, ok: true}}
	r := feed(cfg, StageFault, stream)
	for i := range 3 {
		if r.decisions[i].Judged {
			t.Fatalf("tick %d judged a window below the floor", i)
		}
	}
	last := r.decisions[3]
	if !last.Judged || last.Judgement.ErrorRate != 0.25 || last.Action != Abort {
		t.Fatalf("merged window should carry the early failures: %+v", last)
	}
	if r.state.FaultWindows != 1 {
		t.Fatalf("one merged window expected, got %d", r.state.FaultWindows)
	}
}

func TestSilenceAbortsAsBlindAfterTheTimeout(t *testing.T) {
	stream := append(healthy(2), scrape{ok: false}, scrape{ok: false}, scrape{ok: false}, scrape{ok: false}, scrape{ok: false}, scrape{ok: false}, scrape{ok: false})
	r := feed(cfg, StageFault, stream)
	last := r.decisions[len(r.decisions)-1]
	// Windows at 5 s and 10 s; the timeout runs from the last judged window at 10 s, so
	// the read at 40 s is the first one 30 s past it.
	if r.ticks != 8 || last.Action != Abort || last.Reason != ReasonMetricsUnavailable {
		t.Fatalf("expected a blind abort at tick 8, got %d ticks ending %v %s", r.ticks, last.Action, last.Reason)
	}
}

func TestNoTrafficIsAsBlindAsNoPrometheus(t *testing.T) {
	stream := append(healthy(1), scrape{ok: true}, scrape{ok: true}, scrape{ok: true}, scrape{ok: true}, scrape{ok: true}, scrape{ok: true})
	r := feed(cfg, StageFault, stream)
	last := r.decisions[len(r.decisions)-1]
	if last.Action != Abort || last.Reason != ReasonMetricsUnavailable {
		t.Fatalf("got %v %s after %d ticks", last.Action, last.Reason, r.ticks)
	}
}

func TestATransientOutageUnderTheTimeoutContinues(t *testing.T) {
	stream := append(healthy(2), scrape{ok: false}, scrape{ok: false}, scrape{ok: false})
	stream = append(stream, healthy(2)...)
	r := feed(cfg, StageFault, stream)
	for i, d := range r.decisions {
		if d.Action != Continue {
			t.Fatalf("tick %d: %v %s", i, d.Action, d.Reason)
		}
	}
	if r.state.FaultWindows != 4 {
		t.Fatalf("expected 4 judged windows, got %d", r.state.FaultWindows)
	}
}

func TestRecoveryCompletesOnTheFirstCleanWindow(t *testing.T) {
	r := feed(cfg, StageRecovery, healthy(3))
	if r.ticks != 1 || r.decisions[0].Action != Complete || r.decisions[0].Reason != ReasonRecovered || r.state.RecoveredAfter != 0 {
		t.Fatalf("got %d ticks, %+v, state %+v", r.ticks, r.decisions[0], r.state)
	}
}

func TestRecoveryCountsDegradedWindowsBeforeTheCleanOne(t *testing.T) {
	stream := []scrape{{requests: 100, errors: 20, ok: true}, {requests: 100, errors: 10, ok: true}, {requests: 100, errors: 1, ok: true}}
	r := feed(cfg, StageRecovery, stream)
	if r.ticks != 3 || r.state.RecoveredAfter != 2 || r.decisions[2].Reason != ReasonRecovered {
		t.Fatalf("got %d ticks, state %+v", r.ticks, r.state)
	}
}

func TestRecoveryGivesUpAfterTheConfiguredWindows(t *testing.T) {
	bad := scrape{requests: 100, errors: 20, ok: true}
	r := feed(cfg, StageRecovery, []scrape{bad, bad, bad, bad, bad})
	if r.ticks != 4 || r.decisions[3].Action != Complete || r.decisions[3].Reason != ReasonNotRecovered || r.state.RecoveredAfter != -1 {
		t.Fatalf("got %d ticks, %+v, state %+v", r.ticks, r.decisions[len(r.decisions)-1], r.state)
	}
}

func TestSilenceDuringRecoveryCompletesWithoutRecovery(t *testing.T) {
	stream := []scrape{{ok: false}, {ok: false}, {ok: false}, {ok: false}, {ok: false}, {ok: false}}
	r := feed(cfg, StageRecovery, stream)
	last := r.decisions[len(r.decisions)-1]
	if r.ticks != 6 || last.Action != Complete || last.Reason != ReasonNotRecovered {
		t.Fatalf("got %d ticks ending %v %s", r.ticks, last.Action, last.Reason)
	}
}

func TestScore(t *testing.T) {
	cases := []struct {
		name string
		st   State
		want float64
	}{
		{"untouched and recovered at once", State{FaultWindows: 4, HeadroomTotal: 4, RecoveredAfter: 0}, 100},
		{"half degraded, recovered at once", State{FaultWindows: 4, HeadroomTotal: 2, RecoveredAfter: 0}, 65},
		{"untouched, recovered on the third window", State{FaultWindows: 4, HeadroomTotal: 4, RecoveredAfter: 2}, 85},
		{"untouched, never recovered", State{FaultWindows: 4, HeadroomTotal: 4, RecoveredAfter: -1}, 70},
		{"no judged fault window, recovered", State{RecoveredAfter: 0}, 30},
		{"fully degraded, never recovered", State{FaultWindows: 2, HeadroomTotal: 0, RecoveredAfter: -1}, 0},
	}
	for _, tc := range cases {
		if got := Score(cfg, tc.st); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestZeroErrorBoundLeavesNoHeadroom(t *testing.T) {
	strict := Config{ErrorRateMax: 0, MinSampleSize: 1, RecoveryWindows: 1, MetricsTimeout: time.Minute}
	if j := strict.Judge(Window{Requests: 100, Errors: 1}); !j.Breached || j.Degradation != 1 {
		t.Fatalf("judgement %+v", j)
	}
	if j := strict.Judge(Window{Requests: 100}); j.Breached || j.Degradation != 0 {
		t.Fatalf("judgement %+v", j)
	}
}
