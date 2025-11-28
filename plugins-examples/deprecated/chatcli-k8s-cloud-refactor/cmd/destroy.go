package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/pulumi"
	"github.com/spf13/cobra"
)

var destroyFlags struct {
	confirm      string
	stateBackend string
	region       string
}

var DestroyCmd = &cobra.Command{
	Use:   "destroy [nome-do-cluster]",
	Short: "Remove um cluster e todos seus recursos",
	Long: `Remove completamente um cluster EKS e toda sua infraestrutura:
      • Node Groups
      • EKS Cluster
      • NAT Gateways
      • Elastic IPs
      • Route Tables
      • Subnets (públicas e privadas)
      • Internet Gateway
      • VPC
      • Security Groups
      • IAM Roles e Policies
    
    ⚠️  ATENÇÃO: Esta operação é IRREVERSÍVEL!
       Todos os dados do cluster serão perdidos.
    
    EXEMPLO:
      # Preview da destruição (dry-run)
      @k8s-cloud destroy prod --region us-east-1 --state-backend s3://my-tfstate/k8s --dry-run
    
      # Destruir cluster (requer confirmação)
      @k8s-cloud destroy prod --confirm prod --region us-east-1 --state-backend s3://my-tfstate/k8s
    
      # Forçar destruição (pula validações)
      @k8s-cloud destroy prod --confirm prod --region us-east-1 --state-backend s3://my-tfstate/k8s --force`,
	Args: cobra.ExactArgs(1),
	RunE: runDestroy,
}

func init() {
	DestroyCmd.Flags().StringVar(&destroyFlags.confirm, "confirm", "",
		"Confirma destruição digitando o nome do cluster (obrigatório)")

	DestroyCmd.Flags().StringVar(&destroyFlags.stateBackend, "state-backend", "",
		"Backend S3 onde está o state (obrigatório)")
	DestroyCmd.MarkFlagRequired("state-backend")

	DestroyCmd.Flags().StringVar(&destroyFlags.region, "region", "",
		"Região AWS do cluster (obrigatório)")
	DestroyCmd.MarkFlagRequired("region")
}

func runDestroy(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	clusterName := args[0]

	// Validar confirmação (exceto dry-run ou force)
	if !GlobalFlags.DryRun && !GlobalFlags.Force {
		if destroyFlags.confirm == "" {
			return fmt.Errorf("❌ Para destruir o cluster '%s', use --confirm %s",
				clusterName, clusterName)
		}
		if destroyFlags.confirm != clusterName {
			return fmt.Errorf("❌ Confirmação inválida. Digite exatamente: --confirm %s",
				clusterName)
		}
	}

	// Criar configuração mínima (apenas para carregar state)
	cfg := &config.ClusterConfig{
		Name:        clusterName,
		Provider:    "aws",
		Region:      destroyFlags.region,
		ProjectName: fmt.Sprintf("k8s-cloud-%s", clusterName),
		StackName:   fmt.Sprintf("%s-%s", clusterName, destroyFlags.region),
		Backend:     destroyFlags.stateBackend,
	}

	logger.Separator()
	logger.Warningf("🔥 DESTRUINDO CLUSTER: %s", clusterName)
	logger.Warningf("📍 Região: %s", destroyFlags.region)
	logger.Warningf("💾 Backend: %s", destroyFlags.stateBackend)
	logger.Separator()

	if GlobalFlags.DryRun {
		logger.Info("🔍 Modo DRY-RUN: Simulando destruição...")
		logger.Info("\n📋 Recursos que seriam removidos:")
		logger.Info("  • EKS Cluster")
		logger.Info("  • Node Groups")
		logger.Info("  • NAT Gateways (pode levar ~5 minutos)")
		logger.Info("  • Elastic IPs")
		logger.Info("  • Subnets")
		logger.Info("  • Route Tables")
		logger.Info("  • Internet Gateway")
		logger.Info("  • VPC")
		logger.Info("  • Security Groups")
		logger.Info("  • IAM Roles")
		logger.Separator()
		logger.Info("✅ Preview completo. Para destruir de verdade, remova --dry-run")
		return nil
	}

	logger.Warning("⚠️  Última chance! Pressione Ctrl+C nos próximos 5 segundos para cancelar...")
	logger.Progress("⏳ Aguardando 5 segundos...")

	// Countdown só se não estiver em modo force
	if !GlobalFlags.Force {
		for i := 5; i > 0; i-- {
			logger.Progressf("   %d...", i)
			time.Sleep(1 * time.Second)
		}
	}

	logger.Separator()
	logger.Warning("🔥 Iniciando destruição...")

	// Criar engine Pulumi
	engine, err := pulumi.NewEngine(ctx, cfg)
	if err != nil {
		return fmt.Errorf("❌ Erro ao inicializar Pulumi: %w", err)
	}

	// Executar destruição
	if err := engine.Destroy(); err != nil {
		return err
	}

	logger.Separator()
	logger.Success("✅ Cluster destruído com sucesso!")
	logger.Info("\n📝 Limpeza final:")
	logger.Info("  • State permanece no S3 para histórico")
	logger.Info("  • Para remover completamente o state:")
	logger.Infof("    aws s3 rm %s/%s --recursive", destroyFlags.stateBackend, cfg.StackName)
	logger.Separator()

	return nil
}
