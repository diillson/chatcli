package config

import "time"

// Valores padrão para configuração da aplicação
const (
	// Valores padrão para StackSpot
	DefaultSlugName          = "testeai"
	DefaultTenantName        = "zup"
	StackSpotBaseURL         = "https://genai-code-buddy-api.stackspot.com/v1/quick-commands"
	DefaultMaxAttempts       = 50
	DefaultBackoff           = 300 * time.Second
	StackSpotDefaultModel    = "StackSpotAI"
	StackSpotResponseTimeout = 2 * time.Second
	DefaultLogFile           = "$HOME/app.log"

	// Valores padrão para OpenAI
	DefaultOpenAIModel       = "gpt-4o-mini"
	DefaultOpenAiAssistModel = "gpt-4o-mini"
	OpenAIAPIURL             = "https://api.openai.com/v1/chat/completions"
	OpenAIResponsesAPIURL    = "https://api.openai.com/v1/responses"
	OpenAIDefaultMaxAttempts = 3
	OpenAIDefaultBackoff     = time.Second
	//OpenAIAIDefaultMaxTokens = 10384 // Valor padrão para max_tokens

	// Valores padrão para ClaudeAI
	DefaultClaudeAIModel       = "claude-3-5-sonnet-20241022"
	ClaudeAIAPIURL             = "https://api.anthropic.com/v1/messages"
	ClaudeAIDefaultMaxAttempts = 3
	ClaudeAIDefaultBackoff     = time.Second
	ClaudeAIDefaultMaxTokens   = 8192         // Valor padrão para max_tokens
	ClaudeAIAPIVersionDefault  = "2023-06-01" // Versão padrão da APIClaudeAI

	// Valores padrão para Google Gemini
	DefaultGoogleAIModel       = "gemini-2.0-flash-lite"
	GoogleAIAPIURL             = "https://generativelanguage.googleapis.com/v1beta"
	GoogleAIDefaultMaxAttempts = 3
	GoogleAIDefaultBackoff     = time.Second
	GoogleAIDefaultMaxTokens   = 8192
	DefaultGoogleAITimeout     = 5 * time.Minute

	// Provedor padrão
	DefaultLLMProvider = "OPENAI"

	// Definição do tamanho de historico
	DefaultMaxHistorySize = 50 * 1024 * 1024 // 50MB

	// Constantes para os possíveis valores do campo Status em ResponseData
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusError      = "error"

	// Configurado MaxlogSize Default
	DefaultMaxLogSize = 50 // 10MB

	// Modos de processamento de arquivos
	ModeFull       = "full"    // Envia o conteúdo completo (comportamento atual)
	ModeSummary    = "summary" // Envia apenas uma descrição estrutural
	ModeChunked    = "chunked" // Divide em partes menores para processamento em sequência
	ModeSmartChunk = "smart"   // Seleciona partes relevantes com base na consulta do usuário
)
