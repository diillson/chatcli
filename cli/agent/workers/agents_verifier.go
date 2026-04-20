/*
 * ChatCLI - Verifier agent (Phase 6 of seven-pattern rollout: CoVe).
 *
 * VerifierAgent implements Chain-of-Verification: it generates a small
 * set of independent verification questions about claims in a draft
 * answer, answers each one in isolation, then either confirms the
 * draft or produces a corrected revision that addresses discrepancies.
 *
 * Like RefinerAgent, this is pure-reasoning (no tool access) and is
 * used both directly via <agent_call name="verifier"> and through the
 * quality.VerifyHook PostHook.
 */
package workers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

// VerifyDirective is the leading marker that switches the agent into
// structured input mode (parses Task: + Draft: sections like the
// refiner). Without it, the entire task body is treated as the draft
// to verify.
const VerifyDirective = "[VERIFY_ANSWER]"

// VerifierAgent runs CoVe over a draft.
//
// Default effort: "high" — verification is the most reasoning-heavy
// of the quality patterns; the model needs room to enumerate claims,
// answer each independently, and decide whether to rewrite. Override
// via CHATCLI_AGENT_VERIFIER_EFFORT.
type VerifierAgent struct {
	BuiltinAgentMeta
	skills *SkillSet
}

// NewVerifierAgent constructs a VerifierAgent.
func NewVerifierAgent() *VerifierAgent {
	a := &VerifierAgent{
		BuiltinAgentMeta: NewBuiltinAgentMeta("VERIFIER", "", "high"),
		skills:           NewSkillSet(),
	}
	a.skills.Register(&Skill{Name: "extract-claims", Description: "Enumerate verifiable claims in a draft answer", Type: SkillDescriptive})
	a.skills.Register(&Skill{Name: "verify-claims", Description: "Generate and answer independent verification questions", Type: SkillDescriptive})
	a.skills.Register(&Skill{Name: "rewrite-on-discrepancy", Description: "Rewrite the draft to address verified discrepancies", Type: SkillDescriptive})
	return a
}

func (a *VerifierAgent) Type() AgentType  { return AgentTypeVerifier }
func (a *VerifierAgent) Name() string     { return "VerifierAgent" }
func (a *VerifierAgent) IsReadOnly() bool { return true }
func (a *VerifierAgent) AllowedCommands() []string {
	return []string{}
}
func (a *VerifierAgent) Description() string {
	return "Pure-reasoning Chain-of-Verification (CoVe) pass: generates verification questions about claims in a draft, " +
		"answers each independently, and either confirms or rewrites the draft. Use when factual accuracy matters."
}
func (a *VerifierAgent) SystemPrompt() string { return defaultVerifierPrompt }
func (a *VerifierAgent) Skills() *SkillSet    { return a.skills }

// DefaultNumVerificationQuestions is the baseline number of CoVe
// questions when the caller doesn't override it via "[NUM_QUESTIONS=N]"
// in the task body.
const DefaultNumVerificationQuestions = 3

const defaultVerifierPrompt = `You are a Chain-of-Verification (CoVe) agent in ChatCLI.
You receive a TASK and a DRAFT answer. Your job is to verify the DRAFT.

Procedure (do this internally):

1. List the verifiable factual or technical claims in the DRAFT.
2. For each claim, write a short, INDEPENDENT verification question
   that does not reference the draft itself.
3. Answer each verification question on its own merit.
4. Compare the verification answers to the DRAFT. Mark any
   discrepancies between what the DRAFT says and what verification
   shows.
5. If there are no discrepancies, the FINAL block must repeat the
   DRAFT verbatim and the STATUS line must be "verified-clean".
6. If there are discrepancies, the FINAL block must contain a
   rewritten answer that fixes them, and the STATUS line must be
   "verified-with-corrections".

OUTPUT FORMAT (use exactly these markers):

<status>verified-clean OR verified-with-corrections</status>

<questions>
- Q1
- Q2
- Q3
</questions>

<answers>
- A1
- A2
- A3
</answers>

<discrepancies>
- description (or "none")
</discrepancies>

<final>
…the answer ready to ship to the user…
</final>

Rules:
- Output ONLY the five blocks above. Nothing else.
- Never invent new requirements not in the TASK.
- Default to 3 verification questions unless the task body specifies otherwise via "[NUM_QUESTIONS=N]".`

// Execute runs the CoVe cycle. The returned AgentResult.Output is the
// contents of the <final> block (so callers can swap the user-facing
// answer); the discrepancy state is preserved on AgentResult via
// metadata that callers (Reflexion in Phase 4) can inspect.
//
// Discrepancy detection is surface-level: the parsed <status> wins,
// but if missing the agent infers from a non-empty <discrepancies>
// block.
func (a *VerifierAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	startTime := time.Now()
	callID := nextCallID()

	originalTask := ""
	draft := task
	if strings.HasPrefix(task, VerifyDirective) {
		body := strings.TrimSpace(strings.TrimPrefix(task, VerifyDirective))
		originalTask, draft = parseRefineSections(body) // same Task:/Draft: layout
	}
	if originalTask == "" {
		originalTask = "Verify the factual and technical accuracy of the following draft."
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
			Error:    fmt.Errorf("verifier LLM call failed: %w", err),
			Duration: time.Since(startTime),
		}, err
	}

	parsed := parseVerifierResponse(response)
	output := parsed.Final
	if strings.TrimSpace(output) == "" {
		// Protocol violation — return raw response so user still gets
		// something. Hook will log a warning.
		output = response
	}

	return &AgentResult{
		CallID:   callID,
		Agent:    a.Type(),
		Task:     task,
		Output:   output,
		Duration: time.Since(startTime),
	}, nil
}

// VerifierResponse is the parsed shape of a CoVe agent reply.
type VerifierResponse struct {
	Status        string
	Questions     []string
	Answers       []string
	Discrepancies string
	Final         string
}

// HasDiscrepancy reports whether the verifier flagged a correction.
func (v VerifierResponse) HasDiscrepancy() bool {
	if strings.EqualFold(strings.TrimSpace(v.Status), "verified-with-corrections") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(v.Status), "verified-clean") {
		return false
	}
	d := strings.TrimSpace(strings.ToLower(v.Discrepancies))
	if d == "" || d == "none" || d == "- none" {
		return false
	}
	return true
}

// ParseVerifierOutput extracts the five XML-ish blocks emitted by
// the verifier. Missing blocks yield empty strings/slices. Exported
// so quality.VerifyHook can introspect discrepancies without
// re-parsing in two places.
func ParseVerifierOutput(raw string) VerifierResponse {
	return VerifierResponse{
		Status:        extractBlock(raw, "<status>", "</status>"),
		Questions:     splitBulletLines(extractBlock(raw, "<questions>", "</questions>")),
		Answers:       splitBulletLines(extractBlock(raw, "<answers>", "</answers>")),
		Discrepancies: extractBlock(raw, "<discrepancies>", "</discrepancies>"),
		Final:         extractBlock(raw, "<final>", "</final>"),
	}
}

// parseVerifierResponse is the unexported alias used by
// VerifierAgent.Execute itself.
func parseVerifierResponse(raw string) VerifierResponse {
	return ParseVerifierOutput(raw)
}

// splitBulletLines turns "- a\n- b\n- c" into ["a","b","c"]. Tolerant
// of "*" and missing dashes.
func splitBulletLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "-")
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}
