/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package config

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

// ConfigManager centraliza o acesso a todas as configurações.
// A ordem de prioridade é: Flags (aplicado no main) > Variáveis de Ambiente > Arquivo .env > Padrões.
type ConfigManager struct {
	mu     sync.RWMutex
	values map[string]interface{}
	logger *zap.Logger
}

// Global é a instância singleton do ConfigManager.
var Global *ConfigManager

// New cria uma nova instância do ConfigManager.
func New(logger *zap.Logger) *ConfigManager {
	return &ConfigManager{
		values: make(map[string]interface{}),
		logger: logger,
	}
}

// Load carrega as configurações de todas as fontes.
func (cm *ConfigManager) Load() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.loadDefaults()
	cm.loadEnvFile()
	cm.loadEnvVars()
}

// Reload recarrega as configurações do arquivo .env e das variáveis de ambiente.
func (cm *ConfigManager) Reload(logger *zap.Logger) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.logger = logger                       // Atualiza o logger em caso de recarga
	cm.values = make(map[string]interface{}) // Limpa valores antigos
	cm.loadDefaults()
	cm.loadEnvFile()
	cm.loadEnvVars()
	cm.logger.Info("Configurações recarregadas")
}

// loadDefaults carrega os valores padrão.
func (cm *ConfigManager) loadDefaults() {
	cm.values["LLM_PROVIDER"] = DefaultLLMProvider
	cm.values["OPENAI_MODEL"] = DefaultOpenAIModel
	cm.values["OPENAI_ASSISTANT_MODEL"] = DefaultOpenAiAssistModel
	cm.values["CLAUDEAI_MODEL"] = DefaultClaudeAIModel
	cm.values["GOOGLEAI_MODEL"] = DefaultGoogleAIModel
	cm.values["XAI_MODEL"] = DefaultXAIModel
	cm.values["OLLAMA_MODEL"] = DefaultOllamaModel
	cm.values["OLLAMA_BASE_URL"] = OllamaDefaultBaseURL
	cm.values["HISTORY_MAX_SIZE"] = "100MB"
	cm.values["LOG_MAX_SIZE"] = "100MB"
	cm.values["MAX_RETRIES"] = DefaultMaxRetries
	cm.values["INITIAL_BACKOFF"] = DefaultInitialBackoff
	cm.values["STACKSPOT_REALM"] = DefaultStackSpotRealm
	cm.values["STACKSPOT_AGENT_ID"] = DefaultStackSpotAgentID
}

// loadEnvFile carrega configurações do arquivo .env.
func (cm *ConfigManager) loadEnvFile() {
	envMap, err := godotenv.Read() // Não sobrepõe vars de ambiente existentes
	if err != nil {
		cm.logger.Debug("Arquivo .env não encontrado ou erro na leitura", zap.Error(err))
		return
	}
	for key, value := range envMap {
		cm.values[key] = value
	}
}

// loadEnvVars carrega configurações das variáveis de ambiente do sistema (maior prioridade).
func (cm *ConfigManager) loadEnvVars() {
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if len(pair) == 2 {
			cm.values[pair[0]] = pair[1]
		}
	}
}

// Set injeta um valor, tipicamente de uma flag (maior prioridade).
func (cm *ConfigManager) Set(key string, value interface{}) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.values[key] = value
}

// GetString retorna um valor de configuração como string.
func (cm *ConfigManager) GetString(key string) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if val, ok := cm.values[key]; ok {
		if strVal, ok := val.(string); ok {
			return strVal
		}
	}
	return ""
}

// GetInt retorna um valor de configuração como int.
func (cm *ConfigManager) GetInt(key string, defaultValue int) int {
	valStr := cm.GetString(key)
	if valStr == "" {
		return defaultValue
	}
	if intVal, err := strconv.Atoi(valStr); err == nil {
		return intVal
	}
	return defaultValue
}

// GetBool retorna um valor de configuração como bool.
func (cm *ConfigManager) GetBool(key string, defaultValue bool) bool {
	valStr := cm.GetString(key)
	if valStr == "" {
		return defaultValue
	}
	if boolVal, err := strconv.ParseBool(valStr); err == nil {
		return boolVal
	}
	return defaultValue
}

// GetDuration retorna um valor de configuração como time.Duration.
func (cm *ConfigManager) GetDuration(key string, defaultValue time.Duration) time.Duration {
	valStr := cm.GetString(key)
	if valStr == "" {
		return defaultValue
	}
	if durVal, err := time.ParseDuration(valStr); err == nil {
		return durVal
	}
	return defaultValue
}
