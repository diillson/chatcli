/*
 * ChatCLI - scheduler_command.go
 *
 * Top-level command handlers for /schedule, /wait, /jobs and the agent
 * tool adapters. Routes to either the in-process scheduler or the
 * remote daemon, transparently.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
)

// handleScheduleCommand implements /schedule …
func (cli *ChatCLI) handleScheduleCommand(input string) {
	if !cli.schedulerEnabled() {
		fmt.Println(colorize("  "+i18n.T("scheduler.disabled"), ColorYellow))
		return
	}
	args := strings.Fields(strings.TrimPrefix(input, "/schedule"))
	if len(args) == 0 {
		cli.printScheduleUsage()
		return
	}
	if args[0] == "help" {
		cli.printScheduleUsage()
		return
	}
	in, err := parseScheduleArgs(args)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		cli.printScheduleUsage()
		return
	}
	owner := cli.currentSchedulerOwner()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if cli.schedulerRemote != nil {
		out, err := cli.schedulerRemote.Enqueue(ctx, owner, in)
		if err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		if !out.OK {
			fmt.Println(colorize("  ❌ "+out.Error, ColorRed))
			return
		}
		fmt.Println(colorize("  ✔ "+i18n.T("scheduler.created", out.JobID, in.Name), ColorGreen))
		return
	}

	adapter := scheduler.NewToolAdapter(cli.scheduler)
	raw := mustJSON(in)
	res, _ := adapter.ScheduleJob(ctx, owner, raw)
	var out scheduler.ToolOutput
	_ = jsonDecode(res, &out)
	if !out.OK {
		fmt.Println(colorize("  ❌ "+out.Error, ColorRed))
		return
	}
	fmt.Println(colorize("  ✔ "+i18n.T("scheduler.created", out.JobID, in.Name), ColorGreen))
}

// handleWaitCommand implements /wait …
func (cli *ChatCLI) handleWaitCommand(input string) {
	if !cli.schedulerEnabled() {
		fmt.Println(colorize("  "+i18n.T("scheduler.disabled"), ColorYellow))
		return
	}
	args := strings.Fields(strings.TrimPrefix(input, "/wait"))
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
	owner := cli.currentSchedulerOwner()
	// Wait blocks by default; use --async to fire-and-forget.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	if cli.schedulerRemote != nil {
		out, err := cli.schedulerRemote.Enqueue(ctx, owner, in)
		if err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		if !out.OK {
			fmt.Println(colorize("  ❌ "+out.Error, ColorRed))
			return
		}
		if in.Async {
			fmt.Println(colorize("  ✔ "+i18n.T("scheduler.wait_async", out.JobID), ColorGreen))
			return
		}
		// Poll for terminal status.
		waitForTerminal(ctx, cli.schedulerRemote, out.JobID)
		return
	}
	adapter := scheduler.NewToolAdapter(cli.scheduler)
	raw := mustJSON(in)
	res, _ := adapter.WaitUntil(ctx, owner, raw)
	var out scheduler.ToolOutput
	_ = jsonDecode(res, &out)
	if !out.OK {
		fmt.Println(colorize("  ❌ "+out.Error, ColorRed))
		return
	}
	if in.Async {
		fmt.Println(colorize("  ✔ "+i18n.T("scheduler.wait_async", out.JobID), ColorGreen))
		return
	}
	fmt.Println(colorize(fmt.Sprintf("  ✔ %s", i18n.T("scheduler.wait_completed", out.JobID, string(out.Outcome))), ColorGreen))
	if out.Details != "" {
		fmt.Println(colorize(out.Details, ColorGray))
	}
}

// handleJobsCommand implements /jobs …
func (cli *ChatCLI) handleJobsCommand(input string) {
	if !cli.schedulerEnabled() {
		fmt.Println(colorize("  "+i18n.T("scheduler.disabled"), ColorYellow))
		return
	}
	args := strings.Fields(strings.TrimPrefix(input, "/jobs"))
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "help":
		cli.printJobsUsage()
	case "list":
		cli.jobsList(args[1:])
	case "show":
		cli.jobsShow(args[1:])
	case "tree":
		cli.jobsTree(args[1:])
	case "cancel":
		cli.jobsCancel(args[1:])
	case "pause":
		cli.jobsPause(args[1:])
	case "resume":
		cli.jobsResume(args[1:])
	case "logs":
		cli.jobsLogs(args[1:])
	case "history":
		cli.jobsList([]string{"--all"})
	case "daemon":
		cli.jobsDaemon(args[1:])
	case "gc":
		cli.jobsGC()
	default:
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.unknown", args[0]), ColorYellow))
		cli.printJobsUsage()
	}
}

// ─── /jobs subcommands ────────────────────────────────────────

func (cli *ChatCLI) jobsList(args []string) {
	filter := scheduler.ListFilter{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			filter.IncludeTerminal = true
		case "--status":
			if i+1 < len(args) {
				filter.Statuses = append(filter.Statuses, scheduler.JobStatus(args[i+1]))
				i++
			}
		case "--owner":
			if i+1 < len(args) {
				owner := scheduler.Owner{Kind: scheduler.OwnerKind(args[i+1])}
				if owner.Kind == "me" {
					owner = cli.currentSchedulerOwner()
				}
				filter.Owner = &owner
				i++
			}
		case "--tag":
			if i+1 < len(args) {
				filter.Tag = args[i+1]
				i++
			}
		case "--name":
			if i+1 < len(args) {
				filter.NameSubstr = args[i+1]
				i++
			}
		}
	}
	summaries := cli.schedulerList(filter)
	fmt.Println(scheduler.RenderList(summaries))
}

func (cli *ChatCLI) jobsShow(args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.show_usage"), ColorYellow))
		return
	}
	id := scheduler.JobID(args[0])
	j := cli.schedulerQuery(id)
	if j == nil {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.not_found", id), ColorYellow))
		return
	}
	fmt.Println(scheduler.RenderShow(j))
}

func (cli *ChatCLI) jobsTree(_ []string) {
	summaries := cli.schedulerList(scheduler.ListFilter{IncludeTerminal: true})
	jobs := make([]*scheduler.Job, 0, len(summaries))
	for _, s := range summaries {
		if j := cli.schedulerQuery(s.ID); j != nil {
			jobs = append(jobs, j)
		}
	}
	out := scheduler.RenderTree(jobs)
	if out == "" {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.empty"), ColorGray))
		return
	}
	fmt.Println(out)
}

func (cli *ChatCLI) jobsCancel(args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.cancel_usage"), ColorYellow))
		return
	}
	id := scheduler.JobID(args[0])
	reason := "user-cancelled"
	if len(args) > 1 {
		reason = strings.Join(args[1:], " ")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if cli.schedulerRemote != nil {
		err = cli.schedulerRemote.Cancel(ctx, cli.currentSchedulerOwner(), id, reason)
	} else {
		err = cli.scheduler.Cancel(id, reason, cli.currentSchedulerOwner())
	}
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	fmt.Println(colorize("  ✔ "+i18n.T("scheduler.jobs.cancelled", id), ColorGreen))
}

func (cli *ChatCLI) jobsPause(args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.pause_usage"), ColorYellow))
		return
	}
	id := scheduler.JobID(args[0])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if cli.schedulerRemote != nil {
		err = cli.schedulerRemote.Pause(ctx, cli.currentSchedulerOwner(), id, "user-paused")
	} else {
		err = cli.scheduler.Pause(id, "user-paused", cli.currentSchedulerOwner())
	}
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	fmt.Println(colorize("  ✔ "+i18n.T("scheduler.jobs.paused", id), ColorGreen))
}

func (cli *ChatCLI) jobsResume(args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.resume_usage"), ColorYellow))
		return
	}
	id := scheduler.JobID(args[0])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if cli.schedulerRemote != nil {
		err = cli.schedulerRemote.Resume(ctx, cli.currentSchedulerOwner(), id)
	} else {
		err = cli.scheduler.Resume(id, cli.currentSchedulerOwner())
	}
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	fmt.Println(colorize("  ✔ "+i18n.T("scheduler.jobs.resumed", id), ColorGreen))
}

func (cli *ChatCLI) jobsLogs(args []string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.logs_usage"), ColorYellow))
		return
	}
	id := scheduler.JobID(args[0])
	j := cli.schedulerQuery(id)
	if j == nil {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.not_found", id), ColorYellow))
		return
	}
	if len(j.History) == 0 {
		fmt.Println(colorize("  "+i18n.T("scheduler.jobs.logs_empty"), ColorGray))
		return
	}
	for _, h := range j.History {
		fmt.Printf("  #%d %s %s (%s)\n", h.AttemptNum, h.StartedAt.Format(time.RFC3339), h.Outcome, h.Duration)
		if h.Output != "" {
			fmt.Println(colorize(indent("    ", h.Output), ColorGray))
		}
		if h.Error != "" {
			fmt.Println(colorize(indent("    ", "err: "+h.Error), ColorRed))
		}
		if h.ConditionDetails != "" {
			fmt.Println(colorize(indent("    ", "cond: "+h.ConditionDetails), ColorGray))
		}
	}
}

func (cli *ChatCLI) jobsDaemon(args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		if cli.schedulerRemote != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			stats, err := cli.schedulerRemote.Stats(ctx)
			if err != nil {
				fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
				return
			}
			fmt.Printf("  %s\n  uptime   : %s\n  jobs     : %d active, queue=%d\n  wal segs : %d\n  clients  : %d\n",
				i18n.T("scheduler.daemon.status_header"),
				stats.Uptime, stats.JobsActive, stats.QueueDepth, stats.WALSegments, stats.Connections)
			return
		}
		if cli.scheduler != nil {
			fmt.Println("  " + i18n.T("scheduler.daemon.local"))
			return
		}
		fmt.Println(colorize("  "+i18n.T("scheduler.daemon.none"), ColorGray))
	case "start", "stop", "restart":
		fmt.Println(colorize("  "+i18n.T("scheduler.daemon.use_subcommand"), ColorYellow))
	default:
		fmt.Println(colorize("  "+i18n.T("scheduler.daemon.unknown", sub), ColorYellow))
	}
}

func (cli *ChatCLI) jobsGC() {
	if cli.schedulerRemote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = cli.schedulerRemote.Snapshot(ctx)
	} else if cli.scheduler != nil {
		_ = cli.scheduler.Snapshot()
	}
	fmt.Println(colorize("  ✔ "+i18n.T("scheduler.jobs.gc_done"), ColorGreen))
}

// ─── Helpers ──────────────────────────────────────────────────

// Snapshot exposes writeSnapshotNow for the CLI command. Public method
// belongs on *Scheduler; see scheduler.go.
// (The exported wrapper is added for completeness.)

func (cli *ChatCLI) schedulerList(filter scheduler.ListFilter) []scheduler.JobSummary {
	if cli.schedulerRemote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		list, _ := cli.schedulerRemote.List(ctx, filter)
		return list
	}
	if cli.scheduler != nil {
		return cli.scheduler.List(filter)
	}
	return nil
}

func (cli *ChatCLI) schedulerQuery(id scheduler.JobID) *scheduler.Job {
	if cli.schedulerRemote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		j, err := cli.schedulerRemote.Query(ctx, id)
		if err != nil {
			return nil
		}
		return j
	}
	if cli.scheduler != nil {
		j, err := cli.scheduler.Query(id)
		if err != nil {
			return nil
		}
		return j
	}
	return nil
}

// waitForTerminal polls the remote client until the job reaches a
// terminal state or ctx is cancelled.
func waitForTerminal(ctx context.Context, c *scheduler.RemoteClient, id scheduler.JobID) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println(colorize("  ⚠ "+i18n.T("scheduler.wait_ctx_cancelled"), ColorYellow))
			return
		case <-t.C:
			j, err := c.Query(ctx, id)
			if err != nil {
				continue
			}
			if j.Status.IsTerminal() {
				fmt.Println(colorize(fmt.Sprintf("  ✔ job %s → %s", id, j.Status), ColorGreen))
				return
			}
		}
	}
}

// ─── Argument parsers ─────────────────────────────────────────

// parseScheduleArgs reads /schedule CLI flags into a ToolInput. The
// first positional argument is the job name.
func parseScheduleArgs(args []string) (scheduler.ToolInput, error) {
	in := scheduler.ToolInput{Tags: map[string]string{}}
	if len(args) == 0 {
		return in, fmt.Errorf("schedule: missing job name")
	}
	// If the first token is not a flag, take it as the name.
	if !strings.HasPrefix(args[0], "--") {
		in.Name = args[0]
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--when":
			in.When, i = takeString(args, i)
		case "--cron":
			val, ni := takeString(args, i)
			in.When = "cron:" + val
			i = ni
		case "--every":
			val, ni := takeString(args, i)
			in.When = "every " + val
			i = ni
		case "--do":
			in.Do, i = takeString(args, i)
		case "--wait", "--until":
			in.Until, i = takeString(args, i)
		case "--timeout":
			in.Timeout, i = takeString(args, i)
		case "--wait-timeout":
			in.WaitTimeout, i = takeString(args, i)
		case "--poll":
			in.PollInterval, i = takeString(args, i)
		case "--max-polls":
			val, ni := takeString(args, i)
			if n, err := strconv.Atoi(val); err == nil {
				in.MaxPolls = n
			}
			i = ni
		case "--max-retries":
			val, ni := takeString(args, i)
			if n, err := strconv.Atoi(val); err == nil {
				in.MaxRetries = n
			}
			i = ni
		case "--depends-on":
			val, ni := takeString(args, i)
			in.DependsOn = append(in.DependsOn, val)
			i = ni
		case "--triggers":
			val, ni := takeString(args, i)
			in.Triggers = append(in.Triggers, val)
			i = ni
		case "--ttl":
			in.TTL, i = takeString(args, i)
		case "--description":
			in.Description, i = takeString(args, i)
		case "--tag":
			val, ni := takeString(args, i)
			parts := strings.SplitN(val, "=", 2)
			if len(parts) == 2 {
				in.Tags[parts[0]] = parts[1]
			} else {
				in.Tags[parts[0]] = ""
			}
			i = ni
		case "--on-timeout":
			in.OnTimeout, i = takeString(args, i)
		case "--i-know":
			// Used by the scheduler to mark jobs as dangerous-confirmed.
			// Currently this flag tracks a Boolean tag.
			in.Tags["dangerous_confirmed"] = "true"
		case "--async":
			in.Async = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return in, fmt.Errorf("schedule: unknown flag %s", args[i])
			}
		}
	}
	if in.When == "" {
		return in, fmt.Errorf("schedule: --when is required (got %q)", strings.Join(args, " "))
	}
	if in.Do == "" {
		return in, fmt.Errorf("schedule: --do is required")
	}
	return in, nil
}

func parseWaitArgs(args []string) (scheduler.ToolInput, error) {
	in := scheduler.ToolInput{Tags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--until":
			in.Until, i = takeString(args, i)
		case "--then":
			in.Do, i = takeString(args, i)
		case "--every":
			in.PollInterval, i = takeString(args, i)
		case "--timeout":
			in.WaitTimeout, i = takeString(args, i)
		case "--max-polls":
			val, ni := takeString(args, i)
			if n, err := strconv.Atoi(val); err == nil {
				in.MaxPolls = n
			}
			i = ni
		case "--async":
			in.Async = true
		case "--on-timeout":
			in.OnTimeout, i = takeString(args, i)
		case "--name":
			in.Name, i = takeString(args, i)
		}
	}
	if in.Until == "" {
		return in, fmt.Errorf("wait: --until is required")
	}
	if in.When == "" {
		in.When = "when-ready"
	}
	if in.Do == "" {
		in.Do = "noop"
	}
	if in.Name == "" {
		host, _ := os.Hostname()
		in.Name = fmt.Sprintf("wait-%s-%d", host, time.Now().UnixNano()%100000)
	}
	return in, nil
}

func takeString(args []string, i int) (string, int) {
	if i+1 >= len(args) {
		return "", i
	}
	return args[i+1], i + 1
}

func indent(pref, s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for k := range lines {
		lines[k] = pref + lines[k]
	}
	return strings.Join(lines, "\n")
}

// ─── Usage banners ────────────────────────────────────────────

func (cli *ChatCLI) printScheduleUsage() {
	fmt.Println(colorize(i18n.T("scheduler.schedule.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  " + i18n.T("scheduler.schedule.usage_example_1"))
	fmt.Println("  " + i18n.T("scheduler.schedule.usage_example_2"))
	fmt.Println("  " + i18n.T("scheduler.schedule.usage_example_3"))
	fmt.Println("  " + i18n.T("scheduler.schedule.usage_flags"))
}

func (cli *ChatCLI) printWaitUsage() {
	fmt.Println(colorize(i18n.T("scheduler.wait.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  " + i18n.T("scheduler.wait.usage_example_1"))
	fmt.Println("  " + i18n.T("scheduler.wait.usage_example_2"))
	fmt.Println("  " + i18n.T("scheduler.wait.usage_flags"))
}

func (cli *ChatCLI) printJobsUsage() {
	fmt.Println(colorize(i18n.T("scheduler.jobs.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  /jobs list [--all | --status X | --owner Y | --tag k=v]")
	fmt.Println("  /jobs show <id>")
	fmt.Println("  /jobs tree")
	fmt.Println("  /jobs cancel <id> [reason...]")
	fmt.Println("  /jobs pause <id>")
	fmt.Println("  /jobs resume <id>")
	fmt.Println("  /jobs logs <id>")
	fmt.Println("  /jobs history")
	fmt.Println("  /jobs daemon {status}")
	fmt.Println("  /jobs gc")
}

// mustJSON / jsonDecode are tiny wrappers around encoding/json.
func mustJSON(v any) string {
	b, err := marshalJSON(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// marshalJSON is the real marshaller; kept distinct so mustJSON above
// can be unit-tested without importing encoding/json directly here.
var marshalJSON = func(v any) ([]byte, error) {
	return marshalJSONImpl(v)
}

// jsonDecode mirrors marshalJSON for symmetry.
func jsonDecode(raw string, dst any) error {
	return jsonDecodeImpl(raw, dst)
}
