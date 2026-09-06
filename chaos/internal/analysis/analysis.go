// Package analysis decides, from metric windows alone, whether an experiment must abort and
// what it scores. It knows nothing about faults or Kubernetes, so the abort rule can be
// exercised with synthetic streams.
package analysis

import (
	"math"
	"time"
)

// A p99 bound means at most this share of a window's requests may exceed it.
const SlowFractionMax = 0.01

const (
	headroomWeight = 0.7
	recoveryWeight = 0.3
)

type Config struct {
	ErrorRateMax    float64
	MinSampleSize   int64
	RecoveryWindows int
	// Longest gap without a judged window before the experiment is treated as blind.
	MetricsTimeout time.Duration
}

// Window is the traffic between two counter snapshots.
type Window struct {
	Requests float64
	Errors   float64
	Slow     float64
}

type Judgement struct {
	ErrorRate    float64
	SlowFraction float64
	// How far into the SLO the window went, 0 untouched to 1 at or beyond a bound.
	Degradation float64
	Breached    bool
}

func (c Config) Judge(w Window) Judgement {
	if w.Requests <= 0 {
		return Judgement{}
	}
	j := Judgement{ErrorRate: w.Errors / w.Requests, SlowFraction: w.Slow / w.Requests}
	j.Breached = j.ErrorRate > c.ErrorRateMax || j.SlowFraction > SlowFractionMax
	j.Degradation = math.Min(1, math.Max(ratio(j.ErrorRate, c.ErrorRateMax), ratio(j.SlowFraction, SlowFractionMax)))
	return j
}

// A zero bound leaves no headroom at all: any failure is a full degradation.
func ratio(value, bound float64) float64 {
	if bound <= 0 {
		if value > 0 {
			return 1
		}
		return 0
	}
	return value / bound
}

type Stage int

const (
	// The fault is injected; every judged window counts toward headroom.
	StageFault Stage = iota
	// The fault is gone; windows count toward recovery.
	StageRecovery
)

// State carries the experiment's evidence between ticks; the controller persists it.
type State struct {
	LastWindowAt      time.Time
	FaultWindows      int
	HeadroomTotal     float64
	WorstErrorRate    float64
	WorstSlowFraction float64
	RecoveryWindows   int
	// Zero-based index of the first recovery window within the SLO; -1 until one is.
	RecoveredAfter int
}

func NewState(start time.Time) State {
	return State{LastWindowAt: start, RecoveredAfter: -1}
}

type Action int

const (
	Continue Action = iota
	Abort
	Complete
)

func (a Action) String() string {
	switch a {
	case Abort:
		return "Abort"
	case Complete:
		return "Complete"
	}
	return "Continue"
}

type Reason string

const (
	ReasonSLOBreach          Reason = "SLOBreach"
	ReasonMetricsUnavailable Reason = "MetricsUnavailable"
	ReasonRecovered          Reason = "Recovered"
	ReasonNotRecovered       Reason = "NotRecovered"
)

type Decision struct {
	Action Action
	Reason Reason
	// Set when a window was judged this tick.
	Judged    bool
	Judgement Judgement
}

// Tick folds one read of the counters into the state. A window below the sample floor is
// not judged; the caller keeps its previous snapshot so the next read covers both. During
// the fault a breach aborts on the spot and silence past the timeout aborts as blind;
// during recovery the first clean window completes, and silence or exhausting the
// recovery windows completes without recovery.
func Tick(cfg Config, st *State, now time.Time, stage Stage, w Window, ok bool) Decision {
	if !ok || w.Requests < float64(cfg.MinSampleSize) || w.Requests <= 0 {
		if now.Sub(st.LastWindowAt) >= cfg.MetricsTimeout {
			if stage == StageFault {
				return Decision{Action: Abort, Reason: ReasonMetricsUnavailable}
			}
			return Decision{Action: Complete, Reason: ReasonNotRecovered}
		}
		return Decision{}
	}
	j := cfg.Judge(w)
	st.LastWindowAt = now
	st.WorstErrorRate = math.Max(st.WorstErrorRate, j.ErrorRate)
	st.WorstSlowFraction = math.Max(st.WorstSlowFraction, j.SlowFraction)
	d := Decision{Judged: true, Judgement: j}
	switch stage {
	case StageFault:
		st.FaultWindows++
		st.HeadroomTotal += 1 - j.Degradation
		if j.Breached {
			d.Action, d.Reason = Abort, ReasonSLOBreach
		}
	case StageRecovery:
		st.RecoveryWindows++
		if !j.Breached {
			st.RecoveredAfter = st.RecoveryWindows - 1
			d.Action, d.Reason = Complete, ReasonRecovered
		} else if st.RecoveryWindows >= cfg.RecoveryWindows {
			d.Action, d.Reason = Complete, ReasonNotRecovered
		}
	}
	return d
}

// Score is 0 to 100 (ADR-0016): mean SLO headroom while the fault was live, and how quickly the
// service was back within its SLO afterwards. A fault phase that never produced a judged
// window earns no headroom credit.
func Score(cfg Config, st State) float64 {
	headroom := 0.0
	if st.FaultWindows > 0 {
		headroom = st.HeadroomTotal / float64(st.FaultWindows)
	}
	recovery := 0.0
	if st.RecoveredAfter >= 0 && cfg.RecoveryWindows > 0 {
		recovery = math.Max(0, 1-float64(st.RecoveredAfter)/float64(cfg.RecoveryWindows))
	}
	return math.Round(100*(headroomWeight*headroom+recoveryWeight*recovery)*100) / 100
}
