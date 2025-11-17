package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/types"
	"github.com/spf13/cobra"
)

var (
	version = "1.0.0"
	commit  = "dev"

	globalFlags struct {
		DryRun         bool
		Force          bool
		Verbose        bool
		Output         string
		NonInteractive bool
	}
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Erro: %v\n", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "k8s-cloud",
	Short: "Gerenciador multi-cloud de clusters Kubernetes",
	Long: `@k8s-cloud - Plugin ChatCLI para criar e gerenciar clusters Kubernetes
    em AWS, Azure e GCP com estado gerenciado.
    
    IMPORTANTE: Este plugin é projetado para uso com IA (ChatCLI).
    Todas as operações são não-interativas e requerem confirmação via flags.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		globalFlags.NonInteractive = true
	},
}

var metadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "Exibe metadados do plugin (contrato ChatCLI)",
	Run: func(cmd *cobra.Command, args []string) {
		meta := types.PluginMetadata{
			Name:        "@k8s-cloud",
			Description: "Cria e gerencia clusters Kubernetes em múltiplas clouds (AWS, Azure, GCP) com estado gerenciado como Terraform",
			Usage: `@k8s-cloud <comando> [opções]
    
    MODO NÃO-INTERATIVO (IA):
      Todas as operações requerem confirmação explícita via flags.
      Nenhuma operação solicitará input do usuário.
    
    FLAGS GLOBAIS:
      --dry-run              Mostra o que seria feito sem executar
      --force                Força operações destrutivas sem confirmação
      --output json|yaml     Formato de saída (default: text)
      --verbose              Output detalhado para debug
    
    COMANDOS:
      create eks   - Cria cluster EKS na AWS
      destroy      - Remove um cluster
      status       - Mostra status de um cluster
      list         - Lista todos os clusters
      update       - Atualiza configuração do cluster
      kubeconfig   - Obtém/salva kubeconfig
    
    EXEMPLOS:
      @k8s-cloud create eks --name prod --region us-east-1 --dry-run
      @k8s-cloud list --output json
      @k8s-cloud destroy prod --confirm prod`,
			Version: fmt.Sprintf("%s (commit: %s)", version, commit),
		}

		jsonBytes, _ := json.MarshalIndent(meta, "", "  ")
		fmt.Println(string(jsonBytes))
	},
}

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Cria um novo cluster",
	Long:  "Cria um novo cluster Kubernetes em uma cloud provider",
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "Lista clusters existentes",
	Long:  "Lista todos os clusters gerenciados pelo plugin",
	RunE:  runList,
}

var updateCmd = &cobra.Command{
	Use:   "update [nome]",
	Short: "Atualiza configuração de um cluster",
	Args:  cobra.ExactArgs(1),
	RunE:  runUpdate,
}

var kubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig [nome]",
	Short: "Obtém kubeconfig de um cluster",
	Args:  cobra.ExactArgs(1),
	RunE:  runKubeconfig,
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Gerencia configurações do plugin",
	Long:  "Comandos para gerenciar configurações globais e backends",
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&globalFlags.DryRun, "dry-run", false,
		"Mostra o que seria feito sem executar")
	rootCmd.PersistentFlags().BoolVar(&globalFlags.Force, "force", false,
		"Força operações sem validações adicionais")
	rootCmd.PersistentFlags().BoolVar(&globalFlags.Verbose, "verbose", false,
		"Output detalhado para debug")
	rootCmd.PersistentFlags().StringVar(&globalFlags.Output, "output", "text",
		"Formato de saída: text, json, yaml")

	rootCmd.AddCommand(metadataCmd)
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(destroyCmd)
	rootCmd.AddCommand(kubeconfigCmd)
	rootCmd.AddCommand(configCmd)
}
