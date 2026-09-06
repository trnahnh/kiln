package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:validation:Enum=pod-kill;network-partition;latency-injection;resource-exhaustion
type FaultType string

const (
	FaultPodKill            FaultType = "pod-kill"
	FaultNetworkPartition   FaultType = "network-partition"
	FaultLatencyInjection   FaultType = "latency-injection"
	FaultResourceExhaustion FaultType = "resource-exhaustion"
)

// +kubebuilder:validation:Enum=Scheduled;Running;Aborted;Completed
type Phase string

const (
	PhaseScheduled Phase = "Scheduled"
	PhaseRunning   Phase = "Running"
	PhaseAborted   Phase = "Aborted"
	PhaseCompleted Phase = "Completed"
)

// +kubebuilder:validation:Enum=Selected;Reverted
type TargetState string

const (
	// The controller has put the pod in scope; the fault is live for the injection window.
	TargetSelected TargetState = "Selected"
	// The injection window has ended and the lease has lapsed, so every agent that could
	// have touched this pod has reverted by its own contract.
	TargetReverted TargetState = "Reverted"
)

const (
	ConditionReady         = "Ready"
	ConditionFaultsCleared = "FaultsCleared"

	ReasonInvalidSpec        = "InvalidSpec"
	ReasonWaiting            = "Waiting"
	ReasonInjecting          = "Injecting"
	ReasonRunning            = "Running"
	ReasonRecovering         = "Recovering"
	ReasonCompleted          = "Completed"
	ReasonSLOBreach          = "SLOBreach"
	ReasonMetricsUnavailable = "MetricsUnavailable"
	ReasonInjectionFailed    = "InjectionFailed"
	ReasonDeleted            = "Deleted"
	ReasonFaultsLive         = "FaultsLive"
	ReasonFaultsCleared      = "FaultsCleared"

	Finalizer = "platform.internal/chaos-revert"
)

type TargetSpec struct {
	// Must be the experiment's own namespace; unset means exactly that. An experiment may
	// never reach into another namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Label selector in the `key=value,key in (a,b)` syntax choosing the pods in scope.
	// +kubebuilder:validation:MinLength=1
	LabelSelector string `json:"labelSelector"`

	// Upper bound on the share of matching pods a fault may touch at once, floored to whole
	// pods; an experiment that floors to zero pods is rejected rather than rounded up.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MaxReplicaPercentage int32 `json:"maxReplicaPercentage"`
}

// SLO the experiment must not push the target past. Both bounds are required: an
// experiment cannot opt out of aborting.
type SLO struct {
	// Highest acceptable fraction of failed requests in a window, as seen by the callers.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	ErrorRateMax float64 `json:"errorRateMax"`

	// Highest acceptable p99 latency; at most 1% of requests in a window may be slower.
	// +kubebuilder:validation:Minimum=1
	LatencyP99MaxMs int32 `json:"latencyP99MaxMs"`
}

type FaultSpec struct {
	// latency-injection: delay added to every packet the pod sends. Unset means 500.
	// +kubebuilder:validation:Minimum=1
	// +optional
	LatencyMs *int32 `json:"latencyMs,omitempty"`

	// latency-injection: random variation around latencyMs. Unset means 50.
	// +kubebuilder:validation:Minimum=0
	// +optional
	JitterMs *int32 `json:"jitterMs,omitempty"`

	// resource-exhaustion: share of the container's CPU limit the burner competes for.
	// Unset means 100.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	CPUPercent *int32 `json:"cpuPercent,omitempty"`

	// resource-exhaustion: memory the burner allocates inside the container's cgroup.
	// Unset means 0.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MemoryMiB *int32 `json:"memoryMiB,omitempty"`

	// pod-kill: how often a fresh selection of up to the cap is deleted. Unset means 30s.
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

type AnalysisSpec struct {
	// How often the SLO counters are read. Unset means 5s.
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// Length of one metric window; matches the Prometheus scrape interval. Unset means 15s.
	// +optional
	Window *metav1.Duration `json:"window,omitempty"`

	// Requests a window must hold before it is judged; smaller windows merge into the next.
	// Unset means 20.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinSampleSize *int32 `json:"minSampleSize,omitempty"`

	// Windows observed after the fault is removed, for the recovery term of the score.
	// Unset means 4.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	// +optional
	RecoveryWindows *int32 `json:"recoveryWindows,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; create a new experiment"
type ChaosExperimentSpec struct {
	Target TargetSpec `json:"target"`

	FaultType FaultType `json:"faultType"`

	// How long the fault stays injected. Agents revert on this deadline by their own clock
	// whatever the controller does.
	Duration metav1.Duration `json:"duration"`

	AbortOnSLOBreach SLO `json:"abortOnSLOBreach"`

	// +optional
	Fault *FaultSpec `json:"fault,omitempty"`

	// +optional
	Analysis *AnalysisSpec `json:"analysis,omitempty"`
}

// A pod the controller put in scope. The list is the blast-radius record: its length can
// never exceed floor(maxReplicaPercentage x matching pods). The controller owns it; agents
// only read it, re-validate scope against their own API read, and enforce the fault. What
// actually happened to a pod is proven from the node, not from this field.
type TargetStatus struct {
	Pod  string `json:"pod"`
	UID  string `json:"uid"`
	Node string `json:"node"`
	// Container whose cgroup a resource-exhaustion burner joins.
	// +optional
	Container string `json:"container,omitempty"`
	// +optional
	State TargetState `json:"state,omitempty"`
}

type CounterSnapshot struct {
	Requests float64     `json:"requests"`
	Errors   float64     `json:"errors"`
	Slow     float64     `json:"slow"`
	At       metav1.Time `json:"at"`
}

// Persisted so a controller restart resumes the experiment instead of rescoring from zero.
type AnalysisState struct {
	// +optional
	LastCounters *CounterSnapshot `json:"lastCounters,omitempty"`
	// When the last window with enough samples was judged.
	// +optional
	LastWindowAt *metav1.Time `json:"lastWindowAt,omitempty"`
	// +optional
	FaultWindows int32 `json:"faultWindows"`
	// +optional
	HeadroomTotal float64 `json:"headroomTotal"`
	// +optional
	WorstErrorRate float64 `json:"worstErrorRate"`
	// +optional
	WorstSlowFraction float64 `json:"worstSlowFraction"`
	// +optional
	RecoveryWindows int32 `json:"recoveryWindows"`
	// Zero-based index of the first post-fault window within the SLO; unset until one is.
	// +optional
	RecoveredAfter *int32 `json:"recoveredAfter,omitempty"`
}

type ChaosExperimentStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// +optional
	Reason string `json:"reason,omitempty"`

	// +optional
	AbortReason string `json:"abortReason,omitempty"`

	// 0 to 100 once the experiment has ended; unset while it runs.
	// +optional
	ResilienceScore *float64 `json:"resilienceScore,omitempty"`

	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// startedAt plus duration; the fault is removed at this time unless aborted earlier.
	// +optional
	FaultEndsAt *metav1.Time `json:"faultEndsAt,omitempty"`

	// When every injected fault was confirmed gone.
	// +optional
	FaultEndedAt *metav1.Time `json:"faultEndedAt,omitempty"`

	// +optional
	AbortedAt *metav1.Time `json:"abortedAt,omitempty"`

	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Renewed by the controller every analysis interval; agents revert once it lapses.
	// +optional
	LeaseExpiresAt *metav1.Time `json:"leaseExpiresAt,omitempty"`

	// +optional
	Kills int32 `json:"kills,omitempty"`

	// +optional
	LastKillAt *metav1.Time `json:"lastKillAt,omitempty"`

	// +listType=map
	// +listMapKey=pod
	// +optional
	Targets []TargetStatus `json:"targets,omitempty"`

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
// +kubebuilder:printcolumn:name="Fault",type=string,JSONPath=`.spec.faultType`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="Score",type=number,JSONPath=`.status.resilienceScore`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

type ChaosExperiment struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec ChaosExperimentSpec `json:"spec"`
	// +optional
	Status ChaosExperimentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

type ChaosExperimentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ChaosExperiment `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ChaosExperiment{}, &ChaosExperimentList{})
		return nil
	})
}
