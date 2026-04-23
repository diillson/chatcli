/*
 * ChatCLI - scheduler_init.go
 *
 * Scheduler lifecycle integration into *ChatCLI.
 *
 * Initialization order (called from NewChatCLI, after hooks + bus are ready):
 *
 *   1. Build scheduler.Config from env.
 *   2. If daemon auto-connect enabled and a daemon is reachable, wrap
 *      it in a thin proxy and skip in-process init.
 *   3. Otherwise build the in-process scheduler, register builtins,
 *      start it.
 *
 * Cleanup: DrainAndShutdown is invoked from ChatCLI.cleanup.
 */
package cli

import (
	"context"
	"os"
	"sync/atomic"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/cli/scheduler/builtins"
	"go.uber.org/zap"
)

// pluginsSetAdapter is a thin indirection so tests can stub the
// registration without poking the plugins package.
var pluginsSetAdapter = func(a plugins.SchedulerAdapter) {
	plugins.SetSchedulerAdapter(a)
}

// initScheduler wires the scheduler into ChatCLI. Errors are logged,
// not fatal — a scheduler outage must not break the chat loop.
func (cli *ChatCLI) initScheduler() {
	if cli.logger == nil {
		return
	}
	cfg := scheduler.LoadConfigFromEnv()
	if !cfg.Enabled {
		cli.logger.Info("scheduler: disabled via CHATCLI_SCHEDULER_ENABLED=false")
		return
	}

	// Daemon auto-detect.
	socket := scheduler.DefaultSocketPath(cfg)
	if cfg.DaemonAutoConnect {
		if err := scheduler.CheckDaemon(socket); err == nil {
			client, derr := scheduler.Dial(socket)
			if derr == nil {
				cli.schedulerRemote = client
				cli.schedulerSocket = socket
				// Even in daemon mode, wire the plugin adapter so the
				// ReAct loop can still invoke @scheduler.
				pluginsSetAdapter(&schedulerPluginAdapter{cli: cli})
				cli.logger.Info("scheduler: connected to daemon",
					zap.String("socket", socket))
				return
			}
			cli.logger.Warn("scheduler: daemon reachable but dial failed",
				zap.String("socket", socket), zap.Error(derr))
		}
	}

	bridge := newSchedulerBridge(cli)
	deps := scheduler.SchedulerDeps{
		Hooks: cli.hookManager,
	}
	s, err := scheduler.New(cfg, bridge, deps, cli.logger)
	if err != nil {
		cli.logger.Warn("scheduler: New failed — feature disabled for session", zap.Error(err))
		return
	}
	builtins.RegisterAll(s)

	ctx, cancel := context.WithCancel(context.Background())
	cli.schedulerCancel = cancel
	if err := s.Start(ctx); err != nil {
		cli.logger.Warn("scheduler: Start failed — feature disabled for session", zap.Error(err))
		cancel()
		return
	}
	cli.scheduler = s
	// Wire the @scheduler builtin plugin into the ReAct tool registry.
	pluginsAdapter := &schedulerPluginAdapter{cli: cli}
	pluginsSetAdapter(pluginsAdapter)

	cli.logger.Info("scheduler: running in-process",
		zap.String("data_dir", cfg.DataDir),
		zap.Int("workers", cfg.WorkerCount))
}

// shutdownScheduler tears down whichever scheduler is wired. Called
// from ChatCLI.cleanup.
func (cli *ChatCLI) shutdownScheduler() {
	if cli.schedulerRemote != nil {
		_ = cli.schedulerRemote.Close()
		cli.schedulerRemote = nil
	}
	if cli.scheduler != nil {
		cli.scheduler.DrainAndShutdown(15 * 1_000_000_000) // 15s
		cli.scheduler = nil
	}
	if cli.schedulerCancel != nil {
		cli.schedulerCancel()
		cli.schedulerCancel = nil
	}
}

// currentSchedulerOwner returns the Owner for user-initiated commands
// in this session. Agent-initiated jobs use a different Owner in
// scheduler_command.go.
func (cli *ChatCLI) currentSchedulerOwner() scheduler.Owner {
	id := cli.currentSessionName
	if id == "" {
		if host, err := os.Hostname(); err == nil {
			id = host
		} else {
			id = "interactive"
		}
	}
	return scheduler.Owner{Kind: scheduler.OwnerUser, ID: id}
}

// schedulerEnabled reports whether the scheduler can accept commands.
func (cli *ChatCLI) schedulerEnabled() bool {
	return cli.scheduler != nil || cli.schedulerRemote != nil
}

// markSchedulerDirty bumps an atomic so the prompt prefix refresh
// knows to re-render. Called by scheduler events via the bridge.
func (cli *ChatCLI) markSchedulerDirty() {
	atomic.StoreInt32(&cli.schedulerDirty, 1)
}

// consumeSchedulerDirty returns and clears the dirty flag atomically.
func (cli *ChatCLI) consumeSchedulerDirty() bool {
	return atomic.SwapInt32(&cli.schedulerDirty, 0) == 1
}
