package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// IssueSeverity represents the severity level of an issue.
// +kubebuilder:validation:Enum=critical;high;medium;low
type IssueSeverity string

const (
	IssueSeverityCritical IssueSeverity = "critical"
	IssueSeverityHigh     IssueSeverity = "high"
	IssueSeverityMedium   IssueSeverity = "medium"
	IssueSeverityLow      IssueSeverity = "low"
)

// IssueState represents the lifecycle state of an issue.
// +kubebuilder:validation:Enum=Detected;Analyzing;Remediating;Resolved;Escalated;Failed
type IssueState string

const (
	IssueStateDetected    IssueState = "Detected"
	IssueStateAnalyzing   IssueState = "Analyzing"
	IssueStateRemediating IssueState = "Remediating"
	IssueStateResolved    IssueState = "Resolved"
	IssueStateEscalated   IssueState = "Escalated"
	IssueStateFailed      IssueState = "Failed"
)

// IssueSource represents where the issue signal originated.
// +kubebuilder:validation:Enum=prometheus;events;logs;webhook;watcher
type IssueSource string

const (
	IssueSourcePrometheus IssueSource = "prometheus"
	IssueSourceEvents     IssueSource = "events"
	IssueSourceLogs       IssueSource = "logs"
	IssueSourceWebhook    IssueSource = "webhook"
	IssueSourceWatcher    IssueSource = "watcher"
)

// ResourceRef identifies a Kubernetes resource.
type ResourceRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// IssueRef is a reference to an Issue by name.
type IssueRef struct {
	Name string `json:"name"`
}

// IssueSpec defines the desired state of an Issue.
type IssueSpec struct {
	// Severity of the issue.
	Severity IssueSeverity `json:"severity"`

	// Source that detected the issue.
	Source IssueSource `json:"source"`

	// Resource affected by the issue.
	Resource ResourceRef `json:"resource"`

	// Description of the issue.
	Description string `json:"description"`

	// CorrelationId links correlated anomaly signals.
	// +optional
	CorrelationId string `json:"correlationId,omitempty"`

	// RiskScore is an AI-calculated risk score (0-100).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	RiskScore int32 `json:"riskScore,omitempty"`
}

// IssueStatus defines the observed state of an Issue.
type IssueStatus struct {
	// State is the current lifecycle state.
	State IssueState `json:"state"`

	// DetectedAt is when the issue was first detected.
	// +optional
	DetectedAt *metav1.Time `json:"detectedAt,omitempty"`

	// Resolution describes how the issue was resolved.
	// +optional
	Resolution string `json:"resolution,omitempty"`

	// ResolvedAt is when the issue was resolved.
	// +optional
	ResolvedAt *metav1.Time `json:"resolvedAt,omitempty"`

	// RemediationAttempts is the number of remediation attempts made.
	RemediationAttempts int32 `json:"remediationAttempts"`

	// MaxRemediationAttempts is the maximum number of remediation attempts allowed.
	// +kubebuilder:default=3
	MaxRemediationAttempts int32 `json:"maxRemediationAttempts"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=iss
// +kubebuilder:printcolumn:name="Severity",type="string",JSONPath=".spec.severity"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Risk",type="integer",JSONPath=".spec.riskScore"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Issue represents a detected problem in the cluster.
type Issue struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IssueSpec   `json:"spec,omitempty"`
	Status IssueStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IssueList contains a list of Issue.
type IssueList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Issue `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Issue{}, &IssueList{})
}
