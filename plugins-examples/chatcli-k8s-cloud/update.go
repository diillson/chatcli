package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
	awsprovider "github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/providers/aws"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/state"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
	"github.com/spf13/cobra"
)

func init() {
	updateCmd.Flags().Bool("confirm-update", false,
		"Confirma operação de update (obrigatório)")
	updateCmd.Flags().Int("scale-nodes", 0,
		"Nova quantidade desejada de nodes")
	updateCmd.Flags().Int("node-min", 0,
		"Novo mínimo de nodes")
	updateCmd.Flags().Int("node-max", 0,
		"Novo máximo de nodes")
	updateCmd.Flags().String("state-backend", "",
		"URL do state backend")
	updateCmd.Flags().String("k8s-version", "",
		"Atualizar versão do Kubernetes (ex: 1.29)")
}

func runUpdate(cmd *cobra.Command, args []string) error {
	clusterName := args[0]
	confirmUpdate, _ := cmd.Flags().GetBool("confirm-update")
	scaleNodes, _ := cmd.Flags().GetInt("scale-nodes")
	nodeMin, _ := cmd.Flags().GetInt("node-min")
	nodeMax, _ := cmd.Flags().GetInt("node-max")
	k8sVersion, _ := cmd.Flags().GetString("k8s-version")
	stateBackendURL, _ := cmd.Flags().GetString("state-backend")

	// Validação: requer confirmação
	if !confirmUpdate && !globalFlags.Force {
		return fmt.Errorf("❌ Operação de update requer --confirm-update ou --force")
	}

	// Validação: pelo menos uma operação
	if scaleNodes == 0 && nodeMin == 0 && nodeMax == 0 && k8sVersion == "" {
		return fmt.Errorf("❌ Especifique pelo menos uma operação: --scale-nodes, --node-min, --node-max ou --k8s-version")
	}

	ctx := context.Background()

	// Detectar state backend
	if stateBackendURL == "" {
		stateBackendURL = fmt.Sprintf("s3://k8s-cloud-states/%s", clusterName)
	}

	logger.Separator()
	logger.Infof("🔄 Atualizando cluster: %s", clusterName)
	logger.Separator()

	// Carregar estado
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
		return fmt.Errorf("❌ Cluster '%s' não encontrado", clusterName)
	}

	// Adquirir lock
	if err := backend.Lock(clusterName); err != nil {
		return fmt.Errorf("❌ Falha ao adquirir lock: %w", err)
	}
	defer backend.Unlock(clusterName)

	// Extrair recursos
	resourcesInterface := clusterState.Resources["aws"]
	resourcesJSON, _ := json.Marshal(resourcesInterface)
	var resources awsprovider.FullClusterResources
	if err := json.Unmarshal(resourcesJSON, &resources); err != nil {
		return fmt.Errorf("❌ Estado corrompido: %w", err)
	}

	// Criar provider
	provider, err := awsprovider.NewProvider(
		clusterState.Config.Region,
		clusterName,
	)
	if err != nil {
		return fmt.Errorf("❌ Falha ao criar provider: %w", err)
	}

	changed := false

	// UPDATE 1: Escalar nodes
	if scaleNodes > 0 || nodeMin > 0 || nodeMax > 0 {
		newMin := nodeMin
		newMax := nodeMax
		newDesired := scaleNodes

		if newMin == 0 {
			newMin = clusterState.Config.NodeConfig.MinSize
		}
		if newMax == 0 {
			newMax = clusterState.Config.NodeConfig.MaxSize
		}
		if newDesired == 0 {
			newDesired = clusterState.Config.NodeConfig.DesiredSize
		}

		// Validar
		if newMin > newDesired || newDesired > newMax {
			return fmt.Errorf("❌ Configuração inválida: min (%d) <= desired (%d) <= max (%d)",
				newMin, newDesired, newMax)
		}

		// Mostrar mudanças
		logger.Info("")
		logger.Info("📊 MUDANÇAS DE SCALING:")
		logger.Infof("   Min:     %d → %d", clusterState.Config.NodeConfig.MinSize, newMin)
		logger.Infof("   Desired: %d → %d", clusterState.Config.NodeConfig.DesiredSize, newDesired)
		logger.Infof("   Max:     %d → %d", clusterState.Config.NodeConfig.MaxSize, newMax)
		logger.Info("")

		if !globalFlags.DryRun {
			// Aplicar mudanças
			nodeGroupName := fmt.Sprintf("%s-nodes", clusterName)

			logger.Progress("⏳ Aplicando mudanças de scaling...")
			eksManager := provider.EksManager
			if err := eksManager.UpdateNodeGroup(ctx, nodeGroupName, newMin, newMax, newDesired); err != nil {
				return fmt.Errorf("❌ Falha ao atualizar: %w", err)
			}

			// Atualizar estado
			clusterState.Config.NodeConfig.MinSize = newMin
			clusterState.Config.NodeConfig.MaxSize = newMax
			clusterState.Config.NodeConfig.DesiredSize = newDesired
			changed = true

			logger.Success("✅ Scaling atualizado!")
		}
	}

	// UPDATE 2: Versão do Kubernetes
	if k8sVersion != "" {
		logger.Info("")
		logger.Info("📊 MUDANÇA DE VERSÃO:")
		logger.Infof("   Versão K8s: %s → %s", clusterState.Config.K8sVersion, k8sVersion)
		logger.Info("")

		if !globalFlags.DryRun {
			logger.Progress("⏳ Atualizando versão do Kubernetes...")

			eksManager := provider.EksManager
			if err := eksManager.UpdateClusterVersion(ctx, k8sVersion); err != nil {
				return fmt.Errorf("❌ Falha ao atualizar versão: %w", err)
			}

			clusterState.Config.K8sVersion = k8sVersion
			changed = true

			logger.Success("✅ Versão atualizada!")
		}
	}

	// Salvar estado atualizado
	if changed && !globalFlags.DryRun {
		if err := backend.Save(clusterName, clusterState); err != nil {
			logger.Warningf("⚠️  Estado atualizado na AWS mas falha ao salvar no backend: %v", err)
		}
	}

	if globalFlags.DryRun {
		logger.Info("🔍 DRY RUN - Nenhuma mudança aplicada")
	}

	logger.Separator()
	logger.Success("✅ Cluster atualizado com sucesso!")
	logger.Separator()

	return nil
}
