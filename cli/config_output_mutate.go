/*
 * ChatCLI - /config output mutator.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Output-token reduction on the /config surface — read-only panorama plus
 * runtime mutation:
 *
 *   /config output                 # status (verbosity + effort routing)
 *   /config output full            # no steering (model's natural verbosity)
 *   /config output concise         # drop ceremony/restatement (default)
 *   /config output minimal         # fewest correct tokens
 *   /config output effort on|off   # complexity->effort downgrade (opt-in)
 *
 * Both knobs are read live from the environment each turn, so changes take
 * effect immediately. A hint points to .env for a permanent default; we never
 * rewrite .env.
 */
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/outputpolicy"
	"github.com/diillson/chatcli/i18n"
)

// routeConfigOutput dispatches /config output <sub> [args]. "output" is
// stripped by routeConfigCommand; empty args shows the panorama there.
func (cli *ChatCLI) routeConfigOutput(args []string) {
	if len(args) == 0 {
		cli.showConfigOutput()
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "help", "-h", "--help":
		cli.printConfigOutputUsage()
	case "status", "show":
		cli.showConfigOutput()
	case "full", "off", "concise", "brief", "minimal", "terse", "verbosity":
		sub := args[0]
		if strings.EqualFold(sub, "verbosity") && len(args) >= 2 {
			sub = args[1]
		}
		cli.setOutputVerbosity(sub)
	case "effort":
		if len(args) >= 2 {
			cli.setOutputEffortRouting(args[1])
		} else {
			cli.showConfigOutput()
		}
	default:
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.output.set_invalid", args[0]), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.output.set_valid"), ColorGray))
	}
}

// setOutputVerbosity flips CHATCLI_OUTPUT_VERBOSITY at runtime.
func (cli *ChatCLI) setOutputVerbosity(level string) {
	v, ok := outputpolicy.ParseVerbosity(level)
	if !ok {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.output.set_invalid", level), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.output.set_valid"), ColorGray))
		return
	}
	prev := outputVerbosity()
	_ = os.Setenv("CHATCLI_OUTPUT_VERBOSITY", v.String())
	if prev == v {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.output.set_noop", v.String()), ColorGray))
	} else {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.output.set_ok", prev.String(), v.String()), ColorGreen))
	}
	fmt.Println(colorize("    "+i18n.T("cfg.output.persist_hint", v.String()), ColorGray))
}

// setOutputEffortRouting flips CHATCLI_OUTPUT_EFFORT_ROUTING at runtime.
func (cli *ChatCLI) setOutputEffortRouting(state string) {
	on := false
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "on", "enable", "true", "1", "yes":
		on = true
	case "off", "disable", "false", "0", "no":
		on = false
	default:
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.output.effort_invalid", state), ColorRed))
		return
	}
	val := "off"
	if on {
		val = "on"
	}
	_ = os.Setenv("CHATCLI_OUTPUT_EFFORT_ROUTING", val)
	fmt.Println(colorize("  ✔ "+i18n.T("cfg.output.effort_set", val), ColorGreen))
}

// showConfigOutput renders the output-token-reduction panorama.
func (cli *ChatCLI) showConfigOutput() {
	sectionHeader("✂️", "cfg.section.output.title", ColorBlue)
	p := uiPrefix(ColorBlue)
	kv(p, i18n.T("cfg.output.verbosity"), outputVerbosity().String())
	effort := "off"
	if outputEffortRoutingEnabled() {
		effort = "on"
	}
	kv(p, i18n.T("cfg.output.effort_routing"), effort)
	kv(p, "CHATCLI_OUTPUT_VERBOSITY", envOrDefault("CHATCLI_OUTPUT_VERBOSITY", "concise"))
	kv(p, "CHATCLI_OUTPUT_EFFORT_ROUTING", envOrDefault("CHATCLI_OUTPUT_EFFORT_ROUTING", "off"))
	fmt.Println(p)
	fmt.Println(p + colorize(i18n.T("cfg.output.about"), ColorGray))
	fmt.Println(p + colorize(i18n.T("cfg.output.change_hint"), ColorGray))
	sectionEnd(ColorBlue)
}

// printConfigOutputUsage shows the subcommand cheat sheet.
func (cli *ChatCLI) printConfigOutputUsage() {
	fmt.Println(colorize(i18n.T("cfg.output.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  /config output             # " + i18n.T("cfg.output.usage_status"))
	fmt.Println("  /config output concise     # " + i18n.T("cfg.output.usage_concise"))
	fmt.Println("  /config output minimal     # " + i18n.T("cfg.output.usage_minimal"))
	fmt.Println("  /config output full        # " + i18n.T("cfg.output.usage_full"))
	fmt.Println("  /config output effort on   # " + i18n.T("cfg.output.usage_effort"))
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.output.usage_note"), ColorGray))
}

// getConfigOutputSuggestions autocompletes `/config output …`.
func (cli *ChatCLI) getConfigOutputSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "concise", Description: i18n.T("complete.config.output_concise")},
			{Text: "minimal", Description: i18n.T("complete.config.output_minimal")},
			{Text: "full", Description: i18n.T("complete.config.output_full")},
			{Text: "effort", Description: i18n.T("complete.config.output_effort")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}
	if len(args) >= 3 && strings.EqualFold(args[2], "effort") {
		if len(args) == 3 || (len(args) == 4 && !strings.HasSuffix(line, " ")) {
			return prompt.FilterHasPrefix([]prompt.Suggest{
				{Text: "on", Description: i18n.T("complete.config.output_effort_on")},
				{Text: "off", Description: i18n.T("complete.config.output_effort_off")},
			}, word, true)
		}
	}
	return []prompt.Suggest{}
}
