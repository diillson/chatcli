package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/llm"
	"github.com/diillson/chatcli/utils"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

const (
	defaultSlugName   = "testeai"
	defaultTenantName = "zup"
)

func main() {
	// Carregar variáveis de ambiente do arquivo .env
	if err := godotenv.Load(); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Nenhum arquivo .env encontrado, continuando sem ele")
		} else {
			fmt.Printf("Erro ao carregar o arquivo .env: %v\n", err)
		}
	}

	// Inicializar o logger
	logger, err := utils.InitializeLogger()
	if err != nil {
		fmt.Printf("Não foi possível inicializar o logger: %v\n", err)
		os.Exit(1) // Encerrar a aplicação em caso de erro crítico
	}
	defer logger.Sync()

	// Configurar o contexto para o shutdown gracioso
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handleGracefulShutdown(cancel, logger)

	// Verificar variáveis de ambiente e informar o usuário
	checkEnvVariables(logger)

	// Inicializar o LLMManager
	slugName := utils.GetEnvOrDefault("SLUG_NAME", defaultSlugName)
	tenantName := utils.GetEnvOrDefault("TENANT_NAME", defaultTenantName)
	manager, err := llm.NewLLMManager(logger, slugName, tenantName)
	if err != nil {
		logger.Fatal("Erro ao inicializar o LLMManager", zap.Error(err))
	}

	// Verificar se há provedores disponíveis
	availableProviders := manager.GetAvailableProviders()
	if len(availableProviders) == 0 {
		fmt.Println("Nenhum provedor LLM está configurado. Verifique suas variáveis de ambiente.")
		os.Exit(1)
	}

	// Inicializar e iniciar o ChatCLI
	chatCLI, err := cli.NewChatCLI(manager, logger)
	if err != nil {
		logger.Fatal("Erro ao inicializar o ChatCLI", zap.Error(err))
	}

	chatCLI.Start(ctx)
}

// handleGracefulShutdown configura o tratamento de sinais para um shutdown gracioso
func handleGracefulShutdown(cancelFunc context.CancelFunc, logger *zap.Logger) {
	signals := make(chan os.Signal, 1)
	// Capturar sinais de interrupção e término
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-signals
		logger.Info("Recebido sinal para finalizar a aplicação", zap.String("sinal", sig.String()))
		cancelFunc()
	}()
}

// checkEnvVariables verifica as variáveis de ambiente necessárias e informa o usuário
func checkEnvVariables(logger *zap.Logger) {
	// Verificar SLUG_NAME e TENANT_NAME
	slugName, slugIsDefault := utils.CheckAndNotifyEnv("SLUG_NAME", defaultSlugName, logger)
	tenantName, tenantIsDefault := utils.CheckAndNotifyEnv("TENANT_NAME", defaultTenantName, logger)
	if slugIsDefault || tenantIsDefault {
		fmt.Println("ATENÇÃO: Variáveis de ambiente não definidas, usando valores padrão:")
		if slugIsDefault {
			fmt.Printf("- SLUG_NAME não definido, usando valor padrão: %s\n", slugName)
		}
		if tenantIsDefault {
			fmt.Printf("- TENANT_NAME não definido, usando valor padrão: %s\n", tenantName)
		}
	}

	// Verificar variáveis de ambiente específicas dos provedores
	checkProviderEnvVariables()
}

// checkProviderEnvVariables verifica e notifica sobre as variáveis de ambiente específicas dos provedores
func checkProviderEnvVariables() {
	// Verificar STACKSPOT
	clientID := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		fmt.Println("ATENÇÃO: Variáveis de ambiente necessárias para o provedor STACKSPOT não foram definidas:")
		if clientID == "" {
			fmt.Println("- CLIENT_ID (necessário para o provedor STACKSPOT)")
		}
		if clientSecret == "" {
			fmt.Println("- CLIENT_SECRET (necessário para o provedor STACKSPOT)")
		}
		fmt.Println("O provedor STACKSPOT não estará disponível.")
	}

	// Verificar OPENAI
	openAIKey := os.Getenv("OPENAI_API_KEY")
	if openAIKey == "" {
		fmt.Println("ATENÇÃO: OPENAI_API_KEY não definida, o provedor OPENAI não estará disponível.")
	}
}
