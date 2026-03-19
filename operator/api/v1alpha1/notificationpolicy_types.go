package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// NotificationChannelType defines the type of notification channel.
// +kubebuilder:validation:Enum=slack;pagerduty;opsgenie;email;webhook;teams
type NotificationChannelType string

const (
	ChannelSlack     NotificationChannelType = "slack"
	ChannelPagerDuty NotificationChannelType = "pagerduty"
	ChannelOpsGenie  NotificationChannelType = "opsgenie"
	ChannelEmail     NotificationChannelType = "email"
	ChannelWebhook   NotificationChannelType = "webhook"
	ChannelTeams     NotificationChannelType = "teams"
)

// NotificationChannel defines a notification delivery channel.
type NotificationChannel struct {
	// Name is a unique identifier for this channel.
	Name string `json:"name"`

	// Type of notification channel.
	Type NotificationChannelType `json:"type"`

	// Config holds channel-specific configuration.
	// Slack: webhook_url, channel, username
	// PagerDuty: routing_key, severity_map
	// OpsGenie: api_key, responders, tags
	// Email: smtp_host, smtp_port, smtp_user, smtp_password, from, to, tls_skip_verify
	// Webhook: url, method, headers, secret
	// Teams: webhook_url
	Config map[string]string `json:"config"`

	// SecretRef references a Secret containing sensitive config values (api_key, password, etc.).
	// Keys from the secret override keys in Config.
	// +optional
	SecretRef *SecretRefSpec `json:"secretRef,omitempty"`
}

// NotificationRule defines matching criteria for when to send notifications.
type NotificationRule struct {
	// Name is a human-readable identifier for this rule.
	Name string `json:"name"`

	// Severities to match (empty = all).
	// +optional
	Severities []IssueSeverity `json:"severities,omitempty"`

	// SignalTypes to match (empty = all).
	// +optional
	SignalTypes []string `json:"signalTypes,omitempty"`

	// Namespaces to match (empty = all).
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// ResourceKinds to match (empty = all).
	// +optional
	ResourceKinds []string `json:"resourceKinds,omitempty"`

	// States to match — which issue states trigger a notification.
	// +optional
	States []IssueState `json:"states,omitempty"`

	// Channels lists channel names (from Spec.Channels) to send to when matched.
	Channels []string `json:"channels"`
}

// ThrottleConfig controls notification rate limiting.
type ThrottleConfig struct {
	// MaxPerHour limits the number of notifications per hour per issue.
	// +kubebuilder:default=10
	// +optional
	MaxPerHour int32 `json:"maxPerHour,omitempty"`

	// DeduplicationWindow suppresses duplicate notifications within this window (e.g., "5m", "15m").
	// +kubebuilder:default="5m"
	// +optional
	DeduplicationWindow string `json:"deduplicationWindow,omitempty"`

	// GroupingWindow groups related notifications within this window into a single message.
	// +kubebuilder:default="1m"
	// +optional
	GroupingWindow string `json:"groupingWindow,omitempty"`
}

// NotificationPolicySpec defines the desired state of NotificationPolicy.
type NotificationPolicySpec struct {
	// Channels available for notification delivery.
	Channels []NotificationChannel `json:"channels"`

	// Rules define when and where to send notifications.
	Rules []NotificationRule `json:"rules"`

	// Throttle controls rate limiting.
	// +optional
	Throttle ThrottleConfig `json:"throttle,omitempty"`

	// Templates are custom Go text/template strings keyed by event type.
	// Supported keys: issue_created, issue_resolved, issue_escalated, remediation_started,
	// remediation_completed, remediation_failed.
	// +optional
	Templates map[string]string `json:"templates,omitempty"`

	// Enabled activates this policy.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}

// NotificationDeliveryRecord tracks a single notification delivery.
type NotificationDeliveryRecord struct {
	// Channel name.
	Channel string `json:"channel"`

	// SentAt timestamp.
	SentAt metav1.Time `json:"sentAt"`

	// Success indicates if delivery succeeded.
	Success bool `json:"success"`

	// Error message if delivery failed.
	// +optional
	Error string `json:"error,omitempty"`
}

// NotificationPolicyStatus defines the observed state of NotificationPolicy.
type NotificationPolicyStatus struct {
	// LastNotifiedAt is when the last notification was sent.
	// +optional
	LastNotifiedAt *metav1.Time `json:"lastNotifiedAt,omitempty"`

	// TotalSent is the total number of notifications sent.
	TotalSent int64 `json:"totalSent"`

	// FailedCount is the total number of failed deliveries.
	FailedCount int64 `json:"failedCount"`

	// RecentDeliveries tracks the last 20 delivery attempts.
	// +optional
	RecentDeliveries []NotificationDeliveryRecord `json:"recentDeliveries,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=np
// +kubebuilder:printcolumn:name="Enabled",type="boolean",JSONPath=".spec.enabled"
// +kubebuilder:printcolumn:name="Channels",type="integer",JSONPath=".spec.channels"
// +kubebuilder:printcolumn:name="Sent",type="integer",JSONPath=".status.totalSent"
// +kubebuilder:printcolumn:name="Failed",type="integer",JSONPath=".status.failedCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NotificationPolicy defines notification delivery rules and channels.
type NotificationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NotificationPolicySpec   `json:"spec,omitempty"`
	Status NotificationPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NotificationPolicyList contains a list of NotificationPolicy.
type NotificationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NotificationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NotificationPolicy{}, &NotificationPolicyList{})
}
