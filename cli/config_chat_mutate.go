/*
 * ChatCLI - /config chat mutator.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exposes the chat-mode ask_user exception (CHATCLI_CHAT_ASK) on the
 * /config surface — not just read-only: it can be toggled at runtime.
 *
 *   /config chat                   # status (read-only panorama)
 *   /config chat ask               # status
 *   /config chat ask on            # enable (chat may use ONLY ask_user)
 *   /config chat ask off           # disable (chat stays tool-less)
 *   /config chat ask toggle        # flip
 *
 * The toggle flips process env only (chatAskEnabled reads os.Getenv each
 * turn, so it takes effect immediately). A hint points users to .env for a
 * permanent default; we never rewrite .env ourselves (user-owned territory).
 */
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
	client "github.com/diillson/chatcli/llm/client"
)

// routeConfigChat dispatches /config chat <sub> [args]. "chat" stripped by
// the caller (routeConfigCommand). Empty args is handled there too.
func (cli *ChatCLI) routeConfigChat(args []string) {
	if len(args) == 0 {
		cli.showConfigChat()
		return
	}
	switch strings.ToLower(args[0]) {
	case "help", "-h", "--help":
		cli.printConfigChatUsage()
	case "ask":
		cli.configChatAsk(args[1:])
	case "knowledge", "kb":
		cli.configChatKnowledge(args[1:])
	case "on", "enable", "status", "off", "disable", "toggle":
		// Allow the shorthand `/config chat on|off|toggle|status` too.
		cli.configChatAsk(args)
	default:
		fmt.Println(colorize("  "+i18n.T("cfg.chat.unknown_sub", args[0]), ColorYellow))
		cli.printConfigChatUsage()
	}
}

// configChatAsk handles `/config chat ask [on|off|toggle|status]`.
func (cli *ChatCLI) configChatAsk(args []string) {
	if len(args) == 0 {
		cli.showConfigChat()
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "on", "enable", "true", "1", "yes":
		cli.setChatAsk(true)
	case "off", "disable", "false", "0", "no":
		cli.setChatAsk(false)
	case "toggle":
		cli.setChatAsk(!chatAskEnabled())
	case "status", "show":
		cli.showConfigChat()
	default:
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.chat.ask_invalid", args[0]), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.chat.ask_valid"), ColorGray))
	}
}

// configChatKnowledge handles `/config chat knowledge [on|off|toggle|status]`.
func (cli *ChatCLI) configChatKnowledge(args []string) {
	if len(args) == 0 {
		cli.showConfigChat()
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "on", "enable", "true", "1", "yes":
		cli.setChatToggle(chatKnowledgeEnvVar, "knowledge", chatKnowledgeEnabled(), true)
	case "off", "disable", "false", "0", "no":
		cli.setChatToggle(chatKnowledgeEnvVar, "knowledge", chatKnowledgeEnabled(), false)
	case "toggle":
		cli.setChatToggle(chatKnowledgeEnvVar, "knowledge", chatKnowledgeEnabled(), !chatKnowledgeEnabled())
	case "status", "show":
		cli.showConfigChat()
	default:
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.chat.kb_invalid", args[0]), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.chat.kb_valid"), ColorGray))
	}
}

// setChatAsk flips CHATCLI_CHAT_ASK at runtime.
func (cli *ChatCLI) setChatAsk(enable bool) {
	cli.setChatToggle(chatAskEnvVar, "ask_user", chatAskEnabled(), enable)
}

// setChatToggle flips one chat-exception env knob at runtime. The knobs are
// read live each turn, so the change takes effect immediately; the hint points
// to .env for a permanent default (user-owned territory, never rewritten).
func (cli *ChatCLI) setChatToggle(envVar, feature string, prev, enable bool) {
	val := "false"
	if enable {
		val = "true"
	}
	if err := os.Setenv(envVar, val); err != nil {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.chat.set_failed", err.Error()), ColorRed))
		return
	}
	if prev == enable {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.chat.set_noop_var", feature, chatStateLabel(enable)), ColorGray))
	} else {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.chat.set_ok_var", feature, chatStateLabel(prev), chatStateLabel(enable)), ColorGreen))
	}
	fmt.Println(colorize("    "+i18n.T("cfg.chat.persist_hint_var", envVar, val), ColorGray))
}

// chatStateLabel maps a bool to the localized enabled/disabled label.
func chatStateLabel(b bool) string {
	if b {
		return i18n.T("cfg.val.enabled")
	}
	return i18n.T("cfg.val.disabled")
}

// showConfigChat renders the chat-mode panorama: the ask_user toggle, whether
// the active provider supports native tools, and whether the feature is
// effectively active right now.
func (cli *ChatCLI) showConfigChat() {
	sectionHeader("💬", "cfg.section.chat.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	kv(p, chatAskEnvVar, envBool(chatAskEnvVar))
	kv(p, i18n.T("cfg.chat.ask_effective"), chatStateLabel(chatAskEnabled()))
	kv(p, chatKnowledgeEnvVar, envBool(chatKnowledgeEnvVar))
	kv(p, i18n.T("cfg.chat.kb_effective"), chatStateLabel(chatKnowledgeEnabled()))

	// Both native (API key) and XML (OAuth) providers work; report which path
	// the active provider will take so the user knows what to expect.
	mode := i18n.T("cfg.chat.mode_xml")
	if cli.Client != nil {
		if tac, ok := client.AsToolAware(cli.Client); ok && tac.SupportsNativeTools() {
			mode = i18n.T("cfg.chat.mode_native")
		}
	}
	kv(p, i18n.T("cfg.chat.tool_mode"), mode)
	kv(p, i18n.T("cfg.chat.ask_active"), yesNo(chatAskEnabled()))

	fmt.Println(p)
	fmt.Println(p + colorize(i18n.T("cfg.chat.about"), ColorGray))
	fmt.Println(p + colorize(i18n.T("cfg.chat.change_hint"), ColorGray))
	sectionEnd(ColorBlue)
}

// yesNo maps a bool to the localized yes/no label.
func yesNo(b bool) string {
	if b {
		return i18n.T("cfg.val.yes")
	}
	return i18n.T("cfg.val.no")
}

// getConfigChatSuggestions autocompletes `/config chat …`. The "chat" token is
// args[1]; we offer the ask/on/off/toggle/status subcommands and the on/off/
// toggle/status values after `ask`.
func (cli *ChatCLI) getConfigChatSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// /config chat <TAB>
	if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "ask", Description: i18n.T("complete.config.chat_ask")},
			{Text: "knowledge", Description: i18n.T("complete.config.chat_knowledge")},
			{Text: "on", Description: i18n.T("complete.config.chat_on")},
			{Text: "off", Description: i18n.T("complete.config.chat_off")},
			{Text: "toggle", Description: i18n.T("complete.config.chat_toggle")},
			{Text: "status", Description: i18n.T("complete.config.chat_status")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}

	// /config chat ask|knowledge <TAB>
	if len(args) >= 3 && (strings.ToLower(args[2]) == "ask" || strings.ToLower(args[2]) == "knowledge") {
		if len(args) == 3 || (len(args) == 4 && !strings.HasSuffix(line, " ")) {
			vals := []prompt.Suggest{
				{Text: "on", Description: i18n.T("complete.config.chat_on")},
				{Text: "off", Description: i18n.T("complete.config.chat_off")},
				{Text: "toggle", Description: i18n.T("complete.config.chat_toggle")},
				{Text: "status", Description: i18n.T("complete.config.chat_status")},
			}
			return prompt.FilterHasPrefix(vals, word, true)
		}
	}
	return []prompt.Suggest{}
}

// printConfigChatUsage shows the subcommand cheat sheet for /config chat.
func (cli *ChatCLI) printConfigChatUsage() {
	fmt.Println(colorize(i18n.T("cfg.chat.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  /config chat                  # " + i18n.T("cfg.chat.usage_status"))
	fmt.Println("  /config chat ask on           # " + i18n.T("cfg.chat.usage_on"))
	fmt.Println("  /config chat ask off          # " + i18n.T("cfg.chat.usage_off"))
	fmt.Println("  /config chat ask toggle       # " + i18n.T("cfg.chat.usage_toggle"))
	fmt.Println("  /config chat knowledge on     # " + i18n.T("cfg.chat.usage_kb_on"))
	fmt.Println("  /config chat knowledge off    # " + i18n.T("cfg.chat.usage_kb_off"))
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.chat.usage_note"), ColorGray))
}
