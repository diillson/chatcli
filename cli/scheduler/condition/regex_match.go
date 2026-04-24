/*
 * RegexMatch — evaluator that runs a shell command and matches output
 * against a regex. Used when the condition is easier to express as
 * "grep the last X" than as a dedicated shell_exit check.
 *
 * Spec:
 *   cmd     string — required
 *   pattern string — required (Go regexp syntax)
 *   source  string — optional, "stdout" (default), "stderr", or "combined"
 *   timeout duration — optional
 *   bypass_safety bool — optional
 *   env     map    — optional env overrides
 */
package condition

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// RegexMatch implements scheduler.ConditionEvaluator.
type RegexMatch struct{}

// NewRegexMatch builds the evaluator.
func NewRegexMatch() *RegexMatch { return &RegexMatch{} }

// Type returns the Condition.Type literal.
func (RegexMatch) Type() string { return "regex_match" }

// ValidateSpec enforces required fields.
func (RegexMatch) ValidateSpec(spec map[string]any) error {
	if strings.TrimSpace(asString(spec, "cmd")) == "" {
		return fmt.Errorf("regex_match: spec.cmd is required")
	}
	pat := asString(spec, "pattern")
	if pat == "" {
		return fmt.Errorf("regex_match: spec.pattern is required")
	}
	if _, err := regexp.Compile(pat); err != nil {
		return fmt.Errorf("regex_match: invalid pattern: %w", err)
	}
	return nil
}

// Evaluate runs the shell command and matches.
func (RegexMatch) Evaluate(ctx context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	cmd := cond.SpecString("cmd", "")
	pattern := cond.SpecString("pattern", "")
	source := cond.SpecString("source", "stdout")
	bypass := cond.SpecBool("bypass_safety", false)
	envOverrides := asStringMap(cond.Spec, "env")

	re, err := regexp.Compile(pattern)
	if err != nil {
		return scheduler.EvalOutcome{Err: err}
	}

	if env == nil || env.Bridge == nil {
		return scheduler.EvalOutcome{Err: fmt.Errorf("regex_match: no CLI bridge wired")}
	}
	stdout, stderr, code, runErr := env.Bridge.RunShell(ctx, cmd, envOverrides, bypass)
	if runErr != nil {
		return scheduler.EvalOutcome{Err: runErr, Details: fmt.Sprintf("exit=%d", code)}
	}
	var body string
	switch source {
	case "stderr":
		body = stderr
	case "combined":
		body = stdout + "\n" + stderr
	default:
		body = stdout
	}
	satisfied := re.MatchString(body)
	return scheduler.EvalOutcome{
		Satisfied: satisfied,
		Details:   fmt.Sprintf("exit=%d matched=%v", code, satisfied),
	}
}
