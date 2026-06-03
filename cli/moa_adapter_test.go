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

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// moaFakeClient returns a fixed answer (or error) on SendPrompt.
type moaFakeClient struct {
	answer string
	err    error
}

func (c *moaFakeClient) GetModelName() string { return "moa-fake" }
func (c *moaFakeClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return c.answer, c.err
}

// moaFakeManager reuses minimalManager for the unused interface surface and
// overrides client/provider resolution with per-provider canned answers.
type moaFakeManager struct {
	*minimalManager
	answers map[string]string
	fail    map[string]bool
}

func (m *moaFakeManager) GetClient(provider, _ string) (client.LLMClient, error) {
	if m.fail[provider] {
		return nil, errors.New("client failed: " + provider)
	}
	ans, ok := m.answers[provider]
	if !ok {
		return nil, errors.New("no such provider: " + provider)
	}
	return &moaFakeClient{answer: ans}, nil
}

func (m *moaFakeManager) GetAvailableProviders() []string { return m.minimalManager.providers }

func newMoaCLI(mgr *moaFakeManager, provider, model string) *ChatCLI {
	return &ChatCLI{Provider: provider, Model: model, manager: mgr, logger: zap.NewNop()}
}

func TestMoaRun_Synthesizes(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "b"}},
		answers:        map[string]string{"a": "answer A", "b": "answer B", "agg": "FINAL SYNTHESIS"},
	}
	a := &moaPluginAdapter{cli: newMoaCLI(mgr, "agg", "m")}

	out, err := a.Run(context.Background(), "the question", nil, "")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(out, "FINAL SYNTHESIS") {
		t.Fatalf("expected synthesized answer, got %q", out)
	}
}

func TestMoaRun_SingleMemberNoSynth(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"only"}},
		answers:        map[string]string{"only": "the answer", "agg": "should not be used"},
	}
	a := &moaPluginAdapter{cli: newMoaCLI(mgr, "agg", "m")}

	out, err := a.Run(context.Background(), "q", nil, "")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if out != "the answer" {
		t.Fatalf("single member should return its answer verbatim, got %q", out)
	}
}

func TestMoaRun_ExplicitMembers(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "b", "c", "d", "e"}},
		answers:        map[string]string{"x": "ax", "y": "by", "agg": "SYNTH"},
	}
	a := &moaPluginAdapter{cli: newMoaCLI(mgr, "agg", "m")}

	out, err := a.Run(context.Background(), "q", []string{"x", "y:some-model"}, "")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(out, "SYNTH") {
		t.Fatalf("expected synthesis from explicit members, got %q", out)
	}
}

func TestMoaRun_AllFail(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "b"}},
		answers:        map[string]string{},
		fail:           map[string]bool{"a": true, "b": true},
	}
	a := &moaPluginAdapter{cli: newMoaCLI(mgr, "agg", "m")}

	if _, err := a.Run(context.Background(), "q", nil, ""); err == nil {
		t.Fatal("expected error when all members fail")
	}
}

func TestMoaRun_AggregatorFallback(t *testing.T) {
	// Two members succeed, but the aggregator provider is unavailable →
	// fall back to the best (longest) candidate instead of failing.
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "b"}},
		answers:        map[string]string{"a": "short", "b": "a much longer answer"},
	}
	a := &moaPluginAdapter{cli: newMoaCLI(mgr, "missing-agg", "m")}

	out, err := a.Run(context.Background(), "q", nil, "")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if out != "a much longer answer" {
		t.Fatalf("expected best-candidate fallback, got %q", out)
	}
}

func TestMoaList(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"openai", "anthropic"}},
		answers:        map[string]string{},
	}
	a := &moaPluginAdapter{cli: newMoaCLI(mgr, "openai", "gpt")}
	out, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if !strings.Contains(out, "openai") || !strings.Contains(out, "anthropic") {
		t.Fatalf("list missing providers: %q", out)
	}
}

func TestParseMember(t *testing.T) {
	m := parseMember("anthropic:claude-opus-4-8")
	if m.provider != "anthropic" || m.model != "claude-opus-4-8" {
		t.Fatalf("parseMember = %+v", m)
	}
	// Case is preserved (canonicalization happens at resolve time against the
	// registered provider keys, which are case-sensitive).
	m2 := parseMember("OpenAI")
	if m2.provider != "OpenAI" || m2.model != "" {
		t.Fatalf("parseMember bare = %+v", m2)
	}
}

func TestMoaCanonicalProvider(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"OPENAI", "CLAUDEAI"}},
		answers:        map[string]string{},
	}
	a := &moaPluginAdapter{cli: newMoaCLI(mgr, "OPENAI", "x")}
	if got := a.canonicalProvider("openai"); got != "OPENAI" {
		t.Fatalf("canonicalProvider(openai) = %q, want OPENAI", got)
	}
	if got := a.canonicalProvider("unknown"); got != "unknown" {
		t.Fatalf("unknown should pass through, got %q", got)
	}
}

// Members resolved from configured providers must keep the registry casing so
// the case-sensitive GetClient lookup succeeds (regression: lowercasing them
// made every member fail).
func TestMoaRun_PreservesProviderCase(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"OPENAI", "CLAUDEAI"}},
		answers:        map[string]string{"OPENAI": "a1", "CLAUDEAI": "a2", "AGG": "FINAL"},
	}
	a := &moaPluginAdapter{cli: newMoaCLI(mgr, "AGG", "m")}
	out, err := a.Run(context.Background(), "q", nil, "")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(out, "FINAL") {
		t.Fatalf("expected synthesis, got %q", out)
	}
}
