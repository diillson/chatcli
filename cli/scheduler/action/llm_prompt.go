/*
 * LLMPrompt — executor that sends a single one-shot prompt to the
 * currently-configured LLM (no tool loop). Useful for "summarize this
 * weekly" style automation.
 *
 * Payload:
 *   prompt      string — required
 *   system      string — optional
 *   max_tokens  int    — optional
 *   save_to_history bool — optional; when true, append the result to
 *                           the chat history as an assistant message.
 */
package action

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/models"
)

// LLMPrompt implements scheduler.ActionExecutor.
type LLMPrompt struct{}

// NewLLMPrompt builds the executor.
func NewLLMPrompt() *LLMPrompt { return &LLMPrompt{} }

// Type returns the ActionType literal.
func (LLMPrompt) Type() scheduler.ActionType { return scheduler.ActionLLMPrompt }

// ValidateSpec enforces required fields.
func (LLMPrompt) ValidateSpec(payload map[string]any) error {
	if strings.TrimSpace(asString(payload, "prompt")) == "" {
		return fmt.Errorf("llm_prompt: payload.prompt is required")
	}
	return nil
}

// Execute calls the bridge's SendLLMPrompt.
func (LLMPrompt) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	prompt := asString(action.Payload, "prompt")
	system := asString(action.Payload, "system")
	maxTokens := asInt(action.Payload, "max_tokens")
	if maxTokens == 0 {
		maxTokens = 512
	}
	save := asBool(action.Payload, "save_to_history")

	if env == nil || env.Bridge == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("llm_prompt: no bridge wired")}
	}
	text, tokens, cost, err := env.Bridge.SendLLMPrompt(ctx, system, prompt, maxTokens)
	if err != nil {
		return scheduler.ActionResult{
			Err:       err,
			Transient: ctx.Err() != nil,
		}
	}
	if save {
		env.Bridge.AppendHistory(models.Message{
			Role:    "assistant",
			Content: "[scheduler " + env.Job.Name + "] " + text,
		})
	}
	return scheduler.ActionResult{
		Output: truncate(text, 1<<14),
		Tokens: tokens,
		Cost:   cost,
	}
}

// ensureDurationNotUnused keeps the import referenced when
// tree-shaking considers removing it.
var _ = time.Second
