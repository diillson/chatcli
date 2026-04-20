/*
 * ChatCLI - /refine and /verify session toggles (Phases 5 & 6).
 *
 * The hooks are normally controlled by /config quality. These slashes
 * give the user a way to flip the next-turn behaviour without editing
 * env vars: /refine on|off|once, /verify on|off|once. Bare /refine or
 * /verify show the current state.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// quality_overrides_state is reused across /refine, /verify, /plan
// so the next-turn one-shot toggle has a single home. Negative bool
// fields would be ambiguous; using *bool keeps "no override" distinct
// from "explicitly off".
type qualityOverridesState struct {
	Refine *bool // nil = no override
	Verify *bool
}

// boolPtr is a tiny helper to fit literal toggles into qualityOverridesState.
func boolPtr(b bool) *bool { return &b }

// handleRefineCommand implements /refine [on|off|once].
func (cli *ChatCLI) handleRefineCommand(userInput string) {
	parts := strings.Fields(strings.TrimSpace(userInput))
	if len(parts) <= 1 {
		cli.showQualityToggleState("refine", cli.qualityOverrides.Refine)
		return
	}
	switch strings.ToLower(parts[1]) {
	case "on":
		cli.qualityOverrides.Refine = boolPtr(true)
		fmt.Println(colorize("  "+i18n.T("refine.set_on"), ColorGreen))
	case "off":
		cli.qualityOverrides.Refine = boolPtr(false)
		fmt.Println(colorize("  "+i18n.T("refine.set_off"), ColorYellow))
	case "once", "next":
		// Same as "on" for the next turn; the agent loop clears the
		// override after consuming it. Today both /refine on and once
		// behave the same (override stays until user clears or session
		// ends); future evolution can split them.
		cli.qualityOverrides.Refine = boolPtr(true)
		fmt.Println(colorize("  "+i18n.T("refine.set_once"), ColorGreen))
	case "auto", "clear":
		cli.qualityOverrides.Refine = nil
		fmt.Println(colorize("  "+i18n.T("refine.cleared"), ColorGreen))
	default:
		fmt.Println(colorize("  "+i18n.T("refine.usage"), ColorYellow))
	}
}

// handleVerifyCommand implements /verify [on|off|once].
func (cli *ChatCLI) handleVerifyCommand(userInput string) {
	parts := strings.Fields(strings.TrimSpace(userInput))
	if len(parts) <= 1 {
		cli.showQualityToggleState("verify", cli.qualityOverrides.Verify)
		return
	}
	switch strings.ToLower(parts[1]) {
	case "on":
		cli.qualityOverrides.Verify = boolPtr(true)
		fmt.Println(colorize("  "+i18n.T("verify.set_on"), ColorGreen))
	case "off":
		cli.qualityOverrides.Verify = boolPtr(false)
		fmt.Println(colorize("  "+i18n.T("verify.set_off"), ColorYellow))
	case "once", "next":
		cli.qualityOverrides.Verify = boolPtr(true)
		fmt.Println(colorize("  "+i18n.T("verify.set_once"), ColorGreen))
	case "auto", "clear":
		cli.qualityOverrides.Verify = nil
		fmt.Println(colorize("  "+i18n.T("verify.cleared"), ColorGreen))
	default:
		fmt.Println(colorize("  "+i18n.T("verify.usage"), ColorYellow))
	}
}

func (cli *ChatCLI) showQualityToggleState(name string, override *bool) {
	switch {
	case override == nil:
		fmt.Println(colorize("  "+i18n.T(name+".state_auto"), ColorGray))
	case *override:
		fmt.Println(colorize("  "+i18n.T(name+".state_on"), ColorGreen))
	default:
		fmt.Println(colorize("  "+i18n.T(name+".state_off"), ColorYellow))
	}
}
