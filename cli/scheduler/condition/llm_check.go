/*
 * LLMCheck — evaluator that asks the currently-configured LLM whether
 * a freeform condition is satisfied.
 *
 * Spec:
 *   prompt          string — required. Question to ask the model.
 *   system          string — optional. System prompt prefix.
 *   context_cmd     string — optional. Shell command whose output is
 *                            appended to the prompt as "Current state:".
 *   passes_if       string — optional. "yes" (default — model answer
 *                            must start with "yes"), or a regex that
 *                            must match.
 *   max_tokens      int    — optional, bounds the model response.
 *   timeout         duration — optional.
 *
 * Use cases:
 *   - "Based on output of `kubectl get pods`, is the deploy healthy?"
 *   - "Does this Terraform plan only add resources?"
 */
package condition

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// LLMCheck implements scheduler.ConditionEvaluator.
type LLMCheck struct{}

// NewLLMCheck builds the evaluator.
func NewLLMCheck() *LLMCheck { return &LLMCheck{} }

// Type returns the Condition.Type literal.
func (LLMCheck) Type() string { return "llm_check" }

// ValidateSpec enforces required fields.
func (LLMCheck) ValidateSpec(spec map[string]any) error {
	if strings.TrimSpace(asString(spec, "prompt")) == "" {
		return fmt.Errorf("llm_check: spec.prompt is required")
	}
	if pat := asString(spec, "passes_if"); pat != "" && pat != "yes" && pat != "no" {
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("llm_check: invalid passes_if regex: %w", err)
		}
	}
	return nil
}

// Evaluate asks the LLM.
func (LLMCheck) Evaluate(ctx context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	prompt := cond.SpecString("prompt", "")
	system := cond.SpecString("system", "You are a precise evaluator. Answer with a single line beginning with 'YES' or 'NO' followed by a one-sentence reason.")
	ctxCmd := cond.SpecString("context_cmd", "")
	passesIf := cond.SpecString("passes_if", "yes")
	maxTokens := cond.SpecInt("max_tokens", 120)
	timeout := cond.SpecDuration("timeout", 30*time.Second)

	if env == nil || env.Bridge == nil {
		return scheduler.EvalOutcome{Err: fmt.Errorf("llm_check: no CLI bridge wired")}
	}

	full := prompt
	if ctxCmd != "" {
		stdout, stderr, _, err := env.Bridge.RunShell(ctx, ctxCmd, nil, false, env.DangerousConfirmed)
		if err != nil {
			return scheduler.EvalOutcome{Err: err, Details: "context_cmd failed"}
		}
		snippet := stdout
		if stderr != "" {
			snippet += "\n--- stderr ---\n" + stderr
		}
		if len(snippet) > 16*1024 {
			snippet = snippet[:16*1024] + "\n…[truncated]"
		}
		full = prompt + "\n\nCurrent state:\n```\n" + snippet + "\n```"
	}

	llmCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	text, tokens, cost, err := env.Bridge.SendLLMPrompt(llmCtx, system, full, maxTokens)
	_ = tokens
	_ = cost
	if err != nil {
		return scheduler.EvalOutcome{
			Err:       err,
			Transient: llmCtx.Err() != nil,
			Details:   "llm call failed",
		}
	}
	trim := strings.TrimSpace(text)
	var satisfied bool
	switch passesIf {
	case "yes", "":
		satisfied = strings.HasPrefix(strings.ToUpper(trim), "YES")
	case "no":
		satisfied = strings.HasPrefix(strings.ToUpper(trim), "NO")
	default:
		if re, err := regexp.Compile(passesIf); err == nil {
			satisfied = re.MatchString(trim)
		}
	}
	return scheduler.EvalOutcome{
		Satisfied: satisfied,
		Details:   truncate(trim, 200),
	}
}
