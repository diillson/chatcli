package config

import "github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"

const (
	// Versões padrão
	DefaultK8sVersion   = "1.30"
	DefaultIstioVersion = "1.20.0"

	// Networking
	DefaultVPCCidr = "10.0.0.0/16"
	DefaultAZCount = 3

	// Nodes
	DefaultNodeInstanceType = "t3.medium"
	DefaultNodeMinSize      = 1
	DefaultNodeMaxSize      = 5
	DefaultNodeDesiredSize  = 3
	DefaultNodeDiskSize     = 30 // GB

	// State
	DefaultLockTableName = "k8s-cloud-state-locks"
)

// NewDefaultClusterConfig retorna uma configuração padrão
func NewDefaultClusterConfig(name, provider, region string) types.ClusterConfig {
	return types.ClusterConfig{
		Name:        name,
		Provider:    provider,
		Region:      region,
		Environment: "production",

		// Networking
		CreateVPC:         true,
		VPCCidr:           DefaultVPCCidr,
		AvailabilityZones: DefaultAZCount,

		// Kubernetes
		K8sVersion: DefaultK8sVersion,

		// Nodes
		NodeConfig: types.NodeConfig{
			InstanceType: DefaultNodeInstanceType,
			MinSize:      DefaultNodeMinSize,
			MaxSize:      DefaultNodeMaxSize,
			DesiredSize:  DefaultNodeDesiredSize,
			DiskSize:     DefaultNodeDiskSize,
		},

		// Add-ons desabilitados por padrão
		Addons: types.AddonConfig{},

		// Tags padrão
		Tags: map[string]string{
			"ManagedBy": "chatcli-k8s-cloud",
		},
	}
}

// ValidateConfig valida uma configuração
func ValidateConfig(cfg *types.ClusterConfig) error {
	if cfg.Name == "" {
		return ErrInvalidName
	}

	if cfg.Provider == "" {
		return ErrInvalidProvider
	}

	if cfg.Region == "" {
		return ErrInvalidRegion
	}

	// Validar node config
	if cfg.NodeConfig.MinSize < 1 {
		return ErrInvalidNodeCount
	}

	if cfg.NodeConfig.MaxSize < cfg.NodeConfig.MinSize {
		return ErrInvalidNodeCount
	}

	if cfg.NodeConfig.DesiredSize < cfg.NodeConfig.MinSize ||
		cfg.NodeConfig.DesiredSize > cfg.NodeConfig.MaxSize {
		return ErrInvalidNodeCount
	}

	return nil
}
