package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// EscalationTargetType defines the type of escalation target.
// +kubebuilder:validation:Enum=channel;user;team;oncall
type EscalationTargetType string

const (
	EscalationTargetChannel EscalationTargetType = "channel"
	EscalationTargetUser    EscalationTargetType = "user"
	EscalationTargetTeam    EscalationTargetType = "team"
	EscalationTargetOnCall  EscalationTargetType = "oncall"
)

// EscalationTarget defines who to notify at a given escalation level.
type EscalationTarget struct {
	// Type of target.
	Type EscalationTargetType `json:"type"`

	// Name of the target (user email, team name, channel name, or oncall schedule).
	Name string `json:"name"`
}

// EscalationLevel defines a single level in the escalation chain.
type EscalationLevel struct {
	// Name is a human-readable identifier (e.g., "L1-OnCall", "L2-SeniorSRE", "L3-Management").
	Name string `json:"name"`

	// TimeoutMinutes is the time to wait before escalating to the next level.
	// +kubebuilder:default=15
	TimeoutMinutes int32 `json:"timeoutMinutes"`

	// Targets defines who to notify at this level.
	Targets []EscalationTarget `json:"targets"`

	// NotifyChannels lists notification channel names (from NotificationPolicy) to use.
	// +optional
	NotifyChannels []string `json:"notifyChannels,omitempty"`

	// RepeatIntervalMinutes re-notifies at this interval until acknowledged or escalated.
	// 0 = no repeat.
	// +kubebuilder:default=0
	// +optional
	RepeatIntervalMinutes int32 `json:"repeatIntervalMinutes,omitempty"`
}

// EscalationPolicySpec defines the desired state of EscalationPolicy.
type EscalationPolicySpec struct {
	// Levels defines the escalation chain from L1 to LN.
	Levels []EscalationLevel `json:"levels"`

	// DefaultPolicy marks this as the default policy when no specific policy matches.
	// +optional
	DefaultPolicy bool `json:"defaultPolicy,omitempty"`

	// Severities this policy applies to (empty = all).
	// +optional
	Severities []IssueSeverity `json:"severities,omitempty"`

	// Enabled activates this policy.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}

// ActiveEscalation tracks an ongoing escalation for an issue.
type ActiveEscalation struct {
	// IssueName is the name of the escalated issue.
	IssueName string `json:"issueName"`

	// CurrentLevel is the current escalation level index (0-based).
	CurrentLevel int32 `json:"currentLevel"`

	// EscalatedAt is when the current level was reached.
	EscalatedAt metav1.Time `json:"escalatedAt"`

	// AcknowledgedAt is when the escalation was acknowledged (nil if not).
	// +optional
	AcknowledgedAt *metav1.Time `json:"acknowledgedAt,omitempty"`

	// AcknowledgedBy identifies who acknowledged.
	// +optional
	AcknowledgedBy string `json:"acknowledgedBy,omitempty"`
}

// EscalationPolicyStatus defines the observed state of EscalationPolicy.
type EscalationPolicyStatus struct {
	// ActiveEscalations tracks ongoing escalations.
	// +optional
	ActiveEscalations []ActiveEscalation `json:"activeEscalations,omitempty"`

	// TotalEscalations is the total number of escalations processed.
	TotalEscalations int64 `json:"totalEscalations"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ep
// +kubebuilder:printcolumn:name="Levels",type="integer",JSONPath=".spec.levels"
// +kubebuilder:printcolumn:name="Default",type="boolean",JSONPath=".spec.defaultPolicy"
// +kubebuilder:printcolumn:name="Enabled",type="boolean",JSONPath=".spec.enabled"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EscalationPolicy defines escalation chains for incident management.
type EscalationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EscalationPolicySpec   `json:"spec,omitempty"`
	Status EscalationPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EscalationPolicyList contains a list of EscalationPolicy.
type EscalationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EscalationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EscalationPolicy{}, &EscalationPolicyList{})
}
