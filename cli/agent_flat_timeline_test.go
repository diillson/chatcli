/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/i18n"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/ui/theme"
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

// The live line is painted in place and erased without leaving a row
// behind; the spinner keeps the blank row it has always left.
func TestLiveLineIsReplacedInPlace(t *testing.T) {
	useTheme(t, "onyx")
	prev := stdoutIsTerminal
	t.Cleanup(func() { stdoutIsTerminal = prev })
	stdoutIsTerminal = func() bool { return true }

	run := func(start func(am *AnimationManager, painted chan struct{})) string {
		return captureStdout(t, func() {
			am := NewAnimationManager()
			painted := make(chan struct{}, 64)
			start(am, painted)
			select {
			case <-painted:
			case <-time.After(10 * time.Second):
				t.Error("the animation never painted")
			}
			am.StopThinkingAnimation()
			_ = os.Stdout.Sync()
		})
	}
	live := run(func(am *AnimationManager, painted chan struct{}) {
		am.ShowLiveLine(func() string {
			painted <- struct{}{}
			return "  running exec"
		})
		am.ShowLiveLine(func() string { return "ignored: one is already running" })
	})
	if !strings.Contains(live, "\r\033[K  running exec") || strings.Contains(live, "ignored") {
		t.Errorf("live frames: %q", live)
	}
	if strings.Contains(live, "\n") || !strings.HasSuffix(live, "\r\033[K\033[0m\033[0m") {
		t.Errorf("stopping a live line must erase it and stay on the row: %q", live)
	}
	spinner := run(func(am *AnimationManager, painted chan struct{}) {
		am.ShowThinkingAnimation("thinking")
		painted <- struct{}{}
	})
	if !strings.HasSuffix(spinner, "\n") {
		t.Errorf("the spinner lost its trailing row: %q", spinner)
	}

	// Captured output: nothing is painted and nothing has to be stopped.
	stdoutIsTerminal = func() bool { return false }
	captured := captureStdout(t, func() {
		am := NewAnimationManager()
		am.ShowLiveLine(func() string { return "never" })
		am.StopThinkingAnimation()
	})
	if captured != "" {
		t.Errorf("a live line was painted into captured output: %q", captured)
	}
}

// Only an attended interactive run repaints; every other run gets the same
// final lines without the transient one.
func TestFlatToolLiveLineIsForAttendedRunsOnly(t *testing.T) {
	useTheme(t, "onyx")
	prev := stdoutIsTerminal
	t.Cleanup(func() { stdoutIsTerminal = prev })
	stdoutIsTerminal = func() bool { return true }
	r := agent.NewUIRendererWithStyle(zap.NewNop(), agent.UIStyleCompact)
	r.FlatToolQueue("@coder", "exec", `{"cmd":"exec","args":{"cmd":"make"}}`)
	r.FlatToolBegin()

	running := func(a *AgentMode) bool {
		var on bool
		_ = captureStdout(t, func() {
			a.showFlatToolLive(r)
			a.cli.animation.mu.Lock()
			on = a.cli.animation.isRunning
			a.cli.animation.mu.Unlock()
			a.cli.animation.StopThinkingAnimation()
		})
		return on
	}
	if !running(&AgentMode{cli: &ChatCLI{animation: NewAnimationManager()}}) {
		t.Error("an interactive run shows the live line")
	}
	if running(&AgentMode{isOneShot: true, cli: &ChatCLI{animation: NewAnimationManager()}}) {
		t.Error("a one-shot run must not repaint")
	}
	if running(&AgentMode{cli: &ChatCLI{unattended: true, animation: NewAnimationManager()}}) {
		t.Error("an unattended run must not repaint")
	}
	(&AgentMode{cli: &ChatCLI{}}).showFlatToolLive(r) // no animation wired: no panic
}

// The call sites that printed a literal emoji past every monoglyph hook,
// and the form each takes now. The source is checked because most of them
// sit in command handlers that need a live session to run.
func TestEmojiCallSitesRouteThroughMonoglyph(t *testing.T) {
	for _, site := range []struct{ file, routed string }{
		{"agent_mode.go", `fmt.Println(kit.Plain("⚠️ " + errMsg))`},
		{"agent_command_blocks.go", `fmt.Print(kit.Plain(errMsg))`},
		{"context_io.go", `fmt.Println(kit.Plain("    📦 " +`},
		{"mcp_command.go", `icon = kit.Plain("🔒")`},
		{"mcp_command.go", `ColorYellow, kit.Plain("⚠"), i18n.T("mcp.cmd.trust_mode_active")`},
		{"memory_command.go", `label = kit.Plain("🔒 ") + k`},
		{"cli.go", `prefix = kit.Plain("[" + strings.Join(icons, " ") + "] ")`},
		{"agent_turn_rewrites.go", `printTurnNotice(noticeRewrite, i18n.T("agent.microcompact.applied",`},
		{"agent_typeahead.go", `flatNoticeOr(noticeWarn, unconsumed,`},
		{"agent_typeahead_draft.go", `flatNoticeOr(noticeInfo, parked,`},
		{"scheduler_wait.go", `flatNoticeOr(noticeDone, doneMsg,`},
		{"scheduler_wait.go", `flatNoticeOr(noticeInfo, cycleMsg,`},
		{"scheduler_wait.go", `flatNoticeOr(noticeWarn, timedOut,`},
		{"agent_park.go", `flatNoticeOr(noticeRunning, resumed,`},
		{"agent_park.go", `if renderFlatParkBanner(snap, req) {`},
		{"agent_park_directives.go", `flatNoticeOr(noticeInfo, queued,`},
		{"compact_command.go", `flatNoticeOr(noticeDone, compactedMsg,`},
	} {
		src, err := os.ReadFile(site.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), site.routed) {
			t.Errorf("%s no longer routes its emoji: want %s", site.file, site.routed)
		}
	}
	// What those sites print under each kind of theme.
	useTheme(t, "onyx")
	if got := flatNoticeOr(noticeWarn, "2 queued", colorize("📭 2 queued", ColorYellow)); hasColoredEmoji(got) {
		t.Errorf("onyx: %q", got)
	}
	useTheme(t, "dark")
	if got := plainText(flatNoticeOr(noticeWarn, "2 queued", colorize("📭 2 queued", ColorYellow))); got != "📭 2 queued" {
		t.Errorf("dark keeps its emoji: %q", got)
	}
	_ = theme.Active()
}
