package cmd

import (
	"context"
	"fmt"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/pulumi"
	"github.com/spf13/cobra"
)

var updateFlags struct {
	stateBackend  string
	region        string
	confirmUpdate bool
	scaleNodes    int
	k8sVersion    string
	instanceType  string
}

var UpdateCmd = &cobra.Command{
	Use:   "update [nome-do-cluster]",
	Short: "Atualiza configuração de um cluster",
	Long: `Atualiza configuração de um cluster existente:
      • Escalar nodes (aumentar/diminuir)
      • Atualizar versão do Kubernetes
      • Mudar tipo de instância dos nodes
    
    ⚠️  ATENÇÃO: Algumas operações podem causar downtime.
       Use --dry-run primeiro para ver o que será alterado.
    
    EXEMPLO:
      # Preview (dry-run)
      @k8s-cloud update prod --scale-nodes 5 --region us-east-1 --state-backend s3://my-tfstate/k8s --dry-run
    
      # Atualizar número de nodes
      @k8s-cloud update prod --scale-nodes 5 --confirm-update --region us-east-1 --state-backend s3://my-tfstate/k8s
    
      # Atualizar versão K8s
      @k8s-cloud update prod --k8s-version 1.30 --confirm-update --region us-east-1 --state-backend s3://my-tfstate/k8s`,
	Args: cobra.ExactArgs(1),
	RunE: runUpdate,
}

func init() {
	UpdateCmd.Flags().StringVar(&updateFlags.stateBackend, "state-backend", "",
		"Backend S3 onde está o state (obrigatório)")
	UpdateCmd.MarkFlagRequired("state-backend")

	UpdateCmd.Flags().StringVar(&updateFlags.region, "region", "",
		"Região AWS do cluster (obrigatório)")
	UpdateCmd.MarkFlagRequired("region")

	UpdateCmd.Flags().BoolVar(&updateFlags.confirmUpdate, "confirm-update", false,
		"Confirma operação de update (obrigatório para aplicar)")

	UpdateCmd.Flags().IntVar(&updateFlags.scaleNodes, "scale-nodes", 0,
		"Nova quantidade de nodes (0 = não alterar)")

	UpdateCmd.Flags().StringVar(&updateFlags.k8sVersion, "k8s-version", "",
		"Nova versão do Kubernetes (ex: 1.30)")

	UpdateCmd.Flags().StringVar(&updateFlags.instanceType, "instance-type", "",
		"Novo tipo de instância (ex: t3.large)")
}

func runUpdate(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	clusterName := args[0]

	// Validar que pelo menos uma operação foi especificada
	if updateFlags.scaleNodes == 0 && updateFlags.k8sVersion == "" && updateFlags.instanceType == "" {
		return fmt.Errorf("❌ Especifique pelo menos uma operação: --scale-nodes, --k8s-version ou --instance-type")
	}

	// Validar confirmação (exceto dry-run ou force)
	if !GlobalFlags.DryRun && !GlobalFlags.Force && !updateFlags.confirmUpdate {
		return fmt.Errorf("❌ Operação de update requer --confirm-update (use --dry-run para preview)")
	}

	logger.Separator()
	logger.Infof("🔧 Atualizando cluster '%s'", clusterName)
	logger.Infof("📍 Região: %s", updateFlags.region)
	logger.Separator()
	logger.Info("📋 Operações a serem realizadas:")

	if updateFlags.scaleNodes > 0 {
		logger.Infof("  • Escalar nodes para: %d", updateFlags.scaleNodes)
	}
	if updateFlags.k8sVersion != "" {
		logger.Infof("  • Atualizar K8s para: %s", updateFlags.k8sVersion)
		logger.Warning("    ⚠️  Atualização de versão pode causar downtime")
	}
	if updateFlags.instanceType != "" {
		logger.Infof("  • Mudar instância para: %s", updateFlags.instanceType)
		logger.Warning("    ⚠️  Mudança de instância recria os nodes")
	}
	logger.Separator()

	if GlobalFlags.DryRun {
		logger.Info("🔍 Modo DRY-RUN ativado. Nenhuma mudança será aplicada.")
	}

	// Carregar configuração atual do state
	cfg := &config.ClusterConfig{
		Name:        clusterName,
		Provider:    "aws",
		Region:      updateFlags.region,
		ProjectName: fmt.Sprintf("k8s-cloud-%s", clusterName),
		StackName:   fmt.Sprintf("%s-%s", clusterName, updateFlags.region),
		Backend:     updateFlags.stateBackend,
	}

	// Aplicar mudanças na configuração
	if updateFlags.scaleNodes > 0 {
		cfg.NodeConfig.DesiredSize = updateFlags.scaleNodes
		// Ajustar min/max se necessário
		if cfg.NodeConfig.MinSize > updateFlags.scaleNodes {
			cfg.NodeConfig.MinSize = updateFlags.scaleNodes
		}
		if cfg.NodeConfig.MaxSize < updateFlags.scaleNodes {
			cfg.NodeConfig.MaxSize = updateFlags.scaleNodes * 2
		}
	}
	if updateFlags.k8sVersion != "" {
		cfg.K8sVersion = updateFlags.k8sVersion
	}
	if updateFlags.instanceType != "" {
		cfg.NodeConfig.InstanceType = updateFlags.instanceType
	}

	// Criar engine Pulumi
	engine, err := pulumi.NewEngine(ctx, cfg)
	if err != nil {
		return fmt.Errorf("❌ Erro ao inicializar Pulumi: %w", err)
	}

	// Executar update
	result, err := engine.Up(GlobalFlags.DryRun)
	if err != nil {
		return err
	}

	if result.DryRun {
		logger.Separator()
		logger.Info("✅ Preview concluído. Para aplicar as mudanças, execute sem --dry-run")
		logger.Info("   e adicione --confirm-update")
	} else {
		logger.Separator()
		logger.Success("🎉 Cluster atualizado com sucesso!")
		logger.Info("\n💡 Verificar mudanças:")
		logger.Infof("  @k8s-cloud status %s --region %s --state-backend %s",
			clusterName, updateFlags.region, updateFlags.stateBackend)
	}
	logger.Separator()

	return nil
}
