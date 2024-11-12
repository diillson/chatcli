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
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		fmt.Println("Nenhum arquivo .env encontrado, continuando sem ele")
	} else {
		fmt.Printf("Erro ao carregar o arquivo .env: %v\n", err)
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
	utils.CheckEnvVariables(logger, defaultSlugName, defaultTenantName)

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
