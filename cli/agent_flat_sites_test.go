/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/scheduler"
	llmclient "github.com/diillson/chatcli/llm/client"
)

// The call sites that printed a literal emoji past every monoglyph hook are
// exercised here: each one's own printing function runs under a monoglyph
// theme and what reaches the terminal is checked for pictographs. The same
// call under a framed theme is the control that the emoji is really there
// to be removed.
func TestConvertedCallSitesPrintNoEmojiUnderMonoglyph(t *testing.T) {
	sites := map[string]func(){
		"prompt badges (cli.go changeLivePrefix)": func() {
			c := &ChatCLI{isRemote: true, pendingResumeQueue: []string{"a", "b"}}
			prefix, _ := c.changeLivePrefix()
			os.Stdout.WriteString(prefix + "\n")
		},
		"context chunk detail (context_io.go)": func() {
			printSelectedChunksFeedback(&ctxmgr.FileContext{Chunks: []ctxmgr.FileChunk{{Index: 1, Description: "core", TotalSize: 2048}}}, []int{1})
		},
		"MCP trust warning (mcp_command.go)": func() {
			(&ChatCLI{}).renderMCPServerMetadata("", mcp.ServerConfig{Name: "fs", Trust: true})
		},
		"undelivered queue (agent_typeahead.go)": func() {
			(&AgentMode{salvagedLines: []string{"run the tests too"}}).reportUnconsumedTypeahead()
		},
		"parked draft (agent_typeahead_draft.go)": func() {
			(&AgentMode{}).announceParkedDraft("also update the changelog")
		},
		"job outcome (scheduler_wait.go)": func() {
			printJobOutcome(noticeJob(scheduler.StatusCompleted), false)
			printJobOutcome(noticeJob(scheduler.StatusTimedOut), false)
			printJobOutcome(noticeJob(scheduler.StatusRunning), true)
		},
		"park box (agent_park.go)": func() {
			renderParkBanner(&park.Snapshot{Token: "0f3ded97aa11bb22"}, park.Request{Mode: park.ModeDelay, Delay: time.Minute})
		},
	}
	for name, site := range sites {
		useTheme(t, "dark")
		framed := captureStdout(t, site)
		useTheme(t, "onyx")
		mono := captureStdout(t, site)
		if strings.TrimSpace(plainText(mono)) == "" {
			t.Errorf("%s: printed nothing under onyx", name)
		}
		if hasColoredEmoji(mono) || strings.Contains(mono, "✎") {
			t.Errorf("%s: a pictograph reached the terminal under onyx: %q", name, mono)
		}
		if !hasColoredEmoji(framed) && !strings.Contains(framed, "✎") {
			t.Errorf("%s: the control lost its emoji under dark (is the site still the one that prints it?): %q", name, framed)
		}
	}

	// These cannot be called without a live session behind them (a model
	// client, an MCP manager with a server in the auth-required state, a
	// memory store, a history compactor, the one-shot executor), so their
	// routing is checked in the source. The helper each one routes through
	// is behavior-tested above and in the notice tests.
	for _, site := range []struct{ file, routed string }{
		{"agent_mode.go", `fmt.Println(kit.Plain("⚠️ " + errMsg))`},                              // RunOnce: dangerous auto-exec abort
		{"agent_command_blocks.go", `fmt.Print(kit.Plain(errMsg))`},                              // runSingleCommand: command error
		{"mcp_command.go", `icon = kit.Plain("🔒")`},                                              // mcpShowStatus: auth-required server
		{"memory_command.go", `label = kit.Plain("🔒 ") + k`},                                     // showMemoryProfile: sensitive field
		{"compact_command.go", `flatNoticeOr(noticeDone, compactedMsg,`},                         // /compact result
		{"agent_turn_rewrites.go", `printTurnNotice(noticeRewrite, i18n.T("agent.skills.aged",`}, // skill aging pass
	} {
		src, err := os.ReadFile(site.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), site.routed) {
			t.Errorf("%s no longer routes its emoji: want %s", site.file, site.routed)
		}
	}
}

// The bug this guards: an MCP call cancelled mid-flight left the loop
// without closing its step, so it vanished from the flat timeline while a
// cancelled plugin call kept its line. Every place the tool loop gives up
// a batch on cancellation closes the step first. The loop has no fake to
// run it under, so the pairing is checked where it is written; what the
// closed step looks like is behavior-tested with the renderer.
func TestEveryCancelledCallClosesItsTimelineStep(t *testing.T) {
	src, err := os.ReadFile("agent_mode.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	exits := 0
	for i, line := range lines {
		if !strings.Contains(line, "a.closeNativeBatch(nativeToolCalls, turnToolResults, toolResultNotExecutedCanceled)") {
			continue
		}
		exits++
		if i == 0 || !strings.Contains(lines[i-1], `renderer.FlatToolRefused(agent.FlatCanceled, "")`) {
			t.Errorf("agent_mode.go:%d gives up a cancelled call without closing its timeline step", i+1)
		}
	}
	if exits < 2 {
		t.Fatalf("expected the MCP and the plugin cancellation exits, found %d", exits)
	}
}

// A retried attempt re-streams the reply from its start: the estimate must
// start over with it instead of adding the new attempt to the partial one.
func TestLiveEstimateRestartsWithARetriedAttempt(t *testing.T) {
	useTheme(t, "onyx")
	a := &AgentMode{cli: &ChatCLI{costTracker: NewCostTracker()}}
	ctx := a.withLiveOutput(context.Background())
	llmclient.ReportStreamProgress(ctx, 3000) // first attempt, cut off mid-reply
	if got := a.liveOut.chars.Load(); got != 3000 {
		t.Fatalf("first attempt: %d chars", got)
	}
	llmclient.RestartStreamProgress(ctx)
	if got := a.liveOut.chars.Load(); got != 0 {
		t.Fatalf("a restart left %d chars from the abandoned attempt", got)
	}
	llmclient.ReportStreamProgress(ctx, 4200)
	if got := a.liveOut.chars.Load(); got != 4200 {
		t.Fatalf("the retried attempt counts alone: %d", got)
	}
	// An auxiliary request made under the turn's context is not counted.
	llmclient.ReportStreamProgress(llmclient.WithoutStreamProgress(ctx), 900)
	if got := a.liveOut.chars.Load(); got != 4200 {
		t.Fatalf("an auxiliary request reached the meter: %d", got)
	}
}
