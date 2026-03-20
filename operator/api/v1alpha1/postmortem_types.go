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

// MetricSnapshot captures a metric value at a point in time.
type MetricSnapshot struct {
	// Name of the metric (e.g., "cpu_usage_percent", "memory_usage_bytes").
	Name string `json:"name"`

	// Value is the metric value.
	Value string `json:"value"`

	// Timestamp of the measurement.
	Timestamp metav1.Time `json:"timestamp"`

	// Phase indicates when this was captured: "before", "during", or "after" the incident.
	Phase string `json:"phase"`
}

// BlastRadiusEntry describes a service or resource affected by the incident.
type BlastRadiusEntry struct {
	// Resource affected.
	Resource ResourceRef `json:"resource"`

	// Impact describes how this resource was affected.
	Impact string `json:"impact"`

	// Severity of the impact on this resource.
	Severity string `json:"severity"`
}

// GitCorrelation links the incident to a specific code change.
type GitCorrelation struct {
	// CommitSHA is the suspected commit.
	CommitSHA string `json:"commitSHA"`

	// CommitMessage is the commit message.
	CommitMessage string `json:"commitMessage"`

	// Author of the commit.
	Author string `json:"author"`

	// Timestamp of the commit.
	Timestamp metav1.Time `json:"timestamp"`

	// Confidence that this commit caused the incident (0.0-1.0).
	Confidence float64 `json:"confidence"`

	// FilesChanged lists the files modified.
	// +optional
	FilesChanged []string `json:"filesChanged,omitempty"`
}

// DevFeedback captures human feedback on a PostMortem.
type DevFeedback struct {
	// OverrideRootCause is the human-provided root cause (overrides AI analysis).
	// +optional
	OverrideRootCause string `json:"overrideRootCause,omitempty"`

	// RemediationAccuracy rates the AI's remediation (1-5).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	// +optional
	RemediationAccuracy int32 `json:"remediationAccuracy,omitempty"`

	// Comments from the developer.
	// +optional
	Comments string `json:"comments,omitempty"`

	// ProvidedBy identifies who provided the feedback.
	// +optional
	ProvidedBy string `json:"providedBy,omitempty"`

	// ProvidedAt is when the feedback was given.
	// +optional
	ProvidedAt *metav1.Time `json:"providedAt,omitempty"`
}

// TrendingInfo captures recurring incident patterns.
type TrendingInfo struct {
	// OccurrenceCount is how many times this pattern has occurred in the window.
	OccurrenceCount int32 `json:"occurrenceCount"`

	// WindowDays is the lookback window in days.
	WindowDays int32 `json:"windowDays"`

	// RelatedPostMortems references previous PostMortems for the same pattern.
	// +optional
	RelatedPostMortems []string `json:"relatedPostMortems,omitempty"`

	// Pattern describes the recurring pattern.
	// +optional
	Pattern string `json:"pattern,omitempty"`
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

	// MetricSnapshots are metric values captured before, during, and after the incident.
	// +optional
	MetricSnapshots []MetricSnapshot `json:"metricSnapshots,omitempty"`

	// BlastRadius describes all services and resources impacted by the incident.
	// +optional
	BlastRadius []BlastRadiusEntry `json:"blastRadius,omitempty"`

	// GitCorrelation links the incident to suspected code changes.
	// +optional
	GitCorrelation *GitCorrelation `json:"gitCorrelation,omitempty"`

	// SLIImpact describes the impact on SLI error budgets.
	// +optional
	SLIImpact string `json:"sliImpact,omitempty"`

	// ErrorBudgetBurned is the percentage of error budget consumed by this incident.
	// +optional
	ErrorBudgetBurned float64 `json:"errorBudgetBurned,omitempty"`

	// Trending captures recurring incident pattern information.
	// +optional
	Trending *TrendingInfo `json:"trending,omitempty"`

	// Feedback is human feedback on this PostMortem.
	// +optional
	Feedback *DevFeedback `json:"feedback,omitempty"`

	// GitOpsContext describes Helm/ArgoCD/Flux state at the time of the incident.
	// +optional
	GitOpsContext string `json:"gitOpsContext,omitempty"`

	// LogAnalysisSummary is a summary of key findings from log analysis.
	// +optional
	LogAnalysisSummary string `json:"logAnalysisSummary,omitempty"`

	// CascadeChain describes the cascade failure chain if applicable.
	// +optional
	CascadeChain []string `json:"cascadeChain,omitempty"`
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
