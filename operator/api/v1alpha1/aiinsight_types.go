package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AIInsightSpec defines the desired state of an AIInsight.
type AIInsightSpec struct {
	// IssueRef references the parent Issue.
	IssueRef IssueRef `json:"issueRef"`

	// Provider is the LLM provider used for analysis.
	Provider string `json:"provider"`

	// Model is the LLM model used for analysis.
	Model string `json:"model"`
}

// SuggestedAction represents a concrete remediation action suggested by the AI.
type SuggestedAction struct {
	// Name is a human-readable step name.
	Name string `json:"name"`

	// Action is the remediation action type (ScaleDeployment, RestartDeployment, RollbackDeployment, PatchConfig).
	Action string `json:"action"`

	// Description explains why this action is recommended.
	// +optional
	Description string `json:"description,omitempty"`

	// Params are action-specific parameters.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// AIInsightStatus defines the observed state of an AIInsight.
type AIInsightStatus struct {
	// Analysis is the AI-generated root cause analysis.
	// +optional
	Analysis string `json:"analysis,omitempty"`

	// Confidence is the AI's confidence in its analysis (0.0-1.0).
	// +optional
	Confidence float64 `json:"confidence,omitempty"`

	// Recommendations are AI-suggested actions (human-readable).
	// +optional
	Recommendations []string `json:"recommendations,omitempty"`

	// SuggestedActions are concrete remediation actions suggested by the AI.
	// +optional
	SuggestedActions []SuggestedAction `json:"suggestedActions,omitempty"`

	// GeneratedAt is when the analysis was generated.
	// +optional
	GeneratedAt *metav1.Time `json:"generatedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ai
// +kubebuilder:printcolumn:name="Issue",type="string",JSONPath=".spec.issueRef.name"
// +kubebuilder:printcolumn:name="Provider",type="string",JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Confidence",type="number",JSONPath=".status.confidence"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AIInsight contains AI-generated analysis and recommendations.
type AIInsight struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AIInsightSpec   `json:"spec,omitempty"`
	Status AIInsightStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AIInsightList contains a list of AIInsight.
type AIInsightList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIInsight `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AIInsight{}, &AIInsightList{})
}
