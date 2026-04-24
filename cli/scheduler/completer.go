/*
 * ChatCLI - Scheduler: autocomplete suggestions.
 *
 * The top-level cli/cli_completer.go calls Suggest(...) to fetch
 * /schedule, /wait and /jobs suggestions. Returning plain strings
 * rather than go-prompt.Suggest structs keeps this package free of a
 * TUI library dependency.
 */
package scheduler

// Suggestion is a (text, description) pair consumed by the top-level
// completer. Kept minimal on purpose so the completer can adapt to
// future terminal libraries.
type Suggestion struct {
	Text        string
	Description string
}

// CommandSuggestions returns the top-level /schedule /wait /jobs
// subcommands.
func CommandSuggestions(command string) []Suggestion {
	switch command {
	case "/schedule", "schedule":
		return []Suggestion{
			{Text: "--when", Description: "Schedule time: +5m / cron:0 2 * * * / every 30s"},
			{Text: "--do", Description: "Action: /foo args / shell: cmd / agent: task"},
			{Text: "--wait", Description: "Optional wait condition before firing"},
			{Text: "--timeout", Description: "Override default action timeout"},
			{Text: "--poll", Description: "Override default wait poll interval"},
			{Text: "--depends-on", Description: "Block until job <id> completes"},
			{Text: "--triggers", Description: "Spawn job <id> on completion"},
			{Text: "--tag", Description: "Add a key=value tag"},
			{Text: "--ttl", Description: "How long to keep terminal record"},
			{Text: "--description", Description: "Free-form description"},
			{Text: "--i-know", Description: "Confirm a dangerous shell command"},
		}
	case "/wait", "wait":
		return []Suggestion{
			{Text: "--until", Description: "Condition: http://url==200 / k8s:pod/ns/name:ready"},
			{Text: "--then", Description: "Action to run on satisfaction (optional)"},
			{Text: "--every", Description: "Poll interval (default 5s)"},
			{Text: "--timeout", Description: "Max wait duration (default 30m)"},
			{Text: "--max-polls", Description: "Max poll count"},
			{Text: "--async", Description: "Do not block; return job id"},
			{Text: "--on-timeout", Description: "fail | fire_anyway | fallback"},
		}
	case "/jobs", "jobs":
		return []Suggestion{
			{Text: "list", Description: "Show active jobs"},
			{Text: "show", Description: "Show detail of a single job"},
			{Text: "tree", Description: "ASCII DAG view"},
			{Text: "cancel", Description: "Cancel a running job"},
			{Text: "pause", Description: "Pause a pending job"},
			{Text: "resume", Description: "Resume a paused job"},
			{Text: "logs", Description: "Tail a job's execution history"},
			{Text: "history", Description: "Show terminal jobs"},
			{Text: "daemon", Description: "Daemon control: start|stop|status"},
			{Text: "gc", Description: "Trigger on-demand garbage collection"},
		}
	}
	return nil
}

// JobsListFlags returns the flag suggestions for /jobs list.
func JobsListFlags() []Suggestion {
	return []Suggestion{
		{Text: "--status", Description: "Filter by status (pending|waiting|running|failed|…)"},
		{Text: "--owner", Description: "Filter by owner (me|agent|worker|system|hook)"},
		{Text: "--tag", Description: "Filter by tag"},
		{Text: "--name", Description: "Filter by name substring"},
		{Text: "--all", Description: "Include terminal jobs"},
	}
}
