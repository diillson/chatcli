package pulumi_orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/plugins-examples/chatcli-eks/pkg/config"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optrefresh"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const projectName = "chatcli-eks-platform"

func getStackForCreate(ctx context.Context, backendURL string, cfg *config.EKSConfig) (auto.Stack, error) {
	pulumiProgram := DefineEKSInfrastructure(cfg)

	var wsOpts []auto.LocalWorkspaceOption
	envVars := map[string]string{}

	if backendURL != "" {
		envVars["PULUMI_BACKEND_URL"] = backendURL
	}

	secretsProviderURL := buildSecretsProviderURL(cfg)
	if secretsProviderURL == "" && cfg.SecretsProvider == "awskms" {
		return auto.Stack{}, fmt.Errorf(
			"❌ Falha ao configurar KMS: KeyID=%s Provider=%s",
			cfg.KMSKeyID,
			cfg.SecretsProvider,
		)
	}

	if secretsProviderURL != "" {
		wsOpts = append(wsOpts, auto.SecretsProvider(secretsProviderURL))
	}

	if len(envVars) > 0 {
		wsOpts = append(wsOpts, auto.EnvVars(envVars))
	}

	stack, err := auto.SelectStackInlineSource(ctx, cfg.ClusterName, projectName, pulumiProgram, wsOpts...)
	if err != nil {
		// Stack não existe, criar nova
		fmt.Fprintf(os.Stderr, "📦 Stack '%s' não existe, criando nova...\n", cfg.ClusterName)
		stack, err = auto.NewStackInlineSource(ctx, cfg.ClusterName, projectName, pulumiProgram, wsOpts...)
		if err != nil {
			return auto.Stack{}, fmt.Errorf("falha ao criar stack: %w", err)
		}
		fmt.Fprintf(os.Stderr, "   ✅ Stack criada com secrets provider: %s\n\n", cfg.SecretsProvider)
	} else {
		fmt.Fprintf(os.Stderr, "📦 Stack '%s' encontrada, reutilizando configuração...\n", cfg.ClusterName)

		workspace := stack.Workspace()
		if workspace != nil {
			// Tentar verificar se consegue acessar a stack
			_, err := stack.Info(ctx)
			if err != nil && isSecretsProviderError(err) {
				fmt.Fprintf(os.Stderr, "\n")
				printSecretsProviderHelp(cfg)
				return auto.Stack{}, fmt.Errorf("secrets provider incompatível")
			}
		}

		fmt.Fprintf(os.Stderr, "   ✅ Secrets provider validado\n\n")
	}

	return stack, nil
}

// Helper para construir URL do secrets provider
func buildSecretsProviderURL(cfg *config.EKSConfig) string {
	switch strings.ToLower(cfg.SecretsProvider) {
	case "awskms":
		if cfg.KMSKeyID == "" {
			return ""
		}

		if strings.HasPrefix(cfg.KMSKeyID, "alias/") {
			return fmt.Sprintf("awskms://%s?region=%s", cfg.KMSKeyID, cfg.AWSRegion)
		}

		if strings.HasPrefix(cfg.KMSKeyID, "arn:") {
			return fmt.Sprintf("awskms://%s?region=%s", cfg.KMSKeyID, cfg.AWSRegion)
		}

		return fmt.Sprintf("awskms://alias/%s?region=%s", cfg.KMSKeyID, cfg.AWSRegion)

	case "passphrase":
		return "passphrase"

	default:
		fmt.Fprintf(os.Stderr, "❌ ERRO: secrets-provider inválido: '%s'\n", cfg.SecretsProvider)
		return ""
	}
}

func buildSecretsProviderURLFromParams(secretsProvider, kmsKeyID, awsRegion string) string {
	switch strings.ToLower(secretsProvider) {
	case "awskms":
		if kmsKeyID == "" {
			return ""
		}

		if strings.HasPrefix(kmsKeyID, "alias/") {
			return fmt.Sprintf("awskms://%s?region=%s", kmsKeyID, awsRegion)
		}

		if strings.HasPrefix(kmsKeyID, "arn:") {
			return fmt.Sprintf("awskms://%s?region=%s", kmsKeyID, awsRegion)
		}

		return fmt.Sprintf("awskms://alias/%s?region=%s", kmsKeyID, awsRegion)

	case "passphrase":
		return "passphrase"

	default:
		return ""
	}
}

func getStackForDelete(ctx context.Context, backendURL, stackName, secretsProvider, kmsKeyID, awsRegion string) (auto.Stack, error) {
	pulumiProgram := func(ctx *pulumi.Context) error { return nil }

	var wsOpts []auto.LocalWorkspaceOption
	envVars := map[string]string{}

	if backendURL != "" {
		envVars["PULUMI_BACKEND_URL"] = backendURL
	}

	secretsProviderURL := buildSecretsProviderURLFromParams(secretsProvider, kmsKeyID, awsRegion)
	if secretsProviderURL != "" {
		wsOpts = append(wsOpts, auto.SecretsProvider(secretsProviderURL))
	}

	if len(envVars) > 0 {
		wsOpts = append(wsOpts, auto.EnvVars(envVars))
	}

	stack, err := auto.SelectStackInlineSource(ctx, stackName, projectName, pulumiProgram, wsOpts...)
	if err != nil {
		return auto.Stack{}, fmt.Errorf("❌ Stack '%s' não encontrada no projeto '%s'. \n   DICA: Verifique se você passou o mesmo '--name' usado na criação (ex: 'prod-eks') e não o ID da AWS.", stackName, projectName)
	}
	return stack, nil
}

func refreshStackState(ctx context.Context, stack auto.Stack, clusterName string) error {
	fmt.Fprintf(os.Stderr, "🔄 Validando estado real dos recursos na stack '%s'...\n", clusterName)

	workspace := stack.Workspace()
	if workspace == nil {
		return fmt.Errorf("workspace inválido para refresh")
	}

	refreshRes, err := stack.Refresh(ctx, optrefresh.ProgressStreams(os.Stderr))
	if err != nil {
		// ✅ Detectar erro de secrets provider
		if isSecretsProviderError(err) {
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stderr, "❌ ERRO NO REFRESH: Secrets provider não configurado corretamente\n")
			fmt.Fprintf(os.Stderr, "   Isso geralmente acontece quando:\n")
			fmt.Fprintf(os.Stderr, "   • Stack foi criada com um secrets provider diferente\n")
			fmt.Fprintf(os.Stderr, "   • Variável de ambiente PULUMI_CONFIG_PASSPHRASE não está definida\n")
			fmt.Fprintf(os.Stderr, "   • Chave KMS especificada não existe ou sem permissão\n")
			fmt.Fprintf(os.Stderr, "\n")
			return fmt.Errorf("secrets provider incompatível no refresh")
		}
		return fmt.Errorf("erro ao validar estado: %w", err)
	}

	// Mostrar resumo do refresh
	if refreshRes.Summary.ResourceChanges != nil {
		changes := *refreshRes.Summary.ResourceChanges
		if changes["update"] > 0 || changes["delete"] > 0 {
			fmt.Fprintf(os.Stderr, "⚠️  DIVERGÊNCIAS DETECTADAS:\n")
			if changes["update"] > 0 {
				fmt.Fprintf(os.Stderr, "   - %d recursos modificados externamente\n", changes["update"])
			}
			if changes["delete"] > 0 {
				fmt.Fprintf(os.Stderr, "   - %d recursos deletados externamente\n", changes["delete"])
			}
		} else {
			fmt.Fprintf(os.Stderr, "✅ Estado sincronizado com a realidade\n")
		}
	}

	return nil
}

func CreateOrUpdateEKS(ctx context.Context, backendURL string, cfg *config.EKSConfig) error {
	os.Setenv("AWS_REGION", cfg.AWSRegion)

	stack, err := getStackForCreate(ctx, backendURL, cfg)
	if err != nil {
		if isSecretsProviderError(err) {
			printSecretsProviderHelp(cfg)
		}
		return err
	}

	if err := stack.SetConfig(ctx, "aws:region", auto.ConfigValue{Value: cfg.AWSRegion}); err != nil {
		return err
	}

	if cfg.RefreshState {
		fmt.Fprintf(os.Stderr, "🔐 Validando acesso ao secrets provider...\n")

		// Tentar ler configuração da stack (força descriptografia)
		workspace := stack.Workspace()
		projectSettings, err := workspace.ProjectSettings(ctx)
		if err != nil {
			if isSecretsProviderError(err) {
				fmt.Fprintf(os.Stderr, "\n")
				fmt.Fprintf(os.Stderr, "❌ ERRO: Stack incompatível com secrets provider atual\n")
				printSecretsProviderHelp(cfg)
				return fmt.Errorf("secrets provider incompatível: %w", err)
			}
		}

		// Validar backend antes do refresh
		if projectSettings != nil {
			fmt.Fprintf(os.Stderr, "   ✅ Secrets provider validado\n\n")
		}

		// Agora sim fazer refresh (com tratamento melhorado)
		fmt.Fprintf(os.Stderr, "🔄 Validando estado real dos recursos...\n")
		if err := refreshStackState(ctx, stack, cfg.ClusterName); err != nil {
			// Se erro for de secrets provider, aborta
			if isSecretsProviderError(err) {
				fmt.Fprintf(os.Stderr, "\n")
				printSecretsProviderHelp(cfg)
				return fmt.Errorf("erro crítico no refresh: %w", err)
			}

			// Outros erros: apenas aviso
			fmt.Fprintf(os.Stderr, "⚠️  Aviso: Refresh falhou (pode ser temporário): %v\n", err)
			fmt.Fprintf(os.Stderr, "   Continuando com operação...\n\n")
		}
	}

	fmt.Fprintf(os.Stderr, "🚀 Provisionando Stack '%s' (Project: %s)...\n", cfg.ClusterName, projectName)

	res, err := stack.Up(ctx, optup.ProgressStreams(os.Stderr))
	if err != nil {
		if isSecretsProviderError(err) {
			fmt.Fprintf(os.Stderr, "\n")
			printSecretsProviderHelp(cfg)
		}
		return fmt.Errorf("erro no provisionamento: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✅ Sucesso!\n")
	for k, v := range res.Outputs {
		if k != "kubeconfig" {
			fmt.Fprintf(os.Stderr, "  - %s: %v\n", k, v.Value)
		}
	}
	return nil
}

func DestroyEKS(ctx context.Context, backendURL, clusterName, region string, refreshState bool, secretsProvider, kmsKeyID, configPassphrase string) error {
	os.Setenv("AWS_REGION", region)

	// Se usar passphrase, garantir que está no ambiente
	if strings.ToLower(secretsProvider) == "passphrase" {
		finalPassphrase := configPassphrase
		if finalPassphrase == "" {
			finalPassphrase = os.Getenv("PULUMI_CONFIG_PASSPHRASE")
		}
		if finalPassphrase != "" {
			os.Setenv("PULUMI_CONFIG_PASSPHRASE", finalPassphrase)
		}
	}

	stack, err := getStackForDelete(ctx, backendURL, clusterName, secretsProvider, kmsKeyID, region)
	if err != nil {
		return err
	}

	if refreshState {
		if err := refreshStackState(ctx, stack, clusterName); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Aviso: Falha no refresh: %v\n", err)
			fmt.Fprintf(os.Stderr, "   Continuando com destruição...\n")
		}
	}

	fmt.Fprintf(os.Stderr, "🔥 Destruindo Stack '%s'...\n", clusterName)

	res, err := stack.Destroy(ctx, optdestroy.ProgressStreams(os.Stderr))
	if err != nil {
		return err
	}

	deletedCount := 0
	if res.Summary.ResourceChanges != nil {
		deletedCount = (*res.Summary.ResourceChanges)["delete"]
	}

	if deletedCount == 0 {
		fmt.Fprintf(os.Stderr, "⚠️  AVISO: O comando rodou, mas 0 recursos foram deletados. A stack já estava vazia?\n")
	} else {
		fmt.Fprintf(os.Stderr, "✅ Destruído. Total de recursos removidos: %d\n", deletedCount)
	}

	return nil
}

// isSecretsProviderError detecta erros de secrets provider
func isSecretsProviderError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "passphrase must be set") ||
		strings.Contains(errStr, "pulumi_config_passphrase") ||
		strings.Contains(errStr, "incorrect passphrase") ||
		strings.Contains(errStr, "secrets manager")
}

// printSecretsProviderHelp exibe orientações de resolução
func printSecretsProviderHelp(cfg *config.EKSConfig) {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, strings.Repeat("=", 70))
	fmt.Fprintln(os.Stderr, "❌ ERRO: CONFLITO DE SECRETS PROVIDER")
	fmt.Fprintln(os.Stderr, strings.Repeat("=", 70))
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "A stack '%s' foi criada com um secrets provider diferente.\n", cfg.ClusterName)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "📋 SOLUÇÕES RECOMENDADAS (em ordem de preferência):")
	fmt.Fprintln(os.Stderr, "")

	// Solução 1: Usar secrets provider original
	fmt.Fprintln(os.Stderr, "1️⃣  USAR O SECRETS PROVIDER ORIGINAL (mais simples)")
	fmt.Fprintln(os.Stderr, "   Se a stack foi criada com passphrase:")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "   export PULUMI_CONFIG_PASSPHRASE='sua-senha-original'\n")
	fmt.Fprintf(os.Stderr, "   @eks create \\\n")
	fmt.Fprintf(os.Stderr, "     --name=%s \\\n", cfg.ClusterName)
	fmt.Fprintf(os.Stderr, "     --state-bucket-name=<seu-bucket> \\\n")
	fmt.Fprintf(os.Stderr, "     --secrets-provider=passphrase \\\n")
	fmt.Fprintf(os.Stderr, "     # ... outras flags\n")
	fmt.Fprintln(os.Stderr, "")

	// Solução 2: Novo backend
	fmt.Fprintln(os.Stderr, "2️⃣  USAR NOVO BACKEND S3 (recomendado para produção)")
	fmt.Fprintln(os.Stderr, "   Crie um novo bucket e use KMS desde o início:")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "   @eks create \\\n")
	fmt.Fprintf(os.Stderr, "     --name=%s \\\n", cfg.ClusterName)
	fmt.Fprintf(os.Stderr, "     --state-bucket-name=<novo-bucket> \\\n")
	fmt.Fprintf(os.Stderr, "     --secrets-provider=awskms \\\n")
	if cfg.KMSKeyID != "" {
		fmt.Fprintf(os.Stderr, "     --kms-key-id=%s \\\n", cfg.KMSKeyID)
	}
	fmt.Fprintf(os.Stderr, "     # ... outras flags\n")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "   💡 Vantagens:")
	fmt.Fprintln(os.Stderr, "      • Estado limpo e isolado")
	fmt.Fprintln(os.Stderr, "      • Melhor segurança com KMS")
	fmt.Fprintln(os.Stderr, "      • Evita conflitos de configuração")
	fmt.Fprintln(os.Stderr, "")

	// Solução 3: Delete e recrie
	fmt.Fprintln(os.Stderr, "3️⃣  DELETAR E RECRIAR STACK (mais drástico)")
	fmt.Fprintln(os.Stderr, "   ⚠️  Cuidado: Isso DESTRUIRÁ o cluster atual!")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "   # Backup primeiro (manual via AWS Console)\n")
	fmt.Fprintf(os.Stderr, "   @eks delete --name=%s --secrets-provider=passphrase\n", cfg.ClusterName)
	fmt.Fprintf(os.Stderr, "   @eks create --name=%s --secrets-provider=awskms ...\n", cfg.ClusterName)
	fmt.Fprintln(os.Stderr, "")

	// Solução 4: Pulumi CLI manual
	fmt.Fprintln(os.Stderr, "4️⃣  MIGRAÇÃO MANUAL COM PULUMI CLI (avançado)")
	fmt.Fprintln(os.Stderr, "   Se você tem Pulumi CLI instalado:")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "   pulumi login s3://<seu-bucket>\n")
	fmt.Fprintf(os.Stderr, "   pulumi stack select %s\n", cfg.ClusterName)
	fmt.Fprintf(os.Stderr, "   pulumi stack change-secrets-provider \\\n")
	if cfg.SecretsProvider == "awskms" && cfg.KMSKeyID != "" {
		fmt.Fprintf(os.Stderr, "     \"awskms://%s?region=%s\"\n", cfg.KMSKeyID, cfg.AWSRegion)
	} else {
		fmt.Fprintf(os.Stderr, "     \"awskms://alias/your-key?region=%s\"\n", cfg.AWSRegion)
	}
	fmt.Fprintln(os.Stderr, "")

	fmt.Fprintln(os.Stderr, strings.Repeat("=", 70))
	fmt.Fprintln(os.Stderr, "")
}
