package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/manager"
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

// reloadConfiguration recarrega as variáveis de ambiente e reconfigura o LLMManager
func (cli *ChatCLI) reloadConfiguration() {
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
	variablesToUnset := []string{
		"LOG_LEVEL", "ENV", "LLM_PROVIDER", "LOG_FILE", "LOG_MAX_SIZE", "HISTORY_MAX_SIZE",
		"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_ASSISTANT_MODEL",
		"OPENAI_USE_RESPONSES", "OPENAI_MAX_TOKENS",
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_MAX_TOKENS", "ANTHROPIC_API_VERSION",
		"GOOGLEAI_API_KEY", "GOOGLEAI_MODEL", "GOOGLEAI_MAX_TOKENS",
		"XAI_API_KEY", "XAI_MODEL", "XAI_MAX_TOKENS",
		"ZAI_API_KEY", "ZAI_MODEL", "ZAI_MAX_TOKENS",
		"MINIMAX_API_KEY", "MINIMAX_MODEL", "MINIMAX_MAX_TOKENS",
		"OLLAMA_ENABLED", "OLLAMA_BASE_URL", "OLLAMA_MODEL", "OLLAMA_MAX_TOKENS",
		"CLIENT_ID", "CLIENT_KEY", "STACKSPOT_REALM", "STACKSPOT_AGENT_ID",
		"COPILOT_MODEL", "COPILOT_MAX_TOKENS", "GITHUB_COPILOT_TOKEN",
		"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_MODELS_TOKEN", "GITHUB_MODELS_MODEL",
	}

	for _, variable := range variablesToUnset {
		_ = os.Unsetenv(variable)
	}

	err := godotenv.Overload(envFilePath)
	if err != nil && !os.IsNotExist(err) {
		cli.logger.Error("Erro ao carregar o arquivo .env", zap.Error(err))
	}

	config.Global.Reload(cli.logger)

	cli.reconfigureLogger()

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
			cli.refreshModelCache()
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

func (cli *ChatCLI) configureProviderAndModel() {
	cli.Provider = os.Getenv("LLM_PROVIDER")
	if cli.Provider == "" {
		cli.Provider = config.DefaultLLMProvider
	}
	if cli.Provider == "OPENAI" {
		cli.Model = os.Getenv("OPENAI_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultOpenAIModel
		}
	}
	if cli.Provider == "OPENAI_ASSISTANT" {
		cli.Model = os.Getenv("OPENAI_ASSISTANT_MODEL")
		if cli.Model == "" {
			cli.Model = utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAiAssistModel)
		}
	}
	if cli.Provider == "CLAUDEAI" {
		cli.Model = os.Getenv("ANTHROPIC_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultClaudeAIModel
		}
	}
	if cli.Provider == "GOOGLEAI" {
		cli.Model = os.Getenv("GOOGLEAI_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultGoogleAIModel
		}
	}
	if cli.Provider == "XAI" {
		cli.Model = os.Getenv("XAI_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultXAIModel
		}
	}
	if cli.Provider == "ZAI" {
		cli.Model = os.Getenv("ZAI_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultZAIModel
		}
	}
	if cli.Provider == "MINIMAX" {
		cli.Model = os.Getenv("MINIMAX_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultMiniMaxModel
		}
	}
	if cli.Provider == "OLLAMA" {
		cli.Model = os.Getenv("OLLAMA_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultOllamaModel
		}
	}
	if cli.Provider == "COPILOT" {
		cli.Model = os.Getenv("COPILOT_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultCopilotModel
		}
	}
	if cli.Provider == "GITHUB_MODELS" {
		cli.Model = os.Getenv("GITHUB_MODELS_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultGitHubModelsModel
		}
	}
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
		fmt.Printf("    %s    %s\n", colorize(fmt.Sprintf("%-32s", cmd), cmdColor), colorize(desc, descColor))
	}

	fmt.Println("\n" + colorize(ColorBold, i18n.T("help.header.title")))
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
	printCommand("  Ex: /switch --model gpt-4o-mini", i18n.T("help.command.switch_model_example"))
	printCommand("/switch --max-tokens <num>", i18n.T("help.command.switch_max_tokens"))
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

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.sessions"), ColorLime))
	printCommand("/session save <nome>", i18n.T("help.command.session_save"))
	printCommand("/session load <nome>", i18n.T("help.command.session_load"))
	printCommand("/session list", i18n.T("help.command.session_list"))
	printCommand("/session delete <nome>", i18n.T("help.command.session_delete"))
	printCommand("/session new", i18n.T("help.command.session_new"))

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

func (cli *ChatCLI) ApplyOverrides(mgr manager.LLMManager, provider, model string) error {
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
	cli.refreshModelCache()
	return nil
}

// presence retorna "[SET]" ou "[NOT SET]" para uma env sensível
func presence(v string) string {
	if strings.TrimSpace(v) == "" {
		return "[NOT SET]"
	}
	return "[SET]"
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

func (cli *ChatCLI) showConfig() {
	printItem := func(key, value string) {
		keyColor := ColorCyan
		valueColor := ColorGray
		if strings.Contains(value, "[SET]") {
			valueColor = ColorGreen
		} else if strings.Contains(value, "[NOT SET]") {
			valueColor = ColorYellow
		}
		fmt.Printf("    %s    %s\n", colorize(fmt.Sprintf("%-25s", key+":"), keyColor), colorize(value, valueColor))
	}

	fmt.Println("\n" + colorize(ColorBold, i18n.T("cli.config.header")))
	fmt.Println(colorize(i18n.T("cli.config.subtitle"), ColorGray))

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_general"), ColorLime))
	printItem(i18n.T("cli.config.key_dotenv_file"), cli.getEnvFilePath())
	printItem(i18n.T("cli.config.key_env"), os.Getenv("ENV"))
	printItem(i18n.T("cli.config.key_log_level"), os.Getenv("LOG_LEVEL"))
	printItem(i18n.T("cli.config.key_log_file"), os.Getenv("LOG_FILE"))
	printItem(i18n.T("cli.config.key_log_max_size"), os.Getenv("LOG_MAX_SIZE"))
	printItem(i18n.T("cli.config.key_history_max_size"), os.Getenv("HISTORY_MAX_SIZE"))
	printItem(i18n.T("cli.config.key_history_file_directory"), cli.historyManager.GetHistoryFilePath())

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_coder_mode"), ColorLime))
	coderUIRaw := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_UI")))
	coderUIEffective := "full"
	if coderUIRaw == "minimal" || coderUIRaw == "min" || coderUIRaw == "true" || coderUIRaw == "1" {
		coderUIEffective = "minimal"
	}
	coderBannerRaw := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_BANNER")))
	coderBannerEffective := "on"
	if coderBannerRaw == "false" || coderBannerRaw == "0" || coderBannerRaw == "no" {
		coderBannerEffective = "off"
	}
	printItem("CHATCLI_CODER_UI", os.Getenv("CHATCLI_CODER_UI"))
	printItem("CHATCLI_CODER_UI (effective)", coderUIEffective)
	printItem("CHATCLI_CODER_BANNER", os.Getenv("CHATCLI_CODER_BANNER"))
	printItem("CHATCLI_CODER_BANNER (effective)", coderBannerEffective)

	policyPath := "[unknown]"
	localPath := "[none]"
	localMerge := "off"
	rulesCount := "0"
	lastRule := "[none]"
	if pm, err := coder.NewPolicyManager(cli.logger); err == nil {
		policyPath = pm.ActivePolicyPath()
		if lp := pm.LocalPolicyPath(); strings.TrimSpace(lp) != "" {
			localPath = lp
			if pm.LocalMergeEnabled() {
				localMerge = "on"
			}
		}
		rulesCount = fmt.Sprintf("%d", pm.RulesCount())
	}
	printItem("CODER_POLICY (active)", policyPath)
	printItem("CODER_POLICY (local)", localPath)
	printItem("CODER_POLICY (local merge)", localMerge)
	printItem("CODER_POLICY (rules)", rulesCount)
	if cli.agentMode != nil && cli.agentMode.lastPolicyMatch != nil {
		lastRule = fmt.Sprintf("%s => %s", cli.agentMode.lastPolicyMatch.Pattern, cli.agentMode.lastPolicyMatch.Action)
	}
	printItem("CODER_POLICY (last match)", lastRule)

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_current_provider"), ColorLime))
	printItem(i18n.T("cli.config.key_provider_runtime"), cli.Provider)
	printItem(i18n.T("cli.config.key_model_runtime"), cli.Model)
	if cli.Client != nil {
		printItem(i18n.T("cli.config.key_model_name_client"), cli.Client.GetModelName())
	} else {
		printItem(i18n.T("cli.config.key_model_name_client"), "(no provider)")
	}
	printItem(i18n.T("cli.config.key_preferred_api"), string(catalog.GetPreferredAPI(cli.Provider, cli.Model)))
	printItem(i18n.T("cli.config.key_effective_max_tokens"), fmt.Sprintf("%d", cli.getMaxTokensForCurrentLLM()))

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_max_tokens_overrides"), ColorLime))
	printItem("OPENAI_MAX_TOKENS", os.Getenv("OPENAI_MAX_TOKENS"))
	printItem("ANTHROPIC_MAX_TOKENS", os.Getenv("ANTHROPIC_MAX_TOKENS"))
	printItem("GOOGLEAI_MAX_TOKENS", os.Getenv("GOOGLEAI_MAX_TOKENS"))
	printItem("XAI_MAX_TOKENS", os.Getenv("XAI_MAX_TOKENS"))
	printItem("OLLAMA_MAX_TOKENS", os.Getenv("OLLAMA_MAX_TOKENS"))
	printItem("STACKSPOT_MAX_TOKENS", os.Getenv("STACKSPOT_MAX_TOKENS"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_sensitive_keys"), ColorLime))
	printItem("OPENAI_API_KEY", presence(os.Getenv("OPENAI_API_KEY")))
	printItem("ANTHROPIC_API_KEY", presence(os.Getenv("ANTHROPIC_API_KEY")))
	printItem("GOOGLEAI_API_KEY", presence(os.Getenv("GOOGLEAI_API_KEY")))
	printItem("XAI_API_KEY", presence(os.Getenv("XAI_API_KEY")))
	printItem(i18n.T("cli.config.key_client_id_stackspot"), presence(os.Getenv("CLIENT_ID")))
	printItem(i18n.T("cli.config.key_client_key_stackspot"), presence(os.Getenv("CLIENT_KEY")))

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_provider_settings"), ColorLime))
	printItem("OPENAI_MODEL", os.Getenv("OPENAI_MODEL"))
	printItem("OPENAI_ASSISTANT_MODEL", os.Getenv("OPENAI_ASSISTANT_MODEL"))
	printItem("OPENAI_USE_RESPONSES", os.Getenv("OPENAI_USE_RESPONSES"))
	printItem("ANTHROPIC_MODEL", os.Getenv("ANTHROPIC_MODEL"))
	printItem("ANTHROPIC_API_VERSION", os.Getenv("ANTHROPIC_API_VERSION"))
	printItem("GOOGLEAI_MODEL", os.Getenv("GOOGLEAI_MODEL"))
	printItem("XAI_MODEL", os.Getenv("XAI_MODEL"))
	printItem("OLLAMA_MODEL", os.Getenv("OLLAMA_MODEL"))
	printItem("OLLAMA_BASE_URL", utils.GetEnvOrDefault("OLLAMA_BASE_URL", config.OllamaDefaultBaseURL))

	isStackSpotAvailable := false
	for _, p := range cli.manager.GetAvailableProviders() {
		if p == "STACKSPOT" {
			isStackSpotAvailable = true
			break
		}
	}
	if cli.Provider == "STACKSPOT" || isStackSpotAvailable {
		printItem(i18n.T("cli.config.key_stackspot_realm"), cli.manager.GetStackSpotRealm())
		printItem(i18n.T("cli.config.key_stackspot_agent_id"), cli.manager.GetStackSpotAgentID())
	}

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_available_providers"), ColorLime))
	providers := cli.manager.GetAvailableProviders()
	if len(providers) > 0 {
		for i, p := range providers {
			printItem(i18n.T("cli.config.key_provider_n", i+1), p)
		}
	} else {
		printItem(i18n.T("cli.config.none"), i18n.T("cli.config.no_providers_configured"))
	}
}

func (ch *CommandHandler) handleVersionCommand() {
	versionInfo := version.GetCurrentVersion()

	// Checagem com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	latest, hasUpdate, err := version.CheckLatestVersionWithContext(ctx)

	// Exibir as informações formatadas
	fmt.Println(version.FormatVersionInfo(versionInfo, latest, hasUpdate, err))
}
