/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * ChatCLI - scheduler_wait.go
 *
 * /wait, and everything that lets a wait run without holding the prompt.
 *
 * A wait is a scheduler job: the scheduler polls the condition whether or
 * not anyone is watching. /wait therefore registers the job and returns;
 * the terminal is told when the job finishes (jobNotifier). Holding the
 * prompt until then is a choice the user makes with --block, and letting
 * go of it with Ctrl+C never stops the job.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
)

// notifyTag marks a job whose end the terminal announces. /wait sets it
// unless --quiet; /schedule sets it with --notify. A tag, so it is stored
// with the job, survives a restart and travels to the daemon unchanged.
const notifyTag = "notify"

// Follow timings; variables so tests do not wait real seconds.
var (
	followPollInterval = 500 * time.Millisecond
	followBeatInterval = 20 * time.Second
	// followMaxMisses is how many consecutive failed lookups end a follow:
	// the job was pruned, or the daemon went away.
	followMaxMisses = 10
)

// handleWaitCommand implements /wait: register the wait as a background
// job and return. With --block the prompt is held until the job ends.
func (cli *ChatCLI) handleWaitCommand(ctx context.Context, input string) {
	if !cli.schedulerEnabled() {
		fmt.Println(colorize("  "+i18n.T("scheduler.disabled"), ColorYellow))
		return
	}
	args, err := tokenizeSchedulerInput(strings.TrimPrefix(input, "/wait"))
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	if len(args) == 0 || args[0] == "help" {
		cli.printWaitUsage()
		return
	}
	in, err := parseWaitArgs(args)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		cli.printWaitUsage()
		return
	}
	// The job is always created detached; --block is a view on it.
	block := !in.Async
	in.Async = true

	out, err := cli.schedulerEnqueueWait(ctx, in)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	if !out.OK {
		fmt.Println(colorize("  ❌ "+out.Error, ColorRed))
		return
	}
	cli.jobNotices.own(out.JobID)
	fmt.Println(colorize("  ✔ "+i18n.T("scheduler.wait.registered", in.Name, out.JobID), ColorGreen))

	headless := cli.schedulerHeadless()
	if block && headless {
		// A held command would pin this surface's single command slot
		// for every session until the condition is met.
		fmt.Println(colorize("  "+i18n.T("scheduler.wait.block_unavailable"), ColorYellow))
		block = false
	}
	if block {
		cli.followJob(ctx, out.JobID, 0)
		return
	}
	hint := "scheduler.wait.background_hint"
	if headless || in.Tags[notifyTag] != "on" {
		hint = "scheduler.wait.background_hint_quiet"
	}
	fmt.Println(colorize("  "+i18n.T(hint, out.JobID, out.JobID), ColorGray))
}

// schedulerEnqueueWait creates the wait job on whichever scheduler is
// wired and returns as soon as it is admitted.
func (cli *ChatCLI) schedulerEnqueueWait(ctx context.Context, in scheduler.ToolInput) (*scheduler.ToolOutput, error) {
	in.Async = true
	owner := cli.currentSchedulerOwner()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if cli.schedulerRemote != nil {
		return cli.schedulerRemote.Enqueue(ctx, owner, in)
	}
	res, _ := scheduler.NewToolAdapter(cli.scheduler).WaitUntil(ctx, owner, mustJSON(in))
	var out scheduler.ToolOutput
	if err := jsonDecode(res, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// schedulerHeadless reports whether this command runs with no terminal to
// hold: an unattended surface (ACP, MCP, gateway, server) or a command
// captured for the web page. Blocking there holds the surface, not a
// person's prompt.
func (cli *ChatCLI) schedulerHeadless() bool {
	return cli.unattended || rpcCaptureActive.Load()
}

// followJob holds the prompt until the job ends, printing a sign of life
// now and then. Ctrl+C (or the limit, when positive) lets go of the prompt
// and leaves the job running.
func (cli *ChatCLI) followJob(ctx context.Context, id scheduler.JobID, limit time.Duration) {
	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	// Ctrl+C reaches the process-wide signal handler, which ends the
	// session unless something interruptible is registered. The REPL
	// registers every slash command; a follow started any other way
	// registers itself here (a no-op when already inside one).
	stopCtx, release := cli.beginInterruptible(stopCtx)
	defer release()
	if limit > 0 {
		var cancel context.CancelFunc
		stopCtx, cancel = context.WithTimeout(stopCtx, limit)
		defer cancel()
	}
	cli.jobNotices.follow(id)
	defer cli.jobNotices.unfollow(id)

	fmt.Println(colorize("  "+i18n.T("scheduler.follow.started", id), ColorGray))
	tick := time.NewTicker(followPollInterval)
	defer tick.Stop()
	nextBeat := time.Now().Add(followBeatInterval)
	misses := 0
	for {
		// Lookups use the caller's context: the follow being let go of
		// must not turn the final lookup into a failure.
		if j := cli.schedulerQuery(ctx, id); j == nil {
			misses++
			if misses >= followMaxMisses {
				fmt.Println(colorize("  ⚠ "+i18n.T("scheduler.follow.lost", id), ColorYellow))
				return
			}
		} else {
			misses = 0
			if j.Status.IsTerminal() {
				printJobOutcome(j, false)
				return
			}
			if time.Now().After(nextBeat) {
				fmt.Println(colorize("  ⏳ "+followProgressLine(j), ColorGray))
				nextBeat = time.Now().Add(followBeatInterval)
			}
		}
		select {
		case <-stopCtx.Done():
			if j := cli.schedulerQuery(ctx, id); j != nil && j.Status.IsTerminal() {
				printJobOutcome(j, false)
				return
			}
			fmt.Println(colorize("\n  "+i18n.T("scheduler.follow.detached", id, id), ColorYellow))
			return
		case <-tick.C:
		}
	}
}

// followProgressLine is the sign of life printed while following a job.
func followProgressLine(j *scheduler.Job) string {
	sum := j.Summary()
	if sum.Status == scheduler.StatusWaiting {
		left := "—"
		if !sum.WaitDeadline.IsZero() {
			left = shortDuration(time.Until(sum.WaitDeadline))
		}
		line := i18n.T("scheduler.follow.progress_waiting", sum.Name, sum.WaitPolls, left)
		if sum.WaitDetail != "" {
			line += " — " + sum.WaitDetail
		}
		return line
	}
	return i18n.T("scheduler.follow.progress", sum.Name, string(sum.Status))
}

// ─── Completion notices ───────────────────────────────────────

// jobNotifier decides which finished jobs the terminal announces. Zero
// value is ready to use.
type jobNotifier struct {
	mu        sync.Mutex
	owned     map[scheduler.JobID]bool // created by this process
	following map[scheduler.JobID]int  // held by a foreground follow
	told      map[string]bool          // job+phase already announced
	// events hands scheduler events to the notice loop. Nil until the
	// loop starts; an event offered before that is not announced.
	events chan scheduler.Event
}

// offer queues an event for the notice loop without ever blocking the
// scheduler's dispatch path; with the queue full the notice is skipped
// (the job listing still has the outcome).
func (n *jobNotifier) offer(evt scheduler.Event) {
	n.mu.Lock()
	ch := n.events
	n.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- evt:
	default:
	}
}

// startJobNotices runs the loop that announces finished jobs for as long
// as ctx lives. One loop, so notices come out in the order jobs ended.
func (cli *ChatCLI) startJobNotices(ctx context.Context) {
	ch := make(chan scheduler.Event, 64)
	cli.jobNotices.mu.Lock()
	cli.jobNotices.events = ch
	cli.jobNotices.mu.Unlock()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-ch:
				cli.announceJobEvent(ctx, evt)
			}
		}
	}()
}

func (n *jobNotifier) own(id scheduler.JobID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.owned == nil {
		n.owned = map[scheduler.JobID]bool{}
	}
	n.owned[id] = true
}

func (n *jobNotifier) owns(id scheduler.JobID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.owned[id]
}

func (n *jobNotifier) follow(id scheduler.JobID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.following == nil {
		n.following = map[scheduler.JobID]int{}
	}
	n.following[id]++
}

func (n *jobNotifier) unfollow(id scheduler.JobID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.following[id]--; n.following[id] <= 0 {
		delete(n.following, id)
	}
}

func (n *jobNotifier) isFollowed(id scheduler.JobID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.following[id] > 0
}

// firstTime reports whether key was not announced before, and records it.
func (n *jobNotifier) firstTime(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.told[key] {
		return false
	}
	if n.told == nil {
		n.told = map[string]bool{}
	}
	n.told[key] = true
	return true
}

// firstTimePeek reports whether key is still unannounced, without
// recording it.
func (n *jobNotifier) firstTimePeek(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return !n.told[key]
}

// jobEventEndsRun reports whether evt closes a run of its job: the job
// reached a terminal state, or a recurring job finished a cycle. A wait
// that timed out emits job.timed_out twice — once when the budget runs
// out, once when the job is marked — and only the second is the end.
func jobEventEndsRun(evt scheduler.Event) bool {
	switch evt.Type {
	case scheduler.EventJobCompleted:
		return true
	case scheduler.EventJobFailed, scheduler.EventJobTimedOut:
		return evt.Status.IsTerminal()
	}
	return false
}

// onSchedulerEvent is the notifier's entry point, called for every
// scheduler event. It only queues the event: the announcement looks the
// job up and may wait for the terminal, neither of which belongs on the
// scheduler's dispatch path.
func (cli *ChatCLI) onSchedulerEvent(evt scheduler.Event) {
	if !jobEventEndsRun(evt) || !cli.replInteractive.Load() || cli.unattended {
		return
	}
	cli.jobNotices.offer(evt)
}

// announceJobEvent prints the end of a job above the prompt when the job
// asked for it and nothing in the foreground is already reporting it.
func (cli *ChatCLI) announceJobEvent(ctx context.Context, evt scheduler.Event) {
	if cli.jobNotices.isFollowed(evt.JobID) {
		return
	}
	j := cli.schedulerQuery(ctx, evt.JobID)
	if j == nil || !jobWantsNotice(j) {
		return
	}
	// A daemon serves every terminal on the host: announce only the jobs
	// this terminal created or this session owns.
	if cli.schedulerRemote != nil && !cli.jobNotices.owns(j.ID) {
		me := cli.currentSchedulerOwner()
		if j.Owner.Kind != me.Kind || j.Owner.ID != me.ID {
			return
		}
	}
	cycle := !j.Status.IsTerminal()
	key := string(j.ID) + "|" + string(j.Status)
	if cycle {
		key += "|" + strconv.Itoa(j.CycleCount)
	}
	if !cli.jobNotices.firstTime(key) {
		return
	}
	// While a command is being captured for another surface, stdout is
	// that surface's reply. Wait for it rather than leak into it.
	for i := 0; rpcCaptureActive.Load(); i++ {
		if i >= 150 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println()
	printJobOutcome(j, cycle)
	fmt.Println()
	cli.markSchedulerDirty()
	// Redraw the input line the notice was printed over — unless a turn is
	// streaming, which redraws the prompt itself when it ends.
	if !cli.isExecuting.Load() {
		cli.forceRefreshPrompt()
	}
}

// jobWantsNotice reports whether the job carries the notify tag.
func jobWantsNotice(j *scheduler.Job) bool {
	switch strings.ToLower(strings.TrimSpace(j.Tags[notifyTag])) {
	case "on", "true", "yes", "1":
		return true
	}
	return false
}

// printJobOutcome prints how a job ended (or, for a recurring job, how its
// latest cycle went): one headline, what the action printed, and where to
// read the rest.
func printJobOutcome(j *scheduler.Job, cycle bool) {
	took := shortDuration(jobElapsed(j))
	switch {
	case cycle:
		fmt.Println(colorize("  🔔 "+i18n.T("scheduler.notify.cycle", j.Name, j.ID, j.CycleCount), ColorCyan+ColorBold))
		printJobActionOutput(j)
	case j.Status == scheduler.StatusCompleted:
		key := "scheduler.notify.completed"
		if j.Wait != nil {
			key = "scheduler.notify.wait_met"
		}
		fmt.Println(colorize("  🔔 "+i18n.T(key, j.Name, j.ID, took), ColorGreen+ColorBold))
		printJobActionOutput(j)
	case j.Status == scheduler.StatusTimedOut:
		fmt.Println(colorize("  ⏱ "+i18n.T("scheduler.notify.timed_out", j.Name, j.ID, took), ColorYellow+ColorBold))
		if d := lastConditionDetail(j); d != "" {
			fmt.Println(colorize("    "+i18n.T("scheduler.notify.last_check", d), ColorGray))
		}
	case j.Status == scheduler.StatusCancelled:
		fmt.Println(colorize("  ⊘ "+i18n.T("scheduler.notify.cancelled", j.Name, j.ID), ColorYellow))
	default:
		fmt.Println(colorize("  ✗ "+i18n.T("scheduler.notify.failed", j.Name, j.ID, jobEndReason(j)), ColorRed+ColorBold))
	}
	fmt.Println(colorize("    "+i18n.T("scheduler.notify.hint", j.ID), ColorGray))
}

// printJobActionOutput shows what the job's action ran and the head of
// what it printed. A noop action — a bare wait — has nothing to show.
func printJobActionOutput(j *scheduler.Job) {
	if j.Action.Type == scheduler.ActionNoop {
		return
	}
	label := string(j.Action.Type)
	for _, k := range []string{"command", "task", "prompt", "url", "event"} {
		if v, ok := j.Action.Payload[k].(string); ok && v != "" {
			label = v
			break
		}
	}
	fmt.Println(colorize("    "+i18n.T("scheduler.notify.ran", truncateBridge(label, 100)), ColorGray))
	if j.LastResult == nil || strings.TrimSpace(j.LastResult.Output) == "" {
		return
	}
	lines := strings.Split(strings.TrimRight(j.LastResult.Output, "\n"), "\n")
	const maxLines = 6
	shown := lines
	if len(shown) > maxLines {
		shown = shown[:maxLines]
	}
	for _, l := range shown {
		fmt.Println(colorize("      "+truncateBridge(l, 160), ColorGray))
	}
	if len(lines) > maxLines {
		fmt.Println(colorize("      "+i18n.T("scheduler.notify.more_lines", len(lines)-maxLines), ColorGray))
	}
}

// jobElapsed is how long the job took from creation to its end (or to
// its latest update, for a job still alive).
func jobElapsed(j *scheduler.Job) time.Duration {
	end := j.FinishedAt
	if end.IsZero() {
		end = j.UpdatedAt
	}
	if end.Before(j.CreatedAt) {
		return 0
	}
	return end.Sub(j.CreatedAt)
}

// jobEndReason is why the job ended: the message of its last transition,
// falling back to the last execution's error.
func jobEndReason(j *scheduler.Job) string {
	if n := len(j.Transitions); n > 0 && j.Transitions[n-1].Message != "" {
		return truncateBridge(j.Transitions[n-1].Message, 200)
	}
	if j.LastResult != nil && j.LastResult.Error != "" {
		return truncateBridge(j.LastResult.Error, 200)
	}
	return string(j.Status)
}

// lastConditionDetail is what the latest condition check reported.
func lastConditionDetail(j *scheduler.Job) string {
	for i := len(j.History) - 1; i >= 0; i-- {
		d := j.History[i].ConditionDetails
		if d == "" {
			d = j.History[i].Error
		}
		if d = strings.TrimSpace(d); d != "" {
			if nl := strings.IndexByte(d, '\n'); nl >= 0 {
				d = d[:nl]
			}
			return truncateBridge(d, 160)
		}
	}
	return ""
}

// shortDuration renders a duration for a status line: whole seconds below
// an hour, whole minutes above.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	if d < time.Hour {
		return d.Round(time.Second).String()
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}

// watchRemoteJobEvents feeds the notifier from the daemon's event stream.
// In-process, the scheduler bridge delivers events; a daemon runs in
// another process, so the terminal subscribes to it instead.
func (cli *ChatCLI) watchRemoteJobEvents(ctx context.Context, c *scheduler.RemoteClient) {
	subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	events, err := c.Subscribe(subCtx)
	cancel()
	if err != nil {
		return
	}
	go func() {
		for evt := range events {
			cli.markSchedulerDirty()
			cli.onSchedulerEvent(evt)
		}
	}()
}

// ─── Job references ───────────────────────────────────────────

// resolveJobRef turns what the user typed — a job ID, a job name, or the
// start of an ID — into the job's ID. A name that several jobs carried
// resolves to the live one, else to the most recent. msg is non-empty when
// nothing, or more than one thing, matches.
func (cli *ChatCLI) resolveJobRef(ctx context.Context, ref string) (id scheduler.JobID, msg string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", i18n.T("scheduler.jobs.not_found", ref)
	}
	if cli.schedulerQuery(ctx, scheduler.JobID(ref)) != nil {
		return scheduler.JobID(ref), ""
	}
	all := cli.schedulerList(ctx, scheduler.ListFilter{IncludeTerminal: true})
	id, err := scheduler.MatchJobRef(all, ref)
	if err == nil {
		return id, ""
	}
	if errors.Is(err, scheduler.ErrJobAmbiguous) {
		return "", i18n.T("scheduler.jobs.ambiguous", ref)
	}
	return "", i18n.T("scheduler.jobs.not_found", ref)
}

// jobsWait implements /jobs wait <id|name> [--timeout D]: hold the prompt
// on a job that already exists.
func (cli *ChatCLI) jobsWait(ctx context.Context, args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.wait_usage"), ColorYellow))
		return
	}
	var limit time.Duration
	ref := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--timeout":
			val, ni := takeString(args, i)
			d, err := scheduler.ParseLooseDuration(val)
			if err != nil {
				fmt.Println(colorize("  ❌ --timeout: "+err.Error(), ColorRed))
				return
			}
			limit, i = d, ni
		case strings.HasPrefix(args[i], "--"):
			fmt.Println(colorize("  ❌ "+i18n.T("scheduler.jobs.unknown_flag", args[i]), ColorRed))
			return
		case ref == "":
			ref = args[i]
		}
	}
	id, msg := cli.resolveJobRef(ctx, ref)
	if msg != "" {
		fmt.Println(colorize("  "+msg, ColorYellow))
		return
	}
	if cli.schedulerHeadless() {
		// Nothing to hold here: report where the job stands instead.
		fmt.Println(colorize("  "+i18n.T("scheduler.wait.block_unavailable"), ColorYellow))
		if j := cli.schedulerQuery(ctx, id); j != nil {
			fmt.Println(scheduler.RenderShow(j))
		}
		return
	}
	cli.followJob(ctx, id, limit)
}

// ─── Argument parsing ─────────────────────────────────────────

// waitValueFlags are the /wait flags that take one value and store it as
// is. --timeout is the wait's own budget here; the action's is
// --action-timeout.
var waitValueFlags = map[string]func(*scheduler.ToolInput) *string{
	"--until":          func(in *scheduler.ToolInput) *string { return &in.Until },
	"--then":           func(in *scheduler.ToolInput) *string { return &in.Do },
	"--do":             func(in *scheduler.ToolInput) *string { return &in.Do },
	"--every":          func(in *scheduler.ToolInput) *string { return &in.PollInterval },
	"--poll":           func(in *scheduler.ToolInput) *string { return &in.PollInterval },
	"--timeout":        func(in *scheduler.ToolInput) *string { return &in.WaitTimeout },
	"--wait-timeout":   func(in *scheduler.ToolInput) *string { return &in.WaitTimeout },
	"--action-timeout": func(in *scheduler.ToolInput) *string { return &in.Timeout },
	"--on-timeout":     func(in *scheduler.ToolInput) *string { return &in.OnTimeout },
	"--name":           func(in *scheduler.ToolInput) *string { return &in.Name },
	"--description":    func(in *scheduler.ToolInput) *string { return &in.Description },
	"--ttl":            func(in *scheduler.ToolInput) *string { return &in.TTL },
}

// parseWaitArgs reads /wait flags into a ToolInput. Async is true unless
// --block was given; the notify tag is set unless --quiet was.
func parseWaitArgs(args []string) (scheduler.ToolInput, error) {
	in := scheduler.ToolInput{Tags: map[string]string{}, Async: true}
	notify := true
	for i := 0; i < len(args); i++ {
		if target, ok := waitValueFlags[args[i]]; ok {
			// A flag that ends the line used to be read as an empty value.
			if i+1 >= len(args) {
				return in, fmt.Errorf("wait: %s needs a value", args[i])
			}
			*target(&in), i = takeString(args, i)
			continue
		}
		ni, handled, err := parseSharedJobFlag("wait", &in, args, i)
		if err != nil {
			return in, err
		}
		if handled {
			i = ni
			continue
		}
		switch args[i] {
		case "--block", "--sync", "--foreground", "--fg":
			in.Async = false
		case "--async", "--background", "--bg":
			// The default since waits stopped holding the prompt; still
			// accepted so existing habits and scripts keep working.
			in.Async = true
		case "--quiet", "--no-notify":
			notify = false
		case "--notify":
			notify = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return in, fmt.Errorf("wait: unknown flag %s", args[i])
			}
			if in.Until != "" {
				return in, fmt.Errorf("wait: unexpected positional %q (did you forget to quote a multi-word value?)", args[i])
			}
			// A bare first argument is the condition: /wait tcp://db:5432
			in.Until = args[i]
		}
	}
	return finishWaitInput(in, notify)
}

// finishWaitInput validates the condition and fills what /wait defaults.
func finishWaitInput(in scheduler.ToolInput, notify bool) (scheduler.ToolInput, error) {
	if in.Until == "" {
		return in, fmt.Errorf("wait: a condition is required (--until <cond>)")
	}
	if _, err := scheduler.ParseConditionDSL(in.Until); err != nil {
		return in, fmt.Errorf("wait: %w", err)
	}
	in.When = "when-ready"
	if in.Do == "" {
		in.Do = "noop"
	}
	if in.Name == "" {
		host, _ := os.Hostname()
		in.Name = fmt.Sprintf("wait-%s-%d", host, time.Now().UnixNano()%100000)
	}
	if _, set := in.Tags[notifyTag]; !set {
		in.Tags[notifyTag] = "off"
		if notify {
			in.Tags[notifyTag] = "on"
		}
	}
	return in, nil
}
