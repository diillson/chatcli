package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/pulumi"
	"github.com/spf13/cobra"
)

var createFlags struct {
	name             string
	region           string
	k8sVersion       string
	nodeInstanceType string
	nodeCount        int
	nodeMinSize      int
	nodeMaxSize      int

	// Flags de backend
	stateBackend    string
	stateBucketName string

	// FLAGS DE REDE
	networkMode      string   // auto, mixed, byo
	vpcId            string   // VPC existente
	vpcCidr          string   // CIDR para VPC nova
	publicSubnetIds  []string // Subnets públicas existentes
	privateSubnetIds []string // Subnets privadas existentes
	clusterSgId      string   // Security Group do cluster
	nodeSgId         string   // Security Group dos nodes
	azCount          int      // Número de AZs

	acceptCosts   bool
	confirmCreate bool
}

var CreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Cria um novo cluster Kubernetes",
}

var createEKSCmd = &cobra.Command{
	Use:   "eks",
	Short: "Cria cluster EKS na AWS",
	Long: `Cria um cluster EKS (Elastic Kubernetes Service) na AWS com:
      • VPC multi-AZ com subnets públicas e privadas
      • NAT Gateways para alta disponibilidade
      • Node Group gerenciado com auto-scaling
      • Logging habilitado (CloudWatch)
      • Security Groups configurados
      • State backend auto-gerenciado com guardrails
    
    BACKEND (escolha uma opção):
      1. AUTO (padrão) - Gera bucket único automaticamente
         Formato: k8s-cloud-{account-id}-{region}-{random}
      
      2. --state-bucket meu-bucket - Usa/cria bucket com nome específico
      
      3. --state-backend s3://bucket/path - Usa URL completa
    
    EXEMPLO:
      # AUTO (recomendado) - backend gerado automaticamente
      @k8s-cloud create eks --name prod --region us-east-1 --dry-run
      
      # Com bucket específico
      @k8s-cloud create eks --name prod --region us-east-1 --state-bucket my-company-k8s --dry-run
      
      # Criar de verdade
      @k8s-cloud create eks --name prod --region us-east-1 --accept-costs --confirm-create`,
	RunE: runCreateEKS,
}

func init() {
	CreateCmd.AddCommand(createEKSCmd)

	// Flags obrigatórias
	createEKSCmd.Flags().StringVar(&createFlags.name, "name", "",
		"Nome do cluster (obrigatório)")
	createEKSCmd.MarkFlagRequired("name")

	createEKSCmd.Flags().StringVar(&createFlags.region, "region", "",
		"Região AWS (obrigatório)")
	createEKSCmd.MarkFlagRequired("region")

	// Flags de backend (TODAS OPCIONAIS AGORA)
	createEKSCmd.Flags().StringVar(&createFlags.stateBackend, "state-backend", "",
		"Backend S3 completo (ex: s3://bucket/path) [opcional]")

	createEKSCmd.Flags().StringVar(&createFlags.stateBucketName, "state-bucket", "",
		"Nome do bucket S3 para state (será criado se não existir) [opcional]")

	// Flags opcionais de cluster
	createEKSCmd.Flags().StringVar(&createFlags.k8sVersion, "k8s-version", "1.30",
		"Versão do Kubernetes")
	createEKSCmd.Flags().StringVar(&createFlags.nodeInstanceType, "node-instance-type", "t3.medium",
		"Tipo de instância EC2 para nodes")
	createEKSCmd.Flags().IntVar(&createFlags.nodeCount, "node-count", 3,
		"Quantidade desejada de nodes")
	createEKSCmd.Flags().IntVar(&createFlags.nodeMinSize, "node-min-size", 1,
		"Quantidade mínima de nodes")
	createEKSCmd.Flags().IntVar(&createFlags.nodeMaxSize, "node-max-size", 10,
		"Quantidade máxima de nodes")

	// ✅ MANTER APENAS UMA VEZ
	createEKSCmd.Flags().IntVar(&createFlags.azCount, "availability-zones", 3,
		"Número de AZs (2 ou 3)")

	// Flags de confirmação
	createEKSCmd.Flags().BoolVar(&createFlags.acceptCosts, "accept-costs", false,
		"Aceita custos estimados (obrigatório para criar)")
	createEKSCmd.Flags().BoolVar(&createFlags.confirmCreate, "confirm-create", false,
		"Confirma criação do cluster (obrigatório para criar)")

	// Flags de Rede
	createEKSCmd.Flags().StringVar(&createFlags.networkMode, "network-mode", "auto",
		"Modo de rede: auto (cria tudo), mixed (VPC existente), byo (tudo existente)")

	createEKSCmd.Flags().StringVar(&createFlags.vpcId, "vpc-id", "",
		"ID da VPC existente (modo mixed/byo)")

	createEKSCmd.Flags().StringVar(&createFlags.vpcCidr, "vpc-cidr", "10.0.0.0/16",
		"CIDR para VPC nova (modo auto)")

	createEKSCmd.Flags().StringSliceVar(&createFlags.publicSubnetIds, "public-subnet-ids", nil,
		"IDs de subnets públicas existentes (modo byo)")

	createEKSCmd.Flags().StringSliceVar(&createFlags.privateSubnetIds, "private-subnet-ids", nil,
		"IDs de subnets privadas existentes (modo mixed/byo)")

	createEKSCmd.Flags().StringVar(&createFlags.clusterSgId, "cluster-sg-id", "",
		"ID do Security Group do cluster (modo byo)")

	createEKSCmd.Flags().StringVar(&createFlags.nodeSgId, "node-sg-id", "",
		"ID do Security Group dos nodes (modo byo)")
}

func runCreateEKS(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// =========================================
	// 1. VALIDAR FLAGS DE CONFIRMAÇÃO
	// =========================================
	if !GlobalFlags.DryRun {
		if !createFlags.acceptCosts {
			return fmt.Errorf("❌ Operação de criação requer --accept-costs (use --dry-run para preview)")
		}
		if !createFlags.confirmCreate {
			return fmt.Errorf("❌ Operação de criação requer --confirm-create (use --dry-run para preview)")
		}
	}

	// =========================================
	// 2. DETERMINAR BACKEND (3 OPÇÕES)
	// =========================================
	var backendURL string
	var err error

	if createFlags.stateBackend != "" {
		// OPÇÃO 1: URL completa especificada pelo usuário
		// Exemplo: s3://my-existing-bucket/custom/path
		backendURL = createFlags.stateBackend
		logger.Infof("📦 Usando backend especificado: %s", backendURL)

	} else if createFlags.stateBucketName != "" {
		// OPÇÃO 2: Nome de bucket especificado
		// Exemplo: --state-bucket my-company-k8s
		// Resultado: s3://my-company-k8s/clusters/cluster-name
		backendURL, err = pulumi.GenerateBackendURLWithName(ctx,
			createFlags.stateBucketName, createFlags.region, createFlags.name)
		if err != nil {
			return fmt.Errorf("❌ Erro ao gerar backend URL: %w", err)
		}
		logger.Infof("📦 Usando bucket especificado: %s", backendURL)

	} else {
		// OPÇÃO 3: AUTO-GERAR (PADRÃO E RECOMENDADO)
		// Gera nome único: k8s-cloud-{account-id}-{region}-{random}
		// Exemplo: k8s-cloud-123456789012-us-east-1-a1b2c3d4
		backendURL, err = pulumi.GenerateBackendURL(ctx, createFlags.region, createFlags.name)
		if err != nil {
			return fmt.Errorf("❌ Erro ao gerar backend URL: %w", err)
		}
		logger.Successf("✨ Backend auto-gerado (globalmente único)")
	}

	// =========================================
	// 3. CRIAR CONFIGURAÇÃO DO CLUSTER
	// =========================================
	cfg := config.DefaultClusterConfig(createFlags.name, createFlags.region)
	cfg.K8sVersion = createFlags.k8sVersion
	cfg.NodeConfig.InstanceType = createFlags.nodeInstanceType
	cfg.NodeConfig.DesiredSize = createFlags.nodeCount
	cfg.NodeConfig.MinSize = createFlags.nodeMinSize
	cfg.NodeConfig.MaxSize = createFlags.nodeMaxSize
	cfg.Backend = backendURL
	cfg.NetworkConfig.Mode = createFlags.networkMode
	cfg.NetworkConfig.VpcId = createFlags.vpcId
	cfg.NetworkConfig.VpcCidr = createFlags.vpcCidr
	cfg.NetworkConfig.PublicSubnetIds = createFlags.publicSubnetIds
	cfg.NetworkConfig.PrivateSubnetIds = createFlags.privateSubnetIds
	cfg.NetworkConfig.ClusterSecurityGroupId = createFlags.clusterSgId
	cfg.NetworkConfig.NodeSecurityGroupId = createFlags.nodeSgId
	cfg.NetworkConfig.AvailabilityZones = createFlags.azCount

	// =========================================
	// 4. VALIDAR CONFIGURAÇÃO
	// =========================================
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("❌ Configuração inválida: %w", err)
	}

	// =========================================
	// 5. MOSTRAR CONFIGURAÇÃO
	// =========================================
	logger.Separator()
	logger.Info("📋 Configuração do Cluster:")
	logger.Infof("  • Nome: %s", cfg.Name)
	logger.Infof("  • Região: %s", cfg.Region)
	logger.Infof("  • Versão K8s: %s", cfg.K8sVersion)
	logger.Infof("  • VPC CIDR: %s", cfg.NetworkConfig.VpcCidr)
	logger.Infof("  • Availability Zones: %d", cfg.NetworkConfig.AvailabilityZones)
	logger.Separator()
	logger.Info("🖥️  Configuração de Nodes:")
	logger.Infof("  • Instance Type: %s", cfg.NodeConfig.InstanceType)
	logger.Infof("  • Desired Count: %d", cfg.NodeConfig.DesiredSize)
	logger.Infof("  • Min Size: %d", cfg.NodeConfig.MinSize)
	logger.Infof("  • Max Size: %d", cfg.NodeConfig.MaxSize)
	logger.Infof("  • Disk Size: %d GB", cfg.NodeConfig.DiskSize)
	logger.Separator()
	logger.Info("💾 State Backend:")
	logger.Infof("  • URL: %s", cfg.Backend)
	logger.Info("  • Versionamento: Habilitado")
	logger.Info("  • Encriptação: AES-256")
	logger.Info("  • Acesso Público: Bloqueado")
	logger.Info("  • Lifecycle: 30d → Glacier, 90d → Delete")
	logger.Separator()

	// =========================================
	// 6. ESTIMAR CUSTO
	// =========================================
	estimatedCost := estimateEKSCost(cfg)
	logger.Warningf("💰 Custo Mensal Estimado: $%.2f USD", estimatedCost)
	logger.Warning("   (Valor aproximado para us-east-1. Custos reais podem variar)")
	logger.Infof("   • EKS Control Plane: $73.00")
	logger.Infof("   • NAT Gateways (%dx): $%.2f", cfg.NetworkConfig.AvailabilityZones, 32.85*float64(cfg.NetworkConfig.AvailabilityZones))
	logger.Infof("   • Nodes (%dx %s): $%.2f", cfg.NodeConfig.DesiredSize, cfg.NodeConfig.InstanceType,
		getNodeCost(cfg.NodeConfig.InstanceType)*float64(cfg.NodeConfig.DesiredSize))
	logger.Infof("   • Data Transfer: ~$10.00")
	logger.Separator()

	// =========================================
	// 7. MODO DRY-RUN
	// =========================================
	if GlobalFlags.DryRun {
		logger.Warning("🔍 Modo DRY-RUN ativado. Nenhum recurso será criado.")
		logger.Info("\n📝 Próximos passos para criar de verdade:")
		logger.Info("  1. Revise a configuração acima")
		logger.Info("  2. Execute o comando novamente com:")
		logger.Info("     --accept-costs --confirm-create")
		logger.Info("  3. Remova a flag --dry-run")
		return nil
	}

	// =========================================
	// 8. CRIAR ENGINE PULUMI
	// =========================================
	logger.Separator()
	logger.Info("🚀 Iniciando deployment...")
	logger.Separator()

	engine, err := pulumi.NewEngine(ctx, cfg)
	if err != nil {
		return fmt.Errorf("❌ Erro ao inicializar Pulumi: %w", err)
	}

	// =========================================
	// 9. EXECUTAR DEPLOYMENT
	// =========================================
	result, err := engine.Up(false) // false = não é dry-run
	if err != nil {
		return fmt.Errorf("❌ Erro no deployment: %w", err)
	}

	// =========================================
	// 10. OUTPUT BASEADO NO FORMATO
	// =========================================
	if GlobalFlags.Output == "json" {
		type JsonOutput struct {
			Success       bool              `json:"success"`
			ClusterName   string            `json:"clusterName"`
			Region        string            `json:"region"`
			Backend       string            `json:"backend"`
			EstimatedCost float64           `json:"estimatedCostUSD"`
			Outputs       map[string]string `json:"outputs"`
		}

		outputs := make(map[string]string)
		for k, v := range result.Outputs {
			outputs[k] = fmt.Sprintf("%v", v.Value)
		}

		jsonOut := JsonOutput{
			Success:       result.Success,
			ClusterName:   cfg.Name,
			Region:        cfg.Region,
			Backend:       cfg.Backend,
			EstimatedCost: estimatedCost,
			Outputs:       outputs,
		}

		return json.NewEncoder(os.Stdout).Encode(jsonOut)
	}

	// =========================================
	// 11. OUTPUT TEXTO (PADRÃO)
	// =========================================
	logger.Separator()
	logger.Success("🎉 Cluster criado com sucesso!")
	logger.Separator()
	logger.Info("📝 Próximos passos:")
	logger.Info("")
	logger.Info("  1️⃣  Obter kubeconfig:")
	logger.Infof("     @k8s-cloud kubeconfig %s --region %s --state-backend %s --merge",
		cfg.Name, cfg.Region, cfg.Backend)
	logger.Info("")
	logger.Info("  2️⃣  Verificar status:")
	logger.Infof("     @k8s-cloud status %s --region %s --state-backend %s",
		cfg.Name, cfg.Region, cfg.Backend)
	logger.Info("")
	logger.Info("  3️⃣  Listar nodes:")
	logger.Info("     kubectl get nodes")
	logger.Info("")
	logger.Info("  4️⃣  Ver pods do sistema:")
	logger.Info("     kubectl get pods -A")
	logger.Info("")
	logger.Separator()
	logger.Warning("⚠️  IMPORTANTE: Este cluster está custando dinheiro!")
	logger.Warningf("   Custo estimado: $%.2f/mês", estimatedCost)
	logger.Info("")
	logger.Info("   Para destruir quando não precisar mais:")
	logger.Infof("   @k8s-cloud destroy %s --confirm %s --region %s --state-backend %s",
		cfg.Name, cfg.Name, cfg.Region, cfg.Backend)
	logger.Separator()

	return nil
}

// estimateEKSCost calcula custo mensal aproximado
func estimateEKSCost(cfg *config.ClusterConfig) float64 {
	const (
		eksClusterCost = 73.00 // $0.10/hora
		natGatewayCost = 32.85 // $0.045/hora por NAT
		dataTransfer   = 10.00 // Estimativa conservadora
	)

	nodeCost := getNodeCost(cfg.NodeConfig.InstanceType)
	totalNodeCost := nodeCost * float64(cfg.NodeConfig.DesiredSize)
	natCost := natGatewayCost * float64(cfg.NetworkConfig.AvailabilityZones)

	return eksClusterCost + natCost + totalNodeCost + dataTransfer
}

// getNodeCost retorna custo mensal de um tipo de instância
func getNodeCost(instanceType string) float64 {
	// Preços aproximados us-east-1 (730 horas/mês)
	costs := map[string]float64{
		"t3.nano":    3.80,   // $0.0052/hora
		"t3.micro":   7.59,   // $0.0104/hora
		"t3.small":   15.18,  // $0.0208/hora
		"t3.medium":  30.37,  // $0.0416/hora
		"t3.large":   60.74,  // $0.0832/hora
		"t3.xlarge":  121.47, // $0.1664/hora
		"t3.2xlarge": 242.93, // $0.3328/hora
		"m5.large":   70.08,  // $0.096/hora
		"m5.xlarge":  140.16, // $0.192/hora
		"m5.2xlarge": 280.32, // $0.384/hora
		"m5.4xlarge": 560.64, // $0.768/hora
		"c5.large":   62.05,  // $0.085/hora
		"c5.xlarge":  124.10, // $0.17/hora
		"c5.2xlarge": 248.20, // $0.34/hora
		"r5.large":   91.25,  // $0.125/hora
		"r5.xlarge":  182.50, // $0.25/hora
	}

	if cost, ok := costs[instanceType]; ok {
		return cost
	}

	// Estimativa genérica se tipo não encontrado
	return 50.00
}
