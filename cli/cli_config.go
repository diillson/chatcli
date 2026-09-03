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
	"github.com/diillson/chatcli/ui/kit"
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
// nova de provider precisa entrar nesta lista. AWS_REGION/AWS_PROFILE ficam
// de fora de propósito: são ambientais do SDK e podem vir do shell, não do
// .env — unsetá-las derrubaria a cadeia de credenciais no reload.
var reloadableEnvVars = []string{
	"LOG_LEVEL", "ENV", "LLM_PROVIDER", "LOG_FILE", "LOG_MAX_SIZE", "HISTORY_MAX_SIZE",
	"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_ASSISTANT_MODEL",
	"OPENAI_USE_RESPONSES", "OPENAI_MAX_TOKENS", "OPENAI_API_URL", "OPENAI_RESPONSES_API_URL",
	"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_MAX_TOKENS", "ANTHROPIC_API_VERSION", "ANTHROPIC_BASE_URL",
	"CHATCLI_PROMPT_CACHE_TTL",
	"CHATCLI_COMPACT_MODEL",
	"GOOGLEAI_API_KEY", "GOOGLEAI_MODEL", "GOOGLEAI_MAX_TOKENS",
	"XAI_API_KEY", "XAI_MODEL", "XAI_MAX_TOKENS",
	"ZAI_API_KEY", "ZAI_MODEL", "ZAI_MAX_TOKENS", "ZAI_API_URL", "ZAI_USE_CODING_PLAN", "ZAI_THINKING",
	"MINIMAX_API_KEY", "MINIMAX_MODEL", "MINIMAX_MAX_TOKENS", "MINIMAX_API_COMPAT", "MINIMAX_API_URL",
	"MOONSHOT_API_KEY", "MOONSHOT_MODEL", "MOONSHOT_MAX_TOKENS", "MOONSHOT_THINKING", "MOONSHOT_API_URL",
	"OLLAMA_ENABLED", "OLLAMA_BASE_URL", "OLLAMA_MODEL", "OLLAMA_MAX_TOKENS",
	"CLIENT_ID", "CLIENT_KEY", "STACKSPOT_REALM", "STACKSPOT_AGENT_ID",
	"COPILOT_MODEL", "COPILOT_MAX_TOKENS", "GITHUB_COPILOT_TOKEN",
	"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_MODELS_TOKEN", "GITHUB_MODELS_MODEL", "GITHUB_MODELS_API_URL",
	"BEDROCK_PROVIDER", "BEDROCK_MODEL", "BEDROCK_MAX_TOKENS", "BEDROCK_REGION", "BEDROCK_PROFILE",
	"BEDROCK_BASE_URL", "BEDROCK_CONTROL_BASE_URL", "BEDROCK_MANTLE_BASE_URL",
	"BEDROCK_ANTHROPIC_ENDPOINT", "BEDROCK_TEMPERATURE", "BEDROCK_TOP_P",
	"OPENROUTER_API_KEY", "OPENROUTER_MODEL", "OPENROUTER_MAX_TOKENS", "OPENROUTER_API_URL",
	"OPENROUTER_FALLBACK_MODELS", "OPENROUTER_PROVIDER_ORDER", "OPENROUTER_TRANSFORMS",
	"OPENROUTER_TOOLS", "OPENROUTER_HTTP_REFERER", "OPENROUTER_APP_TITLE",
	"DEVIN_API_KEY", "DEVIN_MODEL", "DEVIN_CLI_PATH", "DEVIN_CLI_TIMEOUT", "DEVIN_CLI_EXTRA_ARGS",
	"DEVIN_CLI_PERMISSION_MODE", "DEVIN_CLI_SANDBOX", "DEVIN_CLI_AGENT_CONFIG", "DEVIN_CLI_USAGE_EXPORT",
	"CHATCLI_WEBFETCH_USER_AGENT", "CHATCLI_WEBFETCH_AUTOSAVE_BYTES",
	"CHATCLI_WEBFETCH_RENDER", "CHATCLI_WEBFETCH_RENDER_TIMEOUT", "CHATCLI_WEBFETCH_RENDER_AUTOPROVISION",
	"CHATCLI_WEBFETCH_RENDER_BROWSER",
	"CHATCLI_EMBED_PROVIDER", "CHATCLI_EMBED_MODEL", "CHATCLI_EMBED_DIMENSIONS",
	"CHATCLI_COMMANDS", "CHATCLI_COMMANDS_AUTOROUTE",
	"CHATCLI_SESSION_BUDGET_USD", "CHATCLI_BUDGET_WARNING_PCT", "CHATCLI_BUDGET_HARD_STOP",
}

// reloadConfiguration recarrega as variáveis de ambiente e reconfigura o LLMManager
func (cli *ChatCLI) reloadConfiguration(ctx context.Context) {
	fmt.Println(i18n.T("status.reloading_config"))

	prevProvider := cli.Provider
	prevModel := cli.Model

	envFilePath := os.Getenv("CHATCLI_DOTENV")
	if envFilePath == "" {
		envFilePath = ".env"
	} else {
		if expanded, err := utils.ExpandPath(envFilePath); err == nil {
			envFilePath = expanded
		} else {
			fmt.Println(i18n.T("main.warn_expand_path", envFilePath, err))
		}
	}
	for _, variable := range reloadableEnvVars {
		_ = os.Unsetenv(variable)
	}
	err := godotenv.Overload(envFilePath)
	if err != nil && !os.IsNotExist(err) {
		cli.logger.Error("Erro ao carregar o arquivo .env", zap.Error(err))
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

	if prevProvider != "" && prevModel != "" {
		if client, err := cli.manager.GetClient(prevProvider, prevModel); err == nil {
			cli.Client = client
			cli.Provider = prevProvider
			cli.Model = prevModel
			cli.refreshModelCache(ctx)
			fmt.Println(i18n.T("status.reload_success_preserved"))
			return
		}
		cli.logger.Warn("Falha ao preservar provider/model após reload; caindo para valores do .env",
			zap.String("provider", prevProvider), zap.String("model", prevModel))
	}
	cli.configureProviderAndModel()
	if client, err := cli.manager.GetClient(cli.Provider, cli.Model); err == nil {
		cli.Client = client
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
	"GITHUB_MODELS":    {envVar: "GITHUB_MODELS_MODEL", defaultName: config.DefaultGitHubModelsModel},
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

func (cli *ChatCLI) configureProviderAndModel() {
	// Normalização de case: LLM_PROVIDER=bedrock (minúsculo) furava todas
	// as comparações exatas e deixava provider E modelo desalinhados.
	cli.Provider = strings.ToUpper(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	if cli.Provider == "" {
		cli.Provider = config.DefaultLLMProvider
	}
	src, ok := bootModelSources[cli.Provider]
	if !ok {
		return
	}
	if m := resolveBootModelEnv(src.envVar); m != "" {
		cli.Model = m
		return
	}
	if m := resolveBootModelEnv(src.fallbackEnv); m != "" {
		cli.Model = m
		return
	}
	cli.Model = src.defaultName
}

func (cli *ChatCLI) setExecutionProfile(p ExecutionProfile) {
	cli.executionProfile = p
}

func (cli *ChatCLI) showHelp() {
	printCommand := func(cmd, desc string) {
		cmdColor := ColorCyan
		descColor := ColorGray
		if strings.HasPrefix(cmd, "  ") {
			cmdColor = ColorGray
			descColor = ColorGray
		}
		fmt.Printf("    %s    %s\n", colorize(kit.PadRight(cmd, 32), cmdColor), colorize(desc, descColor))
	}

	fmt.Println("\n" + colorize(i18n.T("help.header.title"), ColorBold))
	fmt.Println(colorize(i18n.T("help.header.subtitle1"), ColorGray))
	fmt.Println(colorize(i18n.T("help.header.subtitle2"), ColorGray))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.general"), ColorLime))
	printCommand("/help", i18n.T("help.command.help"))
	printCommand("/exit | /quit", i18n.T("help.command.exit"))
	printCommand("/newsession", i18n.T("help.command.newsession"))
	printCommand("/version | /v", i18n.T("help.command.version"))
	printCommand("/compact [instruction]", i18n.T("help.command.compact"))
	printCommand("/rewind", i18n.T("help.command.rewind"))
	printCommand("Esc+Esc", i18n.T("help.command.quick_rewind"))
	printCommand("/memory [subcommand]", i18n.T("help.command.memory"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.config"), ColorLime))
	printCommand("/switch", i18n.T("help.command.switch"))
	printCommand("/switch --model <nome>", i18n.T("help.command.switch_model"))
	printCommand("/model <nome>", i18n.T("help.command.model"))
	printCommand("  Ex: /model gpt-4o-mini", i18n.T("help.command.switch_model_example"))
	printCommand("/switch --max-tokens <num>", i18n.T("help.command.switch_max_tokens"))
	printCommand("/max-tokens <num>", i18n.T("help.command.maxtokens"))
	printCommand("/switch --realm <realm>", i18n.T("help.command.switch_realm"))
	printCommand("/switch --agent-id <id>", i18n.T("help.command.switch_agent_id"))
	printCommand("/config | /status", i18n.T("help.command.config"))
	printCommand("/reload", i18n.T("help.command.reload"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.context"), ColorLime))
	printCommand("@file <caminho>", i18n.T("help.command.file"))
	printCommand("  --mode full", i18n.T("help.command.file_mode_full"))
	printCommand("  --mode chunked", i18n.T("help.command.file_mode_chunked"))
	printCommand("  --mode summary", i18n.T("help.command.file_mode_summary"))
	printCommand("  --mode smart", i18n.T("help.command.file_mode_smart"))
	printCommand("  Ex: @file --mode=smart ./src ...", i18n.T("help.command.file_mode_example"))
	printCommand("@git", i18n.T("help.command.git"))
	printCommand("@history", i18n.T("help.command.history"))
	printCommand("@env", i18n.T("help.command.env"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.chunks"), ColorLime))
	printCommand("/nextchunk", i18n.T("help.command.nextchunk"))
	printCommand("/retry", i18n.T("help.command.retry"))
	printCommand("/retryall", i18n.T("help.command.retryall"))
	printCommand("/skipchunk", i18n.T("help.command.skipchunk"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.contexts"), ColorLime))
	printCommand("/context create <nome> <paths...>", i18n.T("help.command.context_create"))
	printCommand("  --mode <modo>", i18n.T("help.command.context_mode"))
	printCommand("  --description <texto>", i18n.T("help.command.context_description"))
	printCommand("  --tags <tag1,tag2>", i18n.T("help.command.context_tags"))
	printCommand("/context attach <nome>", i18n.T("help.command.context_attach"))
	printCommand("/context detach <nome>", i18n.T("help.command.context_detach"))
	printCommand("/context list", i18n.T("help.command.context_list"))
	printCommand("/context show <nome>", i18n.T("help.command.context_show"))
	printCommand("/context delete <nome>", i18n.T("help.command.context_delete"))
	printCommand("/context merge <novo> <ctx1> <ctx2>...", i18n.T("help.command.context_merge"))
	printCommand("/context attached", i18n.T("help.command.context_attached"))
	printCommand("/context export <nome> <arquivo>", i18n.T("help.command.context_export"))
	printCommand("/context import <arquivo>", i18n.T("help.command.context_import"))
	printCommand("/context metrics", i18n.T("help.command.context_metrics"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.exec"), ColorLime))
	printCommand("@command <cmd>", i18n.T("help.command.command"))
	printCommand("  Ex: @command ls -la", i18n.T("help.command.command_example"))
	printCommand("@command -i <cmd>", i18n.T("help.command.command_i"))
	printCommand("@command --ai <cmd>", i18n.T("help.command.command_ai"))
	printCommand("  Ex: @command --ai git diff", i18n.T("help.command.command_ai_example"))
	printCommand("@command --ai <cmd> > <texto>", i18n.T("help.command.command_ai_context"))
	printCommand("  Ex: @command --ai cat err.log > ...", i18n.T("help.command.command_ai_context_example"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.agent"), ColorLime))
	printCommand("/agent <tarefa>", i18n.T("help.command.agent"))
	printCommand("/run <tarefa>", i18n.T("help.command.run"))
	printCommand("  Ex: /agent ...", i18n.T("help.command.agent_example"))
	printCommand(i18n.T("help.command.agent_inside"), "")
	printCommand("  [1..N]", i18n.T("help.command.agent_exec_n"))
	printCommand("  a", i18n.T("help.command.agent_exec_all"))
	printCommand("  eN", i18n.T("help.command.agent_edit"))
	printCommand("  tN", i18n.T("help.command.agent_dry_run"))
	printCommand("  cN", i18n.T("help.command.agent_continue"))
	printCommand("  pcN", i18n.T("help.command.agent_pre_context"))
	printCommand("  acN", i18n.T("help.command.agent_post_context"))
	printCommand("  vN", i18n.T("help.command.agent_view"))
	printCommand("  wN", i18n.T("help.command.agent_save"))
	printCommand("  p", i18n.T("help.command.agent_toggle_plan"))
	printCommand("  r", i18n.T("help.command.agent_redraw"))
	printCommand("  q", i18n.T("help.command.agent_quit"))
	printCommand(i18n.T("help.command.agent_notes"), "")
	printCommand("  "+i18n.T("help.command.agent_last_result"), "")
	printCommand("  "+i18n.T("help.command.agent_compact_plan"), "")
	printCommand("  "+i18n.T("help.command.agent_full_plan"), "")

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.coder"), ColorLime))
	printCommand("/coder <tarefa>", i18n.T("help.command.coder"))
	printCommand("  Ex: /coder ...", i18n.T("help.command.coder_example"))

	printCommand(i18n.T("help.command.coder_notes"), "")
	printCommand("  "+i18n.T("help.command.coder_note_plugin"), "")
	printCommand("  "+i18n.T("help.command.coder_note_auto_tools"), "")

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.plugins"), ColorLime))
	printCommand("/plugin list", i18n.T("help.command.plugin_list"))
	printCommand("/plugin install <url>", i18n.T("help.command.plugin_install"))
	printCommand("/plugin show <nome>", i18n.T("help.command.plugin_show"))
	printCommand("/plugin inspect <nome>", i18n.T("help.command.plugin_inspect"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.persona"), ColorLime))
	printCommand("/agent list", i18n.T("help.command.persona_list"))
	printCommand("/agent load <nome>", i18n.T("help.command.persona_load"))
	printCommand("/agent skills", i18n.T("help.command.persona_skills"))
	printCommand("/agent show", i18n.T("help.command.persona_show"))
	printCommand("/agent status", i18n.T("help.command.persona_status"))
	printCommand("/agent off", i18n.T("help.command.persona_off"))
	printCommand("/agents {list|show <id>|cancel <id>}", i18n.T("help.command.agents"))
	printCommand("/board {list|show|create|move|…}", i18n.T("help.command.board"))
	printCommand("/mail {list|send <agent> <texto>|pending}", i18n.T("help.command.mail"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.scheduler"), ColorLime))
	printCommand("/schedule <nome> --when <t> --do <a>", i18n.T("help.command.schedule"))
	printCommand("/wait --until <cond> [--then <a>]", i18n.T("help.command.wait"))
	printCommand("/jobs {list|show|tree|cancel|logs|…}", i18n.T("help.command.jobs"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.sessions"), ColorLime))
	printCommand("/session save <nome>", i18n.T("help.command.session_save"))
	printCommand("/session load <nome>", i18n.T("help.command.session_load"))
	printCommand("/session list", i18n.T("help.command.session_list"))
	printCommand("/session delete <nome>", i18n.T("help.command.session_delete"))
	printCommand("/session new", i18n.T("help.command.session_new"))
	printCommand("/hub", i18n.T("help.command.hub"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.oneshot"), ColorLime))
	printCommand("chatcli -p \"<prompt>\"", i18n.T("help.command.oneshot_p"))
	printCommand("  Ex: chatcli -p \"...\"", i18n.T("help.command.oneshot_p_example"))
	printCommand("chatcli --prompt \"<prompt>\"", i18n.T("help.command.oneshot_prompt"))
	printCommand("--provider <nome>", i18n.T("help.command.oneshot_provider"))
	printCommand("--model <nome>", i18n.T("help.command.oneshot_model"))
	printCommand("--agent-id <id>", i18n.T("help.command.oneshot_agent_id"))
	printCommand("--max-tokens <num>", i18n.T("help.command.oneshot_max_tokens"))
	printCommand("--timeout <duração>", i18n.T("help.command.oneshot_timeout"))
	printCommand("--no-anim", i18n.T("help.command.oneshot_no_anim"))
	printCommand("--agent-auto-exec", i18n.T("help.command.oneshot_auto_exec"))
	printCommand(i18n.T("help.command.oneshot_pipes"), "")

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.tips"), ColorLime))
	printCommand("Cancelamento (Ctrl+C)", i18n.T("help.command.tips_cancel"))
	printCommand("Saída Rápida (Ctrl+D)", i18n.T("help.command.tips_exit"))
	printCommand("Operador '>'", i18n.T("help.command.tips_operator"))
	printCommand("Modo Agente: p", i18n.T("help.command.tips_agent_p"))
	printCommand("Modo Agente: vN", i18n.T("help.command.tips_agent_v"))
	printCommand("Modo Agente: wN", i18n.T("help.command.tips_agent_w"))
	printCommand("Modo Agente: r", i18n.T("help.command.tips_agent_r"))

	fmt.Println()
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

// getEnvFilePath retorna o caminho do arquivo .env configurado (expandido).
func (cli *ChatCLI) getEnvFilePath() string {
	envFilePath := os.Getenv("CHATCLI_DOTENV")
	if envFilePath == "" {
		envFilePath = ".env"
	}
	expanded, err := utils.ExpandPath(envFilePath)
	if err != nil {
		cli.logger.Warn("Não foi possível expandir o caminho do .env", zap.Error(err))
		return envFilePath // Retorna o original se falhar
	}
	return expanded
}

func (ch *CommandHandler) handleVersionCommand(ctx context.Context) {
	// Checagem com timeout
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fmt.Println(FormatVersionReport(version.GetReport(ctx)))
}
