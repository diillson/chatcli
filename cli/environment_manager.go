package cli

import (
	"fmt"
	"github.com/joho/godotenv"
	"os"

	"github.com/diillson/chatcli/llm"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// EnvironmentManager gerencia a reconfiguração de variáveis de ambiente
type EnvironmentManager struct{}

// ReloadConfiguration recarrega as variáveis de ambiente e reconfigura o LLMManager
func (em *EnvironmentManager) ReloadConfiguration(logger *zap.Logger, manager *llm.LLMManager) {
	fmt.Println("Recarregando configurações...")

	variablesToUnset := []string{
		"LOG_LEVEL",
		"ENV",
		"LLM_PROVIDER",
		"LOG_FILE",
		"OPENAI_API_KEY",
		"OPENAI_MODEL",
		"CLAUDEAI_API_KEY",
		"CLAUDEAI_MODEL",
		"CLIENT_ID",
		"CLIENT_SECRET",
		"SLUG_NAME",
		"TENANT_NAME",
	}

	for _, variable := range variablesToUnset {
		os.Unsetenv(variable) // Limpa todas as variáveis de ambiente em memória
	}

	// Usar godotenv.Load() para carregar o arquivo .env
	if err := godotenv.Load(); err != nil {
		fmt.Println("Nenhum arquivo .env encontrado, continuando sem ele")
	}

	// Reconfigurar o logger
	logger.Info("Reconfigurando o logger...")
	newLogger, err := utils.InitializeLogger()
	if err != nil {
		logger.Error("Erro ao reinicializar o logger", zap.Error(err))
		return
	}
	logger = newLogger
	logger.Info("Logger reconfigurado com sucesso")

	// Reconfigurar o LLMManager
	utils.CheckEnvVariables(logger, "defaultSlug", "defaultTenant")
}
