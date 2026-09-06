package v1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DefaultLatencyMs       = 500
	DefaultJitterMs        = 50
	DefaultCPUPercent      = 100
	DefaultMemoryMiB       = 0
	DefaultKillInterval    = 30 * time.Second
	DefaultAnalysisTick    = 5 * time.Second
	DefaultWindow          = 15 * time.Second
	DefaultMinSampleSize   = 20
	DefaultRecoveryWindows = 4

	// A window judged, or the fault removed, cannot lag Prometheus by more than this before
	// the experiment counts as blind; two scrape windows plus slack.
	MetricsTimeout = 30 * time.Second
)

func intOr(p *int32, def int) int {
	if p != nil {
		return int(*p)
	}
	return def
}

func durationOr(p *metav1.Duration, def time.Duration) time.Duration {
	if p != nil && p.Duration > 0 {
		return p.Duration
	}
	return def
}

func (s ChaosExperimentSpec) LatencyMs() int {
	return faultInt(s.Fault, func(f *FaultSpec) *int32 { return f.LatencyMs }, DefaultLatencyMs)
}
func (s ChaosExperimentSpec) JitterMs() int {
	return faultInt(s.Fault, func(f *FaultSpec) *int32 { return f.JitterMs }, DefaultJitterMs)
}
func (s ChaosExperimentSpec) CPUPercent() int {
	return faultInt(s.Fault, func(f *FaultSpec) *int32 { return f.CPUPercent }, DefaultCPUPercent)
}
func (s ChaosExperimentSpec) MemoryMiB() int {
	return faultInt(s.Fault, func(f *FaultSpec) *int32 { return f.MemoryMiB }, DefaultMemoryMiB)
}

func (s ChaosExperimentSpec) KillInterval() time.Duration {
	if s.Fault != nil {
		return durationOr(s.Fault.Interval, DefaultKillInterval)
	}
	return DefaultKillInterval
}

func (s ChaosExperimentSpec) AnalysisInterval() time.Duration {
	if s.Analysis != nil {
		return durationOr(s.Analysis.Interval, DefaultAnalysisTick)
	}
	return DefaultAnalysisTick
}

func (s ChaosExperimentSpec) Window() time.Duration {
	if s.Analysis != nil {
		return durationOr(s.Analysis.Window, DefaultWindow)
	}
	return DefaultWindow
}

func (s ChaosExperimentSpec) MinSampleSize() int64 {
	if s.Analysis != nil {
		return int64(intOr(s.Analysis.MinSampleSize, DefaultMinSampleSize))
	}
	return DefaultMinSampleSize
}

func (s ChaosExperimentSpec) RecoveryWindows() int {
	if s.Analysis != nil {
		return intOr(s.Analysis.RecoveryWindows, DefaultRecoveryWindows)
	}
	return DefaultRecoveryWindows
}

func faultInt(f *FaultSpec, get func(*FaultSpec) *int32, def int) int {
	if f != nil {
		return intOr(get(f), def)
	}
	return def
}
