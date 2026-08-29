/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Executor ≠ reviewer: the prompts both roles receive, the deterministic
 * gate the engine runs itself, and the lenient verdict parsing. The
 * executor's self-report is never a verdict — it is merely input handed to
 * a fresh reviewer worker alongside the gate results the engine produced.
 */
package taskgraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// Verdict values recorded on an attempt.
const (
	verdictPass = "PASS"
	verdictFail = "FAIL"
)

// Model-facing prompt strings, named per house style (never inline literals).
const (
	executorPromptFmt = `You are executing ONE task of an approved task graph. Other tasks run in parallel — do ONLY this task, nothing beyond its scope.

TASK %s — %s

%s

When done, report concisely WHAT you changed and HOW you verified it. Your report will be checked by an independent reviewer against the validation contract; unverifiable claims fail review.`

	executorRetryFmt = `

PREVIOUS ATTEMPT %d FAILED VERIFICATION. Reviewer/gate feedback:
%s

Fix exactly what the feedback names, then complete the task.`

	reviewerPromptFmt = `You are an INDEPENDENT REVIEWER for one task of a task graph. The executor claims the task is complete; a self-report is NOT evidence. Verify against the contract using your read-only tools (read, search, tree).

TASK %s — %s

VALIDATION CONTRACT:
%s

DETERMINISTIC GATE RESULTS (commands already executed by the orchestrator engine, outputs are authoritative):
%s

EXECUTOR'S REPORT (tail):
%s

End your response with EXACTLY one final line:
VERDICT: PASS — <one line of concrete evidence>
or
VERDICT: FAIL — <what is missing or broken, concrete and actionable>`

	reviewerNoGateNote  = "(no gate commands in the contract — verify by inspection)"
	reviewerNoVerdict   = "reviewer produced no explicit verdict; treating as FAIL"
	gatePassedNote      = "passed"
	gateFailedNoteFmt   = "FAILED: %v"
	reviewWaivedByGraph = "review waived by plan"
)

// buildExecutorPrompt assembles the worker prompt: task text, dependency
// outputs referenced as #<depID>, and retry feedback when applicable.
func (e *Engine) buildExecutorPrompt(t *Task, attempt int, feedback string) string {
	prompt := t.Prompt
	e.mu.Lock()
	for _, dep := range t.Deps {
		out, ok := e.outputs[dep]
		if !ok || out == "" {
			continue
		}
		ref := "#" + dep
		if strings.Contains(prompt, ref) {
			prompt = strings.ReplaceAll(prompt, ref, headRunes(out, depContextHeadRunes))
		}
	}
	e.mu.Unlock()
	body := fmt.Sprintf(executorPromptFmt, t.ID, t.Title, prompt)
	if attempt > 1 && strings.TrimSpace(feedback) != "" {
		body += fmt.Sprintf(executorRetryFmt, attempt-1, tailRunes(feedback, outputTailRunes))
	}
	return body
}

// runGate executes every Run command of the task's contract through the
// engine's own GateRunner. Returns ok=false with actionable feedback on the
// first failing step. Prose-only steps (empty Run) are skipped here — they
// are the reviewer's job.
func (e *Engine) runGate(ctx context.Context, t *Task, a *Attempt) (bool, string) {
	for _, step := range t.Validation {
		cmd := strings.TrimSpace(step.Run)
		if cmd == "" {
			continue
		}
		if e.cfg.Gate == nil {
			return false, "validation contract has gate commands but no gate runner is available"
		}
		gateCtx, cancel := context.WithTimeout(ctx, gateStepTimeout)
		out, err := e.cfg.Gate.RunGateStep(gateCtx, e.cfg.Workspace, cmd, nil)
		cancel()
		result := GateResult{Run: cmd, Output: tailRunes(out, outputTailRunes), Passed: err == nil}
		e.mu.Lock()
		a.Gate = append(a.Gate, result)
		e.persistLocked()
		e.mu.Unlock()
		detail := gatePassedNote
		if err != nil {
			detail = fmt.Sprintf(gateFailedNoteFmt, err)
		}
		e.emit(Event{Task: t.ID, Type: EventGateResult, Detail: cmd + ": " + detail})
		if err != nil {
			return false, fmt.Sprintf("gate command %q failed (%v). Output tail:\n%s", cmd, err, result.Output)
		}
	}
	return true, ""
}

// dispatchReviewer sends the contract to a fresh reviewer worker and parses
// its verdict. The reviewer is always a distinct dispatch from the executor
// (fresh worker, own run ID) — recorded on the attempt as proof.
func (e *Engine) dispatchReviewer(ctx context.Context, t *Task, attempt int, a *Attempt, executorOutput string) (verdict, evidence, runID string) {
	callID := fmt.Sprintf("tg:%s:r%d", t.ID, attempt)
	e.trackCall(callID, t.ID)
	defer e.untrackCall(callID)

	prompt := fmt.Sprintf(reviewerPromptFmt,
		t.ID, t.Title,
		formatContract(t.Validation),
		formatGateResults(a.Gate),
		tailRunes(executorOutput, outputTailRunes))

	batch := e.cfg.Dispatcher.Dispatch(ctx, []workers.AgentCall{{
		Agent: workers.AgentTypeReviewer,
		Task:  prompt,
		ID:    callID,
	}})
	if len(batch) == 0 {
		return verdictFail, "reviewer dispatch returned no result", ""
	}
	res := batch[0]
	runID = res.Metadata["run_id"]
	if res.Error != nil {
		return verdictFail, "reviewer failed: " + res.Error.Error(), runID
	}
	v, ev := parseVerdict(res.Output)
	return v, ev, runID
}

// parseVerdict hunts the LAST "VERDICT:" line, leniently: substring match,
// case-insensitive, PASS wins only when explicit. No verdict = FAIL — the
// burden of proof is on the review, never on absence of evidence.
func parseVerdict(output string) (string, string) {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		upper := strings.ToUpper(line)
		idx := strings.Index(upper, "VERDICT")
		if idx < 0 {
			continue
		}
		rest := line[idx:]
		restUpper := strings.ToUpper(rest)
		evidence := strings.TrimSpace(strings.TrimLeft(rest[len("VERDICT"):], ": -—"))
		evidence = strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(evidence, "PASS"), "FAIL"), " -—:"))
		if evidence == "" {
			evidence = firstLine(output)
		}
		switch {
		case strings.Contains(restUpper, verdictPass):
			return verdictPass, evidence
		case strings.Contains(restUpper, verdictFail):
			return verdictFail, evidence
		}
	}
	return verdictFail, reviewerNoVerdict + "; reviewer output tail: " + tailRunes(output, 400)
}

// formatContract renders the validation steps for the reviewer prompt.
func formatContract(steps ValidationList) string {
	if len(steps) == 0 {
		return "(none declared — verify the task's title/prompt was genuinely delivered)"
	}
	var b strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. ", i+1)
		switch {
		case s.Run != "" && s.Expect != "":
			fmt.Fprintf(&b, "run `%s` — expect: %s", s.Run, s.Expect)
		case s.Run != "":
			fmt.Fprintf(&b, "run `%s` — expect success", s.Run)
		default:
			b.WriteString(s.Expect)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatGateResults renders executed gate outcomes for the reviewer prompt.
func formatGateResults(results []GateResult) string {
	if len(results) == 0 {
		return reviewerNoGateNote
	}
	var b strings.Builder
	for _, r := range results {
		status := verdictPass
		if !r.Passed {
			status = verdictFail
		}
		fmt.Fprintf(&b, "$ %s → %s\n", r.Run, status)
		if out := strings.TrimSpace(r.Output); out != "" {
			fmt.Fprintf(&b, "%s\n", tailRunes(out, 600))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
