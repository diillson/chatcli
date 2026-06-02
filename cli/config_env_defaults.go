/*
 * ChatCLI - Environment Variable Default Registry
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Single source of truth for the default value of every CHATCLI_* /
 * provider env var that /config surfaces. The registry powers two things:
 *
 *  1. /config rendering — when an env is unset, we no longer print a
 *     bare "[NOT SET]". We print the actual fallback value the runtime
 *     uses so the operator can see what the system is actually doing
 *     without spelunking through the source.
 *
 *  2. Self-documentation — every entry carries a `Source` pointing to
 *     the file:line where the default lives in code. If the runtime
 *     ever drifts from this registry the audit trail makes it cheap to
 *     reconcile.
 *
 * Adding a default
 * ----------------
 * Find where the env var is read with os.Getenv (or a wrapper) in code,
 * look up the literal that's used when the var is empty, and add an
 * entry below using the same literal. Do NOT invent values: if the code
 * has no static fallback (e.g. the var is purely opt-in and the runtime
 * skips the feature when absent), leave it OUT of the registry. The
 * UI will fall back to the "[NOT SET]" placeholder for those, which is
 * the truthful answer.
 */

package cli

import (
	"strings"

	"github.com/diillson/chatcli/config"
)

// envDefault describes the runtime fallback for one env var.
//
//	Value    — what the runtime uses when the env is empty/unset, formatted
//	           the same way the user would set it (e.g. "10m" for durations,
//	           "0.80" for ratios, "100MB" for byte sizes). Booleans use
//	           "true"/"false" (lowercase) regardless of the var's accepted
//	           parsing form, so the display is uniform.
//	Source   — file:line or constant name where the default is defined,
//	           kept short and human-greppable.
//	IsBool   — true when the var is parsed as a boolean. Lets envBool()
//	           render the default with the same on/off labels it uses for
//	           explicitly-set values.
type envDefault struct {
	Value  string
	Source string
	IsBool bool
}

// envDefaults is the static registry. Keys are the exact env var names
// as users would `export` them. The map is intentionally an unsorted
// literal — lookup is O(1) and order does not matter.
var envDefaults = map[string]envDefault{
	// ─── General / process ───────────────────────────────────────
	"CHATCLI_CHAT_ASK":              {Value: "true", IsBool: true, Source: "chat_ask.go"},
	"CHATCLI_DEBUG":                 {Value: "false", IsBool: true, Source: "logger.SetupLogger"},
	"CHATCLI_DISABLE_VERSION_CHECK": {Value: "false", IsBool: true, Source: "version_checker.go"},
	"CHATCLI_LOG_COMPRESS":          {Value: "false", IsBool: true, Source: "logger.go"},
	"CHATCLI_LANG":                  {Value: "(system locale)", Source: "i18n.LoadLocale"},
	"LOG_LEVEL":                     {Value: "info", Source: "logger.SetupLogger"},
	"LOG_MAX_SIZE":                  {Value: "100", Source: "config.DefaultMaxLogSize"},
	"CHATCLI_LOG_FILE":              {Value: config.DefaultLogFile, Source: "config.DefaultLogFile"},
	"CHATCLI_LOG_MAX_SIZE_MB":       {Value: "100", Source: "config.DefaultMaxLogSize"},
	"HISTORY_MAX_SIZE":              {Value: "100MB", Source: "config.DefaultMaxHistorySize"},

	// ─── Provider model defaults ─────────────────────────────────
	"OPENAI_MODEL":               {Value: config.DefaultOpenAIModel, Source: "config.DefaultOpenAIModel"},
	"OPENAI_ASSISTANT_MODEL":     {Value: config.DefaultOpenAiAssistModel, Source: "config.DefaultOpenAiAssistModel"},
	"OPENAI_USE_RESPONSES":       {Value: "(auto, per model)", Source: "catalog.GetPreferredAPI"},
	"ANTHROPIC_MODEL":            {Value: config.DefaultClaudeAIModel, Source: "config.DefaultClaudeAIModel"},
	"ANTHROPIC_BASE_URL":         {Value: config.ClaudeAIAPIURL, Source: "config.ClaudeAIAPIURL"},
	"ANTHROPIC_API_VERSION":      {Value: config.ClaudeAIAPIVersionDefault, Source: "config.ClaudeAIAPIVersionDefault"},
	"GOOGLEAI_MODEL":             {Value: config.DefaultGoogleAIModel, Source: "config.DefaultGoogleAIModel"},
	"XAI_MODEL":                  {Value: config.DefaultXAIModel, Source: "config.DefaultXAIModel"},
	"OLLAMA_MODEL":               {Value: config.DefaultOllamaModel, Source: "config.DefaultOllamaModel"},
	"OLLAMA_BASE_URL":            {Value: config.OllamaDefaultBaseURL, Source: "config.OllamaDefaultBaseURL"},
	"BEDROCK_REGION":             {Value: config.DefaultBedrockRegion, Source: "config.DefaultBedrockRegion"},
	"COPILOT_MODEL":              {Value: config.DefaultCopilotModel, Source: "config.DefaultCopilotModel"},
	"COPILOT_API_BASE_URL":       {Value: config.CopilotAPIURL, Source: "config.CopilotAPIURL"},
	"GITHUB_MODELS_MODEL":        {Value: config.DefaultGitHubModelsModel, Source: "config.DefaultGitHubModelsModel"},
	"ZAI_MODEL":                  {Value: config.DefaultZAIModel, Source: "config.DefaultZAIModel"},
	"MINIMAX_MODEL":              {Value: config.DefaultMiniMaxModel, Source: "config.DefaultMiniMaxModel"},
	"MOONSHOT_MODEL":             {Value: config.DefaultMoonshotModel, Source: "config.DefaultMoonshotModel"},
	"MOONSHOT_API_URL":           {Value: config.MoonshotAPIURL, Source: "config.MoonshotAPIURL"},
	"MOONSHOT_THINKING":          {Value: "auto", Source: "moonshot client default"},
	"OPENROUTER_FALLBACK_MODELS": {Value: "(none)", Source: "openrouter client"},
	"OPENROUTER_PROVIDER_ORDER":  {Value: "(none)", Source: "openrouter client"},

	// ─── UI / theme ──────────────────────────────────────────────
	"CHATCLI_THEME": {Value: config.DefaultTheme, Source: "config.DefaultTheme"},

	// ─── Agent: parallelism / orchestration ──────────────────────
	"CHATCLI_AGENT_PARALLEL_MODE":        {Value: "true", IsBool: true, Source: "agent_mode.go:initMultiAgent"},
	"CHATCLI_AGENT_MAX_WORKERS":          {Value: "4", Source: "workers.DefaultMaxWorkers"},
	"CHATCLI_AGENT_WORKER_TIMEOUT":       {Value: "10m", Source: "workers.DefaultWorkerTimeout"},
	"CHATCLI_AGENT_WORKER_MAX_TURNS":     {Value: "30", Source: "workers.DefaultWorkerMaxTurns"},
	"CHATCLI_AGENT_SUBAGENT_MAX_DEPTH":   {Value: "2", Source: "workers.DefaultSubagentMaxDepth"},
	"CHATCLI_AGENT_SUBAGENT_MAX_TURNS":   {Value: "15", Source: "workers.DefaultSubagentMaxTurns"},
	"CHATCLI_AGENT_PARALLEL_TOOLS":       {Value: "false", IsBool: true, Source: "agent.ParallelToolsEnabled (Fase 3)"},
	"CHATCLI_AGENT_MAX_TOOL_CONCURRENCY": {Value: "10", Source: "agent.defaultMaxToolConcurrency (Fase 3)"},
	"CHATCLI_AGENT_INLINE_CODE_STRICT":   {Value: "false", IsBool: true, Source: "agent.InlineCodeRiskAnalyzer (Fase 1.3)"},

	// ─── Agent: token efficiency ─────────────────────────────────
	"CHATCLI_AGENT_EARLY_EXIT":       {Value: "true", IsBool: true, Source: "agent_earlyexit.earlyExitEnabled"},
	"CHATCLI_AGENT_EARLY_EXIT_TURNS": {Value: "3", Source: "agent_earlyexit.defaultStagnationThreshold"},
	"CHATCLI_AGENT_SMART_ROUTE":      {Value: "hint", Source: "agent_routing.smartRouting"},

	// ─── Agent: execution ────────────────────────────────────────
	"CHATCLI_AGENT_CMD_TIMEOUT":         {Value: "10m", Source: "agent.NewContextManager"},
	"CHATCLI_AGENT_SOURCE_SHELL_CONFIG": {Value: "false", IsBool: true, Source: "command_executor.go"},
	"CHATCLI_AGENT_KEEP_TMPDIR":         {Value: "false", IsBool: true, Source: "session_workspace.go"},
	"CHATCLI_MAX_COMMAND_OUTPUT":        {Value: "102400", Source: "defaultMaxCommandOutput (100KB)"},

	// ─── Resilience: payload / recovery ──────────────────────────
	"CHATCLI_MAX_PAYLOAD":             {Value: "(no cap)", Source: "history_compactor.DefaultCompactConfig"},
	"CHATCLI_MAX_RECOVERY_ATTEMPTS":   {Value: "3", Source: "agent.DefaultContextRecoveryConfig"},
	"CHATCLI_MAX_TOKEN_ESCALATIONS":   {Value: "2", Source: "agent.DefaultContextRecoveryConfig"},
	"CHATCLI_EMERGENCY_KEEP_MESSAGES": {Value: "10", Source: "agent.DefaultContextRecoveryConfig"},

	// ─── Resilience: streaming ───────────────────────────────────
	"CHATCLI_STREAM_IDLE_TIMEOUT_SECONDS": {Value: "90", Source: "client.DefaultWatchdogConfig"},

	// ─── Resilience: compaction ──────────────────────────────────
	"CHATCLI_MICROCOMPACT_TRUNCATE_TURNS":  {Value: "2", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_MICROCOMPACT_SUMMARIZE_TURNS": {Value: "4", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_MICROCOMPACT_HEAD_CHARS":      {Value: "2000", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_MICROCOMPACT_TAIL_CHARS":      {Value: "500", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_MICROCOMPACT_MIN_CONTENT":     {Value: "3000", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_TOOL_RESULT_BUDGET_CHARS":     {Value: "200000", Source: "agent.DefaultTurnBudgetChars"},
	"CHATCLI_TOOL_RESULT_MAX_CHARS":        {Value: "20000", Source: "agent.DefaultPerResultMaxChars"},

	// ─── Resilience: bedrock proxy ───────────────────────────────
	"CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY": {Value: "false", IsBool: true, Source: "bedrock client"},
	"CHATCLI_BEDROCK_ENABLE_IMDS":          {Value: "false", IsBool: true, Source: "bedrock client"},

	// ─── Embeddings (HyDE / RAG) ─────────────────────────────────
	// Defaults are described in llm/embedding/factory.go. CHATCLI_EMBED_PROVIDER
	// is purely opt-in (empty == null provider, no embeddings); we register
	// the model/dimensions defaults so /config can show what each provider
	// would use if selected.
	"CHATCLI_EMBED_PROVIDER":   {Value: "(off)", Source: "llm/embedding/factory.go (empty == null)"},
	"CHATCLI_EMBED_MODEL":      {Value: "(provider-specific)", Source: "voyage-3 / text-embedding-3-small / amazon.titan-embed-text-v2:0"},
	"CHATCLI_EMBED_DIMENSIONS": {Value: "(provider-default)", Source: "openai 1536, titan-v2 1024 (256/512/1024), titan-v1 1536, cohere-v3 1024"},

	// ─── Cost / budget ───────────────────────────────────────────
	"CHATCLI_SESSION_BUDGET_USD": {Value: "(no budget)", Source: "cost_tracker.go"},
	"CHATCLI_BUDGET_WARNING_PCT": {Value: "0.80", Source: "cost_tracker.go"},
	"CHATCLI_SESSION_TTL":        {Value: "90", Source: "session_manager.go (days)"},
	"CHATCLI_DISABLE_HISTORY":    {Value: "false", IsBool: true, Source: "history_manager.go"},

	// ─── Memory / bootstrap ──────────────────────────────────────
	"CHATCLI_MEMORY_ENABLED":    {Value: "true", IsBool: true, Source: "memory.go"},
	"CHATCLI_MEMORY_MODE":       {Value: "index", Source: "memory_mode.go"},
	"CHATCLI_BOOTSTRAP_ENABLED": {Value: "true", IsBool: true, Source: "bootstrap.go"},

	// ─── Integrations ────────────────────────────────────────────
	"CHATCLI_MCP_ENABLED":            {Value: "false", IsBool: true, Source: "mcp manager"},
	"CHATCLI_ALLOW_UNSIGNED_PLUGINS": {Value: "false", IsBool: true, Source: "plugin manager"},
	"CHATCLI_REGISTRY_DISABLE":       {Value: "false", IsBool: true, Source: "skill registry"},
	"CHATCLI_WEBSEARCH_PROVIDER":     {Value: "auto", Source: "websearch_command.go"},

	// ─── Security ────────────────────────────────────────────────
	"CHATCLI_AGENT_SECURITY_MODE":    {Value: "strict", Source: "agent.command_allowlist (default unless permissive)"},
	"CHATCLI_AGENT_ALLOW_SUDO":       {Value: "false", IsBool: true, Source: "command_allowlist"},
	"CHATCLI_AGENT_WORKSPACE_STRICT": {Value: "true", IsBool: true, Source: "session_workspace"},
	"CHATCLI_AGENT_ALLOW_KUBECONFIG": {Value: "false", IsBool: true, Source: "session_workspace"},
	"CHATCLI_BLOCK_TMP_WRITES":       {Value: "false", IsBool: true, Source: "session_workspace.go:150"},
	"CHATCLI_ALLOW_HTTP_PROVIDERS":   {Value: "false", IsBool: true, Source: "TLS posture"},
	"CHATCLI_ALLOW_INSECURE":         {Value: "false", IsBool: true, Source: "TLS posture"},
	"CHATCLI_ENV_REDACT_MODE":        {Value: "normal", Source: "env_redactor.go (strict|normal)"},

	// ─── Server mode ─────────────────────────────────────────────
	"CHATCLI_GRPC_REFLECTION":   {Value: "false", IsBool: true, Source: "server.go"},
	"CHATCLI_FALLBACK_ENABLED":  {Value: "false", IsBool: true, Source: "server fallback"},
	"CHATCLI_OPERATOR_DEV_MODE": {Value: "false", IsBool: true, Source: "operator"},
}

// lookupEnvDefault returns the registered default for `name`, or
// (zero, false) when the var is not in the registry. It's the entry
// point used by envOr/envBool — the registry stays a closed map at
// package level to enforce that defaults come from a single audited
// source instead of being scattered as inline string literals across
// the rendering code.
func lookupEnvDefault(name string) (envDefault, bool) {
	d, ok := envDefaults[name]
	return d, ok
}

// formatDefaultValue normalizes the displayed form of a registered
// default. Long URLs and paths get shortened so they don't blow past
// the column width that the /config grid uses; everything else goes
// through verbatim. Kept tiny on purpose — the registry stores the
// canonical form, this helper only adapts it for terminal width.
func formatDefaultValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	const maxDisplay = 60
	if len(v) <= maxDisplay {
		return v
	}
	return v[:maxDisplay-3] + "..."
}
