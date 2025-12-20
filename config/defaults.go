package config

import "time"

// Valores padrão para configuração da aplicação
const (
	// Valores padrão para StackSpot
	StackSpotBaseURL         = "https://genai-inference-app.stackspot.com/v1" // ATUALIZADO
	StackSpotDefaultModel    = "StackSpotAI"
	StackSpotResponseTimeout = 2 * time.Second
	DefaultLogFile           = "$HOME/app.log"
	DefaultStackSpotRealm    = "zup"
	DefaultStackSpotAgentID  = "default"

	// Valores padrão para OpenAI
	DefaultOpenAIModel       = "gpt-4o-mini"
	DefaultOpenAiAssistModel = "gpt-4o-mini"
	OpenAIAPIURL             = "https://api.openai.com/v1/chat/completions"
	OpenAIResponsesAPIURL    = "https://api.openai.com/v1/responses"

	// Valores padrão para ClaudeAI
	DefaultClaudeAIModel      = "claude-3-5-sonnet-20241022"
	ClaudeAIAPIURL            = "https://api.anthropic.com/v1/messages"
	ClaudeAIAPIVersionDefault = "2023-06-01" // Versão padrão da APIClaudeAI

	// Valores padrão para Google Gemini
	DefaultGoogleAIModel   = "gemini-2.0-flash-lite"
	GoogleAIAPIURL         = "https://generativelanguage.googleapis.com/v1beta"
	DefaultGoogleAITimeout = 5 * time.Minute

	// Valores padrão para xAI
	DefaultXAIModel = "grok-code-fast-1"
	XAIAPIURL       = "https://api.x.ai/v1/chat/completions"

	// Valores padrão para Ollama
	DefaultOllamaModel          = "gpt-oss:20b"
	OllamaDefaultBaseURL        = "http://localhost:11434"
	OllamaFilterThinkingDefault = "true"

	// Provedor padrão
	DefaultLLMProvider = "OPENAI"

	// Definição do tamanho de historico
	DefaultMaxHistorySize = 100 * 1024 * 1024 // 100MB

	// Constantes para os possíveis valores do campo Status em ResponseData
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusError      = "error"

	// Configurado MaxlogSize Default
	DefaultMaxLogSize = 100 // 100MB

	// Modos de processamento de arquivos
	ModeFull       = "full"    // Envia o conteúdo completo (comportamento atual)
	ModeSummary    = "summary" // Envia apenas uma descrição estrutural
	ModeChunked    = "chunked" // Divide em partes menores para processamento em sequência
	ModeSmartChunk = "smart"   // Seleciona partes relevantes com base na consulta do usuário

	// Configuração de retry
	DefaultMaxRetries     = 5               // Máximo de tentativas para retry
	DefaultInitialBackoff = 3 * time.Second // Backoff inicial para retry
	DefaultPluginTimeout  = 15 * time.Minute

	// Agent plugin max turns
	AgentPluginMaxTurnsEnv = "CHATCLI_AGENT_PLUGIN_MAX_TURNS"
	DefaultAgentMaxTurns   = 7
	MaxAgentMaxTurns       = 200 // limite de segurança
)
