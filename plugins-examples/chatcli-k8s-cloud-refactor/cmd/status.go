package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/pulumi"
	"github.com/spf13/cobra"
)

var statusFlags struct {
	stateBackend string
	region       string
}

var StatusCmd = &cobra.Command{
	Use:   "status [nome-do-cluster]",
	Short: "Mostra status detalhado de um cluster",
	Long: `Exibe informações detalhadas sobre um cluster:
      • Status do EKS Control Plane
      • Node Groups e contagem de nodes
      • Versão do Kubernetes
      • VPC e subnets
      • Endpoints (público/privado)
      • Outputs do Pulumi
    
    EXEMPLO:
      @k8s-cloud status prod --region us-east-1 --state-backend s3://my-tfstate/k8s
      @k8s-cloud status prod --region us-east-1 --state-backend s3://my-tfstate/k8s --output json`,
	Args: cobra.ExactArgs(1),
	RunE: runStatus,
}

func init() {
	StatusCmd.Flags().StringVar(&statusFlags.stateBackend, "state-backend", "",
		"Backend S3 onde está o state (obrigatório)")
	StatusCmd.MarkFlagRequired("state-backend")

	StatusCmd.Flags().StringVar(&statusFlags.region, "region", "",
		"Região AWS do cluster (obrigatório)")
	StatusCmd.MarkFlagRequired("region")
}

type ClusterStatus struct {
	Name            string            `json:"name"`
	Region          string            `json:"region"`
	Status          string            `json:"status"`
	K8sVersion      string            `json:"k8sVersion"`
	Endpoint        string            `json:"endpoint"`
	VpcId           string            `json:"vpcId"`
	NodeGroupName   string            `json:"nodeGroupName"`
	NodeGroupStatus string            `json:"nodeGroupStatus"`
	EstimatedCost   string            `json:"estimatedCost"`
	Outputs         map[string]string `json:"outputs"`
	LastUpdated     time.Time         `json:"lastUpdated"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	clusterName := args[0]

	// Criar configuração mínima
	cfg := &config.ClusterConfig{
		Name:        clusterName,
		Provider:    "aws",
		Region:      statusFlags.region,
		ProjectName: fmt.Sprintf("k8s-cloud-%s", clusterName),
		StackName:   fmt.Sprintf("%s-%s", clusterName, statusFlags.region),
		Backend:     statusFlags.stateBackend,
	}

	logger.Infof("🔍 Obtendo status do cluster '%s'...", clusterName)

	// Criar engine Pulumi
	engine, err := pulumi.NewEngine(ctx, cfg)
	if err != nil {
		return fmt.Errorf("❌ Erro ao inicializar Pulumi: %w", err)
	}

	// Obter outputs
	outputs, err := engine.Outputs()
	if err != nil {
		return fmt.Errorf("❌ Erro ao obter outputs: %w", err)
	}

	if len(outputs) == 0 {
		return fmt.Errorf("❌ Cluster '%s' não encontrado ou state vazio", clusterName)
	}

	// Montar status
	status := ClusterStatus{
		Name:        clusterName,
		Region:      statusFlags.region,
		Status:      "ACTIVE",
		LastUpdated: time.Now(),
		Outputs:     make(map[string]string),
	}

	// Extrair informações dos outputs
	for key, output := range outputs {
		if output.Value != nil {
			valueStr := fmt.Sprintf("%v", output.Value)
			status.Outputs[key] = valueStr

			switch key {
			case "clusterVersion":
				status.K8sVersion = valueStr
			case "clusterEndpoint":
				status.Endpoint = valueStr
			case "vpcId":
				status.VpcId = valueStr
			case "nodeGroupName":
				status.NodeGroupName = valueStr
			case "nodeGroupStatus":
				status.NodeGroupStatus = valueStr
			case "estimatedMonthlyCost":
				status.EstimatedCost = valueStr
			}
		}
	}

	// Output baseado no formato
	if GlobalFlags.Output == "json" {
		return json.NewEncoder(os.Stdout).Encode(status)
	}

	// Output text
	logger.Separator()
	logger.Successf("✅ Cluster: %s", status.Name)
	logger.Separator()
	logger.Info("📊 Informações:")
	logger.Infof("  • Status: %s", status.Status)
	logger.Infof("  • Região: %s", status.Region)
	if status.K8sVersion != "" {
		logger.Infof("  • Versão K8s: %s", status.K8sVersion)
	}
	if status.VpcId != "" {
		logger.Infof("  • VPC ID: %s", status.VpcId)
	}
	if status.Endpoint != "" {
		logger.Infof("  • Endpoint: %s", status.Endpoint)
	}

	if status.NodeGroupName != "" {
		logger.Separator()
		logger.Info("🖥️  Node Group:")
		logger.Infof("  • Nome: %s", status.NodeGroupName)
		if status.NodeGroupStatus != "" {
			logger.Infof("  • Status: %s", status.NodeGroupStatus)
		}
	}

	if status.EstimatedCost != "" {
		logger.Separator()
		logger.Infof("💰 Custo Estimado: %s", status.EstimatedCost)
	}

	logger.Separator()
	logger.Info("📋 Outputs completos:")
	for key, value := range status.Outputs {
		if key != "kubeconfig" { // Kubeconfig é muito grande
			logger.Infof("  • %s: %s", key, truncate(value, 60))
		}
	}

	logger.Separator()
	logger.Info("💡 Comandos úteis:")
	logger.Infof("  • Obter kubeconfig: @k8s-cloud kubeconfig %s", clusterName)
	logger.Infof("  • Atualizar cluster: @k8s-cloud update %s", clusterName)
	logger.Infof("  • Sincronizar state: @k8s-cloud refresh %s", clusterName)
	logger.Separator()

	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
