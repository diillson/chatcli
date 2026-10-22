/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

// flakyPlugin fails a given number of times before succeeding, and says
// whether it is read-only.
type flakyPlugin struct {
	failures int
	err      error
	readOnly bool
	calls    int
}

func (p *flakyPlugin) Name() string             { return "@flaky" }
func (p *flakyPlugin) Description() string      { return "flaky" }
func (p *flakyPlugin) Usage() string            { return "flaky" }
func (p *flakyPlugin) Version() string          { return "1.0.0" }
func (p *flakyPlugin) Path() string             { return "" }
func (p *flakyPlugin) Schema() string           { return "" }
func (p *flakyPlugin) IsReadOnly([]string) bool { return p.readOnly }
func (p *flakyPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}
func (p *flakyPlugin) ExecuteWithStream(_ context.Context, _ []string, _ func(string)) (string, error) {
	p.calls++
	if p.calls <= p.failures {
		return "partial", p.err
	}
	return "ok", nil
}

func TestExecuteWithRetry(t *testing.T) {
	a := &AgentMode{logger: zap.NewNop()}
	ctx := context.Background()

	// A transient read failure is retried once and the note says why.
	p := &flakyPlugin{failures: 1, err: errors.New("dial tcp: i/o timeout"), readOnly: true}
	out, err := a.executeWithRetry(ctx, p, "@flaky", nil, nil)
	if err != nil || p.calls != 2 || !strings.HasPrefix(out, "[Transient failure on the first attempt") || !strings.HasSuffix(out, "ok") {
		t.Fatalf("retry: %q %v calls=%d", out, err, p.calls)
	}
	// Only once: a second transient failure is reported.
	p = &flakyPlugin{failures: 2, err: errors.New("HTTP 503 Service Unavailable"), readOnly: true}
	if _, err := a.executeWithRetry(ctx, p, "@flaky", nil, nil); err == nil || p.calls != 2 {
		t.Fatalf("second failure surfaces: %v calls=%d", err, p.calls)
	}
	// A permanent failure is not retried.
	p = &flakyPlugin{failures: 1, err: errors.New("HTTP 404 Not Found"), readOnly: true}
	if _, err := a.executeWithRetry(ctx, p, "@flaky", nil, nil); err == nil || p.calls != 1 {
		t.Fatalf("permanent: %v calls=%d", err, p.calls)
	}
	// A mutating tool is never retried, transient or not.
	p = &flakyPlugin{failures: 1, err: errors.New("connection reset by peer"), readOnly: false}
	if _, err := a.executeWithRetry(ctx, p, "@flaky", nil, nil); err == nil || p.calls != 1 {
		t.Fatalf("mutating: %v calls=%d", err, p.calls)
	}
	// A dead context is not retried either.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	p = &flakyPlugin{failures: 1, err: errors.New("i/o timeout"), readOnly: true}
	if _, err := a.executeWithRetry(canceled, p, "@flaky", nil, nil); err == nil || p.calls != 1 {
		t.Fatalf("canceled ctx: %v calls=%d", err, p.calls)
	}
	// Success on the first try passes through untouched.
	p = &flakyPlugin{readOnly: true}
	if out, err := a.executeWithRetry(ctx, p, "@flaky", nil, nil); err != nil || out != "ok" {
		t.Fatalf("first try: %q %v", out, err)
	}
}

func TestSkipAfterFailureAndReadOnlyLookup(t *testing.T) {
	m, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	m.RegisterBuiltinPlugin(&flakyPlugin{readOnly: true})
	m.RegisterBuiltinPlugin(plugins.NewBuiltinReadPlugin())
	a := &AgentMode{cli: &ChatCLI{pluginManager: m}, logger: zap.NewNop()}

	if !a.toolCallIsReadOnly(agent.ToolCall{Name: "@flaky"}) {
		t.Fatal("a read-only plugin's call is read-only")
	}
	if a.toolCallIsReadOnly(agent.ToolCall{Name: "@nope"}) {
		t.Fatal("an unknown tool counts as mutating")
	}
	if (&AgentMode{}).toolCallIsReadOnly(agent.ToolCall{Name: "@flaky"}) {
		t.Fatal("no plugin manager: mutating")
	}

	var out strings.Builder
	rendered := ""
	res := a.skipAfterFailure(2, agent.ToolCall{Name: "@coder"}, &out, func(s string) { rendered = s })
	if !res.IsError || res.ErrorCode != toolResultErrorCodeNotExecuted || !strings.Contains(res.Output, "Action 3 (@coder) not executed") {
		t.Fatalf("skip result: %+v", res)
	}
	if !strings.Contains(out.String(), "--- Action Result 3 (@coder) ---") || !strings.Contains(rendered, "not executed") {
		t.Fatalf("skip note: %q / %q", out.String(), rendered)
	}
	// A nil renderer is fine (one-shot surfaces).
	a.skipAfterFailure(0, agent.ToolCall{Name: "@x"}, &out, nil)
}
