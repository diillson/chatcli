/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/metrics"
	"github.com/diillson/chatcli/i18n"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
)

// The row a running step repaints has one owner at a time: the turn timer.
// It already paints the turn's status line, the dispatch panel and the
// task-graph status, and everything that prints while one of those is up
// pauses it first (the permission prompts through the policy adapter, the
// side commands, streamed rows), which erases the row, and resumes it
// after. The live line of a flat tool call is one more display of that
// timer, so it inherits every one of those pauses instead of needing its
// own, and it cannot run next to another display because a timer has one.

// startFlatToolLive paints the running tool call as a live row that its
// final line will replace, and returns the call that takes it down. Only
// an attended, interactive run gets it: a one-shot run and an unattended
// one (gateway, ACP, web) have nobody watching a repaint. The timer itself
// only ticks when stdout is a terminal. When nothing was painted nothing is
// erased, so captured output carries no escape sequence from here and the
// timeline is the same either way: the final line is printed regardless.
func (a *AgentMode) startFlatToolLive(ctx context.Context, r *agent.UIRenderer) (stop func()) {
	t := a.turnTimer
	if a.isOneShot || a.cli == nil || a.cli.unattended || t == nil || t.IsRunning() {
		return func() {}
	}
	// A worker prompt raised during the call pauses whatever timer the
	// adapter holds; make sure it is this one.
	if a.policyAdapter != nil {
		a.policyAdapter.setSpinner(t)
	}
	// painted and hadPreview are written by the tick and the pause hook,
	// both under the timer's mutex, and read after Stop, which takes it.
	painted, hadPreview := false, false
	erase := func() {
		if painted {
			fmt.Print(metrics.ClearLine())
			fmt.Print(spinnerPreviewWipe(hadPreview))
		}
		painted, hadPreview = false, false
	}
	t.SetOnPause(erase)
	t.Start(ctx, func(time.Duration) {
		// The type-ahead preview rides under the row exactly as it does
		// under the turn's status line.
		var suffix string
		suffix, hadPreview = formatTypeaheadPreviewBelow(a.typeaheadPreviewSnapshot(), hadPreview)
		fmt.Print("\r" + kit.Truncate(r.FlatToolLiveFrame(), kit.TermWidth()-1) + "\033[K" + suffix)
		painted = true
	})
	return func() {
		t.Stop()
		t.SetOnPause(nil)
		erase()
	}
}

// withLiveRowPaused runs emit with the transient row taken down and puts
// the row back after, for output that arrives from outside the loop while
// a flat step is running (a background job's announcement, a parked draft).
// It is the pause the side commands already use. Framed themes are left as
// they are: their displays have always been printed over by these notices.
func (a *AgentMode) withLiveRowPaused(emit func()) {
	if a != nil && theme.Active().Layout == theme.LayoutFlat {
		if t := a.turnTimer; t != nil && t.IsRunning() {
			t.Pause()
			defer t.Resume()
		}
	}
	emit()
}

// flatStreamRow prints one row of a running call's output under its line:
// serialized with the permission prompts, and with the live row down while
// it is written. hidden is the compact style, which shows no rows and so
// leaves the live row alone.
func (a *AgentMode) flatStreamRow(r *agent.UIRenderer, line string, hidden bool) {
	if hidden {
		return
	}
	row := func() { a.withLiveRowPaused(func() { r.StreamOutput(line) }) }
	if a.policyAdapter != nil {
		a.policyAdapter.withPromptGate(row)
		return
	}
	row()
}

// liveOutputMeter counts the reply text a model call has streamed so far.
// Usage arrives once, when the call ends; until then the text is the only
// thing that moves, and the flat status line turns it into a running token
// estimate. It is written by the goroutine decoding the stream and read by
// the status ticker.
type liveOutputMeter struct{ chars atomic.Int64 }

func (m *liveOutputMeter) reset() { m.chars.Store(0) }

// withLiveOutput starts a fresh count for the call about to be made and,
// under a theme that shows it, asks the provider to report streamed text.
func (a *AgentMode) withLiveOutput(ctx context.Context) context.Context {
	a.liveOut.reset()
	if theme.Active().Layout != theme.LayoutFlat {
		return ctx
	}
	return llmclient.WithStreamProgress(ctx, func(chars int) {
		if chars == llmclient.StreamProgressRestart {
			// A retried attempt re-streams the reply from its start.
			a.liveOut.reset()
			return
		}
		a.liveOut.chars.Add(int64(chars))
	})
}

// liveOutputTokens is the estimate for the call in flight, zero when
// nothing has streamed (a buffered call, or between calls). It uses the
// chars-per-token ratio the session has learned for this model. It is a
// display figure only: nothing is billed or recorded from it.
func (a *AgentMode) liveOutputTokens() int64 {
	chars := a.liveOut.chars.Load()
	if chars <= 0 {
		return 0
	}
	if est := a.cli.estimateTokens64(chars); est > 0 {
		return est
	}
	return 1
}

// turnNoticeKind is what an in-turn notice is about. The notices a turn
// prints between steps (a retry, a compaction pass, a trimmed context, a
// queued instruction) used to be formatted where they were printed; they
// share one form here so the flat layout can give them one look.
type turnNoticeKind int

const (
	noticeInfo     turnNoticeKind = iota // something was adjusted, nothing to do
	noticeCaution                        // worth knowing before it becomes a problem
	noticeWarn                           // a problem the loop is working around
	noticeError                          // a problem the loop gives up on
	noticeProgress                       // a step of a longer pass
	noticeRewrite                        // the history was rewritten to save room
	noticeRail                           // a quiet follow-up to the line above
	noticeRunning                        // something started and is under way
	noticeDone                           // something finished well
)

// turnNoticeLooks is each kind's framed mark and colors (kept exactly as
// the call sites had them) and its flat glyph and tone.
var turnNoticeLooks = [...]struct {
	mark, markColor, textColor string
	glyph                      kit.Glyph
	role                       theme.Role
}{
	noticeInfo:     {"ℹ", ColorGray, ColorGray, kit.GlyphInfo, theme.RoleText},
	noticeCaution:  {"ℹ", ColorYellow, ColorYellow, kit.GlyphWarn, theme.RoleWarning},
	noticeWarn:     {"⚠", ColorYellow, ColorYellow, kit.GlyphWarn, theme.RoleWarning},
	noticeError:    {"✖", ColorRed, ColorRed, kit.GlyphError, theme.RoleToolError},
	noticeProgress: {"│", ColorCyan, ColorGray, kit.GlyphBullet, theme.RoleText},
	noticeRewrite:  {"🗜", ColorGray, ColorGray, kit.GlyphInfo, theme.RoleText},
	noticeRail:     {"│", ColorGray, ColorGray, kit.GlyphBullet, theme.RoleText},
	noticeRunning:  {"↻", ColorCyan, ColorCyan, kit.GlyphRunning, theme.RoleText},
	noticeDone:     {"✓", ColorGreen, ColorGreen, kit.GlyphSuccess, theme.RoleText},
}

// turnNotice renders one in-turn notice. Framed themes get the line they
// have always had. The flat layout gets the compact form: the kind's glyph,
// the statement in its tone, and whatever qualifies it in the muted tone.
func turnNotice(kind turnNoticeKind, text string) string {
	look := turnNoticeLooks[kind]
	if theme.Active().Layout != theme.LayoutFlat {
		return "  " + colorize(look.mark, look.markColor) + " " + colorize(text, look.textColor)
	}
	// Many messages open with their own status emoji, which the theme has
	// just turned into the glyph this line already draws.
	head, detail := splitNoticeDetail(strings.TrimLeft(kit.Plain(text), "▲✗✓•↻ "))
	line := "  " + kit.Colorize(look.glyph.String(), look.glyph.Role()) + " " + kit.Colorize(head, look.role)
	if detail != "" {
		line += "  " + kit.Colorize(detail, theme.RoleMuted)
	}
	return line
}

// printTurnNotice prints a notice on its own line. overSpinner says the
// turn's status line may be painted on this row and has to be erased first.
func printTurnNotice(kind turnNoticeKind, text string, overSpinner bool) {
	erase := ""
	if overSpinner {
		erase = "\r\033[K"
	}
	fmt.Println(erase + turnNotice(kind, text))
}

// flatNoticeOr returns the flat form of a notice under the flat layout and
// framed, the line the call site already built, everywhere else. It is for
// the notices whose framed shape is their own (an emoji lead, blank rows).
func flatNoticeOr(kind turnNoticeKind, text, framed string) string {
	if theme.Active().Layout != theme.LayoutFlat {
		return framed
	}
	return turnNotice(kind, text)
}

// splitNoticeDetail separates what a notice states from what qualifies it:
// at the first colon or dash the messages use, or at a parenthesis that
// closes the message. A parenthesis in the middle of a sentence is part of
// the statement, and a message with no separator is all statement.
func splitNoticeDetail(text string) (head, detail string) {
	text = strings.TrimSpace(text)
	cut, skip := -1, 0
	for _, sep := range []string{": ", " — "} {
		if i := strings.Index(text, sep); i > 0 && (cut < 0 || i < cut) {
			cut, skip = i, len(sep)
		}
	}
	if cut < 0 && strings.HasSuffix(text, ")") {
		if i := strings.LastIndex(text, " ("); i > 0 {
			cut, skip = i, 1 // the parenthesis belongs to the detail
		}
	}
	if cut < 0 {
		return text, ""
	}
	return text[:cut], strings.TrimSpace(text[cut+skip:])
}

// flatDetailOr is flatNoticeOr for the rows under a notice (what a job ran,
// the head of its output, where to read more): under the flat layout they
// hang from the rail a tool result hangs from.
func flatDetailOr(text, framed string) string {
	if theme.Active().Layout != theme.LayoutFlat {
		return framed
	}
	return "  " + kit.Sym(kit.GlyphGutter) + " " + kit.Colorize(kit.Plain(text), theme.RoleMuted)
}

// renderFlatParkBanner draws the park announcement for the flat layout and
// reports whether it did: a notice line for the park and its token, then
// the same fields the framed box lists, under the rail.
func renderFlatParkBanner(snap *park.Snapshot, req park.Request) bool {
	if theme.Active().Layout != theme.LayoutFlat {
		return false
	}
	short := snap.Token
	if len(short) > 8 {
		short = short[:8]
	}
	resumeAt, eta := parkResumeETA(req, time.Now())
	row := func(label, value string) {
		pad := strings.Repeat(" ", max(maxLabelWidth-runeLen(label), 1))
		fmt.Println("  " + kit.Sym(kit.GlyphGutter) + " " + kit.Colorize(label+pad, theme.RoleMuted) + " " + kit.Colorize(kit.Plain(value), theme.RoleText))
	}
	fmt.Println()
	fmt.Println("  " + kit.Sym(kit.GlyphDenied) + " " + theme.ANSI(theme.RoleTextStrong) + kit.Plain(i18n.T("park.box.title")) + theme.Reset() +
		"  " + kit.Colorize(snap.Token, theme.RoleMuted))
	row(i18n.T("park.box.mode"), describeParkRequest(req))
	if req.Note != "" {
		row(i18n.T("park.box.note"), req.Note)
	}
	row(i18n.T("park.box.resume_at"), resumeAt+"  ("+eta+")")
	row("/resume "+short, i18n.T("park.box.controls.resume_now"))
	row("/cancel-park "+short, i18n.T("park.box.controls.cancel"))
	fmt.Println()
	return true
}

// goExitStatus is the text os/exec gives a non-zero exit. It is written by
// the Go runtime in English whatever the locale, so matching it cannot
// read a number out of a translated message.
var goExitStatus = regexp.MustCompile(`\bexit status (\d+)\b`)

// flatExitStatus is the exit status a failed tool call reported, or "" when
// the failure was not a process exiting. It comes from the error value: the
// *exec.ExitError when the chain still holds one, otherwise the runtime's
// own "exit status N" in the error text, for an error that crossed a
// process or RPC boundary and lost its chain. The tool's output is never
// searched, so a number a command happened to print is not mistaken for it.
func flatExitStatus(err error) string {
	if err == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return strconv.Itoa(code)
		}
		return "" // killed by a signal: there is no status to show
	}
	if m := goExitStatus.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return ""
}

// flatAgentOutput is what a dispatched agent's line shows under the rail:
// why it failed, or the head of what it answered.
func flatAgentOutput(ar workers.AgentResult) string {
	if ar.Error != nil {
		return ar.Error.Error()
	}
	return truncateForUI(ar.Output, 200)
}
