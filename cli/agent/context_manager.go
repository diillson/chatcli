/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ContextManager gerencia contexto de execução para o agente
type ContextManager struct {
	logger         *zap.Logger
	defaultTimeout time.Duration
}

// NewContextManager cria uma nova instância do gerenciador de contexto
func NewContextManager(logger *zap.Logger) *ContextManager {
	// Ler timeout de ENV ou usar padrão
	timeout := 10 * time.Minute
	if envTimeout := os.Getenv("CHATCLI_AGENT_CMD_TIMEOUT"); envTimeout != "" {
		if d, err := time.ParseDuration(envTimeout); err == nil && d > 0 {
			timeout = d
		}
	}

	return &ContextManager{
		logger:         logger,
		defaultTimeout: timeout,
	}
}

// CreateExecutionContext cria um contexto com timeout para execução
func (cm *ContextManager) CreateExecutionContext() (context.Context, context.CancelFunc) {
	cm.logger.Debug("Criando contexto de execução",
		zap.Duration("timeout", cm.defaultTimeout))
	return context.WithTimeout(context.Background(), cm.defaultTimeout)
}

// RequestLLMContinuation solicita continuação à LLM com contexto adicional
func (cm *ContextManager) RequestLLMContinuation(
	ctx context.Context,
	llmClient interface{}, // Será tipado corretamente depois
	history []models.Message,
	previousCommand string,
	output string,
	stderr string,
	userContext string,
) (string, error) {
	// Criar novo contexto com timeout
	newCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Sanitizar saídas
	outSafe := utils.SanitizeSensitiveText(output)
	errSafe := utils.SanitizeSensitiveText(stderr)

	// Construir prompt
	prompt := fmt.Sprintf(`O comando sugerido anteriormente foi:
    %s
    
    O resultado (stdout) foi:
    %s
    
    O erro (stderr) foi:
    %s
    
    Por favor, sugira uma correção ou próximos passos baseados no resultado e no contexto fornecido.
    Forneça comandos executáveis no formato apropriado.`, previousCommand, outSafe, errSafe)

	if userContext != "" {
		prompt += fmt.Sprintf("\n\nContexto adicional fornecido pelo usuário:\n%s", userContext)
	}

	cm.logger.Debug("Solicitando continuação à LLM",
		zap.Int("history_size", len(history)),
		zap.Int("prompt_length", len(prompt)))

	deadline, _ := newCtx.Deadline()
	cm.logger.Debug("Contexto para continuação criado", zap.Time("deadline", deadline))

	// Aqui será feita a chamada à LLM (implementaremos na integração)
	return prompt, nil
}

// RequestLLMWithPreExecutionContext solicita refinamento antes da execução
func (cm *ContextManager) RequestLLMWithPreExecutionContext(
	ctx context.Context,
	llmClient interface{},
	history []models.Message,
	originalCommand string,
	userContext string,
) (string, error) {
	newCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	prompt := fmt.Sprintf(`O comando que você sugeriu foi:
    `+"```"+`
    %s
    `+"```"+`
    
    Antes de executá-lo, o usuário forneceu o seguinte contexto ou instrução adicional:
    %s
    
    Por favor, revise o comando sugerido com base neste novo contexto. Se necessário, modifique-o ou sugira um novo conjunto de comandos. Apresente os novos comandos no formato executável apropriado.`, originalCommand, userContext)

	cm.logger.Debug("Solicitando refinamento pré-execução",
		zap.String("original_command", originalCommand),
		zap.Int("context_length", len(userContext)))

	// Retornar o prompt construído (implementação completa virá depois)
	_ = newCtx // Para evitar warning
	return prompt, nil
}

// SetDefaultTimeout atualiza o timeout padrão
func (cm *ContextManager) SetDefaultTimeout(timeout time.Duration) {
	if timeout > 0 {
		cm.defaultTimeout = timeout
		cm.logger.Info("Timeout padrão atualizado", zap.Duration("new_timeout", timeout))
	}
}

// GetDefaultTimeout retorna o timeout padrão configurado
func (cm *ContextManager) GetDefaultTimeout() time.Duration {
	return cm.defaultTimeout
}
