package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RunbookTrigger defines when a runbook should be activated.
type RunbookTrigger struct {
	// SignalType that triggers this runbook.
	SignalType AnomalySignalType `json:"signalType"`

	// Severity that triggers this runbook.
	Severity IssueSeverity `json:"severity"`

	// ResourceKind is the kind of resource this runbook applies to.
	ResourceKind string `json:"resourceKind"`
}

// RunbookStep defines a single operational step.
type RunbookStep struct {
	// Name of the step.
	Name string `json:"name"`

	// Action to execute (e.g., CheckRecentRollout, ScaleDeployment, RollbackDeployment).
	Action string `json:"action"`

	// Description explains why this step is needed and what it does.
	// +optional
	Description string `json:"description,omitempty"`

	// Params are optional key-value parameters for the action.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// RunbookSpec defines the desired state of a Runbook.
type RunbookSpec struct {
	// Description explains the purpose of this runbook.
	// +optional
	Description string `json:"description,omitempty"`

	// Trigger defines when this runbook should be activated.
	Trigger RunbookTrigger `json:"trigger"`

	// Steps are the operational steps to follow.
	Steps []RunbookStep `json:"steps"`

	// MaxAttempts is the maximum number of remediation attempts.
	// +kubebuilder:default=3
	// +optional
	MaxAttempts int32 `json:"maxAttempts,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=rb
// +kubebuilder:printcolumn:name="Signal",type="string",JSONPath=".spec.trigger.signalType"
// +kubebuilder:printcolumn:name="Severity",type="string",JSONPath=".spec.trigger.severity"
// +kubebuilder:printcolumn:name="ResourceKind",type="string",JSONPath=".spec.trigger.resourceKind"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Runbook defines operational procedures linked to issue types.
type Runbook struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RunbookSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// RunbookList contains a list of Runbook.
type RunbookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Runbook `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Runbook{}, &RunbookList{})
}
