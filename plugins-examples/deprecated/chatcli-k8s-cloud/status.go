package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
	awsprovider "github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/providers/aws"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/state"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func init() {
	statusCmd.Flags().String("state-backend", "",
		"URL do state backend")
	statusCmd.Flags().Bool("refresh", false,
		"Atualiza status consultando AWS (mais lento)")
}

var statusCmd = &cobra.Command{
	Use:   "status [nome]",
	Short: "Mostra status de um cluster",
	Long: `Exibe informações detalhadas sobre um cluster existente.
    
    Por padrão, usa o estado salvo (rápido).
    Use --refresh para consultar AWS em tempo real (lento).
    
    Exemplo:
      @k8s-cloud status prod-cluster
      @k8s-cloud status prod-cluster --refresh
      @k8s-cloud status prod-cluster --output json`,
	Args: cobra.ExactArgs(1),
	RunE: runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	clusterName := args[0]
	stateBackendURL, _ := cmd.Flags().GetString("state-backend")
	refresh, _ := cmd.Flags().GetBool("refresh")

	// Detectar state backend se não fornecido
	if stateBackendURL == "" {
		// Usar heurística (simplificado)
		stateBackendURL = fmt.Sprintf("s3://k8s-cloud-states/%s", clusterName)
	}

	// Carregar estado
	backend, err := state.NewBackend(stateBackendURL, "us-east-1")
	if err != nil {
		return fmt.Errorf("❌ Falha ao criar backend: %w", err)
	}

	if err := backend.Initialize(); err != nil {
		return fmt.Errorf("❌ Falha ao inicializar backend: %w", err)
	}

	var clusterState types.ClusterState
	if err := backend.Load(clusterName, &clusterState); err != nil {
		return fmt.Errorf("❌ Cluster '%s' não encontrado no state backend", clusterName)
	}

	// Se refresh, consultar AWS
	if refresh {
		logger.Progress("🔄 Atualizando status (consultando AWS)...")
		provider, err := awsprovider.NewProvider(
			clusterState.Config.Region,
			clusterName,
		)
		if err != nil {
			return err
		}

		// Atualizar status (implementar método GetStatus no provider)
		_ = provider // TODO
	}

	// Output baseado em formato
	switch globalFlags.Output {
	case "json":
		return outputJSON(clusterState)
	case "yaml":
		return outputYAML(clusterState)
	default:
		return outputText(clusterState)
	}
}

func outputJSON(state types.ClusterState) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(state)
}

func outputYAML(state types.ClusterState) error {
	encoder := yaml.NewEncoder(os.Stdout)
	return encoder.Encode(state)
}

func outputText(state types.ClusterState) error {
	logger.Separator()
	logger.Infof("📊 STATUS: %s", state.Config.Name)
	logger.Separator()

	// Status geral
	statusIcon := "✅"
	if !state.Status.Ready {
		statusIcon = "⏳"
	}
	logger.Infof("%s Estado: %s", statusIcon, state.Status.Phase)
	if state.Status.Message != "" {
		logger.Infof("   Mensagem: %s", state.Status.Message)
	}
	logger.Info("")

	// Informações básicas
	logger.Info("📋 INFORMAÇÕES:")
	logger.Infof("   Provider: %s", state.Config.Provider)
	logger.Infof("   Região: %s", state.Config.Region)
	logger.Infof("   Ambiente: %s", state.Config.Environment)
	logger.Infof("   Versão K8s: %s", state.Config.K8sVersion)
	logger.Infof("   Criado em: %s", state.Config.CreatedAt.Format("2006-01-02 15:04:05"))
	logger.Info("")

	// Endpoint
	if state.Status.Endpoint != "" {
		logger.Info("🔗 ENDPOINT:")
		logger.Infof("   %s", state.Status.Endpoint)
		logger.Info("")
	}

	// Nodes
	logger.Info("👷 NODES:")
	logger.Infof("   Prontos: %d/%d", state.Status.NodesReady, state.Status.NodesTotal)
	logger.Infof("   Instance Type: %s", state.Config.NodeConfig.InstanceType)
	logger.Infof("   Min/Desired/Max: %d/%d/%d",
		state.Config.NodeConfig.MinSize,
		state.Config.NodeConfig.DesiredSize,
		state.Config.NodeConfig.MaxSize)
	logger.Info("")

	// Networking
	logger.Info("🌐 NETWORKING:")
	logger.Infof("   VPC CIDR: %s", state.Config.VPCCidr)
	logger.Infof("   Availability Zones: %d", state.Config.AvailabilityZones)
	logger.Info("")

	// Add-ons
	hasAddons := false
	if state.Config.Addons.Istio != nil && state.Config.Addons.Istio.Enabled {
		if !hasAddons {
			logger.Info("🔌 ADD-ONS:")
			hasAddons = true
		}
		logger.Infof("   ✓ Istio %s", state.Config.Addons.Istio.Version)
	}
	if state.Config.Addons.NginxIngress != nil && state.Config.Addons.NginxIngress.Enabled {
		if !hasAddons {
			logger.Info("🔌 ADD-ONS:")
			hasAddons = true
		}
		logger.Info("   ✓ Nginx Ingress")
	}
	if state.Config.Addons.ArgoCD != nil && state.Config.Addons.ArgoCD.Enabled {
		if !hasAddons {
			logger.Info("🔌 ADD-ONS:")
			hasAddons = true
		}
		logger.Info("   ✓ ArgoCD")
	}
	if hasAddons {
		logger.Info("")
	}

	// Custos
	cost := calculateEstimatedCost(state.Config)
	logger.Info("💰 CUSTOS:")
	logger.Infof("   Estimativa mensal: ~$%.2f", cost)
	logger.Info("")

	logger.Separator()

	return nil
}
