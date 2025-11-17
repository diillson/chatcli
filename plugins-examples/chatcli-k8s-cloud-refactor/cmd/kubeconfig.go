package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/pulumi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var kubeconfigFlags struct {
	stateBackend string
	region       string
	merge        bool
	outputFile   string
}

var KubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig [nome-do-cluster]",
	Short: "Obtém ou mescla kubeconfig de um cluster",
	Long: `Obtém o kubeconfig de um cluster EKS e:
      • Exibe no stdout (padrão)
      • Salva em arquivo específico (--output)
      • Mescla com ~/.kube/config existente (--merge)
    
    O kubeconfig gerado usa aws-cli para autenticação,
    então você precisa ter AWS CLI configurada.
    
    EXEMPLO:
      # Exibir kubeconfig
      @k8s-cloud kubeconfig prod --region us-east-1 --state-backend s3://my-tfstate/k8s
    
      # Salvar em arquivo
      @k8s-cloud kubeconfig prod --region us-east-1 --state-backend s3://my-tfstate/k8s --output prod-kubeconfig.yaml
    
      # Mesclar com ~/.kube/config
      @k8s-cloud kubeconfig prod --region us-east-1 --state-backend s3://my-tfstate/k8s --merge`,
	Args: cobra.ExactArgs(1),
	RunE: runKubeconfig,
}

func init() {
	KubeconfigCmd.Flags().StringVar(&kubeconfigFlags.stateBackend, "state-backend", "",
		"Backend S3 onde está o state (obrigatório)")
	KubeconfigCmd.MarkFlagRequired("state-backend")

	KubeconfigCmd.Flags().StringVar(&kubeconfigFlags.region, "region", "",
		"Região AWS do cluster (obrigatório)")
	KubeconfigCmd.MarkFlagRequired("region")

	KubeconfigCmd.Flags().BoolVar(&kubeconfigFlags.merge, "merge", false,
		"Mescla com ~/.kube/config existente")

	KubeconfigCmd.Flags().StringVar(&kubeconfigFlags.outputFile, "output", "",
		"Arquivo de saída (default: stdout)")
}

func runKubeconfig(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	clusterName := args[0]

	// Criar configuração mínima
	cfg := &config.ClusterConfig{
		Name:        clusterName,
		Provider:    "aws",
		Region:      kubeconfigFlags.region,
		ProjectName: fmt.Sprintf("k8s-cloud-%s", clusterName),
		StackName:   fmt.Sprintf("%s-%s", clusterName, kubeconfigFlags.region),
		Backend:     kubeconfigFlags.stateBackend,
	}

	logger.Infof("🔍 Obtendo kubeconfig do cluster '%s'...", clusterName)

	// Criar engine Pulumi
	engine, err := pulumi.NewEngine(ctx, cfg)
	if err != nil {
		return fmt.Errorf("❌ Erro ao inicializar Pulumi: %w", err)
	}

	// Obter kubeconfig do output
	kubeconfig, err := engine.GetKubeconfig()
	if err != nil {
		return fmt.Errorf("❌ Erro ao obter kubeconfig: %w", err)
	}

	// Caso 1: Merge com ~/.kube/config
	if kubeconfigFlags.merge {
		return mergeKubeconfig(kubeconfig, clusterName)
	}

	// Caso 2: Salvar em arquivo
	if kubeconfigFlags.outputFile != "" {
		if err := os.WriteFile(kubeconfigFlags.outputFile, []byte(kubeconfig), 0600); err != nil {
			return fmt.Errorf("❌ Erro ao salvar kubeconfig: %w", err)
		}
		logger.Successf("✅ Kubeconfig salvo em: %s", kubeconfigFlags.outputFile)
		logger.Info("\n💡 Para usar:")
		logger.Infof("   export KUBECONFIG=%s", kubeconfigFlags.outputFile)
		logger.Info("   kubectl get nodes")
		return nil
	}

	// Caso 3: Exibir no stdout
	fmt.Println(kubeconfig)
	return nil
}

func mergeKubeconfig(newKubeconfig, clusterName string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("❌ Erro ao obter diretório home: %w", err)
	}

	kubeDir := filepath.Join(homeDir, ".kube")
	kubeconfigPath := filepath.Join(kubeDir, "config")

	// Criar diretório .kube se não existir
	if err := os.MkdirAll(kubeDir, 0755); err != nil {
		return fmt.Errorf("❌ Erro ao criar diretório .kube: %w", err)
	}

	// Fazer backup do kubeconfig existente
	if _, err := os.Stat(kubeconfigPath); err == nil {
		backupPath := fmt.Sprintf("%s.backup-%d", kubeconfigPath, time.Now().Unix())
		if err := copyFile(kubeconfigPath, backupPath); err != nil {
			logger.Warningf("⚠️  Não foi possível criar backup: %v", err)
		} else {
			logger.Infof("📦 Backup criado: %s", backupPath)
		}
	}

	// Parse do kubeconfig novo
	var newConfig map[string]interface{}
	if err := yaml.Unmarshal([]byte(newKubeconfig), &newConfig); err != nil {
		return fmt.Errorf("❌ Erro ao parsear novo kubeconfig: %w", err)
	}

	// Carregar kubeconfig existente (se houver)
	var existingConfig map[string]interface{}
	if data, err := os.ReadFile(kubeconfigPath); err == nil {
		if err := yaml.Unmarshal(data, &existingConfig); err != nil {
			logger.Warningf("⚠️  Erro ao parsear kubeconfig existente: %v", err)
			existingConfig = make(map[string]interface{})
		}
	} else {
		existingConfig = make(map[string]interface{})
	}

	// Merge configs (simplificado - apenas adiciona novo cluster)
	// Em produção, deveria fazer merge inteligente de clusters/contexts/users
	mergedConfig := mergeKubeconfigMaps(existingConfig, newConfig, clusterName)

	// Salvar merged config
	mergedData, err := yaml.Marshal(mergedConfig)
	if err != nil {
		return fmt.Errorf("❌ Erro ao serializar kubeconfig: %w", err)
	}

	if err := os.WriteFile(kubeconfigPath, mergedData, 0600); err != nil {
		return fmt.Errorf("❌ Erro ao salvar kubeconfig: %w", err)
	}

	logger.Separator()
	logger.Success("✅ Kubeconfig mesclado com sucesso!")
	logger.Infof("📝 Arquivo: %s", kubeconfigPath)
	logger.Separator()
	logger.Info("💡 Para usar o cluster:")
	logger.Infof("   kubectl config use-context %s", clusterName)
	logger.Info("   kubectl get nodes")
	logger.Separator()

	return nil
}

func mergeKubeconfigMaps(existing, new map[string]interface{}, clusterName string) map[string]interface{} {
	// Implementação simplificada - apenas sobrescreve
	// Em produção, deveria fazer merge inteligente

	if len(existing) == 0 {
		return new
	}

	// Merge clusters
	if existingClusters, ok := existing["clusters"].([]interface{}); ok {
		if newClusters, ok := new["clusters"].([]interface{}); ok {
			existing["clusters"] = append(existingClusters, newClusters...)
		}
	}

	// Merge contexts
	if existingContexts, ok := existing["contexts"].([]interface{}); ok {
		if newContexts, ok := new["contexts"].([]interface{}); ok {
			existing["contexts"] = append(existingContexts, newContexts...)
		}
	}

	// Merge users
	if existingUsers, ok := existing["users"].([]interface{}); ok {
		if newUsers, ok := new["users"].([]interface{}); ok {
			existing["users"] = append(existingUsers, newUsers...)
		}
	}

	// Atualizar current-context
	existing["current-context"] = clusterName

	return existing
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}
