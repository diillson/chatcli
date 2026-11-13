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
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
)

// The exit status shown on a tool line comes from the error value, and a
// translated message cannot put a number there.
func TestFlatExitStatusReadsTheErrorNotTheText(t *testing.T) {
	if got := flatExitStatus(nil); got != "" {
		t.Errorf("no error: %q", got)
	}
	// A real non-zero exit: the test binary rejects an unknown flag with 2.
	runErr := exec.Command(os.Args[0], "-test.run=^$", "-no-such-flag-for-exit-status").Run()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("expected an exit error, got %v", runErr)
	}
	want := fmt.Sprint(exitErr.ExitCode())
	if got := flatExitStatus(fmt.Errorf("command failed: %w", runErr)); got != want || want == "0" {
		t.Errorf("wrapped exit error: got %q, want %q", got, want)
	}
	// The chain was lost across a process boundary; the runtime's text remains.
	if got := flatExitStatus(errors.New("plugin error: command failed: exit status 7")); got != "7" {
		t.Errorf("runtime text: %q", got)
	}
	for _, msg := range []string{
		"comando falhou: código de saída 7",
		"o comando terminou com status 7 após 3 tentativas",
		"HTTP 404 from upstream",
		"blocked by hook: exit code 5 expected",
	} {
		if got := flatExitStatus(errors.New(msg)); got != "" {
			t.Errorf("%q produced exit status %q", msg, got)
		}
	}
}

// The coder plugin keeps the exit error in its chain, so the status on the
// line is the structured one for the tool that runs commands.
func TestFlatExitStatusFromTheCoderPlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	t.Chdir(t.TempDir())
	_, err := plugins.NewBuiltinCoderPlugin().Execute(context.Background(), []string{"exec", "--cmd", "exit 3"})
	if err == nil {
		t.Fatal("exit 3 must fail")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the plugin dropped the exit error from the chain: %v", err)
	}
	if got := flatExitStatus(err); got != "3" {
		t.Errorf("exit status = %q, want 3 (%v)", got, err)
	}
}

func noticeJob(status scheduler.JobStatus) *scheduler.Job {
	created := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	return &scheduler.Job{
		ID: "job-7f3a", Name: "nightly-build", Status: status, CycleCount: 3,
		CreatedAt: created, FinishedAt: created.Add(95 * time.Second),
		Action:     scheduler.Action{Type: scheduler.ActionShell, Payload: map[string]any{"command": "make release"}},
		LastResult: &scheduler.ExecutionResult{Output: "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n", Error: "exit status 2"},
	}
}

// Background job announcements: framed themes print what main prints; the
// flat layout uses the shared notice form with the details under the rail.
func TestJobOutcomeNoticeFollowsTheLayout(t *testing.T) {
	render := func(j *scheduler.Job, cycle bool) string {
		return captureStdout(t, func() { printJobOutcome(j, cycle) })
	}
	gray := func(s string) string { return colorize(s, ColorGray) + "\n" }

	useTheme(t, "dark")
	done := noticeJob(scheduler.StatusCompleted)
	want := colorize("  🔔 "+i18n.T("scheduler.notify.completed", done.Name, done.ID, "1m35s"), ColorGreen+ColorBold) + "\n" +
		gray("    "+i18n.T("scheduler.notify.ran", "make release")) +
		gray("      l1") + gray("      l2") + gray("      l3") + gray("      l4") + gray("      l5") + gray("      l6") +
		gray("      "+i18n.T("scheduler.notify.more_lines", 2)) +
		gray("    "+i18n.T("scheduler.notify.hint", done.ID))
	if got := render(done, false); got != want {
		t.Errorf("framed completed job changed:\n got %q\nwant %q", got, want)
	}
	failed := noticeJob(scheduler.StatusFailed)
	want = colorize("  ✗ "+i18n.T("scheduler.notify.failed", failed.Name, failed.ID, "exit status 2"), ColorRed+ColorBold) + "\n" +
		gray("    "+i18n.T("scheduler.notify.hint", failed.ID))
	if got := render(failed, false); got != want {
		t.Errorf("framed failed job changed:\n got %q\nwant %q", got, want)
	}
	cancelled := noticeJob(scheduler.StatusCancelled)
	want = colorize("  ⊘ "+i18n.T("scheduler.notify.cancelled", cancelled.Name, cancelled.ID), ColorYellow) + "\n" +
		gray("    "+i18n.T("scheduler.notify.hint", cancelled.ID))
	if got := render(cancelled, false); got != want {
		t.Errorf("framed cancelled job changed:\n got %q\nwant %q", got, want)
	}
	timedOut := noticeJob(scheduler.StatusTimedOut)
	if got := render(timedOut, false); !strings.HasPrefix(got, colorize("  ⏱ "+i18n.T("scheduler.notify.timed_out", timedOut.Name, timedOut.ID, "1m35s"), ColorYellow+ColorBold)+"\n") {
		t.Errorf("framed timed-out job changed: %q", got)
	}
	running := noticeJob(scheduler.StatusRunning)
	if got := render(running, true); !strings.HasPrefix(got, colorize("  🔔 "+i18n.T("scheduler.notify.cycle", running.Name, running.ID, 3), ColorCyan+ColorBold)+"\n") {
		t.Errorf("framed cycle changed: %q", got)
	}

	useTheme(t, "onyx")
	for _, tc := range []struct {
		job   *scheduler.Job
		cycle bool
		glyph string
	}{{done, false, "  ✓ "}, {failed, false, "  ✗ "}, {cancelled, false, "  ▲ "}, {timedOut, false, "  ▲ "}, {running, true, "  • "}} {
		out := plainText(render(tc.job, tc.cycle))
		if !strings.HasPrefix(out, tc.glyph) || hasColoredEmoji(out) {
			t.Errorf("flat %s job: %q", tc.job.Status, out)
		}
		if !strings.Contains(out, "\n  │ "+plainText(i18n.T("scheduler.notify.hint", tc.job.ID))+"\n") {
			t.Errorf("flat %s job: the hint is not under the rail: %q", tc.job.Status, out)
		}
	}
	flatDone := plainText(render(done, false))
	if !strings.Contains(flatDone, "\n  │   l1\n") || !strings.Contains(flatDone, "\n  │   "+i18n.T("scheduler.notify.more_lines", 2)+"\n") {
		t.Errorf("flat job output rows: %q", flatDone)
	}
}

// Park announcements: the framed box is untouched; the flat layout lists
// the same fields under the rail.
func TestParkAnnouncementsFollowTheLayout(t *testing.T) {
	snap := &park.Snapshot{Token: "0f3ded97aa11bb22"}
	req := park.Request{Mode: park.ModeDelay, Delay: 10 * time.Minute, Note: "wait for CI"}
	banner := func() string { return captureStdout(t, func() { renderParkBanner(snap, req) }) }

	useTheme(t, "dark")
	framed := banner()
	head := "\n" + colorize("  ╭── 🅿️  ", ColorCyan) + colorize(i18n.T("park.box.title"), ColorCyan+ColorBold) + "\n" +
		colorize("  │  ", ColorCyan) + colorize(i18n.T("park.box.token")+strings.Repeat(" ", maxLabelWidth-runeLen(i18n.T("park.box.token"))), ColorGray) +
		"  " + colorize("0f3ded97"+colorize("  (0f3ded97aa11bb22)", ColorGray), "") + "\n"
	if !strings.HasPrefix(framed, head) {
		t.Errorf("framed park box changed:\n got %q\nwant prefix %q", framed, head)
	}
	if tail := colorize("  ╰──", ColorCyan) + "\n\n"; !strings.HasSuffix(framed, tail) || strings.Count(framed, "\n") != 11 {
		t.Errorf("framed park box changed its rows: %q", framed)
	}

	useTheme(t, "onyx")
	flat := plainText(banner())
	if strings.ContainsAny(flat, "╭├╰") || hasColoredEmoji(flat) {
		t.Errorf("flat park announcement kept the box:\n%s", flat)
	}
	for _, want := range []string{
		"\n  ⊘ " + plainText(i18n.T("park.box.title")) + "  0f3ded97aa11bb22\n",
		"wait for CI", "  │ /resume 0f3ded97", "  │ /cancel-park 0f3ded97", describeParkRequest(req),
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("flat park announcement lacks %q:\n%s", want, flat)
		}
	}
	noNote := plainText(captureStdout(t, func() {
		renderParkBanner(&park.Snapshot{Token: "abc"}, park.Request{Mode: park.ModeDelay, Delay: time.Minute})
	}))
	if strings.Contains(noNote, i18n.T("park.box.note")) || !strings.Contains(noNote, "/resume abc") {
		t.Errorf("flat park announcement without a note: %s", noNote)
	}
}

// The remaining in-turn announcements, each under both kinds of theme.
func TestQueueAndParkNoticesFollowTheLayout(t *testing.T) {
	drain := func(queued int) string {
		c := &ChatCLI{messageQueue: make([]string, queued)}
		return captureStdout(t, func() { c.announceQueueDrain() })
	}
	hint := func() string {
		c := &ChatCLI{activeParks: []activePark{{Token: "0f3ded97aa11bb22", ResumeAtDisplay: "14:02"}}}
		return captureStdout(t, func() {
			c.maybeShowParkNoteHint()
			c.maybeShowParkNoteHint() // once per park
		})
	}

	useTheme(t, "dark")
	if got, want := drain(2), "\033[0m\n"+kit.NoticeRole(kit.GlyphRunning, i18n.T("queue.processing_remaining", 2), theme.RoleAction)+"\n"; got != want {
		t.Errorf("framed queue drain changed:\n got %q\nwant %q", got, want)
	}
	if got, want := drain(0), "\033[0m\n"+kit.NoticeRole(kit.GlyphRunning, i18n.T("queue.processing"), theme.RoleAction)+"\n"; got != want {
		t.Errorf("framed queue drain changed:\n got %q\nwant %q", got, want)
	}
	if got, want := hint(), colorize("  ℹ "+i18n.T("park.note.hint", "14:02", "0f3ded97"), ColorGray)+"\n"; got != want {
		t.Errorf("framed park hint changed:\n got %q\nwant %q", got, want)
	}
	msg := "Summarizing 40 messages"
	if got, want := captureStdout(t, func() { printTurnNotice(noticeProgress, msg, false) }),
		fmt.Sprintf("  %s %s\n", colorize("│", ColorCyan), colorize(msg, ColorGray)); got != want {
		t.Errorf("framed /compact progress changed:\n got %q\nwant %q", got, want)
	}
	if got := flatDetailOr("detail", "framed row"); got != "framed row" {
		t.Errorf("a framed theme keeps its detail row: %q", got)
	}

	useTheme(t, "onyx")
	if got := plainText(drain(2)); !strings.HasPrefix(got, "\n  ↻ ") || strings.Count(got, "\n") != 2 {
		t.Errorf("flat queue drain: %q", got)
	}
	if got := plainText(drain(0)); !strings.HasPrefix(got, "\n  ↻ ") {
		t.Errorf("flat queue drain: %q", got)
	}
	if got := plainText(hint()); !strings.HasPrefix(got, "  • ") || strings.Contains(got, "ℹ") || strings.Count(got, "\n") != 1 {
		t.Errorf("flat park hint: %q", got)
	}
	if got := plainText(turnNotice(noticeDone, "Compacted: 40 → 12 messages")); got != "  ✓ Compacted  40 → 12 messages" {
		t.Errorf("flat /compact result: %q", got)
	}
	if got := plainText(flatDetailOr("ran: make", "framed")); got != "  │ ran: make" {
		t.Errorf("flat detail row: %q", got)
	}
}
