/*
 * ChatCLI - Slash command integration (dispatch, expansion, security gate)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Wires the cli/commands catalog into every user-input surface. Expansion is
 * pure prompt rewriting, so a command behaves identically on REPL chat,
 * coder follow-ups, one-shot -p, the messaging gateway, ACP and MCP —
 * whatever the provider.
 *
 * Security invariants (the reason this file owns the ExecRunner):
 *   - Every "!" pre-execution line goes through the SAME machinery as coder
 *     tool calls: IsSafetyImmune → PolicyManager.Check → interactive
 *     approval (PromptSecurityCheckGuarded). The gate is never bypassed.
 *   - Unattended surfaces (gateway/MCP/ACP/scheduler) have no human to
 *     answer an "ask": policy automode may approve, otherwise DENY
 *     fail-safe. Never auto-approve just because nobody is watching.
 *   - allowed-tools from the frontmatter becomes an ephemeral overlay
 *     consumed by the next agent/coder run: a tool outside the list
 *     escalates allow→ask; it never silently widens permissions.
 */
package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/commands"
	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// commandsEnabledEnv is the single feature gate. Default on: the catalog is
// inert (no dirs → no commands) until the user creates command files.
const commandsEnabledEnv = "CHATCLI_COMMANDS"

// slashCommandsEnabled reads CHATCLI_COMMANDS; unset means enabled.
func slashCommandsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(commandsEnabledEnv))) {
	case "false", "0", "off", "no", "disabled":
		return false
	}
	return true
}

// commandEmptyBodyPrompt is the model-facing fallback when a template
// expands to nothing (e.g. every line was a denied pre-exec). English
// constant, not i18n — it is an instruction to the model, same rationale
// as memoryRecallHint.
const commandEmptyBodyPrompt = "Run the %q command."

// preExec limits: one template line must not hang a turn or flood the
// prompt. Output is truncated rune-safe at the cap.
const (
	preExecTimeout   = 60 * time.Second
	preExecOutputCap = 32 * 1024
)

// initSlashCommands builds the catalog. Called once at startup, after the
// command handler exists (the reserved-name check derives from its routes).
func (cli *ChatCLI) initSlashCommands(projectDir string) {
	globalDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		globalDir = filepath.Join(home, ".chatcli", "commands")
	}
	var isReserved func(string) bool
	if cli.commandHandler != nil {
		isReserved = cli.commandHandler.isReservedSlashName
	}
	cli.slashCommands = commands.NewCatalog(projectDir, globalDir, isReserved, cli.logger)
}

// slashCommandCatalog returns the catalog, nil-safe for tests that build a
// bare ChatCLI.
func (cli *ChatCLI) slashCommandCatalog() *commands.Catalog {
	if cli == nil || cli.slashCommands == nil || !slashCommandsEnabled() {
		return nil
	}
	return cli.slashCommands
}

// splitSlashInvocation splits "/token args" → (token, args, true) when the
// input is shaped like a slash invocation.
func splitSlashInvocation(input string) (string, string, bool) {
	if !strings.HasPrefix(input, "/") || len(input) < 2 {
		return "", "", false
	}
	trimmed := strings.TrimPrefix(input, "/")
	parts := strings.SplitN(trimmed, " ", 2)
	token := strings.TrimSpace(parts[0])
	if token == "" {
		return "", "", false
	}
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return token, args, true
}

// expandSlashCommandInput resolves input against the catalog. When it is a
// known command, it returns the fully expanded prompt (pre-exec lines
// resolved through the gate) and stages the command's model/effort and
// allowed-tools hints for the surface that will run the turn. interactive
// governs the pre-exec gate: REPL surfaces may prompt; unattended surfaces
// go through policy automode or fail-safe deny.
func (cli *ChatCLI) expandSlashCommandInput(ctx context.Context, input string, interactive bool) (string, bool) {
	cat := cli.slashCommandCatalog()
	if cat == nil {
		return "", false
	}
	token, args, ok := splitSlashInvocation(input)
	if !ok {
		return "", false
	}
	cmd := cat.Get(token)
	if cmd == nil {
		return "", false
	}

	if interactive && args == "" && cmd.ArgumentHint != "" {
		fmt.Printf("  %s %s\n",
			colorize("hint:", ColorGray),
			colorize(cmd.ArgumentHint, ColorCyan))
	}

	expanded := commands.Expand(cmd, args, cli.commandPreExecRunner(ctx, cmd, args, interactive))
	if strings.TrimSpace(expanded) == "" {
		expanded = fmt.Sprintf(commandEmptyBodyPrompt, cmd.InvocationName())
	}

	cli.pendingCommandModel = cmd.Model
	cli.pendingCommandEffort = cmd.Effort
	cli.pendingCommandAllowedTools = append([]string(nil), cmd.AllowedTools...)

	cli.logger.Info("slash command expanded",
		zap.String("command", cmd.InvocationName()),
		zap.String("source", string(cmd.Source)),
		zap.String("model_hint", cmd.Model),
		zap.Int("allowed_tools", len(cmd.AllowedTools)))
	return expanded, true
}

// consumePendingCommandHints hands the staged model/effort hints to the
// turn assembler and clears them (single-turn semantics, like manual
// skills).
func (cli *ChatCLI) consumePendingCommandHints() (model, effort string) {
	model, effort = cli.pendingCommandModel, cli.pendingCommandEffort
	cli.pendingCommandModel, cli.pendingCommandEffort = "", ""
	return model, effort
}

// consumePendingCommandToolScope hands the staged allowed-tools overlay to
// the agent run and clears it.
func (cli *ChatCLI) consumePendingCommandToolScope() []string {
	scope := cli.pendingCommandAllowedTools
	cli.pendingCommandAllowedTools = nil
	return scope
}

// commandPreExecRunner builds the gated ExecRunner for one invocation.
// Every command line is checked in order: safety immunity → policy → (ask)
// interactive approval or unattended policy resolution.
func (cli *ChatCLI) commandPreExecRunner(ctx context.Context, cmd *commands.Command, args string, interactive bool) commands.ExecRunner {
	lines := commands.PreExecLines(cmd, args)
	if len(lines) == 0 {
		return nil
	}
	pm, err := coder.NewPolicyManager(cli.logger)
	if err != nil {
		cli.logger.Warn("slash command pre-exec: policy manager unavailable, denying all", zap.Error(err))
		return func(string) (string, bool) { return "", false }
	}

	return func(sh string) (string, bool) {
		action := pm.Check("exec", sh)
		if coder.IsSafetyImmune("exec", sh) {
			// Immune commands can never ride an allow rule or automode.
			action = coder.ActionAsk
		}
		switch action {
		case coder.ActionDeny:
			cli.logger.Info("slash command pre-exec denied by policy", zap.String("cmd", sh))
			return "", false
		case coder.ActionAsk:
			if !interactive || cli.unattended {
				// Session automode ("/policy automode on") is the operator's
				// explicit opt-in for unattended asks — but never for
				// safety-immune commands.
				if cli.PolicyAutoMode() && !coder.IsSafetyImmune("exec", sh) {
					cli.logger.Info("slash command pre-exec auto-approved (policy automode)", zap.String("cmd", sh))
					break
				}
				cli.logger.Info("slash command pre-exec denied (unattended, no approver)", zap.String("cmd", sh))
				return "", false
			}
			decision := coder.PromptSecurityCheckGuarded(ctx, "exec", sh, nil)
			pattern := coder.GetSuggestedPattern("exec", sh)
			switch decision {
			case coder.DecisionAllowAlways:
				if pattern != "" {
					_ = pm.AddRule(pattern, coder.ActionAllow)
				}
			case coder.DecisionDenyForever:
				if pattern != "" {
					_ = pm.AddRule(pattern, coder.ActionDeny)
				}
				return "", false
			case coder.DecisionDenyOnce, coder.DecisionCanceled:
				return "", false
			}
		}
		return runPreExecCommand(ctx, sh)
	}
}

// runPreExecCommand executes one approved shell line with a bounded
// timeout, returning combined output truncated rune-safe.
func runPreExecCommand(ctx context.Context, sh string) (string, bool) {
	execCtx, cancel := context.WithTimeout(ctx, preExecTimeout)
	defer cancel()

	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(execCtx, "cmd", "/C", sh) // #nosec G204 -- command approved through the security gate above
	} else {
		c = exec.CommandContext(execCtx, utils.GetUserShell(), "-c", sh) // #nosec G204 -- command approved through the security gate above
	}
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	out := truncateRunesafe(buf.String(), preExecOutputCap)
	if err != nil {
		// The command RAN and failed: that result is real context the model
		// should see (exit status + output), unlike a gate denial.
		return out + "\n[exit error: " + err.Error() + "]", true
	}
	return out, true
}

// commandsPluginAdapter implements plugins.CommandsAdapter: the model-facing
// @commands tool reaches the live catalog through it. Expansion is always
// non-interactive here — the model must never be able to trigger a terminal
// prompt on its own; pre-exec resolves through policy/automode.
type commandsPluginAdapter struct{ cli *ChatCLI }

// List renders the catalog for the model: one line per command.
func (a *commandsPluginAdapter) List() string {
	cat := a.cli.slashCommandCatalog()
	if cat == nil {
		return "Slash commands are disabled in this session."
	}
	list := cat.List()
	if len(list) == 0 {
		return "No slash commands installed. Users create them as markdown files in .chatcli/commands/ (project) or ~/.chatcli/commands/."
	}
	var b strings.Builder
	b.WriteString("Available slash-command templates (expand one with {\"cmd\":\"get\"}):\n")
	for _, cmd := range list {
		fmt.Fprintf(&b, "- %s", cmd.InvocationName())
		if cmd.Description != "" {
			fmt.Fprintf(&b, ": %s", cmd.Description)
		}
		if cmd.ArgumentHint != "" {
			fmt.Fprintf(&b, " (args: %s)", cmd.ArgumentHint)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// Get expands one command for the model.
func (a *commandsPluginAdapter) Get(ctx context.Context, name, args string) (string, bool) {
	return a.cli.RenderCommandPromptRPC(ctx, name, args)
}

// registerCommandPaletteProvider contributes the dynamic slash surface to
// the palette registry: command templates AND user-invocable skills. This
// closes a pre-existing gap — skills appeared in the inline completer but
// never in /menu nor in the ACP command listing, because the palette
// registry was a static literal.
func (cli *ChatCLI) registerCommandPaletteProvider() {
	palette.RegisterProvider(func() []palette.RootCommand {
		var out []palette.RootCommand
		if cat := cli.slashCommandCatalog(); cat != nil {
			for _, cmd := range cat.List() {
				desc := cmd.Description
				if desc == "" {
					desc = string(cmd.Source)
				}
				out = append(out, palette.RootCommand{
					Name: "/" + cmd.InvocationName(), Category: palette.CatCommands, SummaryText: desc,
				})
			}
		}
		if cli.personaHandler != nil {
			if mgr := cli.personaHandler.GetManager(); mgr != nil {
				for _, s := range mgr.ListAllSkills() {
					if s == nil || !s.UserInvocable || s.Name == "" {
						continue
					}
					desc := s.Description
					if desc == "" {
						desc = i18n.T("complete.skill.user_invocable_fallback")
					}
					out = append(out, palette.RootCommand{
						Name: "/" + s.Name, Category: palette.CatCommands, SummaryText: desc,
					})
				}
			}
		}
		return out
	})
}

// ExpandSlashCommandRPC is the remote-surface twin of the REPL dispatch:
// ACP prompts and MCP chat turns call it to resolve a "/name args" input
// into the expanded prompt. Always non-interactive — the pre-exec gate
// resolves through policy/automode, never a terminal prompt.
func (cli *ChatCLI) ExpandSlashCommandRPC(ctx context.Context, text string) (string, bool) {
	return cli.expandSlashCommandInput(ctx, text, false)
}

// ListCommandPromptsRPC exposes the catalog to the MCP prompts primitive.
func (cli *ChatCLI) ListCommandPromptsRPC() [][3]string {
	cat := cli.slashCommandCatalog()
	if cat == nil {
		return nil
	}
	list := cat.List()
	out := make([][3]string, 0, len(list))
	for _, cmd := range list {
		out = append(out, [3]string{cmd.InvocationName(), cmd.Description, cmd.ArgumentHint})
	}
	return out
}

// RenderCommandPromptRPC expands one command for MCP prompts/get. The
// pre-exec gate runs non-interactive (policy/automode or deny).
func (cli *ChatCLI) RenderCommandPromptRPC(ctx context.Context, name, args string) (string, bool) {
	cat := cli.slashCommandCatalog()
	if cat == nil {
		return "", false
	}
	cmd := cat.Get(name)
	if cmd == nil {
		return "", false
	}
	return commands.Expand(cmd, args, cli.commandPreExecRunner(ctx, cmd, args, false)), true
}

// tryInvokeSlashCommand is the REPL dispatch hook: expands a catalog match
// and fires the LLM turn exactly like manual skill invocation does (queue
// when busy, same scheduling rules). Returns true when the input was a
// known command.
func (ch *CommandHandler) tryInvokeSlashCommand(ctx context.Context, userInput string) bool {
	expanded, ok := ch.cli.expandSlashCommandInput(ctx, userInput, !ch.cli.unattended)
	if !ok {
		return false
	}
	if ch.cli.Client == nil {
		fmt.Println(i18n.T("cli.error.no_provider_configured"))
		return true
	}
	if ch.cli.isExecuting.Load() {
		ch.cli.messageQueueMu.Lock()
		ch.cli.messageQueue = append(ch.cli.messageQueue, expanded)
		ch.cli.messageQueueMu.Unlock()
		return true
	}
	ch.cli.interactionState = StateProcessing
	if runtime.GOOS == "windows" {
		ch.cli.processLLMRequest(ctx, expanded)
	} else {
		go ch.cli.processLLMRequest(ctx, expanded)
	}
	return true
}
