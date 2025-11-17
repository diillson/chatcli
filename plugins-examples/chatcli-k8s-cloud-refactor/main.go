package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/cmd"
	"github.com/spf13/cobra"
)

var (
	version = "2.0.0"
	commit  = "dev"
)

type PluginMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "k8s-cloud",
		Short: "Gerenciador multi-cloud de clusters Kubernetes com Pulumi",
		Long: `@k8s-cloud v2.0 - Plugin ChatCLI profissional para criar e gerenciar 
    clusters Kubernetes em AWS e Azure usando Pulumi Automation API.
    
    🔥 NOVA VERSÃO 2.0:
      • Pulumi Automation API (state management robusto)
      • Rollback automático em caso de falha
      • Drift detection integrado
      • Grafo de dependências automático
      • Operações em lote otimizadas`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Flags globais
	rootCmd.PersistentFlags().BoolVar(&cmd.GlobalFlags.DryRun, "dry-run", false,
		"Mostra o que seria feito sem executar")
	rootCmd.PersistentFlags().BoolVar(&cmd.GlobalFlags.Force, "force", false,
		"Força operações sem confirmação")
	rootCmd.PersistentFlags().BoolVar(&cmd.GlobalFlags.Verbose, "verbose", false,
		"Output detalhado")
	rootCmd.PersistentFlags().StringVar(&cmd.GlobalFlags.Output, "output", "text",
		"Formato de saída: text, json, yaml")

	// Comando de metadados (contrato ChatCLI)
	metadataCmd := &cobra.Command{
		Use:   "metadata",
		Short: "Exibe metadados do plugin (contrato ChatCLI)",
		Run: func(c *cobra.Command, args []string) {
			meta := PluginMetadata{
				Name:        "@k8s-cloud",
				Description: "Gerenciador multi-cloud de clusters Kubernetes com Pulumi (v2.0)",
				Usage: `@k8s-cloud <comando> [opções]
    
    COMANDOS:
      create eks      Cria cluster EKS (AWS)
      destroy         Remove cluster
      update          Atualiza cluster
      status          Status do cluster
      list            Lista clusters
      refresh         Sincroniza state com cloud
      kubeconfig      Gerencia kubeconfig
    
    NOVIDADES v2.0:
      • Pulumi state management
      • Rollback automático
      • Drift detection
      • Import de recursos existentes
    
    EXEMPLOS:
      @k8s-cloud create eks --name prod --region us-east-1 --dry-run
      @k8s-cloud refresh prod
      @k8s-cloud destroy prod --confirm prod`,
				Version: fmt.Sprintf("%s (commit: %s)", version, commit),
			}
			json.NewEncoder(os.Stdout).Encode(meta)
		},
	}

	rootCmd.AddCommand(metadataCmd)
	rootCmd.AddCommand(cmd.CreateCmd)
	rootCmd.AddCommand(cmd.DestroyCmd)
	rootCmd.AddCommand(cmd.UpdateCmd)
	rootCmd.AddCommand(cmd.StatusCmd)
	rootCmd.AddCommand(cmd.ListCmd)
	rootCmd.AddCommand(cmd.RefreshCmd)
	rootCmd.AddCommand(cmd.KubeconfigCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Erro: %v\n", err)
		os.Exit(1)
	}
}
