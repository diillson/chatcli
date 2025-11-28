package cmd

import (
	"context"
	"fmt"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/pulumi"
	"github.com/spf13/cobra"
)

var refreshFlags struct {
	stateBackend string
	region       string
}

var RefreshCmd = &cobra.Command{
	Use:   "refresh [nome-do-cluster]",
	Short: "Sincroniza state com o estado real da AWS",
	Long: `Atualiza o Pulumi state com o estado atual dos recursos na AWS.
    
    Útil quando:
      • Recursos foram modificados manualmente no console AWS
      • Você quer detectar drift (diferenças entre state e realidade)
      • Precisa sincronizar após operações externas
    
    ⚠️  IMPORTANTE: Refresh não aplica mudanças, apenas atualiza o state.
       Para aplicar mudanças, use 'update'.
    
    EXEMPLO:
      @k8s-cloud refresh prod --region us-east-1 --state-backend s3://my-tfstate/k8s`,
	Args: cobra.ExactArgs(1),
	RunE: runRefresh,
}

func init() {
	RefreshCmd.Flags().StringVar(&refreshFlags.stateBackend, "state-backend", "",
		"Backend S3 onde está o state (obrigatório)")
	RefreshCmd.MarkFlagRequired("state-backend")

	RefreshCmd.Flags().StringVar(&refreshFlags.region, "region", "",
		"Região AWS do cluster (obrigatório)")
	RefreshCmd.MarkFlagRequired("region")
}

func runRefresh(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	clusterName := args[0]

	// Criar configuração mínima
	cfg := &config.ClusterConfig{
		Name:        clusterName,
		Provider:    "aws",
		Region:      refreshFlags.region,
		ProjectName: fmt.Sprintf("k8s-cloud-%s", clusterName),
		StackName:   fmt.Sprintf("%s-%s", clusterName, refreshFlags.region),
		Backend:     refreshFlags.stateBackend,
	}

	logger.Separator()
	logger.Infof("🔄 Sincronizando state do cluster '%s'", clusterName)
	logger.Infof("📍 Região: %s", refreshFlags.region)
	logger.Separator()

	// Criar engine Pulumi
	engine, err := pulumi.NewEngine(ctx, cfg)
	if err != nil {
		return fmt.Errorf("❌ Erro ao inicializar Pulumi: %w", err)
	}

	// Executar refresh
	if err := engine.Refresh(); err != nil {
		return err
	}

	logger.Separator()
	logger.Success("✅ State sincronizado com sucesso!")
	logger.Info("\n💡 Próximos passos:")
	logger.Info("  • Verificar mudanças detectadas:")
	logger.Infof("    @k8s-cloud status %s --region %s --state-backend %s",
		clusterName, refreshFlags.region, refreshFlags.stateBackend)
	logger.Separator()

	return nil
}
