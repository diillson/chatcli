/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// maxJudgeAnswer caps the candidate answer shown to the judge; maxJudgeTools
// caps the tool-call list. Both keep a runaway transcript from inflating the
// grading bill.
const (
	maxJudgeAnswer = 24 << 10
	maxJudgeTools  = 60
)

// JudgeRequest is what the judge sees: the task, the rubric, an optional gold
// reference, and the candidate's answer and tool trail. It never sees which
// model produced the answer.
type JudgeRequest struct {
	Task      string
	Rubric    string
	Reference string
	Answer    string
	ToolCalls []ToolCall
}

// JudgeVerdict is the judge's grade.
type JudgeVerdict struct {
	Score     float64 // [0,1]
	Reasoning string
	CostUSD   float64
}

// Sender sends one prompt to the judge model and returns the reply and what
// the call cost. The command layer wires it to an LLM client.
type Sender func(ctx context.Context, prompt string) (reply string, costUSD float64, err error)

// LLMJudge grades with an LLM through a Sender.
type LLMJudge struct {
	Send Sender
}

// judgeInstructions is English on purpose: models follow English grading
// instructions more reliably, whatever language the answer is in.
const judgeInstructions = `You are a strict, impartial evaluator of an AI assistant's output.
Grade the CANDIDATE ANSWER against the RUBRIC. Use the REFERENCE ANSWER, when present, as ground truth for facts; the candidate does not need to match its wording.
Judge only what the rubric asks. Do not reward length, confidence or politeness. Penalize factual errors, unmet constraints and instructions that were ignored.
The answer may be in any language; grade its content, and apply language requirements only when the rubric states them.

Reply with ONLY a JSON object, no prose and no code fence:
{"score": <number from 0.0 to 1.0>, "reasoning": "<one or two sentences>"}
Scale: 1.0 fully satisfies the rubric; 0.7 satisfies it with minor flaws; 0.4 partially; 0.0 fails it.`

// BuildJudgePrompt renders the grading prompt.
func BuildJudgePrompt(req JudgeRequest) string {
	var b strings.Builder
	b.WriteString(judgeInstructions)
	b.WriteString("\n\n### TASK GIVEN TO THE ASSISTANT\n")
	b.WriteString(strings.TrimSpace(req.Task))
	b.WriteString("\n\n### RUBRIC\n")
	b.WriteString(strings.TrimSpace(req.Rubric))
	if ref := strings.TrimSpace(req.Reference); ref != "" {
		b.WriteString("\n\n### REFERENCE ANSWER\n")
		b.WriteString(ref)
	}
	if len(req.ToolCalls) > 0 {
		b.WriteString("\n\n### TOOLS THE ASSISTANT CALLED (in order)\n")
		for i, tc := range req.ToolCalls {
			if i == maxJudgeTools {
				fmt.Fprintf(&b, "… (+%d more)\n", len(req.ToolCalls)-i)
				break
			}
			fmt.Fprintf(&b, "- %s %s\n", tc.Name, truncate(strings.ReplaceAll(tc.Args, "\n", " "), 160))
		}
	}
	b.WriteString("\n\n### CANDIDATE ANSWER\n")
	answer := req.Answer
	if len(answer) > maxJudgeAnswer {
		answer = answer[:maxJudgeAnswer] + "\n… [truncated]"
	}
	if strings.TrimSpace(answer) == "" {
		answer = "(empty)"
	}
	b.WriteString(answer)
	return b.String()
}

// Grade asks the judge once and parses its verdict.
func (j *LLMJudge) Grade(ctx context.Context, req JudgeRequest) (JudgeVerdict, error) {
	if j == nil || j.Send == nil {
		return JudgeVerdict{}, errors.New(i18n.T("evals.judge.unavailable"))
	}
	reply, cost, err := j.Send(ctx, BuildJudgePrompt(req))
	if err != nil {
		return JudgeVerdict{CostUSD: cost}, err
	}
	v, err := ParseVerdict(reply)
	v.CostUSD = cost
	return v, err
}

// ParseVerdict reads the judge's JSON leniently: the whole reply, a fenced
// block, or the first object carrying a "score". A score on a 0-10 or 0-100
// scale is normalized, since judges drift from instructions.
func ParseVerdict(reply string) (JudgeVerdict, error) {
	type raw struct {
		Score     json.Number `json:"score"`
		Reasoning string      `json:"reasoning"`
	}
	try := func(s string) (JudgeVerdict, bool) {
		var r raw
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		if dec.Decode(&r) != nil || r.Score == "" {
			return JudgeVerdict{}, false
		}
		f, err := r.Score.Float64()
		if err != nil || math.IsNaN(f) || f < 0 {
			return JudgeVerdict{}, false
		}
		switch {
		case f <= 1:
		case f <= 10:
			f /= 10
		case f <= 100:
			f /= 100
		default:
			return JudgeVerdict{}, false
		}
		return JudgeVerdict{Score: f, Reasoning: strings.TrimSpace(r.Reasoning)}, true
	}
	if raw, ok := extractJSON(reply); ok {
		if v, ok := try(string(raw)); ok {
			return v, nil
		}
	}
	for i := 0; i < len(reply); i++ {
		if reply[i] == '{' {
			if v, ok := try(reply[i:]); ok {
				return v, nil
			}
		}
	}
	return JudgeVerdict{}, errors.New(i18n.T("evals.judge.unparsable", truncate(strings.TrimSpace(reply), 200)))
}

// checkJudge runs a judge check: Samples calls, median score against the
// threshold. A failed call counts as a failed sample, never a silent pass.
func checkJudge(ctx context.Context, c *Check, out *Outcome, judge Judge, res CheckResult) CheckResult {
	if judge == nil {
		res.Detail = i18n.T("evals.judge.unavailable")
		return res
	}
	req := JudgeRequest{
		Task:      out.Prompt,
		Rubric:    c.Judge.Rubric,
		Reference: c.Judge.Reference,
		Answer:    out.Final,
	}
	if out.Record != nil {
		req.ToolCalls = out.Record.ToolCalls
	}
	samples := c.Judge.Samples
	if samples < 1 {
		samples = 1
	}
	var scores []float64
	var reasons []string
	var lastErr error
	for i := 0; i < samples; i++ {
		v, err := judge.Grade(ctx, req)
		res.CostUSD += v.CostUSD
		if err != nil {
			lastErr = err
			scores = append(scores, 0)
			continue
		}
		scores = append(scores, v.Score)
		if v.Reasoning != "" {
			reasons = append(reasons, v.Reasoning)
		}
	}
	res.Score = median(scores)
	res.Passed = res.Score >= c.Judge.Threshold
	detail := i18n.T("evals.judge.score", res.Score, c.Judge.Threshold)
	if len(reasons) > 0 {
		detail += " — " + reasons[0]
	}
	if lastErr != nil {
		detail += " (" + i18n.T("evals.judge.sample_failed", lastErr) + ")"
	}
	res.Detail = detail
	return res
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}
