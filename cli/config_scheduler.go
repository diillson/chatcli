/*
 * ChatCLI - /config scheduler section.
 *
 * Lives in its own file (mirrors config_quality.go) and is dispatched
 * by config_sections.go's routeConfigCommand. Shows operator-tunable
 * env vars side-by-side with live runtime state (queue depth, WAL
 * segments, daemon status).
 */
package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
)

// showConfigScheduler renders the /config scheduler section.
func (cli *ChatCLI) showConfigScheduler() {
	sectionHeader("⏲", "cfg.section.scheduler.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	subheader(p, "cfg.sub.sched.core")
	kv(p, i18n.T("cfg.kv.scheduler_enabled"), envBool("CHATCLI_SCHEDULER_ENABLED"))
	kv(p, i18n.T("cfg.kv.scheduler_data_dir"), envOr("CHATCLI_SCHEDULER_DATA_DIR"))
	kv(p, "CHATCLI_SCHEDULER_MAX_JOBS", envOr("CHATCLI_SCHEDULER_MAX_JOBS"))
	kv(p, "CHATCLI_SCHEDULER_WORKER_COUNT", envOr("CHATCLI_SCHEDULER_WORKER_COUNT"))
	kv(p, "CHATCLI_SCHEDULER_ALLOW_AGENTS", envBool("CHATCLI_SCHEDULER_ALLOW_AGENTS"))
	kv(p, "CHATCLI_SCHEDULER_ACTION_ALLOWLIST", envOr("CHATCLI_SCHEDULER_ACTION_ALLOWLIST"))

	fmt.Println(p)
	subheader(p, "cfg.sub.sched.budget")
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_ACTION_TIMEOUT", envOr("CHATCLI_SCHEDULER_DEFAULT_ACTION_TIMEOUT"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_POLL_INTERVAL", envOr("CHATCLI_SCHEDULER_DEFAULT_POLL_INTERVAL"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_WAIT_TIMEOUT", envOr("CHATCLI_SCHEDULER_DEFAULT_WAIT_TIMEOUT"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_MAX_POLLS", envOr("CHATCLI_SCHEDULER_DEFAULT_MAX_POLLS"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_BACKOFF_INITIAL", envOr("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_INITIAL"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MAX", envOr("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MAX"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MULT", envOr("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MULT"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_BACKOFF_JITTER", envOr("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_JITTER"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_MAX_RETRIES", envOr("CHATCLI_SCHEDULER_DEFAULT_MAX_RETRIES"))
	kv(p, "CHATCLI_SCHEDULER_DEFAULT_TTL", envOr("CHATCLI_SCHEDULER_DEFAULT_TTL"))
	kv(p, "CHATCLI_SCHEDULER_HISTORY_LIMIT", envOr("CHATCLI_SCHEDULER_HISTORY_LIMIT"))

	fmt.Println(p)
	subheader(p, "cfg.sub.sched.safety")
	kv(p, "CHATCLI_SCHEDULER_RATE_LIMIT_GLOBAL_RPS", envOr("CHATCLI_SCHEDULER_RATE_LIMIT_GLOBAL_RPS"))
	kv(p, "CHATCLI_SCHEDULER_RATE_LIMIT_OWNER_RPS", envOr("CHATCLI_SCHEDULER_RATE_LIMIT_OWNER_RPS"))
	kv(p, "CHATCLI_SCHEDULER_BREAKER_FAILURE_THRESHOLD", envOr("CHATCLI_SCHEDULER_BREAKER_FAILURE_THRESHOLD"))
	kv(p, "CHATCLI_SCHEDULER_BREAKER_WINDOW", envOr("CHATCLI_SCHEDULER_BREAKER_WINDOW"))
	kv(p, "CHATCLI_SCHEDULER_BREAKER_COOLDOWN", envOr("CHATCLI_SCHEDULER_BREAKER_COOLDOWN"))
	kv(p, "CHATCLI_SCHEDULER_AUDIT_ENABLED", envBool("CHATCLI_SCHEDULER_AUDIT_ENABLED"))
	kv(p, "CHATCLI_SCHEDULER_AUDIT_MAX_SIZE_MB", envOr("CHATCLI_SCHEDULER_AUDIT_MAX_SIZE_MB"))
	kv(p, "CHATCLI_SCHEDULER_SHELL_ALLOW_BYPASS", envBool("CHATCLI_SCHEDULER_SHELL_ALLOW_BYPASS"))

	fmt.Println(p)
	subheader(p, "cfg.sub.sched.daemon")
	kv(p, "CHATCLI_SCHEDULER_DAEMON_SOCKET", envOr("CHATCLI_SCHEDULER_DAEMON_SOCKET"))
	kv(p, "CHATCLI_SCHEDULER_DAEMON_AUTO_CONNECT", envBool("CHATCLI_SCHEDULER_DAEMON_AUTO_CONNECT"))
	kv(p, "CHATCLI_SCHEDULER_SNAPSHOT_INTERVAL", envOr("CHATCLI_SCHEDULER_SNAPSHOT_INTERVAL"))
	kv(p, "CHATCLI_SCHEDULER_WAL_GC_INTERVAL", envOr("CHATCLI_SCHEDULER_WAL_GC_INTERVAL"))

	// Live state.
	fmt.Println(p)
	kv(p, i18n.T("cfg.kv.scheduler_active"), fmt.Sprintf("%d", cli.schedulerActiveCount()))
	kv(p, i18n.T("cfg.kv.scheduler_queue_depth"), fmt.Sprintf("%d", cli.schedulerQueueDepth()))
	kv(p, i18n.T("cfg.kv.scheduler_wal_segments"), fmt.Sprintf("%d", cli.schedulerWALCount()))
	kv(p, i18n.T("cfg.kv.scheduler_daemon"), cli.schedulerDaemonStatus())

	sectionEnd(ColorBlue)
}

// schedulerActiveCount returns the current active-jobs count across
// both in-process and remote scheduler.
func (cli *ChatCLI) schedulerActiveCount() int {
	if cli.schedulerRemote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stats, err := cli.schedulerRemote.Stats(ctx); err == nil {
			return stats.JobsActive
		}
		return 0
	}
	if cli.scheduler == nil {
		return 0
	}
	list := cli.scheduler.List(scheduler.ListFilter{})
	return len(list)
}

func (cli *ChatCLI) schedulerQueueDepth() int {
	if cli.schedulerRemote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stats, err := cli.schedulerRemote.Stats(ctx); err == nil {
			return stats.QueueDepth
		}
		return 0
	}
	// In-process scheduler doesn't expose the heap len directly; report
	// the active count as a stand-in.
	return cli.schedulerActiveCount()
}

func (cli *ChatCLI) schedulerWALCount() int {
	if cli.schedulerRemote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stats, err := cli.schedulerRemote.Stats(ctx); err == nil {
			return stats.WALSegments
		}
	}
	return 0
}

func (cli *ChatCLI) schedulerDaemonStatus() string {
	if cli.schedulerRemote != nil {
		return "connected @ " + cli.schedulerSocket
	}
	if cli.scheduler != nil {
		return "in-process"
	}
	return "disabled"
}
