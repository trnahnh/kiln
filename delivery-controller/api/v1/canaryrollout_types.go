package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:validation:Enum=prometheus
type MetricProvider string

const MetricProviderPrometheus MetricProvider = "prometheus"

// +kubebuilder:validation:Enum=Initializing;Progressing;Analyzing;Promoting;Draining;RolledBack;Succeeded
type Phase string

const (
	PhaseInitializing Phase = "Initializing"
	PhaseProgressing  Phase = "Progressing"
	PhaseAnalyzing    Phase = "Analyzing"
	PhasePromoting    Phase = "Promoting"
	PhaseDraining     Phase = "Draining"
	PhaseRolledBack   Phase = "RolledBack"
	PhaseSucceeded    Phase = "Succeeded"
)

// +kubebuilder:validation:Enum=Pending;Pass;Fail
type AnalysisResult string

const (
	AnalysisPending AnalysisResult = "Pending"
	AnalysisPass    AnalysisResult = "Pass"
	AnalysisFail    AnalysisResult = "Fail"
)

const (
	ConditionReady       = "Ready"
	ConditionProgressing = "Progressing"

	ReasonInitializing       = "Initializing"
	ReasonTargetMissing      = "TargetMissing"
	ReasonServiceMissing     = "ServiceMissing"
	ReasonInvalidSpec        = "InvalidSpec"
	ReasonCanaryUnavailable  = "CanaryUnavailable"
	ReasonRolloutStarted     = "RolloutStarted"
	ReasonTrafficShifted     = "TrafficShifted"
	ReasonWaitingForCanary   = "WaitingForCanary"
	ReasonAnalyzing          = "Analyzing"
	ReasonPromoting          = "Promoting"
	ReasonPromoted           = "Promoted"
	ReasonRegressionDetected = "RegressionDetected"
	ReasonInconclusive       = "Inconclusive"
	ReasonMetricsUnavailable = "MetricsUnavailable"
	ReasonIdle               = "Idle"
)

// The controller tells primary pods from canary pods by this label, which it adds to both
// pod templates; the target Deployment's own selector is immutable and stays untouched.
const (
	LabelRole       = "platform.internal/canary-role"
	RolePrimary     = "primary"
	RoleCanary      = "canary"
	LabelRolloutRef = "platform.internal/canary-rollout"
)

type SuccessCriteria struct {
	// Highest acceptable fraction of 5xx responses; the null hypothesis of the error test.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	ErrorRateMax float64 `json:"errorRateMax"`

	// Highest acceptable p99 latency; at most 1% of requests may be slower than this.
	// +kubebuilder:validation:Minimum=1
	LatencyP99MaxMs int32 `json:"latencyP99MaxMs"`

	// Requests the canary must have served since reaching the current checkpoint before any
	// decision, promote or rollback, is allowed to fire.
	// +kubebuilder:validation:Minimum=1
	MinSampleSize int32 `json:"minSampleSize"`
}

type AnalysisSpec struct {
	// How often metrics are read and the sequential test advanced.
	// +kubebuilder:default="15s"
	// +optional
	Interval metav1.Duration `json:"interval,omitempty"`

	// A checkpoint that has neither promoted nor rolled back within this window rolls back.
	// +kubebuilder:default="30m"
	// +optional
	MaxStepDuration metav1.Duration `json:"maxStepDuration,omitempty"`

	// Ceiling on the probability that a canary meeting the criteria is rolled back.
	// +kubebuilder:default=0.05
	// +kubebuilder:validation:Minimum=0.0001
	// +kubebuilder:validation:Maximum=0.5
	// +optional
	Alpha float64 `json:"alpha,omitempty"`

	// Ceiling on the probability that a canary at the regression magnitude is promoted.
	// +kubebuilder:default=0.1
	// +kubebuilder:validation:Minimum=0.0001
	// +kubebuilder:validation:Maximum=0.5
	// +optional
	Beta float64 `json:"beta,omitempty"`

	// The alternative hypothesis is this multiple of the configured maximum.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1.1
	// +optional
	RegressionFactor float64 `json:"regressionFactor,omitempty"`

	// How long the canary keeps running after traffic has been routed away from it, so
	// meshed clients whose sidecars have not yet received the new routes are not sent to
	// pods that are already gone. Unset means 10s; an explicit zero parks the canary
	// immediately.
	// +optional
	DrainGrace *metav1.Duration `json:"drainGrace,omitempty"`
}

type CanaryRolloutSpec struct {
	// Deployment in the same namespace whose pod template drives rollouts; it serves as the
	// canary, the controller owns the primary copy.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="targetDeployment is immutable"
	TargetDeployment string `json:"targetDeployment"`

	// +kubebuilder:default=prometheus
	// +optional
	MetricProvider MetricProvider `json:"metricProvider,omitempty"`

	SuccessCriteria SuccessCriteria `json:"successCriteria"`

	// Canary traffic checkpoints, ascending, ending at 100. Each must be accepted on fresh
	// samples; between them traffic moves in confidence-sized sub-steps.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=100
	StepPercentages []int32 `json:"stepPercentages"`

	// +optional
	Analysis *AnalysisSpec `json:"analysis,omitempty"`
}

// Per-criterion sequential test state; the cumulative statistic decides rollback, the
// checkpoint statistic decides acceptance and sizes the next sub-step.
type CriterionState struct {
	// +optional
	Cumulative float64 `json:"cumulative"`
	// +optional
	SinceCheckpoint float64 `json:"sinceCheckpoint"`
}

type CounterSnapshot struct {
	Requests float64     `json:"requests"`
	Errors   float64     `json:"errors"`
	Slow     float64     `json:"slow"`
	At       metav1.Time `json:"at"`
}

type AnalysisState struct {
	// Index into stepPercentages of the last checkpoint reached.
	Checkpoint int32 `json:"checkpoint"`
	// +optional
	Errors CriterionState `json:"errors"`
	// +optional
	Latency CriterionState `json:"latency"`
	// +optional
	SamplesSinceCheckpoint int64 `json:"samplesSinceCheckpoint"`
	// +optional
	TotalSamples int64 `json:"totalSamples"`
	// +optional
	Confidence float64 `json:"confidence"`
	// Pending halvings of the next sub-step, one per anomalous tick.
	// +optional
	Shrink int32 `json:"shrink"`
	// +optional
	Anomalies int32 `json:"anomalies"`
	// +optional
	CheckpointStartedAt *metav1.Time `json:"checkpointStartedAt,omitempty"`
	// +optional
	LastTickAt *metav1.Time `json:"lastTickAt,omitempty"`
	// +optional
	LastCounters *CounterSnapshot `json:"lastCounters,omitempty"`
}

type CanaryRolloutStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// 1-based index of the last stepPercentages checkpoint reached.
	// +optional
	CurrentStep int32 `json:"currentStep,omitempty"`

	// +optional
	CanaryWeight int32 `json:"canaryWeight,omitempty"`

	// +optional
	LastAnalysisResult AnalysisResult `json:"lastAnalysisResult,omitempty"`

	// +optional
	Reason string `json:"reason,omitempty"`

	// Replica count the target Deployment had when last seen non-zero; restored for each
	// rollout since the target is scaled to zero while idle.
	// +optional
	TargetReplicas *int32 `json:"targetReplicas,omitempty"`

	// +optional
	ObservedTemplateHash string `json:"observedTemplateHash,omitempty"`

	// +optional
	PromotedTemplateHash string `json:"promotedTemplateHash,omitempty"`

	// When the VirtualService was last routed entirely to primary; the canary is parked
	// drainGrace after this.
	// +optional
	TrafficFlippedAt *metav1.Time `json:"trafficFlippedAt,omitempty"`

	// +optional
	Analysis *AnalysisState `json:"analysis,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetDeployment`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Step",type=integer,JSONPath=`.status.currentStep`
// +kubebuilder:printcolumn:name="Weight",type=integer,JSONPath=`.status.canaryWeight`
// +kubebuilder:printcolumn:name="Analysis",type=string,JSONPath=`.status.lastAnalysisResult`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

type CanaryRollout struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec CanaryRolloutSpec `json:"spec"`
	// +optional
	Status CanaryRolloutStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

type CanaryRolloutList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CanaryRollout `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &CanaryRollout{}, &CanaryRolloutList{})
		return nil
	})
}
