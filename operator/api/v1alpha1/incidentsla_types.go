package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// BusinessHoursSpec defines business hours for SLA clock calculation.
type BusinessHoursSpec struct {
	// Timezone for business hours (e.g., "America/Sao_Paulo", "UTC").
	// +kubebuilder:default="UTC"
	Timezone string `json:"timezone"`

	// StartHour is the start of business hours (0-23).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=23
	// +kubebuilder:default=9
	StartHour int32 `json:"startHour"`

	// EndHour is the end of business hours (0-23).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=23
	// +kubebuilder:default=18
	EndHour int32 `json:"endHour"`

	// WorkDays are the days when the SLA clock runs.
	// +kubebuilder:default={"Monday","Tuesday","Wednesday","Thursday","Friday"}
	WorkDays []string `json:"workDays"`
}

// IncidentSLASpec defines the desired state of IncidentSLA.
type IncidentSLASpec struct {
	// Severity this SLA applies to.
	Severity IssueSeverity `json:"severity"`

	// ResponseTime is the maximum allowed time from detection to first analysis (e.g., "5m", "15m", "1h").
	ResponseTime string `json:"responseTime"`

	// ResolutionTime is the maximum allowed time from detection to resolution (e.g., "1h", "4h", "24h").
	ResolutionTime string `json:"resolutionTime"`

	// EscalationPolicyRef references an EscalationPolicy to trigger on SLA breach.
	// +optional
	EscalationPolicyRef string `json:"escalationPolicyRef,omitempty"`

	// NotificationPolicyRef references a NotificationPolicy for SLA breach notifications.
	// +optional
	NotificationPolicyRef string `json:"notificationPolicyRef,omitempty"`

	// BusinessHoursOnly pauses the SLA clock outside business hours.
	// +optional
	BusinessHoursOnly bool `json:"businessHoursOnly,omitempty"`

	// BusinessHours defines the business hours window.
	// +optional
	BusinessHours *BusinessHoursSpec `json:"businessHours,omitempty"`
}

// SLAViolationRecord tracks a single SLA violation.
type SLAViolationRecord struct {
	// IssueName is the violating issue.
	IssueName string `json:"issueName"`

	// Type is "response" or "resolution".
	Type string `json:"type"`

	// Elapsed is the actual duration before the SLA was breached.
	Elapsed string `json:"elapsed"`

	// Threshold is the SLA threshold that was breached.
	Threshold string `json:"threshold"`

	// ViolatedAt is when the breach was detected.
	ViolatedAt metav1.Time `json:"violatedAt"`
}

// IncidentSLAStatus defines the observed state of IncidentSLA.
type IncidentSLAStatus struct {
	// ActiveViolations is the number of currently open SLA violations.
	ActiveViolations int32 `json:"activeViolations"`

	// TotalViolations is the total number of SLA violations.
	TotalViolations int64 `json:"totalViolations"`

	// CompliancePercentage is the SLA compliance rate (0-100).
	CompliancePercentage float64 `json:"compliancePercentage"`

	// TotalIssuesTracked is the total number of issues tracked by this SLA.
	TotalIssuesTracked int64 `json:"totalIssuesTracked"`

	// AverageResponseTime is the average time to first analysis.
	// +optional
	AverageResponseTime string `json:"averageResponseTime,omitempty"`

	// AverageResolutionTime is the average time to resolution.
	// +optional
	AverageResolutionTime string `json:"averageResolutionTime,omitempty"`

	// RecentViolations tracks the last 50 violations.
	// +optional
	RecentViolations []SLAViolationRecord `json:"recentViolations,omitempty"`

	// LastViolationAt is when the last SLA breach occurred.
	// +optional
	LastViolationAt *metav1.Time `json:"lastViolationAt,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sla
// +kubebuilder:printcolumn:name="Severity",type="string",JSONPath=".spec.severity"
// +kubebuilder:printcolumn:name="ResponseTime",type="string",JSONPath=".spec.responseTime"
// +kubebuilder:printcolumn:name="ResolutionTime",type="string",JSONPath=".spec.resolutionTime"
// +kubebuilder:printcolumn:name="Compliance%",type="number",JSONPath=".status.compliancePercentage"
// +kubebuilder:printcolumn:name="Violations",type="integer",JSONPath=".status.totalViolations"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// IncidentSLA defines SLA targets for incident response and resolution by severity.
type IncidentSLA struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IncidentSLASpec   `json:"spec,omitempty"`
	Status IncidentSLAStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IncidentSLAList contains a list of IncidentSLA.
type IncidentSLAList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IncidentSLA `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IncidentSLA{}, &IncidentSLAList{})
}
