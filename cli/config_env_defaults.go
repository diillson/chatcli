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
	"CHATCLI_CHAT_KNOWLEDGE":        {Value: "true", IsBool: true, Source: "chat_knowledge.go"},
	"CHATCLI_DEBUG":                 {Value: "false", IsBool: true, Source: "logger.SetupLogger"},
	"CHATCLI_AUTO_UPDATE":           {Value: "notify", Source: "update/apply.go"},
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
	"OPENAI_API_URL":             {Value: config.OpenAIAPIURL, Source: "config.OpenAIAPIURL"},
	"OPENAI_RESPONSES_API_URL":   {Value: config.OpenAIResponsesAPIURL, Source: "config.OpenAIResponsesAPIURL (OAuth: config.OpenAIOAuthResponsesURL)"},
	"ANTHROPIC_MODEL":            {Value: config.DefaultClaudeAIModel, Source: "config.DefaultClaudeAIModel"},
	"ANTHROPIC_BASE_URL":         {Value: config.ClaudeAIAPIURL, Source: "config.ClaudeAIAPIURL"},
	"ANTHROPIC_API_VERSION":      {Value: config.ClaudeAIAPIVersionDefault, Source: "config.ClaudeAIAPIVersionDefault"},
	"GOOGLEAI_MODEL":             {Value: config.DefaultGoogleAIModel, Source: "config.DefaultGoogleAIModel"},
	"XAI_MODEL":                  {Value: config.DefaultXAIModel, Source: "config.DefaultXAIModel"},
	"OLLAMA_MODEL":               {Value: config.DefaultOllamaModel, Source: "config.DefaultOllamaModel"},
	"CHATCLI_MCP_TOOLS":          {Value: "all", Source: "hardcoded (mcp-server/acp exposure policy: all|safe|csv allowlist; covers plugins and proxied mcp_* tools)"},
	"DEVIN_MODEL":                {Value: config.DefaultDevinModel, Source: "config.DefaultDevinModel"},
	"DEVIN_CLI_PATH":             {Value: config.DevinCLIDefaultBinary + " (PATH lookup)", Source: "config.DevinCLIDefaultBinary"},
	"DEVIN_CLI_PERMISSION_MODE":  {Value: config.DevinCLIDefaultPermissionMode, Source: "config.DevinCLIDefaultPermissionMode"},
	"DEVIN_CLI_TIMEOUT":          {Value: config.DevinCLIDefaultTimeout.String(), Source: "config.DevinCLIDefaultTimeout"},
	"DEVIN_CLI_SANDBOX":          {Value: "false", Source: "hardcoded"},
	"DEVIN_CLI_USAGE_EXPORT":     {Value: "true", IsBool: true, Source: "hardcoded (per-turn ATIF --export read back for real token usage; false = chars/4 estimate)"},
	"OLLAMA_BASE_URL":            {Value: config.OllamaDefaultBaseURL, Source: "config.OllamaDefaultBaseURL"},
	"BEDROCK_MODEL":              {Value: config.DefaultBedrockModel, Source: "config.DefaultBedrockModel"},
	"BEDROCK_REGION":             {Value: config.DefaultBedrockRegion, Source: "config.DefaultBedrockRegion"},
	"COPILOT_MODEL":              {Value: config.DefaultCopilotModel, Source: "config.DefaultCopilotModel"},
	"COPILOT_API_BASE_URL":       {Value: config.CopilotAPIURL, Source: "config.CopilotAPIURL"},
	"GITHUB_MODELS_MODEL":        {Value: config.DefaultGitHubModelsModel, Source: "config.DefaultGitHubModelsModel"},
	"ZAI_MODEL":                  {Value: config.DefaultZAIModel, Source: "config.DefaultZAIModel"},
	"ZAI_API_URL":                {Value: config.ZAIAPIURL, Source: "config.ZAIAPIURL"},
	"ZAI_USE_CODING_PLAN":        {Value: "false", Source: "hardcoded (true → config.ZAICodingAPIURL, GLM Coding Plan)"},
	"ZAI_THINKING":               {Value: "(backend default)", Source: "zai client (enabled|disabled)"},
	"MINIMAX_MODEL":              {Value: config.DefaultMiniMaxModel, Source: "config.DefaultMiniMaxModel"},
	"MOONSHOT_MODEL":             {Value: config.DefaultMoonshotModel, Source: "config.DefaultMoonshotModel"},
	"MOONSHOT_API_URL":           {Value: config.MoonshotAPIURL, Source: "config.MoonshotAPIURL"},
	"MOONSHOT_THINKING":          {Value: "auto", Source: "moonshot client default"},
	"OPENROUTER_MODEL":           {Value: config.DefaultOpenRouterModel, Source: "config.DefaultOpenRouterModel"},
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
	"CHATCLI_AGENT_RUNS_HISTORY":         {Value: "200", Source: "runs.DefaultHistorySize"},
	"CHATCLI_HUB_RUNS":                   {Value: "true", IsBool: true, Source: "cli.hubRunsEnabled (runs mirror via hub)"},
	"CHATCLI_AGENT_PARALLEL_TOOLS":       {Value: "false", IsBool: true, Source: "agent.ParallelToolsEnabled (Fase 3)"},
	"CHATCLI_AGENT_MAX_TOOL_CONCURRENCY": {Value: "10", Source: "agent.defaultMaxToolConcurrency (Fase 3)"},
	"CHATCLI_AGENT_INLINE_CODE_STRICT":   {Value: "false", IsBool: true, Source: "agent.InlineCodeRiskAnalyzer (Fase 1.3)"},

	// ─── Agent: token efficiency ─────────────────────────────────
	"CHATCLI_AGENT_EARLY_EXIT":       {Value: "true", IsBool: true, Source: "agent_earlyexit.earlyExitEnabled"},
	"CHATCLI_AGENT_SKILL_RESCAN":     {Value: "true", IsBool: true, Source: "skill_rescan.skillRescanEnabled"},
	"CHATCLI_AGENT_EARLY_EXIT_TURNS": {Value: "3", Source: "agent_earlyexit.defaultStagnationThreshold"},
	"CHATCLI_AGENT_SMART_ROUTE":      {Value: "hint", Source: "agent_routing.smartRouting"},
	"CHATCLI_AGENT_MODEL_TOOL":       {Value: "true", IsBool: true, Source: "model_tool_adapter.isModelToolEnabled"},
	"CHATCLI_SKILL_INJECT_BUDGET":    {Value: "24000", Source: "skill_activation.skillInjectBudget — 0 = unlimited; run cap = 2x this"},
	"CHATCLI_SKILL_AGE_TURNS":        {Value: "6", Source: "agent.DefaultSkillAgingConfig — turns before a mid-loop skill block collapses to a stub (also the re-inject cooldown)"},

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
	"CHATCLI_CONTEXT_WINDOW":               {Value: "(auto from catalog)", Source: "catalog.GetContextWindow"},
	"CHATCLI_PROMPT_CACHE_TTL":             {Value: "5m", Source: "llm/client/cache_prefix.go (5m|1h, Anthropic direct)"},
	"CHATCLI_MICROCOMPACT_TRUNCATE_TURNS":  {Value: "2", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_MICROCOMPACT_SUMMARIZE_TURNS": {Value: "4", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_MICROCOMPACT_HEAD_CHARS":      {Value: "2000", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_MICROCOMPACT_TAIL_CHARS":      {Value: "500", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_MICROCOMPACT_MIN_CONTENT":     {Value: "3000", Source: "agent.DefaultMicrocompactConfig"},
	"CHATCLI_TOOL_RESULT_BUDGET_CHARS":     {Value: "200000", Source: "agent.DefaultTurnBudgetChars"},
	"CHATCLI_TOOL_RESULT_MAX_CHARS":        {Value: "20000", Source: "agent.DefaultPerResultMaxChars"},

	// ─── Resilience: global TLS trust ────────────────────────────
	"CHATCLI_CA_BUNDLE":                {Value: "(system trust store)", Source: "utils.ApplyGlobalTLSTrust"},
	"CHATCLI_TLS_INSECURE_SKIP_VERIFY": {Value: "false", IsBool: true, Source: "utils.ApplyGlobalTLSTrust"},

	// ─── Resilience: bedrock proxy ───────────────────────────────
	"CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY": {Value: "false", IsBool: true, Source: "bedrock client"},
	"CHATCLI_BEDROCK_ENABLE_IMDS":          {Value: "false", IsBool: true, Source: "bedrock client"},

	// ─── Embeddings (HyDE / RAG) ─────────────────────────────────
	// Defaults are described in llm/embedding/factory.go. CHATCLI_EMBED_PROVIDER
	// is purely opt-in (empty == null provider, no embeddings); we register
	// the model/dimensions defaults so /config can show what each provider
	// would use if selected.
	"CHATCLI_EMBED_PROVIDER":   {Value: "(off)", Source: "llm/embedding/factory.go (empty == null)"},
	"CHATCLI_EMBED_MODEL":      {Value: "(provider-specific)", Source: "voyage-4 / text-embedding-3-small / gemini-embedding-2 / amazon.titan-embed-text-v2:0"},
	"CHATCLI_EMBED_DIMENSIONS": {Value: "(provider-default)", Source: "openai 1536, google 3072 (128-3072), titan-v2 1024 (256/512/1024), titan-v1 1536, cohere-v3 1024, cohere-v4 1536, nova-mme 3072 (256/384/1024/3072)"},

	// ─── Cost / budget ───────────────────────────────────────────
	"CHATCLI_SESSION_BUDGET_USD": {Value: "(no budget)", Source: "cost_tracker.go"},
	"CHATCLI_BUDGET_WARNING_PCT": {Value: "0.80", Source: "cost_tracker.go"},
	"CHATCLI_BUDGET_HARD_STOP":   {Value: "false", IsBool: true, Source: "cost_tracker.go (refuse turns once budget exceeded)"},
	"CHATCLI_SESSION_TTL":        {Value: "90", Source: "session_manager.go (days)"},
	"CHATCLI_DISABLE_HISTORY":    {Value: "false", IsBool: true, Source: "history_manager.go"},

	// ─── Memory / bootstrap ──────────────────────────────────────
	"CHATCLI_MEMORY_ENABLED":    {Value: "true", IsBool: true, Source: "memory.go"},
	"CHATCLI_MEMORY_MODE":       {Value: "index", Source: "memory_mode.go"},
	"CHATCLI_MEMORY_GRAPH":      {Value: "true", IsBool: true, Source: "knowledge_graph.memoryGraphEnabled — persisted graph cache (graph.json) + recall neighborhood expansion; off = legacy derive-per-call"},
	"CHATCLI_BOOTSTRAP_ENABLED": {Value: "true", IsBool: true, Source: "bootstrap.go"},

	// ─── Gateway: voice transcription ────────────────────────────
	"CHATCLI_TRANSCRIPTION_PROVIDER":  {Value: "auto", Source: "transcription.NewFromEnv: auto|embedded|command|url|groq|openai"},
	"CHATCLI_TRANSCRIPTION_MODEL":     {Value: "whisper-1", Source: "transcription.defaultModel; embedded default base"},
	"CHATCLI_TRANSCRIPTION_LANG":      {Value: "(auto)", Source: "transcription.NewFromEnv"},
	"CHATCLI_TRANSCRIPTION_CACHE_DIR": {Value: "(os cache dir)", Source: "transcription embedded engine/model cache"},
	"CHATCLI_GATEWAY_MAX_AUDIO_BYTES": {Value: "20971520", Source: "gateway.defaultMaxAudioBytes"},
	"CHATCLI_GATEWAY_MAX_IMAGE_BYTES": {Value: "20971520", Source: "gateway.defaultMaxImageBytes"},

	// ─── Text-to-speech (@speak + gateway voice replies) ─────────
	"CHATCLI_TTS_PROVIDER":                {Value: "auto", Source: "tts.NewFromEnv: auto|embedded|command|url|openai|groq|google"},
	"CHATCLI_TTS_MODEL":                   {Value: "tts-1", Source: "tts.defaultTTSModel"},
	"CHATCLI_TTS_VOICE":                   {Value: "alloy", Source: "tts.defaultVoice; embedded default bm_george"},
	"CHATCLI_TTS_VOICE_PT":                {Value: "pm_alex", Source: "tts embedded voice for Portuguese replies"},
	"CHATCLI_TTS_CACHE_DIR":               {Value: "(os cache dir)", Source: "tts embedded engine/model cache"},
	"CHATCLI_GATEWAY_VOICE_REPLY":         {Value: "auto", Source: "gateway voice reply: auto|always|never"},
	"CHATCLI_GATEWAY_IMAGE_REPLY":         {Value: "auto", Source: "gateway image reply: auto|never"},
	"CHATCLI_GATEWAY_STRUCTURED_PROGRESS": {Value: "true", IsBool: true, Source: "gateway_command.go:gatewayStructuredProgressEnabled"},

	// ─── Image generation/editing (@image) ───────────────────────
	"CHATCLI_IMAGE_PROVIDER":      {Value: "auto", Source: "imagegen.NewFromEnv"},
	"CHATCLI_IMAGE_API":           {Value: "images", Source: "imagegen (images|responses)"},
	"CHATCLI_IMAGE_URL":           {Value: "(none)", Source: "imagegen self-hosted endpoint"},
	"CHATCLI_IMAGE_MODEL":         {Value: "gpt-image-2", Source: "imagegen.defaultImageModel"},
	"CHATCLI_IMAGE_EDIT_PROVIDER": {Value: "(inherit)", Source: "imagegen edit fallback when active provider can't edit"},
	"CHATCLI_IMAGE_EDIT_MODEL":    {Value: "(default)", Source: "imagegen edit fallback model"},

	// ─── Diagram rendering (@diagram) ────────────────────────────
	"CHATCLI_DIAGRAM_BACKEND": {Value: "auto", Source: "plugins.configuredDiagramBackend: auto|system|embedded"},

	// ─── Interactive graph view (@graphview) ─────────────────────
	"CHATCLI_GRAPHVIEW_OPEN":  {Value: "true", IsBool: true, Source: "plugins.graphViewOpenEnvEnabled: open the rendered HTML in the browser"},
	"CHATCLI_GRAPHVIEW_THEME": {Value: "dark", Source: "plugins.graphViewDefaultTheme: dark|light"},
	"CHATCLI_CHAT_GRAPHVIEW":  {Value: "true", IsBool: true, Source: "chat_graphview.go: chat-mode @graphview exception"},
	"CHATCLI_CHAT_MEMORY":     {Value: "true", IsBool: true, Source: "chat_memory.go: chat-mode @memory exception (profile/facts persistence; also enables index memory mode in chat)"},
	"CHATCLI_ATTACH_AUTO_RAG": {Value: "true", IsBool: true, Source: "context_autorag.go: /context attach auto-upgrades large contexts (>=32KiB) to semantic retrieval; --full opts out per call"},

	// ─── Vision input (image understanding / describe-fallback) ───
	"CHATCLI_VISION_INPUT":    {Value: "auto", Source: "vision input mode: auto|native|describe|off"},
	"CHATCLI_VISION_PROVIDER": {Value: "(auto)", Source: "vision describe-fallback provider override"},
	"CHATCLI_VISION_MODEL":    {Value: "(auto)", Source: "vision describe-fallback model override"},

	// ─── Gateway: @send home channels (proactive outbound) ───────
	"CHATCLI_TELEGRAM_HOME_CHANNEL": {Value: "(none)", Source: "send_adapter.go (@send default target)"},
	"CHATCLI_WHATSAPP_HOME_CHANNEL": {Value: "(none)", Source: "send_adapter.go (@send default target)"},
	"CHATCLI_DISCORD_HOME_CHANNEL":  {Value: "(none)", Source: "send_adapter.go (@send default target)"},
	"CHATCLI_SLACK_HOME_CHANNEL":    {Value: "(none)", Source: "send_adapter.go (@send default target)"},
	"CHATCLI_WEBHOOK_HOME_CHANNEL":  {Value: "(none)", Source: "send_adapter.go (@send default target)"},

	// ─── Integrations ────────────────────────────────────────────
	"CHATCLI_MCP_ENABLED":                   {Value: "false", IsBool: true, Source: "mcp manager"},
	"CHATCLI_MCP_DYNAMIC_TOOLS":             {Value: "true", IsBool: true, Source: "mcp.dynamicToolRefreshEnabled — refresh on tools/list_changed"},
	"CHATCLI_MCP_DANGER":                    {Value: "allow", Source: "cmd/rpcserve.go (mcp-server unattended danger policy: allow|block)"},
	"CHATCLI_MCP_ELICITATION":               {Value: "on", Source: "cli/rpcserve/mcp.go (permission dialogs via elicitation/create: on|off; off restores the legacy unattended contract)"},
	"CHATCLI_MCP_PERMISSION_TIMEOUT":        {Value: "600s", Source: "cli/rpcserve (max wait for the client's permission dialog, MCP elicitation and ACP request_permission; duration or seconds; 0|off lifts the bound to a 24h ceiling — never truly unbounded; tune below MCP clients' own tools/call timeout)"},
	"CHATCLI_MCP_OAUTH_PORT":                {Value: "8765", Source: "cli/mcp/oauth.go (fixed loopback port for the OAuth callback; keeps redirect_uri and client registration stable; falls back to an ephemeral port when busy)"},
	"CHATCLI_MCP_RESOURCES":                 {Value: "on", Source: "cli/rpc_support_resources.go (chatcli:// read-only resources: on|off)"},
	"CHATCLI_MCP_MAX_HISTORY":               {Value: "0", Source: "cmd/rpcserve.go (0 = token-aware compaction bounds the history)"},
	"CHATCLI_MCP_SESSION_AUTOSAVE":          {Value: "true", IsBool: true, Source: "cmd/rpcserve.go (persist live MCP sessions as mcp-<session>; unset follows CHATCLI_SESSION_AUTOSAVE)"},
	"CHATCLI_SESSION_AUTOSAVE_KEEP":         {Value: "600", Source: "cli/cli_session_autosave.go (machine-session keep-count backstop; TTL is the primary retention)"},
	"CHATCLI_SESSION_AUTORECALL":            {Value: "true", IsBool: true, Source: "cli/session_autorecall.go (proactive saved-session recall block in chat/agent/coder turns)"},
	"CHATCLI_COMMANDS":                      {Value: "true", IsBool: true, Source: "cli/commands_integration.go (slash-command templates: .chatcli/commands + ~/.chatcli/commands + .claude/commands + .devin/workflows)"},
	"CHATCLI_COMMANDS_AUTOROUTE":            {Value: "true", IsBool: true, Source: "cli/commands_integration.go (auto-route mode:coder slash commands from chat into a coder one-shot run)"},
	"CHATCLI_SESSION_WRITETHROUGH":          {Value: "true", IsBool: true, Source: "cli/cli_session_binding.go (write each turn through to the active named session + adopt other surfaces' writes)"},
	"CHATCLI_MCP_HUB":                       {Value: "on", Source: "cmd/rpcserve.go (join the conversation hub in resume mode: on|off)"},
	"CHATCLI_MCP_HUB_PRINCIPAL":             {Value: "(hub default)", Source: "cmd/rpcserve.go (isolate the MCP thread under another principal)"},
	"CHATCLI_ALLOW_UNSIGNED_PLUGINS":        {Value: "false", IsBool: true, Source: "plugin manager"},
	"CHATCLI_REGISTRY_DISABLE":              {Value: "false", IsBool: true, Source: "skill registry"},
	"CHATCLI_WEBSEARCH_PROVIDER":            {Value: "auto", Source: "websearch_command.go"},
	"CHATCLI_WEBFETCH_RENDER":               {Value: "auto", Source: "plugins/webfetch_render.go (auto|always|never)"},
	"CHATCLI_WEBFETCH_RENDER_TIMEOUT":       {Value: "25", Source: "plugins/webfetch_render.go (seconds)"},
	"CHATCLI_WEBFETCH_RENDER_BROWSER":       {Value: "(auto-detected)", Source: "plugins/webfetch_render.go (Chrome/Chromium/Edge/Brave lookup)"},
	"CHATCLI_WEBFETCH_RENDER_AUTOPROVISION": {Value: "false", IsBool: true, Source: "plugins/webfetch_render.go (opt-in Chromium download)"},

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
