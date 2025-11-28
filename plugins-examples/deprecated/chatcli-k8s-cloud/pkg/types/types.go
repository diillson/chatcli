package types

import "time"

// Metadata do plugin (contrato com ChatCLI)
type PluginMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

// ClusterConfig é a configuração completa de um cluster
type ClusterConfig struct {
	// Identificação
	Name        string `json:"name" yaml:"name"`
	Provider    string `json:"provider" yaml:"provider"` // aws, azure, gcp
	Region      string `json:"region" yaml:"region"`
	Environment string `json:"environment" yaml:"environment"` // production, development, staging

	// Networking
	CreateVPC         bool     `json:"createVpc" yaml:"createVpc"`
	VPCCidr           string   `json:"vpcCidr" yaml:"vpcCidr"`
	ExistingVPCID     string   `json:"existingVpcId,omitempty" yaml:"existingVpcId,omitempty"`
	ExistingSubnetIDs []string `json:"existingSubnetIds,omitempty" yaml:"existingSubnetIds,omitempty"`
	AvailabilityZones int      `json:"availabilityZones" yaml:"availabilityZones"`

	// Kubernetes
	K8sVersion string `json:"k8sVersion" yaml:"k8sVersion"`

	// Nodes
	NodeConfig NodeConfig `json:"nodeConfig" yaml:"nodeConfig"`

	// Add-ons
	Addons AddonConfig `json:"addons" yaml:"addons"`

	// State
	StateBackend StateBackendConfig `json:"stateBackend" yaml:"stateBackend"`

	// Metadata
	Tags      map[string]string `json:"tags" yaml:"tags"`
	CreatedAt time.Time         `json:"createdAt" yaml:"createdAt"`
	CreatedBy string            `json:"createdBy" yaml:"createdBy"`
}

// NodeConfig configuração dos worker nodes
type NodeConfig struct {
	InstanceType string `json:"instanceType" yaml:"instanceType"`
	MinSize      int    `json:"minSize" yaml:"minSize"`
	MaxSize      int    `json:"maxSize" yaml:"maxSize"`
	DesiredSize  int    `json:"desiredSize" yaml:"desiredSize"`
	DiskSize     int    `json:"diskSize" yaml:"diskSize"` // GB

	// Opções avançadas
	SpotInstances bool              `json:"spotInstances" yaml:"spotInstances"`
	Labels        map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Taints        []string          `json:"taints,omitempty" yaml:"taints,omitempty"`
}

// AddonConfig configuração de add-ons
type AddonConfig struct {
	Istio        *IstioConfig        `json:"istio,omitempty" yaml:"istio,omitempty"`
	NginxIngress *NginxIngressConfig `json:"nginxIngress,omitempty" yaml:"nginxIngress,omitempty"`
	ArgoCD       *ArgoCDConfig       `json:"argocd,omitempty" yaml:"argocd,omitempty"`
	Prometheus   *PrometheusConfig   `json:"prometheus,omitempty" yaml:"prometheus,omitempty"`
	CertManager  *CertManagerConfig  `json:"certManager,omitempty" yaml:"certManager,omitempty"`
}

type IstioConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Version string `json:"version" yaml:"version"`
	Profile string `json:"profile" yaml:"profile"` // demo, default, minimal
}

type NginxIngressConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

type ArgoCDConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

type PrometheusConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

type CertManagerConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// StateBackendConfig configuração do backend de estado
type StateBackendConfig struct {
	Type          string `json:"type" yaml:"type"` // s3, azblob
	URL           string `json:"url" yaml:"url"`   // s3://bucket/path
	Region        string `json:"region" yaml:"region"`
	LockTableName string `json:"lockTableName,omitempty" yaml:"lockTableName,omitempty"`

	// Segurança
	Encryption string `json:"encryption" yaml:"encryption"` // AES256, aws:kms
	KMSKeyID   string `json:"kmsKeyId,omitempty" yaml:"kmsKeyId,omitempty"`
}

// ClusterState representa o estado atual de um cluster
type ClusterState struct {
	Config    ClusterConfig          `json:"config" yaml:"config"`
	Status    ClusterStatus          `json:"status" yaml:"status"`
	Resources map[string]interface{} `json:"resources" yaml:"resources"`
	UpdatedAt time.Time              `json:"updatedAt" yaml:"updatedAt"`
}

// ClusterStatus status atual do cluster
type ClusterStatus struct {
	Phase      string    `json:"phase" yaml:"phase"` // Creating, Active, Updating, Deleting, Failed
	Ready      bool      `json:"ready" yaml:"ready"`
	Message    string    `json:"message,omitempty" yaml:"message,omitempty"`
	Endpoint   string    `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	NodesReady int       `json:"nodesReady" yaml:"nodesReady"`
	NodesTotal int       `json:"nodesTotal" yaml:"nodesTotal"`
	UpdatedAt  time.Time `json:"updatedAt" yaml:"updatedAt"`
}

// ClusterList lista de clusters
type ClusterList struct {
	Clusters []ClusterSummary `json:"clusters" yaml:"clusters"`
}

// ClusterSummary resumo de um cluster (para listagem)
type ClusterSummary struct {
	Name        string        `json:"name" yaml:"name"`
	Provider    string        `json:"provider" yaml:"provider"`
	Region      string        `json:"region" yaml:"region"`
	Status      ClusterStatus `json:"status" yaml:"status"`
	NodeCount   int           `json:"nodeCount" yaml:"nodeCount"`
	K8sVersion  string        `json:"k8sVersion" yaml:"k8sVersion"`
	Environment string        `json:"environment" yaml:"environment"`
	CreatedAt   time.Time     `json:"createdAt" yaml:"createdAt"`
}
