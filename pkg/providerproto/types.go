/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerproto

// ProtocolVersion is the wire contract version. The host sends the versions
// it speaks in InitializeParams; the plugin answers with the one it chose.
// Additive changes (new optional fields, new optional methods) do not bump
// it; anything a conforming peer could misread does.
const ProtocolVersion = "1"

// Method names. Requests expect a response; notifications ("$/…" and the
// ones documented as such) never do.
const (
	MethodInitialize  = "initialize"
	MethodInitialized = "initialized" // notification, host → plugin
	MethodShutdown    = "shutdown"
	MethodExit        = "exit" // notification, host → plugin

	MethodCancelRequest = "$/cancelRequest" // notification, either direction
	MethodLog           = "$/log"           // notification, plugin → host

	MethodAuthStatus   = "auth/status"
	MethodAuthLogin    = "auth/login"
	MethodAuthContinue = "auth/continue"
	MethodAuthToken    = "auth/token"
	MethodAuthLogout   = "auth/logout"

	MethodModelsList = "models/list"

	MethodChatSend  = "chat/send"
	MethodChatDelta = "chat/delta" // notification, plugin → host

	MethodTokensCount = "tokens/count"
)

// Mode says how much of the provider the plugin implements.
type Mode string

const (
	// ModeDialect: the plugin owns authentication and the model list; the
	// host talks to Dialect.BaseURL with its built-in client for Dialect.API,
	// injecting the headers returned by auth/token. Caching, native tools,
	// streaming and usage parsing come from the built-in client.
	ModeDialect Mode = "dialect"
	// ModeFull: the plugin also serves chat/send (and optionally streaming
	// and tokens/count) over a normalized message schema.
	ModeFull Mode = "full"
)

// DialectAPI names a wire format the host already implements.
type DialectAPI string

const (
	DialectOpenAIChat        DialectAPI = "openai-chat"
	DialectOpenAIResponses   DialectAPI = "openai-responses"
	DialectAnthropicMessages DialectAPI = "anthropic-messages"
	DialectGemini            DialectAPI = "gemini"
)

// ---------------------------------------------------------------- lifecycle

// HostInfo identifies the host to the plugin.
type HostInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// HostCapabilities tells the plugin what the host can do for it.
type HostCapabilities struct {
	// InteractiveAuth is true when a human can answer auth/login steps.
	// Unattended hosts (servers, CI) set it to false; plugins must then
	// fail auth/login with ErrCodeAuthRequired instead of prompting.
	InteractiveAuth bool `json:"interactiveAuth"`
}

// InitializeParams opens the session.
type InitializeParams struct {
	ProtocolVersions []string         `json:"protocolVersions"`
	Host             HostInfo         `json:"host"`
	Capabilities     HostCapabilities `json:"capabilities"`
}

// ProviderInfo describes the provider the plugin exposes.
type ProviderInfo struct {
	// Name is the provider id users select (LLM_PROVIDER, /switch). Upper
	// case, ^[A-Z][A-Z0-9_]{1,31}$, and never a built-in provider's name.
	Name string `json:"name"`
	// DisplayName may be localized; keys are BCP 47 tags ("en", "pt-BR").
	// "en" is required.
	DisplayName map[string]string `json:"displayName"`
	Version     string            `json:"version"`
	Description map[string]string `json:"description,omitempty"`
}

// Dialect configures ModeDialect.
type Dialect struct {
	API DialectAPI `json:"api"`
	// BaseURL is the API root the built-in client targets (for openai-chat,
	// the URL that "/chat/completions" is appended to).
	BaseURL string `json:"baseURL"`
}

// AuthCapabilities lists the auth methods the plugin serves.
type AuthCapabilities struct {
	Login  bool `json:"login"`
	Token  bool `json:"token"`
	Logout bool `json:"logout"`
}

// ChatCapabilities lists what chat/send supports (ModeFull).
type ChatCapabilities struct {
	Stream   bool `json:"stream"`
	Tools    bool `json:"tools"`
	Images   bool `json:"images"`
	Thinking bool `json:"thinking"`
}

// Capabilities is the plugin's side of the negotiation. A method whose
// capability is false must not be called by the host.
type Capabilities struct {
	Auth        AuthCapabilities `json:"auth"`
	Models      bool             `json:"models"`
	Chat        ChatCapabilities `json:"chat"`
	CountTokens bool             `json:"countTokens"`
}

// InitializeResult closes the handshake.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Provider        ProviderInfo `json:"provider"`
	Mode            Mode         `json:"mode"`
	Dialect         *Dialect     `json:"dialect,omitempty"`
	Capabilities    Capabilities `json:"capabilities"`
	// AuthEnv lists env vars the plugin reads for credentials. The host
	// passes only these (plus a minimal base environment) to the process
	// and treats them as secrets.
	AuthEnv []string `json:"authEnv,omitempty"`
}

// LogParams is a structured log line from the plugin.
type LogParams struct {
	Level   string `json:"level"` // debug | info | warn | error
	Message string `json:"message"`
}

// CancelParams asks the peer to abandon an in-flight request.
type CancelParams struct {
	ID any `json:"id"`
}

// --------------------------------------------------------------------- auth

// Credential is opaque to the host: the plugin mints it in auth/login,
// the host stores it encrypted and hands it back on every call that needs
// it. The plugin keeps no secrets of its own.
type Credential map[string]string

// AuthState is the outcome of auth/status.
type AuthState string

const (
	AuthLoggedIn  AuthState = "logged_in"
	AuthLoggedOut AuthState = "logged_out"
	AuthExpired   AuthState = "expired"
)

// CredentialParams carries the stored credential (nil when none).
type CredentialParams struct {
	Credential Credential `json:"credential,omitempty"`
}

// AuthStatusResult reports whether the credential is usable.
type AuthStatusResult struct {
	State     AuthState `json:"state"`
	Account   string    `json:"account,omitempty"`
	ExpiresAt string    `json:"expiresAt,omitempty"` // RFC 3339
}

// AuthLoginParams starts a login.
type AuthLoginParams struct {
	// Inputs pre-answers prompt fields by id (from flags or env), so an
	// unattended host can log in without steps.
	Inputs map[string]string `json:"inputs,omitempty"`
}

// StepKind is what the host must show the user.
type StepKind string

const (
	StepOpenURL    StepKind = "open_url"
	StepDeviceCode StepKind = "device_code"
	StepPrompt     StepKind = "prompt"
	StepDone       StepKind = "done"
)

// PromptField is one value the user types.
type PromptField struct {
	ID      string            `json:"id"`
	Label   map[string]string `json:"label"`
	Secret  bool              `json:"secret,omitempty"`
	Default string            `json:"default,omitempty"`
	// Choices restricts the answer (rendered as a picker).
	Choices []string `json:"choices,omitempty"`
}

// AuthStep is one round of the login state machine. The host renders it,
// collects answers and calls auth/continue with the same Session until a
// StepDone arrives. Plugins never touch the terminal, so the same flow
// works in the CLI, the web UI, IDE clients and messaging channels.
type AuthStep struct {
	Kind    StepKind `json:"kind"`
	Session string   `json:"session,omitempty"`

	Message map[string]string `json:"message,omitempty"`
	URL     string            `json:"url,omitempty"`       // open_url, device_code
	Code    string            `json:"code,omitempty"`      // device_code
	PollMs  int               `json:"pollMs,omitempty"`    // device_code: call auth/continue every PollMs
	Fields  []PromptField     `json:"fields,omitempty"`    // prompt
	Account string            `json:"account,omitempty"`   // done
	Expires string            `json:"expiresAt,omitempty"` // done, RFC 3339
	// Credential is set on StepDone only.
	Credential Credential `json:"credential,omitempty"`
}

// AuthContinueParams answers the previous step.
type AuthContinueParams struct {
	Session string            `json:"session"`
	Inputs  map[string]string `json:"inputs,omitempty"`
}

// AuthTokenResult is what the host attaches to requests. In ModeDialect the
// headers go on every HTTP request to Dialect.BaseURL.
type AuthTokenResult struct {
	Headers   map[string]string `json:"headers"`
	ExpiresAt string            `json:"expiresAt,omitempty"` // RFC 3339; host refreshes before it
	// Credential, when set, replaces the stored one (rotated refresh token).
	Credential Credential `json:"credential,omitempty"`
}

// ------------------------------------------------------------------- models

// Pricing is per million tokens, in USD.
type Pricing struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

// Upstream names the built-in provider/model a gateway model fronts, so the
// host can reuse what it already knows about it (window, pricing,
// capabilities) when the plugin omits them.
type Upstream struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Model is one entry of models/list.
type Model struct {
	ID              string    `json:"id"`
	DisplayName     string    `json:"displayName,omitempty"`
	Upstream        *Upstream `json:"upstream,omitempty"`
	ContextWindow   int       `json:"contextWindow,omitempty"`
	MaxOutputTokens int       `json:"maxOutputTokens,omitempty"`
	Pricing         *Pricing  `json:"pricing,omitempty"`
	// Capabilities uses the catalog vocabulary: "vision", "tools",
	// "thinking", "prompt_cache", "json_mode".
	Capabilities []string `json:"capabilities,omitempty"`
	// Headers are added to dialect requests for this model only (e.g. a
	// gateway that routes by header).
	Headers map[string]string `json:"headers,omitempty"`
	// Dialect overrides the plugin-level dialect API for this model.
	Dialect DialectAPI `json:"dialect,omitempty"`
}

// ModelsListResult answers models/list.
type ModelsListResult struct {
	Models []Model `json:"models"`
}

// --------------------------------------------------------------------- chat

// Roles of a normalized message.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Part types of a normalized message.
const (
	PartText     = "text"
	PartImage    = "image"
	PartThinking = "thinking"
)

// Part is one content block.
type Part struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// image
	MediaType string `json:"mediaType,omitempty"`
	Data      string `json:"data,omitempty"` // base64
	URL       string `json:"url,omitempty"`

	// thinking: replay Signature/Redacted verbatim on the next request.
	Signature string `json:"signature,omitempty"`
	Redacted  string `json:"redacted,omitempty"`

	// CacheBreakpoint marks the end of a cacheable prefix. Plugins whose
	// backend supports prompt caching should place a cache marker here.
	CacheBreakpoint bool `json:"cacheBreakpoint,omitempty"`
}

// ToolCall is a function invocation requested by the model.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON object, as text
}

// Message is the normalized chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    []Part     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`  // assistant
	ToolCallID string     `json:"toolCallId,omitempty"` // tool
}

// Tool declares a function the model may call.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema object
}

// Hints are optional; a plugin applies the ones its backend supports and
// ignores the rest.
type Hints struct {
	CacheTTL        string   `json:"cacheTTL,omitempty"` // "5m" | "1h"
	PromptCacheKey  string   `json:"promptCacheKey,omitempty"`
	ThinkingBudget  int      `json:"thinkingBudget,omitempty"`
	ReasoningEffort string   `json:"reasoningEffort,omitempty"` // low | medium | high
	Temperature     *float64 `json:"temperature,omitempty"`
}

// ChatParams is chat/send's request.
type ChatParams struct {
	Credential Credential `json:"credential,omitempty"`
	Model      string     `json:"model"`
	Messages   []Message  `json:"messages"`
	Tools      []Tool     `json:"tools,omitempty"`
	MaxTokens  int        `json:"maxTokens,omitempty"`
	Hints      Hints      `json:"hints,omitempty"`
	// Stream asks for chat/delta notifications before the final result.
	Stream bool `json:"stream,omitempty"`
}

// Stop reasons.
const (
	StopEnd       = "end"
	StopMaxTokens = "max_tokens"
	StopToolUse   = "tool_use"
	StopRefusal   = "refusal"
)

// Usage reports token accounting for one call. Input excludes cached
// tokens; the host totals them.
type Usage struct {
	InputTokens      int      `json:"inputTokens"`
	OutputTokens     int      `json:"outputTokens"`
	CacheReadTokens  int      `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int      `json:"cacheWriteTokens,omitempty"`
	ReasoningTokens  int      `json:"reasoningTokens,omitempty"`
	CostUSD          *float64 `json:"costUSD,omitempty"` // authoritative when set
}

// ChatResult is chat/send's final response.
type ChatResult struct {
	Message    Message `json:"message"`
	StopReason string  `json:"stopReason"`
	Usage      *Usage  `json:"usage,omitempty"`
}

// ChatDelta is an incremental chunk of a streaming chat/send. RequestID
// echoes the JSON-RPC id of the chat/send it belongs to.
type ChatDelta struct {
	RequestID any       `json:"requestId"`
	Text      string    `json:"text,omitempty"`
	Thinking  string    `json:"thinking,omitempty"`
	ToolCall  *ToolCall `json:"toolCall,omitempty"`
}

// TokensCountParams is tokens/count's request.
type TokensCountParams struct {
	Credential Credential `json:"credential,omitempty"`
	Model      string     `json:"model"`
	Messages   []Message  `json:"messages"`
	Tools      []Tool     `json:"tools,omitempty"`
}

// TokensCountResult is tokens/count's response.
type TokensCountResult struct {
	Tokens int `json:"tokens"`
}
