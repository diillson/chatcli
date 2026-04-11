/*
 * ChatCLI - Manual skill invocation via /<skill-name>
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements the "/<skill-name> [args]" routing promised by the skill
 * frontmatter advanced docs:
 *   - Only skills with `user-invocable: true` are routable.
 *   - Skills with `disable-model-invocation: true` are still allowed via
 *     manual invocation (the flag only blocks *auto* activation).
 *   - The skill's full content is injected into the system prompt for the
 *     single turn via cli.pendingManualSkill.
 *   - The skill's `model:` / `effort:` hints are honored for that turn.
 *   - Any trailing args become the user message passed to the LLM; when
 *     empty, a neutral "apply skill X" instruction is synthesized and the
 *     `argument-hint` (if any) is shown to the user as a usage nudge.
 */
package cli

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// reservedSlashCommands is the set of built-in slash commands that must NEVER
// be shadowed by a skill, regardless of whether a skill with the same name
// exists. The list mirrors command_handler.go's dispatch table.
var reservedSlashCommands = map[string]bool{
	"agent": true, "run": true, "coder": true, "switch": true, "help": true,
	"config": true, "status": true, "settings": true, "version": true, "v": true,
	"nextchunk": true, "retry": true, "retryall": true, "skipchunk": true,
	"newsession": true, "session": true, "context": true, "auth": true,
	"plugin": true, "skill": true, "connect": true, "disconnect": true,
	"watch": true, "compact": true, "rewind": true, "memory": true, "metrics": true,
	"mcp": true, "hooks": true, "cost": true, "worktree": true, "channel": true,
	"reset": true, "redraw": true, "clear": true, "exit": true, "quit": true,
	"reload": true, "fast": true,
}

// tryInvokeUserSkill inspects userInput for a `/<name> [args]` pattern and,
// if `<name>` resolves to a user-invocable skill, stages the skill for the
// next LLM turn and kicks off processLLMRequest. Returns true when the input
// was recognized (so the caller's default "unknown command" branch should
// not fire); returns false when the input is not a skill invocation.
func (ch *CommandHandler) tryInvokeUserSkill(userInput string) bool {
	if !strings.HasPrefix(userInput, "/") || len(userInput) < 2 {
		return false
	}
	if ch.cli.personaHandler == nil {
		return false
	}
	mgr := ch.cli.personaHandler.GetManager()
	if mgr == nil {
		return false
	}

	// Split into "/name" + rest.
	trimmed := strings.TrimPrefix(userInput, "/")
	parts := strings.SplitN(trimmed, " ", 2)
	name := strings.TrimSpace(parts[0])
	if name == "" || reservedSlashCommands[strings.ToLower(name)] {
		return false
	}

	skill, err := mgr.GetSkillByName(name)
	if err != nil || skill == nil {
		return false
	}
	if !skill.UserInvocable {
		fmt.Println(colorize(
			fmt.Sprintf("  %s: /%s", i18n.T("skill.invoke.not_invocable"), name),
			ColorYellow))
		return true // recognized name but refused — don't fall through to "unknown command"
	}

	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	// If the client isn't configured yet we can't actually run the turn.
	if ch.cli.Client == nil {
		fmt.Println(i18n.T("cli.error.no_provider_configured"))
		return true
	}

	// Show argument-hint when the user called the skill with no args.
	if args == "" && skill.ArgumentHint != "" {
		fmt.Printf("  %s %s\n",
			colorize("hint:", ColorGray),
			colorize(skill.ArgumentHint, ColorCyan))
	}

	// Stage the skill for the next processLLMRequest call. It is cleared
	// inside processLLMRequest after injection so it only affects this turn.
	ch.cli.pendingManualSkill = skill
	ch.cli.pendingManualSkillArgs = args

	// Synthesize the user-visible prompt. When the user passes no args we
	// emit a neutral instruction that preserves intent without guessing.
	prompt := args
	if prompt == "" {
		prompt = fmt.Sprintf("Apply skill \"%s\" to the current context.", skill.Name)
	}

	ch.cli.logger.Info("manual skill invocation",
		zap.String("skill", skill.Name),
		zap.String("args", args),
		zap.String("model_hint", skill.Model),
		zap.String("effort_hint", skill.Effort))

	// If a response is already being produced, queue the synthesized prompt
	// so it runs in-order just like any typed-ahead message.
	if ch.cli.isExecuting.Load() {
		ch.cli.messageQueueMu.Lock()
		ch.cli.messageQueue = append(ch.cli.messageQueue, prompt)
		ch.cli.messageQueueMu.Unlock()
		return true
	}

	// Fire the LLM turn exactly like the executor would for a normal user
	// message (same windows/non-windows scheduling rules).
	ch.cli.interactionState = StateProcessing
	if runtime.GOOS == "windows" {
		ch.cli.processLLMRequest(prompt)
	} else {
		go ch.cli.processLLMRequest(prompt)
	}
	return true
}

// renderManualSkillBlock formats a manual skill invocation as a dedicated
// system-prompt block. Kept separate from the auto-activated block so the
// LLM can tell the two sources apart when both happen in the same turn.
func renderManualSkillBlock(skill *persona.Skill, args string) string {
	if skill == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Manually Invoked Skill\n\n")
	b.WriteString("The user invoked this skill explicitly via `/")
	b.WriteString(skill.Name)
	b.WriteString("`. Follow its instructions as the primary guidance for this turn.\n\n")

	fmt.Fprintf(&b, "## Skill: %s", skill.Name)
	if skill.Version != "" {
		fmt.Fprintf(&b, " (v%s)", skill.Version)
	}
	b.WriteString("\n\n")
	if skill.Description != "" {
		b.WriteString(skill.Description)
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(skill.Content) != "" {
		b.WriteString(skill.Content)
		b.WriteString("\n\n")
	}
	if args != "" {
		b.WriteString("### Invocation arguments\n\n")
		b.WriteString("```\n")
		b.WriteString(args)
		b.WriteString("\n```\n")
	}
	return b.String()
}
