package config

import "time"

// Valores padrão para configuração da aplicação
const (
	// Valores padrão para StackSpot
	StackSpotBaseURL         = "https://genai-inference-app.stackspot.com/v1" // ATUALIZADO
	StackSpotDefaultModel    = "StackSpotAI"
	StackSpotResponseTimeout = 2 * time.Second
	DefaultLogFile           = "~/.chatcli/app.log"
	DefaultStackSpotRealm    = "zup"
	DefaultStackSpotAgentID  = "default"

	// Valores padrão para OpenAI
	DefaultOpenAIModel       = "gpt-5.4"
	DefaultOpenAiAssistModel = "gpt-4o"
	OpenAIAPIURL             = "https://api.openai.com/v1/chat/completions"
	OpenAIResponsesAPIURL    = "https://api.openai.com/v1/responses"

	// OAuth (ChatGPT plan) endpoint — used when authenticated via OAuth instead of API key
	OpenAIOAuthResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

	// Valores padrão para ClaudeAI
	DefaultClaudeAIModel      = "claude-sonnet-4-6"
	ClaudeAIAPIURL            = "https://api.anthropic.com/v1/messages"
	ClaudeAIAPIVersionDefault = "2023-06-01" // Versão padrão da APIClaudeAI

	// Valores padrão para Google Gemini
	DefaultGoogleAIModel   = "gemini-2.5-flash"
	GoogleAIAPIURL         = "https://generativelanguage.googleapis.com/v1beta"
	DefaultGoogleAITimeout = 5 * time.Minute

	// Valores padrão para xAI
	DefaultXAIModel = "grok-4-1"
	XAIAPIURL       = "https://api.x.ai/v1/chat/completions"

	// Valores padrão para ZAI (Zhipu AI / z.ai)
	DefaultZAIModel  = "glm-5"
	ZAIAPIURL        = "https://api.z.ai/api/paas/v4/chat/completions"
	DefaultZAIJWTTTL = 30 * time.Minute // JWT token validity for ZAI auth

	// Valores padrão para MiniMax
	DefaultMiniMaxModel    = "MiniMax-M2.7"
	MiniMaxAPIURL          = "https://api.minimax.io/v1/text/chatcompletion_v2"
	MiniMaxAnthropicAPIURL = "https://api.minimax.io/anthropic/v1/messages"

	// Valores padrão para GitHub Copilot
	DefaultCopilotModel = "gpt-4o"
	CopilotAPIURL       = "https://api.githubcopilot.com/chat/completions"

	// Valores padrão para GitHub Models (marketplace)
	DefaultGitHubModelsModel = "gpt-4o"
	GitHubModelsAPIURL       = "https://models.inference.ai.azure.com/chat/completions"

	// Valores padrão para OpenRouter
	DefaultOpenRouterModel = "openai/gpt-5.2"
	OpenRouterAPIURL       = "https://openrouter.ai/api/v1/chat/completions"

	// Valores padrão para AWS Bedrock (modelos Anthropic Claude)
	// Model ids seguem o formato Bedrock (ex.: "anthropic.claude-3-5-sonnet-20241022-v2:0").
	// Para inference profiles regionais, use o prefixo apropriado (ex.: "us.anthropic.claude-sonnet-4-20250514-v1:0").
	// Default usa inference profile global (exigido por modelos 3.7+ / 4.x+).
	DefaultBedrockModel  = "global.anthropic.claude-sonnet-4-5-20250929-v1:0"
	DefaultBedrockRegion = "us-east-1"

	// Valores padrão para Ollama
	DefaultOllamaModel          = "gpt-oss:20b"
	OllamaDefaultBaseURL        = "http://localhost:11434"
	OllamaFilterThinkingDefault = "true"

	// Provedor padrão
	DefaultLLMProvider = "OPENAI"

	// Definição do tamanho de historico
	DefaultMaxHistorySize = 100 * 1024 * 1024 // 100MB
	DefaultHistoryFile    = ".chatcli_history"

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
	DefaultAgentMaxTurns   = 50
	MaxAgentMaxTurns       = 200 // limite de segurança

	// Memory system configuration
	DefaultMemoryMaxSize      = 32 * 1024 // 32KB max for MEMORY.md
	DefaultDailyNoteRetention = 30        // days to keep daily notes
	DefaultMaxFacts           = 500       // max facts in index
	DefaultRetrievalBudget    = 4000      // chars for system prompt memory section
	DefaultDecayHalfLife      = 30.0      // days for score decay half-life

	// Memory system environment variable overrides
	MemoryMaxSizeEnv      = "CHATCLI_MEMORY_MAX_SIZE"
	MemoryRetentionEnv    = "CHATCLI_MEMORY_RETENTION_DAYS"
	MemoryMaxFactsEnv     = "CHATCLI_MEMORY_MAX_FACTS"
	MemoryRetrievalEnv    = "CHATCLI_MEMORY_RETRIEVAL_BUDGET"
	MemoryCompactionHours = 24 // hours between compaction runs
)
