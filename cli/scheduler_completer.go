/*
 * ChatCLI - scheduler_completer.go
 *
 * Context-aware autocomplete for /schedule, /wait and /jobs. Follows
 * the same pattern as getContextSuggestions / getSessionSuggestions:
 *
 *   - Subcommand suggestions when the user is at position 1.
 *   - Flag suggestions after the subcommand, with short descriptions.
 *   - Value suggestions right after a flag that has a fixed-vocabulary
 *     (like --status, --on-timeout, --owner), plus live lookups of
 *     active job IDs for --depends-on / --triggers / show / cancel /
 *     pause / resume / logs.
 *
 * All descriptions go through i18n so a future translation pass is
 * trivial. Lookups hit the local scheduler, or the remote daemon via
 * schedulerList when running in thin-client mode.
 */
package cli

import (
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
)

// ─── Shared value vocabularies ─────────────────────────────────

// scheduleStatusValues are the JobStatus strings the user may filter
// on via --status. Terminal statuses appear after the active ones so
// the dropdown ordering matches mental model.
var scheduleStatusValues = []prompt.Suggest{
	{Text: "pending", Description: i18n.T("sched.status.pending")},
	{Text: "blocked", Description: i18n.T("sched.status.blocked")},
	{Text: "waiting", Description: i18n.T("sched.status.waiting")},
	{Text: "running", Description: i18n.T("sched.status.running")},
	{Text: "paused", Description: i18n.T("sched.status.paused")},
	{Text: "completed", Description: i18n.T("sched.status.completed")},
	{Text: "failed", Description: i18n.T("sched.status.failed")},
	{Text: "cancelled", Description: i18n.T("sched.status.cancelled")},
	{Text: "timed_out", Description: i18n.T("sched.status.timed_out")},
	{Text: "skipped", Description: i18n.T("sched.status.skipped")},
}

var scheduleOwnerValues = []prompt.Suggest{
	{Text: "me", Description: i18n.T("sched.owner.me")},
	{Text: "user", Description: i18n.T("sched.owner.user")},
	{Text: "agent", Description: i18n.T("sched.owner.agent")},
	{Text: "worker", Description: i18n.T("sched.owner.worker")},
	{Text: "system", Description: i18n.T("sched.owner.system")},
	{Text: "hook", Description: i18n.T("sched.owner.hook")},
}

var scheduleOnTimeoutValues = []prompt.Suggest{
	{Text: "fail", Description: i18n.T("sched.ontimeout.fail")},
	{Text: "fire_anyway", Description: i18n.T("sched.ontimeout.fire_anyway")},
	{Text: "fallback", Description: i18n.T("sched.ontimeout.fallback")},
}

// scheduleWhenHints are DSL templates the user can pick as a starting
// point for --when. Values are copy-pasteable literals.
var scheduleWhenHints = []prompt.Suggest{
	{Text: "+5m", Description: i18n.T("sched.when.relative_5m")},
	{Text: "+30s", Description: i18n.T("sched.when.relative_30s")},
	{Text: "in 2h", Description: i18n.T("sched.when.relative_in_2h")},
	{Text: "every 30s", Description: i18n.T("sched.when.every_30s")},
	{Text: "every 5m", Description: i18n.T("sched.when.every_5m")},
	{Text: "every 1h", Description: i18n.T("sched.when.every_1h")},
	{Text: "@hourly", Description: i18n.T("sched.when.at_hourly")},
	{Text: "@daily", Description: i18n.T("sched.when.at_daily")},
	{Text: "@weekly", Description: i18n.T("sched.when.at_weekly")},
	{Text: "cron:0 2 * * *", Description: i18n.T("sched.when.cron_0_2")},
	{Text: "when-ready", Description: i18n.T("sched.when.when_ready")},
	{Text: "manual", Description: i18n.T("sched.when.manual")},
}

var scheduleUntilHints = []prompt.Suggest{
	{Text: "http://localhost:8080/health==200", Description: i18n.T("sched.until.http_200")},
	{Text: "tcp://localhost:5432", Description: i18n.T("sched.until.tcp_port")},
	{Text: "k8s:pod/prod/api:Ready", Description: i18n.T("sched.until.k8s_pod")},
	{Text: "k8s:deployment/prod/api:Available", Description: i18n.T("sched.until.k8s_deploy")},
	{Text: "docker:container-name:healthy", Description: i18n.T("sched.until.docker_healthy")},
	{Text: "file:/tmp/done", Description: i18n.T("sched.until.file_exists")},
	{Text: "shell: test -f /tmp/done", Description: i18n.T("sched.until.shell_exit")},
	{Text: "llm: is the deploy healthy?", Description: i18n.T("sched.until.llm_check")},
}

var scheduleDoHints = []prompt.Suggest{
	{Text: "/run ", Description: i18n.T("sched.do.slash_run")},
	{Text: "shell: ", Description: i18n.T("sched.do.shell")},
	{Text: "agent: ", Description: i18n.T("sched.do.agent")},
	{Text: "llm: ", Description: i18n.T("sched.do.llm")},
	{Text: "noop", Description: i18n.T("sched.do.noop")},
	{Text: "POST https://", Description: i18n.T("sched.do.webhook")},
	{Text: "hook:PostToolUse", Description: i18n.T("sched.do.hook")},
}

// scheduleDurationHints offers reasonable defaults for duration-valued
// flags (--timeout, --poll, --every, --ttl, --wait-timeout).
var scheduleDurationHints = []prompt.Suggest{
	{Text: "5s", Description: ""},
	{Text: "30s", Description: ""},
	{Text: "1m", Description: ""},
	{Text: "5m", Description: ""},
	{Text: "10m", Description: ""},
	{Text: "30m", Description: ""},
	{Text: "1h", Description: ""},
	{Text: "6h", Description: ""},
	{Text: "24h", Description: ""},
}

// ─── /schedule ────────────────────────────────────────────────

// getScheduleSuggestions completes /schedule subcommands and flags.
func (cli *ChatCLI) getScheduleSuggestions(d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), "/schedule")
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	// First positional (job name) — no suggestions unless the user is
	// clearly starting a flag. "help" is a valid subcommand so we list
	// it.
	if len(args) == 0 {
		return []prompt.Suggest{
			{Text: "help", Description: i18n.T("sched.complete.help")},
		}
	}

	prevFlag := lastFlag(args, trailingSpace)

	// Flag value completion.
	switch prevFlag {
	case "--when", "--cron", "--every":
		if prevFlag == "--when" {
			return prompt.FilterHasPrefix(scheduleWhenHints, current, true)
		}
		if prevFlag == "--every" {
			return prompt.FilterHasPrefix(scheduleDurationHints, current, true)
		}
	case "--do":
		return prompt.FilterHasPrefix(scheduleDoHints, current, true)
	case "--wait", "--until":
		return prompt.FilterHasPrefix(scheduleUntilHints, current, true)
	case "--timeout", "--wait-timeout", "--poll", "--ttl":
		return prompt.FilterHasPrefix(scheduleDurationHints, current, true)
	case "--on-timeout":
		return prompt.FilterHasPrefix(scheduleOnTimeoutValues, current, true)
	case "--depends-on", "--triggers":
		return prompt.FilterHasPrefix(cli.schedulerJobIDSuggestions(true), current, true)
	case "--tag":
		// No fixed vocabulary — let the user type freely.
		return nil
	case "--max-polls", "--max-retries", "--description":
		return nil
	}

	// Flag listing.
	if strings.HasPrefix(current, "-") || (trailingSpace && len(args) >= 1) {
		return prompt.FilterHasPrefix(scheduleFlagSuggestions(), current, true)
	}
	return nil
}

func scheduleFlagSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "--when", Description: i18n.T("sched.flag.when")},
		{Text: "--cron", Description: i18n.T("sched.flag.cron")},
		{Text: "--every", Description: i18n.T("sched.flag.every")},
		{Text: "--do", Description: i18n.T("sched.flag.do")},
		{Text: "--wait", Description: i18n.T("sched.flag.wait")},
		{Text: "--timeout", Description: i18n.T("sched.flag.timeout")},
		{Text: "--wait-timeout", Description: i18n.T("sched.flag.wait_timeout")},
		{Text: "--poll", Description: i18n.T("sched.flag.poll")},
		{Text: "--max-polls", Description: i18n.T("sched.flag.max_polls")},
		{Text: "--max-retries", Description: i18n.T("sched.flag.max_retries")},
		{Text: "--depends-on", Description: i18n.T("sched.flag.depends_on")},
		{Text: "--triggers", Description: i18n.T("sched.flag.triggers")},
		{Text: "--ttl", Description: i18n.T("sched.flag.ttl")},
		{Text: "--description", Description: i18n.T("sched.flag.description")},
		{Text: "--tag", Description: i18n.T("sched.flag.tag")},
		{Text: "--on-timeout", Description: i18n.T("sched.flag.on_timeout")},
		{Text: "--async", Description: i18n.T("sched.flag.async")},
		{Text: "--i-know", Description: i18n.T("sched.flag.i_know")},
	}
}

// ─── /wait ────────────────────────────────────────────────────

func (cli *ChatCLI) getWaitSuggestions(d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), "/wait")
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	if len(args) == 0 || (len(args) == 1 && !trailingSpace) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "--until", Description: i18n.T("sched.flag.until")},
			{Text: "help", Description: i18n.T("sched.complete.help")},
		}, current, true)
	}

	prevFlag := lastFlag(args, trailingSpace)
	switch prevFlag {
	case "--until":
		return prompt.FilterHasPrefix(scheduleUntilHints, current, true)
	case "--then":
		return prompt.FilterHasPrefix(scheduleDoHints, current, true)
	case "--every", "--timeout":
		return prompt.FilterHasPrefix(scheduleDurationHints, current, true)
	case "--on-timeout":
		return prompt.FilterHasPrefix(scheduleOnTimeoutValues, current, true)
	case "--max-polls", "--name":
		return nil
	}

	if strings.HasPrefix(current, "-") || trailingSpace {
		return prompt.FilterHasPrefix(waitFlagSuggestions(), current, true)
	}
	return nil
}

func waitFlagSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "--until", Description: i18n.T("sched.flag.until")},
		{Text: "--then", Description: i18n.T("sched.flag.then")},
		{Text: "--every", Description: i18n.T("sched.flag.every")},
		{Text: "--timeout", Description: i18n.T("sched.flag.timeout")},
		{Text: "--max-polls", Description: i18n.T("sched.flag.max_polls")},
		{Text: "--on-timeout", Description: i18n.T("sched.flag.on_timeout")},
		{Text: "--async", Description: i18n.T("sched.flag.async")},
		{Text: "--name", Description: i18n.T("sched.flag.name")},
	}
}

// ─── /jobs ────────────────────────────────────────────────────

// Subcommands that take a single <id> positional argument. We offer
// active job IDs for the first few; history/tree/list take flags.
var jobsIDSubcommands = map[string]bool{
	"show":   true,
	"cancel": true,
	"pause":  true,
	"resume": true,
	"logs":   true,
}

// jobsSubcommandSuggestions lists the top-level subcommand table.
var jobsSubcommandSuggestions = []prompt.Suggest{
	{Text: "list", Description: i18n.T("sched.jobs.sub.list")},
	{Text: "show", Description: i18n.T("sched.jobs.sub.show")},
	{Text: "tree", Description: i18n.T("sched.jobs.sub.tree")},
	{Text: "cancel", Description: i18n.T("sched.jobs.sub.cancel")},
	{Text: "pause", Description: i18n.T("sched.jobs.sub.pause")},
	{Text: "resume", Description: i18n.T("sched.jobs.sub.resume")},
	{Text: "logs", Description: i18n.T("sched.jobs.sub.logs")},
	{Text: "history", Description: i18n.T("sched.jobs.sub.history")},
	{Text: "daemon", Description: i18n.T("sched.jobs.sub.daemon")},
	{Text: "gc", Description: i18n.T("sched.jobs.sub.gc")},
	{Text: "help", Description: i18n.T("sched.complete.help")},
}

var jobsListFlagSuggestions = []prompt.Suggest{
	{Text: "--all", Description: i18n.T("sched.jobs.flag.all")},
	{Text: "--status", Description: i18n.T("sched.jobs.flag.status")},
	{Text: "--owner", Description: i18n.T("sched.jobs.flag.owner")},
	{Text: "--tag", Description: i18n.T("sched.jobs.flag.tag")},
	{Text: "--name", Description: i18n.T("sched.jobs.flag.name")},
}

var jobsDaemonSubcommands = []prompt.Suggest{
	{Text: "status", Description: i18n.T("sched.jobs.daemon.status")},
	{Text: "start", Description: i18n.T("sched.jobs.daemon.start")},
	{Text: "stop", Description: i18n.T("sched.jobs.daemon.stop")},
	{Text: "restart", Description: i18n.T("sched.jobs.daemon.restart")},
}

func (cli *ChatCLI) getJobsSuggestions(d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), "/jobs")
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	// "/jobs" alone — suggest subcommand.
	if len(args) == 0 || (len(args) == 1 && !trailingSpace) {
		return prompt.FilterHasPrefix(jobsSubcommandSuggestions, current, true)
	}

	sub := args[0]

	// `/jobs <sub> ` — positional completion or flag listing.
	if jobsIDSubcommands[sub] {
		// `show <id>` etc. — complete with live IDs.
		if len(args) == 1 || (len(args) == 2 && !trailingSpace) {
			// Active jobs for show/cancel/pause/resume/logs — include
			// terminal entries for logs/show since users often inspect
			// completed jobs.
			includeTerminal := sub == "logs" || sub == "show"
			return prompt.FilterHasPrefix(cli.schedulerJobIDSuggestions(!includeTerminal), current, true)
		}
		return nil
	}

	if sub == "list" || sub == "history" {
		prevFlag := lastFlag(args[1:], trailingSpace)
		switch prevFlag {
		case "--status":
			return prompt.FilterHasPrefix(scheduleStatusValues, current, true)
		case "--owner":
			return prompt.FilterHasPrefix(scheduleOwnerValues, current, true)
		case "--tag", "--name":
			return nil
		}
		if strings.HasPrefix(current, "-") || trailingSpace {
			return prompt.FilterHasPrefix(jobsListFlagSuggestions, current, true)
		}
		return nil
	}

	if sub == "tree" {
		return nil
	}

	if sub == "daemon" {
		if len(args) == 1 || (len(args) == 2 && !trailingSpace) {
			return prompt.FilterHasPrefix(jobsDaemonSubcommands, current, true)
		}
		return nil
	}

	return nil
}

// ─── Helpers ──────────────────────────────────────────────────

// schedulerJobIDSuggestions pulls live IDs from the scheduler so the
// user can tab-complete `/jobs cancel <id>`. activeOnly filters to
// non-terminal jobs; pass false to include history.
func (cli *ChatCLI) schedulerJobIDSuggestions(activeOnly bool) []prompt.Suggest {
	if !cli.schedulerEnabled() {
		return nil
	}
	filter := scheduler.ListFilter{IncludeTerminal: !activeOnly}
	list := cli.schedulerList(filter)
	out := make([]prompt.Suggest, 0, len(list))
	for _, s := range list {
		desc := s.Name + "  [" + string(s.Status) + "]"
		if s.Description != "" {
			desc = s.Description + "  [" + string(s.Status) + "]"
		}
		out = append(out, prompt.Suggest{Text: string(s.ID), Description: desc})
	}
	return out
}

// lastFlag returns the last "--foo" token that the current word could
// be a value for. Respects trailingSpace: "/x --foo bar" with space
// after "bar" means prevFlag="--foo" only if "bar" is the value being
// typed — otherwise "bar" is consumed and prevFlag is empty.
func lastFlag(args []string, trailingSpace bool) string {
	// When the caret has no trailing space, the last token is what
	// the user is typing now — look one back.
	end := len(args)
	if !trailingSpace && end > 0 {
		end--
	}
	if end <= 0 {
		return ""
	}
	last := args[end-1]
	if strings.HasPrefix(last, "--") || strings.HasPrefix(last, "-") {
		return last
	}
	// Not a flag; the user already moved past a flag's value.
	return ""
}
