/*
 * Custom — evaluator that delegates to a user script. The script
 * receives the condition spec as JSON on stdin and is expected to
 * exit 0 when satisfied, non-zero otherwise. Stdout is shown as
 * details in /jobs show.
 *
 * Spec:
 *   script   string — required; path to the script (absolute or
 *                     relative to workspace).
 *   args     []string — optional; additional positional arguments.
 *   env      map    — optional; env overrides passed to the script.
 *   timeout  duration — optional.
 */
package condition

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// Custom implements scheduler.ConditionEvaluator.
type Custom struct{}

// NewCustom builds the evaluator.
func NewCustom() *Custom { return &Custom{} }

// Type returns the Condition.Type literal.
func (Custom) Type() string { return "custom" }

// ValidateSpec enforces required fields.
func (Custom) ValidateSpec(spec map[string]any) error {
	script := asString(spec, "script")
	if strings.TrimSpace(script) == "" {
		return fmt.Errorf("custom: spec.script is required")
	}
	return nil
}

// Evaluate shells out to the user script.
func (Custom) Evaluate(ctx context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	script := cond.SpecString("script", "")
	if !filepath.IsAbs(script) && env != nil && env.Bridge != nil {
		if ws := env.Bridge.WorkspaceDir(); ws != "" {
			script = filepath.Join(ws, script)
		}
	}
	if _, err := os.Stat(script); err != nil { //#nosec G304 -- operator-scheduled path
		return scheduler.EvalOutcome{Err: fmt.Errorf("custom: %w", err)}
	}
	args := []string{script}
	if extra, ok := cond.Spec["args"].([]any); ok {
		for _, a := range extra {
			args = append(args, fmt.Sprint(a))
		}
	}
	envOverrides := asStringMap(cond.Spec, "env")
	timeout := cond.SpecDuration("timeout", 30*time.Second)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Send spec as JSON on stdin? The CLIBridge.RunShell doesn't support
	// stdin injection; we pass spec via CHATCLI_SCHEDULER_SPEC env var
	// so scripts can parse it. This is good enough for a custom hook.
	specJSON, _ := json.Marshal(cond.Spec)
	envOverrides["CHATCLI_SCHEDULER_SPEC"] = string(specJSON)

	if env == nil || env.Bridge == nil {
		return scheduler.EvalOutcome{Err: fmt.Errorf("custom: no CLI bridge wired")}
	}
	stdout, stderr, code, err := env.Bridge.RunShell(runCtx, strings.Join(args, " "), envOverrides, false, env.DangerousConfirmed)
	details := fmt.Sprintf("exit=%d\nstdout:\n%s\nstderr:\n%s",
		code, truncate(stdout, 512), truncate(stderr, 512))
	if err != nil {
		return scheduler.EvalOutcome{
			Err:       err,
			Transient: runCtx.Err() != nil,
			Details:   details,
		}
	}
	return scheduler.EvalOutcome{
		Satisfied: code == 0,
		Details:   details,
	}
}
