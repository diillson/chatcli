/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package theme_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/ui/theme"
	"go.uber.org/zap"
)

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func captured(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	os.Stdout = orig
	_ = w.Close()
	return ansiSeq.ReplaceAllString(<-done, "")
}

// No built-in theme asks for monochrome glyphs while keeping its frames,
// but the two switches are independent and a theme that sets only the
// first must still get both: the frames it kept, and no pictographs inside
// them. This is the path the framed permission prompt and the framed
// timeline take under such a theme.
func TestMonoglyphThemeThatKeepsItsFrames(t *testing.T) {
	prevProfile := theme.ActiveProfile()
	theme.SetProfile(theme.ProfileTrueColor)
	t.Cleanup(func() { theme.SetProfile(prevProfile) })

	prompt := func() string {
		answers := make(chan string, 1)
		answers <- "n"
		return captured(t, func() {
			_ = coder.PromptSecurityCheckWithContext(context.Background(), "@coder",
				`{"cmd":"exec","args":{"cmd":"make release"}}`,
				&coder.SecurityContext{AgentName: "planner", TaskDesc: "ship the release"}, answers)
		})
	}
	timeline := func() string {
		r := agent.NewUIRendererWithStyle(zap.NewNop(), agent.UIStyleFull)
		return captured(t, func() {
			r.RenderToolCallWithProgress("@coder", `{"cmd":"read"}`, 1, 1)
			r.RenderStreamBoxStart("🔨", "EXEC", agent.ColorPurple)
			r.StreamOutput("✅ Success.")
			r.RenderStreamBoxEnd(agent.ColorPurple)
			r.RenderToolResult("✅ Success.\n📦 3 files", false)
		})
	}
	const pictographs = "🔒🤖📋⚡📜🔨✅📦"

	custom := theme.DarkTheme()
	custom.Name = "mono-framed"
	custom.Monoglyph = true
	if custom.Layout != theme.LayoutFramed {
		t.Fatal("the dark theme is expected to keep its frames")
	}
	restore := theme.UseForTest(custom)
	framedPrompt, framedTimeline := prompt(), timeline()
	restore()

	if !strings.Contains(framedPrompt, "╭") || !strings.Contains(framedPrompt, "make release") {
		t.Fatalf("a framed theme lost the prompt box:\n%s", framedPrompt)
	}
	if strings.ContainsAny(framedPrompt, pictographs) {
		t.Errorf("the framed prompt kept pictographs under a monoglyph theme:\n%s", framedPrompt)
	}
	if strings.ContainsAny(framedTimeline, pictographs) || !strings.Contains(framedTimeline, "✓ Success.") {
		t.Errorf("the framed timeline kept pictographs under a monoglyph theme:\n%s", framedTimeline)
	}
	if strings.Contains(framedTimeline, "│") || strings.Contains(framedTimeline, "└") {
		t.Errorf("a framed theme got the flat rail:\n%s", framedTimeline)
	}

	// The control: the same theme without the switch keeps every emoji.
	plain := theme.UseForTest(theme.DarkTheme())
	defer plain()
	if p := prompt(); !strings.ContainsRune(p, '🔒') || !strings.ContainsRune(p, '⚡') {
		t.Errorf("the dark prompt lost its emoji:\n%s", p)
	}
	if tl := timeline(); !strings.ContainsRune(tl, '🔨') || !strings.ContainsRune(tl, '✅') {
		t.Errorf("the dark timeline lost its emoji:\n%s", tl)
	}
}
