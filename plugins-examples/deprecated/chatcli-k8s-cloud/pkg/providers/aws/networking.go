package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
)

// NetworkManager gerencia recursos de rede AWS
type NetworkManager struct {
	ec2Client   *ec2.Client
	region      string
	clusterName string
}

// NewNetworkManager cria um novo gerenciador de rede
func NewNetworkManager(ec2Client *ec2.Client, region, clusterName string) *NetworkManager {
	return &NetworkManager{
		ec2Client:   ec2Client,
		region:      region,
		clusterName: clusterName,
	}
}

// CreateNetworking cria toda a infraestrutura de rede
func (nm *NetworkManager) CreateNetworking(ctx context.Context, config NetworkingConfig) (*NetworkingResources, error) {
	logger.Info("🌐 Criando infraestrutura de rede...")

	resources := &NetworkingResources{}

	// 1. Criar VPC
	vpc, err := nm.createVPC(ctx, config.VPCConfig)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar VPC: %w", err)
	}
	resources.VPC = *vpc
	logger.Successf("   ✓ VPC criada: %s (%s)", vpc.ID, vpc.CIDR)

	// 2. Habilitar DNS
	if err := nm.enableVPCDNS(ctx, vpc.ID); err != nil {
		return nil, fmt.Errorf("falha ao habilitar DNS: %w", err)
	}
	logger.Success("   ✓ DNS habilitado")

	// 3. Criar Internet Gateway (se necessário)
	var igw *IGWResource
	if config.CreateIGW {
		igw, err = nm.createInternetGateway(ctx, vpc.ID)
		if err != nil {
			return nil, fmt.Errorf("falha ao criar Internet Gateway: %w", err)
		}
		resources.InternetGateway = igw
		logger.Successf("   ✓ Internet Gateway criado: %s", igw.ID)
	}

	// 4. Criar Subnets
	logger.Progress("   ⏳ Criando subnets...")
	publicSubnets, privateSubnets, err := nm.createSubnets(ctx, vpc.ID, config.SubnetConfigs)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar subnets: %w", err)
	}
	resources.PublicSubnets = publicSubnets
	resources.PrivateSubnets = privateSubnets
	logger.Successf("   ✓ %d subnets públicas criadas", len(publicSubnets))
	logger.Successf("   ✓ %d subnets privadas criadas", len(privateSubnets))

	// 5. Criar NAT Gateways (se necessário)
	if config.CreateNAT && len(publicSubnets) > 0 {
		logger.Progress("   ⏳ Criando NAT Gateways...")
		natGateways, err := nm.createNATGateways(ctx, publicSubnets)
		if err != nil {
			return nil, fmt.Errorf("falha ao criar NAT Gateways: %w", err)
		}
		resources.NATGateways = natGateways
		logger.Successf("   ✓ %d NAT Gateways criados", len(natGateways))
	}

	// 6. Criar Route Tables
	logger.Progress("   ⏳ Configurando route tables...")
	routeTables, err := nm.createRouteTables(ctx, vpc.ID, resources)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar route tables: %w", err)
	}
	resources.RouteTables = routeTables
	logger.Successf("   ✓ %d route tables criadas", len(routeTables))

	// 7. Criar Security Groups
	logger.Progress("   ⏳ Criando security groups...")
	securityGroups, err := nm.createSecurityGroups(ctx, vpc.ID)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar security groups: %w", err)
	}
	resources.SecurityGroups = securityGroups
	logger.Successf("   ✓ %d security groups criados", len(securityGroups))

	logger.Success("✅ Infraestrutura de rede criada com sucesso!")
	return resources, nil
}

// createVPC cria uma VPC
func (nm *NetworkManager) createVPC(ctx context.Context, config VPCConfig) (*VPCResource, error) {
	// Criar VPC
	result, err := nm.ec2Client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String(config.CIDR),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeVpc,
				Tags:         nm.buildTags(config.Tags, "vpc"),
			},
		},
	})

	if err != nil {
		return nil, err
	}

	vpcID := *result.Vpc.VpcId

	// Aguardar VPC ficar disponível
	waiter := ec2.NewVpcAvailableWaiter(nm.ec2Client)
	if err := waiter.Wait(ctx, &ec2.DescribeVpcsInput{
		VpcIds: []string{vpcID},
	}, 2*time.Minute); err != nil {
		return nil, fmt.Errorf("timeout aguardando VPC: %w", err)
	}

	return &VPCResource{
		ID:   vpcID,
		CIDR: config.CIDR,
		ARN:  fmt.Sprintf("arn:aws:ec2:%s::vpc/%s", nm.region, vpcID),
	}, nil
}

// enableVPCDNS habilita DNS support e hostnames
func (nm *NetworkManager) enableVPCDNS(ctx context.Context, vpcID string) error {
	// Enable DNS Support
	_, err := nm.ec2Client.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:            aws.String(vpcID),
		EnableDnsSupport: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	if err != nil {
		return err
	}

	// Enable DNS Hostnames
	_, err = nm.ec2Client.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:              aws.String(vpcID),
		EnableDnsHostnames: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})

	return err
}

// createInternetGateway cria e anexa um Internet Gateway
func (nm *NetworkManager) createInternetGateway(ctx context.Context, vpcID string) (*IGWResource, error) {
	// Criar IGW
	result, err := nm.ec2Client.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInternetGateway,
				Tags:         nm.buildTags(nil, "igw"),
			},
		},
	})

	if err != nil {
		return nil, err
	}

	igwID := *result.InternetGateway.InternetGatewayId

	// Anexar à VPC
	_, err = nm.ec2Client.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
		InternetGatewayId: aws.String(igwID),
		VpcId:             aws.String(vpcID),
	})

	if err != nil {
		return nil, err
	}

	return &IGWResource{ID: igwID}, nil
}

// createSubnets cria subnets públicas e privadas
func (nm *NetworkManager) createSubnets(ctx context.Context, vpcID string, configs []SubnetConfig) ([]SubnetResource, []SubnetResource, error) {
	var publicSubnets []SubnetResource
	var privateSubnets []SubnetResource

	for i, config := range configs {
		result, err := nm.ec2Client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId:            aws.String(vpcID),
			CidrBlock:        aws.String(config.CIDR),
			AvailabilityZone: aws.String(config.AvailabilityZone),
			TagSpecifications: []types.TagSpecification{
				{
					ResourceType: types.ResourceTypeSubnet,
					Tags:         nm.buildTags(config.Tags, fmt.Sprintf("subnet-%d", i)),
				},
			},
		})

		if err != nil {
			return nil, nil, err
		}

		subnet := SubnetResource{
			ID:               *result.Subnet.SubnetId,
			CIDR:             config.CIDR,
			AvailabilityZone: config.AvailabilityZone,
			Public:           config.Public,
		}

		// Se for subnet pública, habilitar auto-assign public IP
		if config.Public {
			_, err = nm.ec2Client.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
				SubnetId:            result.Subnet.SubnetId,
				MapPublicIpOnLaunch: &types.AttributeBooleanValue{Value: aws.Bool(true)},
			})
			if err != nil {
				return nil, nil, err
			}

			publicSubnets = append(publicSubnets, subnet)
		} else {
			privateSubnets = append(privateSubnets, subnet)
		}
	}

	return publicSubnets, privateSubnets, nil
}

// createNATGateways cria NAT Gateways (um por AZ)
func (nm *NetworkManager) createNATGateways(ctx context.Context, publicSubnets []SubnetResource) ([]NATResource, error) {
	var natGateways []NATResource

	for i, subnet := range publicSubnets {
		// Alocar Elastic IP
		eipResult, err := nm.ec2Client.AllocateAddress(ctx, &ec2.AllocateAddressInput{
			Domain: types.DomainTypeVpc,
			TagSpecifications: []types.TagSpecification{
				{
					ResourceType: types.ResourceTypeElasticIp,
					Tags:         nm.buildTags(nil, fmt.Sprintf("nat-eip-%d", i)),
				},
			},
		})

		if err != nil {
			return nil, err
		}

		// Criar NAT Gateway
		natResult, err := nm.ec2Client.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{
			SubnetId:     aws.String(subnet.ID),
			AllocationId: eipResult.AllocationId,
			TagSpecifications: []types.TagSpecification{
				{
					ResourceType: types.ResourceTypeNatgateway,
					Tags:         nm.buildTags(nil, fmt.Sprintf("nat-%s", subnet.AvailabilityZone)),
				},
			},
		})

		if err != nil {
			return nil, err
		}

		natGateways = append(natGateways, NATResource{
			ID:               *natResult.NatGateway.NatGatewayId,
			AllocationID:     *eipResult.AllocationId,
			SubnetID:         subnet.ID,
			AvailabilityZone: subnet.AvailabilityZone,
		})
	}

	// Aguardar NAT Gateways ficarem disponíveis
	logger.Progress("      ⏳ Aguardando NAT Gateways ficarem disponíveis...")
	for _, nat := range natGateways {
		waiter := ec2.NewNatGatewayAvailableWaiter(nm.ec2Client)
		if err := waiter.Wait(ctx, &ec2.DescribeNatGatewaysInput{
			NatGatewayIds: []string{nat.ID},
		}, 5*time.Minute); err != nil {
			return nil, fmt.Errorf("timeout aguardando NAT Gateway %s: %w", nat.ID, err)
		}
	}

	return natGateways, nil
}

// createRouteTables cria route tables para subnets públicas e privadas
func (nm *NetworkManager) createRouteTables(ctx context.Context, vpcID string, resources *NetworkingResources) ([]RouteTableResource, error) {
	var routeTables []RouteTableResource

	// 1. Route table pública (uma para todas as subnets públicas)
	if len(resources.PublicSubnets) > 0 && resources.InternetGateway != nil {
		publicRT, err := nm.createPublicRouteTable(ctx, vpcID, resources.InternetGateway.ID)
		if err != nil {
			return nil, err
		}

		// Associar com todas as subnets públicas
		for _, subnet := range resources.PublicSubnets {
			_, err := nm.ec2Client.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
				RouteTableId: aws.String(publicRT.ID),
				SubnetId:     aws.String(subnet.ID),
			})
			if err != nil {
				return nil, err
			}
			publicRT.Subnets = append(publicRT.Subnets, subnet.ID)
		}

		routeTables = append(routeTables, *publicRT)
	}

	// 2. Route tables privadas (uma por subnet privada, apontando para seu NAT)
	for i, subnet := range resources.PrivateSubnets {
		var natGatewayID string

		// Encontrar NAT Gateway na mesma AZ (ou primeiro se não houver)
		if len(resources.NATGateways) > 0 {
			natGatewayID = resources.NATGateways[0].ID
			for _, nat := range resources.NATGateways {
				if nat.AvailabilityZone == subnet.AvailabilityZone {
					natGatewayID = nat.ID
					break
				}
			}
		}

		privateRT, err := nm.createPrivateRouteTable(ctx, vpcID, natGatewayID, i)
		if err != nil {
			return nil, err
		}

		// Associar com a subnet
		_, err = nm.ec2Client.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
			RouteTableId: aws.String(privateRT.ID),
			SubnetId:     aws.String(subnet.ID),
		})
		if err != nil {
			return nil, err
		}

		privateRT.Subnets = []string{subnet.ID}
		routeTables = append(routeTables, *privateRT)
	}

	return routeTables, nil
}

// createPublicRouteTable cria route table pública
func (nm *NetworkManager) createPublicRouteTable(ctx context.Context, vpcID, igwID string) (*RouteTableResource, error) {
	// Criar route table
	result, err := nm.ec2Client.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{
		VpcId: aws.String(vpcID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeRouteTable,
				Tags:         nm.buildTags(nil, "public-rt"),
			},
		},
	})

	if err != nil {
		return nil, err
	}

	rtID := *result.RouteTable.RouteTableId

	// Adicionar rota para internet via IGW
	_, err = nm.ec2Client.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId:         aws.String(rtID),
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		GatewayId:            aws.String(igwID),
	})

	if err != nil {
		return nil, err
	}

	return &RouteTableResource{
		ID:     rtID,
		Public: true,
	}, nil
}

// createPrivateRouteTable cria route table privada
func (nm *NetworkManager) createPrivateRouteTable(ctx context.Context, vpcID, natGatewayID string, index int) (*RouteTableResource, error) {
	// Criar route table
	result, err := nm.ec2Client.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{
		VpcId: aws.String(vpcID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeRouteTable,
				Tags:         nm.buildTags(nil, fmt.Sprintf("private-rt-%d", index)),
			},
		},
	})

	if err != nil {
		return nil, err
	}

	rtID := *result.RouteTable.RouteTableId

	// Adicionar rota para internet via NAT (se houver)
	if natGatewayID != "" {
		_, err = nm.ec2Client.CreateRoute(ctx, &ec2.CreateRouteInput{
			RouteTableId:         aws.String(rtID),
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			NatGatewayId:         aws.String(natGatewayID),
		})

		if err != nil {
			return nil, err
		}
	}

	return &RouteTableResource{
		ID:     rtID,
		Public: false,
	}, nil
}

// createSecurityGroups cria security groups para EKS
func (nm *NetworkManager) createSecurityGroups(ctx context.Context, vpcID string) ([]SecurityGroupResource, error) {
	var securityGroups []SecurityGroupResource

	// 1. Cluster Security Group
	clusterSG, err := nm.createClusterSecurityGroup(ctx, vpcID)
	if err != nil {
		return nil, err
	}
	securityGroups = append(securityGroups, *clusterSG)

	// 2. Node Security Group
	nodeSG, err := nm.createNodeSecurityGroup(ctx, vpcID, clusterSG.ID)
	if err != nil {
		return nil, err
	}
	securityGroups = append(securityGroups, *nodeSG)

	return securityGroups, nil
}

// createClusterSecurityGroup cria SG para o control plane
func (nm *NetworkManager) createClusterSecurityGroup(ctx context.Context, vpcID string) (*SecurityGroupResource, error) {
	result, err := nm.ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(fmt.Sprintf("%s-cluster-sg", nm.clusterName)),
		Description: aws.String("Security group for EKS cluster control plane"),
		VpcId:       aws.String(vpcID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeSecurityGroup,
				Tags:         nm.buildTags(nil, "cluster-sg"),
			},
		},
	})

	if err != nil {
		return nil, err
	}

	sgID := *result.GroupId

	// Regra: Permitir comunicação HTTPS do control plane para nodes
	_, err = nm.ec2Client.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(443),
				ToPort:     aws.Int32(443),
				IpRanges: []types.IpRange{
					{CidrIp: aws.String("0.0.0.0/0")},
				},
			},
		},
	})

	if err != nil {
		return nil, err
	}

	return &SecurityGroupResource{
		ID:          sgID,
		Name:        fmt.Sprintf("%s-cluster-sg", nm.clusterName),
		Description: "EKS cluster security group",
	}, nil
}

// createNodeSecurityGroup cria SG para os worker nodes
func (nm *NetworkManager) createNodeSecurityGroup(ctx context.Context, vpcID, clusterSGID string) (*SecurityGroupResource, error) {
	result, err := nm.ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(fmt.Sprintf("%s-node-sg", nm.clusterName)),
		Description: aws.String("Security group for EKS worker nodes"),
		VpcId:       aws.String(vpcID),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeSecurityGroup,
				Tags:         nm.buildTags(nil, "node-sg"),
			},
		},
	})

	if err != nil {
		return nil, err
	}

	sgID := *result.GroupId

	// Regra 1: Permitir comunicação entre nodes
	_, err = nm.ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []types.IpPermission{
			{
				IpProtocol: aws.String("-1"),
				UserIdGroupPairs: []types.UserIdGroupPair{
					{GroupId: aws.String(sgID)},
				},
			},
		},
	})

	if err != nil {
		return nil, err
	}

	// Regra 2: Permitir comunicação do control plane
	_, err = nm.ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(443),
				ToPort:     aws.Int32(443),
				UserIdGroupPairs: []types.UserIdGroupPair{
					{GroupId: aws.String(clusterSGID)},
				},
			},
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(1025),
				ToPort:     aws.Int32(65535),
				UserIdGroupPairs: []types.UserIdGroupPair{
					{GroupId: aws.String(clusterSGID)},
				},
			},
		},
	})

	if err != nil {
		return nil, err
	}

	return &SecurityGroupResource{
		ID:          sgID,
		Name:        fmt.Sprintf("%s-node-sg", nm.clusterName),
		Description: "EKS node security group",
	}, nil
}

// buildTags helper para construir tags
func (nm *NetworkManager) buildTags(customTags map[string]string, resourceName string) []types.Tag {
	tags := []types.Tag{
		{
			Key:   aws.String("Name"),
			Value: aws.String(fmt.Sprintf("%s-%s", nm.clusterName, resourceName)),
		},
		{
			Key:   aws.String("kubernetes.io/cluster/" + nm.clusterName),
			Value: aws.String("owned"),
		},
		{
			Key:   aws.String("ManagedBy"),
			Value: aws.String("chatcli-k8s-cloud"),
		},
	}

	// Adicionar tags customizadas
	for k, v := range customTags {
		tags = append(tags, types.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	return tags
}

// DeleteNetworking remove toda a infraestrutura de rede
func (nm *NetworkManager) DeleteNetworking(ctx context.Context, resources *NetworkingResources) error {
	logger.Info("🗑️  Removendo infraestrutura de rede...")

	// Ordem de remoção (inversa da criação)

	// 1. Remover NAT Gateways
	for _, nat := range resources.NATGateways {
		logger.Progressf("   ⏳ Removendo NAT Gateway %s...", nat.ID)
		_, err := nm.ec2Client.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{
			NatGatewayId: aws.String(nat.ID),
		})
		if err != nil {
			logger.Warningf("   ⚠️  Erro ao remover NAT Gateway: %v", err)
		}

		// Aguardar NAT ser deletado
		waiter := ec2.NewNatGatewayDeletedWaiter(nm.ec2Client)
		_ = waiter.Wait(ctx, &ec2.DescribeNatGatewaysInput{
			NatGatewayIds: []string{nat.ID},
		}, 5*time.Minute)

		// Liberar Elastic IP
		_, _ = nm.ec2Client.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
			AllocationId: aws.String(nat.AllocationID),
		})
	}

	// 2. Remover Internet Gateway
	if resources.InternetGateway != nil {
		logger.Progressf("   ⏳ Removendo Internet Gateway %s...", resources.InternetGateway.ID)
		_, _ = nm.ec2Client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
			InternetGatewayId: aws.String(resources.InternetGateway.ID),
			VpcId:             aws.String(resources.VPC.ID),
		})
		_, _ = nm.ec2Client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
			InternetGatewayId: aws.String(resources.InternetGateway.ID),
		})
	}

	// 3. Remover Route Tables (exceto a main)
	for _, rt := range resources.RouteTables {
		logger.Progressf("   ⏳ Removendo Route Table %s...", rt.ID)
		_, _ = nm.ec2Client.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{
			RouteTableId: aws.String(rt.ID),
		})
	}

	// 4. Remover Subnets
	allSubnets := append(resources.PublicSubnets, resources.PrivateSubnets...)
	for _, subnet := range allSubnets {
		logger.Progressf("   ⏳ Removendo Subnet %s...", subnet.ID)
		_, _ = nm.ec2Client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
			SubnetId: aws.String(subnet.ID),
		})
	}

	// 5. Remover Security Groups
	for _, sg := range resources.SecurityGroups {
		logger.Progressf("   ⏳ Removendo Security Group %s...", sg.ID)
		_, _ = nm.ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
			GroupId: aws.String(sg.ID),
		})
	}

	// 6. Remover VPC
	logger.Progressf("   ⏳ Removendo VPC %s...", resources.VPC.ID)
	_, err := nm.ec2Client.DeleteVpc(ctx, &ec2.DeleteVpcInput{
		VpcId: aws.String(resources.VPC.ID),
	})
	if err != nil {
		return fmt.Errorf("falha ao remover VPC: %w", err)
	}

	logger.Success("✅ Infraestrutura de rede removida!")
	return nil
}
