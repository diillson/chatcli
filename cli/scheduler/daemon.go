/*
 * ChatCLI - Scheduler: daemon mode.
 *
 * Running `chatcli daemon start` detaches the scheduler from the
 * interactive process. Any CLI that comes up on the same host detects
 * the daemon via the UNIX socket and becomes a thin client: /schedule,
 * /wait, /jobs all round-trip over IPC.
 *
 * Lifecycle:
 *
 *   start → bind socket, start Scheduler, accept connections
 *   stop  → orderly Scheduler.DrainAndShutdown + close listener
 *   status → print stats retrieved via IPC
 *
 * The daemon uses a PID file (daemon.pid) alongside the socket to
 * detect stale sockets after a crash. If the PID file exists but the
 * process is gone, the socket file is removed and start retries.
 */
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/diillson/chatcli/cli/bus"
	"github.com/diillson/chatcli/cli/hooks"
	"go.uber.org/zap"
)

var _ = syscall.EAGAIN // keep syscall import referenced on all platforms

// Daemon is the long-lived server binding the scheduler to a UNIX
// socket. Built from an existing Scheduler so tests can inject a mock.
type Daemon struct {
	sched      *Scheduler
	socketPath string
	pidPath    string
	logger     *zap.Logger

	listener net.Listener
	started  time.Time

	connsMu sync.Mutex
	conns   map[*daemonConn]struct{}

	// eventSubs tracks connections that subscribed to the event stream.
	// Publishing an event iterates this set under a brief lock.
	subsMu sync.RWMutex
	subs   map[*daemonConn]struct{}

	// connCounter used for metrics.
	connCounter atomic.Int64

	stopped atomic.Bool
}

// NewDaemon builds a Daemon. Caller must Start and then Wait/Stop.
func NewDaemon(s *Scheduler, socketPath string, logger *zap.Logger) *Daemon {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Daemon{
		sched:      s,
		socketPath: socketPath,
		pidPath:    socketPath + ".pid",
		logger:     logger,
		conns:      make(map[*daemonConn]struct{}),
		subs:       make(map[*daemonConn]struct{}),
	}
}

// Start binds the socket, writes the pid file, and starts accepting.
// If the socket is already held, returns ErrDaemonRunning.
func (d *Daemon) Start(ctx context.Context) error {
	// Detect stale socket.
	if err := d.maybeCleanStale(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.socketPath), 0o750); err != nil {
		return fmt.Errorf("daemon: mkdir: %w", err)
	}
	l, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", d.socketPath, err)
	}
	if err := os.Chmod(d.socketPath, 0o600); err != nil {
		_ = l.Close()
		return fmt.Errorf("daemon: chmod: %w", err)
	}
	if err := os.WriteFile(d.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		_ = l.Close()
		return fmt.Errorf("daemon: write pid: %w", err)
	}
	d.listener = l
	d.started = time.Now()

	// Subscribe to scheduler events so we can fan out to event-stream
	// connections. The bridge's PublishEvent path goes through
	// Scheduler.emit which calls bridge.PublishEvent; but since the
	// daemon is the authority, we listen to the scheduler's internal
	// emit by wrapping the bridge.
	d.sched.SetBridge(newDaemonBridge(d.sched.bridge, d))

	// Accept loop.
	go d.acceptLoop(ctx)

	d.logger.Info("scheduler daemon: started",
		zap.String("socket", d.socketPath),
		zap.String("pid_file", d.pidPath))
	return nil
}

// Stop closes the listener and shuts down the scheduler.
func (d *Daemon) Stop(drainTimeout time.Duration) error {
	if d.stopped.Swap(true) {
		return nil
	}
	if d.listener != nil {
		_ = d.listener.Close()
	}
	// Close every live connection.
	d.connsMu.Lock()
	for c := range d.conns {
		_ = c.conn.Close()
	}
	d.connsMu.Unlock()

	d.sched.DrainAndShutdown(drainTimeout)

	_ = os.Remove(d.socketPath)
	_ = os.Remove(d.pidPath)
	d.logger.Info("scheduler daemon: stopped")
	return nil
}

// Run starts the daemon and blocks until ctx is cancelled or the
// process receives SIGINT/SIGTERM. Intended entry point from the
// "chatcli daemon start" subcommand.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.Start(ctx); err != nil {
		return err
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case <-ctx.Done():
	case sig := <-sigCh:
		d.logger.Info("scheduler daemon: signal received", zap.Stringer("sig", sig))
	}
	return d.Stop(30 * time.Second)
}

func (d *Daemon) maybeCleanStale() error {
	if _, err := os.Stat(d.socketPath); err == nil {
		// Socket exists — probe it.
		if err := CheckDaemon(d.socketPath); err == nil {
			return ErrDaemonRunning
		}
		// Daemon-unreachable socket: check pid file, clean if stale.
		if data, err := os.ReadFile(d.pidPath); err == nil { //#nosec G304 -- daemon-scoped
			if pid, perr := strconv.Atoi(string(data)); perr == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					if sigErr := proc.Signal(syscall.Signal(0)); sigErr == nil {
						return ErrDaemonRunning
					}
				}
			}
		}
		_ = os.Remove(d.socketPath)
		_ = os.Remove(d.pidPath)
	}
	return nil
}

func (d *Daemon) acceptLoop(ctx context.Context) {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			// net.Listener on Unix domain sockets returns one of:
			//   - net.ErrClosed after Stop -> exit loop
			//   - ctx-driven deadline errors that manifest as syscall.EAGAIN
			//     wrapped in an OpError; these are transient and we back off
			//   - os.ErrDeadlineExceeded on a set deadline (not used here
			//     but covered for completeness)
			// nerr.Temporary() was deprecated in Go 1.18; we check the
			// specific transient errors directly.
			if errors.Is(err, net.ErrClosed) || d.stopped.Load() {
				return
			}
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, os.ErrDeadlineExceeded) {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			d.logger.Warn("daemon: accept error", zap.Error(err))
			return
		}
		dc := &daemonConn{conn: conn}
		d.connsMu.Lock()
		d.conns[dc] = struct{}{}
		d.connCounter.Add(1)
		d.connsMu.Unlock()
		d.sched.metrics.DaemonConnections.Set(float64(len(d.conns)))
		go d.handleConn(ctx, dc)
	}
}

// daemonConn is one open client connection.
type daemonConn struct {
	conn  net.Conn
	mu    sync.Mutex
	owner Owner
}

func (d *Daemon) handleConn(ctx context.Context, dc *daemonConn) {
	defer func() {
		d.connsMu.Lock()
		delete(d.conns, dc)
		d.connsMu.Unlock()
		d.subsMu.Lock()
		delete(d.subs, dc)
		d.subsMu.Unlock()
		_ = dc.conn.Close()
		d.sched.metrics.DaemonConnections.Set(float64(len(d.conns)))
	}()
	for {
		frame, err := readFrame(dc.conn)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			d.logger.Debug("daemon: read frame", zap.Error(err))
			return
		}
		if frame.Owner != nil {
			dc.owner = *frame.Owner
		}
		switch frame.Kind {
		case KindPing:
			_ = dc.Write(Frame{ID: frame.ID, Kind: KindOK})
		case KindBye:
			_ = dc.Write(Frame{ID: frame.ID, Kind: KindOK})
			return
		case KindSubscribe:
			d.subsMu.Lock()
			d.subs[dc] = struct{}{}
			d.subsMu.Unlock()
			_ = dc.Write(Frame{ID: frame.ID, Kind: KindOK})
		case KindStats:
			_ = dc.Write(d.handleStats(frame))
		case KindEnqueue:
			_ = dc.Write(d.handleEnqueue(ctx, frame))
		case KindCancel:
			_ = dc.Write(d.handleCancel(frame))
		case KindPause:
			_ = dc.Write(d.handlePause(frame))
		case KindResume:
			_ = dc.Write(d.handleResume(frame))
		case KindQuery:
			_ = dc.Write(d.handleQuery(frame))
		case KindList:
			_ = dc.Write(d.handleList(frame))
		case KindSnapshot:
			_ = dc.Write(d.handleSnapshot(frame))
		default:
			_ = dc.Write(Frame{ID: frame.ID, Kind: KindError, Error: fmt.Sprintf("unknown kind %q", frame.Kind)})
		}
	}
}

func (dc *daemonConn) Write(f Frame) error { return writeFrame(dc.conn, &dc.mu, f) }

// ─── frame handlers ──────────────────────────────────────────

func (d *Daemon) handleStats(frame Frame) Frame {
	br := map[string]BreakerState{}
	for k, v := range d.sched.condBreakers.Snapshot() {
		br["cond:"+k] = v
	}
	for k, v := range d.sched.actBreakers.Snapshot() {
		br["act:"+k] = v
	}
	stats := DaemonStats{
		Version:     "1.0",
		Started:     d.started,
		Uptime:      time.Since(d.started).String(),
		JobsActive:  d.sched.activeCount(),
		QueueDepth:  d.sched.queue.Len(),
		WALSegments: d.sched.wal.Count(),
		Connections: len(d.conns),
		Breakers:    br,
	}
	body, _ := json.Marshal(stats)
	return Frame{ID: frame.ID, Kind: KindOK, Result: body}
}

func (d *Daemon) handleEnqueue(ctx context.Context, frame Frame) Frame {
	if frame.Owner == nil {
		return errFrame(frame.ID, "owner is required")
	}
	adapter := NewToolAdapter(d.sched)
	out, _ := adapter.ScheduleJob(ctx, *frame.Owner, string(frame.Input))
	return Frame{ID: frame.ID, Kind: KindOK, Result: json.RawMessage(out)}
}

func (d *Daemon) handleCancel(frame Frame) Frame {
	var in ToolInput
	_ = json.Unmarshal(frame.Input, &in)
	owner := Owner{Kind: OwnerUser}
	if frame.Owner != nil {
		owner = *frame.Owner
	}
	if err := d.sched.Cancel(JobID(in.ID), firstNonEmpty(in.Reason, "ipc-cancelled"), owner); err != nil {
		return errFrame(frame.ID, err.Error())
	}
	return Frame{ID: frame.ID, Kind: KindOK}
}

func (d *Daemon) handlePause(frame Frame) Frame {
	var in ToolInput
	_ = json.Unmarshal(frame.Input, &in)
	owner := Owner{Kind: OwnerUser}
	if frame.Owner != nil {
		owner = *frame.Owner
	}
	if err := d.sched.Pause(JobID(in.ID), firstNonEmpty(in.Reason, "ipc-paused"), owner); err != nil {
		return errFrame(frame.ID, err.Error())
	}
	return Frame{ID: frame.ID, Kind: KindOK}
}

func (d *Daemon) handleResume(frame Frame) Frame {
	var in ToolInput
	_ = json.Unmarshal(frame.Input, &in)
	owner := Owner{Kind: OwnerUser}
	if frame.Owner != nil {
		owner = *frame.Owner
	}
	if err := d.sched.Resume(JobID(in.ID), owner); err != nil {
		return errFrame(frame.ID, err.Error())
	}
	return Frame{ID: frame.ID, Kind: KindOK}
}

func (d *Daemon) handleQuery(frame Frame) Frame {
	var in ToolInput
	_ = json.Unmarshal(frame.Input, &in)
	j, err := d.sched.Query(JobID(in.ID))
	if err != nil {
		return errFrame(frame.ID, err.Error())
	}
	body, _ := json.Marshal(ToolOutput{OK: true, Job: j, Status: j.Status})
	return Frame{ID: frame.ID, Kind: KindOK, Result: body}
}

func (d *Daemon) handleList(frame Frame) Frame {
	var in ToolInput
	_ = json.Unmarshal(frame.Input, &in)
	list := d.sched.List(in.Filter)
	body, _ := json.Marshal(ToolOutput{OK: true, Jobs: list})
	return Frame{ID: frame.ID, Kind: KindOK, Result: body}
}

func (d *Daemon) handleSnapshot(frame Frame) Frame {
	if err := d.sched.writeSnapshotNow(); err != nil {
		return errFrame(frame.ID, err.Error())
	}
	return Frame{ID: frame.ID, Kind: KindOK}
}

func errFrame(id, msg string) Frame {
	return Frame{ID: id, Kind: KindError, Error: msg}
}

// fanOutEvent pushes an Event to every subscribed connection.
func (d *Daemon) fanOutEvent(evt Event) {
	d.subsMu.RLock()
	subs := make([]*daemonConn, 0, len(d.subs))
	for c := range d.subs {
		subs = append(subs, c)
	}
	d.subsMu.RUnlock()
	for _, c := range subs {
		_ = c.Write(Frame{Kind: KindEvent, Event: &evt})
	}
}

// daemonBridge wraps the configured CLIBridge and also fans events
// out to subscribed IPC clients.
type daemonBridge struct {
	CLIBridge
	d *Daemon
}

func newDaemonBridge(inner CLIBridge, d *Daemon) CLIBridge {
	return &daemonBridge{CLIBridge: inner, d: d}
}

func (b *daemonBridge) PublishEvent(evt Event) {
	if b.CLIBridge != nil {
		b.CLIBridge.PublishEvent(evt)
	}
	b.d.fanOutEvent(evt)
}

// Ensure external imports remain referenced.
var (
	_ = bus.MessageTypeSystem
	_ = hooks.EventSessionStart
)
