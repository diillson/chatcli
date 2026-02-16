package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChatCLIInstanceSpec defines the desired state of a ChatCLI deployment.
type ChatCLIInstanceSpec struct {
	// Replicas is the number of ChatCLI server pods.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Provider is the LLM provider (OPENAI, CLAUDEAI, GOOGLEAI, XAI, STACKSPOT, OLLAMA).
	Provider string `json:"provider"`

	// Model is the LLM model name.
	// +optional
	Model string `json:"model,omitempty"`

	// Image defines the container image for ChatCLI.
	// +optional
	Image ImageSpec `json:"image,omitempty"`

	// Server configures the gRPC server.
	// +optional
	Server ServerSpec `json:"server,omitempty"`

	// Watcher configures the Kubernetes resource watcher.
	// +optional
	Watcher *WatcherSpec `json:"watcher,omitempty"`

	// Resources defines resource requests/limits for the ChatCLI container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Persistence configures persistent storage for sessions.
	// +optional
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// SecurityContext for the pod.
	// +optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`

	// APIKeys references a Secret containing provider API keys.
	// Expected keys: OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_AI_API_KEY, etc.
	// +optional
	APIKeys *SecretRefSpec `json:"apiKeys,omitempty"`
}

// ImageSpec defines the container image configuration.
type ImageSpec struct {
	// Repository is the container image repository.
	// +kubebuilder:default="ghcr.io/diillson/chatcli"
	Repository string `json:"repository,omitempty"`

	// Tag is the container image tag.
	// +kubebuilder:default="latest"
	Tag string `json:"tag,omitempty"`

	// PullPolicy defines the image pull policy.
	// +kubebuilder:default="IfNotPresent"
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// ServerSpec configures the gRPC server.
type ServerSpec struct {
	// Port is the gRPC server port.
	// +kubebuilder:default=50051
	Port int32 `json:"port,omitempty"`

	// MetricsPort is the Prometheus metrics HTTP port (0 = disabled).
	// +kubebuilder:default=9090
	// +optional
	MetricsPort int32 `json:"metricsPort,omitempty"`

	// TLS configures TLS for the gRPC server.
	// +optional
	TLS *TLSSpec `json:"tls,omitempty"`

	// Token references a Secret containing the auth token.
	// +optional
	Token *SecretKeyRefSpec `json:"token,omitempty"`
}

// TLSSpec configures TLS.
type TLSSpec struct {
	// Enabled enables TLS.
	Enabled bool `json:"enabled,omitempty"`

	// SecretName is the name of the Secret containing tls.crt and tls.key.
	SecretName string `json:"secretName,omitempty"`
}

// SecretKeyRefSpec references a specific key in a Secret.
type SecretKeyRefSpec struct {
	// Name is the Secret name.
	Name string `json:"name"`
	// Key is the key within the Secret.
	Key string `json:"key"`
}

// SecretRefSpec references a Secret by name.
type SecretRefSpec struct {
	// Name is the Secret name.
	Name string `json:"name"`
}

// WatcherSpec configures the Kubernetes resource watcher.
type WatcherSpec struct {
	// Enabled activates the watcher.
	Enabled bool `json:"enabled,omitempty"`

	// Deployment is the target deployment to watch (legacy single-target mode).
	// +optional
	Deployment string `json:"deployment,omitempty"`

	// Namespace is the namespace of the target deployment (legacy single-target mode).
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Targets defines multiple deployments to watch simultaneously.
	// When set, Deployment and Namespace fields are ignored.
	// +optional
	Targets []WatchTargetSpec `json:"targets,omitempty"`

	// Interval is the collection interval (e.g., "30s", "1m").
	// +kubebuilder:default="30s"
	Interval string `json:"interval,omitempty"`

	// Window is the observation window (e.g., "2h", "30m").
	// +kubebuilder:default="2h"
	Window string `json:"window,omitempty"`

	// MaxLogLines is the maximum number of log lines to collect per pod.
	// +kubebuilder:default=100
	MaxLogLines int32 `json:"maxLogLines,omitempty"`

	// MaxContextChars is the maximum characters for LLM context budget.
	// +kubebuilder:default=32000
	MaxContextChars int32 `json:"maxContextChars,omitempty"`
}

// WatchTargetSpec defines a single deployment target for multi-target watching.
type WatchTargetSpec struct {
	// Deployment is the deployment name to monitor.
	Deployment string `json:"deployment"`

	// Namespace is the namespace of the deployment.
	Namespace string `json:"namespace"`

	// MetricsPort is the port where Prometheus metrics are exposed (0 = disabled).
	// +optional
	MetricsPort int32 `json:"metricsPort,omitempty"`

	// MetricsPath is the HTTP path for Prometheus metrics (default: "/metrics").
	// +optional
	MetricsPath string `json:"metricsPath,omitempty"`

	// MetricsFilter is a list of glob patterns to filter Prometheus metrics.
	// +optional
	MetricsFilter []string `json:"metricsFilter,omitempty"`
}

// PersistenceSpec configures persistent storage.
type PersistenceSpec struct {
	// Enabled activates persistent storage.
	Enabled bool `json:"enabled,omitempty"`

	// Size is the PVC size (e.g., "1Gi").
	// +kubebuilder:default="1Gi"
	Size string `json:"size,omitempty"`

	// StorageClassName is the storage class to use.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// ChatCLIInstanceStatus defines the observed state of ChatCLIInstance.
type ChatCLIInstanceStatus struct {
	// Ready indicates whether all replicas are available.
	Ready bool `json:"ready"`

	// Replicas is the total number of desired replicas.
	Replicas int32 `json:"replicas"`

	// ReadyReplicas is the number of ready replicas.
	ReadyReplicas int32 `json:"readyReplicas"`

	// Conditions represent the latest available observations of the instance state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration tracks which generation was last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.replicas"
// +kubebuilder:printcolumn:name="Provider",type="string",JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ChatCLIInstance is the Schema for the chatcliinstances API.
type ChatCLIInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChatCLIInstanceSpec   `json:"spec,omitempty"`
	Status ChatCLIInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ChatCLIInstanceList contains a list of ChatCLIInstance.
type ChatCLIInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChatCLIInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChatCLIInstance{}, &ChatCLIInstanceList{})
}
