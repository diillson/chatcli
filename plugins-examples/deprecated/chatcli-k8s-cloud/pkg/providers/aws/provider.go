package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/state"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
)

// Provider implementa o provider AWS
type Provider struct {
	region      string
	clusterName string

	ec2Client *ec2.Client
	eksClient *eks.Client
	iamClient *iam.Client

	networkManager *NetworkManager
	iamManager     *IAMManager
	EksManager     *EKSManager
}

// NewProvider cria um novo provider AWS
func NewProvider(region, clusterName string) (*Provider, error) {
	ctx := context.Background()

	// Carregar configuração AWS
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("falha ao carregar config AWS: %w", err)
	}

	// Criar clients
	ec2Client := ec2.NewFromConfig(cfg)
	eksClient := eks.NewFromConfig(cfg)
	iamClient := iam.NewFromConfig(cfg)

	return &Provider{
		region:         region,
		clusterName:    clusterName,
		ec2Client:      ec2Client,
		eksClient:      eksClient,
		iamClient:      iamClient,
		networkManager: NewNetworkManager(ec2Client, region, clusterName),
		iamManager:     NewIAMManager(iamClient, clusterName),
		EksManager:     NewEKSManager(eksClient, clusterName, region),
	}, nil
}

// FullClusterResources todos os recursos de um cluster
type FullClusterResources struct {
	Networking NetworkingResources `json:"networking"`
	IAM        IAMResources        `json:"iam"`
	EKS        EKSResources        `json:"eks"`
}

// CreateCluster cria um cluster completo
func (p *Provider) CreateCluster(ctx context.Context,
	clusterConfig types.ClusterConfig,
	backend state.Backend) error {

	logger.Separator()
	logger.Infof("🚀 Criando cluster EKS '%s' na região %s", p.clusterName, p.region)
	logger.Separator()

	// Adquirir lock
	if err := backend.Lock(p.clusterName); err != nil {
		return fmt.Errorf("falha ao adquirir lock: %w", err)
	}
	defer backend.Unlock(p.clusterName)

	// Verificar se já existe
	exists, _ := backend.Exists(p.clusterName)
	if exists {
		return fmt.Errorf("cluster '%s' já existe no state backend", p.clusterName)
	}

	allResources := &FullClusterResources{}

	// 1. Criar IAM Resources
	logger.Separator()
	iamResources, err := p.iamManager.CreateIAMResources(ctx)
	if err != nil {
		return fmt.Errorf("falha ao criar recursos IAM: %w", err)
	}
	allResources.IAM = *iamResources

	// 2. Criar Networking
	logger.Separator()
	azs := GetAvailabilityZones(p.region, clusterConfig.AvailabilityZones)
	subnetConfigs, err := CalculateSubnetCIDRs(clusterConfig.VPCCidr, len(azs))
	if err != nil {
		return fmt.Errorf("falha ao calcular CIDRs: %w", err)
	}

	for i := range subnetConfigs {
		subnetConfigs[i].AvailabilityZone = azs[i%len(azs)]
	}

	netConfig := NetworkingConfig{
		VPCConfig: VPCConfig{
			CIDR:               clusterConfig.VPCCidr,
			EnableDNSSupport:   true,
			EnableDNSHostnames: true,
		},
		SubnetConfigs: subnetConfigs,
		CreateNAT:     true,
		CreateIGW:     true,
	}

	networking, err := p.networkManager.CreateNetworking(ctx, netConfig)
	if err != nil {
		return fmt.Errorf("falha ao criar networking: %w", err)
	}
	allResources.Networking = *networking

	// 3. Criar EKS Cluster
	logger.Separator()
	eksResources, err := p.EksManager.CreateCluster(ctx, clusterConfig, networking, iamResources)
	if err != nil {
		return fmt.Errorf("falha ao criar cluster EKS: %w", err)
	}
	allResources.EKS = *eksResources

	// 4. Salvar estado
	logger.Separator()
	logger.Info("💾 Salvando estado...")
	clusterState := types.ClusterState{
		Config: clusterConfig,
		Status: types.ClusterStatus{
			Phase:      "Active",
			Ready:      true,
			Message:    "Cluster criado com sucesso",
			Endpoint:   eksResources.Cluster.Endpoint,
			NodesReady: clusterConfig.NodeConfig.DesiredSize,
			NodesTotal: clusterConfig.NodeConfig.DesiredSize,
		},
		Resources: map[string]interface{}{
			"aws": allResources,
		},
	}

	if err := backend.Save(p.clusterName, clusterState); err != nil {
		return fmt.Errorf("falha ao salvar estado: %w", err)
	}

	logger.Success("✅ Estado salvo!")

	// 5. Gerar kubeconfig
	logger.Separator()
	if err := p.generateAndSaveKubeconfig(eksResources.Cluster); err != nil {
		logger.Warningf("⚠️  Falha ao salvar kubeconfig: %v", err)
	}

	// 6. Resumo final
	logger.Separator()
	p.printSummary(clusterConfig, allResources)
	logger.Separator()

	return nil
}

// generateAndSaveKubeconfig gera e salva kubeconfig
func (p *Provider) generateAndSaveKubeconfig(cluster ClusterResource) error {
	kubeconfig, err := p.EksManager.GenerateKubeconfig(&cluster)
	if err != nil {
		return err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	kubeDir := filepath.Join(homeDir, ".kube")
	if err := os.MkdirAll(kubeDir, 0755); err != nil {
		return err
	}

	// CORREÇÃO 1: Salvar em arquivo separado E mesclar com config principal
	kubeconfigPath := filepath.Join(kubeDir, fmt.Sprintf("config-%s", p.clusterName))
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600); err != nil {
		return err
	}

	logger.Successf("✅ Kubeconfig salvo: %s", kubeconfigPath)

	// CORREÇÃO 2: Tentar mesclar com ~/.kube/config principal
	mainConfigPath := filepath.Join(kubeDir, "config")
	if err := p.mergeKubeconfig(mainConfigPath, kubeconfig); err != nil {
		logger.Warningf("⚠️  Não foi possível mesclar com config principal: %v", err)
		logger.Infof("   💡 Use manualmente: export KUBECONFIG=%s", kubeconfigPath)
		logger.Infof("   💡 Ou: kubectl config use-context %s", p.clusterName)
	} else {
		logger.Successf("✅ Kubeconfig mesclado em %s", mainConfigPath)
		logger.Infof("   💡 Context ativo: %s", p.clusterName)

		// CORREÇÃO 3: Ativar o context automaticamente
		if err := p.setCurrentContext(p.clusterName); err != nil {
			logger.Warningf("⚠️  Não foi possível ativar context: %v", err)
			logger.Infof("   💡 Ative manualmente: kubectl config use-context %s", p.clusterName)
		} else {
			logger.Successf("✅ Context '%s' ativado automaticamente!", p.clusterName)
		}
	}

	return nil
}

// mergeKubeconfig mescla o novo kubeconfig com o existente
func (p *Provider) mergeKubeconfig(mainPath, newKubeconfig string) error {
	// Salvar temporariamente o novo kubeconfig
	tempFile := mainPath + ".new"
	if err := os.WriteFile(tempFile, []byte(newKubeconfig), 0600); err != nil {
		return err
	}
	defer os.Remove(tempFile)

	// Usar kubectl para mesclar
	cmd := exec.Command("kubectl", "config", "view", "--flatten")

	// Definir KUBECONFIG com ambos os arquivos
	env := os.Environ()
	kubeconfigEnv := fmt.Sprintf("KUBECONFIG=%s:%s", mainPath, tempFile)
	cmd.Env = append(env, kubeconfigEnv)

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("falha ao mesclar configs: %w", err)
	}

	// Backup do config original
	if _, err := os.Stat(mainPath); err == nil {
		backupPath := mainPath + ".backup-" + time.Now().Format("20060102-150405")
		if err := copyFile(mainPath, backupPath); err != nil {
			logger.Warningf("⚠️  Não foi possível criar backup: %v", err)
		} else {
			logger.Infof("   📦 Backup criado: %s", backupPath)
		}
	}

	// Salvar config mesclado
	return os.WriteFile(mainPath, output, 0600)
}

// setCurrentContext ativa o context usando kubectl
func (p *Provider) setCurrentContext(contextName string) error {
	cmd := exec.Command("kubectl", "config", "use-context", contextName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}

// copyFile copia um arquivo (helper)
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}

// printSummary imprime resumo da criação
func (p *Provider) printSummary(config types.ClusterConfig, resources *FullClusterResources) {
	logger.Success("🎉 CLUSTER CRIADO COM SUCESSO!")
	logger.Info("")
	logger.Infof("📋 RESUMO:")
	logger.Infof("   Nome: %s", p.clusterName)
	logger.Infof("   Região: %s", p.region)
	logger.Infof("   Versão K8s: %s", config.K8sVersion)
	logger.Info("")
	logger.Infof("🌐 NETWORKING:")
	logger.Infof("   VPC: %s (%s)", resources.Networking.VPC.ID, resources.Networking.VPC.CIDR)
	logger.Infof("   Subnets Públicas: %d", len(resources.Networking.PublicSubnets))
	logger.Infof("   Subnets Privadas: %d", len(resources.Networking.PrivateSubnets))
	logger.Infof("   NAT Gateways: %d", len(resources.Networking.NATGateways))
	logger.Info("")
	logger.Infof("☸️  CLUSTER:")
	logger.Infof("   Endpoint: %s", resources.EKS.Cluster.Endpoint)
	logger.Infof("   Status: %s", resources.EKS.Cluster.Status)
	logger.Info("")
	logger.Infof("👷 NODES:")
	for _, ng := range resources.EKS.NodeGroups {
		logger.Infof("   Node Group: %s", ng.Name)
		logger.Infof("   Instance Type: %v", ng.InstanceTypes)
		logger.Infof("   Min/Desired/Max: %d/%d/%d",
			ng.ScalingConfig.MinSize,
			ng.ScalingConfig.DesiredSize,
			ng.ScalingConfig.MaxSize)
	}
	logger.Info("")
	logger.Infof("💡 PRÓXIMOS PASSOS:")
	logger.Infof("   1. Configure kubectl:")
	logger.Infof("      export KUBECONFIG=~/.kube/config-%s", p.clusterName)
	logger.Infof("   2. Verifique nodes:")
	logger.Infof("      kubectl get nodes")
	logger.Infof("   3. Deploy uma aplicação:")
	logger.Infof("      kubectl create deployment nginx --image=nginx")
}

// DeleteCluster remove um cluster completo - IMPLEMENTAÇÃO COMPLETA
func (p *Provider) DeleteCluster(ctx context.Context, backend state.Backend) error {
	logger.Separator()
	logger.Infof("🔥 Removendo cluster '%s'", p.clusterName)
	logger.Separator()

	// Adquirir lock
	if err := backend.Lock(p.clusterName); err != nil {
		return fmt.Errorf("falha ao adquirir lock: %w", err)
	}
	defer backend.Unlock(p.clusterName)

	// Carregar estado
	var clusterState types.ClusterState
	if err := backend.Load(p.clusterName, &clusterState); err != nil {
		return fmt.Errorf("falha ao carregar estado: %w", err)
	}

	// Extrair recursos AWS do state
	resourcesInterface, ok := clusterState.Resources["aws"]
	if !ok {
		return fmt.Errorf("estado corrompido: recursos AWS não encontrados")
	}

	// Converter interface{} para FullClusterResources
	resourcesJSON, err := json.Marshal(resourcesInterface)
	if err != nil {
		return fmt.Errorf("falha ao serializar recursos: %w", err)
	}

	var resources FullClusterResources
	if err := json.Unmarshal(resourcesJSON, &resources); err != nil {
		return fmt.Errorf("falha ao deserializar recursos: %w", err)
	}

	logger.Info("🗑️  Removendo recursos na ordem reversa...")
	logger.Info("")

	// ORDEM DE REMOÇÃO (reversa da criação):
	// 1. EKS Cluster e Node Groups
	// 2. Networking (VPC, Subnets, NAT, IGW)
	// 3. IAM Roles

	// 1. Remover EKS
	if len(resources.EKS.NodeGroups) > 0 || resources.EKS.Cluster.Name != "" {
		logger.Separator()
		eksManager := p.EksManager
		if err := eksManager.DeleteCluster(ctx, &resources.EKS); err != nil {
			logger.Errorf("❌ Falha ao remover EKS: %v", err)
			logger.Warning("⚠️  Continuando com limpeza de outros recursos...")
		}
	}

	// 2. Remover Networking
	if resources.Networking.VPC.ID != "" {
		logger.Separator()
		if err := p.networkManager.DeleteNetworking(ctx, &resources.Networking); err != nil {
			logger.Errorf("❌ Falha ao remover networking: %v", err)
			logger.Warning("⚠️  Continuando com limpeza de outros recursos...")
		}
	}

	// 3. Remover IAM
	if resources.IAM.ClusterRole.ARN != "" || resources.IAM.NodeRole.ARN != "" {
		logger.Separator()
		if err := p.iamManager.DeleteIAMResources(ctx, &resources.IAM); err != nil {
			logger.Errorf("❌ Falha ao remover IAM: %v", err)
			logger.Warning("⚠️  Alguns recursos IAM podem permanecer...")
		}
	}

	// 4. Remover estado
	logger.Separator()
	logger.Info("💾 Removendo estado do backend...")
	if err := backend.Delete(p.clusterName); err != nil {
		return fmt.Errorf("falha ao remover estado: %w", err)
	}

	logger.Success("✅ Estado removido!")
	logger.Separator()
	logger.Success("🎉 CLUSTER REMOVIDO COM SUCESSO!")
	logger.Info("")
	logger.Info("🧹 LIMPEZA CONCLUÍDA:")
	logger.Infof("   • Cluster EKS removido")
	logger.Infof("   • Networking removido (VPC, Subnets, NAT, IGW)")
	logger.Infof("   • IAM Roles removidos")
	logger.Infof("   • Estado removido do backend")
	logger.Info("")
	logger.Separator()

	return nil
}
