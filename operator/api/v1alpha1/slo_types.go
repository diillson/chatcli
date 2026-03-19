package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SLOIndicatorType defines the type of service level indicator.
// +kubebuilder:validation:Enum=availability;latency;error_rate;throughput
type SLOIndicatorType string

const (
	SLOIndicatorAvailability SLOIndicatorType = "availability"
	SLOIndicatorLatency      SLOIndicatorType = "latency"
	SLOIndicatorErrorRate    SLOIndicatorType = "error_rate"
	SLOIndicatorThroughput   SLOIndicatorType = "throughput"
)

// SLOMetricSource defines where the SLI data comes from.
// +kubebuilder:validation:Enum=prometheus;watcher;issues
type SLOMetricSource string

const (
	SLOSourcePrometheus SLOMetricSource = "prometheus"
	SLOSourceWatcher    SLOMetricSource = "watcher"
	SLOSourceIssues     SLOMetricSource = "issues"
)

// SLOIndicator defines the service level indicator measurement.
type SLOIndicator struct {
	// Type of indicator.
	Type SLOIndicatorType `json:"type"`

	// MetricSource defines where to get the metric data.
	// +kubebuilder:default="issues"
	MetricSource SLOMetricSource `json:"metricSource"`

	// PrometheusQuery is a PromQL query for custom metrics (used when metricSource=prometheus).
	// +optional
	PrometheusQuery string `json:"prometheusQuery,omitempty"`

	// Resource is the target resource to measure (used when metricSource=issues or watcher).
	// +optional
	Resource *ResourceRef `json:"resource,omitempty"`

	// LatencyPercentile specifies which percentile to measure (p50, p95, p99).
	// Only applicable when type=latency.
	// +optional
	LatencyPercentile string `json:"latencyPercentile,omitempty"`

	// LatencyThresholdMs is the latency threshold in milliseconds.
	// Only applicable when type=latency.
	// +optional
	LatencyThresholdMs int64 `json:"latencyThresholdMs,omitempty"`
}

// SLOTarget defines the objective target.
type SLOTarget struct {
	// Percentage is the SLO target (e.g., 99.9 for 99.9%).
	Percentage float64 `json:"percentage"`

	// Window is the measurement window (e.g., "7d", "30d", "90d").
	// +kubebuilder:default="30d"
	Window string `json:"window"`
}

// BurnRateWindow defines a multi-window burn rate alert configuration.
type BurnRateWindow struct {
	// ShortWindow for fast detection (e.g., "1h").
	ShortWindow string `json:"shortWindow"`

	// LongWindow for confirmation (e.g., "6h").
	LongWindow string `json:"longWindow"`

	// BurnRateThreshold triggers alert when burn rate exceeds this in both windows.
	BurnRateThreshold float64 `json:"burnRateThreshold"`

	// Severity of the alert when this window fires.
	Severity IssueSeverity `json:"severity"`
}

// SLOAlertPolicy defines burn rate alerting configuration.
type SLOAlertPolicy struct {
	// BurnRateWindows defines multi-window burn rate alerts.
	// +optional
	BurnRateWindows []BurnRateWindow `json:"burnRateWindows,omitempty"`

	// PageOnBudgetExhausted creates a critical issue when error budget is fully consumed.
	// +optional
	PageOnBudgetExhausted bool `json:"pageOnBudgetExhausted,omitempty"`

	// NotificationPolicyRef references a NotificationPolicy to use for SLO alerts.
	// +optional
	NotificationPolicyRef string `json:"notificationPolicyRef,omitempty"`
}

// ServiceLevelObjectiveSpec defines the desired state of ServiceLevelObjective.
type ServiceLevelObjectiveSpec struct {
	// ServiceName is the service this SLO belongs to.
	ServiceName string `json:"serviceName"`

	// Description explains this SLO.
	// +optional
	Description string `json:"description,omitempty"`

	// Indicator defines what to measure.
	Indicator SLOIndicator `json:"indicator"`

	// Target defines the objective.
	Target SLOTarget `json:"target"`

	// AlertPolicy defines burn rate alerting.
	// +optional
	AlertPolicy SLOAlertPolicy `json:"alertPolicy,omitempty"`

	// Enabled activates this SLO.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`
}

// SLOAlert represents an active burn rate alert.
type SLOAlert struct {
	// Window that triggered (e.g., "1h/6h").
	Window string `json:"window"`

	// BurnRate observed.
	BurnRate float64 `json:"burnRate"`

	// Severity of this alert.
	Severity IssueSeverity `json:"severity"`

	// FiredAt is when the alert fired.
	FiredAt metav1.Time `json:"firedAt"`
}

// ServiceLevelObjectiveStatus defines the observed state of ServiceLevelObjective.
type ServiceLevelObjectiveStatus struct {
	// CurrentValue is the current SLI value (e.g., 0.9987 for 99.87% availability).
	CurrentValue float64 `json:"currentValue"`

	// TargetMet indicates if the SLO target is currently being met.
	TargetMet bool `json:"targetMet"`

	// ErrorBudgetTotal is the total allowed error rate (1 - target, e.g., 0.001 for 99.9%).
	ErrorBudgetTotal float64 `json:"errorBudgetTotal"`

	// ErrorBudgetRemaining is the remaining budget as a fraction (0.0-1.0).
	ErrorBudgetRemaining float64 `json:"errorBudgetRemaining"`

	// ErrorBudgetConsumedPercentage is 0-100%.
	ErrorBudgetConsumedPercentage float64 `json:"errorBudgetConsumedPercentage"`

	// BurnRate1h is the burn rate over the last 1 hour.
	BurnRate1h float64 `json:"burnRate1h"`

	// BurnRate6h is the burn rate over the last 6 hours.
	BurnRate6h float64 `json:"burnRate6h"`

	// BurnRate24h is the burn rate over the last 24 hours.
	BurnRate24h float64 `json:"burnRate24h"`

	// BurnRate72h is the burn rate over the last 72 hours.
	BurnRate72h float64 `json:"burnRate72h"`

	// LastCalculatedAt is when the SLO was last recalculated.
	// +optional
	LastCalculatedAt *metav1.Time `json:"lastCalculatedAt,omitempty"`

	// ActiveAlerts tracks currently firing burn rate alerts.
	// +optional
	ActiveAlerts []SLOAlert `json:"activeAlerts,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=slo
// +kubebuilder:printcolumn:name="Service",type="string",JSONPath=".spec.serviceName"
// +kubebuilder:printcolumn:name="Target%",type="number",JSONPath=".spec.target.percentage"
// +kubebuilder:printcolumn:name="Current",type="number",JSONPath=".status.currentValue"
// +kubebuilder:printcolumn:name="BudgetRemaining%",type="number",JSONPath=".status.errorBudgetConsumedPercentage"
// +kubebuilder:printcolumn:name="BurnRate1h",type="number",JSONPath=".status.burnRate1h"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ServiceLevelObjective defines a service level objective with burn rate alerting.
type ServiceLevelObjective struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceLevelObjectiveSpec   `json:"spec,omitempty"`
	Status ServiceLevelObjectiveStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServiceLevelObjectiveList contains a list of ServiceLevelObjective.
type ServiceLevelObjectiveList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceLevelObjective `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServiceLevelObjective{}, &ServiceLevelObjectiveList{})
}
