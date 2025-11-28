package programs

import (
	"fmt"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// CreateEKSProgram retorna programa Pulumi para criar EKS cluster
func CreateEKSProgram(cfg *config.ClusterConfig) pulumi.RunFunc {
	return func(ctx *pulumi.Context) error {
		// Determinar modo de rede
		mode := cfg.NetworkConfig.Mode
		if mode == "" {
			mode = cfg.NetworkConfig.DetermineMode()
		}

		ctx.Log.Info(fmt.Sprintf("🌐 Modo de rede: %s", mode), nil)

		// Executar criação baseado no modo
		switch mode {
		case "auto":
			return createEKSAutoMode(ctx, cfg)
		case "mixed":
			return createEKSMixedMode(ctx, cfg)
		case "byo":
			return createEKSBYOMode(ctx, cfg)
		default:
			return fmt.Errorf("modo de rede inválido: %s", mode)
		}
	}
}

// ========================================
// MODO AUTO: Cria tudo do zero
// ========================================
func createEKSAutoMode(ctx *pulumi.Context, cfg *config.ClusterConfig) error {
	ctx.Log.Info("🏗️  Criando infraestrutura completa (VPC + Subnets + NAT + EKS)", nil)

	// VPC
	vpc, err := ec2.NewVpc(ctx, fmt.Sprintf("%s-vpc", cfg.Name), &ec2.VpcArgs{
		CidrBlock:          pulumi.String(cfg.NetworkConfig.VpcCidr),
		EnableDnsHostnames: pulumi.Bool(true),
		EnableDnsSupport:   pulumi.Bool(true),
		Tags: pulumi.StringMap{
			"Name":      pulumi.String(fmt.Sprintf("%s-vpc", cfg.Name)),
			"ManagedBy": pulumi.String("chatcli-k8s-cloud"),
			fmt.Sprintf("kubernetes.io/cluster/%s", cfg.Name): pulumi.String("shared"),
		},
	})
	if err != nil {
		return err
	}

	// Internet Gateway
	igw, err := ec2.NewInternetGateway(ctx, fmt.Sprintf("%s-igw", cfg.Name), &ec2.InternetGatewayArgs{
		VpcId: vpc.ID(),
		Tags: pulumi.StringMap{
			"Name": pulumi.String(fmt.Sprintf("%s-igw", cfg.Name)),
		},
	})
	if err != nil {
		return err
	}

	// Determinar AZs
	azs := generateAZs(cfg.Region, cfg.NetworkConfig.AvailabilityZones)

	// Criar subnets públicas
	publicSubnets, err := createPublicSubnets(ctx, cfg, vpc, igw, azs)
	if err != nil {
		return err
	}

	// Criar subnets privadas
	privateSubnets, err := createPrivateSubnets(ctx, cfg, vpc, azs)
	if err != nil {
		return err
	}

	// Criar NAT Gateways
	natGateways, err := createNATGateways(ctx, cfg, publicSubnets, igw)
	if err != nil {
		return err
	}

	// Configurar routing
	if err := setupRouting(ctx, cfg, vpc, publicSubnets, privateSubnets, igw, natGateways); err != nil {
		return err
	}

	// Coletar IDs de subnets
	var allSubnetIDs pulumi.StringArray
	for _, subnet := range publicSubnets {
		allSubnetIDs = append(allSubnetIDs, subnet.ID())
	}
	for _, subnet := range privateSubnets {
		allSubnetIDs = append(allSubnetIDs, subnet.ID())
	}

	var privateSubnetIDs pulumi.StringArray
	for _, subnet := range privateSubnets {
		privateSubnetIDs = append(privateSubnetIDs, subnet.ID())
	}

	// Criar EKS cluster
	cluster, clusterSG, err := createEKSCluster(ctx, cfg, vpc.ID(), allSubnetIDs)
	if err != nil {
		return err
	}

	// Criar Node Group
	if err := createNodeGroup(ctx, cfg, cluster, privateSubnetIDs); err != nil {
		return err
	}

	// Exports
	return exportOutputs(ctx, cfg, vpc.ID(), publicSubnets, privateSubnets, natGateways, cluster, clusterSG)
}

// ========================================
// MODO MIXED: VPC existente + criar recursos
// ========================================
func createEKSMixedMode(ctx *pulumi.Context, cfg *config.ClusterConfig) error {
	ctx.Log.Info(fmt.Sprintf("🔗 Usando VPC existente: %s", cfg.NetworkConfig.VpcId), nil)

	// Lookup VPC existente
	vpcId := pulumi.String(cfg.NetworkConfig.VpcId)

	// Se subnets foram especificadas, usar existentes
	var privateSubnetIDs pulumi.StringArray
	var allSubnetIDs pulumi.StringArray

	if len(cfg.NetworkConfig.PrivateSubnetIds) > 0 {
		ctx.Log.Info("📋 Usando subnets privadas existentes", nil)
		for _, subnetId := range cfg.NetworkConfig.PrivateSubnetIds {
			id := pulumi.String(subnetId)
			privateSubnetIDs = append(privateSubnetIDs, id)
			allSubnetIDs = append(allSubnetIDs, id)
		}
	}

	if len(cfg.NetworkConfig.PublicSubnetIds) > 0 {
		ctx.Log.Info("📋 Usando subnets públicas existentes", nil)
		for _, subnetId := range cfg.NetworkConfig.PublicSubnetIds {
			allSubnetIDs = append(allSubnetIDs, pulumi.String(subnetId))
		}
	}

	// Se não tem subnets, precisa criar
	if len(privateSubnetIDs) == 0 {
		return fmt.Errorf("modo MIXED requer subnets privadas existentes ou criação (não implementado nesta versão)")
	}

	// Criar EKS cluster
	cluster, clusterSG, err := createEKSCluster(ctx, cfg, vpcId, allSubnetIDs)
	if err != nil {
		return err
	}

	// Criar Node Group
	if err := createNodeGroup(ctx, cfg, cluster, privateSubnetIDs); err != nil {
		return err
	}

	// Exports simplificados
	ctx.Export("vpcId", vpcId)
	ctx.Export("clusterName", cluster.Name)
	ctx.Export("clusterArn", cluster.Arn)
	ctx.Export("clusterEndpoint", cluster.Endpoint)
	ctx.Export("clusterVersion", cluster.Version)
	ctx.Export("clusterSecurityGroupId", clusterSG.ID())
	ctx.Export("clusterCertificateAuthority", cluster.CertificateAuthority.Data().Elem())

	// Kubeconfig
	kubeconfig := generateKubeconfig(cfg, cluster)
	ctx.Export("kubeconfig", kubeconfig)

	ctx.Export("estimatedMonthlyCost", pulumi.String(fmt.Sprintf("$%.2f USD",
		estimateEKSCostValue(cfg))))

	return nil
}

// ========================================
// MODO BYO: Usa tudo existente
// ========================================
func createEKSBYOMode(ctx *pulumi.Context, cfg *config.ClusterConfig) error {
	ctx.Log.Info("♻️  Usando infraestrutura existente (BYO)", nil)
	ctx.Log.Info(fmt.Sprintf("   VPC: %s", cfg.NetworkConfig.VpcId), nil)
	ctx.Log.Info(fmt.Sprintf("   Subnets privadas: %d", len(cfg.NetworkConfig.PrivateSubnetIds)), nil)

	// VPC existente
	vpcId := pulumi.String(cfg.NetworkConfig.VpcId)

	// Subnets existentes
	var privateSubnetIDs pulumi.StringArray
	var allSubnetIDs pulumi.StringArray

	for _, subnetId := range cfg.NetworkConfig.PrivateSubnetIds {
		id := pulumi.String(subnetId)
		privateSubnetIDs = append(privateSubnetIDs, id)
		allSubnetIDs = append(allSubnetIDs, id)
	}

	for _, subnetId := range cfg.NetworkConfig.PublicSubnetIds {
		allSubnetIDs = append(allSubnetIDs, pulumi.String(subnetId))
	}

	// Security Group: usar existente OU criar
	var clusterSGId pulumi.StringInput
	var clusterSG *ec2.SecurityGroup

	if cfg.NetworkConfig.ClusterSecurityGroupId != "" {
		// Usar SG existente
		ctx.Log.Info(fmt.Sprintf("   Cluster SG: %s (existente)", cfg.NetworkConfig.ClusterSecurityGroupId), nil)
		clusterSGId = pulumi.String(cfg.NetworkConfig.ClusterSecurityGroupId)
	} else {
		// Criar novo SG
		ctx.Log.Info("   Criando Security Group para cluster", nil)
		var err error
		clusterSG, err = createClusterSecurityGroup(ctx, cfg, vpcId)
		if err != nil {
			return err
		}
		clusterSGId = clusterSG.ID()
	}

	// IAM Role para cluster
	clusterRole, err := createClusterIAMRole(ctx, cfg)
	if err != nil {
		return err
	}

	// Tags do cluster
	clusterTags := buildClusterTags(cfg)

	// EKS Cluster
	cluster, err := eks.NewCluster(ctx, cfg.Name, &eks.ClusterArgs{
		Name:    pulumi.String(cfg.Name),
		Version: pulumi.String(cfg.K8sVersion),
		RoleArn: clusterRole.Arn,
		VpcConfig: &eks.ClusterVpcConfigArgs{
			SubnetIds:             allSubnetIDs,
			SecurityGroupIds:      pulumi.StringArray{clusterSGId},
			EndpointPrivateAccess: pulumi.Bool(true),
			EndpointPublicAccess:  pulumi.Bool(true),
		},
		EnabledClusterLogTypes: pulumi.StringArray{
			pulumi.String("api"),
			pulumi.String("audit"),
			pulumi.String("authenticator"),
		},
		Tags: clusterTags,
	}, pulumi.DependsOn([]pulumi.Resource{clusterRole}))
	if err != nil {
		return err
	}

	// Criar Node Group
	if err := createNodeGroup(ctx, cfg, cluster, privateSubnetIDs); err != nil {
		return err
	}

	// Exports
	ctx.Export("vpcId", vpcId)
	ctx.Export("clusterName", cluster.Name)
	ctx.Export("clusterArn", cluster.Arn)
	ctx.Export("clusterEndpoint", cluster.Endpoint)
	ctx.Export("clusterVersion", cluster.Version)
	if clusterSG != nil {
		ctx.Export("clusterSecurityGroupId", clusterSG.ID())
	} else {
		ctx.Export("clusterSecurityGroupId", clusterSGId)
	}
	ctx.Export("clusterCertificateAuthority", cluster.CertificateAuthority.Data().Elem())

	// Kubeconfig
	kubeconfig := generateKubeconfig(cfg, cluster)
	ctx.Export("kubeconfig", kubeconfig)

	ctx.Export("estimatedMonthlyCost", pulumi.String(fmt.Sprintf("$%.2f USD",
		estimateEKSCostValue(cfg))))

	return nil
}

// ========================================
// FUNÇÕES AUXILIARES
// ========================================

func generateAZs(region string, count int) []string {
	azs := []string{
		fmt.Sprintf("%sa", region),
		fmt.Sprintf("%sb", region),
	}
	if count == 3 {
		azs = append(azs, fmt.Sprintf("%sc", region))
	}
	return azs
}

func createPublicSubnets(ctx *pulumi.Context, cfg *config.ClusterConfig, vpc *ec2.Vpc, igw *ec2.InternetGateway, azs []string) ([]*ec2.Subnet, error) {
	var publicSubnets []*ec2.Subnet
	for i, az := range azs {
		subnet, err := ec2.NewSubnet(ctx, fmt.Sprintf("%s-public-%d", cfg.Name, i), &ec2.SubnetArgs{
			VpcId:               vpc.ID(),
			CidrBlock:           pulumi.String(fmt.Sprintf("10.0.%d.0/24", 100+i)),
			AvailabilityZone:    pulumi.String(az),
			MapPublicIpOnLaunch: pulumi.Bool(true),
			Tags: pulumi.StringMap{
				"Name":                   pulumi.String(fmt.Sprintf("%s-public-%d", cfg.Name, i)),
				"kubernetes.io/role/elb": pulumi.String("1"),
				fmt.Sprintf("kubernetes.io/cluster/%s", cfg.Name): pulumi.String("shared"),
			},
		})
		if err != nil {
			return nil, err
		}
		publicSubnets = append(publicSubnets, subnet)
	}
	return publicSubnets, nil
}

func createPrivateSubnets(ctx *pulumi.Context, cfg *config.ClusterConfig, vpc *ec2.Vpc, azs []string) ([]*ec2.Subnet, error) {
	var privateSubnets []*ec2.Subnet
	for i, az := range azs {
		subnet, err := ec2.NewSubnet(ctx, fmt.Sprintf("%s-private-%d", cfg.Name, i), &ec2.SubnetArgs{
			VpcId:            vpc.ID(),
			CidrBlock:        pulumi.String(fmt.Sprintf("10.0.%d.0/24", i)),
			AvailabilityZone: pulumi.String(az),
			Tags: pulumi.StringMap{
				"Name":                            pulumi.String(fmt.Sprintf("%s-private-%d", cfg.Name, i)),
				"kubernetes.io/role/internal-elb": pulumi.String("1"),
				fmt.Sprintf("kubernetes.io/cluster/%s", cfg.Name): pulumi.String("shared"),
			},
		})
		if err != nil {
			return nil, err
		}
		privateSubnets = append(privateSubnets, subnet)
	}
	return privateSubnets, nil
}

func createNATGateways(ctx *pulumi.Context, cfg *config.ClusterConfig, publicSubnets []*ec2.Subnet, igw *ec2.InternetGateway) ([]*ec2.NatGateway, error) {
	var natGateways []*ec2.NatGateway
	for i, publicSubnet := range publicSubnets {
		// Elastic IP
		eip, err := ec2.NewEip(ctx, fmt.Sprintf("%s-nat-eip-%d", cfg.Name, i), &ec2.EipArgs{
			Domain: pulumi.String("vpc"),
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-nat-eip-%d", cfg.Name, i)),
			},
		}, pulumi.DependsOn([]pulumi.Resource{igw}))
		if err != nil {
			return nil, err
		}

		// NAT Gateway
		nat, err := ec2.NewNatGateway(ctx, fmt.Sprintf("%s-nat-%d", cfg.Name, i), &ec2.NatGatewayArgs{
			AllocationId: eip.ID(),
			SubnetId:     publicSubnet.ID(),
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-nat-%d", cfg.Name, i)),
			},
		}, pulumi.DependsOn([]pulumi.Resource{igw}))
		if err != nil {
			return nil, err
		}
		natGateways = append(natGateways, nat)
	}
	return natGateways, nil
}

func setupRouting(ctx *pulumi.Context, cfg *config.ClusterConfig, vpc *ec2.Vpc, publicSubnets []*ec2.Subnet, privateSubnets []*ec2.Subnet, igw *ec2.InternetGateway, natGateways []*ec2.NatGateway) error {
	// Route Table Pública
	publicRT, err := ec2.NewRouteTable(ctx, fmt.Sprintf("%s-public-rt", cfg.Name), &ec2.RouteTableArgs{
		VpcId: vpc.ID(),
		Tags: pulumi.StringMap{
			"Name": pulumi.String(fmt.Sprintf("%s-public-rt", cfg.Name)),
		},
	})
	if err != nil {
		return err
	}

	// Rota default pública
	_, err = ec2.NewRoute(ctx, fmt.Sprintf("%s-public-route", cfg.Name), &ec2.RouteArgs{
		RouteTableId:         publicRT.ID(),
		DestinationCidrBlock: pulumi.String("0.0.0.0/0"),
		GatewayId:            igw.ID(),
	})
	if err != nil {
		return err
	}

	// Associar subnets públicas
	for i, subnet := range publicSubnets {
		_, err := ec2.NewRouteTableAssociation(ctx, fmt.Sprintf("%s-public-rta-%d", cfg.Name, i), &ec2.RouteTableAssociationArgs{
			SubnetId:     subnet.ID(),
			RouteTableId: publicRT.ID(),
		})
		if err != nil {
			return err
		}
	}

	// Route Tables Privadas
	for i, subnet := range privateSubnets {
		rt, err := ec2.NewRouteTable(ctx, fmt.Sprintf("%s-private-rt-%d", cfg.Name, i), &ec2.RouteTableArgs{
			VpcId: vpc.ID(),
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-private-rt-%d", cfg.Name, i)),
			},
		})
		if err != nil {
			return err
		}

		_, err = ec2.NewRoute(ctx, fmt.Sprintf("%s-private-route-%d", cfg.Name, i), &ec2.RouteArgs{
			RouteTableId:         rt.ID(),
			DestinationCidrBlock: pulumi.String("0.0.0.0/0"),
			NatGatewayId:         natGateways[i].ID(),
		})
		if err != nil {
			return err
		}

		_, err = ec2.NewRouteTableAssociation(ctx, fmt.Sprintf("%s-private-rta-%d", cfg.Name, i), &ec2.RouteTableAssociationArgs{
			SubnetId:     subnet.ID(),
			RouteTableId: rt.ID(),
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func createClusterSecurityGroup(ctx *pulumi.Context, cfg *config.ClusterConfig, vpcId pulumi.StringInput) (*ec2.SecurityGroup, error) {
	return ec2.NewSecurityGroup(ctx, fmt.Sprintf("%s-cluster-sg", cfg.Name), &ec2.SecurityGroupArgs{
		VpcId:       vpcId,
		Description: pulumi.String(fmt.Sprintf("Security group for EKS cluster %s", cfg.Name)),
		Egress: ec2.SecurityGroupEgressArray{
			&ec2.SecurityGroupEgressArgs{
				FromPort: pulumi.Int(0),
				ToPort:   pulumi.Int(0),
				Protocol: pulumi.String("-1"),
				CidrBlocks: pulumi.StringArray{
					pulumi.String("0.0.0.0/0"),
				},
				Description: pulumi.String("Allow all outbound traffic"),
			},
		},
		Tags: pulumi.StringMap{
			"Name": pulumi.String(fmt.Sprintf("%s-cluster-sg", cfg.Name)),
		},
	})
}

func createClusterIAMRole(ctx *pulumi.Context, cfg *config.ClusterConfig) (*iam.Role, error) {
	clusterRole, err := iam.NewRole(ctx, fmt.Sprintf("%s-cluster-role", cfg.Name), &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(`{
                        "Version": "2012-10-17",
                        "Statement": [{
                                "Effect": "Allow",
                                "Principal": {
                                        "Service": "eks.amazonaws.com"
                                },
                                "Action": "sts:AssumeRole"
                        }]
                }`),
		Tags: pulumi.StringMap{
			"Name": pulumi.String(fmt.Sprintf("%s-cluster-role", cfg.Name)),
		},
	})
	if err != nil {
		return nil, err
	}

	_, err = iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("%s-cluster-policy", cfg.Name), &iam.RolePolicyAttachmentArgs{
		Role:      clusterRole.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"),
	})
	if err != nil {
		return nil, err
	}

	return clusterRole, nil
}

func buildClusterTags(cfg *config.ClusterConfig) pulumi.StringMap {
	tags := pulumi.StringMap{
		"Name":        pulumi.String(cfg.Name),
		"ManagedBy":   pulumi.String("chatcli-k8s-cloud"),
		"Environment": pulumi.String(cfg.Environment),
	}
	for k, v := range cfg.Tags {
		tags[k] = pulumi.String(v)
	}
	return tags
}

func createEKSCluster(ctx *pulumi.Context, cfg *config.ClusterConfig, vpcId pulumi.StringInput, subnetIds pulumi.StringArray) (*eks.Cluster, *ec2.SecurityGroup, error) {
	// Security Group
	clusterSG, err := createClusterSecurityGroup(ctx, cfg, vpcId)
	if err != nil {
		return nil, nil, err
	}

	// IAM Role
	clusterRole, err := createClusterIAMRole(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	// Tags
	clusterTags := buildClusterTags(cfg)

	// EKS Cluster
	cluster, err := eks.NewCluster(ctx, cfg.Name, &eks.ClusterArgs{
		Name:    pulumi.String(cfg.Name),
		Version: pulumi.String(cfg.K8sVersion),
		RoleArn: clusterRole.Arn,
		VpcConfig: &eks.ClusterVpcConfigArgs{
			SubnetIds:             subnetIds,
			SecurityGroupIds:      pulumi.StringArray{clusterSG.ID()},
			EndpointPrivateAccess: pulumi.Bool(true),
			EndpointPublicAccess:  pulumi.Bool(true),
		},
		EnabledClusterLogTypes: pulumi.StringArray{
			pulumi.String("api"),
			pulumi.String("audit"),
			pulumi.String("authenticator"),
		},
		Tags: clusterTags,
	}, pulumi.DependsOn([]pulumi.Resource{clusterRole}))

	return cluster, clusterSG, err
}

func createNodeGroup(ctx *pulumi.Context, cfg *config.ClusterConfig, cluster *eks.Cluster, subnetIds pulumi.StringArray) error {
	// IAM Role para nodes
	nodeRole, err := iam.NewRole(ctx, fmt.Sprintf("%s-node-role", cfg.Name), &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(`{
                        "Version": "2012-10-17",
                        "Statement": [{
                                "Effect": "Allow",
                                "Principal": {
                                        "Service": "ec2.amazonaws.com"
                                },
                                "Action": "sts:AssumeRole"
                        }]
                }`),
		Tags: pulumi.StringMap{
			"Name": pulumi.String(fmt.Sprintf("%s-node-role", cfg.Name)),
		},
	})
	if err != nil {
		return err
	}

	// Attach policies
	nodePolicies := []string{
		"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
		"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
		"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
		"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
	}

	for i, policyArn := range nodePolicies {
		_, err := iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("%s-node-policy-%d", cfg.Name, i), &iam.RolePolicyAttachmentArgs{
			Role:      nodeRole.Name,
			PolicyArn: pulumi.String(policyArn),
		})
		if err != nil {
			return err
		}
	}

	// Node labels
	var nodeLabels pulumi.StringMap
	if len(cfg.NodeConfig.Labels) > 0 {
		nodeLabels = make(pulumi.StringMap)
		for k, v := range cfg.NodeConfig.Labels {
			nodeLabels[k] = pulumi.String(v)
		}
	}

	// Node Group
	_, err = eks.NewNodeGroup(ctx, fmt.Sprintf("%s-nodes", cfg.Name), &eks.NodeGroupArgs{
		ClusterName:   cluster.Name,
		NodeGroupName: pulumi.String(fmt.Sprintf("%s-nodes", cfg.Name)),
		NodeRoleArn:   nodeRole.Arn,
		SubnetIds:     subnetIds,
		ScalingConfig: &eks.NodeGroupScalingConfigArgs{
			DesiredSize: pulumi.Int(cfg.NodeConfig.DesiredSize),
			MinSize:     pulumi.Int(cfg.NodeConfig.MinSize),
			MaxSize:     pulumi.Int(cfg.NodeConfig.MaxSize),
		},
		DiskSize: pulumi.Int(cfg.NodeConfig.DiskSize),
		InstanceTypes: pulumi.StringArray{
			pulumi.String(cfg.NodeConfig.InstanceType),
		},
		Labels: nodeLabels,
		Tags: pulumi.StringMap{
			"Name":      pulumi.String(fmt.Sprintf("%s-nodes", cfg.Name)),
			"ManagedBy": pulumi.String("chatcli-k8s-cloud"),
		},
		UpdateConfig: &eks.NodeGroupUpdateConfigArgs{
			MaxUnavailable: pulumi.Int(1),
		},
	}, pulumi.DependsOn([]pulumi.Resource{nodeRole, cluster}))

	return err
}

func generateKubeconfig(cfg *config.ClusterConfig, cluster *eks.Cluster) pulumi.StringOutput {
	return pulumi.Sprintf(`apiVersion: v1
    clusters:
    - cluster:
        certificate-authority-data: %s
        server: %s
      name: %s
    contexts:
    - context:
        cluster: %s
        user: %s
      name: %s
    current-context: %s
    kind: Config
    preferences: {}
    users:
    - name: %s
      user:
        exec:
          apiVersion: client.authentication.k8s.io/v1beta1
          command: aws
          args:
            - eks
            - get-token
            - --cluster-name
            - %s
            - --region
            - %s
    `,
		cluster.CertificateAuthority.Data().Elem(),
		cluster.Endpoint,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		pulumi.String(cfg.Region),
	)
}

func exportOutputs(ctx *pulumi.Context, cfg *config.ClusterConfig, vpcId pulumi.IDOutput, publicSubnets []*ec2.Subnet, privateSubnets []*ec2.Subnet, natGateways []*ec2.NatGateway, cluster *eks.Cluster, clusterSG *ec2.SecurityGroup) error {
	// VPC
	ctx.Export("vpcId", vpcId)

	// Subnets
	var publicSubnetIDsOutput pulumi.StringArray
	for _, subnet := range publicSubnets {
		publicSubnetIDsOutput = append(publicSubnetIDsOutput, subnet.ID())
	}
	ctx.Export("publicSubnetIds", publicSubnetIDsOutput)

	var privateSubnetIDsOutput pulumi.StringArray
	for _, subnet := range privateSubnets {
		privateSubnetIDsOutput = append(privateSubnetIDsOutput, subnet.ID())
	}
	ctx.Export("privateSubnetIds", privateSubnetIDsOutput)

	// NAT Gateways
	var natGatewayIDsOutput pulumi.StringArray
	for _, nat := range natGateways {
		natGatewayIDsOutput = append(natGatewayIDsOutput, nat.ID())
	}
	ctx.Export("natGatewayIds", natGatewayIDsOutput)

	// EKS Cluster
	ctx.Export("clusterName", cluster.Name)
	ctx.Export("clusterArn", cluster.Arn)
	ctx.Export("clusterEndpoint", cluster.Endpoint)
	ctx.Export("clusterVersion", cluster.Version)
	ctx.Export("clusterSecurityGroupId", clusterSG.ID())
	ctx.Export("clusterCertificateAuthority", cluster.CertificateAuthority.Data().Elem())

	// Kubeconfig
	kubeconfig := generateKubeconfig(cfg, cluster)
	ctx.Export("kubeconfig", kubeconfig)

	// Custo estimado
	ctx.Export("estimatedMonthlyCost", pulumi.String(fmt.Sprintf("$%.2f USD",
		estimateEKSCostValue(cfg))))

	return nil
}

func estimateEKSCostValue(cfg *config.ClusterConfig) float64 {
	const (
		eksClusterCost = 73.00
		natGatewayCost = 32.85
		dataTransfer   = 10.00
	)

	nodeCost := 0.0
	switch cfg.NodeConfig.InstanceType {
	case "t3.small":
		nodeCost = 15.18
	case "t3.medium":
		nodeCost = 30.37
	case "t3.large":
		nodeCost = 60.74
	case "m5.large":
		nodeCost = 70.08
	case "m5.xlarge":
		nodeCost = 140.16
	default:
		nodeCost = 50.00
	}

	totalNodeCost := nodeCost * float64(cfg.NodeConfig.DesiredSize)

	// Apenas conta NAT se modo auto
	natCost := 0.0
	if cfg.NetworkConfig.Mode == "auto" {
		natCost = natGatewayCost * float64(cfg.NetworkConfig.AvailabilityZones)
	}

	return eksClusterCost + natCost + totalNodeCost + dataTransfer
}
