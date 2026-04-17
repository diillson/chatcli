/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
)

// handleWebSearchCommand routes /websearch subcommands.
//
//	/websearch                → current status + chain
//	/websearch list           → all known providers + configured?
//	/websearch provider <name> → override for this session (sets env var)
//	/websearch reset          → clear override → back to auto
func (cli *ChatCLI) handleWebSearchCommand(userInput string) {
	args := strings.Fields(userInput)
	if len(args) == 1 {
		cli.showWebSearchStatus()
		return
	}
	switch args[1] {
	case "list", "ls":
		cli.showWebSearchList()
	case "provider", "set", "use":
		if len(args) < 3 {
			fmt.Println(colorize("  "+i18n.T("ws.cmd.usage_provider"), ColorYellow))
			return
		}
		cli.setWebSearchProvider(args[2])
	case "reset", "clear":
		_ = os.Unsetenv("CHATCLI_WEBSEARCH_PROVIDER")
		fmt.Println(colorize("  "+i18n.T("ws.cmd.override_cleared"), ColorGreen))
		cli.showWebSearchStatus()
	case "status":
		cli.showWebSearchStatus()
	default:
		fmt.Println(colorize("  "+i18n.T("ws.cmd.unknown_sub", args[1]), ColorYellow))
		fmt.Println(colorize("  "+i18n.T("ws.cmd.usage_hint"), ColorGray))
	}
}

// showWebSearchStatus prints the current override, SearxNG config, and the
// active fallback chain that a query would actually use right now.
func (cli *ChatCLI) showWebSearchStatus() {
	fmt.Println()
	fmt.Println(uiBox("🔎", i18n.T("ws.cmd.box_title"), ColorCyan))
	p := uiPrefix(ColorCyan)

	override := os.Getenv("CHATCLI_WEBSEARCH_PROVIDER")
	if override == "" {
		override = "auto"
	}
	searxngURL := os.Getenv("SEARXNG_URL")

	fmt.Println(p + fmt.Sprintf("  %s%-16s%s %s",
		ColorGray, i18n.T("ws.cmd.kv_override")+":", ColorReset, override))
	if searxngURL != "" {
		fmt.Println(p + fmt.Sprintf("  %s%-16s%s %s",
			ColorGray, i18n.T("ws.cmd.kv_searxng_url")+":", ColorReset, searxngURL))
	} else {
		fmt.Println(p + fmt.Sprintf("  %s%-16s%s %s",
			ColorGray, i18n.T("ws.cmd.kv_searxng_url")+":", ColorReset,
			i18n.T("ws.cmd.searxng_not_configured")))
	}

	fmt.Println(p + "")
	fmt.Println(p + fmt.Sprintf("  %s%s%s", ColorGray, i18n.T("ws.cmd.active_chain_header"), ColorReset))
	for i, name := range plugins.SelectSearchChainNames() {
		marker := "↓"
		if i == 0 {
			marker = "★"
		}
		fmt.Println(p + fmt.Sprintf("    %s %d. %s", marker, i+1, name))
	}
	fmt.Println()
}

// showWebSearchList enumerates all known providers with configured-or-not state.
func (cli *ChatCLI) showWebSearchList() {
	fmt.Println()
	fmt.Println(uiBox("🔎", i18n.T("ws.cmd.box_providers"), ColorCyan))
	p := uiPrefix(ColorCyan)

	for _, name := range plugins.KnownSearchProviders {
		switch name {
		case plugins.ProviderSearXNG:
			status := colorize(i18n.T("ws.cmd.sx_not_configured"), ColorYellow)
			if os.Getenv("SEARXNG_URL") != "" {
				status = colorize(i18n.T("ws.cmd.sx_configured"), ColorGreen)
			}
			fmt.Println(p + fmt.Sprintf("  %s%-12s%s %s", ColorGray, name, ColorReset, status))
			fmt.Println(p + fmt.Sprintf("    %s%s%s", ColorGray, i18n.T("ws.cmd.sx_tagline"), ColorReset))
		case plugins.ProviderDuckDuckGo:
			fmt.Println(p + fmt.Sprintf("  %s%-12s%s %s",
				ColorGray, name, ColorReset,
				colorize(i18n.T("ws.cmd.ddg_available"), ColorGreen)))
			fmt.Println(p + fmt.Sprintf("    %s%s%s", ColorGray, i18n.T("ws.cmd.ddg_tagline"), ColorReset))
		}
	}
	fmt.Println()
}

// setWebSearchProvider validates the name and applies it via env var for
// this process. For persistence, the user sets it in their shell or .env.
func (cli *ChatCLI) setWebSearchProvider(name string) {
	name = strings.ToLower(strings.TrimSpace(name))

	valid := map[string]bool{
		string(plugins.ProviderAuto):       true,
		string(plugins.ProviderSearXNG):    true,
		string(plugins.ProviderDuckDuckGo): true,
	}
	if !valid[name] {
		fmt.Println(colorize("  "+i18n.T("ws.cmd.invalid_provider", name), ColorYellow))
		fmt.Println(colorize("  "+i18n.T("ws.cmd.valid_options"), ColorGray))
		return
	}

	if name == string(plugins.ProviderAuto) {
		_ = os.Unsetenv("CHATCLI_WEBSEARCH_PROVIDER")
	} else {
		_ = os.Setenv("CHATCLI_WEBSEARCH_PROVIDER", name)
	}

	if name == string(plugins.ProviderSearXNG) && os.Getenv("SEARXNG_URL") == "" {
		fmt.Println(colorize("  "+i18n.T("ws.cmd.sx_missing_url_warn"), ColorYellow))
		fmt.Println(colorize("    "+i18n.T("ws.cmd.sx_missing_url_hint1"), ColorGray))
		fmt.Println(colorize("      "+i18n.T("ws.cmd.sx_missing_url_hint2"), ColorGray))
	}

	fmt.Println(colorize("  "+i18n.T("ws.cmd.provider_set_session", name), ColorGreen))
	fmt.Println(colorize("    "+i18n.T("ws.cmd.persist_hint"), ColorGray))
	if name == string(plugins.ProviderAuto) {
		fmt.Println(colorize("      "+i18n.T("ws.cmd.persist_unset"), ColorGray))
	} else {
		fmt.Println(colorize("      "+i18n.T("ws.cmd.persist_set", name), ColorGray))
	}
	cli.showWebSearchStatus()
}
