package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RemediationState represents the execution state of a remediation plan.
// +kubebuilder:validation:Enum=Pending;Executing;Verifying;Completed;Failed;RolledBack
type RemediationState string

const (
	RemediationStatePending    RemediationState = "Pending"
	RemediationStateExecuting  RemediationState = "Executing"
	RemediationStateVerifying  RemediationState = "Verifying"
	RemediationStateCompleted  RemediationState = "Completed"
	RemediationStateFailed     RemediationState = "Failed"
	RemediationStateRolledBack RemediationState = "RolledBack"
)

// RemediationActionType defines the type of remediation action.
// +kubebuilder:validation:Enum=ScaleDeployment;RollbackDeployment;RestartDeployment;PatchConfig;Custom
type RemediationActionType string

const (
	ActionScaleDeployment    RemediationActionType = "ScaleDeployment"
	ActionRollbackDeployment RemediationActionType = "RollbackDeployment"
	ActionRestartDeployment  RemediationActionType = "RestartDeployment"
	ActionPatchConfig        RemediationActionType = "PatchConfig"
	ActionCustom             RemediationActionType = "Custom"
)

// RemediationAction defines a single remediation step.
type RemediationAction struct {
	// Type of the remediation action.
	Type RemediationActionType `json:"type"`

	// Params are key-value parameters for the action.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// RemediationPlanSpec defines the desired state of a RemediationPlan.
type RemediationPlanSpec struct {
	// IssueRef references the parent Issue.
	IssueRef IssueRef `json:"issueRef"`

	// Attempt number (1-based).
	Attempt int32 `json:"attempt"`

	// Strategy describes the remediation approach.
	Strategy string `json:"strategy"`

	// Actions to execute.
	Actions []RemediationAction `json:"actions"`

	// SafetyConstraints that must be respected.
	// +optional
	SafetyConstraints []string `json:"safetyConstraints,omitempty"`
}

// EvidenceItem is a piece of evidence collected during remediation.
type EvidenceItem struct {
	// Type of evidence (log, metric_snapshot, event).
	Type string `json:"type"`

	// Data content.
	Data string `json:"data"`

	// Timestamp when collected.
	Timestamp metav1.Time `json:"timestamp"`
}

// RemediationPlanStatus defines the observed state of a RemediationPlan.
type RemediationPlanStatus struct {
	// State of the remediation plan execution.
	State RemediationState `json:"state"`

	// StartedAt is when execution began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// ActionsCompletedAt is when all actions finished executing and verification began.
	// +optional
	ActionsCompletedAt *metav1.Time `json:"actionsCompletedAt,omitempty"`

	// CompletedAt is when execution finished (after verification).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Result describes the outcome.
	// +optional
	Result string `json:"result,omitempty"`

	// Evidence collected during execution.
	// +optional
	Evidence []EvidenceItem `json:"evidence,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rp
// +kubebuilder:printcolumn:name="Issue",type="string",JSONPath=".spec.issueRef.name"
// +kubebuilder:printcolumn:name="Attempt",type="integer",JSONPath=".spec.attempt"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// RemediationPlan records a remediation attempt for an Issue.
type RemediationPlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RemediationPlanSpec   `json:"spec,omitempty"`
	Status RemediationPlanStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RemediationPlanList contains a list of RemediationPlan.
type RemediationPlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RemediationPlan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RemediationPlan{}, &RemediationPlanList{})
}
