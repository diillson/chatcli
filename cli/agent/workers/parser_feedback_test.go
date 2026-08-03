/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"strings"
	"testing"
)

// TestCountAgentCallTags pins the attempt counter the dispatch loop uses to
// detect malformed calls the parser dropped silently.
func TestCountAgentCallTags(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want int
	}{
		"none":                {"just prose, no tags", 0},
		"one well formed":     {`<agent_call agent="coder" task="x" />`, 1},
		"two mixed forms":     {`<agent_call agent="a" task="t" /> text <agent_call agent="b" task="u">body</agent_call>`, 2},
		"malformed no close":  {`<agent_call agent="coder" task="broken`, 1},
		"missing attributes":  {`<agent_call foo="bar" />`, 1},
		"word boundary":       {`the <agent_calls> concept`, 0},
		"tag at end of text":  {`dispatching now: <agent_call`, 1},
		"self-close no space": {`<agent_call/>`, 1},
	} {
		t.Run(name, func(t *testing.T) {
			if got := CountAgentCallTags(tc.text); got != tc.want {
				t.Errorf("CountAgentCallTags(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// TestMalformedDetectionEndToEnd pins the exact failure mode from the field:
// a malformed tag parses to zero calls while the attempt counter still sees
// it — the delta is what triggers corrective feedback.
func TestMalformedDetectionEndToEnd(t *testing.T) {
	malformed := `I'll dispatch the specialist now:
<agent_call agent="database-architect" task="create the schema>` // missing closing quote + />
	calls, _ := ParseAgentCalls(malformed)
	attempts := CountAgentCallTags(malformed)
	if len(calls) != 0 {
		t.Fatalf("expected the malformed tag to be dropped by the parser, got %d calls", len(calls))
	}
	if attempts != 1 {
		t.Fatalf("attempt counter must still see the malformed tag, got %d", attempts)
	}
}

// TestMalformedAgentCallFeedback pins the corrective message contract: it
// names the count, shows the required syntax and forbids abandoning the
// delegation flow.
func TestMalformedAgentCallFeedback(t *testing.T) {
	msg := MalformedAgentCallFeedback(2, 1)
	for _, want := range []string{
		"[AGENT_CALL PARSE ERROR]",
		"2 of your <agent_call> tag(s)",
		"(1 parsed correctly and did dispatch)",
		`<agent_call agent="coder" task=`,
		"RE-EMIT",
		"Do NOT execute those tasks yourself",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("feedback must contain %q\n---\n%s", want, msg)
		}
	}
	if strings.Contains(MalformedAgentCallFeedback(1, 0), "parsed correctly") {
		t.Error("zero-parsed feedback must not mention parsed calls")
	}
}

// TestFormatResultsFailureEpilogue pins the re-dispatch directive appended
// when any worker failed — the counterpart nudge that keeps the
// orchestrator in the delegation flow after runtime failures.
func TestFormatResultsFailureEpilogue(t *testing.T) {
	okOnly := FormatResults([]AgentResult{{Agent: "coder", CallID: "ac-1", Task: "t", Output: "done"}})
	if strings.Contains(okOnly, "SQUAD FLOW") {
		t.Error("all-success results must not carry the failure epilogue")
	}

	withFail := FormatResults([]AgentResult{
		{Agent: "coder", CallID: "ac-1", Task: "t", Output: "done"},
		{Agent: "tester", CallID: "ac-2", Task: "u", Error: errTestBoom},
	})
	for _, want := range []string{
		"SQUAD FLOW — 1 task(s) FAILED",
		"RE-DISPATCH",
		"Do NOT absorb a failed specialist's task",
	} {
		if !strings.Contains(withFail, want) {
			t.Errorf("failure epilogue must contain %q\n---\n%s", want, withFail)
		}
	}
}

var errTestBoom = errBoom{}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
