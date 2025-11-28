package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
	awsprovider "github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/providers/aws"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/state"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
	"github.com/spf13/cobra"
)

func init() {
	kubeconfigCmd.Flags().Bool("save", false,
		"Salva kubeconfig em ~/.kube/config-CLUSTER")
	kubeconfigCmd.Flags().Bool("merge", false,
		"Mescla com ~/.kube/config existente")
	kubeconfigCmd.Flags().String("state-backend", "",
		"URL do state backend")
}

func runKubeconfig(cmd *cobra.Command, args []string) error {
	clusterName := args[0]
	save, _ := cmd.Flags().GetBool("save")
	merge, _ := cmd.Flags().GetBool("merge")
	stateBackendURL, _ := cmd.Flags().GetString("state-backend")

	// Detectar state backend
	if stateBackendURL == "" {
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
		return fmt.Errorf("❌ Cluster '%s' não encontrado", clusterName)
	}

	// Criar provider para gerar kubeconfig
	provider, err := awsprovider.NewProvider(
		clusterState.Config.Region,
		clusterName,
	)
	if err != nil {
		return fmt.Errorf("❌ Falha ao criar provider: %w", err)
	}

	// Obter informações do cluster
	eksManager := provider.EksManager
	clusterInfo, err := eksManager.GetClusterInfo(cmd.Context())
	if err != nil {
		return fmt.Errorf("❌ Falha ao obter info do cluster: %w", err)
	}

	// Gerar kubeconfig
	kubeconfig, err := eksManager.GenerateKubeconfig(clusterInfo)
	if err != nil {
		return fmt.Errorf("❌ Falha ao gerar kubeconfig: %w", err)
	}

	// Salvar ou imprimir
	if save || merge {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("❌ Falha ao obter diretório home: %w", err)
		}

		kubeDir := filepath.Join(homeDir, ".kube")
		if err := os.MkdirAll(kubeDir, 0755); err != nil {
			return fmt.Errorf("❌ Falha ao criar diretório .kube: %w", err)
		}

		if merge {
			// TODO: Implementar merge com kubeconfig existente
			logger.Warning("⚠️  Merge ainda não implementado, salvando separado")
			save = true
		}

		if save {
			kubeconfigPath := filepath.Join(kubeDir, fmt.Sprintf("config-%s", clusterName))
			if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600); err != nil {
				return fmt.Errorf("❌ Falha ao salvar kubeconfig: %w", err)
			}

			logger.Successf("✅ Kubeconfig salvo: %s", kubeconfigPath)
			logger.Info("")
			logger.Info("💡 Para usar:")
			logger.Infof("   export KUBECONFIG=%s", kubeconfigPath)
			logger.Info("   kubectl get nodes")
		}
	} else {
		// Apenas imprimir
		fmt.Println(kubeconfig)
	}

	return nil
}
