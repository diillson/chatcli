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
	"sync"
	"testing"
	"time"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestParseMoALayers(t *testing.T) {
	t.Setenv("CHATCLI_MOA_LAYERS", "3")
	cases := []struct {
		in, prompt string
		layers     int
		err        bool
	}{
		{"what now?", "what now?", 3, false},
		{"--layers 2 what now?", "what now?", 2, false},
		{"--layers=4 what now?", "what now?", 4, false},
		{"--layers 9 q", "q", moa.MaxLayers, false},
		{"--layers 2", "", 2, false},
		{"--layersx q", "--layersx q", 3, false},
		{"--layers two q", "", 0, true},
	}
	for _, c := range cases {
		n, p, err := parseMoALayers(c.in)
		if (err != nil) != c.err || n != c.layers || p != c.prompt {
			t.Errorf("parseMoALayers(%q) = %d, %q, %v; want %d, %q, err=%v", c.in, n, p, err, c.layers, c.prompt, c.err)
		}
	}
}

func TestMoaLayersFromEnv(t *testing.T) {
	for raw, want := range map[string]int{"": 1, "x": 1, "0": 1, "2": 2, " 3 ": 3, "12": moa.MaxLayers} {
		t.Setenv("CHATCLI_MOA_LAYERS", raw)
		if got := moaLayersFromEnv(); got != want {
			t.Errorf("CHATCLI_MOA_LAYERS=%q → %d, want %d", raw, got, want)
		}
	}
}

func TestMoaPositionsMode(t *testing.T) {
	t.Setenv("CHATCLI_MOA_PANEL", "full")
	for raw, want := range map[string]string{"": "full", "LEAD": "lead", "off": "off", "full": "full", "nope": "full"} {
		if got := moaPositionsMode(raw); got != want {
			t.Errorf("moaPositionsMode(%q) = %q, want %q", raw, got, want)
		}
	}
}

func layeredOutcome() moa.Outcome {
	a, b, c := moa.Ref{Provider: "a", Model: "m"}, moa.Ref{Provider: "b"}, moa.Ref{Provider: "c"}
	return moa.Outcome{
		Layers: [][]moa.RefResult{
			{{Ref: a, Output: "## Verdict\nYes, migrate gradually. Then more."}, {Ref: b, Output: "No, wait a quarter. Because."}, {Ref: c, Err: errors.New("401\n{\"error\":1}")}},
			{{Ref: a, Output: "Yes, migrate gradually. Still."}, {Ref: b, Output: "Yes, but only after a canary. Now."}, {Ref: c, Err: moa.ErrSkipped}},
		},
		Stances: [][]moa.Stance{{"", "", ""}, {moa.StanceKept, moa.StanceRevised, ""}},
	}
}

func TestMoaPositionsReport(t *testing.T) {
	out := layeredOutcome()
	lead := moaPositionsReport(moaPanelLead, out)
	for _, want := range []string{
		moaPositionsHeading, "### Round 1\n", "- a:m: Yes, migrate gradually.\n", "- c: no answer (401 {\"error\":1})\n",
		"### Round 2 (cross-review", "- a:m [kept]: Yes, migrate gradually.\n", "- b [revised]: Yes, but only after a canary.\n",
	} {
		if !strings.Contains(lead, want) {
			t.Fatalf("lead report lacks %q:\n%s", want, lead)
		}
	}
	if strings.Count(lead, "- c") != 1 {
		t.Fatalf("a member left out of a round must not be listed in it:\n%s", lead)
	}
	full := moaPositionsReport(moaPanelFull, out)
	if !strings.Contains(full, "#### b [revised]\nYes, but only after a canary. Now.\n") {
		t.Fatalf("full report should carry whole answers:\n%s", full)
	}
	if moaPositionsReport(moaPanelOff, out) != "" || moaPositionsReport(moaPanelLead, moa.Outcome{}) != "" {
		t.Fatal("off (or nothing to report) renders nothing")
	}
	noLead := moa.Outcome{Layers: [][]moa.RefResult{{{Ref: moa.Ref{Provider: "x"}, Output: "| a | b |\n|---|---|\n| 1 | 2 |"}}}}
	if r := moaPositionsReport(moaPanelLead, noLead); !strings.Contains(r, "- x: | a | b |") {
		t.Fatalf("an answer without a prose lead falls back to its first characters:\n%s", r)
	}
	empty := moa.Outcome{Layers: [][]moa.RefResult{{{Ref: moa.Ref{Provider: "x"}, Output: "  "}}}}
	if r := moaPositionsReport(moaPanelLead, empty); !strings.Contains(r, "- x: no answer (empty answer)") {
		t.Fatalf("an empty answer is reported as such:\n%s", r)
	}
}

func TestMoaToolPanel_StreamsRoundsAndStances(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	p := newMoAToolPanel(func(s string) { mu.Lock(); lines = append(lines, s); mu.Unlock() }, time.Now())
	h := p.hooks()
	h.OnLayer(1, 1, 3) // a single round has no header
	h.OnLayer(1, 2, 3)
	h.OnDone(1, moa.RefResult{Ref: moa.Ref{Provider: "a"}, Output: "Yes, migrate now. More."}, moa.StanceNone)
	h.OnLayer(2, 2, 2)
	h.OnDone(2, moa.RefResult{Ref: moa.Ref{Provider: "a"}, Output: "Yes."}, moa.StanceKept)
	h.OnDone(2, moa.RefResult{Ref: moa.Ref{Provider: "b"}, Output: "No."}, moa.StanceRevised)
	h.OnDone(2, moa.RefResult{Ref: moa.Ref{Provider: "c"}, Err: errors.New("quota")}, moa.StanceNone)
	h.OnDone(2, moa.RefResult{Ref: moa.Ref{Provider: "d"}}, moa.StanceNone)
	p.synthesizing(moa.Ref{Provider: "agg", Model: "m"})

	got := strings.Join(lines, "\n")
	for _, want := range []string{
		i18n.T("moa.layers.first", 1, 2, 3), i18n.T("moa.layers.review", 2, 2, 2), "Yes, migrate now.",
		"= " + i18n.T("moa.layers.kept"), "↻ " + i18n.T("moa.layers.revised"), "✗ c", "quota",
		i18n.T("moa.panel.empty"), i18n.T("moa.panel.synthesizing", "agg:m"),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, i18n.T("moa.layers.first", 1, 1, 3)) {
		t.Fatalf("a single-round run should print no round header:\n%s", got)
	}
	newMoAToolPanel(nil, time.Now()).hooks().OnLayer(1, 2, 2) // nil sink is a no-op
}

func TestMoaRunRequest_LayersPositionsAndProgress(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "b", "agg", "envagg"}},
		answers:        map[string]string{"a": "Yes, migrate. A.", "b": "No, wait. B.", "agg": "AGG", "envagg": "ENV SYNTHESIS"},
	}
	ad := &moaPluginAdapter{cli: newMoaCLI(mgr, "agg", "m")}
	t.Setenv("CHATCLI_MOA_MODELS", "")
	t.Setenv("CHATCLI_MOA_AGGREGATOR", "envagg:x")
	t.Setenv("CHATCLI_MOA_PANEL", "")
	var progress []string
	out, err := ad.RunRequest(context.Background(), plugins.MoaRequest{Prompt: "q", Members: []string{"a", "b"}, Layers: 2},
		func(s string) { progress = append(progress, s) })
	if err != nil {
		t.Fatalf("RunRequest: %v", err)
	}
	for _, want := range []string{
		i18n.T("moa.tool.synthesized_layers", "a, b", 2, "envagg"), "### Round 1\n- a: Yes, migrate.",
		"### Round 2 (cross-review", moaSynthesisHeading + "ENV SYNTHESIS",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("result lacks %q:\n%s", want, out)
		}
	}
	if p := strings.Join(progress, "\n"); !strings.Contains(p, i18n.T("moa.layers.review", 2, 2, 2)) || !strings.Contains(p, "envagg:x") {
		t.Fatalf("progress should stream the rounds and the synthesis:\n%s", p)
	}

	off, err := ad.RunRequest(context.Background(), plugins.MoaRequest{Prompt: "q", Members: []string{"a", "b"}, Positions: "off"}, nil)
	if err != nil || strings.Contains(off, moaPositionsHeading) || !strings.HasSuffix(off, "\n\nENV SYNTHESIS") {
		t.Fatalf("positions=off should return only the header and the synthesis: %q (%v)", off, err)
	}
	t.Setenv("CHATCLI_MOA_LAYERS", "2")
	viaRun, err := ad.Run(context.Background(), "q", []string{"a", "b"}, "agg")
	if err != nil || !strings.Contains(viaRun, "### Round 2") || !strings.HasSuffix(viaRun, "AGG") {
		t.Fatalf("Run should honor CHATCLI_MOA_LAYERS and the explicit aggregator: %q (%v)", viaRun, err)
	}
}

func TestMoaRunRequest_Errors(t *testing.T) {
	if _, err := (&moaPluginAdapter{}).RunRequest(context.Background(), plugins.MoaRequest{Prompt: "q"}, nil); err == nil {
		t.Fatal("no CLI should fail")
	}
	mgr := &moaFakeManager{minimalManager: &minimalManager{providers: []string{"a"}}, fail: map[string]bool{"a": true}}
	ad := &moaPluginAdapter{cli: newMoaCLI(mgr, "agg", "m")}
	if _, err := ad.RunRequest(context.Background(), plugins.MoaRequest{Prompt: "q", Members: []string{"a"}}, nil); err == nil ||
		!strings.Contains(err.Error(), "client failed") {
		t.Fatalf("all members failing should surface the provider error, got %v", err)
	}
}

func TestRunMoACommand_Layers(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "b", "agg"}},
		answers:        map[string]string{"a": "[kept]\nYes, migrate. A.", "b": "No, wait. B.", "agg": "FINAL SYNTHESIS"},
	}
	c := newMoaCLI(mgr, "agg", "m")
	ch, err := NewContextHandlerAt(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatalf("NewContextHandlerAt: %v", err)
	}
	c.contextHandler = ch
	t.Setenv("CHATCLI_MOA_MODELS", "a:m1,b:m2")
	t.Setenv("CHATCLI_MOA_PANEL", "")
	t.Setenv("CHATCLI_MOA_LAYERS", "")
	out := captureStdout(t, func() { err = c.runMoACommand(context.Background(), "/moa --layers 2 replace the autoscaler?") })
	if err != nil {
		t.Fatalf("runMoACommand: %v", err)
	}
	for _, want := range []string{
		i18n.T("moa.layers.running", 2, 2), i18n.T("moa.layers.first", 1, 2, 2), i18n.T("moa.layers.review", 2, 2, 2),
		i18n.T("moa.layers.kept"), "FINAL SYNTHESIS", "2/2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if c.history[0].Content != "replace the autoscaler?" {
		t.Fatalf("the flag must not reach the recorded prompt: %q", c.history[0].Content)
	}
	out = captureStdout(t, func() { err = c.runMoACommand(context.Background(), "/moa --layers many q") })
	if !errors.Is(err, errMoALayers) || !strings.Contains(out, i18n.T("moa.layers.usage")) {
		t.Fatalf("a bad --layers should print usage and fail: %v\n%s", err, out)
	}
}

func TestMoaStanceBadge(t *testing.T) {
	if moaStanceBadge(moa.StanceNone) != "" || !strings.Contains(moaStanceBadge(moa.StanceRevised), "↻") ||
		!strings.Contains(moaStanceBadge(moa.StanceKept), "=") {
		t.Fatal("unexpected stance badges")
	}
}

func TestGetMoASuggestions(t *testing.T) {
	c := &ChatCLI{}
	texts := func(line string) []string {
		buf := prompt.NewBuffer()
		buf.InsertText(line, false, true)
		var out []string
		for _, s := range c.getMoASuggestions(*buf.Document()) {
			out = append(out, s.Text)
		}
		return out
	}
	if got := texts("/moa"); len(got) != 1 || got[0] != "/moa" {
		t.Fatalf("/moa → %v", got)
	}
	if got := texts("/moa --l"); len(got) != 1 || got[0] != "--layers" {
		t.Fatalf("/moa --l → %v", got)
	}
	if got := texts("/moa --layers "); len(got) != 3 {
		t.Fatalf("/moa --layers → %v", got)
	}
	if got := texts("/moa --layers 2 why "); len(got) != 0 {
		t.Fatalf("free text gets no suggestions, got %v", got)
	}
}

// moaCaptureClient records the history each call received.
type moaCaptureClient struct {
	mu        *sync.Mutex
	histories *[][]models.Message
	answer    string
}

func (c *moaCaptureClient) GetModelName() string { return "capture" }
func (c *moaCaptureClient) SendPrompt(_ context.Context, _ string, h []models.Message, _ int) (string, error) {
	c.mu.Lock()
	*c.histories = append(*c.histories, h)
	c.mu.Unlock()
	return c.answer, nil
}

type moaCaptureManager struct {
	*moaFakeManager
	mu        sync.Mutex
	histories [][]models.Message
}

func (m *moaCaptureManager) GetClient(provider, _ string) (client.LLMClient, error) {
	return &moaCaptureClient{mu: &m.mu, histories: &m.histories, answer: "answer from " + provider}, nil
}

func TestMoaRunRequest_MembersDoNotSeeTheAgentTranscript(t *testing.T) {
	mgr := &moaCaptureManager{moaFakeManager: &moaFakeManager{minimalManager: &minimalManager{providers: []string{"OPENAI", "XAI", "agg"}}}}
	c := &ChatCLI{Provider: "agg", Model: "m", manager: mgr, logger: zap.NewNop()}
	c.history = []models.Message{
		{Role: "system", Content: "You are the coder agent. Always open with <plan>…</plan> and call tools."},
		{Role: "user", Content: "fix the build"},
		{Role: "assistant", Content: `<plan>ask the panel</plan><tool_call name="@moa" args='{}' />`},
	}
	t.Setenv("CHATCLI_MOA_AGGREGATOR", "")
	ad := &moaPluginAdapter{cli: c}
	out, err := ad.RunRequest(context.Background(), plugins.MoaRequest{Prompt: "Is PDB maxUnavailable 0 safe?", Members: []string{"openai", "xai"}}, nil)
	if err != nil {
		t.Fatalf("RunRequest: %v", err)
	}
	if len(mgr.histories) != 3 {
		t.Fatalf("expected 2 members + 1 aggregator call, got %d", len(mgr.histories))
	}
	for i, h := range mgr.histories {
		for _, m := range h {
			if strings.Contains(m.Content, "coder agent") || strings.Contains(m.Content, "<plan>") {
				t.Fatalf("call %d received the agent transcript: %+v", i, h)
			}
		}
	}
	if !strings.Contains(out, "- openai: answer from OPENAI") || strings.Contains(out, "- OPENAI") ||
		!strings.Contains(out, i18n.T("moa.tool.synthesized", "openai, xai", "agg")) {
		t.Fatalf("members and aggregator should be labeled in lower case, as /moa does:\n%s", out)
	}
}

type moaRecordingRunner struct{ req plugins.MoaRequest }

func (r *moaRecordingRunner) Run(context.Context, string, []string, string) (string, error) {
	return "", errors.New("RunRequest should be used")
}
func (r *moaRecordingRunner) List(context.Context) (string, error) { return "", nil }
func (r *moaRecordingRunner) RunRequest(_ context.Context, req plugins.MoaRequest, _ func(string)) (string, error) {
	r.req = req
	return "ok", nil
}

// The coder flattens the tool call's JSON into argv before running the
// tool; every @moa option must come through that path intact.
func TestMoaToolCall_AgentArgvPath(t *testing.T) {
	r := &moaRecordingRunner{}
	plugins.SetMoaAdapter(r)
	t.Cleanup(func() { plugins.SetMoaAdapter(nil) })
	argv, err := parseToolArgsWithJSON(`{"cmd":"ask","args":{"prompt":"PDB com maxUnavailable 0?","layers":2,"positions":"full","models":["openai","xai"],"aggregator":"claudeai"}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := plugins.NewBuiltinMoaPlugin().Execute(context.Background(), argv); err != nil {
		t.Fatalf("execute %v: %v", argv, err)
	}
	got := r.req
	if got.Layers != 2 || got.Positions != "full" || got.Prompt != "PDB com maxUnavailable 0?" ||
		got.Aggregator != "claudeai" || len(got.Members) != 2 {
		t.Fatalf("argv %v reached the tool as %+v", argv, got)
	}
}
