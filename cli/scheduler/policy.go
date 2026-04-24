/*
 * ChatCLI - Scheduler: shell policy preflight.
 *
 * Every Job admitted into the scheduler is walked BEFORE being written
 * to the WAL, and each shell command embedded in it is classified
 * against the CoderMode policy manager (via CLIBridge.ClassifyShellCommand).
 *
 * The rules:
 *
 *   ShellPolicyDeny  → reject enqueue with ErrShellPolicyDeny, always.
 *                      Even --i-know (DangerousConfirmed=true) cannot
 *                      override a deny — denylist is authoritative.
 *
 *   ShellPolicyAsk   → reject enqueue with ErrShellPolicyAsk, UNLESS
 *                      job.DangerousConfirmed is true. The user (or
 *                      agent with explicit user blessing) must
 *                      pre-acknowledge that the command would normally
 *                      prompt. This converts an interactive approval
 *                      into a durable decision attached to the job.
 *
 *   ShellPolicyAllow → admit normally.
 *
 * At fire time RunShell on the bridge re-checks — a policy update
 * between schedule and fire that moves a command from Allow→Deny
 * makes the job fail cleanly.
 *
 * This file also enumerates shell commands from a Job. Shell commands
 * hide in three places:
 *
 *   1. Action.Payload — when Action.Type == ActionShell, the command
 *      lives under payload.command.
 *   2. Wait.Condition — shell-based evaluators (shell_exit, regex_match,
 *      custom, k8s_resource_ready, docker_running, llm_check with
 *      context_cmd) carry a command in Condition.Spec.
 *   3. Composite children — all_of/any_of recurse through Children.
 *
 * Non-shell actions/conditions (webhook, http_status, file_exists,
 * tcp_reachable) do not need policy review here — their security
 * gates are in the network / filesystem layers.
 */
package scheduler

import (
	"fmt"
	"strings"
)

// enumerateShellCommands walks a Job's Action + Wait chain and
// returns every shell command that would be executed. Used by the
// preflight check so we classify all of them in one pass.
func enumerateShellCommands(j *Job) []string {
	var cmds []string
	if j == nil {
		return nil
	}
	if cmd := shellFromAction(j.Action); cmd != "" {
		cmds = append(cmds, cmd)
	}
	if j.Wait != nil {
		cmds = append(cmds, shellFromCondition(j.Wait.Condition)...)
		if j.Wait.Fallback != nil {
			if cmd := shellFromAction(*j.Wait.Fallback); cmd != "" {
				cmds = append(cmds, cmd)
			}
		}
	}
	return cmds
}

// shellFromAction extracts the shell command for ActionShell. Other
// action types return "" because their effective payload is not a
// raw shell command (slash commands go through the CLI's own policy;
// webhooks do HTTP; agent/worker actions re-enter ReAct which has
// its own check).
func shellFromAction(a Action) string {
	if a.Type != ActionShell {
		return ""
	}
	return strings.TrimSpace(a.PayloadString("command", ""))
}

// shellFromCondition recurses through composites and pulls the shell
// command out of any condition type that runs a shell. Unknown types
// return no commands — new evaluators that invoke shell must add
// their spec key here to be preflight-checked.
func shellFromCondition(c Condition) []string {
	var out []string
	switch c.Type {
	case "shell_exit":
		if cmd := c.SpecString("cmd", ""); cmd != "" {
			out = append(out, cmd)
		}
	case "regex_match":
		if cmd := c.SpecString("cmd", ""); cmd != "" {
			out = append(out, cmd)
		}
	case "custom":
		// custom scripts are shell-invoked (see condition/custom.go).
		if script := c.SpecString("script", ""); script != "" {
			out = append(out, script)
		}
	case "llm_check":
		// llm_check runs context_cmd via the bridge before the LLM call.
		if cmd := c.SpecString("context_cmd", ""); cmd != "" {
			out = append(out, cmd)
		}
	case "k8s_resource_ready", "docker_running":
		// These synthesize their own kubectl/docker commands
		// internally. The commands are well-known and read-only by
		// nature (`kubectl get -o jsonpath`, `docker inspect`), so we
		// delegate classification to the bridge on the generated
		// string — but since the string is only known at fire time,
		// we mark these as "needs fire-time check" rather than
		// surfacing the command here. The bridge re-checks on fire.
	case "all_of", "any_of":
		for _, child := range c.Children {
			out = append(out, shellFromCondition(child)...)
		}
	}
	return out
}

// preflightShellPolicy classifies every shell command embedded in the
// job and rejects the enqueue if any fails policy. Called from
// Scheduler.Enqueue before the WAL write.
func (s *Scheduler) preflightShellPolicy(j *Job) error {
	if s.bridge == nil {
		// Fail-closed when there is no bridge to classify against.
		return fmt.Errorf("%w: no bridge wired for policy classification", ErrShellPolicyAsk)
	}
	commands := enumerateShellCommands(j)
	if len(commands) == 0 {
		return nil
	}
	for _, cmd := range commands {
		policy := s.bridge.ClassifyShellCommand(cmd)
		switch policy {
		case ShellPolicyAllow:
			// ok
		case ShellPolicyDeny:
			// Denylist beats --i-know.
			return fmt.Errorf("%w: %s", ErrShellPolicyDeny, truncate(cmd, 120))
		case ShellPolicyAsk:
			if j.DangerousConfirmed {
				// User (or agent with explicit --i-know/i_know) pre-authorized.
				continue
			}
			return fmt.Errorf("%w: %s", ErrShellPolicyAsk, truncate(cmd, 120))
		default:
			return fmt.Errorf("%w: unknown policy result %d for %q", ErrInvalidJob, policy, truncate(cmd, 120))
		}
	}
	return nil
}
