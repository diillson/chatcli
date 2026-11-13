/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
	"go.uber.org/zap"
)

// withTheme activates a theme for one test with a color-capable profile, so
// the output carries the escapes a real terminal would get.
func withTheme(t *testing.T, name string) {
	t.Helper()
	prev, prevProfile := theme.Active().Name, theme.ActiveProfile()
	if err := theme.SetActive(name); err != nil {
		t.Fatal(err)
	}
	theme.SetProfile(theme.ProfileTrueColor)
	t.Cleanup(func() {
		_ = theme.SetActive(prev)
		theme.SetProfile(prevProfile)
	})
}

const execArgs = `{"cmd":"exec","args":{"cmd":"go test ./..."}}`

// runFlatCall draws one call the way the agent loop drives the renderer.
func runFlatCall(t *testing.T, style UIStyle, stream []string, output string, failed bool) string {
	t.Helper()
	return runFlatCallExit(t, style, stream, output, failed, "")
}

func runFlatCallExit(t *testing.T, style UIStyle, stream []string, output string, failed bool, exit string) string {
	t.Helper()
	r := NewUIRendererWithStyle(zap.NewNop(), style)
	return stripANSI(captureStdout(t, func() {
		r.FlatToolQueue("@coder", "exec", execArgs)
		r.FlatToolBegin()
		r.RenderStreamBoxStart("🔨", "ignored", ColorPurple)
		for _, line := range stream {
			r.StreamOutput(line)
		}
		r.RenderStreamBoxEnd(ColorPurple)
		r.FlatToolDone(output, failed, 1200*time.Millisecond, exit)
	}))
}

func TestFlatTimelineOneLinePerCall(t *testing.T) {
	withTheme(t, "onyx")
	out := runFlatCall(t, UIStyleCompact, nil, "ok\tpkg/a\nok\tpkg/b", false)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("a call with no streamed output is one line, got %d:\n%s", len(lines), out)
	}
	line := lines[0]
	if !strings.HasPrefix(line, "  ✓ exec   go test ./...") {
		t.Errorf("state glyph, verb and argument lead the line: %q", line)
	}
	if !strings.HasSuffix(line, "1.2s · "+i18n.T("agent.flat.lines", 2)) {
		t.Errorf("metadata ends the line: %q", line)
	}
	if got := kit.VisibleLen(line); got != kit.ContentWidth() {
		t.Errorf("the line spans the content width: %d != %d", got, kit.ContentWidth())
	}
	if strings.ContainsAny(out, "╭╰🔨") {
		t.Errorf("flat output drew a frame or an emoji:\n%s", out)
	}
}

func TestFlatTimelineStylesDecideTheResultBody(t *testing.T) {
	withTheme(t, "onyx")
	result := "first row\nsecond row\nthird row"
	if out := runFlatCall(t, UIStyleCompact, nil, result, false); strings.Contains(out, "first row") {
		t.Errorf("compact shows no result body:\n%s", out)
	}
	minimal := runFlatCall(t, UIStyleMinimal, nil, result, false)
	if !strings.Contains(minimal, "  │ first row\n") || strings.Contains(minimal, "second row") {
		t.Errorf("minimal shows the first row under the rail:\n%s", minimal)
	}
	full := runFlatCall(t, UIStyleFull, nil, result, false)
	for _, row := range []string{"  │ first row\n", "  │ second row\n", "  │ third row\n"} {
		if !strings.Contains(full, row) {
			t.Errorf("full lacks %q:\n%s", row, full)
		}
	}
	long := runFlatCall(t, UIStyleFull, nil, strings.Repeat("z", flatResultBudget+500), false)
	if strings.Count(long, "z") > flatResultBudget {
		t.Errorf("full keeps the result within its budget: %d", strings.Count(long, "z"))
	}
}

func TestFlatTimelineStreamedOutputHangsFromTheCall(t *testing.T) {
	withTheme(t, "onyx")
	out := runFlatCall(t, UIStyleFull, []string{"ok  pkg/a", "ERR: warning: unused"}, "ok  pkg/a\nwarning: unused", false)
	want := "  ↻ exec   go test ./...\n  │ ok  pkg/a\n  │ warning: unused\n  └ ✓ 1.2s · " + i18n.T("agent.flat.lines", 2) + "\n"
	if out != want {
		t.Fatalf("streamed call:\n got %q\nwant %q", out, want)
	}
}

func TestFlatTimelineFailureAndRefusal(t *testing.T) {
	withTheme(t, "onyx")
	failed := runFlatCallExit(t, UIStyleCompact, nil, "FAIL pkg/a\n\n--- ERRO ---\nexit status 2", true, "2")
	if !strings.HasPrefix(failed, "  ✗ exec   go test ./...") || !strings.Contains(failed, "· "+i18n.T("agent.flat.exit", "2")+" ·") {
		t.Errorf("failed call:\n%s", failed)
	}
	if !strings.Contains(failed, "  │ exit status 2\n") {
		t.Errorf("a failure says why even in the compact style:\n%s", failed)
	}

	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	denied := stripANSI(captureStdout(t, func() {
		r.FlatToolQueue("@coder", "exec", execArgs)
		r.FlatToolRefused(FlatDenied, "blocked by policy")
		r.FlatToolDone("late", true, time.Second, "") // the loop's result step: already closed
	}))
	if !strings.HasPrefix(denied, "  ⊘ exec   go test ./...") || !strings.Contains(denied, i18n.T("agent.flat.denied")+"\n  │ blocked by policy\n") {
		t.Errorf("denied call:\n%s", denied)
	}
	if strings.Contains(denied, "late") {
		t.Errorf("a refused call was closed twice:\n%s", denied)
	}

	rejected := stripANSI(captureStdout(t, func() {
		r.FlatToolQueue("@coder", "exec", execArgs)
		r.FlatToolBegin()
		r.FlatToolRefused(FlatFailed, "args contain line breaks")
		r.FlatToolRefused(FlatFailed, "no call open")
	}))
	if !strings.HasPrefix(rejected, "  ✗ exec   go test ./...") || !strings.Contains(rejected, i18n.T("agent.flat.failed")+"\n") ||
		!strings.HasSuffix(rejected, "  ✗ no call open\n") {
		t.Errorf("rejected call:\n%s", rejected)
	}
}

func TestFlatTimelineAbandonedCallStaysInTheTimeline(t *testing.T) {
	withTheme(t, "onyx")
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	out := stripANSI(captureStdout(t, func() {
		r.FlatToolRefused(FlatCanceled, "") // nothing open: prints nothing
		r.FlatToolQueue("@park", "", `{"until":"10m"}`)
		r.FlatToolBegin()
		r.FlatToolRefused(FlatParked, "")
		r.FlatToolQueue("@coder", "exec", execArgs)
		r.FlatToolBegin()
		r.StreamOutput("partial")
		r.FlatToolRefused(FlatCanceled, "")
	}))
	want := []string{"  ⊘ park", i18n.T("agent.flat.parked"), "  ↻ exec   go test ./...", "  │ partial", "  └ ⊘ " + i18n.T("agent.flat.canceled")}
	at := 0
	for _, piece := range want {
		i := strings.Index(out[at:], piece)
		if i < 0 {
			t.Fatalf("missing %q (in order) in:\n%s", piece, out)
		}
		at += i
	}
	if strings.Contains(out, "nothing open") {
		t.Errorf("abandoning with no call open printed:\n%s", out)
	}
}

func TestFlatToolLiveFrameIsTheRunningLine(t *testing.T) {
	withTheme(t, "onyx")
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleCompact)
	r.FlatToolQueue("@webfetch", "", `{"url":"https://example.com/docs"}`)
	r.FlatToolBegin()
	frame := stripANSI(r.FlatToolLiveFrame())
	if !strings.HasPrefix(frame, "  ↻ webfetch https://example.com/docs") || !strings.HasSuffix(frame, "ms") {
		t.Fatalf("live frame: %q", frame)
	}
	if strings.Contains(frame, "\n") {
		t.Fatalf("the live frame is one row: %q", frame)
	}
}

func TestFlatToolLabel(t *testing.T) {
	for _, tc := range []struct{ tool, sub, args, verb, arg string }{
		{"@coder", "read", `{"cmd":"read","args":{"file":"main.go"}}`, "read", "main.go"},
		{"@coder", "git-status", `{"cmd":"git-status"}`, "git-status", "."},
		{"@webfetch", `{"url":"https://example.com"}`, `{"url":"https://example.com"}`, "webfetch", "https://example.com"},
		{"@scheduler", "list", `{"cmd":"list"}`, "scheduler list", ""},
		{"@websearch", "unknown", `{"query":"go  1.27\nrelease"}`, "websearch", "go 1.27 release"},
		{"mcp_fs_read", "--path", "--path /tmp/x", "mcp_fs_read", "/tmp/x"},
		{"@coder", "", "", "coder", ""},
		{"@memory", "note", "not json at all", "memory note", ""},
	} {
		verb, arg := flatToolLabel(tc.tool, tc.sub, tc.args)
		if verb != tc.verb || arg != tc.arg {
			t.Errorf("%s %s: got (%q, %q), want (%q, %q)", tc.tool, tc.sub, verb, arg, tc.verb, tc.arg)
		}
	}
}

func TestFlatToolLineKeepsMetadataOnNarrowTerminals(t *testing.T) {
	withTheme(t, "onyx")
	line := stripANSI(flatToolLine(kit.GlyphSuccess, "exec", strings.Repeat("long-argument ", 20), []string{"1.2s", "9 lines"}, 50))
	if kit.VisibleLen(line) > 50 || !strings.HasSuffix(line, "1.2s · 9 lines") || !strings.Contains(line, "…") {
		t.Fatalf("narrow line: %q (%d cols)", line, kit.VisibleLen(line))
	}
	for d, want := range map[time.Duration]string{120 * time.Millisecond: "120ms", 2500 * time.Millisecond: "2.5s", 95 * time.Second: "1m35s"} {
		if got := flatDuration(d); got != want {
			t.Errorf("flatDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if meta := flatToolMeta("", time.Second, ""); len(meta) != 1 {
		t.Errorf("an empty result has no size: %v", meta)
	}
	if meta := flatToolMeta("one", time.Second, ""); meta[1] != i18n.T("agent.flat.line") {
		t.Errorf("singular: %v", meta)
	}
}

func TestFlatBlockTitleCarriesStateInTheGlyph(t *testing.T) {
	withTheme(t, "onyx")
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	out := stripANSI(captureStdout(t, func() {
		r.RenderTimelineEvent("🧭", "PLAN", "read the file", ColorCyan)
		r.RenderTimelineEvent("❌", "FAILED", "boom", ColorRed)
		r.RenderTimelineEvent("✅", "DONE", "fine", ColorGreen)
	}))
	for _, want := range []string{"\n  ◆ PLAN\n    read the file\n", "\n  ✗ FAILED\n", "\n  ✓ DONE\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("flat block lacks %q:\n%q", want, out)
		}
	}
}

// Every theme that keeps its frames renders the renderer's sections exactly
// as before: the flat branches must not leak into them, and the flat calls
// the loop makes unconditionally must stay silent.
func TestFramedThemesKeepTheirTimeline(t *testing.T) {
	withTheme(t, "dark")
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	if r.FlatLayout() {
		t.Fatal("dark is not a flat theme")
	}
	silent := captureStdout(t, func() {
		r.FlatToolRefused(FlatCanceled, "")
		r.FlatToolDone("output", false, time.Second, "")
	})
	if silent != "" {
		t.Fatalf("flat calls printed under a framed theme: %q", silent)
	}
	recolor := func(s, color string) string { return theme.Recolor(color + s + ColorReset) }
	got := captureStdout(t, func() {
		r.RenderStreamBoxStart("🔨", "EXEC", ColorPurple)
		r.StreamOutput("line one")
		r.StreamOutput("ERR: careful")
		r.RenderStreamBoxEnd(ColorPurple)
		r.RenderTimelineEvent("🧭", "PLAN", "read the file", ColorCyan)
		r.CompactToolStart("Exec(go test)")
		r.CompactToolDone("Exec(go test)", "1.2s", false)
	})
	want := "\n  " + recolor("🔨 EXEC", ColorPurple+ColorBold) + "\n" +
		"    " + recolor("  line one", ColorGray) + "\n" +
		"    " + recolor("▲  careful", ColorYellow) + "\n" +
		"\n" +
		"\n  " + recolor("🧭 PLAN", ColorCyan+ColorBold) + "\n    read the file\n" +
		"  " + recolor("↻", ColorCyan) + " " + recolor("Exec(go test)", ColorCyan) + "\n" +
		"  " + recolor("✓", ColorGreen+ColorBold) + " " + recolor("Exec(go test)", ColorCyan) + " " + recolor("1.2s", ColorCyan) + "\n"
	if got != want {
		t.Fatalf("framed timeline changed:\n got %q\nwant %q", got, want)
	}
}

// The exit status on a call's line is the one the caller read from the
// error. A number in the output is never taken for it.
func TestFlatTimelineExitStatusComesFromTheCaller(t *testing.T) {
	withTheme(t, "onyx")
	printed := runFlatCallExit(t, UIStyleCompact, nil, "worker said: exit status 9\nexit code: 4", true, "")
	if strings.Contains(strings.SplitN(printed, "\n", 2)[0], i18n.T("agent.flat.exit", "9")) ||
		strings.Contains(strings.SplitN(printed, "\n", 2)[0], i18n.T("agent.flat.exit", "4")) {
		t.Errorf("an exit status was read out of the output:\n%s", printed)
	}
	given := runFlatCallExit(t, UIStyleCompact, nil, "worker said: exit status 9", true, "3")
	if !strings.Contains(given, "· "+i18n.T("agent.flat.exit", "3")+" ·") {
		t.Errorf("the caller's exit status is missing:\n%s", given)
	}
}

// A dispatched agent is drawn like a tool call, so a squad run reads as the
// same timeline; framed themes never reach these calls.
func TestFlatTimelineDispatchedAgents(t *testing.T) {
	withTheme(t, "onyx")
	draw := func(style UIStyle) string {
		r := NewUIRendererWithStyle(zap.NewNop(), style)
		return stripANSI(captureStdout(t, func() {
			r.FlatAgentDone("planner", "map the  renderer\ncall sites", "Found 3 call sites.\nAll in cli/agent.", false, 12400*time.Millisecond, 4, 2)
			r.FlatAgentDone("tester", "run the timeline tests", "context deadline exceeded", true, 30*time.Second, 1, 0)
			r.FlatAgentSummary(1, 2, 5, 42400*time.Millisecond)
		}))
	}
	full := draw(UIStyleFull)
	lines := strings.Split(strings.TrimRight(full, "\n"), "\n")
	wantLead := []string{"  ✓ planner map the renderer call sites", "  │ Found 3 call sites.", "  │ All in cli/agent.",
		"  ✗ tester run the timeline tests", "  │ context deadline exceeded", "  ✗ " + i18n.T("agent.flat.agents") + " 1/2"}
	if len(lines) != len(wantLead) {
		t.Fatalf("full dispatch has %d lines, want %d:\n%s", len(lines), len(wantLead), full)
	}
	for i, lead := range wantLead {
		if !strings.HasPrefix(lines[i], lead) {
			t.Errorf("line %d = %q, want it to start with %q", i, lines[i], lead)
		}
	}
	if want := "12.4s · " + i18n.T("agent.flat.tool_calls", 4) + " · " + i18n.T("agent.flat.parallel", 2); !strings.HasSuffix(lines[0], want) {
		t.Errorf("agent metadata: %q, want suffix %q", lines[0], want)
	}
	if want := "30.0s · " + i18n.T("agent.flat.tool_call"); !strings.HasSuffix(lines[3], want) {
		t.Errorf("failed agent metadata: %q, want suffix %q", lines[3], want)
	}
	if got := kit.VisibleLen(lines[0]); got != kit.ContentWidth() {
		t.Errorf("an agent line spans the content width like a tool line: %d", got)
	}
	compact := draw(UIStyleCompact)
	if strings.Contains(compact, "Found 3") || !strings.Contains(compact, "  │ context deadline exceeded") {
		t.Errorf("compact hides results and keeps the failure:\n%s", compact)
	}
	if minimal := draw(UIStyleMinimal); !strings.Contains(minimal, "  │ Found 3 call sites.") || strings.Contains(minimal, "All in cli/agent.") {
		t.Errorf("minimal shows the first row:\n%s", minimal)
	}
	all := stripANSI(captureStdout(t, func() {
		NewUIRendererWithStyle(zap.NewNop(), UIStyleCompact).FlatAgentSummary(2, 2, 7, time.Second)
	}))
	if !strings.HasPrefix(all, "  ✓ ") {
		t.Errorf("a dispatch with every agent done is marked done: %q", all)
	}
}

// The state on a refused call's line is the outcome the loop reports, not a
// guess from how far the call got: each outcome has its word, the calls
// that never ran share the not-run glyph, and only a real failure gets the
// failure glyph.
func TestFlatTimelineOutcomesAreReportedNotInferred(t *testing.T) {
	withTheme(t, "onyx")
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleCompact)
	draw := func(outcome FlatOutcome, begun bool, msg string) string {
		return stripANSI(captureStdout(t, func() {
			r.FlatToolQueue("@coder", "exec", execArgs)
			if begun {
				r.FlatToolBegin()
			}
			r.FlatToolRefused(outcome, msg)
		}))
	}
	for _, tc := range []struct {
		outcome FlatOutcome
		begun   bool
		glyph   string
		word    string
	}{
		{FlatDenied, false, "⊘", i18n.T("agent.flat.denied")},   // the user said no at the prompt
		{FlatDenied, true, "⊘", i18n.T("agent.flat.denied")},    // the user said no to a dangerous command, after the call began
		{FlatBlocked, false, "⊘", i18n.T("agent.flat.blocked")}, // a policy rule
		{FlatBlocked, true, "⊘", i18n.T("agent.flat.blocked")},  // a hook, after the call began
		{FlatSkipped, false, "⊘", i18n.T("agent.flat.skipped")}, // a sibling failed first
		{FlatCanceled, false, "⊘", i18n.T("agent.flat.canceled")},
		{FlatCanceled, true, "⊘", i18n.T("agent.flat.canceled")},
		{FlatParked, true, "⊘", i18n.T("agent.flat.parked")},
		{FlatFailed, false, "✗", i18n.T("agent.flat.failed")}, // bad arguments, before the call began
		{FlatFailed, true, "✗", i18n.T("agent.flat.failed")},
	} {
		out := draw(tc.outcome, tc.begun, "the reason")
		first := strings.SplitN(out, "\n", 2)[0]
		if !strings.HasPrefix(first, "  "+tc.glyph+" exec   go test ./...") || !strings.HasSuffix(first, " "+tc.word) {
			t.Errorf("outcome %d (begun=%v): %q, want glyph %s and word %q", tc.outcome, tc.begun, first, tc.glyph, tc.word)
		}
		if !strings.Contains(out, "\n  │ the reason\n") {
			t.Errorf("outcome %d: the reason is not under the rail:\n%s", tc.outcome, out)
		}
	}
	words := map[string]bool{}
	for o := FlatFailed; o <= FlatParked; o++ {
		words[o.word()] = true
	}
	if len(words) != 6 {
		t.Errorf("every outcome needs its own word, got %v", words)
	}
	// A call cancelled with no message (an MCP call, a plugin call) keeps
	// its line and adds no row.
	if out := draw(FlatCanceled, true, ""); strings.Count(out, "\n") != 1 || !strings.Contains(out, i18n.T("agent.flat.canceled")) {
		t.Errorf("cancelled call: %q", out)
	}
	// After output streamed, the live row is the open end of the rail.
	r.FlatToolQueue("@coder", "exec", execArgs)
	r.FlatToolBegin()
	_ = captureStdout(t, func() { r.StreamOutput("row") })
	if frame := stripANSI(r.FlatToolLiveFrame()); !strings.HasPrefix(frame, "  └ ↻ ") || strings.Contains(frame, "exec") {
		t.Errorf("live row under streamed output: %q", frame)
	}
	_ = captureStdout(t, func() { r.FlatToolRefused(FlatCanceled, "") })
}
