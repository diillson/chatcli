package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
	"github.com/spf13/cobra"
)

var listFlags struct {
	stateBackend string
	region       string
}

var ListCmd = &cobra.Command{
	Use:   "list",
	Short: "Lista todos os clusters gerenciados",
	Long: `Lista todos os clusters Kubernetes gerenciados pelo plugin,
    baseado nos states armazenados no S3.
    
    EXEMPLO:
      @k8s-cloud list --state-backend s3://my-tfstate/k8s --region us-east-1
      @k8s-cloud list --state-backend s3://my-tfstate/k8s --region us-east-1 --output json`,
	RunE: runList,
}

func init() {
	ListCmd.Flags().StringVar(&listFlags.stateBackend, "state-backend", "",
		"Backend S3 para procurar states (obrigatório)")
	ListCmd.MarkFlagRequired("state-backend")

	ListCmd.Flags().StringVar(&listFlags.region, "region", "",
		"Região AWS (obrigatório)")
	ListCmd.MarkFlagRequired("region")
}

type ClusterInfo struct {
	Name      string `json:"name"`
	Region    string `json:"region"`
	StackName string `json:"stackName"`
	StateKey  string `json:"stateKey"`
}

func runList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	logger.Progress("🔍 Procurando clusters no backend S3...")

	// Parse backend URL
	if !strings.HasPrefix(listFlags.stateBackend, "s3://") {
		return fmt.Errorf("❌ Backend deve começar com s3://")
	}

	parts := strings.TrimPrefix(listFlags.stateBackend, "s3://")
	segments := strings.SplitN(parts, "/", 2)
	bucket := segments[0]
	prefix := ""
	if len(segments) == 2 {
		prefix = segments[1]
	}

	// Configurar AWS S3 client
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(listFlags.region))
	if err != nil {
		return fmt.Errorf("❌ Erro ao carregar config AWS: %w", err)
	}

	s3Client := s3.NewFromConfig(cfg)

	// Listar objetos no bucket
	listOutput, err := s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return fmt.Errorf("❌ Erro ao listar objetos S3: %w", err)
	}

	// Filtrar states do Pulumi (.pulumi/stacks/)
	var clusters []ClusterInfo
	for _, obj := range listOutput.Contents {
		key := *obj.Key

		// Procurar por arquivos .json de stack do Pulumi
		if strings.Contains(key, ".pulumi/stacks/") && strings.HasSuffix(key, ".json") {
			// Extrair nome do stack
			parts := strings.Split(key, "/")
			if len(parts) > 0 {
				stackName := strings.TrimSuffix(parts[len(parts)-1], ".json")

				// Tentar extrair nome do cluster do stack
				// Formato esperado: clustername-region
				clusterParts := strings.Split(stackName, "-")
				if len(clusterParts) >= 2 {
					clusterName := strings.Join(clusterParts[:len(clusterParts)-1], "-")
					region := clusterParts[len(clusterParts)-1]

					clusters = append(clusters, ClusterInfo{
						Name:      clusterName,
						Region:    region,
						StackName: stackName,
						StateKey:  key,
					})
				}
			}
		}
	}

	// Output baseado no formato
	if GlobalFlags.Output == "json" {
		return json.NewEncoder(os.Stdout).Encode(clusters)
	}

	// Output text
	if len(clusters) == 0 {
		logger.Warning("⚠️  Nenhum cluster encontrado")
		logger.Infof("📍 Backend verificado: s3://%s/%s", bucket, prefix)
		return nil
	}

	logger.Separator()
	logger.Successf("✅ Encontrados %d cluster(s):", len(clusters))
	logger.Separator()

	for _, cluster := range clusters {
		logger.Infof("📦 %s", cluster.Name)
		logger.Infof("   • Região: %s", cluster.Region)
		logger.Infof("   • Stack: %s", cluster.StackName)
		logger.Infof("   • State: s3://%s/%s", bucket, cluster.StateKey)
		logger.Info("")
	}

	logger.Separator()
	logger.Info("💡 Para ver detalhes de um cluster:")
	if len(clusters) > 0 {
		logger.Infof("   @k8s-cloud status %s --region %s --state-backend %s",
			clusters[0].Name, clusters[0].Region, listFlags.stateBackend)
	}
	logger.Separator()

	return nil
}
