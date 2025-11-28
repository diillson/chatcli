package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
	k8stypes "github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
)

// EKSManager gerencia clusters EKS
type EKSManager struct {
	eksClient   *eks.Client
	clusterName string
	region      string
}

// NewEKSManager cria um novo gerenciador EKS
func NewEKSManager(eksClient *eks.Client, clusterName, region string) *EKSManager {
	return &EKSManager{
		eksClient:   eksClient,
		clusterName: clusterName,
		region:      region,
	}
}

// EKSResources recursos EKS criados
type EKSResources struct {
	Cluster    ClusterResource     `json:"cluster"`
	NodeGroups []NodeGroupResource `json:"nodeGroups"`
}

// ClusterResource representa um cluster EKS
type ClusterResource struct {
	Name                 string    `json:"name"`
	ARN                  string    `json:"arn"`
	Version              string    `json:"version"`
	Endpoint             string    `json:"endpoint"`
	CertificateAuthority string    `json:"certificateAuthority"`
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"createdAt"`
}

// NodeGroupResource representa um node group
type NodeGroupResource struct {
	Name          string                 `json:"name"`
	ARN           string                 `json:"arn"`
	Status        string                 `json:"status"`
	InstanceTypes []string               `json:"instanceTypes"`
	ScalingConfig NodeGroupScalingConfig `json:"scalingConfig"`
	CreatedAt     time.Time              `json:"createdAt"`
}

// NodeGroupScalingConfig configuração de scaling
type NodeGroupScalingConfig struct {
	MinSize     int `json:"minSize"`
	MaxSize     int `json:"maxSize"`
	DesiredSize int `json:"desiredSize"`
}

// CreateCluster cria um cluster EKS
func (em *EKSManager) CreateCluster(ctx context.Context,
	config k8stypes.ClusterConfig,
	networking *NetworkingResources,
	iamResources *IAMResources) (*EKSResources, error) {

	logger.Info("☸️  Criando cluster EKS...")

	resources := &EKSResources{}

	// 1. Criar cluster
	cluster, err := em.createEKSCluster(ctx, config, networking, iamResources)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar cluster: %w", err)
	}
	resources.Cluster = *cluster
	logger.Successf("   ✓ Cluster criado: %s", cluster.Name)

	// 2. Aguardar cluster ficar ativo
	logger.Progress("   ⏳ Aguardando cluster ficar ativo (pode levar 10-15 minutos)...")
	if err := em.waitForClusterActive(ctx); err != nil {
		return nil, fmt.Errorf("timeout aguardando cluster: %w", err)
	}
	logger.Success("   ✓ Cluster ativo!")

	// 3. Criar node group
	logger.Progress("   ⏳ Criando node group...")
	nodeGroup, err := em.createNodeGroup(ctx, config, networking, iamResources)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar node group: %w", err)
	}
	resources.NodeGroups = []NodeGroupResource{*nodeGroup}
	logger.Successf("   ✓ Node group criado: %s", nodeGroup.Name)

	// 4. Aguardar nodes ficarem ativos
	logger.Progress("   ⏳ Aguardando nodes ficarem ativos (pode levar 5-10 minutos)...")
	if err := em.waitForNodeGroupActive(ctx, nodeGroup.Name); err != nil {
		return nil, fmt.Errorf("timeout aguardando nodes: %w", err)
	}
	logger.Success("   ✓ Nodes ativos!")

	logger.Success("✅ Cluster EKS criado com sucesso!")
	return resources, nil
}

// createEKSCluster cria o control plane do EKS
func (em *EKSManager) createEKSCluster(ctx context.Context,
	config k8stypes.ClusterConfig,
	networking *NetworkingResources,
	iamResources *IAMResources) (*ClusterResource, error) {

	// Coletar IDs das subnets privadas
	var subnetIDs []string
	for _, subnet := range networking.PrivateSubnets {
		subnetIDs = append(subnetIDs, subnet.ID)
	}

	// Adicionar subnets públicas também (para ELB)
	for _, subnet := range networking.PublicSubnets {
		subnetIDs = append(subnetIDs, subnet.ID)
	}

	// Coletar IDs dos security groups
	var securityGroupIDs []string
	for _, sg := range networking.SecurityGroups {
		if sg.Name == fmt.Sprintf("%s-cluster-sg", em.clusterName) {
			securityGroupIDs = append(securityGroupIDs, sg.ID)
		}
	}

	// Criar cluster
	result, err := em.eksClient.CreateCluster(ctx, &eks.CreateClusterInput{
		Name:    aws.String(em.clusterName),
		Version: aws.String(config.K8sVersion),
		RoleArn: aws.String(iamResources.ClusterRole.ARN),

		ResourcesVpcConfig: &types.VpcConfigRequest{
			SubnetIds:             subnetIDs,
			SecurityGroupIds:      securityGroupIDs,
			EndpointPublicAccess:  aws.Bool(true),
			EndpointPrivateAccess: aws.Bool(true),
		},

		Logging: &types.Logging{
			ClusterLogging: []types.LogSetup{
				{
					Enabled: aws.Bool(true),
					Types: []types.LogType{
						types.LogTypeApi,
						types.LogTypeAudit,
						types.LogTypeAuthenticator,
						types.LogTypeControllerManager,
						types.LogTypeScheduler,
					},
				},
			},
		},

		Tags: map[string]string{
			"ManagedBy":   "chatcli-k8s-cloud",
			"Environment": config.Environment,
		},
	})

	if err != nil {
		return nil, err
	}

	return &ClusterResource{
		Name:      *result.Cluster.Name,
		ARN:       *result.Cluster.Arn,
		Version:   *result.Cluster.Version,
		Status:    string(result.Cluster.Status),
		CreatedAt: *result.Cluster.CreatedAt,
	}, nil
}

// createNodeGroup cria um node group
func (em *EKSManager) createNodeGroup(ctx context.Context,
	config k8stypes.ClusterConfig,
	networking *NetworkingResources,
	iamResources *IAMResources) (*NodeGroupResource, error) {

	nodeGroupName := fmt.Sprintf("%s-nodes", em.clusterName)

	// Coletar IDs das subnets privadas apenas (nodes devem estar em privadas)
	var subnetIDs []string
	for _, subnet := range networking.PrivateSubnets {
		subnetIDs = append(subnetIDs, subnet.ID)
	}

	// Criar node group
	result, err := em.eksClient.CreateNodegroup(ctx, &eks.CreateNodegroupInput{
		ClusterName:   aws.String(em.clusterName),
		NodegroupName: aws.String(nodeGroupName),
		NodeRole:      aws.String(iamResources.NodeRole.ARN),
		Subnets:       subnetIDs,

		ScalingConfig: &types.NodegroupScalingConfig{
			MinSize:     aws.Int32(int32(config.NodeConfig.MinSize)),
			MaxSize:     aws.Int32(int32(config.NodeConfig.MaxSize)),
			DesiredSize: aws.Int32(int32(config.NodeConfig.DesiredSize)),
		},

		InstanceTypes: []string{config.NodeConfig.InstanceType},

		DiskSize: aws.Int32(int32(config.NodeConfig.DiskSize)),

		AmiType: types.AMITypesAl2X8664, // Amazon Linux 2

		Labels: config.NodeConfig.Labels,

		Tags: map[string]string{
			"ManagedBy":   "chatcli-k8s-cloud",
			"Environment": config.Environment,
		},
	})

	if err != nil {
		return nil, err
	}

	return &NodeGroupResource{
		Name:          *result.Nodegroup.NodegroupName,
		ARN:           *result.Nodegroup.NodegroupArn,
		Status:        string(result.Nodegroup.Status),
		InstanceTypes: result.Nodegroup.InstanceTypes,
		ScalingConfig: NodeGroupScalingConfig{
			MinSize:     int(*result.Nodegroup.ScalingConfig.MinSize),
			MaxSize:     int(*result.Nodegroup.ScalingConfig.MaxSize),
			DesiredSize: int(*result.Nodegroup.ScalingConfig.DesiredSize),
		},
		CreatedAt: *result.Nodegroup.CreatedAt,
	}, nil
}

// waitForClusterActive aguarda cluster ficar ativo
func (em *EKSManager) waitForClusterActive(ctx context.Context) error {
	waiter := eks.NewClusterActiveWaiter(em.eksClient)
	return waiter.Wait(ctx, &eks.DescribeClusterInput{
		Name: aws.String(em.clusterName),
	}, 20*time.Minute)
}

// waitForNodeGroupActive aguarda node group ficar ativo
func (em *EKSManager) waitForNodeGroupActive(ctx context.Context, nodeGroupName string) error {
	waiter := eks.NewNodegroupActiveWaiter(em.eksClient)
	return waiter.Wait(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(em.clusterName),
		NodegroupName: aws.String(nodeGroupName),
	}, 15*time.Minute)
}

// GetClusterInfo obtém informações do cluster
func (em *EKSManager) GetClusterInfo(ctx context.Context) (*ClusterResource, error) {
	result, err := em.eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(em.clusterName),
	})

	if err != nil {
		return nil, err
	}

	cluster := result.Cluster

	var ca string
	if cluster.CertificateAuthority != nil {
		ca = *cluster.CertificateAuthority.Data
	}

	return &ClusterResource{
		Name:                 *cluster.Name,
		ARN:                  *cluster.Arn,
		Version:              *cluster.Version,
		Endpoint:             *cluster.Endpoint,
		CertificateAuthority: ca,
		Status:               string(cluster.Status),
		CreatedAt:            *cluster.CreatedAt,
	}, nil
}

// GenerateKubeconfig gera arquivo kubeconfig
func (em *EKSManager) GenerateKubeconfig(cluster *ClusterResource) (string, error) {
	// Template kubeconfig (usar CA direto como base64)
	kubeconfig := fmt.Sprintf(`apiVersion: v1
    kind: Config
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
		cluster.CertificateAuthority,
		cluster.Endpoint,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		cluster.Name,
		em.region,
	)

	return kubeconfig, nil
}

// DeleteCluster remove um cluster EKS
func (em *EKSManager) DeleteCluster(ctx context.Context, resources *EKSResources) error {
	logger.Info("☸️  Removendo cluster EKS...")

	// 1. Remover node groups
	for _, ng := range resources.NodeGroups {
		logger.Progressf("   ⏳ Removendo node group %s...", ng.Name)
		_, err := em.eksClient.DeleteNodegroup(ctx, &eks.DeleteNodegroupInput{
			ClusterName:   aws.String(em.clusterName),
			NodegroupName: aws.String(ng.Name),
		})

		if err != nil {
			logger.Warningf("   ⚠️  Erro ao remover node group: %v", err)
		}

		// Aguardar node group ser removido
		waiter := eks.NewNodegroupDeletedWaiter(em.eksClient)
		_ = waiter.Wait(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(em.clusterName),
			NodegroupName: aws.String(ng.Name),
		}, 15*time.Minute)
	}

	// 2. Remover cluster
	logger.Progressf("   ⏳ Removendo cluster %s...", em.clusterName)
	_, err := em.eksClient.DeleteCluster(ctx, &eks.DeleteClusterInput{
		Name: aws.String(em.clusterName),
	})

	if err != nil {
		return fmt.Errorf("falha ao remover cluster: %w", err)
	}

	// Aguardar cluster ser removido
	waiter := eks.NewClusterDeletedWaiter(em.eksClient)
	if err := waiter.Wait(ctx, &eks.DescribeClusterInput{
		Name: aws.String(em.clusterName),
	}, 15*time.Minute); err != nil {
		return fmt.Errorf("timeout aguardando remoção do cluster: %w", err)
	}

	logger.Success("✅ Cluster EKS removido!")
	return nil
}

// UpdateNodeGroup atualiza configuração do node group
func (em *EKSManager) UpdateNodeGroup(ctx context.Context, nodeGroupName string,
	minSize, maxSize, desiredSize int) error {

	logger.Infof("🔄 Atualizando node group %s...", nodeGroupName)

	_, err := em.eksClient.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
		ClusterName:   aws.String(em.clusterName),
		NodegroupName: aws.String(nodeGroupName),
		ScalingConfig: &types.NodegroupScalingConfig{
			MinSize:     aws.Int32(int32(minSize)),
			MaxSize:     aws.Int32(int32(maxSize)),
			DesiredSize: aws.Int32(int32(desiredSize)),
		},
	})

	if err != nil {
		return fmt.Errorf("falha ao atualizar node group: %w", err)
	}

	logger.Success("✅ Node group atualizado!")
	return nil
}

// UpdateClusterVersion atualiza a versão do Kubernetes
func (em *EKSManager) UpdateClusterVersion(ctx context.Context, version string) error {
	logger.Infof("🔄 Atualizando cluster para versão %s...", version)

	_, err := em.eksClient.UpdateClusterVersion(ctx, &eks.UpdateClusterVersionInput{
		Name:    aws.String(em.clusterName),
		Version: aws.String(version),
	})

	if err != nil {
		return fmt.Errorf("falha ao atualizar versão: %w", err)
	}

	// Aguardar update
	logger.Progress("   ⏳ Aguardando update completar (pode levar 20-30 minutos)...")
	waiter := eks.NewClusterActiveWaiter(em.eksClient)
	if err := waiter.Wait(ctx, &eks.DescribeClusterInput{
		Name: aws.String(em.clusterName),
	}, 40*time.Minute); err != nil {
		return fmt.Errorf("timeout aguardando update: %w", err)
	}

	logger.Success("✅ Versão do cluster atualizada!")
	return nil
}
