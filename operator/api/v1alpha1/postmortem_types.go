package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PostMortemState represents the review lifecycle of a PostMortem.
// +kubebuilder:validation:Enum=Open;InReview;Closed
type PostMortemState string

const (
	PostMortemStateOpen     PostMortemState = "Open"
	PostMortemStateInReview PostMortemState = "InReview"
	PostMortemStateClosed   PostMortemState = "Closed"
)

// TimelineEvent is a single timestamped event in the incident timeline.
type TimelineEvent struct {
	// Timestamp of this event.
	Timestamp metav1.Time `json:"timestamp"`

	// Type is one of: detected, analyzed, action_executed, action_failed, verified, resolved.
	Type string `json:"type"`

	// Detail describes what happened.
	Detail string `json:"detail"`
}

// ActionRecord captures a single remediation action and its outcome.
type ActionRecord struct {
	// Action type that was executed (e.g., "RestartDeployment", "AdjustResources").
	Action string `json:"action"`

	// Params are the action-specific parameters.
	// +optional
	Params map[string]string `json:"params,omitempty"`

	// Result is "success" or "failed".
	Result string `json:"result"`

	// Detail describes the outcome.
	Detail string `json:"detail"`

	// Timestamp of execution.
	Timestamp metav1.Time `json:"timestamp"`
}

// PostMortemSpec defines the immutable input fields for a PostMortem.
type PostMortemSpec struct {
	// IssueRef references the parent Issue.
	IssueRef IssueRef `json:"issueRef"`

	// Resource that was affected.
	Resource ResourceRef `json:"resource"`

	// Severity of the original issue.
	Severity IssueSeverity `json:"severity"`
}

// PostMortemStatus defines the observed state of a PostMortem.
type PostMortemStatus struct {
	// State is the review lifecycle state.
	State PostMortemState `json:"state"`

	// Summary is an AI-generated incident summary.
	// +optional
	Summary string `json:"summary,omitempty"`

	// RootCause is the AI-determined root cause.
	// +optional
	RootCause string `json:"rootCause,omitempty"`

	// Impact describes what was affected by the incident.
	// +optional
	Impact string `json:"impact,omitempty"`

	// Timeline is a chronological list of incident events.
	// +optional
	Timeline []TimelineEvent `json:"timeline,omitempty"`

	// ActionsExecuted records all remediation actions and their results.
	// +optional
	ActionsExecuted []ActionRecord `json:"actionsExecuted,omitempty"`

	// LessonsLearned are AI-generated lessons from the incident.
	// +optional
	LessonsLearned []string `json:"lessonsLearned,omitempty"`

	// PreventionActions are AI-generated steps to prevent recurrence.
	// +optional
	PreventionActions []string `json:"preventionActions,omitempty"`

	// Duration is the total incident duration (e.g., "12m34s").
	// +optional
	Duration string `json:"duration,omitempty"`

	// GeneratedAt is when this PostMortem was auto-generated.
	// +optional
	GeneratedAt *metav1.Time `json:"generatedAt,omitempty"`

	// ReviewedAt is when the PostMortem was reviewed by a human.
	// +optional
	ReviewedAt *metav1.Time `json:"reviewedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pm
// +kubebuilder:printcolumn:name="Issue",type="string",JSONPath=".spec.issueRef.name"
// +kubebuilder:printcolumn:name="Severity",type="string",JSONPath=".spec.severity"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// PostMortem captures the full incident lifecycle after resolution.
type PostMortem struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PostMortemSpec   `json:"spec,omitempty"`
	Status PostMortemStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PostMortemList contains a list of PostMortem.
type PostMortemList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PostMortem `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PostMortem{}, &PostMortemList{})
}
