package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/state"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func init() {
	listCmd.Flags().String("state-backend", "",
		"URL do state backend")
}

func runList(cmd *cobra.Command, args []string) error {
	stateBackendURL, _ := cmd.Flags().GetString("state-backend")

	if stateBackendURL == "" {
		return fmt.Errorf("❌ --state-backend é obrigatório para listar clusters")
	}

	// Criar backend
	backend, err := state.NewBackend(stateBackendURL, "us-east-1")
	if err != nil {
		return fmt.Errorf("❌ Falha ao criar backend: %w", err)
	}

	if err := backend.Initialize(); err != nil {
		return fmt.Errorf("❌ Falha ao inicializar backend: %w", err)
	}

	// Listar clusters
	clusterNames, err := backend.List()
	if err != nil {
		return fmt.Errorf("❌ Falha ao listar clusters: %w", err)
	}

	if len(clusterNames) == 0 {
		logger.Info("📋 Nenhum cluster encontrado.")
		return nil
	}

	// Carregar detalhes de cada cluster
	var clusterSummaries []types.ClusterSummary

	for _, name := range clusterNames {
		var state types.ClusterState
		if err := backend.Load(name, &state); err != nil {
			logger.Warningf("⚠️  Falha ao carregar estado do cluster '%s': %v", name, err)
			continue
		}

		summary := types.ClusterSummary{
			Name:        state.Config.Name,
			Provider:    state.Config.Provider,
			Region:      state.Config.Region,
			Status:      state.Status,
			NodeCount:   state.Config.NodeConfig.DesiredSize,
			K8sVersion:  state.Config.K8sVersion,
			Environment: state.Config.Environment,
			CreatedAt:   state.Config.CreatedAt,
		}

		clusterSummaries = append(clusterSummaries, summary)
	}

	// Output baseado em formato
	switch globalFlags.Output {
	case "json":
		return outputListJSON(clusterSummaries)
	case "yaml":
		return outputListYAML(clusterSummaries)
	default:
		return outputListText(clusterSummaries)
	}
}

func outputListJSON(clusters []types.ClusterSummary) error {
	list := types.ClusterList{Clusters: clusters}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(list)
}

func outputListYAML(clusters []types.ClusterSummary) error {
	list := types.ClusterList{Clusters: clusters}
	encoder := yaml.NewEncoder(os.Stdout)
	return encoder.Encode(list)
}

func outputListText(clusters []types.ClusterSummary) error {
	logger.Separator()
	logger.Infof("📋 CLUSTERS ENCONTRADOS: %d", len(clusters))
	logger.Separator()

	for i, cluster := range clusters {
		statusIcon := "✅"
		if !cluster.Status.Ready {
			statusIcon = "⏳"
		}

		logger.Infof("%d. %s %s", i+1, statusIcon, cluster.Name)
		logger.Infof("   Provider: %s", cluster.Provider)
		logger.Infof("   Região: %s", cluster.Region)
		logger.Infof("   Ambiente: %s", cluster.Environment)
		logger.Infof("   Versão K8s: %s", cluster.K8sVersion)
		logger.Infof("   Nodes: %d", cluster.NodeCount)
		logger.Infof("   Status: %s", cluster.Status.Phase)
		if cluster.Status.Endpoint != "" {
			logger.Infof("   Endpoint: %s", cluster.Status.Endpoint)
		}
		logger.Infof("   Criado: %s", cluster.CreatedAt.Format("2006-01-02 15:04"))
		logger.Info("")
	}

	logger.Separator()

	return nil
}
