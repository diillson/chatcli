package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ApprovalMode defines how approval decisions are made.
// +kubebuilder:validation:Enum=auto;manual;quorum
type ApprovalMode string

const (
	ApprovalModeAuto   ApprovalMode = "auto"
	ApprovalModeManual ApprovalMode = "manual"
	ApprovalModeQuorum ApprovalMode = "quorum"
)

// ApprovalRequestState defines the lifecycle state of an approval request.
// +kubebuilder:validation:Enum=Pending;Approved;Rejected;Expired
type ApprovalRequestState string

const (
	ApprovalStatePending  ApprovalRequestState = "Pending"
	ApprovalStateApproved ApprovalRequestState = "Approved"
	ApprovalStateRejected ApprovalRequestState = "Rejected"
	ApprovalStateExpired  ApprovalRequestState = "Expired"
)

// ApprovalMatch defines criteria for matching remediation actions to approval rules.
type ApprovalMatch struct {
	// Severities to match (empty = all).
	// +optional
	Severities []IssueSeverity `json:"severities,omitempty"`

	// ActionTypes to match (empty = all).
	// +optional
	ActionTypes []RemediationActionType `json:"actionTypes,omitempty"`

	// Namespaces to match (empty = all).
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// ResourceKinds to match (empty = all).
	// +optional
	ResourceKinds []string `json:"resourceKinds,omitempty"`
}

// ChangeWindowSpec defines when changes are allowed.
type ChangeWindowSpec struct {
	// Timezone for the change window (e.g., "America/Sao_Paulo").
	// +kubebuilder:default="UTC"
	Timezone string `json:"timezone"`

	// AllowedDays when changes are permitted.
	// +kubebuilder:default={"Monday","Tuesday","Wednesday","Thursday","Friday"}
	AllowedDays []string `json:"allowedDays"`

	// StartHour of the change window (0-23).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=23
	StartHour int32 `json:"startHour"`

	// EndHour of the change window (0-23).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=23
	EndHour int32 `json:"endHour"`
}

// AutoApproveConditions defines conditions for automatic approval.
type AutoApproveConditions struct {
	// MinConfidence from AIInsight required for auto-approval (0.0-1.0).
	MinConfidence float64 `json:"minConfidence"`

	// MaxSeverity — auto-approve only up to this severity level.
	MaxSeverity IssueSeverity `json:"maxSeverity"`

	// HistoricalSuccessRate required for auto-approval (0.0-1.0).
	HistoricalSuccessRate float64 `json:"historicalSuccessRate"`
}

// ApprovalRule defines a single approval requirement.
type ApprovalRule struct {
	// Name is a human-readable identifier.
	Name string `json:"name"`

	// Match criteria for when this rule applies.
	Match ApprovalMatch `json:"match"`

	// Mode determines how approval is handled.
	// +kubebuilder:default="manual"
	Mode ApprovalMode `json:"mode"`

	// RequiredApprovers for quorum mode.
	// +kubebuilder:default=1
	// +optional
	RequiredApprovers int32 `json:"requiredApprovers,omitempty"`

	// TimeoutMinutes before the request auto-expires.
	// +kubebuilder:default=30
	// +optional
	TimeoutMinutes int32 `json:"timeoutMinutes,omitempty"`

	// ChangeWindow restricts when changes can be executed.
	// +optional
	ChangeWindow *ChangeWindowSpec `json:"changeWindow,omitempty"`

	// AutoApproveConditions for auto mode — all conditions must be met.
	// +optional
	AutoApproveConditions *AutoApproveConditions `json:"autoApproveConditions,omitempty"`
}

// ApprovalPolicySpec defines the desired state of ApprovalPolicy.
type ApprovalPolicySpec struct {
	// Rules define approval requirements for different action/severity combinations.
	Rules []ApprovalRule `json:"rules"`

	// DefaultMode is used when no rule matches.
	// +kubebuilder:default="manual"
	// +optional
	DefaultMode string `json:"defaultMode,omitempty"`

	// Enabled activates this policy.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}

// ApprovalPolicyStatus defines the observed state of ApprovalPolicy.
type ApprovalPolicyStatus struct {
	// TotalApproved is the total approved requests.
	TotalApproved int64 `json:"totalApproved"`

	// TotalRejected is the total rejected requests.
	TotalRejected int64 `json:"totalRejected"`

	// TotalExpired is the total expired requests.
	TotalExpired int64 `json:"totalExpired"`

	// TotalAutoApproved is the total auto-approved requests.
	TotalAutoApproved int64 `json:"totalAutoApproved"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ap
// +kubebuilder:printcolumn:name="DefaultMode",type="string",JSONPath=".spec.defaultMode"
// +kubebuilder:printcolumn:name="Enabled",type="boolean",JSONPath=".spec.enabled"
// +kubebuilder:printcolumn:name="Approved",type="integer",JSONPath=".status.totalApproved"
// +kubebuilder:printcolumn:name="Rejected",type="integer",JSONPath=".status.totalRejected"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ApprovalPolicy defines approval requirements for remediation actions.
type ApprovalPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ApprovalPolicySpec   `json:"spec,omitempty"`
	Status ApprovalPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ApprovalPolicyList contains a list of ApprovalPolicy.
type ApprovalPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApprovalPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApprovalPolicy{}, &ApprovalPolicyList{})
}
