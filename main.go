package main

import (
	"fmt"
	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/llm"
	"github.com/diillson/chatcli/utils"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"os"
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
		os.Exit(1) // Usar os.Exit para encerrar a aplicação em caso de erro crítico
	}
	defer logger.Sync()

	// Obter slug e tenantname das variáveis de ambiente com valores padrão
	slugName, slugIsDefault := utils.GetEnv("SLUG_NAME", "testeai", logger)
	tenantName, tenantIsDefault := utils.GetEnv("TENANT_NAME", "zup", logger)

	// Se slugName ou tenantName estão usando valores padrão, mostrar mensagem para o usuário
	if slugIsDefault || tenantIsDefault {
		fmt.Println("ATENÇÃO: As seguintes variáveis de ambiente não foram definidas e valores padrão estão sendo usados:")
		if slugIsDefault {
			fmt.Printf("- SLUG_NAME não definido, usando valor padrão: %s\n\n", slugName)
		}
		if tenantIsDefault {
			fmt.Printf("- TENANT_NAME não definido, usando valor padrão: %s\n\n", tenantName)
		}
	}

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
