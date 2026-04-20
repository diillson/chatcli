/*
 * ChatCLI - /refine and /verify session toggles (Phases 5 & 6).
 *
 * The hooks are normally controlled by /config quality. These slashes
 * give the user a way to flip the next-turn behavior without editing
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

// qualityToggleSpec describes one session-level quality toggle
// (/refine, /verify) so the two slashes share their parsing and
// message-emission logic without duplicating the switch ladder.
type qualityToggleSpec struct {
	name    string // "refine" | "verify" (also the i18n prefix)
	current **bool // pointer to the override field on ChatCLI
	usage   string // i18n usage string key
}

// handleRefineCommand implements /refine [on|off|once|auto|clear].
func (cli *ChatCLI) handleRefineCommand(userInput string) {
	cli.handleQualityToggle(userInput, qualityToggleSpec{
		name:    "refine",
		current: &cli.qualityOverrides.Refine,
		usage:   "refine.usage",
	})
}

// handleVerifyCommand implements /verify [on|off|once|auto|clear].
func (cli *ChatCLI) handleVerifyCommand(userInput string) {
	cli.handleQualityToggle(userInput, qualityToggleSpec{
		name:    "verify",
		current: &cli.qualityOverrides.Verify,
		usage:   "verify.usage",
	})
}

// handleQualityToggle is the shared parser/emitter for /refine and
// /verify. Each verb emits its own i18n key (e.g. "refine.set_on",
// "verify.set_on") so localized messages remain faithful to the
// slash the user typed.
func (cli *ChatCLI) handleQualityToggle(userInput string, spec qualityToggleSpec) {
	parts := strings.Fields(strings.TrimSpace(userInput))
	if len(parts) <= 1 {
		cli.showQualityToggleState(spec.name, *spec.current)
		return
	}
	switch strings.ToLower(parts[1]) {
	case "on":
		*spec.current = boolPtr(true)
		fmt.Println(colorize("  "+i18n.T(spec.name+".set_on"), ColorGreen))
	case "off":
		*spec.current = boolPtr(false)
		fmt.Println(colorize("  "+i18n.T(spec.name+".set_off"), ColorYellow))
	case "once", "next":
		*spec.current = boolPtr(true)
		fmt.Println(colorize("  "+i18n.T(spec.name+".set_once"), ColorGreen))
	case "auto", "clear":
		*spec.current = nil
		fmt.Println(colorize("  "+i18n.T(spec.name+".cleared"), ColorGreen))
	default:
		fmt.Println(colorize("  "+i18n.T(spec.usage), ColorYellow))
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
