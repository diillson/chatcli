package config

import (
	"fmt"
	"regexp"
)

var (
	clusterNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)
	vpcIdRegex       = regexp.MustCompile(`^vpc-[a-f0-9]{8,17}$`)
	subnetIdRegex    = regexp.MustCompile(`^subnet-[a-f0-9]{8,17}$`)
	sgIdRegex        = regexp.MustCompile(`^sg-[a-f0-9]{8,17}$`)
	awsRegions       = []string{
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1",
		"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-south-1",
		"sa-east-1", "ca-central-1",
	}
	k8sVersions = []string{"1.33", "1.32", "1.31", "1.30", "1.29", "1.28", "1.27", "1.26", "1.25"}
)

// Validate valida a configuração do cluster
func (c *ClusterConfig) Validate() error {
	// Nome do cluster
	if c.Name == "" {
		return fmt.Errorf("nome do cluster é obrigatório")
	}
	if !clusterNameRegex.MatchString(c.Name) {
		return fmt.Errorf("nome do cluster inválido")
	}

	// Provider
	if c.Provider != "aws" {
		return fmt.Errorf("provider '%s' não suportado", c.Provider)
	}

	// Região
	if c.Region == "" {
		return fmt.Errorf("região é obrigatória")
	}
	if !contains(awsRegions, c.Region) {
		return fmt.Errorf("região '%s' inválida", c.Region)
	}

	// Versão K8s
	if c.K8sVersion == "" {
		c.K8sVersion = "1.30"
	}
	if !contains(k8sVersions, c.K8sVersion) {
		return fmt.Errorf("versão K8s '%s' não suportada", c.K8sVersion)
	}

	// Validar NetworkConfig
	if err := c.NetworkConfig.Validate(); err != nil {
		return fmt.Errorf("configuração de rede inválida: %w", err)
	}

	// Validar NodeConfig
	if err := c.NodeConfig.Validate(); err != nil {
		return fmt.Errorf("configuração de nodes inválida: %w", err)
	}

	return nil
}

// Validate valida configuração de rede (NOVO)
func (n *NetworkConfig) Validate() error {
	// Determinar modo automaticamente se não especificado
	if n.Mode == "" {
		n.Mode = n.DetermineMode()
	}

	// Validar modo
	validModes := []string{"auto", "mixed", "byo"}
	if !contains(validModes, n.Mode) {
		return fmt.Errorf("modo '%s' inválido. Use: auto, mixed, byo", n.Mode)
	}

	// Validações específicas por modo
	switch n.Mode {
	case "auto":
		return n.validateAutoMode()
	case "mixed":
		return n.validateMixedMode()
	case "byo":
		return n.validateBYOMode()
	}

	return nil
}

// DetermineMode determina o modo baseado nos campos preenchidos
func (n *NetworkConfig) DetermineMode() string {
	// BYO: VPC + Subnets + SG tudo existente
	if n.VpcId != "" &&
		len(n.PrivateSubnetIds) > 0 &&
		(n.ClusterSecurityGroupId != "" || n.NodeSecurityGroupId != "") {
		return "byo"
	}

	// MIXED: VPC existente mas precisa criar subnets/SG
	if n.VpcId != "" {
		return "mixed"
	}

	// AUTO: Nada especificado, cria tudo
	return "auto"
}

func (n *NetworkConfig) validateAutoMode() error {
	// No modo AUTO, não pode ter recursos existentes especificados
	if n.VpcId != "" {
		return fmt.Errorf("modo AUTO não aceita --vpc-id (remove ou use modo mixed/byo)")
	}
	if len(n.PublicSubnetIds) > 0 || len(n.PrivateSubnetIds) > 0 {
		return fmt.Errorf("modo AUTO não aceita subnet IDs existentes")
	}

	// Validar CIDR
	if n.VpcCidr == "" {
		n.VpcCidr = "10.0.0.0/16"
	}

	// Validar AZs
	if n.AvailabilityZones < 2 || n.AvailabilityZones > 3 {
		return fmt.Errorf("availability zones deve ser 2 ou 3")
	}

	return nil
}

func (n *NetworkConfig) validateMixedMode() error {
	// Modo MIXED requer VPC existente
	if n.VpcId == "" {
		return fmt.Errorf("modo MIXED requer --vpc-id")
	}
	if !vpcIdRegex.MatchString(n.VpcId) {
		return fmt.Errorf("VPC ID inválido: %s", n.VpcId)
	}

	// Deve criar subnets OU especificar existentes
	hasExistingSubnets := len(n.PrivateSubnetIds) > 0
	willCreateSubnets := n.CreatePrivateSubnets

	if !hasExistingSubnets && !willCreateSubnets {
		return fmt.Errorf("modo MIXED: especifique subnets existentes ou habilite criação")
	}

	// Validar subnet IDs existentes
	for _, subnetId := range n.PublicSubnetIds {
		if !subnetIdRegex.MatchString(subnetId) {
			return fmt.Errorf("subnet ID inválido: %s", subnetId)
		}
	}
	for _, subnetId := range n.PrivateSubnetIds {
		if !subnetIdRegex.MatchString(subnetId) {
			return fmt.Errorf("subnet ID inválido: %s", subnetId)
		}
	}

	return nil
}

func (n *NetworkConfig) validateBYOMode() error {
	// Modo BYO requer VPC
	if n.VpcId == "" {
		return fmt.Errorf("modo BYO requer --vpc-id")
	}
	if !vpcIdRegex.MatchString(n.VpcId) {
		return fmt.Errorf("VPC ID inválido: %s", n.VpcId)
	}

	// Requer subnets privadas (mínimo)
	if len(n.PrivateSubnetIds) == 0 {
		return fmt.Errorf("modo BYO requer pelo menos 2 subnets privadas")
	}
	if len(n.PrivateSubnetIds) < 2 {
		return fmt.Errorf("EKS requer pelo menos 2 subnets em AZs diferentes")
	}

	// Validar todos os subnet IDs
	for _, subnetId := range append(n.PublicSubnetIds, n.PrivateSubnetIds...) {
		if !subnetIdRegex.MatchString(subnetId) {
			return fmt.Errorf("subnet ID inválido: %s", subnetId)
		}
	}

	// Validar Security Group IDs (opcional mas se especificado, validar)
	if n.ClusterSecurityGroupId != "" && !sgIdRegex.MatchString(n.ClusterSecurityGroupId) {
		return fmt.Errorf("security group ID inválido: %s", n.ClusterSecurityGroupId)
	}
	if n.NodeSecurityGroupId != "" && !sgIdRegex.MatchString(n.NodeSecurityGroupId) {
		return fmt.Errorf("security group ID inválido: %s", n.NodeSecurityGroupId)
	}

	return nil
}

// Validate valida configuração de nodes
func (n *NodeConfig) Validate() error {
	if n.MinSize < 1 {
		return fmt.Errorf("minSize deve ser >= 1")
	}
	if n.MaxSize < n.MinSize {
		return fmt.Errorf("maxSize deve ser >= minSize")
	}
	if n.DesiredSize < n.MinSize || n.DesiredSize > n.MaxSize {
		return fmt.Errorf("desiredSize deve estar entre minSize e maxSize")
	}
	if n.DiskSize < 20 {
		return fmt.Errorf("diskSize deve ser >= 20 GB")
	}
	if n.InstanceType == "" {
		return fmt.Errorf("instanceType é obrigatório")
	}
	return nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
