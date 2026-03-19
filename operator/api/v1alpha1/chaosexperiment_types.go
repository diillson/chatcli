package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ChaosExperimentType defines the kind of chaos to inject.
// +kubebuilder:validation:Enum=pod_kill;pod_failure;cpu_stress;memory_stress;network_delay;network_loss;disk_stress
type ChaosExperimentType string

const (
	ChaosTypePodKill      ChaosExperimentType = "pod_kill"
	ChaosTypePodFailure   ChaosExperimentType = "pod_failure"
	ChaosTypeCPUStress    ChaosExperimentType = "cpu_stress"
	ChaosTypeMemoryStress ChaosExperimentType = "memory_stress"
	ChaosTypeNetworkDelay ChaosExperimentType = "network_delay"
	ChaosTypeNetworkLoss  ChaosExperimentType = "network_loss"
	ChaosTypeDiskStress   ChaosExperimentType = "disk_stress"
)

// ChaosExperimentState defines the lifecycle state of a chaos experiment.
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed;Aborted
type ChaosExperimentState string

const (
	ChaosStatePending   ChaosExperimentState = "Pending"
	ChaosStateRunning   ChaosExperimentState = "Running"
	ChaosStateCompleted ChaosExperimentState = "Completed"
	ChaosStateFailed    ChaosExperimentState = "Failed"
	ChaosStateAborted   ChaosExperimentState = "Aborted"
)

// ChaosSafetyChecks defines safety constraints for a chaos experiment.
type ChaosSafetyChecks struct {
	// MinHealthyPods is the minimum number of pods that must remain healthy during the experiment.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinHealthyPods int32 `json:"minHealthyPods,omitempty"`

	// MaxConcurrentExperiments is the maximum number of chaos experiments that can run simultaneously.
	// +kubebuilder:default=1
	// +optional
	MaxConcurrentExperiments int32 `json:"maxConcurrentExperiments,omitempty"`

	// AbortOnIssueDetected stops the experiment if AIOps detects a new issue targeting the same resource.
	// +optional
	AbortOnIssueDetected bool `json:"abortOnIssueDetected,omitempty"`

	// RequireApproval creates an ApprovalRequest before executing the experiment.
	// +optional
	RequireApproval bool `json:"requireApproval,omitempty"`

	// AllowedNamespaces restricts which namespaces the experiment can target. Empty means all.
	// +optional
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`

	// BlockedNamespaces prevents experiments from targeting these namespaces.
	// +optional
	BlockedNamespaces []string `json:"blockedNamespaces,omitempty"`
}

// PostExperimentSpec configures post-experiment verification behavior.
type PostExperimentSpec struct {
	// VerifyRecovery checks if the target deployment recovers after the experiment.
	// +optional
	VerifyRecovery bool `json:"verifyRecovery,omitempty"`

	// RecoveryTimeout is the maximum time to wait for recovery (e.g., "5m").
	// +optional
	RecoveryTimeout string `json:"recoveryTimeout,omitempty"`

	// RunRemediationTest re-injects the fault after a successful remediation to validate it works.
	// +optional
	RunRemediationTest bool `json:"runRemediationTest,omitempty"`
}

// ChaosExperimentSpec defines the desired state of a ChaosExperiment.
type ChaosExperimentSpec struct {
	// ExperimentType is the type of chaos to inject.
	ExperimentType ChaosExperimentType `json:"experimentType"`

	// Target is the Kubernetes resource to test.
	Target ResourceRef `json:"target"`

	// Duration of the experiment (e.g., "2m", "5m", "30s").
	Duration string `json:"duration"`

	// Parameters are experiment-specific key-value pairs.
	// For pod_kill: count (number of pods to kill).
	// For pod_failure: count, gracePeriodSeconds.
	// For cpu_stress: cores, loadPercent.
	// For memory_stress: bytes (e.g., "256Mi"), workers.
	// For network_delay: latencyMs, jitterMs, interface.
	// For network_loss: percent, correlation.
	// For disk_stress: size (e.g., "1Gi"), workers.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// Schedule is an optional cron expression for recurring experiments (e.g., "0 3 * * 1").
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// DryRun simulates the experiment without executing destructive actions.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`

	// SafetyChecks configures guardrails for the experiment.
	// +optional
	SafetyChecks ChaosSafetyChecks `json:"safetyChecks,omitempty"`

	// PostExperiment configures recovery verification and remediation testing.
	// +optional
	PostExperiment PostExperimentSpec `json:"postExperiment,omitempty"`

	// LinkedIssueRef optionally links this experiment to an Issue it validates.
	// +optional
	LinkedIssueRef *IssueRef `json:"linkedIssueRef,omitempty"`

	// Enabled controls whether the experiment is active. Disabled experiments are skipped.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}

// ChaosExperimentStatus defines the observed state of a ChaosExperiment.
type ChaosExperimentStatus struct {
	// State is the current lifecycle state of the experiment.
	State ChaosExperimentState `json:"state"`

	// StartedAt is when the experiment began executing.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the experiment finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Result is a human-readable summary of the experiment outcome.
	// +optional
	Result string `json:"result,omitempty"`

	// RecoveryVerified indicates whether the target recovered after the experiment.
	RecoveryVerified bool `json:"recoveryVerified"`

	// RecoveryTime is how long it took the target to recover (e.g., "45s").
	// +optional
	RecoveryTime string `json:"recoveryTime,omitempty"`

	// PodsAffected is the number of pods that were impacted by the experiment.
	PodsAffected int32 `json:"podsAffected"`

	// PreExperimentSnapshot captures the deployment state before the experiment.
	// +optional
	PreExperimentSnapshot string `json:"preExperimentSnapshot,omitempty"`

	// PostExperimentSnapshot captures the deployment state after the experiment.
	// +optional
	PostExperimentSnapshot string `json:"postExperimentSnapshot,omitempty"`

	// Conditions represent the latest available observations of the experiment.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=chaos
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.experimentType"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.target.name"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Duration",type="string",JSONPath=".spec.duration"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ChaosExperiment represents a chaos engineering experiment targeting a Kubernetes resource.
type ChaosExperiment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChaosExperimentSpec   `json:"spec,omitempty"`
	Status ChaosExperimentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ChaosExperimentList contains a list of ChaosExperiment.
type ChaosExperimentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChaosExperiment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChaosExperiment{}, &ChaosExperimentList{})
}
