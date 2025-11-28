package aws

import (
	"fmt"
	"net"
)

// CalculateSubnetCIDRs calcula CIDRs para subnets baseado no CIDR da VPC
// Cria subnets /24 (256 IPs cada) distribuídas pelas AZs
func CalculateSubnetCIDRs(vpcCIDR string, azCount int) ([]SubnetConfig, error) {
	_, ipNet, err := net.ParseCIDR(vpcCIDR)
	if err != nil {
		return nil, fmt.Errorf("CIDR inválido: %w", err)
	}

	// Obter bits de host disponíveis
	ones, bits := ipNet.Mask.Size()
	if bits-ones < 8 {
		return nil, fmt.Errorf("VPC CIDR muito pequeno para criar subnets")
	}

	var configs []SubnetConfig
	baseIP := ipNet.IP

	// Criar subnets públicas (primeira metade)
	for i := 0; i < azCount; i++ {
		cidr := fmt.Sprintf("%d.%d.%d.0/24",
			baseIP[0],
			baseIP[1],
			100+i, // 10.0.100.0/24, 10.0.101.0/24, ...
		)

		configs = append(configs, SubnetConfig{
			CIDR:   cidr,
			Public: true,
			Tags: map[string]string{
				"kubernetes.io/role/elb": "1",
				"Type":                   "public",
			},
		})
	}

	// Criar subnets privadas (segunda metade)
	for i := 0; i < azCount; i++ {
		cidr := fmt.Sprintf("%d.%d.%d.0/24",
			baseIP[0],
			baseIP[1],
			i, // 10.0.0.0/24, 10.0.1.0/24, ...
		)

		configs = append(configs, SubnetConfig{
			CIDR:   cidr,
			Public: false,
			Tags: map[string]string{
				"kubernetes.io/role/internal-elb": "1",
				"Type":                            "private",
			},
		})
	}

	return configs, nil
}

// GetAvailabilityZones retorna as AZs disponíveis em uma região
func GetAvailabilityZones(region string, count int) []string {
	// Mapa de regiões para AZs (simplificado)
	// Em produção, usar DescribeAvailabilityZones da API
	azMap := map[string][]string{
		"us-east-1": {"us-east-1a", "us-east-1b", "us-east-1c", "us-east-1d"},
		"us-east-2": {"us-east-2a", "us-east-2b", "us-east-2c"},
		"us-west-1": {"us-west-1a", "us-west-1b"},
		"us-west-2": {"us-west-2a", "us-west-2b", "us-west-2c", "us-west-2d"},
		"eu-west-1": {"eu-west-1a", "eu-west-1b", "eu-west-1c"},
	}

	azs, exists := azMap[region]
	if !exists {
		// Fallback genérico
		azs = []string{
			fmt.Sprintf("%sa", region),
			fmt.Sprintf("%sb", region),
			fmt.Sprintf("%sc", region),
		}
	}

	if count > len(azs) {
		count = len(azs)
	}

	return azs[:count]
}
