package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/diillson/chatcli/utils"
	"github.com/diillson/chatcli/version"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func (cli *ChatCLI) reconfigureLogger() {
	cli.logger.Info("Reconfigurando o logger...")

	if err := cli.logger.Sync(); err != nil {
		cli.logger.Error("Erro ao sincronizar o logger", zap.Error(err))
	}

	newLogger, err := utils.InitializeLogger()
	if err != nil {
		cli.logger.Error("Erro ao reinicializar o logger", zap.Error(err))
		return
	}

	cli.logger = newLogger
	cli.logger.Info("Logger reconfigurado com sucesso")
}

// reloadableEnvVars são as variáveis limpas do ambiente do processo antes do
// godotenv.Overload em reloadConfiguration — sem constar aqui, um valor
// removido do .env sobrevive ao /reload até o processo reiniciar. Toda env
// nova de provider precisa entrar nesta lista.
//
// AWS_PROFILE/AWS_REGION ficaram fora daqui por muito tempo com uma razão
// legítima: são ambientais do SDK, costumam vir do shell e não do .env, e
// unsetá-las derrubava a cadeia de credenciais no /reload. O que mudou é que
// limpar deixou de significar perder — reloadConfiguration chama
// config.RestoreBootEnv depois do Overload (devolve o que o shell ou o bloco
// env da IDE/cliente MCP entregou no boot e que o arquivo não redefine) e
// config.ReapplyProjectDotenv (devolve o que o .env do projeto contribuiu).
// Com as duas redes no lugar, remover a variável do arquivo passa a valer
// sem reiniciar, que é a razão de existir desta lista, sem que um valor de
// fora do arquivo seja perdido. O contrato de comportamento está travado em
// TestReload_KeepsClientProvidedVariables (pacote config) e no caso AWS de
// TestReloadableEnvVarsCoverCriticalProviderVars.
var reloadableEnvVars = []string{
	"LOG_LEVEL", "ENV", "LLM_PROVIDER", "LOG_FILE", "LOG_MAX_SIZE", "HISTORY_MAX_SIZE",
	"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_ASSISTANT_MODEL",
	"OPENAI_USE_RESPONSES", "OPENAI_MAX_TOKENS", "OPENAI_API_URL", "OPENAI_RESPONSES_API_URL",
	"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_MAX_TOKENS", "ANTHROPIC_API_VERSION", "ANTHROPIC_BASE_URL",
	"CHATCLI_PROMPT_CACHE_TTL", "CHATCLI_PROMPT_CACHE_EXPLICIT", "CHATCLI_MODEL_PRICING", "CHATCLI_DASH",
	"CHATCLI_COMPACT_MODEL", "CHATCLI_KNOWLEDGE_RERANK", "CHATCLI_KNOWLEDGE_NORMALIZE", "CHATCLI_ENCRYPTION_KEY_PREVIOUS", "CHATCLI_MEMORY_PROVIDER", "CHATCLI_CONTEXT_ENGINE",
	"GOOGLEAI_API_KEY", "GOOGLEAI_MODEL", "GOOGLEAI_MAX_TOKENS",
	"XAI_API_KEY", "XAI_MODEL", "XAI_MAX_TOKENS",
	"ZAI_API_KEY", "ZAI_MODEL", "ZAI_MAX_TOKENS", "ZAI_API_URL", "ZAI_USE_CODING_PLAN", "ZAI_THINKING",
	"MINIMAX_API_KEY", "MINIMAX_MODEL", "MINIMAX_MAX_TOKENS", "MINIMAX_API_COMPAT", "MINIMAX_API_URL",
	"MOONSHOT_API_KEY", "MOONSHOT_MODEL", "MOONSHOT_MAX_TOKENS", "MOONSHOT_THINKING", "MOONSHOT_API_URL",
	"OLLAMA_ENABLED", "OLLAMA_BASE_URL", "OLLAMA_MODEL", "OLLAMA_MAX_TOKENS",
	"CLIENT_ID", "CLIENT_KEY", "STACKSPOT_REALM", "STACKSPOT_AGENT_ID",
	"COPILOT_MODEL", "COPILOT_MAX_TOKENS", "GITHUB_COPILOT_TOKEN",
	"BEDROCK_PROVIDER", "BEDROCK_MODEL", "BEDROCK_MAX_TOKENS", "BEDROCK_REGION", "BEDROCK_PROFILE",
	"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
	"BEDROCK_BASE_URL", "BEDROCK_CONTROL_BASE_URL", "BEDROCK_MANTLE_BASE_URL",
	"BEDROCK_ANTHROPIC_ENDPOINT", "BEDROCK_TEMPERATURE", "BEDROCK_TOP_P",
	"OPENROUTER_API_KEY", "OPENROUTER_MODEL", "OPENROUTER_MAX_TOKENS", "OPENROUTER_API_URL",
	"OPENROUTER_FALLBACK_MODELS", "OPENROUTER_PROVIDER_ORDER", "OPENROUTER_TRANSFORMS",
	"OPENROUTER_TOOLS", "OPENROUTER_HTTP_REFERER", "OPENROUTER_APP_TITLE",
	"DEVIN_API_KEY", "DEVIN_MODEL", "DEVIN_CLI_PATH", "DEVIN_CLI_TIMEOUT", "DEVIN_CLI_EXTRA_ARGS",
	"DEVIN_CLI_PERMISSION_MODE", "DEVIN_CLI_SANDBOX", "DEVIN_CLI_AGENT_CONFIG", "DEVIN_CLI_USAGE_EXPORT",
	"DEVIN_CLI_RESPECT_WORKSPACE_TRUST",
	"CHATCLI_WEBFETCH_USER_AGENT", "CHATCLI_WEBFETCH_AUTOSAVE_BYTES",
	"CHATCLI_WEBFETCH_RENDER", "CHATCLI_WEBFETCH_RENDER_TIMEOUT", "CHATCLI_WEBFETCH_RENDER_AUTOPROVISION",
	"CHATCLI_WEBFETCH_RENDER_BROWSER",
	"CHATCLI_EMBED_PROVIDER", "CHATCLI_EMBED_MODEL", "CHATCLI_EMBED_DIMENSIONS",
	"CHATCLI_COMMANDS", "CHATCLI_COMMANDS_AUTOROUTE", "CHATCLI_CHAT_CODER_HANDOFF",
	"CHATCLI_SESSION_BUDGET_USD", "CHATCLI_DAILY_BUDGET_USD", "CHATCLI_BUDGET_WARNING_PCT", "CHATCLI_BUDGET_HARD_STOP",
	// Security, sessions, memory, hub, telemetry: a value removed from
	// .env must not survive /reload.
	"CHATCLI_ENCRYPTION_KEY", "CHATCLI_AUDIT_LOG_PATH", "CHATCLI_ENV_REDACT_MODE", "CHATCLI_MANAGED_CONFIG",
	"CHATCLI_ALLOW_UNSIGNED_PLUGINS", "CHATCLI_PLUGIN_QUARANTINE",
	"CHATCLI_KEYCHAIN_BACKEND",
	"CHATCLI_PROJECT_ENV",
	"CHATCLI_SESSION_TRANSCRIPT", "CHATCLI_SESSION_TTL", "CHATCLI_GATEWAY_MAX_TENANTS", "CHATCLI_HUB_TTL_HOURS",
	"CHATCLI_MEMORY_MODE", "CHATCLI_MEMORY_ENABLED", "CHATCLI_MEMORY_AUTORECALL", "CHATCLI_SESSION_AUTORECALL",
	"CHATCLI_MEMORY_FALLBACK_PROVIDERS", "CHATCLI_MEMORY_MAX_FACTS", "CHATCLI_MEMORY_MAX_SIZE",
	"CHATCLI_MEMORY_RETENTION_DAYS", "CHATCLI_MEMORY_RETRIEVAL_BUDGET",
	"CHATCLI_COMPRESSION_CCR_TTL", "CHATCLI_COMPRESSION_CCR_MAX_MB", "OLLAMA_HOST",
	"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_HEADERS",
	"OTEL_METRIC_EXPORT_INTERVAL", "OTEL_RESOURCE_ATTRIBUTES", "OTEL_SERVICE_NAME",
}

// reloadConfiguration recarrega as variáveis de ambiente e reconfigura o LLMManager
func (cli *ChatCLI) reloadConfiguration(ctx context.Context) {
	fmt.Println(i18n.T("status.reloading_config"))

	prevProvider := cli.Provider
	prevModel := cli.Model

	// Same discovery rule as boot (CHATCLI_DOTENV → ./.env → ~/.chatcli/.env
	// → ~/.env), re-run here so /reload picks up a file created since boot
	// and every later reader agrees on which file is in effect.
	res := config.ResolveDotenv()
	config.SetActiveDotenv(res)
	if res.ExpandErr != nil {
		fmt.Println(i18n.T("main.warn_expand_path", res.Path, res.ExpandErr))
	}
	envFilePath := res.Path
	for _, variable := range reloadableEnvVars {
		_ = os.Unsetenv(variable)
	}
	err := godotenv.Overload(envFilePath)
	if err != nil && !os.IsNotExist(err) {
		cli.logger.Error("Erro ao carregar o arquivo .env", zap.Error(err))
	}
	// Managed policy survives a reload: locked values are re-asserted and
	// defaults re-filled after the user's .env has been applied.
	if rep := config.ApplyManaged(); rep.Err != nil {
		cli.logger.Warn("managed config unreadable; ignored", zap.String("path", rep.Path), zap.Error(rep.Err))
	}
	// Anything the file does NOT define falls back to what the shell — or an
	// editor/MCP client's env block — provided at boot, instead of staying
	// cleared. The file still wins for every variable it declares, so
	// editing it remains the way to change a value; what this prevents is a
	// single /reload silently dropping the provider, model or AWS profile
	// pinned by a client that never writes an environment file.
	if restored := config.RestoreBootEnv(reloadableEnvVars); len(restored) > 0 {
		cli.logger.Info("reload: restored process-provided variables", zap.Strings("keys", restored))
	}
	// Same for the project overlay: it runs once per process, so without
	// this the project's contribution would be gone after the first reload.
	if reapplied := config.ReapplyProjectDotenv(); len(reapplied) > 0 {
		cli.logger.Info("reload: re-applied project .env variables", zap.Strings("keys", reapplied))
	}

	// Slash-command catalog: force a re-scan so /reload picks up command
	// files created or edited since boot (the stat fingerprint would catch
	// them within a turn anyway; this makes it immediate and explicit).
	if cli.slashCommands != nil {
		cli.slashCommands.Invalidate()
	}

	// Re-apply the UI theme so a CHATCLI_THEME change in .env takes effect on
	// reload, mirroring how the provider/model are re-resolved below.
	theme.InitFromEnv()

	config.Global.Reload(cli.logger)

	// Budget envs are read once by NewCostTracker; re-read them here so a
	// .env change to CHATCLI_SESSION_BUDGET_USD / _WARNING_PCT / _HARD_STOP
	// takes effect on /reload without restarting the process.
	if cli.costTracker != nil {
		cli.costTracker.ReloadBudget()
	}

	cli.reconfigureLogger()

	// Rebuild the embedding provider so a CHATCLI_EMBED_PROVIDER change in
	// .env takes effect on reload — without this the session keeps the
	// provider captured at boot until the process restarts.
	if oldEmb, newEmb := cli.refreshEmbeddingProvider(); oldEmb != newEmb {
		fmt.Println(i18n.T("status.reload_embed_provider", newEmb))
	}

	manager, err := manager.NewLLMManager(cli.logger)
	if err != nil {
		cli.logger.Error("Erro ao reconfigurar o LLMManager", zap.Error(err))
		return
	}

	cli.manager = manager
	// Servers that cached the manager (MCP/ACP backend) must follow the
	// rebuild, or a reload from the client changes nothing for them.
	notifyManagerRebuilt(manager)

	if prevProvider != "" && prevModel != "" {
		if client, err := cli.manager.GetClient(prevProvider, prevModel); err == nil {
			cli.Client = client
			cli.Provider = prevProvider
			cli.Model = prevModel
			cli.refreshModelCache(ctx)
			cli.pulseRouteChanged("/reload")
			fmt.Println(i18n.T("status.reload_success_preserved"))
			return
		}
		cli.logger.Warn("Falha ao preservar provider/model após reload; caindo para valores do .env",
			zap.String("provider", prevProvider), zap.String("model", prevModel))
	}
	cli.configureProviderAndModel()
	if client, err := cli.manager.GetClient(cli.Provider, cli.Model); err == nil {
		cli.Client = client
		cli.pulseRouteChanged("/reload")
		fmt.Println(i18n.T("status.reload_success"))
	} else {
		cli.logger.Error("Erro ao obter o cliente LLM", zap.Error(err))
		fmt.Println(i18n.T("status.reload_fail_client"))
	}
}

// bootModelSource declara de onde vem o modelo de cada provider no boot:
// env primária, env de fallback opcional e o default do config. Cada
// provider suportado pelo manager PRECISA de uma entrada aqui — um provider
// ausente deixava cli.Model vazio, o catálogo resolvia (provider, "") para
// os fallbacks conservadores e a sessão inteira rodava com max-tokens e
// janela de contexto degradados, mesmo com o client interno falando com o
// modelo certo (foi o caso do BEDROCK: 128K virando default).
// STACKSPOT fica de fora por desenho: o "modelo" é o agent (realm/agent-id),
// e o fallback de provider do catálogo já cobre a sizing.
type bootModelSource struct {
	envVar      string
	fallbackEnv string
	defaultName string
}

var bootModelSources = map[string]bootModelSource{
	"OPENAI":           {envVar: "OPENAI_MODEL", defaultName: config.DefaultOpenAIModel},
	"OPENAI_ASSISTANT": {envVar: "OPENAI_ASSISTANT_MODEL", fallbackEnv: "OPENAI_MODEL", defaultName: config.DefaultOpenAiAssistModel},
	"CLAUDEAI":         {envVar: "ANTHROPIC_MODEL", defaultName: config.DefaultClaudeAIModel},
	"GOOGLEAI":         {envVar: "GOOGLEAI_MODEL", defaultName: config.DefaultGoogleAIModel},
	"XAI":              {envVar: "XAI_MODEL", defaultName: config.DefaultXAIModel},
	"ZAI":              {envVar: "ZAI_MODEL", defaultName: config.DefaultZAIModel},
	"MINIMAX":          {envVar: "MINIMAX_MODEL", defaultName: config.DefaultMiniMaxModel},
	"MOONSHOT":         {envVar: "MOONSHOT_MODEL", defaultName: config.DefaultMoonshotModel},
	"OLLAMA":           {envVar: "OLLAMA_MODEL", defaultName: config.DefaultOllamaModel},
	"COPILOT":          {envVar: "COPILOT_MODEL", defaultName: config.DefaultCopilotModel},
	"BEDROCK":          {envVar: "BEDROCK_MODEL", defaultName: config.DefaultBedrockModel},
	"OPENROUTER":       {envVar: "OPENROUTER_MODEL", defaultName: config.DefaultOpenRouterModel},
	"DEVIN":            {envVar: "DEVIN_MODEL", defaultName: config.DefaultDevinModel},
}

// resolveBootModelEnv resolve uma env de modelo no boot com a mesma
// precedência que as factories do manager usam: ambiente do processo
// primeiro, depois o config.Global (.env/config persistido).
func resolveBootModelEnv(name string) string {
	if name == "" {
		return ""
	}
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	if config.Global != nil {
		if v := strings.TrimSpace(config.Global.GetString(name)); v != "" {
			return v
		}
	}
	return ""
}

// DefaultModelForProvider returns the model a provider runs when nothing
// else names one: its model variable (process environment first, then the
// persisted config), its fallback variable, then the built-in default. It
// is empty for a provider without a model of its own (STACKSPOT) and for an
// unknown provider. The provider name is case-insensitive.
func DefaultModelForProvider(provider string) string {
	src, ok := bootModelSources[strings.ToUpper(strings.TrimSpace(provider))]
	if !ok {
		return ""
	}
	if m := resolveBootModelEnv(src.envVar); m != "" {
		return m
	}
	if m := resolveBootModelEnv(src.fallbackEnv); m != "" {
		return m
	}
	return src.defaultName
}

func (cli *ChatCLI) configureProviderAndModel() {
	// Normalização de case: LLM_PROVIDER=bedrock (minúsculo) furava todas
	// as comparações exatas e deixava provider E modelo desalinhados.
	cli.Provider = strings.ToUpper(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	if cli.Provider == "" {
		cli.Provider = config.DefaultLLMProvider
	}
	if m := DefaultModelForProvider(cli.Provider); m != "" {
		cli.Model = m
	}
}

func (cli *ChatCLI) setExecutionProfile(p ExecutionProfile) {
	cli.executionProfile = p
}

func (cli *ChatCLI) ApplyOverrides(ctx context.Context, mgr manager.LLMManager, provider, model string) error {
	if provider == "" && model == "" {
		return nil
	}
	prov := cli.Provider
	mod := cli.Model
	if provider != "" {
		prov = strings.ToUpper(provider)
	}
	if model != "" {
		mod = model
	}
	if prov == cli.Provider && mod == cli.Model {
		return nil
	}
	newClient, err := mgr.GetClient(prov, mod)
	if err != nil {
		return err
	}
	cli.Client = newClient
	cli.Provider = prov
	cli.Model = mod
	cli.refreshModelCache(ctx)
	cli.pulseRouteChanged("rpc override")
	return nil
}

// presence retorna "[SET]" ou "[NOT SET]" para uma env sensível
func presence(v string) string {
	if strings.TrimSpace(v) == "" {
		return "[NOT SET]"
	}
	return "[SET]"
}

// firstNonEmptyEnvVal returns the value of the first set, non-blank env var
// among names. Used to surface a setting that may be spelled upper- or
// lower-case (e.g. HTTPS_PROXY / https_proxy) under one config line.
func firstNonEmptyEnvVal(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// getEnvFilePath retorna o caminho do arquivo .env em efeito (expandido),
// pela mesma descoberta do boot — não apenas CHATCLI_DOTENV/"./.env".
func (cli *ChatCLI) getEnvFilePath() string {
	res := config.ActiveDotenv()
	if res.ExpandErr != nil {
		cli.logger.Warn("Não foi possível expandir o caminho do .env", zap.Error(res.ExpandErr))
	}
	return res.Path
}

// dotenvOriginLabel descreve, em uma palavra, de onde veio o arquivo de
// ambiente em efeito — a informação que separa "o .env não foi lido" de
// "o .env foi lido e a variável não está lá".
func (cli *ChatCLI) dotenvOriginLabel() string {
	res := config.ActiveDotenv()
	switch {
	case !res.Exists && res.Origin == config.DotenvNotFound:
		return i18n.T("cfg.dotenv.origin_none")
	case !res.Exists:
		return i18n.T("cfg.dotenv.origin_missing", string(res.Origin))
	default:
		return string(res.Origin)
	}
}

func (ch *CommandHandler) handleVersionCommand(ctx context.Context) {
	// Checagem com timeout
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fmt.Println(FormatVersionReport(version.GetReport(ctx)))
}
