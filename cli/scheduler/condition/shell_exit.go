/*
 * ShellExit — evaluator that runs a shell command and returns true iff
 * its exit code matches `expected` (default 0).
 *
 * Spec:
 *   cmd      string   — required, the command to run
 *   expected int      — optional, expected exit code (default 0)
 *   shell    string   — optional, "bash"/"sh"/"powershell" (default OS default)
 *   timeout  duration — optional, bounds the single run
 *   workdir  string   — optional, working directory
 *   env      map      — optional, extra env vars
 *   bypass_safety bool — when true and the operator has allowed it,
 *                        skip CoderMode command allowlist. Rejected by
 *                        the bridge otherwise.
 *
 * Safety: shell_exit always routes through the CLIBridge.RunShell
 * method so the same CoderMode safety the agent uses applies here.
 */
package condition

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// ShellExit implements scheduler.ConditionEvaluator.
type ShellExit struct{}

// NewShellExit returns a fresh evaluator.
func NewShellExit() *ShellExit { return &ShellExit{} }

// Type returns the Condition.Type literal.
func (ShellExit) Type() string { return "shell_exit" }

// ValidateSpec enforces required fields.
func (ShellExit) ValidateSpec(spec map[string]any) error {
	cmd := asString(spec, "cmd")
	if strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("shell_exit: spec.cmd is required")
	}
	return nil
}

// Evaluate runs the shell command.
func (ShellExit) Evaluate(ctx context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	cmd := cond.SpecString("cmd", "")
	expected := cond.SpecInt("expected", 0)
	envOverrides := map[string]string{}
	if m := asStringMap(cond.Spec, "env"); m != nil {
		envOverrides = m
	}
	bypass := cond.SpecBool("bypass_safety", false)

	if env == nil || env.Bridge == nil {
		return scheduler.EvalOutcome{
			Err:       fmt.Errorf("shell_exit: no CLI bridge wired"),
			Transient: false,
		}
	}
	stdout, stderr, code, err := env.Bridge.RunShell(ctx, cmd, envOverrides, bypass)
	details := fmt.Sprintf("exit=%d expected=%d\nstdout:\n%s\nstderr:\n%s",
		code, expected, truncate(stdout, 1024), truncate(stderr, 1024))

	if err != nil {
		// ctx errors are transient from the breaker's point of view.
		transient := ctx.Err() != nil
		return scheduler.EvalOutcome{
			Satisfied: false,
			Transient: transient,
			Details:   details,
			Err:       err,
		}
	}
	return scheduler.EvalOutcome{
		Satisfied: code == expected,
		Details:   details,
	}
}
