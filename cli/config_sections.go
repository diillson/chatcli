/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config subsections.
 *
 * Design: each showConfigX() is self-contained and reads directly from the
 * live CLI state (env vars, managers, handlers). None of them pre-compute or
 * cache — `/config` is called interactively and performance is irrelevant.
 *
 * i18n: every user-facing string goes through i18n.T(). Keys live in
 * i18n/locales/*.json under cfg.*, ws.cmd.*, complete.config.* namespaces.
 *
 * Routing layout:
 *   /config                → showConfigPanorama  (short overview)
 *   /config all            → showConfigAll       (every section back-to-back)
 *   /config general        → showConfigGeneral
 *   /config providers      → showConfigProviders
 *   /config agent          → showConfigAgent
 *   /config resilience     → showConfigResilience
 *   /config session        → showConfigSession
 *   /config integrations   → showConfigIntegrations
 *   /config auth           → showConfigAuth
 *   /config security       → showConfigSecurity
 *   /config server         → showConfigServer
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/imagegen"
	"github.com/diillson/chatcli/llm/transcription"
	"github.com/diillson/chatcli/llm/tts"
)

// ─── Routing ───────────────────────────────────────────────────

// routeConfigCommand dispatches /config [section]. Args comes without the
// leading "/config" token.
func (cli *ChatCLI) routeConfigCommand(args []string) {
	if len(args) == 0 {
		cli.showConfigPanorama()
		return
	}
	switch strings.ToLower(args[0]) {
	case "all", "full":
		cli.showConfigAll()
	case "general":
		cli.showConfigGeneral()
	case "providers", "provider":
		cli.showConfigProviders()
	case "agent":
		// /config agent is also hierarchical now: bare form dumps the
		// read-only panorama; ui (and future subcommands) mutate the
		// runtime style. Mirrors the security routing immediately
		// below so behavior is uniform across sections.
		if len(args) == 1 {
			cli.showConfigAgent()
		} else {
			cli.routeConfigAgent(args[1:])
		}
	case "ui":
		// Hierarchical like security/hub: bare form shows the theme +
		// color-profile panorama; `ui theme <name>` switches at runtime.
		if len(args) == 1 {
			cli.printConfigUIStatus()
		} else {
			cli.routeConfigUI(args[1:])
		}
	case "theme":
		// Convenience alias so `/config theme dark` == `/config ui theme dark`.
		// args already starts with "theme", which routeConfigUI reads as its
		// subcommand, so pass it through unstripped.
		cli.routeConfigUI(args)
	case "resilience", "proxy":
		cli.showConfigResilience()
	case "session":
		cli.showConfigSession()
	case "integrations", "integration":
		cli.showConfigIntegrations()
	case "auth":
		cli.showConfigAuth()
	case "security":
		// /config security is now hierarchical: the bare form still
		// dumps the read-only panorama, but rules/allow/deny/forget/
		// reload subcommands mutate the coder PolicyManager live.
		if len(args) == 1 {
			cli.showConfigSecurity()
		} else {
			cli.routeConfigSecurity(args[1:])
		}
	case "chat":
		// Hierarchical like security/agent: bare form shows the chat panorama;
		// `chat ask on|off|toggle` flips the ask_user exception at runtime.
		if len(args) == 1 {
			cli.showConfigChat()
		} else {
			cli.routeConfigChat(args[1:])
		}
	case "image", "img":
		// Hierarchical: bare form shows the @image panorama; provider/api/
		// model/url/models/reset mutate the backend at runtime.
		if len(args) == 1 {
			cli.showConfigImage()
		} else {
			cli.routeConfigImage(args[1:])
		}
	case "quality":
		cli.showConfigQuality()
	case "memory", "mem":
		cli.showConfigMemory()
	case "scheduler", "schedule":
		cli.showConfigScheduler()
	case "server":
		cli.showConfigServer()
	case "hub":
		// Hierarchical like security: bare form shows the panorama; set/reset
		// mutate the live, db-backed hub settings (read by the gateway too).
		if len(args) == 1 {
			cli.showConfigHub()
		} else {
			cli.routeConfigHub(args[1:])
		}
	default:
		fmt.Println(colorize("  "+i18n.T("cfg.route.unknown_section", args[0]), ColorYellow))
		fmt.Println(colorize("  "+i18n.T("cfg.route.hint"), ColorGray))
	}
}

// ─── Shared formatting helpers ─────────────────────────────────

// sectionHeader prints a colored box header for a config section.
func sectionHeader(icon, titleKey, color string) {
	fmt.Println()
	fmt.Println(uiBox(icon, i18n.T(titleKey), color))
}

// sectionEnd prints the matching closing box border.
func sectionEnd(color string) {
	fmt.Println(uiBoxEnd(color))
}

// defaultMarker is a zero-width sentinel prefix that envOr/envBool use
// to flag a value as "this is the runtime default, the env var is unset".
// kv() detects the marker, strips it for display, and renders the line
// in a distinct color with a "(default)" suffix so the operator sees at
// a glance whether the value comes from their environment or from the
// code's fallback. Using an ASCII control character (U+0001) keeps the
// channel out-of-band from any legitimate env value the user could set.
const defaultMarker = "\x01"

// kv prints a key/value line inside a section with automatic coloring.
// key is an i18n-translated label (the caller passes i18n.T(...)) or a
// literal env var name when no translation applies.
//
// Coloring rules:
//   - "[SET]" / enabled / on     → green (explicitly configured truthy)
//   - "[NOT SET]" / disabled     → yellow (off OR unconfigured w/ no fallback)
//   - defaultMarker prefix       → cyan + "(default)" suffix (the runtime
//     fallback from the env-default registry)
//   - everything else            → gray (user-set value)
func kv(prefix, key, value string) {
	valueColor := ColorGray
	display := strings.TrimSpace(value)
	enabledLabel := i18n.T("cfg.val.enabled")
	disabledLabel := i18n.T("cfg.val.disabled")
	defaultTag := i18n.T("cfg.val.default_tag")

	// Default-value path: strip the sentinel, render in cyan with a
	// trailing "(default)" tag so the line visibly distinguishes from
	// user-set values without losing the actual value.
	if strings.HasPrefix(display, defaultMarker) {
		display = strings.TrimPrefix(display, defaultMarker)
		fmt.Printf("%s%s  %s %s\n",
			prefix,
			colorize(fmt.Sprintf("%-32s", key+":"), ColorCyan),
			colorize(display, ColorCyan),
			colorize(defaultTag, ColorGray))
		return
	}

	switch {
	case display == "":
		display = i18n.T("cfg.val.default")
	case strings.Contains(display, "[SET]") || display == enabledLabel || display == i18n.T("cfg.val.on"):
		valueColor = ColorGreen
	case strings.Contains(display, "[NOT SET]") ||
		display == i18n.T("cfg.val.not_set") ||
		display == i18n.T("cfg.val.none") ||
		display == disabledLabel ||
		display == i18n.T("cfg.val.off"):
		valueColor = ColorYellow
	}
	fmt.Printf("%s%s  %s\n",
		prefix,
		colorize(fmt.Sprintf("%-32s", key+":"), ColorCyan),
		colorize(display, valueColor))
}

// envOr returns the env value, or the registered runtime default tagged
// with defaultMarker so kv() can color it as "(default)", or the
// localized "(not set)" placeholder when neither source has a value.
//
// The default registry is consulted only when the env is unset — an
// explicitly-set empty string still goes through the not_set branch
// (matching pre-registry behavior).
func envOr(name string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	if d, ok := lookupEnvDefault(name); ok && !d.IsBool {
		return defaultMarker + formatDefaultValue(d.Value)
	}
	return i18n.T("cfg.val.not_set")
}

// envBool renders the localized enabled/disabled/default labels for boolean
// envs (accepts 1/true/yes). When the var is unset and the default
// registry has a known boolean fallback, the rendered line is the
// localized enabled/disabled label tagged with defaultMarker so kv()
// reports e.g. "enabled (default)" instead of a bare "(default)" with
// no truth value attached.
func envBool(name string) string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "1", "true", "yes", "on":
		return i18n.T("cfg.val.enabled")
	case "0", "false", "no", "off":
		return i18n.T("cfg.val.disabled")
	case "":
		if d, ok := lookupEnvDefault(name); ok && d.IsBool {
			label := i18n.T("cfg.val.disabled")
			if strings.EqualFold(d.Value, "true") {
				label = i18n.T("cfg.val.enabled")
			}
			return defaultMarker + label
		}
		return i18n.T("cfg.val.default")
	default:
		return v
	}
}

// shorten trims long values (paths, tokens) for display.
func shorten(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max < 8 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// humanDuration formats a duration as "2h 34m" / "12m 3s" / "45s".
// Non-translated: the h/m/s suffixes are language-neutral shorthand.
func humanDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	return fmt.Sprintf("%dh %dm", h, m)
}

// subheader prints a "── <label>" subsection divider in gray.
func subheader(prefix, labelKey string) {
	fmt.Println(prefix + colorize(i18n.T(labelKey), ColorGray))
}

// ─── Section: PANORAMA ─────────────────────────────────────────

// showConfigPanorama renders the short default view: what matters now, at a
// glance, with numeric summaries for the heavier sections so the user knows
// where to drill down.
func (cli *ChatCLI) showConfigPanorama() {
	sectionHeader("⚙", "cfg.panorama.title", ColorCyan)
	p := uiPrefix(ColorCyan)

	// Provider/model
	kv(p, i18n.T("cfg.kv.provider"), cli.Provider)
	kv(p, i18n.T("cfg.kv.model"), cli.Model)
	if cli.Client != nil {
		kv(p, i18n.T("cfg.kv.model_client"), cli.Client.GetModelName())
	}
	kv(p, i18n.T("cfg.kv.preferred_api"), string(catalog.GetPreferredAPI(cli.Provider, cli.Model)))
	kv(p, i18n.T("cfg.kv.effective_max_tokens"), fmt.Sprintf("%d", cli.getMaxTokensForCurrentLLM()))

	// Session summary
	fmt.Println(p)
	sessName := cli.currentSessionName
	if sessName == "" {
		sessName = i18n.T("cfg.val.unnamed_session")
	}
	kv(p, i18n.T("cfg.kv.session"), sessName)
	kv(p, i18n.T("cfg.kv.messages"), fmt.Sprintf("%d", len(cli.history)))
	if cli.costTracker != nil {
		tokens := cli.costTracker.TotalTokens()
		cost := cli.costTracker.TotalCost()
		kv(p, i18n.T("cfg.kv.tokens_used"), fmt.Sprintf("%d", tokens))
		kv(p, i18n.T("cfg.kv.cost_usd"), fmt.Sprintf("$%.4f", cost))
		if cli.costTracker.CheckBudget() != BudgetOK {
			kv(p, i18n.T("cfg.kv.budget"), colorize(cli.costTracker.BudgetMessage(), ColorYellow))
		}
	}
	if !cli.sessionStartTime.IsZero() {
		kv(p, i18n.T("cfg.kv.session_duration"), humanDuration(time.Since(cli.sessionStartTime)))
	}

	// Active agent / persona
	fmt.Println(p)
	if cli.personaHandler != nil && cli.personaHandler.GetManager() != nil {
		if active := cli.personaHandler.GetManager().GetActiveAgent(); active != nil {
			kv(p, i18n.T("cfg.kv.persona"), active.Name)
		} else {
			kv(p, i18n.T("cfg.kv.persona"), i18n.T("cfg.val.none"))
		}
	}

	// Integration counters (no-op-safe)
	fmt.Println(p)
	pluginCount := 0
	if cli.pluginManager != nil {
		pluginCount = len(cli.pluginManager.GetPlugins())
	}
	kv(p, i18n.T("cfg.kv.plugins_loaded"), fmt.Sprintf("%d", pluginCount))

	mcpServers, mcpTools := 0, 0
	if cli.mcpManager != nil {
		for _, s := range cli.mcpManager.GetServerStatus() {
			mcpServers++
			if s.Connected {
				mcpTools += s.ToolCount
			}
		}
	}
	kv(p, i18n.T("cfg.kv.mcp_servers"), fmt.Sprintf("%d (%d tools)", mcpServers, mcpTools))

	hookCount := 0
	if cli.hookManager != nil {
		hookCount = cli.hookManager.Count()
	}
	kv(p, i18n.T("cfg.kv.hooks_configured"), fmt.Sprintf("%d", hookCount))

	// Web search chain
	chain := plugins.SelectSearchChainNames()
	names := make([]string, 0, len(chain))
	for _, n := range chain {
		names = append(names, string(n))
	}
	kv(p, i18n.T("cfg.kv.websearch_chain"), strings.Join(names, " → "))

	// Remote
	if cli.isRemote && cli.remoteAddress != "" {
		kv(p, i18n.T("cfg.kv.remote"), cli.remoteAddress)
	}

	// Footer
	fmt.Println(p)
	fmt.Println(p + colorize(i18n.T("cfg.panorama.drill_down"), ColorGray) + "  " + colorize("/config all · /config <section>", ColorBold))
	fmt.Println(p + colorize(i18n.T("cfg.panorama.sections_hint"), ColorGray))
	sectionEnd(ColorCyan)
}

// showConfigAll runs every section in sequence. Used by `/config all`.
func (cli *ChatCLI) showConfigAll() {
	cli.showConfigPanorama()
	cli.showConfigGeneral()
	cli.showConfigProviders()
	cli.showConfigAgent()
	cli.showConfigUI()
	cli.showConfigResilience()
	cli.showConfigSession()
	cli.showConfigIntegrations()
	cli.showConfigAuth()
	cli.showConfigSecurity()
	cli.showConfigChat()
	cli.showConfigQuality()
	cli.showConfigScheduler()
	// server block is conditional (see its own guard)
	cli.showConfigServer()
}

// ─── Section: GENERAL ──────────────────────────────────────────

// showConfigGeneral covers the process-wide basics: dotenv, ENV flag,
// logging, history persistence, locale, and version-check toggles.
func (cli *ChatCLI) showConfigGeneral() {
	sectionHeader("🧭", "cfg.section.general.title", ColorCyan)
	p := uiPrefix(ColorCyan)

	kv(p, i18n.T("cfg.kv.dotenv_path"), cli.getEnvFilePath())
	kv(p, "CHATCLI_DOTENV", envOr("CHATCLI_DOTENV"))
	kv(p, "ENV", envOr("ENV"))
	kv(p, "CHATCLI_ENV", envOr("CHATCLI_ENV"))
	kv(p, "CHATCLI_DEBUG", envBool("CHATCLI_DEBUG"))

	fmt.Println(p)
	subheader(p, "cfg.sub.general.locale")
	kv(p, "CHATCLI_LANG", envOr("CHATCLI_LANG"))
	kv(p, "LANG", envOr("LANG"))
	kv(p, "LC_ALL", envOr("LC_ALL"))
	kv(p, "CHATCLI_DISABLE_VERSION_CHECK", envBool("CHATCLI_DISABLE_VERSION_CHECK"))
	kv(p, "CHATCLI_LATEST_VERSION_URL", envOr("CHATCLI_LATEST_VERSION_URL"))

	fmt.Println(p)
	subheader(p, "cfg.sub.general.logging")
	kv(p, "LOG_LEVEL", envOr("LOG_LEVEL"))
	kv(p, "LOG_FILE", envOr("LOG_FILE"))
	kv(p, "LOG_MAX_SIZE", envOr("LOG_MAX_SIZE"))
	kv(p, "CHATCLI_LOG_FILE", envOr("CHATCLI_LOG_FILE"))
	kv(p, "CHATCLI_LOG_MAX_SIZE_MB", envOr("CHATCLI_LOG_MAX_SIZE_MB"))
	kv(p, "CHATCLI_LOG_MAX_BACKUPS", envOr("CHATCLI_LOG_MAX_BACKUPS"))
	kv(p, "CHATCLI_LOG_MAX_AGE_DAYS", envOr("CHATCLI_LOG_MAX_AGE_DAYS"))
	kv(p, "CHATCLI_LOG_COMPRESS", envBool("CHATCLI_LOG_COMPRESS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.general.history")
	kv(p, "HISTORY_MAX_SIZE", envOr("HISTORY_MAX_SIZE"))
	if cli.historyManager != nil {
		kv(p, i18n.T("cfg.kv.history_file"), cli.historyManager.GetHistoryFilePath())
	}

	sectionEnd(ColorCyan)
}

// ─── Section: PROVIDERS ────────────────────────────────────────

// showConfigProviders dumps every provider-specific env var + sensitive
// keys (presence only, never the value) and StackSpot runtime overrides.
func (cli *ChatCLI) showConfigProviders() {
	sectionHeader("🔌", "cfg.section.providers.title", ColorPurple)
	p := uiPrefix(ColorPurple)

	// Current
	kv(p, i18n.T("cfg.kv.active_provider"), cli.Provider)
	kv(p, i18n.T("cfg.kv.active_model"), cli.Model)
	kv(p, i18n.T("cfg.kv.effective_max_tokens"), fmt.Sprintf("%d", cli.getMaxTokensForCurrentLLM()))

	// Available (nil-safe — manager may not be wired during early boot/tests)
	if cli.manager != nil {
		if providers := cli.manager.GetAvailableProviders(); len(providers) > 0 {
			fmt.Println(p)
			for i, name := range providers {
				kv(p, i18n.T("cfg.kv.available_n", i+1), name)
			}
		}
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.openai")
	kv(p, "OPENAI_API_KEY", presence(os.Getenv("OPENAI_API_KEY")))
	kv(p, "OPENAI_MODEL", envOr("OPENAI_MODEL"))
	kv(p, "OPENAI_ASSISTANT_MODEL", envOr("OPENAI_ASSISTANT_MODEL"))
	kv(p, "OPENAI_USE_RESPONSES", envOr("OPENAI_USE_RESPONSES"))
	kv(p, "OPENAI_MAX_TOKENS", envOr("OPENAI_MAX_TOKENS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.anthropic")
	kv(p, "ANTHROPIC_API_KEY", presence(os.Getenv("ANTHROPIC_API_KEY")))
	kv(p, "ANTHROPIC_MODEL", envOr("ANTHROPIC_MODEL"))
	kv(p, "ANTHROPIC_BASE_URL", envOr("ANTHROPIC_BASE_URL"))
	kv(p, "ANTHROPIC_API_VERSION", envOr("ANTHROPIC_API_VERSION"))
	kv(p, "ANTHROPIC_MAX_TOKENS", envOr("ANTHROPIC_MAX_TOKENS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.googleai")
	kv(p, "GOOGLEAI_API_KEY", presence(os.Getenv("GOOGLEAI_API_KEY")))
	kv(p, "GOOGLEAI_MODEL", envOr("GOOGLEAI_MODEL"))
	kv(p, "GOOGLEAI_MAX_TOKENS", envOr("GOOGLEAI_MAX_TOKENS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.xai")
	kv(p, "XAI_API_KEY", presence(os.Getenv("XAI_API_KEY")))
	kv(p, "XAI_MODEL", envOr("XAI_MODEL"))
	kv(p, "XAI_MAX_TOKENS", envOr("XAI_MAX_TOKENS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.ollama")
	kv(p, "OLLAMA_MODEL", envOr("OLLAMA_MODEL"))
	kv(p, "OLLAMA_BASE_URL", envOr("OLLAMA_BASE_URL"))
	kv(p, "OLLAMA_MAX_TOKENS", envOr("OLLAMA_MAX_TOKENS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.bedrock")
	kv(p, "BEDROCK_REGION", envOr("BEDROCK_REGION"))
	kv(p, "AWS_REGION", envOr("AWS_REGION"))
	kv(p, "AWS_PROFILE", envOr("AWS_PROFILE"))
	kv(p, "BEDROCK_PROVIDER", envOr("BEDROCK_PROVIDER"))
	kv(p, "BEDROCK_MAX_TOKENS", envOr("BEDROCK_MAX_TOKENS"))
	kv(p, "BEDROCK_TEMPERATURE", envOr("BEDROCK_TEMPERATURE"))
	kv(p, "BEDROCK_TOP_P", envOr("BEDROCK_TOP_P"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.copilot")
	kv(p, "COPILOT_MODEL", envOr("COPILOT_MODEL"))
	kv(p, "COPILOT_API_BASE_URL", envOr("COPILOT_API_BASE_URL"))
	kv(p, "COPILOT_MAX_TOKENS", envOr("COPILOT_MAX_TOKENS"))
	kv(p, "GITHUB_COPILOT_TOKEN", presence(os.Getenv("GITHUB_COPILOT_TOKEN")))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.github_models")
	kv(p, "GITHUB_MODELS_MODEL", envOr("GITHUB_MODELS_MODEL"))
	kv(p, "GITHUB_MODELS_MAX_TOKENS", envOr("GITHUB_MODELS_MAX_TOKENS"))
	kv(p, "GITHUB_MODELS_TOKEN", presence(os.Getenv("GITHUB_MODELS_TOKEN")))
	kv(p, "GITHUB_TOKEN", presence(os.Getenv("GITHUB_TOKEN")))
	kv(p, "GH_TOKEN", presence(os.Getenv("GH_TOKEN")))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.openrouter")
	kv(p, "OPENROUTER_API_KEY", presence(os.Getenv("OPENROUTER_API_KEY")))
	kv(p, "OPENROUTER_MAX_TOKENS", envOr("OPENROUTER_MAX_TOKENS"))
	kv(p, "OPENROUTER_FALLBACK_MODELS", envOr("OPENROUTER_FALLBACK_MODELS"))
	kv(p, "OPENROUTER_PROVIDER_ORDER", envOr("OPENROUTER_PROVIDER_ORDER"))
	kv(p, "OPENROUTER_TRANSFORMS", envOr("OPENROUTER_TRANSFORMS"))
	kv(p, "OPENROUTER_TOOLS", envOr("OPENROUTER_TOOLS"))
	kv(p, "OPENROUTER_HTTP_REFERER", envOr("OPENROUTER_HTTP_REFERER"))
	kv(p, "OPENROUTER_APP_TITLE", envOr("OPENROUTER_APP_TITLE"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.zai")
	kv(p, "ZAI_API_KEY", presence(os.Getenv("ZAI_API_KEY")))
	kv(p, "ZAI_MODEL", envOr("ZAI_MODEL"))
	kv(p, "ZAI_MAX_TOKENS", envOr("ZAI_MAX_TOKENS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.minimax")
	kv(p, "MINIMAX_API_KEY", presence(os.Getenv("MINIMAX_API_KEY")))
	kv(p, "MINIMAX_MODEL", envOr("MINIMAX_MODEL"))
	kv(p, "MINIMAX_API_COMPAT", envOr("MINIMAX_API_COMPAT"))
	kv(p, "MINIMAX_MAX_TOKENS", envOr("MINIMAX_MAX_TOKENS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.moonshot")
	kv(p, "MOONSHOT_API_KEY", presence(os.Getenv("MOONSHOT_API_KEY")))
	kv(p, "MOONSHOT_MODEL", envOr("MOONSHOT_MODEL"))
	kv(p, "MOONSHOT_MAX_TOKENS", envOr("MOONSHOT_MAX_TOKENS"))
	kv(p, "MOONSHOT_THINKING", envOr("MOONSHOT_THINKING"))
	kv(p, "MOONSHOT_API_URL", envOr("MOONSHOT_API_URL"))

	fmt.Println(p)
	subheader(p, "cfg.sub.prov.stackspot")
	kv(p, "CLIENT_ID", presence(os.Getenv("CLIENT_ID")))
	kv(p, "CLIENT_KEY", presence(os.Getenv("CLIENT_KEY")))
	kv(p, "STACKSPOT_MAX_TOKENS", envOr("STACKSPOT_MAX_TOKENS"))
	if cli.manager != nil {
		kv(p, i18n.T("cfg.kv.realm_runtime"), cli.manager.GetStackSpotRealm())
		kv(p, i18n.T("cfg.kv.agent_id_runtime"), cli.manager.GetStackSpotAgentID())
	} else {
		kv(p, i18n.T("cfg.kv.realm_runtime"), i18n.T("cfg.msg.manager_not_init"))
	}

	sectionEnd(ColorPurple)
}

// ─── Section: AGENT ────────────────────────────────────────────

// showConfigAgent renders agent runtime knobs — concurrency, timeouts,
// subagent depth, workspace sandbox — plus the security-related envs that
// live inline with the agent executor. Split from /config security
// intentionally: this block is about *behavior*, the security block is
// about *policy*.
func (cli *ChatCLI) showConfigAgent() {
	sectionHeader("🤖", "cfg.section.agent.title", ColorLime)
	p := uiPrefix(ColorLime)

	// UI style — now drives both /coder and /agent timelines. Resolved
	// through agent.DefaultUIStyleFromEnv so the same parsing rules apply
	// everywhere (legacy name "CHATCLI_CODER_UI" retained for back-compat
	// after the cross-mode unification).
	coderUIEffective := agent.DefaultUIStyleFromEnv().String()
	coderBanner := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_BANNER")))
	coderBannerEffective := i18n.T("cfg.val.on")
	if coderBanner == "false" || coderBanner == "0" || coderBanner == "no" {
		coderBannerEffective = i18n.T("cfg.val.off")
	}
	kv(p, "CHATCLI_CODER_UI", envOr("CHATCLI_CODER_UI"))
	kv(p, "CHATCLI_CODER_UI (effective)", coderUIEffective+" — applies to /coder AND /agent")
	kv(p, "CHATCLI_CODER_BANNER", envOr("CHATCLI_CODER_BANNER"))
	kv(p, "CHATCLI_CODER_BANNER (effective)", coderBannerEffective)

	fmt.Println(p)
	subheader(p, "cfg.sub.agent.parallelism")
	kv(p, "CHATCLI_AGENT_PARALLEL_MODE", envOr("CHATCLI_AGENT_PARALLEL_MODE"))
	kv(p, "CHATCLI_AGENT_MAX_WORKERS", envOr("CHATCLI_AGENT_MAX_WORKERS"))
	kv(p, "CHATCLI_AGENT_WORKER_TIMEOUT", envOr("CHATCLI_AGENT_WORKER_TIMEOUT"))
	kv(p, "CHATCLI_AGENT_WORKER_MAX_TURNS", envOr("CHATCLI_AGENT_WORKER_MAX_TURNS"))
	kv(p, "CHATCLI_AGENT_SUBAGENT_MAX_DEPTH", envOr("CHATCLI_AGENT_SUBAGENT_MAX_DEPTH"))
	kv(p, "CHATCLI_AGENT_SUBAGENT_MAX_TURNS", envOr("CHATCLI_AGENT_SUBAGENT_MAX_TURNS"))
	kv(p, "CHATCLI_AGENT_PARALLEL_TOOLS", envBool("CHATCLI_AGENT_PARALLEL_TOOLS"))
	kv(p, "CHATCLI_AGENT_MAX_TOOL_CONCURRENCY", envOr("CHATCLI_AGENT_MAX_TOOL_CONCURRENCY"))
	kv(p, "CHATCLI_AGENT_INLINE_CODE_STRICT", envBool("CHATCLI_AGENT_INLINE_CODE_STRICT"))

	fmt.Println(p)
	subheader(p, "cfg.sub.agent.token_efficiency")
	kv(p, "CHATCLI_AGENT_EARLY_EXIT", envOr("CHATCLI_AGENT_EARLY_EXIT"))
	kv(p, "CHATCLI_AGENT_EARLY_EXIT_TURNS", envOr("CHATCLI_AGENT_EARLY_EXIT_TURNS"))
	kv(p, "CHATCLI_AGENT_SMART_ROUTE", envOr("CHATCLI_AGENT_SMART_ROUTE"))

	fmt.Println(p)
	subheader(p, "cfg.sub.agent.execution")
	kv(p, "CHATCLI_AGENT_CMD_TIMEOUT", envOr("CHATCLI_AGENT_CMD_TIMEOUT"))
	kv(p, "CHATCLI_AGENT_SOURCE_SHELL_CONFIG", envBool("CHATCLI_AGENT_SOURCE_SHELL_CONFIG"))
	kv(p, "CHATCLI_AGENT_TMPDIR", envOr("CHATCLI_AGENT_TMPDIR"))
	kv(p, "CHATCLI_AGENT_KEEP_TMPDIR", envBool("CHATCLI_AGENT_KEEP_TMPDIR"))
	kv(p, "CHATCLI_MAX_COMMAND_OUTPUT", envOr("CHATCLI_MAX_COMMAND_OUTPUT"))

	fmt.Println(p)
	subheader(p, "cfg.sub.agent.coder_denial")
	kv(p, "CHATCLI_MAX_CONSECUTIVE_DENIALS", envOr("CHATCLI_MAX_CONSECUTIVE_DENIALS"))
	kv(p, "CHATCLI_MAX_TOTAL_DENIALS", envOr("CHATCLI_MAX_TOTAL_DENIALS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.agent.per_agent")
	perAgent := collectPerAgentOverrides()
	if len(perAgent) == 0 {
		kv(p, "CHATCLI_AGENT_<NAME>_MODEL", i18n.T("cfg.val.none_set"))
		kv(p, "CHATCLI_AGENT_<NAME>_EFFORT", i18n.T("cfg.val.none_set"))
	} else {
		for _, line := range perAgent {
			kv(p, line.key, line.val)
		}
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.agent.persona")
	if cli.personaHandler != nil && cli.personaHandler.GetManager() != nil {
		active := cli.personaHandler.GetManager().GetActiveAgent()
		if active != nil {
			kv(p, i18n.T("cfg.kv.name"), active.Name)
			if active.Description != "" {
				kv(p, i18n.T("cfg.kv.description"), shorten(active.Description, 80))
			}
			attached := cli.personaHandler.GetManager().GetActiveAgents()
			kv(p, i18n.T("cfg.kv.attached_count"), fmt.Sprintf("%d", len(attached)))
		} else {
			kv(p, i18n.T("cfg.kv.active_persona"), i18n.T("cfg.msg.no_persona"))
		}
	} else {
		kv(p, i18n.T("cfg.kv.persona_system"), i18n.T("cfg.val.not_initialized"))
	}

	sectionEnd(ColorLime)
}

// perAgentEntry is a captured CHATCLI_AGENT_<NAME>_<SUFFIX>=value pair.
type perAgentEntry struct{ key, val string }

// collectPerAgentOverrides scans os.Environ() for the per-agent override
// pattern and returns the entries sorted for stable display.
func collectPerAgentOverrides() []perAgentEntry {
	var out []perAgentEntry
	for _, e := range os.Environ() {
		eq := strings.IndexByte(e, '=')
		if eq < 0 {
			continue
		}
		k, v := e[:eq], e[eq+1:]
		if !strings.HasPrefix(k, "CHATCLI_AGENT_") {
			continue
		}
		if !(strings.HasSuffix(k, "_MODEL") || strings.HasSuffix(k, "_EFFORT")) {
			continue
		}
		// Exclude the well-known, non-per-agent suffixes captured elsewhere.
		switch k {
		case "CHATCLI_AGENT_MODEL", "CHATCLI_AGENT_EFFORT":
			continue
		}
		out = append(out, perAgentEntry{key: k, val: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// ─── Section: RESILIENCE ───────────────────────────────────────

// showConfigResilience covers everything that matters when a request fails
// or gets truncated: payload caps, recovery ladders, stream watchdog, and
// the Bedrock corporate-proxy knobs.
func (cli *ChatCLI) showConfigResilience() {
	sectionHeader("🛡", "cfg.section.resilience.title", ColorYellow)
	p := uiPrefix(ColorYellow)

	subheader(p, "cfg.sub.resil.payload")
	kv(p, "CHATCLI_MAX_PAYLOAD", envOr("CHATCLI_MAX_PAYLOAD"))
	kv(p, "CHATCLI_MAX_RECOVERY_ATTEMPTS", envOr("CHATCLI_MAX_RECOVERY_ATTEMPTS"))
	kv(p, "CHATCLI_MAX_TOKEN_ESCALATIONS", envOr("CHATCLI_MAX_TOKEN_ESCALATIONS"))
	kv(p, "CHATCLI_EMERGENCY_KEEP_MESSAGES", envOr("CHATCLI_EMERGENCY_KEEP_MESSAGES"))

	fmt.Println(p)
	subheader(p, "cfg.sub.resil.streaming")
	kv(p, "CHATCLI_STREAM_IDLE_TIMEOUT_SECONDS", envOr("CHATCLI_STREAM_IDLE_TIMEOUT_SECONDS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.resil.compaction")
	kv(p, "CHATCLI_MICROCOMPACT_TRUNCATE_TURNS", envOr("CHATCLI_MICROCOMPACT_TRUNCATE_TURNS"))
	kv(p, "CHATCLI_MICROCOMPACT_SUMMARIZE_TURNS", envOr("CHATCLI_MICROCOMPACT_SUMMARIZE_TURNS"))
	kv(p, "CHATCLI_MICROCOMPACT_HEAD_CHARS", envOr("CHATCLI_MICROCOMPACT_HEAD_CHARS"))
	kv(p, "CHATCLI_MICROCOMPACT_TAIL_CHARS", envOr("CHATCLI_MICROCOMPACT_TAIL_CHARS"))
	kv(p, "CHATCLI_MICROCOMPACT_MIN_CONTENT", envOr("CHATCLI_MICROCOMPACT_MIN_CONTENT"))
	kv(p, "CHATCLI_TOOL_RESULT_BUDGET_CHARS", envOr("CHATCLI_TOOL_RESULT_BUDGET_CHARS"))
	kv(p, "CHATCLI_TOOL_RESULT_MAX_CHARS", envOr("CHATCLI_TOOL_RESULT_MAX_CHARS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.resil.bedrock_proxy")
	kv(p, "CHATCLI_BEDROCK_CA_BUNDLE", envOr("CHATCLI_BEDROCK_CA_BUNDLE"))
	kv(p, "CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", envBool("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY"))
	kv(p, "CHATCLI_BEDROCK_ENABLE_IMDS", envBool("CHATCLI_BEDROCK_ENABLE_IMDS"))
	kv(p, "AWS_EC2_METADATA_DISABLED", envOr("AWS_EC2_METADATA_DISABLED"))

	fmt.Println(p)
	subheader(p, "cfg.sub.resil.web_proxy")
	// Standard proxy vars. A login:senha embedded in the URL is honored and
	// parsed tolerantly (domain users / special-char passwords included), so we
	// mask the value — it carries a secret.
	kv(p, "HTTPS_PROXY", presence(firstNonEmptyEnvVal("HTTPS_PROXY", "https_proxy")))
	kv(p, "HTTP_PROXY", presence(firstNonEmptyEnvVal("HTTP_PROXY", "http_proxy")))
	kv(p, "NO_PROXY", envOr("NO_PROXY"))
	// Escape hatch for non-Basic proxy auth (Negotiate/NTLM/Kerberos/Bearer).
	kv(p, "CHATCLI_PROXY_AUTH", presence(os.Getenv("CHATCLI_PROXY_AUTH")))
	// SSRF guard: metadata/link-local is always blocked; private/loopback only
	// when this is enabled (web tools legitimately fetch internal services).
	kv(p, "CHATCLI_WEBFETCH_BLOCK_PRIVATE", envBool("CHATCLI_WEBFETCH_BLOCK_PRIVATE"))

	fmt.Println(p)
	subheader(p, "cfg.sub.resil.session_state")
	kv(p, i18n.T("cfg.kv.history_messages"), fmt.Sprintf("%d", len(cli.history)))
	historyChars := 0
	for _, m := range cli.history {
		historyChars += len(m.Content)
	}
	kv(p, i18n.T("cfg.kv.history_chars"), fmt.Sprintf("%d", historyChars))
	if cli.proxyPayloadWarned {
		kv(p, i18n.T("cfg.kv.proxy_warning"), i18n.T("cfg.val.already_warned"))
	}

	sectionEnd(ColorYellow)
}

// ─── Section: SESSION ──────────────────────────────────────────

// showConfigSession renders session identity, cost, budget, attached
// contexts, and memory state — everything scoped to the running session.
func (cli *ChatCLI) showConfigSession() {
	sectionHeader("📋", "cfg.section.session.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	// Identity
	name := cli.currentSessionName
	if name == "" {
		name = i18n.T("cfg.val.unnamed")
	}
	kv(p, i18n.T("cfg.kv.name"), name)
	kv(p, i18n.T("cfg.kv.messages"), fmt.Sprintf("%d", len(cli.history)))
	if !cli.sessionStartTime.IsZero() {
		kv(p, i18n.T("cfg.kv.started"), cli.sessionStartTime.Format(time.RFC3339))
		kv(p, i18n.T("cfg.kv.duration"), humanDuration(time.Since(cli.sessionStartTime)))
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.session.cost")
	if cli.costTracker != nil {
		kv(p, i18n.T("cfg.kv.total_cost_usd"), fmt.Sprintf("$%.4f", cli.costTracker.TotalCost()))
		kv(p, i18n.T("cfg.kv.total_tokens"), fmt.Sprintf("%d", cli.costTracker.TotalTokens()))
		kv(p, i18n.T("cfg.kv.budget_status"), budgetLevelString(cli.costTracker.CheckBudget()))
	} else {
		kv(p, i18n.T("cfg.kv.cost_tracker"), i18n.T("cfg.val.not_initialized"))
	}
	kv(p, "CHATCLI_SESSION_BUDGET_USD", envOr("CHATCLI_SESSION_BUDGET_USD"))
	kv(p, "CHATCLI_BUDGET_WARNING_PCT", envOr("CHATCLI_BUDGET_WARNING_PCT"))
	kv(p, "CHATCLI_SESSION_TTL", envOr("CHATCLI_SESSION_TTL"))
	kv(p, "CHATCLI_DISABLE_HISTORY", envBool("CHATCLI_DISABLE_HISTORY"))

	fmt.Println(p)
	subheader(p, "cfg.sub.session.attached")
	attachedCount := 0
	if cli.contextHandler != nil {
		sessionID := cli.currentSessionName
		if sessionID == "" {
			sessionID = "default"
		}
		if mgr := cli.contextHandler.GetManager(); mgr != nil {
			if ctxs, err := mgr.GetAttachedContexts(sessionID); err == nil {
				attachedCount = len(ctxs)
				for _, c := range ctxs {
					desc := fmt.Sprintf("mode=%s, %d files, %.1f KB",
						c.Mode, c.FileCount, float64(c.TotalSize)/1024.0)
					kv(p, "· "+c.Name, desc)
				}
			}
		}
	}
	if attachedCount == 0 {
		kv(p, i18n.T("cfg.kv.attached"), i18n.T("cfg.msg.no_contexts_attached"))
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.session.memory")
	kv(p, "CHATCLI_MEMORY_ENABLED", envBool("CHATCLI_MEMORY_ENABLED"))
	kv(p, "CHATCLI_MEMORY_MODE", envOr("CHATCLI_MEMORY_MODE"))
	kv(p, "CHATCLI_BOOTSTRAP_ENABLED", envBool("CHATCLI_BOOTSTRAP_ENABLED"))
	kv(p, "CHATCLI_BOOTSTRAP_DIR", envOr("CHATCLI_BOOTSTRAP_DIR"))
	if cli.memoryStore != nil {
		notes := cli.memoryStore.GetRecentDailyNotes(365)
		kv(p, i18n.T("cfg.kv.daily_notes_on_disk"), fmt.Sprintf("%d", len(notes)))
		kv(p, i18n.T("cfg.kv.today_note_path"), shorten(cli.memoryStore.TodayNotePath(), 80))
		if longTerm := cli.memoryStore.ReadLongTerm(); longTerm != "" {
			kv(p, i18n.T("cfg.kv.long_term_size"), fmt.Sprintf("%d bytes", len(longTerm)))
		} else {
			kv(p, i18n.T("cfg.kv.long_term_memory"), i18n.T("cfg.val.empty"))
		}
	} else {
		kv(p, i18n.T("cfg.kv.memory_store"), i18n.T("cfg.val.not_initialized"))
	}

	sectionEnd(ColorBlue)
}

// budgetLevelString turns a BudgetLevel into a localized label.
func budgetLevelString(l BudgetLevel) string {
	switch l {
	case BudgetOK:
		return i18n.T("cfg.val.budget_ok")
	case BudgetWarning:
		return i18n.T("cfg.val.budget_warning")
	case BudgetExceeded:
		return i18n.T("cfg.val.budget_exceeded")
	default:
		return i18n.T("cfg.val.unknown")
	}
}

// ─── Section: INTEGRATIONS ─────────────────────────────────────

// showConfigIntegrations covers MCP, hooks, plugins, skill registries,
// websearch, worktrees, and remote connection — everything that links the
// CLI to an external subsystem.
func (cli *ChatCLI) showConfigIntegrations() {
	sectionHeader("🔗", "cfg.section.integrations.title", ColorPurple)
	p := uiPrefix(ColorPurple)

	subheader(p, "cfg.sub.integ.mcp")
	kv(p, "CHATCLI_MCP_ENABLED", envBool("CHATCLI_MCP_ENABLED"))
	kv(p, "CHATCLI_MCP_CONFIG", envOr("CHATCLI_MCP_CONFIG"))
	if cli.mcpManager != nil {
		statuses := cli.mcpManager.GetServerStatus()
		kv(p, i18n.T("cfg.kv.servers"), fmt.Sprintf("%d", len(statuses)))
		for _, s := range statuses {
			status := i18n.T("cfg.msg.disconnected")
			if s.Connected {
				status = i18n.T("cfg.msg.connected_tools", s.ToolCount)
			}
			if s.LastError != nil {
				status += i18n.T("cfg.msg.last_error", shorten(s.LastError.Error(), 50))
			}
			kv(p, "· "+s.Name, status)
		}
		kv(p, i18n.T("cfg.kv.total_tools"), fmt.Sprintf("%d", cli.mcpManager.ToolCount()))
		if shadowed := cli.mcpManager.GetShadowedBuiltins(); len(shadowed) > 0 {
			kv(p, i18n.T("cfg.kv.shadowed_builtins"), strings.Join(shadowed, ", "))
		}
	} else {
		kv(p, i18n.T("cfg.kv.mcp_manager"), i18n.T("cfg.val.not_initialized"))
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.integ.hooks")
	if cli.hookManager != nil {
		kv(p, i18n.T("cfg.kv.configured"), fmt.Sprintf("%d", cli.hookManager.Count()))
		for _, h := range cli.hookManager.GetHooks() {
			label := fmt.Sprintf("event=%s, type=%s", h.Event, h.Type)
			name := h.Name
			if name == "" {
				name = fmt.Sprintf("[hook:%s]", h.Event)
			}
			kv(p, "· "+name, label)
		}
	} else {
		kv(p, i18n.T("cfg.kv.hook_manager"), i18n.T("cfg.val.not_initialized"))
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.integ.plugins")
	if cli.pluginManager != nil {
		plist := cli.pluginManager.GetPlugins()
		kv(p, i18n.T("cfg.kv.loaded"), fmt.Sprintf("%d", len(plist)))
		for _, pl := range plist {
			kv(p, "· "+pl.Name(), fmt.Sprintf("v%s — %s", pl.Version(), shorten(pl.Description(), 50)))
		}
		kv(p, "CHATCLI_ALLOW_UNSIGNED_PLUGINS", envBool("CHATCLI_ALLOW_UNSIGNED_PLUGINS"))
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.integ.skills")
	kv(p, "CHATCLI_REGISTRY_DISABLE", envBool("CHATCLI_REGISTRY_DISABLE"))
	kv(p, "CHATCLI_REGISTRY_URLS", envOr("CHATCLI_REGISTRY_URLS"))
	kv(p, "CHATCLI_SKILL_INSTALL_DIR", envOr("CHATCLI_SKILL_INSTALL_DIR"))
	if cli.skillHandler != nil && cli.skillHandler.registryMgr != nil {
		regs := cli.skillHandler.registryMgr.GetRegistries()
		kv(p, i18n.T("cfg.kv.registries"), fmt.Sprintf("%d", len(regs)))
		for _, r := range regs {
			state := i18n.T("cfg.val.enabled")
			if !r.Enabled {
				state = i18n.T("cfg.val.disabled")
			}
			kv(p, "· "+r.Name, fmt.Sprintf("%s, %s", state, shorten(r.URL, 60)))
		}
		if installed, err := cli.skillHandler.registryMgr.ListInstalled(); err == nil {
			kv(p, i18n.T("cfg.kv.skills_installed"), fmt.Sprintf("%d", len(installed)))
		}
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.integ.websearch")
	kv(p, "CHATCLI_WEBSEARCH_PROVIDER", envOr("CHATCLI_WEBSEARCH_PROVIDER"))
	kv(p, "SEARXNG_URL", envOr("SEARXNG_URL"))
	kv(p, "CHATCLI_WEBFETCH_AUTOSAVE_BYTES", envOr("CHATCLI_WEBFETCH_AUTOSAVE_BYTES"))
	kv(p, "CHATCLI_WEBFETCH_USER_AGENT", envOr("CHATCLI_WEBFETCH_USER_AGENT"))
	chain := plugins.SelectSearchChainNames()
	chainNames := make([]string, 0, len(chain))
	for _, n := range chain {
		chainNames = append(chainNames, string(n))
	}
	kv(p, i18n.T("cfg.kv.active_chain"), strings.Join(chainNames, " → "))

	fmt.Println(p)
	subheader(p, "cfg.sub.integ.gateway_voice")
	voiceStatus := i18n.T("cfg.val.transcription_off")
	if t := transcription.NewFromEnv(cli.logger); !transcription.IsNull(t) {
		voiceStatus = t.Name()
	}
	kv(p, i18n.T("cfg.kv.transcription"), voiceStatus)
	kv(p, "CHATCLI_TRANSCRIPTION_CMD", envOr("CHATCLI_TRANSCRIPTION_CMD"))
	kv(p, "CHATCLI_TRANSCRIPTION_URL", envOr("CHATCLI_TRANSCRIPTION_URL"))
	kv(p, "CHATCLI_TRANSCRIPTION_PROVIDER", envOr("CHATCLI_TRANSCRIPTION_PROVIDER"))
	kv(p, "CHATCLI_TRANSCRIPTION_MODEL", envOr("CHATCLI_TRANSCRIPTION_MODEL"))
	kv(p, "CHATCLI_GATEWAY_MAX_AUDIO_BYTES", envOr("CHATCLI_GATEWAY_MAX_AUDIO_BYTES"))

	fmt.Println(p)
	subheader(p, "cfg.sub.integ.gateway_tts")
	ttsStatus := i18n.T("cfg.val.tts_off")
	if s := tts.NewFromEnv(cli.logger); !tts.IsNull(s) {
		ttsStatus = s.Name()
	}
	kv(p, i18n.T("cfg.kv.tts"), ttsStatus)
	kv(p, "CHATCLI_TTS_PROVIDER", envOr("CHATCLI_TTS_PROVIDER"))
	kv(p, "CHATCLI_TTS_VOICE", envOr("CHATCLI_TTS_VOICE"))
	kv(p, "CHATCLI_TTS_VOICE_PT", envOr("CHATCLI_TTS_VOICE_PT"))
	kv(p, "CHATCLI_GATEWAY_VOICE_REPLY", envOr("CHATCLI_GATEWAY_VOICE_REPLY"))

	fmt.Println(p)
	subheader(p, "cfg.sub.integ.imagegen")
	imgStatus := i18n.T("cfg.val.imagegen_off")
	if g := imagegen.NewFromEnv(cli.logger); !imagegen.IsNull(g) {
		imgStatus = g.Name()
	}
	kv(p, i18n.T("cfg.kv.imagegen"), imgStatus)
	kv(p, "CHATCLI_IMAGE_PROVIDER", envOr("CHATCLI_IMAGE_PROVIDER"))
	kv(p, "CHATCLI_IMAGE_API", envOr("CHATCLI_IMAGE_API"))
	kv(p, "CHATCLI_IMAGE_MODEL", envOr("CHATCLI_IMAGE_MODEL"))
	kv(p, "CHATCLI_IMAGE_URL", envOr("CHATCLI_IMAGE_URL"))

	fmt.Println(p)
	subheader(p, "cfg.sub.integ.gateway_send")
	if built, err := gateway.BuildConfigured(); err == nil && len(built) > 0 {
		names := make([]string, 0, len(built))
		for _, ad := range built {
			names = append(names, ad.Name())
		}
		sort.Strings(names)
		kv(p, i18n.T("cfg.kv.send_platforms"), strings.Join(names, ", "))
		for _, n := range names {
			env := "CHATCLI_" + strings.ToUpper(n) + "_HOME_CHANNEL"
			kv(p, env, envOr(env))
		}
	} else {
		kv(p, i18n.T("cfg.kv.send_platforms"), i18n.T("send.tool.list.empty"))
	}

	if isGitRepo() {
		fmt.Println(p)
		subheader(p, "cfg.sub.integ.worktrees")
		kv(p, i18n.T("cfg.kv.repo_root"), getGitRepoRoot())
		kv(p, i18n.T("cfg.kv.current_branch"), getCurrentBranch())
	}

	if cli.isWatching {
		fmt.Println(p)
		subheader(p, "cfg.sub.integ.watcher")
		kv(p, i18n.T("cfg.kv.active"), i18n.T("cfg.val.yes"))
		if cli.watchStatusFunc != nil {
			kv(p, i18n.T("cfg.kv.status"), cli.watchStatusFunc())
		}
	}

	if cli.isRemote {
		fmt.Println(p)
		subheader(p, "cfg.sub.integ.remote")
		kv(p, i18n.T("cfg.kv.address"), cli.remoteAddress)
		kv(p, i18n.T("cfg.kv.provider_remote"), cli.Provider)
		kv(p, i18n.T("cfg.kv.model_remote"), cli.Model)
		kv(p, i18n.T("cfg.kv.local_provider_saved"), cli.localProvider)
		kv(p, i18n.T("cfg.kv.local_model_saved"), cli.localModel)
	}

	sectionEnd(ColorPurple)
}

// ─── Section: AUTH ─────────────────────────────────────────────

// showConfigAuth enumerates OAuth profile state from the encrypted store so
// the user can see at a glance which providers are authenticated and which
// tokens are expired — without leaking any secret material.
func (cli *ChatCLI) showConfigAuth() {
	sectionHeader("🔑", "cfg.section.auth.title", ColorGreen)
	p := uiPrefix(ColorGreen)

	kv(p, "CHATCLI_AUTH_DIR", envOr("CHATCLI_AUTH_DIR"))
	kv(p, "CHATCLI_KEYCHAIN_BACKEND", envOr("CHATCLI_KEYCHAIN_BACKEND"))
	kv(p, "CHATCLI_ENCRYPTION_KEY", presence(os.Getenv("CHATCLI_ENCRYPTION_KEY")))
	kv(p, "CHATCLI_COPILOT_CLIENT_ID", presence(os.Getenv("CHATCLI_COPILOT_CLIENT_ID")))
	kv(p, "CHATCLI_OPENAI_CLIENT_ID", presence(os.Getenv("CHATCLI_OPENAI_CLIENT_ID")))

	fmt.Println(p)
	subheader(p, "cfg.sub.auth.oauth")

	providers := []auth.ProviderID{
		"anthropic",
		"openai-codex",
		"github-copilot",
		"github-models",
	}

	any := false
	for _, pid := range providers {
		profileIDs := auth.ListProfilesForProvider(pid, cli.logger)
		if len(profileIDs) == 0 {
			kv(p, string(pid), i18n.T("cfg.msg.not_logged_in"))
			continue
		}
		any = true
		for _, id := range profileIDs {
			cred := auth.GetProfile(id, cli.logger)
			if cred == nil {
				kv(p, string(pid), i18n.T("cfg.msg.profile_load_error", id))
				continue
			}
			status := i18n.T("cfg.val.valid")
			if cred.IsExpired() {
				status = i18n.T("cfg.val.expired")
			}
			if cred.Expires > 0 {
				expiresAt := time.UnixMilli(cred.Expires)
				remain := time.Until(expiresAt)
				if remain > 0 {
					status += " " + i18n.T("cfg.val.expires_in", humanDuration(remain))
				} else {
					status += " " + i18n.T("cfg.val.expired_ago", humanDuration(-remain))
				}
			}
			if cred.Email != "" {
				status += fmt.Sprintf(" · %s", cred.Email)
			}
			kv(p, fmt.Sprintf("%s [%s]", string(pid), id), status)
		}
	}
	if !any {
		fmt.Println(p + colorize("  "+i18n.T("cfg.msg.auth_hint"), ColorGray))
	}

	sectionEnd(ColorGreen)
}

// ─── Section: SECURITY ─────────────────────────────────────────

// showConfigSecurity covers policy/sandbox — distinct from /config agent
// which is about behavior. Grouped here: command allow/deny, workspace
// path validation, TLS posture, SSRF toggle, redaction rules, plugin
// signature enforcement.
func (cli *ChatCLI) showConfigSecurity() {
	sectionHeader("🔒", "cfg.section.security.title", ColorRed)
	p := uiPrefix(ColorRed)

	subheader(p, "cfg.sub.sec.cmd_policy")
	kv(p, "CHATCLI_AGENT_SECURITY_MODE", envOr("CHATCLI_AGENT_SECURITY_MODE"))
	kv(p, "CHATCLI_AGENT_ALLOWLIST", envOr("CHATCLI_AGENT_ALLOWLIST"))
	kv(p, "CHATCLI_AGENT_DENYLIST", envOr("CHATCLI_AGENT_DENYLIST"))
	kv(p, "CHATCLI_AGENT_ALLOW_SUDO", envBool("CHATCLI_AGENT_ALLOW_SUDO"))

	fmt.Println(p)
	subheader(p, "cfg.sub.sec.workspace")
	kv(p, "CHATCLI_AGENT_WORKSPACE_STRICT", envBool("CHATCLI_AGENT_WORKSPACE_STRICT"))
	kv(p, "CHATCLI_AGENT_ALLOW_KUBECONFIG", envBool("CHATCLI_AGENT_ALLOW_KUBECONFIG"))
	kv(p, "CHATCLI_AGENT_EXTRA_READ_PATHS", envOr("CHATCLI_AGENT_EXTRA_READ_PATHS"))
	kv(p, "CHATCLI_BLOCK_TMP_WRITES", envBool("CHATCLI_BLOCK_TMP_WRITES"))
	kv(p, "CHATCLI_IGNORE", envOr("CHATCLI_IGNORE"))

	fmt.Println(p)
	subheader(p, "cfg.sub.sec.coder_policy")
	cli.renderCoderPolicy(p)

	fmt.Println(p)
	subheader(p, "cfg.sub.sec.tls")
	kv(p, "CHATCLI_ALLOW_HTTP_PROVIDERS", envBool("CHATCLI_ALLOW_HTTP_PROVIDERS"))
	kv(p, "CHATCLI_ALLOW_INSECURE", envBool("CHATCLI_ALLOW_INSECURE"))
	kv(p, "CHATCLI_TLS_CLIENT_CERT", envOr("CHATCLI_TLS_CLIENT_CERT"))
	kv(p, "CHATCLI_TLS_CLIENT_KEY", presence(os.Getenv("CHATCLI_TLS_CLIENT_KEY")))

	fmt.Println(p)
	subheader(p, "cfg.sub.sec.redaction")
	kv(p, "CHATCLI_ENV_REDACT_MODE", envOr("CHATCLI_ENV_REDACT_MODE"))
	kv(p, "CHATCLI_REDACT_PATTERNS", envOr("CHATCLI_REDACT_PATTERNS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.sec.plugins")
	kv(p, "CHATCLI_ALLOW_UNSIGNED_PLUGINS", envBool("CHATCLI_ALLOW_UNSIGNED_PLUGINS"))

	sectionEnd(ColorRed)
}

// ─── Section: SERVER (conditional) ─────────────────────────────

// showConfigServer is conditional — only meaningful when the process was
// launched with server/operator env. If none of the server-mode envs are
// set we short-circuit with a hint, so the block doesn't confuse CLI users.
func (cli *ChatCLI) showConfigServer() {
	anySet := false
	serverVars := []string{
		"CHATCLI_SERVER_TOKEN", "CHATCLI_SERVER_TLS_CERT", "CHATCLI_SERVER_TLS_KEY",
		"CHATCLI_BIND_ADDRESS", "CHATCLI_GRPC_REFLECTION",
		"CHATCLI_JWT_SECRET", "CHATCLI_JWT_ISSUER", "CHATCLI_JWT_AUDIENCE",
		"CHATCLI_RATE_LIMIT_RPS", "CHATCLI_RATE_LIMIT_BURST",
		"CHATCLI_FALLBACK_PROVIDERS", "CHATCLI_FALLBACK_ENABLED",
		"CHATCLI_AUDIT_LOG_PATH",
		"CHATCLI_WATCH_DEPLOYMENT", "CHATCLI_WATCH_NAMESPACE",
		"CHATCLI_AIOPS_PORT",
	}
	for _, v := range serverVars {
		if os.Getenv(v) != "" {
			anySet = true
			break
		}
	}

	sectionHeader("🖥", "cfg.section.server.title", ColorGray)
	p := uiPrefix(ColorGray)
	if !anySet {
		fmt.Println(p + colorize(i18n.T("cfg.msg.skip_server"), ColorGray))
		fmt.Println(p + colorize(i18n.T("cfg.msg.server_hint"), ColorGray))
		sectionEnd(ColorGray)
		return
	}

	subheader(p, "cfg.sub.server.grpc")
	kv(p, "CHATCLI_BIND_ADDRESS", envOr("CHATCLI_BIND_ADDRESS"))
	kv(p, "CHATCLI_GRPC_REFLECTION", envBool("CHATCLI_GRPC_REFLECTION"))
	kv(p, "CHATCLI_SERVER_TOKEN", presence(os.Getenv("CHATCLI_SERVER_TOKEN")))
	kv(p, "CHATCLI_SERVER_TLS_CERT", envOr("CHATCLI_SERVER_TLS_CERT"))
	kv(p, "CHATCLI_SERVER_TLS_KEY", presence(os.Getenv("CHATCLI_SERVER_TLS_KEY")))

	fmt.Println(p)
	subheader(p, "cfg.sub.server.jwt")
	kv(p, "CHATCLI_JWT_SECRET", presence(os.Getenv("CHATCLI_JWT_SECRET")))
	kv(p, "CHATCLI_JWT_ISSUER", envOr("CHATCLI_JWT_ISSUER"))
	kv(p, "CHATCLI_JWT_AUDIENCE", envOr("CHATCLI_JWT_AUDIENCE"))

	fmt.Println(p)
	subheader(p, "cfg.sub.server.rate_limit")
	kv(p, "CHATCLI_RATE_LIMIT_RPS", envOr("CHATCLI_RATE_LIMIT_RPS"))
	kv(p, "CHATCLI_RATE_LIMIT_BURST", envOr("CHATCLI_RATE_LIMIT_BURST"))

	fmt.Println(p)
	subheader(p, "cfg.sub.server.fallback")
	kv(p, "CHATCLI_FALLBACK_ENABLED", envBool("CHATCLI_FALLBACK_ENABLED"))
	kv(p, "CHATCLI_FALLBACK_PROVIDERS", envOr("CHATCLI_FALLBACK_PROVIDERS"))
	kv(p, "CHATCLI_FALLBACK_MAX_RETRIES", envOr("CHATCLI_FALLBACK_MAX_RETRIES"))
	kv(p, "CHATCLI_FALLBACK_COOLDOWN_BASE", envOr("CHATCLI_FALLBACK_COOLDOWN_BASE"))
	kv(p, "CHATCLI_FALLBACK_COOLDOWN_MAX", envOr("CHATCLI_FALLBACK_COOLDOWN_MAX"))

	fmt.Println(p)
	subheader(p, "cfg.sub.server.watcher")
	kv(p, "CHATCLI_WATCH_DEPLOYMENT", envOr("CHATCLI_WATCH_DEPLOYMENT"))
	kv(p, "CHATCLI_WATCH_NAMESPACE", envOr("CHATCLI_WATCH_NAMESPACE"))
	kv(p, "CHATCLI_WATCH_CONFIG", envOr("CHATCLI_WATCH_CONFIG"))
	kv(p, "CHATCLI_KUBECONFIG", envOr("CHATCLI_KUBECONFIG"))

	fmt.Println(p)
	subheader(p, "cfg.sub.server.audit")
	kv(p, "CHATCLI_AUDIT_LOG_PATH", envOr("CHATCLI_AUDIT_LOG_PATH"))

	fmt.Println(p)
	subheader(p, "cfg.sub.server.operator")
	kv(p, "CHATCLI_AIOPS_PORT", envOr("CHATCLI_AIOPS_PORT"))
	kv(p, "CHATCLI_OPERATOR_DEV_MODE", envBool("CHATCLI_OPERATOR_DEV_MODE"))
	kv(p, "CHATCLI_AIOPS_TLS_CERT", envOr("CHATCLI_AIOPS_TLS_CERT"))
	kv(p, "CHATCLI_AIOPS_TLS_KEY", presence(os.Getenv("CHATCLI_AIOPS_TLS_KEY")))

	sectionEnd(ColorGray)
}

// hubSettingMeta describes a mutable hub setting for display and `/config hub set`.
type hubSettingMeta struct {
	key  string
	env  string
	def  string
	kind string // "bool" | "int" | "str"
}

var hubSettings = []hubSettingMeta{
	{hubKeyEnabled, envHubEnabled, "true", "bool"},
	{hubKeyPrincipal, envHubPrincipal, defaultHubPrincipal, "str"},
	{hubKeyIsolate, envHubIsolate, "false", "bool"},
	{hubKeyTTLHours, envHubTTLHours, strconv.Itoa(defaultHubTTLHours), "int"},
}

// hubEffective resolves a setting's value and where it came from, mirroring the
// runtime precedence: db setting > env var > default.
func hubEffective(settings map[string]string, m hubSettingMeta) (val, source string) {
	if v, ok := settings[m.key]; ok && v != "" {
		return v, "setting"
	}
	if v := strings.TrimSpace(os.Getenv(m.env)); v != "" {
		return v, "env"
	}
	return m.def, "default"
}

// showConfigHub renders the conversation-hub settings (cross-channel continuity)
// and the live sync state. Every setting shown can be changed live with
// `/config hub set <key> <value>` (persisted in the shared db, read by the
// gateway too); the value's source — setting/env/default — is shown so it's
// clear where it came from.
func (cli *ChatCLI) showConfigHub() {
	sectionHeader("🔗", "cfg.section.hub.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	var settings map[string]string
	mutable := false
	if cli.hubSync != nil {
		if s, ok := cli.hubSync.allSettings(context.Background()); ok {
			settings = s
			mutable = true
		}
	}

	subheader(p, "cfg.sub.hub.settings")
	for _, m := range hubSettings {
		val, source := hubEffective(settings, m)
		if source == "default" {
			val = defaultMarker + val // kv renders this cyan with a "(default)" tag
		}
		kv(p, m.key, val)
	}
	kv(p, "bindings", presence(os.Getenv("CHATCLI_HUB_BINDINGS")))
	kv(p, "db", envOr("CHATCLI_HUB_DB"))

	fmt.Println(p)
	if mutable {
		fmt.Println(p + colorize(i18n.T("cfg.hub.mutable_hint"), ColorGray))
	} else {
		fmt.Println(p + colorize(i18n.T("cfg.hub.readonly_hint"), ColorGray))
	}

	fmt.Println(p)
	subheader(p, "cfg.sub.hub.session")
	if cli.hubSync != nil {
		convID, principal := cli.hubSync.status()
		kv(p, i18n.T("cfg.hub.state"), i18n.T("cfg.hub.connected"))
		kv(p, i18n.T("cfg.hub.principal"), principal)
		kv(p, i18n.T("cfg.hub.conversation"), convID)
	} else {
		kv(p, i18n.T("cfg.hub.state"), i18n.T("cfg.hub.not_connected"))
	}

	sectionEnd(ColorBlue)
}

// routeConfigHub handles `/config hub set|reset <key> [value]`, mutating the
// live db-backed settings.
func (cli *ChatCLI) routeConfigHub(args []string) {
	if cli.hubSync == nil {
		fmt.Println(colorize("  "+i18n.T("cfg.hub.no_session"), ColorYellow))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch strings.ToLower(args[0]) {
	case "set":
		if len(args) < 3 {
			fmt.Println(colorize("  "+i18n.T("cfg.hub.set_usage"), ColorGray))
			return
		}
		key := strings.ToLower(args[1])
		meta, ok := hubSettingMetaFor(key)
		if !ok {
			fmt.Println(colorize("  "+i18n.T("cfg.hub.unknown_key", key, hubSettingKeyList()), ColorYellow))
			return
		}
		value, verr := normalizeHubValue(meta, strings.Join(args[2:], " "))
		if verr != nil {
			fmt.Println(colorize("  "+verr.Error(), ColorYellow))
			return
		}
		if err := cli.hubSync.setSetting(ctx, key, value); err != nil {
			fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
			return
		}
		fmt.Println(colorize("  "+i18n.T("cfg.hub.set_ok", key, value), ColorGreen))
		cli.showConfigHub()

	case "reset", "unset":
		if len(args) < 2 {
			fmt.Println(colorize("  "+i18n.T("cfg.hub.set_usage"), ColorGray))
			return
		}
		key := strings.ToLower(args[1])
		if _, ok := hubSettingMetaFor(key); !ok {
			fmt.Println(colorize("  "+i18n.T("cfg.hub.unknown_key", key, hubSettingKeyList()), ColorYellow))
			return
		}
		if err := cli.hubSync.resetSetting(ctx, key); err != nil {
			fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
			return
		}
		fmt.Println(colorize("  "+i18n.T("cfg.hub.reset_ok", key), ColorGreen))
		cli.showConfigHub()

	default:
		fmt.Println(colorize("  "+i18n.T("cfg.hub.set_usage"), ColorGray))
	}
}

func hubSettingMetaFor(key string) (hubSettingMeta, bool) {
	for _, m := range hubSettings {
		if m.key == key {
			return m, true
		}
	}
	return hubSettingMeta{}, false
}

func hubSettingKeyList() string {
	keys := make([]string, len(hubSettings))
	for i, m := range hubSettings {
		keys[i] = m.key
	}
	return strings.Join(keys, ", ")
}

// normalizeHubValue validates and canonicalizes a setting value by kind:
// booleans accept on/off/true/false/1/0/yes/no → "true"/"false"; ints must parse.
func normalizeHubValue(m hubSettingMeta, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch m.kind {
	case "bool":
		switch strings.ToLower(raw) {
		case "true", "on", "1", "yes", "y":
			return "true", nil
		case "false", "off", "0", "no", "n":
			return "false", nil
		default:
			return "", fmt.Errorf("%s", i18n.T("cfg.hub.bad_bool", m.key))
		}
	case "int":
		if n, err := strconv.Atoi(raw); err != nil || n < 0 {
			return "", fmt.Errorf("%s", i18n.T("cfg.hub.bad_int", m.key))
		}
		return raw, nil
	default:
		return raw, nil
	}
}

// renderCoderPolicy prints coder policy state: active policy file, local
// override, merge mode, rule count, and the last pattern that matched in
// this session. Isolated from the main security section so a policy-manager
// init failure doesn't abort the whole block.
func (cli *ChatCLI) renderCoderPolicy(p string) {
	policyPath := i18n.T("cfg.val.unknown")
	localPath := i18n.T("cfg.val.none")
	localMerge := i18n.T("cfg.val.off")
	rulesCount := "0"
	lastRule := i18n.T("cfg.val.no_matches_yet")

	if pm, err := coder.NewPolicyManager(cli.logger); err == nil {
		policyPath = pm.ActivePolicyPath()
		if lp := pm.LocalPolicyPath(); strings.TrimSpace(lp) != "" {
			localPath = lp
			if pm.LocalMergeEnabled() {
				localMerge = i18n.T("cfg.val.on")
			}
		}
		rulesCount = fmt.Sprintf("%d", pm.RulesCount())
	}
	if cli.agentMode != nil && cli.agentMode.lastPolicyMatch != nil {
		lastRule = fmt.Sprintf("%s => %s",
			cli.agentMode.lastPolicyMatch.Pattern,
			cli.agentMode.lastPolicyMatch.Action)
	}

	kv(p, i18n.T("cfg.kv.active_policy"), policyPath)
	kv(p, i18n.T("cfg.kv.local_override"), localPath)
	kv(p, i18n.T("cfg.kv.local_merge"), localMerge)
	kv(p, i18n.T("cfg.kv.rule_count"), rulesCount)
	kv(p, i18n.T("cfg.kv.last_match"), lastRule)
}
