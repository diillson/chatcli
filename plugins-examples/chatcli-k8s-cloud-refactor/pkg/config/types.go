package config

import "time"

type ClusterConfig struct {
	// Identificação
	Name        string    `json:"name" yaml:"name"`
	Provider    string    `json:"provider" yaml:"provider"`
	Region      string    `json:"region" yaml:"region"`
	Environment string    `json:"environment" yaml:"environment"`
	ProjectName string    `json:"projectName" yaml:"projectName"`
	StackName   string    `json:"stackName" yaml:"stackName"`
	Backend     string    `json:"backend" yaml:"backend"`
	CreatedAt   time.Time `json:"createdAt" yaml:"createdAt"`
	CreatedBy   string    `json:"createdBy" yaml:"createdBy"`

	// Kubernetes
	K8sVersion string `json:"k8sVersion" yaml:"k8sVersion"`

	// Networking
	NetworkConfig NetworkConfig `json:"networkConfig" yaml:"networkConfig"`

	// Nodes
	NodeConfig NodeConfig `json:"nodeConfig" yaml:"nodeConfig"`

	// Add-ons
	Addons AddonConfig `json:"addons" yaml:"addons"`

	// Tags
	Tags map[string]string `json:"tags" yaml:"tags"`
}

// NetworkConfig configuração de rede
type NetworkConfig struct {
	// Modo de operação
	Mode string `json:"mode" yaml:"mode"` // "auto", "mixed", "byo"

	// VPC
	VpcId             string   `json:"vpcId,omitempty" yaml:"vpcId,omitempty"`             // Usar VPC existente
	VpcCidr           string   `json:"vpcCidr,omitempty" yaml:"vpcCidr,omitempty"`         // Criar VPC com este CIDR
	AvailabilityZones int      `json:"availabilityZones" yaml:"availabilityZones"`         // Número de AZs
	SpecificAZs       []string `json:"specificAZs,omitempty" yaml:"specificAZs,omitempty"` // AZs específicas

	// Subnets (usar existentes)
	PublicSubnetIds  []string `json:"publicSubnetIds,omitempty" yaml:"publicSubnetIds,omitempty"`
	PrivateSubnetIds []string `json:"privateSubnetIds,omitempty" yaml:"privateSubnetIds,omitempty"`

	// Subnets (criar novas - apenas se VPC existente)
	CreatePublicSubnets  bool     `json:"createPublicSubnets" yaml:"createPublicSubnets"`
	CreatePrivateSubnets bool     `json:"createPrivateSubnets" yaml:"createPrivateSubnets"`
	PublicSubnetCidrs    []string `json:"publicSubnetCidrs,omitempty" yaml:"publicSubnetCidrs,omitempty"`
	PrivateSubnetCidrs   []string `json:"privateSubnetCidrs,omitempty" yaml:"privateSubnetCidrs,omitempty"`

	// Security Groups
	ClusterSecurityGroupId string   `json:"clusterSecurityGroupId,omitempty" yaml:"clusterSecurityGroupId,omitempty"`
	NodeSecurityGroupId    string   `json:"nodeSecurityGroupId,omitempty" yaml:"nodeSecurityGroupId,omitempty"`
	AdditionalSGIds        []string `json:"additionalSGIds,omitempty" yaml:"additionalSGIds,omitempty"`

	// NAT/Internet Gateway
	UseExistingNAT    bool     `json:"useExistingNAT" yaml:"useExistingNAT"`
	UseExistingIGW    bool     `json:"useExistingIGW" yaml:"useExistingIGW"`
	NatGatewayIds     []string `json:"natGatewayIds,omitempty" yaml:"natGatewayIds,omitempty"`
	InternetGatewayId string   `json:"internetGatewayId,omitempty" yaml:"internetGatewayId,omitempty"`
}

// NodeConfig
type NodeConfig struct {
	InstanceType string            `json:"instanceType" yaml:"instanceType"`
	MinSize      int               `json:"minSize" yaml:"minSize"`
	MaxSize      int               `json:"maxSize" yaml:"maxSize"`
	DesiredSize  int               `json:"desiredSize" yaml:"desiredSize"`
	DiskSize     int               `json:"diskSize" yaml:"diskSize"`
	Labels       map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// AddonConfig
type AddonConfig struct {
	Istio        *IstioConfig        `json:"istio,omitempty" yaml:"istio,omitempty"`
	NginxIngress *NginxIngressConfig `json:"nginxIngress,omitempty" yaml:"nginxIngress,omitempty"`
	ArgoCD       *ArgoCDConfig       `json:"argocd,omitempty" yaml:"argocd,omitempty"`
	CertManager  *CertManagerConfig  `json:"certManager,omitempty" yaml:"certManager,omitempty"`
}

type IstioConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Version string `json:"version" yaml:"version"`
	Profile string `json:"profile" yaml:"profile"`
}

type NginxIngressConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

type ArgoCDConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

type CertManagerConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}
