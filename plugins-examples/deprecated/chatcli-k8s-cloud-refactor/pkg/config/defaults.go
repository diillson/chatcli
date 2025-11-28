package config

import (
	"fmt"
	"os"
)

// DefaultClusterConfig retorna configuração padrão para EKS
func DefaultClusterConfig(name, region string) *ClusterConfig {
	return &ClusterConfig{
		Name:        name,
		Provider:    "aws",
		Region:      region,
		Environment: "production",
		ProjectName: fmt.Sprintf("k8s-cloud-%s", name),
		StackName:   fmt.Sprintf("%s-%s", name, region),
		Backend:     "",
		K8sVersion:  "1.30",
		NetworkConfig: NetworkConfig{
			Mode:                 "auto", // Modo padrão
			VpcCidr:              "10.0.0.0/16",
			AvailabilityZones:    3,
			CreatePublicSubnets:  true,
			CreatePrivateSubnets: true,
		},
		NodeConfig: NodeConfig{
			InstanceType: "t3.medium",
			MinSize:      1,
			MaxSize:      10,
			DesiredSize:  3,
			DiskSize:     20,
		},
		Addons: AddonConfig{},
		Tags: map[string]string{
			"ManagedBy": "chatcli-k8s-cloud",
			"Tool":      "pulumi",
		},
		CreatedBy: getUsername(),
	}
}

func getUsername() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return "unknown"
}
