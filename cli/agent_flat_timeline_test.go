/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/i18n"
	llmclient "github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// The in-turn notices of a framed theme are the lines their call sites
// used to format by hand, byte for byte.
func TestTurnNoticeFramedIsUnchanged(t *testing.T) {
	useTheme(t, "dark")
	r := agent.NewUIRenderer(zap.NewNop())
	legacy := func(mark, markColor, textColor, text string) string {
		return fmt.Sprintf("  %s %s", r.Colorize(mark, markColor), r.Colorize(text, textColor))
	}
	text := "Recovering: context too long (4.2 MB)"
	for kind, want := range map[turnNoticeKind]string{
		noticeInfo:     legacy("ℹ", agent.ColorGray, agent.ColorGray, text),
		noticeCaution:  legacy("ℹ", agent.ColorYellow, agent.ColorYellow, text),
		noticeWarn:     legacy("⚠", agent.ColorYellow, agent.ColorYellow, text),
		noticeError:    legacy("✖", agent.ColorRed, agent.ColorRed, text),
		noticeProgress: legacy("│", agent.ColorCyan, agent.ColorGray, text),
		noticeRewrite:  legacy("🗜", agent.ColorGray, agent.ColorGray, text),
		noticeRail:     legacy("│", agent.ColorGray, agent.ColorGray, text),
	} {
		if got := turnNotice(kind, text); got != want {
			t.Errorf("kind %d:\n got %q\nwant %q", kind, got, want)
		}
	}
	over := captureStdout(t, func() { printTurnNotice(noticeWarn, text, true) })
	if want := "\r\033[K" + legacy("⚠", agent.ColorYellow, agent.ColorYellow, text) + "\n"; over != want {
		t.Errorf("over the spinner:\n got %q\nwant %q", over, want)
	}
	plain := captureStdout(t, func() { printTurnNotice(noticeInfo, text, false) })
	if want := legacy("ℹ", agent.ColorGray, agent.ColorGray, text) + "\n"; plain != want {
		t.Errorf("on its own row:\n got %q\nwant %q", plain, want)
	}
	if got := flatNoticeOr(noticeInfo, text, "framed line"); got != "framed line" {
		t.Errorf("a framed theme keeps the call site's own line: %q", got)
	}
}

func TestTurnNoticeFlatSharesOneForm(t *testing.T) {
	useTheme(t, "onyx")
	for _, tc := range []struct {
		kind       turnNoticeKind
		text, want string
	}{
		{noticeWarn, "⚠️ Recovering: context too long", "  ▲ Recovering  context too long"},
		{noticeError, "System prompt exceeds the cap (4.2 MB)", "  ✗ System prompt exceeds the cap  (4.2 MB)"},
		{noticeRewrite, "Microcompact — 3 results trimmed", "  • Microcompact  3 results trimmed"},
		{noticeProgress, "Summarizing history", "  · Summarizing history"},
		{noticeInfo, "  ", "  • "},
		{noticeWarn, "Recoverable failure (context too long) — compacting", "  ▲ Recoverable failure (context too long)  compacting"},
		{noticeDone, "Job build (job-1) completed after 2s.", "  ✓ Job build (job-1) completed after 2s."},
	} {
		if got := plainText(turnNotice(tc.kind, tc.text)); got != tc.want {
			t.Errorf("%q:\n got %q\nwant %q", tc.text, got, tc.want)
		}
	}
	if got := plainText(flatNoticeOr(noticeInfo, "New instruction received", "framed")); got != "  • New instruction received" {
		t.Errorf("flat notice: %q", got)
	}
	if hasColoredEmoji(turnNotice(noticeRewrite, "🗜 trimmed")) {
		t.Error("a flat notice kept a pictograph")
	}
}

// While a reply streams, the status line adds an estimate of its tokens and
// marks the figure; the cost stays on real usage; the estimate is gone when
// the call ends.
func TestFlatStatusEstimatesTokensWhileStreaming(t *testing.T) {
	ct := NewCostTracker()
	ct.totalInputTokens, ct.totalCompletionTokens, ct.totalCostUSD = 18000, 400, 0.0412
	a := &AgentMode{cli: &ChatCLI{costTracker: ct}}
	status := func() string {
		frame, _ := a.buildTurnSpinnerFrame(4*time.Second, "model-x", false)
		return plainText(frame)
	}
	tokens := func(n int64) string { return i18n.T("agent.turn.flat.tokens", formatTokenCount64(n)) }

	useTheme(t, "onyx")
	ctx := a.withLiveOutput(context.Background())
	if line := status(); !strings.Contains(line, tokens(18400)) || strings.Contains(line, "~") {
		t.Fatalf("before any text streams the figure is the real one: %q", line)
	}
	llmclient.ReportStreamProgress(ctx, 4000)
	llmclient.ReportStreamProgress(ctx, 4000)
	est := a.liveOutputTokens()
	if est <= 0 || est != a.cli.estimateTokens64(8000) {
		t.Fatalf("estimate for 8000 chars = %d", est)
	}
	line := status()
	if !strings.Contains(line, "~"+tokens(18400+est)) {
		t.Errorf("streaming figure is marked and includes the estimate: %q", line)
	}
	if !strings.Contains(line, "$0.04") {
		t.Errorf("cost moved on an estimate: %q", line)
	}
	if ct.TotalTokens() != 18400 || ct.TotalCost() != 0.0412 {
		t.Errorf("the estimate reached the tracker: %d tokens, $%v", ct.TotalTokens(), ct.TotalCost())
	}
	a.liveOut.reset() // the call ended and real usage was recorded
	if line := status(); !strings.Contains(line, tokens(18400)) || strings.Contains(line, "~") {
		t.Errorf("after the call the figure is the real one again: %q", line)
	}

	// A session with no usage yet still shows the reply arriving.
	fresh := &AgentMode{cli: &ChatCLI{costTracker: NewCostTracker()}}
	freshCtx := fresh.withLiveOutput(context.Background())
	llmclient.ReportStreamProgress(freshCtx, 2)
	if frame, _ := fresh.buildTurnSpinnerFrame(time.Second, "model-x", false); !strings.Contains(plainText(frame), "~"+tokens(1)) || strings.Contains(plainText(frame), "$") {
		t.Errorf("first call of a session: %q", plainText(frame))
	}

	// A framed theme asks no provider for progress and shows no estimate.
	useTheme(t, "dark")
	a.liveOut.chars.Store(8000)
	framedCtx := a.withLiveOutput(context.Background())
	llmclient.ReportStreamProgress(framedCtx, 4000)
	if a.liveOut.chars.Load() != 0 {
		t.Errorf("a framed theme counted streamed text: %d", a.liveOut.chars.Load())
	}
	if line := status(); strings.Contains(line, "~") || strings.Contains(line, "tokens") {
		t.Errorf("the framed status line changed: %q", line)
	}
}
