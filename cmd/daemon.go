/*
 * ChatCLI - cmd/daemon.go
 *
 * "chatcli daemon {start|stop|status|ping}" subcommand. Manages the
 * scheduler as a background process bound to a UNIX socket. Daemons
 * are independent of any interactive session — a CLI started later
 * detects the daemon via the socket and becomes a thin client.
 */
package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/cli/scheduler/builtins"
	"go.uber.org/zap"
)

// RunDaemon dispatches the "daemon" subcommand. Returns a terminal
// error suitable for os.Exit mapping upstream.
func RunDaemon(ctx context.Context, args []string, logger *zap.Logger) error {
	if len(args) == 0 {
		return daemonUsageError()
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "start":
		return daemonStart(ctx, rest, logger)
	case "stop":
		return daemonStop(rest, logger)
	case "status":
		return daemonStatus(rest)
	case "ping":
		return daemonPing(rest)
	case "install":
		return daemonInstall(rest, logger)
	case "help", "-h", "--help":
		return daemonUsageError()
	}
	return fmt.Errorf("daemon: unknown subcommand %q\n%s", sub, daemonUsageText())
}

// ─── start ─────────────────────────────────────────────────────

func daemonStart(ctx context.Context, args []string, logger *zap.Logger) error {
	fs := flag.NewFlagSet("daemon start", flag.ContinueOnError)
	detach := fs.Bool("detach", false, "fork and detach from terminal")
	socket := fs.String("socket", "", "override socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := scheduler.LoadConfigFromEnv()
	cfg.Enabled = true
	if *socket != "" {
		cfg.DaemonSocket = *socket
	}
	resolvedSocket := scheduler.DefaultSocketPath(cfg)

	if *detach {
		return daemonStartDetached(resolvedSocket)
	}

	// Foreground: build Scheduler + Daemon, Run.
	s, err := scheduler.New(cfg, scheduler.NewNoopBridge(), scheduler.SchedulerDeps{}, logger)
	if err != nil {
		return fmt.Errorf("daemon: build scheduler: %w", err)
	}
	builtins.RegisterAll(s)

	d := scheduler.NewDaemon(s, resolvedSocket, logger)
	logger.Info("chatcli daemon starting", zap.String("socket", resolvedSocket))
	return d.Run(ctx)
}

// daemonStartDetached re-exec's the current binary with the foreground
// `daemon start` subcommand in a detached child process and returns.
func daemonStartDetached(socket string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("daemon: locate executable: %w", err)
	}
	logPath := filepath.Join(filepath.Dir(socket), "daemon.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o750)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- daemon-scoped
	if err != nil {
		return fmt.Errorf("daemon: open log: %w", err)
	}
	cmd := exec.Command(exe, "daemon", "start", "--socket", socket) // #nosec G204 -- exe path is self, flags are operator-provided
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("daemon: spawn detached: %w", err)
	}
	// Close parent's handle; child keeps its own.
	_ = logFile.Close()

	// Wait briefly for the socket to appear, then return.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := scheduler.CheckDaemon(socket); err == nil {
			fmt.Fprintf(os.Stdout, "chatcli daemon started (pid=%d, socket=%s, log=%s)\n",
				cmd.Process.Pid, socket, logPath)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon: started pid=%d but socket %s did not appear within 5s", cmd.Process.Pid, socket)
}

// ─── stop ──────────────────────────────────────────────────────

func daemonStop(args []string, logger *zap.Logger) error {
	fs := flag.NewFlagSet("daemon stop", flag.ContinueOnError)
	socket := fs.String("socket", "", "override socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := scheduler.LoadConfigFromEnv()
	if *socket != "" {
		cfg.DaemonSocket = *socket
	}
	s := scheduler.DefaultSocketPath(cfg)
	if err := scheduler.CheckDaemon(s); err != nil {
		return fmt.Errorf("daemon: no reachable daemon at %s (%w)", s, err)
	}
	// We send SIGTERM to the PID referenced by <socket>.pid.
	data, err := os.ReadFile(s + ".pid") // #nosec G304 -- daemon-scoped
	if err != nil {
		return fmt.Errorf("daemon: read pid file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("daemon: parse pid: %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("daemon: find process: %w", err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("daemon: send SIGTERM: %w", err)
	}
	// Wait for socket to disappear.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if scheduler.CheckDaemon(s) != nil {
			fmt.Fprintln(os.Stdout, "chatcli daemon stopped")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	logger.Warn("daemon did not exit within 15s; forcing SIGKILL")
	_ = proc.Signal(syscall.SIGKILL)
	return nil
}

// ─── status / ping / install ──────────────────────────────────

func daemonStatus(args []string) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	socket := fs.String("socket", "", "override socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := scheduler.LoadConfigFromEnv()
	if *socket != "" {
		cfg.DaemonSocket = *socket
	}
	sock := scheduler.DefaultSocketPath(cfg)
	if err := scheduler.CheckDaemon(sock); err != nil {
		fmt.Fprintln(os.Stdout, "chatcli daemon: not running")
		return nil
	}
	c, err := scheduler.Dial(sock)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stats, err := c.Stats(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "chatcli daemon\n  socket   : %s\n  uptime   : %s\n  jobs     : %d active, queue=%d\n  wal segs : %d\n  clients  : %d\n",
		sock, stats.Uptime, stats.JobsActive, stats.QueueDepth, stats.WALSegments, stats.Connections)
	return nil
}

func daemonPing(args []string) error {
	fs := flag.NewFlagSet("daemon ping", flag.ContinueOnError)
	socket := fs.String("socket", "", "override socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := scheduler.LoadConfigFromEnv()
	if *socket != "" {
		cfg.DaemonSocket = *socket
	}
	if err := scheduler.CheckDaemon(scheduler.DefaultSocketPath(cfg)); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "pong")
	return nil
}

// daemonInstall writes a systemd / launchd unit template. Best-effort
// scaffold — operator adjusts as needed.
func daemonInstall(args []string, logger *zap.Logger) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cfg := scheduler.LoadConfigFromEnv()
	sock := scheduler.DefaultSocketPath(cfg)
	template := fmt.Sprintf(`# chatcli scheduler daemon unit
# Adapt as needed for your init system.

# systemd: drop at /etc/systemd/system/chatcli-scheduler.service
[Unit]
Description=chatcli scheduler daemon
After=network.target

[Service]
Type=simple
ExecStart=%s daemon start --socket %s
Restart=on-failure
User=%s

[Install]
WantedBy=default.target
`, exe, sock, os.Getenv("USER"))
	fmt.Println(template)
	logger.Info("daemon install: template printed", zap.String("socket", sock))
	_ = args
	return nil
}

// ─── helpers ──────────────────────────────────────────────────

func daemonUsageError() error {
	return fmt.Errorf("%s", daemonUsageText())
}

func daemonUsageText() string {
	return strings.Join([]string{
		"chatcli daemon {start|stop|status|ping|install} [--socket path] [--detach]",
		"",
		"Examples:",
		"  chatcli daemon start --detach",
		"  chatcli daemon status",
		"  chatcli daemon stop",
	}, "\n")
}
