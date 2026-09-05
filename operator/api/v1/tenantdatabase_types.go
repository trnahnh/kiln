package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:validation:Enum=postgres;redis
type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineRedis    Engine = "redis"
)

// +kubebuilder:validation:Enum=standard;custom
type Tier string

const (
	TierStandard Tier = "standard"
	TierCustom   Tier = "custom"
)

// +kubebuilder:validation:Enum=Provisioning;Ready;"Backing Up";Restoring;Failed
type Phase string

const (
	PhaseProvisioning Phase = "Provisioning"
	PhaseReady        Phase = "Ready"
	PhaseBackingUp    Phase = "Backing Up"
	PhaseRestoring    Phase = "Restoring"
	PhaseFailed       Phase = "Failed"
)

const (
	ConditionReady       = "Ready"
	ConditionProgressing = "Progressing"

	ReasonProvisioning      = "Provisioning"
	ReasonProvisionFailed   = "ProvisionFailed"
	ReasonScaling           = "Scaling"
	ReasonBackingUp         = "BackingUp"
	ReasonBackupFailed      = "BackupFailed"
	ReasonRestoring         = "Restoring"
	ReasonRestoreFailed     = "RestoreFailed"
	ReasonUnsupportedEngine = "UnsupportedEngine"
	ReasonReconcileConflict = "RECONCILE_CONFLICT"
	ReasonReconciled        = "Reconciled"
)

// One-shot actions are requested through annotations (ADR-0002).
const (
	AnnotationBackup      = "platform.internal/backup"
	AnnotationBackupNow   = "now"
	AnnotationRestoreFrom = "platform.internal/restore-from"
	RestoreFromLatest     = "latest"
)

type TenantDatabaseSpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="engine is immutable"
	Engine Engine `json:"engine"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="version is immutable"
	Version string `json:"version"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self >= oldSelf",message="storageGB cannot shrink"
	StorageGB int32 `json:"storageGB"`

	// Cron expression; empty disables scheduled backups.
	// +optional
	BackupSchedule string `json:"backupSchedule,omitempty"`

	// +kubebuilder:default=standard
	// +optional
	Tier Tier `json:"tier,omitempty"`
}

type TenantDatabaseStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// +optional
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Engine",type=string,JSONPath=`.spec.engine`
// +kubebuilder:printcolumn:name="Storage",type=integer,JSONPath=`.spec.storageGB`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Last Backup",type=date,JSONPath=`.status.lastBackupTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

type TenantDatabase struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec TenantDatabaseSpec `json:"spec"`
	// +optional
	Status TenantDatabaseStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

type TenantDatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TenantDatabase `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &TenantDatabase{}, &TenantDatabaseList{})
		return nil
	})
}
