/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/evals"
	"go.uber.org/zap"
)

func TestEvalModeFor(t *testing.T) {
	cases := []struct {
		in    string
		route bool
		want  string
	}{
		{"hello", false, evalModeChat},
		{"/coder fix it", false, evalModeCoder},
		{"expanded command body", true, evalModeCoder},
		{"/agent list pods", false, evalModeAgent},
		{"/run ls", false, evalModeAgent},
	}
	for _, c := range cases {
		if got := evalModeFor(c.in, c.route); got != c.want {
			t.Errorf("%q route=%v: %s want %s", c.in, c.route, got, c.want)
		}
	}
}

func TestWriteEvalRecordIsInertWithoutEnv(t *testing.T) {
	t.Setenv(evals.RecordEnv, "")
	dir := t.TempDir()
	c := &ChatCLI{logger: zap.NewNop()}
	c.writeEvalRecord(evalModeChat, nil)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("no record may be written without the env var")
	}
}

func TestWriteEvalRecordChat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rec.json")
	t.Setenv(evals.RecordEnv, p)
	ct := NewCostTrackerAt(t.TempDir())
	ct.RecordRealUsage("OPENAI", "gpt-x", &models.UsageInfo{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	c := &ChatCLI{
		logger:      zap.NewNop(),
		Provider:    "OPENAI",
		Model:       "gpt-x",
		costTracker: ct,
		history: []models.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "q"},
			{Role: "assistant", Content: "  The answer is 42.  "},
		},
	}
	c.writeEvalRecord(evalModeChat, nil)
	rec, err := evals.ReadRecord(p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Final != "The answer is 42." || rec.Mode != evalModeChat || rec.Turns != 1 || rec.Provider != "OPENAI" {
		t.Errorf("record: %+v", rec)
	}
	if rec.InputTokens != 100 || rec.OutputTokens != 20 || rec.Requests != 1 {
		t.Errorf("usage: in=%d out=%d req=%d", rec.InputTokens, rec.OutputTokens, rec.Requests)
	}
	if len(rec.Transcript) == 0 {
		t.Error("transcript missing")
	}
}

func TestBuildEvalRecordCoderToolsAndError(t *testing.T) {
	c := &ChatCLI{
		logger:         zap.NewNop(),
		lastAgentReply: "Fixed the off-by-one.",
		history: []models.Message{
			{Role: "user", Content: "fix"},
			{Role: "assistant", Content: "<plan>1. read</plan>\n" + `<tool_call name="@coder" args='{"cmd":"read","args":{"file":"a.go"}}' />`},
			{Role: "user", Content: "result"},
			{Role: "assistant", ToolCalls: []models.ToolCall{{Name: "@coder", Arguments: map[string]interface{}{"cmd": "write"}}}},
			{Role: "assistant", Content: "Fixed the off-by-one."},
		},
	}
	rec := c.buildEvalRecord(evalModeCoder, errors.New("boom"))
	if rec.Final != "Fixed the off-by-one." || rec.Turns != 3 || rec.Error != "boom" {
		t.Errorf("record: %+v", rec)
	}
	if len(rec.ToolCalls) != 2 || rec.ToolCalls[0].Name != "@coder" || !strings.Contains(rec.ToolCalls[1].Args, "write") {
		t.Errorf("tool calls: %+v", rec.ToolCalls)
	}

	// Without a captured prose answer the last assistant message stands in.
	c.lastAgentReply = ""
	if got := c.buildEvalRecord(evalModeCoder, nil).Final; got != "Fixed the off-by-one." {
		t.Errorf("fallback final: %q", got)
	}
}

type fakeJudgeClient struct {
	reply  string
	err    error
	prompt string
}

func (f *fakeJudgeClient) GetModelName() string { return "gpt-4o" }
func (f *fakeJudgeClient) SendPrompt(_ context.Context, prompt string, history []models.Message, _ int) (string, error) {
	f.prompt = prompt
	if len(history) != 1 || history[0].Role != "user" {
		return "", errors.New("judge must send a fresh single-message conversation")
	}
	return f.reply, f.err
}

func TestEvalJudgeSendBooksCost(t *testing.T) {
	fc := &fakeJudgeClient{reply: `{"score": 1}`}
	c := &ChatCLI{logger: zap.NewNop(), Client: fc, Provider: "OPENAI", Model: "gpt-4o", costTracker: NewCostTrackerAt(t.TempDir())}
	j, err := c.NewEvalJudge("", "")
	if err != nil {
		t.Fatal(err)
	}
	if j.Provider() != "OPENAI" || j.Model() != "gpt-4o" { // the session's model id
		t.Errorf("judge target: %s %s", j.Provider(), j.Model())
	}
	reply, cost, err := j.Send(context.Background(), "grade this")
	if err != nil || reply != `{"score": 1}` || fc.prompt != "grade this" {
		t.Fatalf("send: %q %v", reply, err)
	}
	if cost <= 0 {
		t.Errorf("judge spend must be booked on the cost tracker, got %v", cost)
	}

	fc.err = errors.New("down")
	if _, _, err := j.Send(context.Background(), "x"); err == nil {
		t.Error("errors propagate")
	}
}

func TestNewEvalJudgeNeedsAProvider(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop()}
	if _, err := c.NewEvalJudge("", ""); err == nil {
		t.Error("no session client and no target must fail")
	}
	if _, err := c.NewEvalJudge("OPENAI", "gpt-x"); err == nil {
		t.Error("no manager must fail")
	}
}
