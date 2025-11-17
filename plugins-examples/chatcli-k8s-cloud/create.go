package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	awsprovider "github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/providers/aws"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/state"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
	"github.com/spf13/cobra"
)

var createEKSCmd = &cobra.Command{
	Use:   "eks",
	Short: "Cria um cluster EKS na AWS",
	Long: `Cria um cluster EKS completo na AWS incluindo:
      - VPC com subnets públicas e privadas
      - Internet Gateway e NAT Gateways
      - Security Groups
      - IAM Roles
      - EKS Control Plane
      - Node Groups
    
    Exemplo:
      @k8s-cloud create eks \
        --name prod-cluster \
        --region us-east-1 \
        --state-backend s3://my-bucket \
        --node-count 3 \
        --confirm-create`,
	RunE: runCreateEKS,
}

// Flags para create eks
var (
	createEKSFlags struct {
		// Básicas
		name         string
		region       string
		environment  string
		stateBackend string

		// Kubernetes
		k8sVersion string

		// Networking
		vpcCidr string
		azCount int

		// Nodes
		nodeInstanceType string
		nodeMinSize      int
		nodeMaxSize      int
		nodeDesiredSize  int
		nodeDiskSize     int

		// Add-ons
		withIstio        bool
		withNginxIngress bool
		withArgoCD       bool

		// Confirmação (obrigatório para IA)
		confirmCreate bool
		acceptCosts   bool

		// Tags
		tags []string
	}
)

func init() {
	createCmd.AddCommand(createEKSCmd)

	// Flags obrigatórias
	createEKSCmd.Flags().StringVar(&createEKSFlags.name, "name", "",
		"Nome do cluster (obrigatório)")
	createEKSCmd.Flags().StringVar(&createEKSFlags.region, "region", "",
		"Região AWS (obrigatório)")
	createEKSCmd.Flags().StringVar(&createEKSFlags.stateBackend, "state-backend", "",
		"URL do state backend (ex: s3://bucket/path) (obrigatório)")

	createEKSCmd.MarkFlagRequired("name")
	createEKSCmd.MarkFlagRequired("region")
	createEKSCmd.MarkFlagRequired("state-backend")

	// Flags opcionais com valores padrão
	createEKSCmd.Flags().StringVar(&createEKSFlags.environment, "environment", "production",
		"Ambiente (production, staging, development)")
	createEKSCmd.Flags().StringVar(&createEKSFlags.k8sVersion, "k8s-version",
		config.DefaultK8sVersion, "Versão do Kubernetes")

	// Networking
	createEKSCmd.Flags().StringVar(&createEKSFlags.vpcCidr, "vpc-cidr",
		config.DefaultVPCCidr, "CIDR da VPC")
	createEKSCmd.Flags().IntVar(&createEKSFlags.azCount, "az-count",
		config.DefaultAZCount, "Número de Availability Zones")

	// Nodes
	createEKSCmd.Flags().StringVar(&createEKSFlags.nodeInstanceType, "node-type",
		config.DefaultNodeInstanceType, "Tipo de instância dos nodes")
	createEKSCmd.Flags().IntVar(&createEKSFlags.nodeMinSize, "node-min",
		config.DefaultNodeMinSize, "Número mínimo de nodes")
	createEKSCmd.Flags().IntVar(&createEKSFlags.nodeMaxSize, "node-max",
		config.DefaultNodeMaxSize, "Número máximo de nodes")
	createEKSCmd.Flags().IntVar(&createEKSFlags.nodeDesiredSize, "node-count",
		config.DefaultNodeDesiredSize, "Número desejado de nodes")
	createEKSCmd.Flags().IntVar(&createEKSFlags.nodeDiskSize, "node-disk",
		config.DefaultNodeDiskSize, "Tamanho do disco dos nodes (GB)")

	// Add-ons
	createEKSCmd.Flags().BoolVar(&createEKSFlags.withIstio, "with-istio", false,
		"Instalar Istio service mesh")
	createEKSCmd.Flags().BoolVar(&createEKSFlags.withNginxIngress, "with-nginx-ingress", false,
		"Instalar Nginx Ingress Controller")
	createEKSCmd.Flags().BoolVar(&createEKSFlags.withArgoCD, "with-argocd", false,
		"Instalar ArgoCD")

	// Confirmação (CRÍTICO para operação não-interativa)
	createEKSCmd.Flags().BoolVar(&createEKSFlags.confirmCreate, "confirm-create", false,
		"Confirma criação do cluster (obrigatório)")
	createEKSCmd.Flags().BoolVar(&createEKSFlags.acceptCosts, "accept-costs", false,
		"Confirma que está ciente dos custos")

	// Tags
	createEKSCmd.Flags().StringSliceVar(&createEKSFlags.tags, "tags", nil,
		"Tags customizadas (formato: key=value)")
}

func runCreateEKS(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// VALIDAÇÃO 1: Confirmação obrigatória (modo não-interativo)
	if !createEKSFlags.confirmCreate && !globalFlags.Force {
		return fmt.Errorf("❌ Operação de criação requer --confirm-create ou --force")
	}

	if !createEKSFlags.acceptCosts && !globalFlags.Force {
		logger.Warning("⚠️  AVISO: Criar um cluster EKS gera custos na AWS")
		logger.Warning("   Estimativa: ~$73/mês (control plane) + ~$30-150/mês por node")
		return fmt.Errorf("❌ Operação requer --accept-costs ou --force")
	}

	// Configurar logger
	if globalFlags.Verbose {
		os.Setenv("DEBUG", "1")
	}

	// Timer para medir tempo total
	timer := logger.NewTimer("Criação do cluster")
	defer timer.Stop()

	// VALIDAÇÃO 2: Configuração
	clusterConfig := buildClusterConfig()
	if err := config.ValidateConfig(&clusterConfig); err != nil {
		return fmt.Errorf("❌ Configuração inválida: %w", err)
	}

	// Dry-run: apenas mostrar o que seria feito
	if globalFlags.DryRun {
		return printDryRun(clusterConfig)
	}

	// INÍCIO DA EXECUÇÃO REAL
	logger.Separator()
	logger.Info("🚀 ChatCLI K8s Cloud - AWS EKS Provider")
	logger.Infof("   Versão: %s", version)
	logger.Separator()

	// 1. Inicializar State Backend
	logger.Info("📦 Inicializando state backend...")
	backend, err := state.NewBackend(createEKSFlags.stateBackend, createEKSFlags.region)
	if err != nil {
		return fmt.Errorf("❌ Falha ao criar backend: %w", err)
	}

	if err := backend.Initialize(); err != nil {
		return fmt.Errorf("❌ Falha ao inicializar backend: %w", err)
	}

	// 2. Criar Provider AWS
	logger.Separator()
	logger.Info("☁️  Inicializando provider AWS...")
	provider, err := awsprovider.NewProvider(createEKSFlags.region, createEKSFlags.name)
	if err != nil {
		return fmt.Errorf("❌ Falha ao criar provider: %w", err)
	}

	// 3. Criar Cluster
	logger.Separator()
	if err := provider.CreateCluster(ctx, clusterConfig, backend); err != nil {
		logger.Error("❌ Falha ao criar cluster!")
		logger.Error("")
		logger.Error("💡 TROUBLESHOOTING:")
		logger.Error("   1. Verifique credenciais AWS:")
		logger.Error("      aws sts get-caller-identity")
		logger.Error("   2. Verifique permissões IAM necessárias")
		logger.Error("   3. Verifique limites de serviço (service quotas)")
		logger.Error("   4. Consulte logs detalhados com --verbose")
		logger.Error("")
		logger.Error("🗑️  LIMPEZA:")
		logger.Errorf("   Execute: @k8s-cloud destroy %s --confirm %s --force",
			createEKSFlags.name, createEKSFlags.name)
		logger.Error("")
		return err
	}

	// 4. Sucesso!
	logger.Separator()
	logger.Success("🎉 CLUSTER CRIADO COM SUCESSO!")
	logger.Info("")
	logger.Info("📋 PRÓXIMOS PASSOS:")
	logger.Infof("   1. Configure kubectl:")
	logger.Infof("      export KUBECONFIG=~/.kube/config-%s", createEKSFlags.name)
	logger.Info("")
	logger.Info("   2. Verifique o cluster:")
	logger.Info("      kubectl cluster-info")
	logger.Info("      kubectl get nodes")
	logger.Info("")
	logger.Info("   3. Verifique o status via plugin:")
	logger.Infof("      @k8s-cloud status %s", createEKSFlags.name)
	logger.Info("")

	if createEKSFlags.withIstio || createEKSFlags.withNginxIngress || createEKSFlags.withArgoCD {
		logger.Info("   4. Add-ons serão instalados (aguarde alguns minutos):")
		if createEKSFlags.withIstio {
			logger.Info("      • Istio: kubectl get pods -n istio-system")
		}
		if createEKSFlags.withNginxIngress {
			logger.Info("      • Nginx Ingress: kubectl get pods -n ingress-nginx")
		}
		if createEKSFlags.withArgoCD {
			logger.Info("      • ArgoCD: kubectl get pods -n argocd")
		}
		logger.Info("")
	}

	logger.Info("💰 CUSTOS ESTIMADOS:")
	estimatedCost := calculateEstimatedCost(clusterConfig)
	logger.Infof("   ~$%.2f/mês", estimatedCost)
	logger.Info("   (Control Plane + Nodes + Networking)")
	logger.Info("")

	logger.Separator()

	return nil
}

// buildClusterConfig constrói configuração do cluster a partir das flags
func buildClusterConfig() types.ClusterConfig {
	cfg := config.NewDefaultClusterConfig(
		createEKSFlags.name,
		"aws",
		createEKSFlags.region,
	)

	// Sobrescrever com flags do usuário
	cfg.Environment = createEKSFlags.environment
	cfg.K8sVersion = createEKSFlags.k8sVersion
	cfg.VPCCidr = createEKSFlags.vpcCidr
	cfg.AvailabilityZones = createEKSFlags.azCount

	// Node config
	cfg.NodeConfig = types.NodeConfig{
		InstanceType: createEKSFlags.nodeInstanceType,
		MinSize:      createEKSFlags.nodeMinSize,
		MaxSize:      createEKSFlags.nodeMaxSize,
		DesiredSize:  createEKSFlags.nodeDesiredSize,
		DiskSize:     createEKSFlags.nodeDiskSize,
	}

	// Add-ons
	if createEKSFlags.withIstio {
		cfg.Addons.Istio = &types.IstioConfig{
			Enabled: true,
			Version: config.DefaultIstioVersion,
			Profile: "demo",
		}
	}

	if createEKSFlags.withNginxIngress {
		cfg.Addons.NginxIngress = &types.NginxIngressConfig{
			Enabled: true,
		}
	}

	if createEKSFlags.withArgoCD {
		cfg.Addons.ArgoCD = &types.ArgoCDConfig{
			Enabled: true,
		}
	}

	// State backend
	cfg.StateBackend = types.StateBackendConfig{
		Type:   "s3",
		URL:    createEKSFlags.stateBackend,
		Region: createEKSFlags.region,
	}

	// Tags customizadas
	for _, tag := range createEKSFlags.tags {
		// Parse key=value
		parts := splitKeyValue(tag)
		if len(parts) == 2 {
			cfg.Tags[parts[0]] = parts[1]
		}
	}

	// Metadata
	cfg.CreatedAt = time.Now()
	if user := os.Getenv("USER"); user != "" {
		cfg.CreatedBy = user
	}

	return cfg
}

// printDryRun mostra o que seria feito sem executar
func printDryRun(cfg types.ClusterConfig) error {
	logger.Info("🔍 DRY RUN - Nenhuma ação será executada")
	logger.Separator()

	logger.Info("📋 CONFIGURAÇÃO DO CLUSTER:")
	logger.Infof("   Nome: %s", cfg.Name)
	logger.Infof("   Provider: %s", cfg.Provider)
	logger.Infof("   Região: %s", cfg.Region)
	logger.Infof("   Ambiente: %s", cfg.Environment)
	logger.Infof("   Versão K8s: %s", cfg.K8sVersion)
	logger.Info("")

	logger.Info("🌐 NETWORKING:")
	logger.Infof("   VPC CIDR: %s", cfg.VPCCidr)
	logger.Infof("   Availability Zones: %d", cfg.AvailabilityZones)
	logger.Info("   • Subnets públicas: 3")
	logger.Info("   • Subnets privadas: 3")
	logger.Info("   • NAT Gateways: 3 (um por AZ)")
	logger.Info("   • Internet Gateway: 1")
	logger.Info("")

	logger.Info("👷 NODES:")
	logger.Infof("   Instance Type: %s", cfg.NodeConfig.InstanceType)
	logger.Infof("   Min/Desired/Max: %d/%d/%d",
		cfg.NodeConfig.MinSize, cfg.NodeConfig.DesiredSize, cfg.NodeConfig.MaxSize)
	logger.Infof("   Disk Size: %d GB", cfg.NodeConfig.DiskSize)
	logger.Info("")

	if cfg.Addons.Istio != nil && cfg.Addons.Istio.Enabled {
		logger.Info("🕸️  ADD-ONS:")
		logger.Infof("   • Istio %s (profile: %s)",
			cfg.Addons.Istio.Version, cfg.Addons.Istio.Profile)
	}
	if cfg.Addons.NginxIngress != nil && cfg.Addons.NginxIngress.Enabled {
		logger.Info("   • Nginx Ingress Controller")
	}
	if cfg.Addons.ArgoCD != nil && cfg.Addons.ArgoCD.Enabled {
		logger.Info("   • ArgoCD")
	}
	logger.Info("")

	logger.Info("💰 CUSTOS ESTIMADOS:")
	cost := calculateEstimatedCost(cfg)
	logger.Infof("   Total: ~$%.2f/mês", cost)
	logger.Info("")
	logger.Info("   Breakdown:")
	logger.Info("   • EKS Control Plane: $73/mês")
	logger.Infof("   • EC2 Instances (%dx %s): $%.2f/mês",
		cfg.NodeConfig.DesiredSize, cfg.NodeConfig.InstanceType,
		calculateNodeCost(cfg.NodeConfig))
	logger.Infof("   • NAT Gateways (3x): $%.2f/mês", 32.40*3) // $0.045/hora
	logger.Info("   • EBS Volumes: incluído nos nodes")
	logger.Info("   • Data Transfer: variável")
	logger.Info("")

	logger.Info("⏱️  TEMPO ESTIMADO:")
	logger.Info("   • Networking: ~5 minutos")
	logger.Info("   • EKS Control Plane: ~10-15 minutos")
	logger.Info("   • Node Groups: ~5-10 minutos")
	if cfg.Addons.Istio != nil && cfg.Addons.Istio.Enabled {
		logger.Info("   • Istio: ~3-5 minutos")
	}
	logger.Info("   TOTAL: ~20-35 minutos")
	logger.Info("")

	logger.Separator()
	logger.Info("✅ Para executar de verdade, remova --dry-run")
	logger.Separator()

	return nil
}

// calculateEstimatedCost calcula custo mensal estimado
func calculateEstimatedCost(cfg types.ClusterConfig) float64 {
	cost := 0.0

	// EKS Control Plane
	cost += 73.0 // $73/mês

	// EC2 Instances (nodes)
	cost += calculateNodeCost(cfg.NodeConfig)

	// NAT Gateways (3 AZs)
	cost += 32.40 * float64(cfg.AvailabilityZones) // $0.045/hora * 720h

	// EBS Volumes (incluído no cálculo dos nodes)

	return cost
}

// calculateNodeCost calcula custo dos nodes
func calculateNodeCost(nodeConfig types.NodeConfig) float64 {
	// Mapa simplificado de preços (us-east-1)
	priceMap := map[string]float64{
		"t3.micro":   7.30,   // $0.0104/hora
		"t3.small":   14.60,  // $0.0208/hora
		"t3.medium":  29.20,  // $0.0416/hora
		"t3.large":   58.40,  // $0.0832/hora
		"t3.xlarge":  116.80, // $0.1664/hora
		"t3.2xlarge": 233.60, // $0.3328/hora
		"m5.large":   69.12,  // $0.096/hora
		"m5.xlarge":  138.24, // $0.192/hora
		"m5.2xlarge": 276.48, // $0.384/hora
	}

	pricePerNode, exists := priceMap[nodeConfig.InstanceType]
	if !exists {
		pricePerNode = 50.0 // fallback genérico
	}

	// Usar DesiredSize para estimativa
	return pricePerNode * float64(nodeConfig.DesiredSize)
}

// splitKeyValue divide "key=value" em ["key", "value"]
func splitKeyValue(s string) []string {
	parts := make([]string, 0, 2)
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			parts = append(parts, s[:i], s[i+1:])
			return parts
		}
	}
	return parts
}
