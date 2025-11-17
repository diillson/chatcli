package pulumi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/config"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/pulumi/programs"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optrefresh"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Engine wrapper para Pulumi Automation API
type Engine struct {
	config *config.ClusterConfig
	stack  auto.Stack
	ctx    context.Context
}

// configurePulumiEnvironment configura PATH para incluir Pulumi
func configurePulumiEnvironment(pulumiPath string) error {
	pulumiDir := filepath.Dir(pulumiPath)

	// Adicionar ao PATH atual
	currentPath := os.Getenv("PATH")
	if !strings.Contains(currentPath, pulumiDir) {
		newPath := fmt.Sprintf("%s%c%s", pulumiDir, os.PathListSeparator, currentPath)
		os.Setenv("PATH", newPath)
		logger.Debugf("PATH atualizado: %s adicionado", pulumiDir)
	}

	return nil
}

// NewEngine cria nova engine Pulumi
//func NewEngine(ctx context.Context, cfg *config.ClusterConfig) (*Engine, error) {
//	logger.Infof("🚀 Inicializando Pulumi Engine para cluster '%s'", cfg.Name)
//
//	pulumiPath, err := EnsurePulumiInstalled(ctx)
//	if err != nil {
//		return nil, fmt.Errorf("erro ao garantir Pulumi CLI: %w", err)
//	}
//
//	// Configurar PATH para incluir Pulumi
//	if err := configurePulumiEnvironment(pulumiPath); err != nil {
//		return nil, fmt.Errorf("erro ao configurar ambiente Pulumi: %w", err)
//	}
//
//	if err := InstallLanguagePlugins(ctx, pulumiPath); err != nil {
//		return nil, fmt.Errorf("erro ao instalar plugins de linguagem: %w", err)
//	}
//
//	// Configurar passphrase para modo não-interativo
//	if err := configurePulumiPassphrase(); err != nil {
//		return nil, fmt.Errorf("erro ao configurar passphrase: %w", err)
//	}
//
//	// Validar configuração
//	if err := cfg.Validate(); err != nil {
//		return nil, fmt.Errorf("configuração inválida: %w", err)
//	}
//
//	// Parse backend URL
//	backendCfg, err := ParseBackendURL(cfg.Backend, cfg.Region, cfg.Name)
//	if err != nil {
//		return nil, err
//	}
//	backendCfg.Region = cfg.Region
//
//	// Garantir que backend existe
//	if err := EnsureBackend(ctx, backendCfg); err != nil {
//		return nil, err
//	}
//
//	// Atualizar config com URL completa do backend
//	cfg.Backend = fmt.Sprintf("s3://%s/%s", backendCfg.S3Bucket, backendCfg.S3KeyPrefix)
//
//	// Criar programa Pulumi baseado no provider
//	var program pulumi.RunFunc
//	switch cfg.Provider {
//	case "aws":
//		program = programs.CreateEKSProgram(cfg)
//	default:
//		return nil, fmt.Errorf("provider '%s' não suportado", cfg.Provider)
//	}
//
//	// Configurar workspace local
//	workDir := filepath.Join(os.TempDir(), "pulumi-k8s-cloud", cfg.StackName)
//	if err := os.MkdirAll(workDir, 0755); err != nil {
//		return nil, fmt.Errorf("erro ao criar diretório de trabalho: %w", err)
//	}
//
//	logger.Debugf("📁 Diretório de trabalho: %s", workDir)
//
//	// Criar projeto Pulumi
//	project := workspace.Project{
//		Name:    tokens.PackageName(cfg.ProjectName),
//		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
//		Backend: &workspace.ProjectBackend{
//			URL: cfg.Backend,
//		},
//	}
//
//	// Criar workspace com configuração de secrets provider
//	ws, err := auto.NewLocalWorkspace(ctx,
//		auto.Program(program),
//		auto.Project(project),
//		auto.WorkDir(workDir),
//		auto.SecretsProvider("passphrase"), // ✅ ADICIONAR ISSO
//		auto.EnvVars(map[string]string{ // ✅ E ISSO
//			"PULUMI_CONFIG_PASSPHRASE": getPulumiPassphrase(),
//		}),
//	)
//	if err != nil {
//		return nil, fmt.Errorf("erro ao criar workspace: %w", err)
//	}
//
//	logger.Debugf("✅ Workspace criado")
//
//	// Selecionar ou criar stack
//	stack, err := auto.UpsertStackLocalSource(ctx, cfg.StackName, workDir)
//	if err != nil {
//		return nil, fmt.Errorf("erro ao criar/selecionar stack: %w", err)
//	}
//
//	logger.Infof("✅ Stack '%s' selecionada", cfg.StackName)
//
//	// Configurar AWS
//	if err := stack.SetConfig(ctx, "aws:region", auto.ConfigValue{Value: cfg.Region}); err != nil {
//		return nil, fmt.Errorf("erro ao configurar região AWS: %w", err)
//	}
//
//	// Instalar plugins necessários
//	logger.Progress("📦 Instalando plugins Pulumi (AWS)...")
//	if err := ws.InstallPlugin(ctx, "aws", "v6.52.0"); err != nil {
//		logger.Warningf("⚠️  Erro ao instalar plugin AWS: %v (continuando...)", err)
//	}
//
//	return &Engine{
//		config: cfg,
//		stack:  stack,
//		ctx:    ctx,
//	}, nil
//}

func NewEngine(ctx context.Context, cfg *config.ClusterConfig) (*Engine, error) {
	logger.Infof("🚀 Inicializando Pulumi Engine para cluster '%s'", cfg.Name)

	// Garantir que Pulumi CLI está disponível
	pulumiPath, err := EnsurePulumiInstalled(ctx)
	if err != nil {
		return nil, fmt.Errorf("erro ao garantir Pulumi CLI: %w", err)
	}

	// Configurar PATH
	if err := configurePulumiEnvironment(pulumiPath); err != nil {
		return nil, fmt.Errorf("erro ao configurar ambiente Pulumi: %w", err)
	}

	// Configurar passphrase
	if err := configurePulumiPassphrase(); err != nil {
		return nil, fmt.Errorf("erro ao configurar passphrase: %w", err)
	}

	// ✅ INSTALAR PLUGIN GO ANTES DE CRIAR WORKSPACE
	logger.Progress("📦 Verificando plugins necessários...")
	if err := ensureGoPlugin(ctx, pulumiPath); err != nil {
		// Se falhar, tentar continuar (pode já estar instalado)
		logger.Warningf("⚠️  Aviso ao instalar plugin Go: %v", err)
	}

	// Validar configuração
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuração inválida: %w", err)
	}

	// Parse backend URL
	backendCfg, err := ParseBackendURL(cfg.Backend, cfg.Region, cfg.Name)
	if err != nil {
		return nil, err
	}
	backendCfg.Region = cfg.Region

	// Garantir que backend existe
	if err := EnsureBackend(ctx, backendCfg); err != nil {
		return nil, err
	}

	// Atualizar config com URL completa
	cfg.Backend = fmt.Sprintf("s3://%s/%s", backendCfg.S3Bucket, backendCfg.S3KeyPrefix)

	// Criar programa Pulumi
	var program pulumi.RunFunc
	switch cfg.Provider {
	case "aws":
		program = programs.CreateEKSProgram(cfg)
	default:
		return nil, fmt.Errorf("provider '%s' não suportado", cfg.Provider)
	}

	// Workspace directory
	workDir := filepath.Join(os.TempDir(), "pulumi-k8s-cloud", cfg.StackName)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("erro ao criar diretório: %w", err)
	}

	logger.Debugf("📁 Workspace: %s", workDir)

	// Criar projeto
	project := workspace.Project{
		Name:    tokens.PackageName(cfg.ProjectName),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{
			URL: cfg.Backend,
		},
	}

	// ✅ CRIAR WORKSPACE COM CONFIGURAÇÕES CORRETAS
	ws, err := auto.NewLocalWorkspace(ctx,
		auto.Program(program),
		auto.Project(project),
		auto.WorkDir(workDir),
		auto.PulumiHome(filepath.Join(os.Getenv("HOME"), ".pulumi")), // ✅ ADICIONAR
		auto.EnvVars(map[string]string{
			"PULUMI_CONFIG_PASSPHRASE": getPulumiPassphrase(),
			"PULUMI_SKIP_UPDATE_CHECK": "true", // ✅ ADICIONAR (evita check de updates)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar workspace: %w", err)
	}

	logger.Debug("✅ Workspace criado")

	// Criar/selecionar stack
	stack, err := auto.UpsertStackLocalSource(ctx, cfg.StackName, workDir)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar stack: %w", err)
	}

	logger.Infof("✅ Stack '%s' pronta", cfg.StackName)

	// Configurar região AWS
	if err := stack.SetConfig(ctx, "aws:region", auto.ConfigValue{Value: cfg.Region}); err != nil {
		return nil, fmt.Errorf("erro ao configurar região: %w", err)
	}

	// Instalar plugin AWS (provider)
	logger.Progress("📦 Instalando provider AWS...")
	if err := ws.InstallPlugin(ctx, "aws", "v6.52.0"); err != nil {
		logger.Warningf("⚠️  Aviso: %v", err)
	}

	return &Engine{
		config: cfg,
		stack:  stack,
		ctx:    ctx,
	}, nil
}

// ✅ NOVA FUNÇÃO: Garantir que plugin Go está instalado
func ensureGoPlugin(ctx context.Context, pulumiPath string) error {
	// Verificar se já está instalado
	cmd := exec.CommandContext(ctx, pulumiPath, "plugin", "ls")
	output, err := cmd.Output()
	if err == nil && strings.Contains(string(output), "language") && strings.Contains(string(output), "go") {
		logger.Debug("✅ Plugin Go já instalado")
		return nil
	}

	// Instalar plugin Go
	logger.Progress("📦 Instalando plugin de linguagem Go...")
	cmd = exec.CommandContext(ctx, pulumiPath, "plugin", "install", "language", "go")

	// Redirecionar output para debug
	cmd.Stdout = logger.Output
	cmd.Stderr = logger.Output

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("falha ao instalar plugin: %w", err)
	}

	logger.Success("✅ Plugin Go instalado")
	return nil
}

// configurePulumiPassphrase configura passphrase fixa
func configurePulumiPassphrase() error {
	if os.Getenv("PULUMI_CONFIG_PASSPHRASE") == "" {
		// Passphrase fixa para uso não-interativo
		// Segurança real vem do S3 encryption + AWS IAM
		os.Setenv("PULUMI_CONFIG_PASSPHRASE", "k8s-cloud-automation-2024")
		logger.Debug("✅ Passphrase configurada")
	}
	return nil
}

// getPulumiPassphrase retorna passphrase
func getPulumiPassphrase() string {
	if pass := os.Getenv("PULUMI_CONFIG_PASSPHRASE"); pass != "" {
		return pass
	}
	return "k8s-cloud-automation-2024"
}

// Up cria ou atualiza a infraestrutura
func (e *Engine) Up(dryRun bool) (*UpResult, error) {
	timer := logger.NewTimer("Deployment")
	defer timer.Stop()

	logger.Separator()
	logger.Infof("🚀 Iniciando deployment do cluster '%s'", e.config.Name)
	logger.Infof("📍 Região: %s", e.config.Region)
	logger.Infof("🔢 Versão K8s: %s", e.config.K8sVersion)
	logger.Infof("💾 Backend: %s", e.config.Backend)
	logger.Separator()

	if dryRun {
		logger.Warning("🔍 Modo DRY-RUN: Executando preview sem aplicar mudanças")

		// Preview com streams corretos
		preview, err := e.stack.Preview(e.ctx, optpreview.ProgressStreams(os.Stdout))
		if err != nil {
			return nil, fmt.Errorf("erro no preview: %w", err)
		}

		// Converter ChangeSummary de map[apitype.OpType]int para map[string]int
		changeSummary := convertChangeSummary(preview.ChangeSummary)

		logger.Separator()
		logger.Infof("📊 Preview completo:")
		logger.Infof("  • Recursos a criar: %d", changeSummary["create"])
		logger.Infof("  • Recursos a atualizar: %d", changeSummary["update"])
		logger.Infof("  • Recursos a deletar: %d", changeSummary["delete"])
		logger.Infof("  • Recursos inalterados: %d", changeSummary["same"])
		logger.Separator()

		return &UpResult{
			Success: true,
			DryRun:  true,
			Summary: changeSummary,
		}, nil
	}

	// Executar deployment real
	logger.Progress("⚙️  Aplicando mudanças...")

	upResult, err := e.stack.Up(e.ctx, optup.ProgressStreams(os.Stdout))
	if err != nil {
		return nil, fmt.Errorf("❌ Erro no deployment: %w", err)
	}

	// Obter outputs
	outputs, err := e.stack.Outputs(e.ctx)
	if err != nil {
		logger.Warningf("⚠️  Não foi possível obter outputs: %v", err)
	}

	// Extrair ResourceChanges corretamente
	resourceChanges := make(map[string]int)
	if upResult.Summary.ResourceChanges != nil {
		for k, v := range *upResult.Summary.ResourceChanges {
			resourceChanges[k] = v
		}
	}

	logger.Separator()
	logger.Success("🎉 Deployment concluído com sucesso!")
	logger.Infof("📊 Resumo:")
	logger.Infof("  • Recursos criados: %d", resourceChanges["create"])
	logger.Infof("  • Recursos atualizados: %d", resourceChanges["update"])
	logger.Infof("  • Recursos inalterados: %d", resourceChanges["same"])

	if len(outputs) > 0 {
		logger.Separator()
		logger.Info("📤 Outputs:")
		for key, output := range outputs {
			logger.Infof("  • %s: %v", key, output.Value)
		}
	}
	logger.Separator()

	return &UpResult{
		Success: true,
		DryRun:  false,
		Summary: resourceChanges,
		Outputs: outputs,
	}, nil
}

// Destroy remove toda a infraestrutura
func (e *Engine) Destroy() error {
	timer := logger.NewTimer("Destruição")
	defer timer.Stop()

	logger.Separator()
	logger.Warningf("🔥 DESTRUINDO cluster '%s'", e.config.Name)
	logger.Warning("⚠️  Esta operação é IRREVERSÍVEL!")
	logger.Separator()

	logger.Progress("🗑️  Removendo recursos...")

	_, err := e.stack.Destroy(e.ctx, optdestroy.ProgressStreams(os.Stdout))
	if err != nil {
		return fmt.Errorf("❌ Erro na destruição: %w", err)
	}

	logger.Separator()
	logger.Success("✅ Cluster destruído com sucesso!")
	logger.Separator()

	return nil
}

// Refresh atualiza state com estado real da AWS
func (e *Engine) Refresh() error {
	logger.Infof("🔄 Sincronizando state do cluster '%s'", e.config.Name)

	_, err := e.stack.Refresh(e.ctx, optrefresh.ProgressStreams(os.Stdout))
	if err != nil {
		return fmt.Errorf("erro no refresh: %w", err)
	}

	logger.Success("✅ State atualizado com sucesso")
	return nil
}

// Outputs retorna outputs do Pulumi
func (e *Engine) Outputs() (auto.OutputMap, error) {
	return e.stack.Outputs(e.ctx)
}

// GetKubeconfig retorna o kubeconfig do cluster
func (e *Engine) GetKubeconfig() (string, error) {
	outputs, err := e.Outputs()
	if err != nil {
		return "", err
	}

	kubeconfigOutput, ok := outputs["kubeconfig"]
	if !ok {
		return "", fmt.Errorf("output 'kubeconfig' não encontrado")
	}

	kubeconfig, ok := kubeconfigOutput.Value.(string)
	if !ok {
		return "", fmt.Errorf("kubeconfig tem tipo inválido")
	}

	return kubeconfig, nil
}

// convertChangeSummary converte map[apitype.OpType]int para map[string]int
func convertChangeSummary(summary map[apitype.OpType]int) map[string]int {
	result := make(map[string]int)
	for opType, count := range summary {
		result[string(opType)] = count
	}
	return result
}

// UpResult resultado do deployment
type UpResult struct {
	Success bool
	DryRun  bool
	Summary map[string]int
	Outputs auto.OutputMap
}
