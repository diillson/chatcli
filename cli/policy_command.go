/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * policy_command.go
 *
 * /policy — session-scoped security-policy mode. In automode every coder
 * policy "ask" verdict auto-approves, so the agent runs uninterrupted;
 * explicit deny rules, the dangerous-command validator and safety-immune
 * operations keep gating exactly as in interactive mode. The mode is never
 * persisted: a new session always starts interactive. Exposed on all three
 * surfaces — terminal REPL, ACP (slash command), MCP (manage_session
 * policy_mode action).
 */
package cli

import (
	"fmt"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// PolicyAutoMode reports whether the session is in policy automode ("ask"
// verdicts auto-approve). Safe for concurrent use: toggled from command
// surfaces while the agent loop reads it.
func (cli *ChatCLI) PolicyAutoMode() bool {
	return cli.policyAutoMode.Load()
}

// SetPolicyAutoMode switches the session's policy mode. Session-scoped by
// design — never persisted, so every new process starts interactive.
func (cli *ChatCLI) SetPolicyAutoMode(on bool) {
	cli.policyAutoMode.Store(on)
	if cli.logger != nil {
		cli.logger.Info("security policy session mode changed", zap.Bool("automode", on))
	}
}

// PolicyModeLabel returns the localized label of the current session mode.
func (cli *ChatCLI) PolicyModeLabel() string {
	if cli.PolicyAutoMode() {
		return i18n.T("policy.mode.auto_label")
	}
	return i18n.T("policy.mode.interactive_label")
}

// parsePolicyModeArg maps a user-typed mode token to the automode flag.
// Deliberately lenient (synonyms, any case): a strict vocabulary here would
// make the command fail for zero safety benefit.
func parsePolicyModeArg(arg string) (auto, ok bool) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "auto", "automode", "on":
		return true, true
	case "interactive", "ask", "manual", "off":
		return false, true
	}
	return false, false
}

// handlePolicyCommand dispatches /policy:
//
//	/policy                     → status
//	/policy status              → status
//	/policy mode                → status
//	/policy mode auto           → enable automode
//	/policy mode interactive    → back to interactive
//	/policy auto|off|…          → shorthand for mode switch
func (cli *ChatCLI) handlePolicyCommand(userInput string) {
	args := strings.Fields(userInput)
	if len(args) > 0 {
		args = args[1:]
	}
	switch {
	case len(args) == 0 || strings.EqualFold(args[0], "status"):
		cli.printPolicyStatus()
	case strings.EqualFold(args[0], "mode"):
		if len(args) == 1 {
			cli.printPolicyStatus()
			return
		}
		cli.applyPolicyMode(args[1])
	default:
		cli.applyPolicyMode(args[0])
	}
}

// applyPolicyMode parses and applies one mode token, reporting the result.
func (cli *ChatCLI) applyPolicyMode(arg string) {
	auto, ok := parsePolicyModeArg(arg)
	if !ok {
		fmt.Println(i18n.T("policy.usage", arg))
		return
	}
	cli.SetPolicyAutoMode(auto)
	if auto {
		fmt.Println(i18n.T("policy.mode_set_auto"))
		fmt.Println(i18n.T("policy.gated_note"))
		return
	}
	fmt.Println(i18n.T("policy.mode_set_interactive"))
}

// printPolicyStatus shows the session mode and the policy file summary.
func (cli *ChatCLI) printPolicyStatus() {
	fmt.Println(i18n.T("policy.status.header"))
	fmt.Println("  " + i18n.T("policy.status.mode", cli.PolicyModeLabel()))
	if cli.PolicyAutoMode() {
		fmt.Println("  " + i18n.T("policy.gated_note"))
	}
	if pm, err := coder.NewPolicyManager(cli.logger); err == nil {
		fmt.Println("  " + i18n.T("policy.status.rules", pm.RulesCount(), pm.ActivePolicyPath()))
	}
	fmt.Println("  " + i18n.T("policy.status.usage_hint"))
}

// askAutoApproved reports whether a coder-policy "ask" verdict should
// auto-approve under the session's automode. Deny rules never reach this
// gate (deny wins before ask in PolicyManager.Check), and safety-immune
// operations keep prompting: automode widens convenience, never the
// blast radius the immunity list protects against.
func (a *AgentMode) askAutoApproved(toolName, args string) bool {
	if a.cli == nil || !a.cli.PolicyAutoMode() {
		return false
	}
	return !coder.IsSafetyImmune(toolName, args)
}

// getPolicySuggestions powers inline completion for /policy.
func (cli *ChatCLI) getPolicySuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/policy", Description: i18n.T("complete.root.policy")},
		}
	}
	// First argument slot: subcommands.
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "mode", Description: i18n.T("complete.policy.mode")},
			{Text: "status", Description: i18n.T("complete.policy.status")},
		}, word, true)
	}
	// Mode values after "mode".
	if len(args) >= 2 && strings.EqualFold(args[1], "mode") {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "auto", Description: i18n.T("complete.policy.mode_auto")},
			{Text: "interactive", Description: i18n.T("complete.policy.mode_interactive")},
		}, word, true)
	}
	return nil
}
