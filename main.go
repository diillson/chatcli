package main

import (
	"fmt"
	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/llm"
	"github.com/diillson/chatcli/utils"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	// Carregar variáveis de ambiente
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Nenhum arquivo .env encontrado, continuando sem ele")
	}

	logger, err := utils.InitializeLogger()
	if err != nil {
		panic(fmt.Sprintf("Não foi possível inicializar o logger: %v", err))
	}
	defer logger.Sync()

	// Obter slug e tenantname das variáveis de ambiente com valores padrão
	slugName := llm.GetEnv("SLUG_NAME", "testeai", logger)
	tenantName := llm.GetEnv("TENANT_NAME", "zup", logger)

	// Inicializar o LLMManager com TokenManager configurado
	manager, err := llm.NewLLMManager(logger, slugName, tenantName)
	if err != nil {
		logger.Fatal("Erro ao inicializar o LLMManager", zap.Error(err))
	}

	// Inicializar e iniciar o ChatCLI
	chatCLI, err := cli.NewChatCLI(manager, logger)
	if err != nil {
		logger.Fatal("Erro ao inicializar o ChatCLI", zap.Error(err))
	}

	chatCLI.Start()
}
