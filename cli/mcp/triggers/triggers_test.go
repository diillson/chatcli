/*
 * ChatCLI - trigger engine tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package triggers

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func mkEvent(server, channel, content string) ChannelEvent {
	return ChannelEvent{
		ServerName: server,
		Channel:    channel,
		Content:    content,
		Timestamp:  time.Now().UTC(),
		Seq:        1,
	}
}

func TestEngine_NotifyEmitsAction(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()

	if err := e.SetRules([]Rule{
		{Name: "r1", Channel: "ci", Mode: ModeNotify},
	}); err != nil {
		t.Fatalf("SetRules: %v", err)
	}

	e.Dispatch(mkEvent("srv", "ci", "broken"))

	select {
	case a := <-e.Actions():
		if a.Rule.Name != "r1" {
			t.Errorf("Rule.Name = %q, want r1", a.Rule.Name)
		}
		if a.Mode != ModeNotify {
			t.Errorf("Mode = %q, want notify", a.Mode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no action emitted")
	}
}

func TestEngine_ChannelGlobMatchesPrefix(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	if err := e.SetRules([]Rule{{Name: "alerts", Channel: "alerts/*", Mode: ModeNotify}}); err != nil {
		t.Fatal(err)
	}

	for _, ch := range []string{"alerts/critical", "alerts/info"} {
		e.Dispatch(mkEvent("srv", ch, "x"))
	}
	// "errors" must NOT match
	e.Dispatch(mkEvent("srv", "errors", "x"))

	count := 0
loop:
	for {
		select {
		case <-e.Actions():
			count++
		case <-time.After(100 * time.Millisecond):
			break loop
		}
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestEngine_ContentRegexFilters(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	if err := e.SetRules([]Rule{{
		Name: "critical-only", ContentRegex: `(?i)critical|sev1`, Mode: ModeNotify,
	}}); err != nil {
		t.Fatal(err)
	}

	e.Dispatch(mkEvent("s", "c", "All good"))
	e.Dispatch(mkEvent("s", "c", "CRITICAL: prod down"))
	e.Dispatch(mkEvent("s", "c", "sev1 alert"))

	count := 0
loop:
	for {
		select {
		case <-e.Actions():
			count++
		case <-time.After(100 * time.Millisecond):
			break loop
		}
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestEngine_RateLimitSuppresses(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	if err := e.SetRules([]Rule{{
		Name: "rl", Channel: "ci", Mode: ModeNotify, RateLimit: 200 * time.Millisecond,
	}}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		e.Dispatch(mkEvent("s", "ci", "x"))
	}

	count := 0
loop:
	for {
		select {
		case <-e.Actions():
			count++
		case <-time.After(50 * time.Millisecond):
			break loop
		}
	}
	if count != 1 {
		t.Fatalf("rate-limited fires = %d, want 1", count)
	}

	time.Sleep(220 * time.Millisecond)
	e.Dispatch(mkEvent("s", "ci", "x"))

	select {
	case <-e.Actions():
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Fatal("rate-limit window did not reopen")
	}
}

func TestEngine_DedupSuppressesIdenticalContent(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	if err := e.SetRules([]Rule{{
		Name: "dd", Channel: "ci", Mode: ModeNotify, DedupWindow: 500 * time.Millisecond,
	}}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		e.Dispatch(mkEvent("s", "ci", "identical body"))
	}
	count := 0
loop:
	for {
		select {
		case <-e.Actions():
			count++
		case <-time.After(80 * time.Millisecond):
			break loop
		}
	}
	if count != 1 {
		t.Fatalf("dedup count = %d, want 1", count)
	}
}

func TestEngine_PauseStopsDispatch(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	if err := e.SetRules([]Rule{{Name: "p", Channel: "ci", Mode: ModeNotify}}); err != nil {
		t.Fatal(err)
	}
	e.Pause()
	e.Dispatch(mkEvent("s", "ci", "x"))
	select {
	case <-e.Actions():
		t.Fatal("paused engine emitted action")
	case <-time.After(80 * time.Millisecond):
		// expected
	}
	e.Resume()
	e.Dispatch(mkEvent("s", "ci", "x"))
	select {
	case <-e.Actions():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("resume did not re-enable dispatch")
	}
}

func TestEngine_AutoModeRequiresTools(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	err := e.SetRules([]Rule{{
		Name: "auto-no-tools", Channel: "*", Mode: ModeAuto, Prompt: "x",
	}})
	if err == nil || !strings.Contains(err.Error(), "tools whitelist") {
		t.Fatalf("expected tools whitelist error, got %v", err)
	}
}

func TestEngine_ConfirmActionGetsExpiry(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	if err := e.SetRules([]Rule{{
		Name: "c", Channel: "ci", Mode: ModeConfirm, Prompt: "go {{content}}",
	}}); err != nil {
		t.Fatal(err)
	}
	e.Dispatch(mkEvent("s", "ci", "issue 42"))

	a := <-e.Actions()
	if a.ExpiresAt.IsZero() {
		t.Errorf("confirm action should have ExpiresAt set")
	}
	if a.Prompt != "go issue 42" {
		t.Errorf("Prompt = %q, want 'go issue 42'", a.Prompt)
	}
}

func TestEngine_DuplicateRuleNamesRejected(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	err := e.SetRules([]Rule{
		{Name: "dup", Channel: "a", Mode: ModeNotify},
		{Name: "dup", Channel: "b", Mode: ModeNotify},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate rule name") {
		t.Fatalf("expected duplicate-name error, got %v", err)
	}
}

func TestEngine_BadRegexRejected(t *testing.T) {
	e := NewEngine(zap.NewNop())
	defer e.Close()
	err := e.SetRules([]Rule{{
		Name: "bad", ContentRegex: "[unterminated", Mode: ModeNotify,
	}})
	if err == nil || !strings.Contains(err.Error(), "invalid contentRegex") {
		t.Fatalf("expected regex error, got %v", err)
	}
}

func TestRule_RenderPrompt_DefaultsWhenEmpty(t *testing.T) {
	r := Rule{Name: "x", Mode: ModeNotify}
	got := r.renderPrompt(mkEvent("srv", "ci", "build failed"))
	if !strings.Contains(got, "build failed") || !strings.Contains(got, "srv/ci") {
		t.Errorf("default prompt = %q", got)
	}
}

func TestRule_RenderPrompt_AllPlaceholders(t *testing.T) {
	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	r := Rule{
		Name: "x", Mode: ModeNotify,
		Prompt: "[{{server}}/{{channel}}] #{{seq}} at {{timestamp}}: {{content}}",
	}
	ev := ChannelEvent{ServerName: "s", Channel: "c", Content: "hi", Seq: 9, Timestamp: ts}
	got := r.renderPrompt(ev)
	want := "[s/c] #9 at 2025-01-02T03:04:05Z: hi"
	if got != want {
		t.Errorf("rendered = %q, want %q", got, want)
	}
}
