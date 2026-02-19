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
// +kubebuilder:validation:Enum=ScaleDeployment;RollbackDeployment;RestartDeployment;PatchConfig;AdjustResources;DeletePod;Custom
type RemediationActionType string

const (
	ActionScaleDeployment    RemediationActionType = "ScaleDeployment"
	ActionRollbackDeployment RemediationActionType = "RollbackDeployment"
	ActionRestartDeployment  RemediationActionType = "RestartDeployment"
	ActionPatchConfig        RemediationActionType = "PatchConfig"
	ActionAdjustResources    RemediationActionType = "AdjustResources"
	ActionDeletePod          RemediationActionType = "DeletePod"
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

// AgenticStep records one turn of the AI-driven remediation loop.
type AgenticStep struct {
	// StepNumber is 1-based, incremented each reconcile cycle.
	StepNumber int32 `json:"stepNumber"`

	// AIMessage is the AI's reasoning for this step.
	AIMessage string `json:"aiMessage"`

	// Action is the action the AI decided to take (nil if observation-only or resolved).
	// +optional
	Action *RemediationAction `json:"action,omitempty"`

	// Observation is the result of executing the action.
	// +optional
	Observation string `json:"observation,omitempty"`

	// Timestamp is when this step was recorded.
	Timestamp metav1.Time `json:"timestamp"`
}

// RemediationPlanSpec defines the desired state of a RemediationPlan.
type RemediationPlanSpec struct {
	// IssueRef references the parent Issue.
	IssueRef IssueRef `json:"issueRef"`

	// Attempt number (1-based).
	Attempt int32 `json:"attempt"`

	// Strategy describes the remediation approach.
	Strategy string `json:"strategy"`

	// Actions to execute (ignored when AgenticMode is true).
	// +optional
	Actions []RemediationAction `json:"actions,omitempty"`

	// SafetyConstraints that must be respected.
	// +optional
	SafetyConstraints []string `json:"safetyConstraints,omitempty"`

	// AgenticMode enables AI-driven step-by-step remediation.
	// When true, Actions is ignored and the AI decides each step.
	// +optional
	AgenticMode bool `json:"agenticMode,omitempty"`

	// AgenticHistory records all steps of the agentic loop.
	// Stored in spec (not status) because it is the authoritative
	// conversation history sent to the AI on every step.
	// +optional
	AgenticHistory []AgenticStep `json:"agenticHistory,omitempty"`

	// AgenticMaxSteps is the maximum number of agentic steps before forced failure.
	// +kubebuilder:default=10
	// +optional
	AgenticMaxSteps int32 `json:"agenticMaxSteps,omitempty"`
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

	// AgenticStepCount is the number of agentic steps completed.
	// +optional
	AgenticStepCount int32 `json:"agenticStepCount,omitempty"`

	// AgenticStartedAt is when the agentic loop first started.
	// +optional
	AgenticStartedAt *metav1.Time `json:"agenticStartedAt,omitempty"`
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
