package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AuditActor identifies who performed an action.
type AuditActor struct {
	// Type of actor (system/user/controller).
	Type string `json:"type"`

	// Name identifies the actor.
	Name string `json:"name"`

	// Controller identifies which controller generated this event.
	// +optional
	Controller string `json:"controller,omitempty"`
}

// AuditResource identifies the affected resource.
type AuditResource struct {
	// Kind of the resource.
	Kind string `json:"kind"`

	// Name of the resource.
	Name string `json:"name"`

	// Namespace of the resource.
	Namespace string `json:"namespace"`

	// UID of the resource.
	// +optional
	UID string `json:"uid,omitempty"`
}

// AuditEventSpec defines the immutable audit event record.
type AuditEventSpec struct {
	// EventType classifies the event.
	// Supported: issue_created, issue_resolved, issue_escalated, remediation_started,
	// remediation_completed, remediation_failed, approval_requested, approval_granted,
	// approval_rejected, approval_expired, notification_sent, slo_violation, sla_breach,
	// pattern_learned, config_changed, cluster_connected, cluster_disconnected,
	// escalation_triggered, postmortem_created, runbook_generated.
	EventType string `json:"eventType"`

	// Actor who performed the action.
	Actor AuditActor `json:"actor"`

	// Resource affected by the action.
	Resource AuditResource `json:"resource"`

	// Details contains event-specific key-value pairs.
	// +optional
	Details map[string]string `json:"details,omitempty"`

	// Severity of the audit event (info/warning/critical).
	// +kubebuilder:default="info"
	Severity string `json:"severity"`

	// CorrelationID links related audit events (e.g., incident ID).
	// +optional
	CorrelationID string `json:"correlationId,omitempty"`

	// Timestamp of when the event occurred.
	Timestamp metav1.Time `json:"timestamp"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=ae
// +kubebuilder:printcolumn:name="EventType",type="string",JSONPath=".spec.eventType"
// +kubebuilder:printcolumn:name="Actor",type="string",JSONPath=".spec.actor.name"
// +kubebuilder:printcolumn:name="Resource",type="string",JSONPath=".spec.resource.name"
// +kubebuilder:printcolumn:name="Severity",type="string",JSONPath=".spec.severity"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AuditEvent is an immutable record of an action in the AIOps platform.
// AuditEvents are append-only and should not be modified after creation.
type AuditEvent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AuditEventSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// AuditEventList contains a list of AuditEvent.
type AuditEventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuditEvent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuditEvent{}, &AuditEventList{})
}
