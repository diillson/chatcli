package main

import (
	"context"
	"fmt"
	"os"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
	awsprovider "github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/providers/aws"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/state"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
	"github.com/spf13/cobra"
)

func init() {
	destroyCmd.Flags().String("confirm", "",
		"Confirma destruição digitando o nome do cluster (obrigatório)")
	destroyCmd.Flags().String("state-backend", "",
		"URL do state backend (se não especificado, tenta detectar)")
}

var destroyCmd = &cobra.Command{
	Use:   "destroy [nome]",
	Short: "Remove um cluster",
	Long: `Remove completamente um cluster e toda sua infraestrutura:
      - EKS Cluster e Node Groups
      - Networking (VPC, Subnets, NAT Gateways, etc)
      - IAM Roles
      - State do backend
    
    IMPORTANTE: Esta operação é IRREVERSÍVEL!
    
    Exemplo:
      @k8s-cloud destroy prod-cluster --confirm prod-cluster`,
	Args: cobra.ExactArgs(1),
	RunE: runDestroy,
}

func runDestroy(cmd *cobra.Command, args []string) error {
	clusterName := args[0]
	confirmName, _ := cmd.Flags().GetString("confirm")
	stateBackendURL, _ := cmd.Flags().GetString("state-backend")

	// VALIDAÇÃO CRÍTICA: Confirmação obrigatória
	if confirmName != clusterName && !globalFlags.Force {
		return fmt.Errorf("❌ Para destruir o cluster '%s', use --confirm %s",
			clusterName, clusterName)
	}

	ctx := context.Background()

	// Timer
	timer := logger.NewTimer("Destruição do cluster")
	defer timer.Stop()

	logger.Separator()
	logger.Infof("🔥 DESTRUINDO CLUSTER: %s", clusterName)
	logger.Warning("⚠️  ESTA OPERAÇÃO É IRREVERSÍVEL!")
	logger.Separator()

	// 1. Tentar detectar state backend se não fornecido
	if stateBackendURL == "" {
		logger.Warning("⚠️  State backend não especificado")
		logger.Info("💡 Tentando detectar automaticamente...")

		// Tentar backends comuns
		commonBackends := []string{
			fmt.Sprintf("s3://k8s-cloud-states/%s", clusterName),
			fmt.Sprintf("s3://%s-k8s-states/%s", os.Getenv("USER"), clusterName),
		}

		var foundBackend state.Backend
		for _, backendURL := range commonBackends {
			backend, err := state.NewBackend(backendURL, "us-east-1")
			if err != nil {
				continue
			}

			if err := backend.Initialize(); err != nil {
				continue
			}

			exists, _ := backend.Exists(clusterName)
			if exists {
				foundBackend = backend
				stateBackendURL = backendURL
				logger.Successf("✅ State encontrado em: %s", backendURL)
				break
			}
		}

		if foundBackend == nil {
			return fmt.Errorf("❌ State backend não encontrado. Use --state-backend")
		}
	}

	// 2. Carregar estado
	logger.Info("📦 Carregando estado do cluster...")
	backend, err := state.NewBackend(stateBackendURL, "us-east-1")
	if err != nil {
		return fmt.Errorf("❌ Falha ao criar backend: %w", err)
	}

	if err := backend.Initialize(); err != nil {
		return fmt.Errorf("❌ Falha ao inicializar backend: %w", err)
	}

	var clusterState types.ClusterState
	if err := backend.Load(clusterName, &clusterState); err != nil {
		return fmt.Errorf("❌ Falha ao carregar estado: %w", err)
	}

	// 3. Mostrar resumo do que será destruído
	if !globalFlags.Force {
		logger.Info("")
		logger.Warning("⚠️  RECURSOS QUE SERÃO DESTRUÍDOS:")
		logger.Infof("   • Cluster EKS: %s", clusterName)
		logger.Infof("   • Região: %s", clusterState.Config.Region)
		logger.Infof("   • Nodes: %d", clusterState.Status.NodesTotal)
		logger.Info("   • VPC completa (subnets, NAT, IGW)")
		logger.Info("   • Security Groups")
		logger.Info("   • IAM Roles")
		logger.Info("   • State backend")
		logger.Info("")

		savings := calculateEstimatedCost(clusterState.Config)
		logger.Infof("💰 Economia mensal: ~$%.2f", savings)
		logger.Info("")
	}

	// Dry-run
	if globalFlags.DryRun {
		logger.Info("🔍 DRY RUN - Nenhuma ação executada")
		logger.Separator()
		return nil
	}

	// 4. Iniciar destruição
	logger.Separator()
	logger.Info("🗑️  Iniciando destruição...")

	provider, err := awsprovider.NewProvider(
		clusterState.Config.Region,
		clusterName,
	)
	if err != nil {
		return fmt.Errorf("❌ Falha ao criar provider: %w", err)
	}

	if err := provider.DeleteCluster(ctx, backend); err != nil {
		logger.Error("❌ Falha na destruição!")
		logger.Error("")
		logger.Error("💡 TROUBLESHOOTING:")
		logger.Error("   • Alguns recursos podem ter sido removidos parcialmente")
		logger.Error("   • Verifique o console AWS para recursos órfãos")
		logger.Error("   • Tente novamente com --force")
		logger.Error("")
		return err
	}

	// 5. Sucesso
	logger.Separator()
	logger.Success("✅ CLUSTER DESTRUÍDO COM SUCESSO!")
	logger.Info("")
	logger.Info("📋 RESUMO:")
	logger.Infof("   • Cluster '%s' removido", clusterName)
	logger.Infof("   • Economia mensal: ~$%.2f", calculateEstimatedCost(clusterState.Config))
	logger.Info("")
	logger.Info("🧹 LIMPEZA:")
	logger.Info("   • Estado removido do backend")
	logger.Infof("   • Kubeconfig local: rm ~/.kube/config-%s", clusterName)
	logger.Info("")
	logger.Separator()

	return nil
}
