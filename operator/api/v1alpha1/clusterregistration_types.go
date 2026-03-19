package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ClusterRegistrationSpec defines the desired state of ClusterRegistration.
type ClusterRegistrationSpec struct {
	// DisplayName is a human-readable name for this cluster.
	DisplayName string `json:"displayName"`

	// KubeconfigSecretRef references a Secret containing the kubeconfig for this cluster.
	KubeconfigSecretRef SecretRefSpec `json:"kubeconfigSecretRef"`

	// Region where the cluster is deployed (e.g., "us-east-1", "eu-west-1").
	// +optional
	Region string `json:"region,omitempty"`

	// Environment classification (prod/staging/dev).
	// +kubebuilder:validation:Enum=prod;staging;dev
	// +kubebuilder:default="dev"
	Environment string `json:"environment"`

	// Tier determines remediation policies (critical/standard/non-critical).
	// +kubebuilder:validation:Enum=critical;standard;non-critical
	// +kubebuilder:default="standard"
	Tier string `json:"tier"`

	// Labels for custom metadata.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// HealthCheckInterval defines how often to check cluster connectivity (e.g., "30s", "1m").
	// +kubebuilder:default="30s"
	// +optional
	HealthCheckInterval string `json:"healthCheckInterval,omitempty"`

	// Capabilities lists available cluster features (e.g., "metrics-server", "prometheus", "istio").
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// MaxConcurrentRemediations limits parallel remediations in this cluster.
	// +kubebuilder:default=3
	// +optional
	MaxConcurrentRemediations int32 `json:"maxConcurrentRemediations,omitempty"`
}

// ClusterRegistrationStatus defines the observed state of ClusterRegistration.
type ClusterRegistrationStatus struct {
	// Connected indicates if the cluster is reachable.
	Connected bool `json:"connected"`

	// LastHealthCheck is when connectivity was last verified.
	// +optional
	LastHealthCheck *metav1.Time `json:"lastHealthCheck,omitempty"`

	// KubernetesVersion reported by the cluster.
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// NodeCount in the cluster.
	NodeCount int32 `json:"nodeCount"`

	// NamespaceCount in the cluster.
	NamespaceCount int32 `json:"namespaceCount"`

	// ActiveIssues across all namespaces in this cluster.
	ActiveIssues int32 `json:"activeIssues"`

	// ActiveRemediations in this cluster.
	ActiveRemediations int32 `json:"activeRemediations"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cr
// +kubebuilder:printcolumn:name="DisplayName",type="string",JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Region",type="string",JSONPath=".spec.region"
// +kubebuilder:printcolumn:name="Environment",type="string",JSONPath=".spec.environment"
// +kubebuilder:printcolumn:name="Connected",type="boolean",JSONPath=".status.connected"
// +kubebuilder:printcolumn:name="Nodes",type="integer",JSONPath=".status.nodeCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ClusterRegistration represents a registered Kubernetes cluster for multi-cluster federation.
type ClusterRegistration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterRegistrationSpec   `json:"spec,omitempty"`
	Status ClusterRegistrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterRegistrationList contains a list of ClusterRegistration.
type ClusterRegistrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRegistration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterRegistration{}, &ClusterRegistrationList{})
}
