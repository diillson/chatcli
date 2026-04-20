/*
 * ChatCLI - Self-Refine agent (Phase 5 of seven-pattern rollout).
 *
 * RefinerAgent is a pure-reasoning worker (no tool access) that
 * critiques an arbitrary draft against the original task and produces
 * an improved version. It is used in two ways:
 *
 *   1. Directly via <agent_call name="refiner" task="…"> when the
 *      orchestrator wants a deliberate quality pass on someone else's
 *      output.
 *   2. Indirectly by quality.RefineHook (a PostHook in the pipeline),
 *      which wraps the just-finished worker's output and re-dispatches
 *      it through this agent for a multi-pass refine cycle.
 */
package workers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

// RefineDirective is the leading marker that toggles structured input
// for the RefinerAgent. The hook prepends it so the agent knows the
// task body carries (original, draft) sections to critique.
//
// When absent, the agent treats the task as the prose to be refined
// in-place.
const RefineDirective = "[REFINE_DRAFT]"

// RefinerAgent runs Self-Refine over a draft.
//
// Default effort: "medium" — refine is a structured critique pass
// (worth giving the model some room to think) but not so deep that
// it should always burn the high-effort budget. Override via
// CHATCLI_AGENT_REFINER_EFFORT.
type RefinerAgent struct {
	BuiltinAgentMeta
	skills *SkillSet
}

// NewRefinerAgent constructs a RefinerAgent with its skill catalog.
func NewRefinerAgent() *RefinerAgent {
	a := &RefinerAgent{
		BuiltinAgentMeta: NewBuiltinAgentMeta("REFINER", "", "medium"),
		skills:           NewSkillSet(),
	}
	a.skills.Register(&Skill{Name: "self-critique", Description: "Critique a draft against its task and propose targeted improvements", Type: SkillDescriptive})
	a.skills.Register(&Skill{Name: "rewrite-for-quality", Description: "Produce a revised draft that addresses identified issues without inventing new requirements", Type: SkillDescriptive})
	return a
}

// Type identifies the agent.
func (a *RefinerAgent) Type() AgentType  { return AgentTypeRefiner }
func (a *RefinerAgent) Name() string     { return "RefinerAgent" }
func (a *RefinerAgent) IsReadOnly() bool { return true }
func (a *RefinerAgent) AllowedCommands() []string {
	return []string{}
}
func (a *RefinerAgent) Description() string {
	return "Pure-reasoning quality pass: critiques a draft against its original task and produces a revised version. " +
		"No tool access — operates only on text in/text out. Use this agent when accuracy or polish matters."
}
func (a *RefinerAgent) SystemPrompt() string {
	return defaultRefinerPrompt
}
func (a *RefinerAgent) Skills() *SkillSet { return a.skills }

const defaultRefinerPrompt = `You are a Self-Refine agent in ChatCLI.
You receive a TASK and a DRAFT response. Your job is to:

1. Privately critique the DRAFT against the TASK in 3-6 bullet points
   (logical gaps, factual errors, vague claims, missing edge cases,
   tone mismatch).
2. Produce a REVISED response that addresses every critique bullet.
   Do NOT change anything that was already correct.
3. The REVISED response must follow the same format the DRAFT used —
   if the DRAFT was code, the revised version is code; if the DRAFT
   was prose, the revised version is prose.

OUTPUT FORMAT (use exactly these markers):

<critique>
- bullet 1
- bullet 2
</critique>

<revised>
…the revised response, ready to ship to the user…
</revised>

Rules:
- Output ONLY the two blocks above. Nothing before, nothing after.
- If the DRAFT is already excellent, the <revised> block must repeat
  it verbatim and the <critique> block should say "no material issues".
- Never invent new requirements not present in the TASK.
- Never refer to "the user" in the revised block — it goes straight
  to them and the framing must match the original DRAFT's voice.`

// Execute runs the refine cycle. When the task starts with
// RefineDirective the agent extracts the "Task:" and "Draft:" sections
// from the body; otherwise the whole task is treated as the draft to
// be refined and a synthesized "TASK: improve quality" is used.
//
// The returned AgentResult.Output is the contents of the <revised>
// block (so callers can swap in the new text without parsing); the
// raw critique is preserved in the bracketed prefix when present so
// downstream Reflexion can inspect it.
func (a *RefinerAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	startTime := time.Now()
	callID := nextCallID()

	body := task
	originalTask := ""
	draft := task
	if strings.HasPrefix(task, RefineDirective) {
		body = strings.TrimSpace(strings.TrimPrefix(task, RefineDirective))
		originalTask, draft = parseRefineSections(body)
	}
	if originalTask == "" {
		originalTask = "Improve the quality and accuracy of the following draft."
	}

	user := fmt.Sprintf("TASK:\n%s\n\nDRAFT:\n%s", originalTask, draft)
	history := []models.Message{
		{Role: "system", Content: a.SystemPrompt()},
		{Role: "user", Content: user},
	}

	response, err := deps.LLMClient.SendPrompt(ctx, "", history, 0)
	if err != nil {
		return &AgentResult{
			CallID:   callID,
			Agent:    a.Type(),
			Task:     task,
			Error:    fmt.Errorf("refiner LLM call failed: %w", err),
			Duration: time.Since(startTime),
		}, err
	}

	revised, _ := parseRefinerResponse(response)
	if strings.TrimSpace(revised) == "" {
		// Model didn't follow the protocol — treat the whole response
		// as the revised draft so the caller still gets something
		// usable. Reflexion can pick up the protocol violation.
		revised = response
	}

	return &AgentResult{
		CallID:   callID,
		Agent:    a.Type(),
		Task:     task,
		Output:   revised,
		Duration: time.Since(startTime),
	}, nil
}

// parseRefineSections splits a "Task: …\n\nDraft: …" body into its two
// halves. Tolerant of extra whitespace and case in the headers.
func parseRefineSections(body string) (originalTask, draft string) {
	lower := strings.ToLower(body)
	idx := strings.Index(lower, "draft:")
	if idx < 0 {
		return "", body
	}
	taskHeader := strings.Index(lower, "task:")
	if taskHeader < 0 {
		return "", strings.TrimSpace(body[idx+len("draft:"):])
	}
	originalTask = strings.TrimSpace(body[taskHeader+len("task:") : idx])
	draft = strings.TrimSpace(body[idx+len("draft:"):])
	return
}

// parseRefinerResponse pulls the <revised>…</revised> body and the
// <critique>…</critique> body out of the response. Either may be
// empty if the model deviates.
func parseRefinerResponse(s string) (revised, critique string) {
	revised = extractBlock(s, "<revised>", "</revised>")
	critique = extractBlock(s, "<critique>", "</critique>")
	return
}

func extractBlock(s, openTag, closeTag string) string {
	start := strings.Index(s, openTag)
	if start < 0 {
		return ""
	}
	start += len(openTag)
	end := strings.Index(s[start:], closeTag)
	if end < 0 {
		return strings.TrimSpace(s[start:])
	}
	return strings.TrimSpace(s[start : start+end])
}
