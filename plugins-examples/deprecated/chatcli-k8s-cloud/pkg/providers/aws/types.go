package aws

import "github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"

// NetworkingConfig configuração de rede AWS
type NetworkingConfig struct {
	VPCConfig     VPCConfig      `json:"vpcConfig"`
	SubnetConfigs []SubnetConfig `json:"subnetConfigs"`
	CreateNAT     bool           `json:"createNat"`
	CreateIGW     bool           `json:"createIgw"`
}

// VPCConfig configuração de VPC
type VPCConfig struct {
	CIDR               string            `json:"cidr"`
	EnableDNSSupport   bool              `json:"enableDnsSupport"`
	EnableDNSHostnames bool              `json:"enableDnsHostnames"`
	Tags               map[string]string `json:"tags"`
}

// SubnetConfig configuração de subnet
type SubnetConfig struct {
	CIDR             string            `json:"cidr"`
	AvailabilityZone string            `json:"availabilityZone"`
	Public           bool              `json:"public"`
	Tags             map[string]string `json:"tags"`
}

// NetworkingResources recursos criados
type NetworkingResources struct {
	VPC             VPCResource             `json:"vpc"`
	PublicSubnets   []SubnetResource        `json:"publicSubnets"`
	PrivateSubnets  []SubnetResource        `json:"privateSubnets"`
	InternetGateway *IGWResource            `json:"internetGateway,omitempty"`
	NATGateways     []NATResource           `json:"natGateways"`
	RouteTables     []RouteTableResource    `json:"routeTables"`
	SecurityGroups  []SecurityGroupResource `json:"securityGroups"`
}

// VPCResource VPC criada
type VPCResource struct {
	ID   string `json:"id"`
	CIDR string `json:"cidr"`
	ARN  string `json:"arn"`
}

// SubnetResource subnet criada
type SubnetResource struct {
	ID               string `json:"id"`
	CIDR             string `json:"cidr"`
	AvailabilityZone string `json:"availabilityZone"`
	Public           bool   `json:"public"`
}

// IGWResource internet gateway
type IGWResource struct {
	ID string `json:"id"`
}

// NATResource NAT gateway
type NATResource struct {
	ID               string `json:"id"`
	AllocationID     string `json:"allocationId"` // Elastic IP
	SubnetID         string `json:"subnetId"`
	AvailabilityZone string `json:"availabilityZone"`
}

// RouteTableResource route table
type RouteTableResource struct {
	ID      string   `json:"id"`
	Public  bool     `json:"public"`
	Subnets []string `json:"subnets"`
}

// SecurityGroupResource security group
type SecurityGroupResource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// EKSClusterConfig configuração específica do EKS
type EKSClusterConfig struct {
	ClusterConfig types.ClusterConfig
	Networking    NetworkingResources

	// EKS específico
	RoleARN           string   `json:"roleArn"`
	SecurityGroupIDs  []string `json:"securityGroupIds"`
	EndpointPrivate   bool     `json:"endpointPrivate"`
	EndpointPublic    bool     `json:"endpointPublic"`
	PublicAccessCIDRs []string `json:"publicAccessCidrs"`
}
