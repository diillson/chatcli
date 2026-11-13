/*
 * Slash command handlers for the park subsystem:
 *
 *   /parked            list all snapshots on disk + their scheduler jobs
 *   /resume <token>    force-resume a parked agent immediately
 *   /cancel-park <tok> cancel a parked agent (delete snapshot + job)
 *
 * Plus the auto-resume hook drainPendingResumes that the outer Run()
 * loop in cli.go calls between user prompts.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
)

// handleParkedCommand renders the on-disk snapshots together with the
// status of their scheduler jobs (when reachable). The output is a
// compact table; users with many parks can run /jobs list for the full
// scheduler view.
//
// Subcommands:
//
//	/parked          — list (default)
//	/parked prune    — remove snapshots whose scheduler job is in a
//	                   terminal state (completed, failed, cancelled,
//	                   timed_out). Useful to clean up after the resume
//	                   fired but the snapshot was kept for forensics.
//	/parked gc <dur> — remove snapshots older than <dur> regardless of
//	                   job state, e.g. "/parked gc 24h".
//	/parked wake <token> [reason...] — resume the agent now with
//	                   outcome=cancelled (what @park cancel does), in
//	                   whichever live process owns the park.
func (cli *ChatCLI) handleParkedCommand(userInput string) {
	args := strings.Fields(strings.TrimSpace(userInput))
	if len(args) >= 2 {
		switch args[1] {
		case "prune":
			cli.parkPrune()
			return
		case "wake":
			cli.parkWake(args[2:])
			return
		case "gc":
			cli.parkGC(args[2:])
			return
		case "help", "-h", "--help":
			printParkedUsage()
			return
		case "list", "ls":
		default:
			// A mistyped subcommand used to list everything in silence.
			fmt.Println(colorize("  "+i18n.T("park.cmd.unknown_sub", args[1]), ColorYellow))
			printParkedUsage()
			return
		}
	}
	snaps, errs := park.List()
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Println(colorize("  ⚠ "+e.Error(), ColorYellow))
		}
	}
	if len(snaps) == 0 {
		fmt.Println(colorize("  "+i18n.T("park.list.empty"), ColorGray))
		return
	}

	fmt.Println(colorize("  "+i18n.T("park.list.header"), ColorCyan+ColorBold))
	for _, s := range snaps {
		eta := parkRequestETA(s.Park)
		desc := describeParkRequest(s.Park)
		mode := string(s.Park.Mode)
		jobStatus := cli.lookupParkJobStatus(s)

		fmt.Printf("    %s  %s\n",
			colorize(s.Token[:min(8, len(s.Token))], ColorPurple),
			fmt.Sprintf("[%s] %s", mode, desc))
		fmt.Printf("    %s  resume_at=%s  job=%s%s\n",
			strings.Repeat(" ", 8),
			eta,
			s.SchedulerJobID,
			jobStatus)
		fmt.Printf("    %s  created=%s%s\n",
			strings.Repeat(" ", 8),
			s.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			lastResumeBadge(s))
		fmt.Println()
	}
}

// lookupParkJobStatus annotates a snapshot with its current scheduler
// state — "(running)", "(queued)", "(failed)", or "(missing)" when the
// job no longer exists. Returns "" when the scheduler is disabled.
func (cli *ChatCLI) lookupParkJobStatus(s *park.Snapshot) string {
	if cli.scheduler == nil || s.SchedulerJobID == "" {
		return ""
	}
	j, err := cli.scheduler.Query(scheduler.JobID(s.SchedulerJobID))
	if err != nil || j == nil {
		return colorize(" (missing)", ColorRed)
	}
	color := ColorGray
	switch j.Status {
	case scheduler.StatusRunning, scheduler.StatusWaiting:
		color = ColorGreen
	case scheduler.StatusFailed, scheduler.StatusCancelled, scheduler.StatusTimedOut:
		color = ColorYellow
	}
	return colorize(fmt.Sprintf(" (%s)", j.Status), color)
}

// lastResumeBadge appends "  resumed=<time>" when the snapshot has been
// consumed at least once. Useful for forensics on crash-then-restore.
func lastResumeBadge(s *park.Snapshot) string {
	if s.LastResumeAt.IsZero() {
		return ""
	}
	return "  last_resume=" + s.LastResumeAt.Local().Format("2006-01-02 15:04:05")
}

// handleResumeCommand parses /resume <token> and dispatches to the
// resume runner. The token may be a unique prefix (matches the same way
// `git checkout abc123` resolves a SHA prefix). On ambiguity we list
// the candidates and bail.
//
// Idempotency: when drainPendingResumes consumes a token (via the
// auto-resume path or the executor hook), it stamps recentlyResumed.
// A subsequent /resume <token> for the same token within the TTL is
// silently no-op'd — that is the typical race when NotifyParkComplete
// auto-injects the command right after the drain has already run it.
func (cli *ChatCLI) handleResumeCommand(ctx context.Context, userInput string) {
	parts := strings.Fields(strings.TrimSpace(userInput))
	if len(parts) < 2 {
		fmt.Println(colorize("  "+i18n.T("park.resume.usage"), ColorYellow))
		return
	}
	if cli.wasRecentlyResumed(parts[1]) {
		// The auto-resume already consumed this token. Silently no-op.
		return
	}
	token, err := cli.resolveParkToken(parts[1])
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		return
	}
	cli.runResumeForToken(ctx, token, "manual", "")
}

// markRecentlyResumed records that drainPendingResumes consumed a
// token. The auto-injected /resume <token> command lands shortly after;
// handleResumeCommand reads this map to short-circuit cleanly.
func (cli *ChatCLI) markRecentlyResumed(token string) {
	cli.recentlyResumedMu.Lock()
	defer cli.recentlyResumedMu.Unlock()
	if cli.recentlyResumedTokens == nil {
		cli.recentlyResumedTokens = make(map[string]time.Time)
	}
	now := time.Now()
	cli.recentlyResumedTokens[token] = now
	// Sweep stale entries on every write so the map can't grow without
	// bound. 30 s TTL is generous; the auto-injection lands within ms.
	cutoff := now.Add(-30 * time.Second)
	for k, ts := range cli.recentlyResumedTokens {
		if ts.Before(cutoff) {
			delete(cli.recentlyResumedTokens, k)
		}
	}
}

// wasRecentlyResumed returns true when the given token (full or 8-char
// prefix) was consumed by drainPendingResumes within the TTL.
func (cli *ChatCLI) wasRecentlyResumed(input string) bool {
	cli.recentlyResumedMu.Lock()
	defer cli.recentlyResumedMu.Unlock()
	if len(cli.recentlyResumedTokens) == 0 {
		return false
	}
	cutoff := time.Now().Add(-30 * time.Second)
	for full, ts := range cli.recentlyResumedTokens {
		if ts.Before(cutoff) {
			delete(cli.recentlyResumedTokens, full)
			continue
		}
		if full == input || strings.HasPrefix(full, input) {
			return true
		}
	}
	return false
}

// handleCancelParkCommand parses /cancel-park <token> and removes both
// the on-disk snapshot and the scheduler job (best-effort on each).
func (cli *ChatCLI) handleCancelParkCommand(userInput string) {
	parts := strings.Fields(strings.TrimSpace(userInput))
	if len(parts) < 2 {
		fmt.Println(colorize("  "+i18n.T("park.cancel.usage"), ColorYellow))
		return
	}
	token, err := cli.resolveParkToken(parts[1])
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		return
	}

	jobErr, delErr := cli.discardPark(token, "user requested /cancel-park")
	if jobErr != nil {
		fmt.Println(colorize("  ⚠ "+i18n.T("park.cancel.job_failed", jobErr), ColorYellow))
	}
	if delErr != nil {
		fmt.Println(colorize("  ⚠ "+i18n.T("park.cancel.delete_failed", delErr), ColorYellow))
		return
	}
	fmt.Println(colorize("  ✓ "+i18n.T("park.cancel.ok", token), ColorGreen))
}

// discardPark removes a park without resuming it: scheduler job (best
// effort, reported as jobErr) and snapshot. The one body of /cancel-park
// and @park discard.
func (cli *ChatCLI) discardPark(token, reason string) (jobErr, err error) {
	snap, loadErr := park.Load(token)
	if loadErr == nil && snap.SchedulerJobID != "" && cli.scheduler != nil {
		owner := scheduler.Owner{Kind: "park", ID: "agent", Tag: snap.Token}
		jobErr = cli.scheduler.Cancel(scheduler.JobID(snap.SchedulerJobID), reason, owner)
	}
	if err := park.Delete(token); err != nil {
		return jobErr, err
	}
	// Drop any pending resume for this token so the auto-resume hook
	// doesn't fire it after the snapshot is gone, and stop capturing
	// directives for it.
	cli.dropPendingResume(token)
	cli.unregisterActivePark(token)
	// On an unattended surface the parked turn is blocked on its in-turn
	// waiter and nothing else will wake it now: end it here (the turn finds
	// the snapshot gone and closes) and stop mirroring the park.
	if ch := cli.parkWaiter(token); ch != nil {
		select {
		case ch <- parkOutcome{Outcome: parkDiscardOutcome, Detail: reason}:
		default:
		}
		cli.unregisterParkWaiter(token)
	}
	return jobErr, nil
}

// parkDiscardOutcome is handed to the in-turn waiter of a discarded park.
const parkDiscardOutcome = "discarded"

// parkWake implements /parked wake <token> [reason...], the human side of
// @park cancel. Unlike /cancel-park it keeps the agent: it resumes with
// outcome=cancelled and the reason, here or in the process that owns it.
func (cli *ChatCLI) parkWake(args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("parkpar.wake.usage"), ColorYellow))
		return
	}
	token, err := cli.resolveParkToken(args[0])
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		return
	}
	reason := strings.TrimSpace(strings.Join(args[1:], " "))
	if reason == "" {
		reason = "cancelled via /parked wake"
	}
	detail, err := newLiveParkAdapter(cli).CancelPark(token, reason)
	if err != nil {
		fmt.Println(colorize("  ⚠ "+i18n.T("parkpar.wake.failed", err), ColorYellow))
		return
	}
	fmt.Println(colorize("  ✓ "+i18n.T("parkpar.wake.ok", token[:min(8, len(token))], detail), ColorGreen))
}

// resolveParkToken accepts a prefix and returns the unique full token
// or an error. We compare against on-disk snapshots only — the user's
// input is untrusted; the snapshot directory is the canonical set.
func (cli *ChatCLI) resolveParkToken(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New(i18n.T("park.resolve.empty"))
	}
	snaps, _ := park.List()
	var matches []string
	for _, s := range snaps {
		if s.Token == input || strings.HasPrefix(s.Token, input) {
			matches = append(matches, s.Token)
		}
	}
	switch len(matches) {
	case 0:
		return "", errors.New(i18n.T("park.resolve.not_found", input))
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", errors.New(i18n.T("park.resolve.ambiguous", input, strings.Join(matches, ", ")))
	}
}

// runResumeForToken loads the snapshot and re-enters AgentMode via
// RunResumed. Used by both manual /resume and the auto-resume drain.
//
// outcome and detail come from either the AgentResume payload (auto)
// or are fixed strings ("manual", "") for explicit /resume.
func (cli *ChatCLI) runResumeForToken(ctx context.Context, token, outcome, detail string) bool {
	// The wait is over regardless of outcome — stop capturing directives
	// for this token (a re-park inside the resumed run registers anew).
	cli.unregisterActivePark(token)

	snap, err := park.Load(token)
	if err != nil {
		fmt.Println(colorize("  ⚠ "+i18n.T("park.resume.load_failed", err), ColorYellow))
		return false
	}
	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}
	var runErr error
	cli.runWithCancellation(ctx, "Park Resume", func(ctx context.Context) error {
		runErr = cli.agentMode.RunResumed(ctx, snap, outcome, detail)
		return runErr
	})
	if cli.memWorker != nil {
		cli.memWorker.nudge(ctx)
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		// RunResumed kept the snapshot for retry — surface the recovery
		// path, otherwise the user only sees the failure print and has no
		// idea an immediate /resume works.
		short := token
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Println(colorize("  ⚠ "+i18n.T("park.resume.retry_hint", short), ColorYellow))
	}
	return runErr == nil
}

// drainPendingResumes consumes the bridge-populated queue once. Called
// from the outer Run() loop in cli.go between user inputs (right after
// agent/coder/chat dispatch returns), so resumes don't interrupt active
// foreground work but DO fire as soon as the user is back at idle.
//
// Returns true when at least one resume was processed — the caller
// can use that signal to skip the prompt redraw cycle for one tick.
func (cli *ChatCLI) drainPendingResumes(ctx context.Context) bool {
	cli.pendingResumeMu.Lock()
	if len(cli.pendingResumeQueue) == 0 {
		cli.pendingResumeMu.Unlock()
		return false
	}
	tokens := cli.pendingResumeQueue
	cli.pendingResumeQueue = nil
	cli.pendingResumeMu.Unlock()

	processed := false
	for _, token := range tokens {
		// Pull the matching outcome/detail captured by the bridge at
		// notification time. Empty defaults are safe — RunResumed still
		// produces a usable synthetic tool result.
		cli.parkOutcomeMu.Lock()
		out, ok := cli.parkOutcomes[token]
		if ok {
			delete(cli.parkOutcomes, token)
		}
		cli.parkOutcomeMu.Unlock()

		outcome := "elapsed"
		detail := ""
		if ok {
			outcome = out.Outcome
			detail = out.Detail
		}
		// Stamp the idempotency guard ONLY on success: a failed resume keeps
		// its snapshot precisely so the user can retry with /resume, and the
		// 30s recently-resumed window would silently swallow that retry.
		if cli.runResumeForToken(ctx, token, outcome, detail) {
			cli.markRecentlyResumed(token)
		}
		processed = true
	}
	return processed
}

// hasPendingResume reports whether token is already sitting in the
// pending auto-resume queue. The agent stdin drain uses it to swallow
// stray "/resume <token>" lines injected by the scheduler bridge that
// raced with an active run — they must not reach the LLM as user text.
func (cli *ChatCLI) hasPendingResume(token string) bool {
	cli.pendingResumeMu.Lock()
	defer cli.pendingResumeMu.Unlock()
	for _, t := range cli.pendingResumeQueue {
		if t == token {
			return true
		}
	}
	return false
}

// parsePendingResumeLine extracts the token from a "/resume <token>"
// line exactly as typed/injected — no prefix resolution, no fuzzy
// matching. Only the canonical two-field form is accepted.
func parsePendingResumeLine(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) == 2 && fields[0] == "/resume" {
		return fields[1], true
	}
	return "", false
}

// dropPendingResume removes a token from the pending queue and the
// outcome map. Used by /cancel-park to undo a queued auto-resume.
func (cli *ChatCLI) dropPendingResume(token string) {
	cli.pendingResumeMu.Lock()
	if len(cli.pendingResumeQueue) > 0 {
		filtered := cli.pendingResumeQueue[:0]
		for _, t := range cli.pendingResumeQueue {
			if t != token {
				filtered = append(filtered, t)
			}
		}
		cli.pendingResumeQueue = filtered
	}
	cli.pendingResumeMu.Unlock()

	cli.parkOutcomeMu.Lock()
	delete(cli.parkOutcomes, token)
	cli.parkOutcomeMu.Unlock()
}

// parkPrune removes on-disk snapshots whose scheduler job has reached
// a terminal state (completed / failed / cancelled / timed_out). The
// scheduler keeps terminal jobs around for the audit window, but the
// snapshot file is dead weight — the agent has either resumed (and
// the snapshot was kept for forensics) or the job will never fire.
func (cli *ChatCLI) parkPrune() {
	snaps, _ := park.List()
	if len(snaps) == 0 {
		fmt.Println(colorize("  "+i18n.T("park.list.empty"), ColorGray))
		return
	}
	removed := 0
	kept := 0
	for _, s := range snaps {
		terminal := false
		if cli.scheduler != nil && s.SchedulerJobID != "" {
			j, err := cli.scheduler.Query(scheduler.JobID(s.SchedulerJobID))
			if err != nil || j == nil {
				// Missing job means the snapshot is orphaned — prune it.
				terminal = true
			} else {
				terminal = j.Status.IsTerminal()
			}
		} else {
			// No scheduler reference → treat as orphaned.
			terminal = true
		}
		if terminal {
			if err := park.Delete(s.Token); err == nil {
				removed++
			}
		} else {
			kept++
		}
	}
	fmt.Printf("  %s: %d removed, %d still pending\n",
		colorize("✓ parked prune", ColorGreen), removed, kept)
}

// parkGC removes snapshots older than a Go duration regardless of
// scheduler state. /parked gc 24h is the typical invocation.
func (cli *ChatCLI) parkGC(args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("park.cmd.gc_usage"), ColorYellow))
		return
	}
	d, err := parseParkAge(args[0])
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("park.cmd.gc_bad_duration", args[0]), ColorYellow))
		return
	}
	cutoff := time.Now().Add(-d)
	removed, errs := SweepStaleParks(cutoff)
	for _, e := range errs {
		fmt.Println(colorize("  ⚠ "+e.Error(), ColorYellow))
	}
	fmt.Printf("  %s: %d removed (older than %s)\n",
		colorize("✓ parked gc", ColorGreen), removed, d)
}

// SweepStaleParks deletes snapshots whose CreatedAt is older than the
// cutoff. Returns the count removed and any per-file errors. Suitable
// for a background goroutine; we don't wire one yet but expose the
// helper so /parked gc can call it on demand.
func SweepStaleParks(cutoff time.Time) (int, []error) {
	snaps, errs := park.List()
	removed := 0
	for _, s := range snaps {
		if s.CreatedAt.Before(cutoff) {
			if err := park.Delete(s.Token); err == nil {
				removed++
			} else {
				errs = append(errs, fmt.Errorf("%s: %w", s.Token, err))
			}
		}
	}
	return removed, errs
}

// printParkedUsage prints the /parked subcommands.
func printParkedUsage() {
	for _, key := range []string{"park.cmd.usage_list", "park.cmd.usage_prune", "park.cmd.usage_gc", "parkpar.usage_wake", "parkpar.usage_verbs"} {
		fmt.Println(colorize("  "+i18n.T(key), ColorGray))
	}
}

// parseParkAge reads the age for /parked gc: a Go duration, or whole days
// ("7d", which the completer has always advertised and time.ParseDuration
// rejects). It must be positive: a zero or negative age put the cutoff at
// or after now, and the sweep then deleted every snapshot.
func parseParkAge(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	var d time.Duration
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, err
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, err
		}
		d = parsed
	}
	if d <= 0 {
		return 0, fmt.Errorf("age must be positive")
	}
	return d, nil
}

// nearSubcommand returns the subcommand word is a near-miss of (one edit,
// or two adjacent letters swapped), or "" when it is none or is one of
// them exactly.
func nearSubcommand(word string, subs ...string) string {
	word = strings.ToLower(word)
	for _, s := range subs {
		if word == s {
			return ""
		}
	}
	for _, s := range subs {
		if len(s) >= 4 && withinOneEdit(word, s) {
			return s
		}
	}
	return ""
}

// withinOneEdit reports whether a becomes b by one insertion, deletion or
// substitution, or by swapping two adjacent letters.
func withinOneEdit(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == len(rb) {
		var diff []int
		for i := range ra {
			if ra[i] != rb[i] {
				diff = append(diff, i)
			}
		}
		switch len(diff) {
		case 1:
			return true
		case 2:
			i, j := diff[0], diff[1]
			return j == i+1 && ra[i] == rb[j] && ra[j] == rb[i]
		}
		return false
	}
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	if len(rb)-len(ra) != 1 {
		return false
	}
	i, j, skipped := 0, 0, false
	for i < len(ra) && j < len(rb) {
		if ra[i] == rb[j] {
			i++
			j++
			continue
		}
		if skipped {
			return false
		}
		skipped = true
		j++
	}
	return true
}
