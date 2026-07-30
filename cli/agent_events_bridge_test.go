/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agentevents"
)

func TestClassifyToolCall(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		argv     []string
		wantKind agentevents.ToolKind
		wantLocs int
	}{
		{"coder read with file", "@coder", []string{"read", "--file", "main.go"}, agentevents.KindRead, 1},
		{"coder write", "@coder", []string{"write", "--file", "out.go", "--content", "x"}, agentevents.KindEdit, 1},
		{"coder exec", "@coder", []string{"exec", "--cmd", "go test ./..."}, agentevents.KindExecute, 0},
		{"coder grep", "@coder", []string{"grep", "--pattern", "foo", "--path", "cli/"}, agentevents.KindSearch, 1},
		{"coder delete", "@coder", []string{"delete", "--file", "tmp.txt"}, agentevents.KindDelete, 1},
		{"coder move", "@coder", []string{"move", "--file", "a.go", "--dest", "b.go"}, agentevents.KindMove, 2},
		{"coder unknown subcmd", "@coder", []string{"mystery"}, agentevents.KindOther, 0},
		{"shell", "@shell", []string{"ls"}, agentevents.KindExecute, 0},
		{"git mixed case", "@Git", []string{"status"}, agentevents.KindExecute, 0},
		{"file", "@file", []string{"--path", "README.md"}, agentevents.KindRead, 1},
		{"websearch", "@websearch", []string{"query"}, agentevents.KindSearch, 0},
		{"webfetch", "@webfetch", []string{"--url", "https://x"}, agentevents.KindFetch, 0},
		{"memory", "@memory", nil, agentevents.KindThink, 0},
		{"unknown plugin", "@banana", []string{"x"}, agentevents.KindOther, 0},
		{"flag without value ignored", "@coder", []string{"read", "--file"}, agentevents.KindRead, 0},
		{"flag value that is a flag ignored", "@coder", []string{"read", "--file", "--path"}, agentevents.KindRead, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, locs := classifyToolCall(tc.tool, tc.argv)
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if len(locs) != tc.wantLocs {
				t.Errorf("locations = %d (%v), want %d", len(locs), locs, tc.wantLocs)
			}
		})
	}
}

func TestPlanToEvent(t *testing.T) {
	plan := &agent.TaskPlan{Tasks: []*agent.Task{
		{ID: 1, Description: "read files", Status: agent.TaskCompleted},
		{ID: 2, Description: "apply patch", Status: agent.TaskInProgress},
		{ID: 3, Description: "run tests", Status: agent.TaskPending},
		{ID: 4, Description: "deploy", Status: agent.TaskFailed, Error: "no creds"},
		nil,
	}}
	ev := planToEvent(plan)
	if len(ev.Entries) != 4 {
		t.Fatalf("entries = %d, want 4 (nil task skipped)", len(ev.Entries))
	}
	if ev.Entries[0].Status != "completed" || ev.Entries[1].Status != "in_progress" || ev.Entries[2].Status != "pending" {
		t.Errorf("unexpected statuses: %+v", ev.Entries)
	}
	// Failed tasks map to pending with the error folded into the content —
	// the ACP plan vocabulary has no failed status.
	if ev.Entries[3].Status != "pending" {
		t.Errorf("failed task status = %q, want pending", ev.Entries[3].Status)
	}
	if ev.Entries[3].Content != "deploy (failed: no creds)" {
		t.Errorf("failed task content = %q", ev.Entries[3].Content)
	}
	for _, e := range ev.Entries {
		if e.Priority != "medium" {
			t.Errorf("priority = %q, want medium", e.Priority)
		}
	}

	if got := planToEvent(nil); len(got.Entries) != 0 {
		t.Errorf("nil plan should yield empty event")
	}
}

// sinkRecorder captures events for assertions.
type sinkRecorder struct {
	thoughts []string
	messages []string
	starts   []agentevents.ToolCall
	ends     []agentevents.ToolCall
	plans    []agentevents.Plan
}

func (s *sinkRecorder) Thought(t string)                  { s.thoughts = append(s.thoughts, t) }
func (s *sinkRecorder) Message(t string)                  { s.messages = append(s.messages, t) }
func (s *sinkRecorder) ToolStart(tc agentevents.ToolCall) { s.starts = append(s.starts, tc) }
func (s *sinkRecorder) ToolEnd(tc agentevents.ToolCall)   { s.ends = append(s.ends, tc) }
func (s *sinkRecorder) PlanUpdate(p agentevents.Plan)     { s.plans = append(s.plans, p) }

func TestEmitHelpersNilSafe(t *testing.T) {
	a := &AgentMode{} // events nil — every helper must be a silent no-op
	a.emitThought("x")
	a.emitMessage("y")
	a.emitPlan()
	tc := a.emitToolStart("@coder", "t", "raw", []string{"read"})
	a.emitToolEnd(tc, "out", nil, "", 0)
	a.emitBlockedTool("@coder", "raw", "msg")
	if allowed, asked := a.requestActionPermission(agentevents.ToolCall{}, "r"); allowed || asked {
		t.Errorf("nil sink must not grant nor be consulted, got allowed=%v asked=%v", allowed, asked)
	}
}

func TestEmitToolLifecycle(t *testing.T) {
	rec := &sinkRecorder{}
	a := &AgentMode{events: rec}

	tc := a.emitToolStart("@coder", "Reading: main.go", `{"cmd":"read"}`, []string{"read", "--file", "main.go"})
	if len(rec.starts) != 1 {
		t.Fatalf("expected 1 start, got %d", len(rec.starts))
	}
	if rec.starts[0].Status != agentevents.StatusInProgress || rec.starts[0].Kind != agentevents.KindRead {
		t.Errorf("unexpected start: %+v", rec.starts[0])
	}
	if rec.starts[0].ID == "" {
		t.Error("start must carry an id")
	}

	a.emitToolEnd(tc, "file contents", nil, "", 0)
	if len(rec.ends) != 1 {
		t.Fatalf("expected 1 end, got %d", len(rec.ends))
	}
	end := rec.ends[0]
	if end.ID != tc.ID {
		t.Errorf("end id %q != start id %q", end.ID, tc.ID)
	}
	if end.Status != agentevents.StatusCompleted || end.IsError {
		t.Errorf("unexpected end: %+v", end)
	}
	if !end.OmitContent {
		t.Error("successful read must omit content (no file dumps in chat)")
	}

	// Failed exec: content kept, status failed.
	tc2 := a.emitToolStart("@coder", "exec", `{"cmd":"exec"}`, []string{"exec", "--cmd", "false"})
	a.emitToolEnd(tc2, "", errTest, "EXEC", 0)
	end2 := rec.ends[1]
	if end2.Status != agentevents.StatusFailed || !end2.IsError || end2.OmitContent {
		t.Errorf("unexpected failed end: %+v", end2)
	}
	if end2.Output == "" {
		t.Error("failed end with empty output must fall back to the error text")
	}
	if tc2.ID == tc.ID {
		t.Error("ids must be unique per run")
	}
}

func TestFeedbackLabelsNeverLeakInternalNames(t *testing.T) {
	// The label lands inside "The tool '%s' was executed…" and models echo
	// it to the user — it must be real tool names, never a placeholder.
	got := toolCallNamesLabel([]agent.ToolCall{{Name: "@coder"}, {Name: "@coder"}, {Name: "@file"}})
	if got != "@coder, @file" {
		t.Errorf("toolCallNamesLabel = %q", got)
	}
	if got := toolCallNamesLabel(nil); got != "@coder" {
		t.Errorf("empty batch label = %q", got)
	}

	blocks := []CommandBlock{{Language: "sh"}, {Language: "sh"}, {Language: "git"}, {Language: ""}}
	if got := commandBlockNamesLabel(blocks); got != "sh, git, shell" {
		t.Errorf("commandBlockNamesLabel = %q", got)
	}
	if got := commandBlockNamesLabel(nil); got != "shell" {
		t.Errorf("empty blocks label = %q", got)
	}
}

var errTest = errFixed("boom")

type errFixed string

func (e errFixed) Error() string { return string(e) }
