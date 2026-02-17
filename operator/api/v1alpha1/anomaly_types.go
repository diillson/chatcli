package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AnomalySignalType classifies the kind of anomaly signal.
// +kubebuilder:validation:Enum=error_rate;latency;pod_restart;cpu_high;memory_high;oom_kill;pod_not_ready;deploy_failing
type AnomalySignalType string

const (
	SignalErrorRate   AnomalySignalType = "error_rate"
	SignalLatency     AnomalySignalType = "latency"
	SignalPodRestart  AnomalySignalType = "pod_restart"
	SignalCPUHigh     AnomalySignalType = "cpu_high"
	SignalMemoryHigh  AnomalySignalType = "memory_high"
	SignalOOMKill     AnomalySignalType = "oom_kill"
	SignalPodNotReady AnomalySignalType = "pod_not_ready"
	SignalDeployFail  AnomalySignalType = "deploy_failing"
)

// AnomalySource is the origin system.
// +kubebuilder:validation:Enum=prometheus;events;logs;webhook;watcher
type AnomalySource string

const (
	AnomalySourcePrometheus AnomalySource = "prometheus"
	AnomalySourceEvents     AnomalySource = "events"
	AnomalySourceLogs       AnomalySource = "logs"
	AnomalySourceWebhook    AnomalySource = "webhook"
	AnomalySourceWatcher    AnomalySource = "watcher"
)

// AnomalySpec defines the desired state of an Anomaly.
type AnomalySpec struct {
	// Source that detected the anomaly.
	Source AnomalySource `json:"source"`

	// SignalType classifies the anomaly.
	SignalType AnomalySignalType `json:"signalType"`

	// Resource affected by the anomaly.
	Resource ResourceRef `json:"resource"`

	// Value is the observed metric value.
	Value string `json:"value"`

	// Threshold is the expected normal value.
	Threshold string `json:"threshold"`

	// Description of the anomaly.
	// +optional
	Description string `json:"description,omitempty"`
}

// AnomalyStatus defines the observed state of an Anomaly.
type AnomalyStatus struct {
	// Correlated indicates whether this anomaly has been grouped into an Issue.
	Correlated bool `json:"correlated"`

	// IssueRef references the Issue this anomaly was correlated to.
	// +optional
	IssueRef *IssueRef `json:"issueRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=anom
// +kubebuilder:printcolumn:name="Source",type="string",JSONPath=".spec.source"
// +kubebuilder:printcolumn:name="Signal",type="string",JSONPath=".spec.signalType"
// +kubebuilder:printcolumn:name="Correlated",type="boolean",JSONPath=".status.correlated"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Anomaly is a raw signal from watchers before correlation.
type Anomaly struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AnomalySpec   `json:"spec,omitempty"`
	Status AnomalyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AnomalyList contains a list of Anomaly.
type AnomalyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Anomaly `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Anomaly{}, &AnomalyList{})
}
