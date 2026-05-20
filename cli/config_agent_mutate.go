/*
 * ChatCLI - /config agent mutator.
 *
 * Closes the gap where /config agent was read-only: switching the
 * timeline UI (full / compact / minimal) required restarting the
 * process with CHATCLI_CODER_UI exported. Now it flips at runtime
 * and the next /coder or /agent picks up the new style on the next
 * NewUIRenderer call (which reads agent.DefaultUIStyleFromEnv each
 * time the agent loop starts).
 *
 *   /config agent                  # read-only dump (legacy)
 *   /config agent ui               # show current style + options
 *   /config agent ui full          # switch to bordered cards
 *   /config agent ui compact       # switch to inline ↻/✓ lines
 *   /config agent ui minimal       # switch to truncated cards
 *
 * Persistence: the switch lives in process env only. The mutator
 * prints a hint so users that want a permanent default know to add
 * `CHATCLI_CODER_UI=<value>` to their .env (or wherever CHATCLI_DOTENV
 * points). We deliberately do NOT rewrite the .env file ourselves —
 * that file is user-owned territory; transparently editing it would
 * destroy comments / ordering and surprise anyone who reads it next.
 */
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/i18n"
)

// agentUIStyleEnvVar is the env knob backing the timeline style. It
// is the SAME variable that controls /coder — the cross-mode rename
// is intentional: a single knob, two surfaces.
const agentUIStyleEnvVar = "CHATCLI_CODER_UI"

// routeConfigAgent dispatches a /config agent <sub> [args...] call.
// Args comes with the "agent" token already stripped (consistent with
// routeConfigSecurity). The empty-args case is handled by the caller
// in routeConfigCommand and never reaches this function.
func (cli *ChatCLI) routeConfigAgent(args []string) {
	if len(args) == 0 {
		cli.showConfigAgent()
		return
	}
	sub := strings.ToLower(args[0])
	rest := args[1:]

	switch sub {
	case "help", "-h", "--help":
		cli.printConfigAgentUsage()
	case "ui", "style":
		cli.configAgentUI(rest)
	default:
		fmt.Println(colorize("  "+i18n.T("cfg.agent.unknown_sub", sub), ColorYellow))
		cli.printConfigAgentUsage()
	}
}

// printConfigAgentUsage shows the subcommand cheat sheet for /config
// agent. Strings flow through i18n so the en/pt help blocks stay in
// sync with the rest of the /config surface.
func (cli *ChatCLI) printConfigAgentUsage() {
	fmt.Println(colorize(i18n.T("cfg.agent.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  /config agent")
	fmt.Println("  /config agent ui                       # " + i18n.T("cfg.agent.usage_ui_show"))
	fmt.Println("  /config agent ui full                  # " + i18n.T("cfg.agent.usage_ui_full"))
	fmt.Println("  /config agent ui compact               # " + i18n.T("cfg.agent.usage_ui_compact"))
	fmt.Println("  /config agent ui minimal               # " + i18n.T("cfg.agent.usage_ui_minimal"))
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.agent.usage_note_scope"), ColorGray))
	fmt.Println(colorize("  "+i18n.T("cfg.agent.usage_note_persist"), ColorGray))
}

// configAgentUI handles `/config agent ui [value]`. No arg = show
// current resolved style plus a chooser; with arg = set runtime style.
func (cli *ChatCLI) configAgentUI(args []string) {
	if len(args) == 0 {
		cli.printConfigAgentUIStatus()
		return
	}
	target := strings.ToLower(strings.TrimSpace(args[0]))
	style, ok := parseUIStyle(target)
	if !ok {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.agent.ui_invalid_value", target), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.agent.ui_valid_values"), ColorGray))
		return
	}

	previous := agent.DefaultUIStyleFromEnv()
	envValue := uiStyleEnvValue(style)
	if err := os.Setenv(agentUIStyleEnvVar, envValue); err != nil {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.agent.ui_set_failed", err.Error()), ColorRed))
		return
	}

	if previous == style {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.agent.ui_set_noop", style.String()), ColorGray))
	} else {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.agent.ui_set_ok", previous.String(), style.String()), ColorGreen))
	}
	fmt.Println(colorize("    "+i18n.T("cfg.agent.ui_runtime_hint"), ColorGray))
	fmt.Println(colorize("    "+i18n.T("cfg.agent.ui_persist_hint", envValue), ColorGray))
}

// printConfigAgentUIStatus shows the current resolved style and the
// available alternatives so the user knows what to type next.
func (cli *ChatCLI) printConfigAgentUIStatus() {
	current := agent.DefaultUIStyleFromEnv()
	envRaw := os.Getenv(agentUIStyleEnvVar)
	source := i18n.T("cfg.agent.ui_source_env")
	if strings.TrimSpace(envRaw) == "" {
		source = i18n.T("cfg.agent.ui_source_default")
	}

	fmt.Println(colorize(i18n.T("cfg.agent.ui_status_header"), ColorCyan+ColorBold))
	fmt.Printf("  %s %s  (%s)\n",
		colorize(i18n.T("cfg.agent.ui_status_current"), ColorGray),
		colorize(current.String(), ColorLime+ColorBold),
		source,
	)
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.agent.ui_status_options"), ColorGray))
	for _, opt := range []struct {
		key  string
		desc string
	}{
		{"full", i18n.T("cfg.agent.ui_desc_full")},
		{"compact", i18n.T("cfg.agent.ui_desc_compact")},
		{"minimal", i18n.T("cfg.agent.ui_desc_minimal")},
	} {
		marker := "  "
		key := opt.key
		if uiStyleEnvValue(current) == key {
			marker = colorize("→ ", ColorLime+ColorBold)
		}
		fmt.Printf("  %s%s%s  %s\n",
			marker,
			colorize(fmt.Sprintf("%-8s", key), ColorYellow),
			colorize(" ·", ColorGray),
			opt.desc,
		)
	}
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.agent.ui_status_change_hint"), ColorGray))
}

// parseUIStyle resolves the user-typed token (case-insensitive) into
// an agent.UIStyle. Returns ok=false for unrecognized values so the
// caller can print a friendly error instead of silently picking Full.
func parseUIStyle(s string) (agent.UIStyle, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "full":
		return agent.UIStyleFull, true
	case "compact":
		return agent.UIStyleCompact, true
	case "minimal", "min":
		return agent.UIStyleMinimal, true
	default:
		return agent.UIStyleFull, false
	}
}

// uiStyleEnvValue inverts parseUIStyle: given a UIStyle enum, return
// the canonical env value (matches DefaultUIStyleFromEnv parsing).
func uiStyleEnvValue(s agent.UIStyle) string {
	switch s {
	case agent.UIStyleCompact:
		return "compact"
	case agent.UIStyleMinimal:
		return "minimal"
	default:
		return "full"
	}
}
