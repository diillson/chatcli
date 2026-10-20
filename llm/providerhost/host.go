/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package providerhost runs provider plugins (pkg/providerproto) inside
// chatcli: it supervises each plugin process, keeps its credential in the
// auth store, and exposes the plugin as an ordinary LLM provider — a full-
// mode adapter over chat/send, or a built-in client pointed at the plugin's
// backend in dialect mode.
package providerhost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/pkg/textfold"
	"go.uber.org/zap"
)

// Supervision bounds. They keep a crashing plugin from becoming a fork
// loop and a hung one from hanging the session.
const (
	startTimeout     = 15 * time.Second
	defaultCallLimit = 60 * time.Second
	exitGrace        = 5 * time.Second
	minRestartDelay  = 500 * time.Millisecond
	maxRestartDelay  = 60 * time.Second
	breakerThreshold = 5
	breakerCooldown  = 2 * time.Minute
	stderrTailBytes  = 16 << 10
	crashSettle      = 2 * time.Second
)

// ErrUnavailable reports a plugin that cannot serve right now (failed to
// start, crashed, or its circuit breaker is open). Callers map it to an
// upstream-unavailable error so retries and failover behave as they do for
// a built-in provider that is down.
var ErrUnavailable = errors.New("provider plugin unavailable")

// HostOptions configures one plugin process.
type HostOptions struct {
	// Path is the plugin binary as installed.
	Path string
	// Resolve applies the signature policy and returns what to execute
	// (a staged verified copy, usually). Nil runs Path as-is.
	Resolve func(path string) (string, error)
	Logger  *zap.Logger
	// HostVersion is sent in initialize.
	HostVersion string
	// Interactive tells the plugin whether a person can answer login steps.
	Interactive bool
	// Environ is the parent environment; nil means os.Environ().
	Environ []string
}

// Host supervises one plugin process. It starts lazily, restarts after a
// crash with exponential backoff and jitter, and opens a circuit breaker
// after repeated start failures. All methods are safe for concurrent use.
type Host struct {
	opts   HostOptions
	logger *zap.Logger

	mu        sync.Mutex
	proc      *process
	info      *providerproto.InitializeResult
	authEnv   []string
	failures  int
	nextStart time.Time
	openUntil time.Time
	closed    bool
	lastErr   error

	sinksMu sync.Mutex
	sinks   map[string]func(providerproto.ChatDelta)

	// label is the provider name once known. It is read on the connection
	// reader goroutine ($/log), which must never wait on mu: ensure holds mu
	// while it waits for the initialize reply that same reader delivers.
	label atomic.Value
}

type process struct {
	cmd    *exec.Cmd
	conn   *providerproto.Conn
	stderr *tailBuffer
	stdin  interface{ Close() error }
	done   chan struct{}
	cancel context.CancelFunc
}

// NewHost prepares a host; nothing runs until the first call.
func NewHost(opts HostOptions) *Host {
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Host{opts: opts, logger: logger, sinks: map[string]func(providerproto.ChatDelta){}}
}

// Info returns the handshake result, starting the process if needed.
func (h *Host) Info(ctx context.Context) (providerproto.InitializeResult, error) {
	if _, err := h.ensure(ctx); err != nil {
		return providerproto.InitializeResult{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.info, nil
}

// LastError returns the most recent start or crash error, for diagnostics.
func (h *Host) LastError() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastErr
}

// Stderr returns the tail of what the running (or last) process logged.
func (h *Host) Stderr() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.proc == nil {
		return ""
	}
	return h.proc.stderr.String()
}

// Call sends a request, starting or restarting the plugin when needed.
// Non-chat calls are bounded by a default timeout when ctx has none.
func (h *Host) Call(ctx context.Context, method string, params, out any) error {
	return h.callWith(ctx, method, params, out, nil)
}

// CallStreaming is Call for chat/send with stream=true: each chat/delta for
// this request is handed to onDelta, in order, before Call returns.
func (h *Host) CallStreaming(ctx context.Context, method string, params, out any, onDelta func(providerproto.ChatDelta)) error {
	return h.callWith(ctx, method, params, out, onDelta)
}

func (h *Host) callWith(ctx context.Context, method string, params, out any, onDelta func(providerproto.ChatDelta)) error {
	p, err := h.ensure(ctx)
	if err != nil {
		return err
	}
	if _, has := ctx.Deadline(); !has && method != providerproto.MethodChatSend {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallLimit)
		defer cancel()
	}
	var onID func(json.RawMessage)
	if onDelta != nil {
		var key string
		onID = func(id json.RawMessage) {
			key = string(id)
			h.sinksMu.Lock()
			h.sinks[key] = onDelta
			h.sinksMu.Unlock()
		}
		defer func() {
			h.sinksMu.Lock()
			delete(h.sinks, key)
			h.sinksMu.Unlock()
		}()
	}
	err = p.conn.CallObserved(ctx, method, params, out, onID)
	if errors.Is(err, providerproto.ErrClosed) {
		// The connection closes before exec finishes copying the process's
		// stderr (that completes inside Wait); wait for the exit, briefly,
		// so the error can quote what the plugin printed as it died.
		select {
		case <-p.done:
		case <-time.After(crashSettle):
		}
		h.markCrashed(p)
		return fmt.Errorf("%w: %s exited: %s", ErrUnavailable, h.name(), h.crashDetail(p))
	}
	return err
}

func (h *Host) name() string {
	if v, ok := h.label.Load().(string); ok && v != "" {
		return v
	}
	return h.opts.Path
}

// crashDetailLines is how much of the stderr tail a crash error quotes: a
// runtime warning or the last frame of a trace often follows the real cause.
const (
	crashDetailLines = 3
	crashDetailBytes = 400
)

func (h *Host) crashDetail(p *process) string {
	var lines []string
	for _, l := range strings.Split(p.stderr.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "no stderr output"
	}
	if len(lines) > crashDetailLines {
		lines = lines[len(lines)-crashDetailLines:]
	}
	detail := strings.Join(lines, " | ")
	if len(detail) > crashDetailBytes {
		detail = "…" + textfold.Tail(detail, crashDetailBytes)
	}
	return detail
}

// handle serves plugin → host messages: chat/delta routing and $/log.
func (h *Host) handle(_ context.Context, req *providerproto.Request) (json.RawMessage, error) {
	switch req.Method {
	case providerproto.MethodChatDelta:
		var d providerproto.ChatDelta
		if json.Unmarshal(req.Params, &d) != nil {
			return nil, nil
		}
		h.sinksMu.Lock()
		sink := h.sinks[string(d.RequestID)]
		h.sinksMu.Unlock()
		if sink != nil {
			sink(d)
		}
	case providerproto.MethodLog:
		var l providerproto.LogParams
		if json.Unmarshal(req.Params, &l) == nil {
			h.log(l)
		}
	default:
		if !req.IsNotification() {
			return nil, providerproto.NewError(providerproto.ErrCodeMethodNotFound, "host does not serve "+req.Method)
		}
	}
	return nil, nil
}

func (h *Host) log(l providerproto.LogParams) {
	fields := []zap.Field{zap.String("plugin", h.name()), zap.String("message", l.Message)}
	switch l.Level {
	case "error":
		h.logger.Error("provider plugin", fields...)
	case "warn":
		h.logger.Warn("provider plugin", fields...)
	case "info":
		h.logger.Info("provider plugin", fields...)
	default:
		h.logger.Debug("provider plugin", fields...)
	}
}

// ensure returns a running, initialized process.
func (h *Host) ensure(ctx context.Context) (*process, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, fmt.Errorf("%w: host closed", ErrUnavailable)
	}
	if h.proc != nil {
		select {
		case <-h.proc.done:
			h.proc = nil
		default:
			return h.proc, nil
		}
	}
	now := time.Now()
	if now.Before(h.openUntil) {
		return nil, fmt.Errorf("%w: circuit open for another %s after %d failed starts%s",
			ErrUnavailable, h.openUntil.Sub(now).Round(time.Second), breakerThreshold, errSuffix(h.lastErr))
	}
	if now.Before(h.nextStart) {
		return nil, fmt.Errorf("%w: restarting in %s%s", ErrUnavailable, h.nextStart.Sub(now).Round(time.Millisecond), errSuffix(h.lastErr))
	}

	p, info, err := h.start(ctx, h.authEnv)
	if err == nil && len(info.AuthEnv) > 0 && !sameSet(info.AuthEnv, h.authEnv) {
		// The plugin declared the env vars it reads only now; restart it
		// with them so credentials from the environment reach it.
		h.stop(ctx, p)
		h.authEnv = append([]string(nil), info.AuthEnv...)
		p, info, err = h.start(ctx, h.authEnv)
	}
	if err != nil {
		h.recordFailure(err)
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	h.failures = 0
	h.lastErr = nil
	h.proc = p
	h.info = &info
	h.label.Store(info.Provider.Name)
	return p, nil
}

func (h *Host) recordFailure(err error) {
	h.lastErr = err
	h.failures++
	delay := minRestartDelay << (h.failures - 1)
	if delay <= 0 || delay > maxRestartDelay {
		delay = maxRestartDelay
	}
	// Jitter keeps many sessions from restarting a shared plugin in
	// lockstep.
	delay = delay/2 + jitter(delay/2)
	h.nextStart = time.Now().Add(delay)
	if h.failures >= breakerThreshold {
		h.openUntil = time.Now().Add(breakerCooldown)
		h.failures = 0
	}
	h.logger.Warn("provider plugin failed to start",
		zap.String("path", h.opts.Path), zap.Error(err), zap.Duration("retry_in", delay))
}

func (h *Host) markCrashed(p *process) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.proc == p {
		h.proc = nil
		h.lastErr = fmt.Errorf("plugin exited: %s", h.crashDetail(p))
		h.nextStart = time.Now().Add(minRestartDelay)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			return false
		}
	}
	return true
}

// start spawns the plugin and completes the handshake.
func (h *Host) start(ctx context.Context, authEnv []string) (*process, providerproto.InitializeResult, error) {
	var zero providerproto.InitializeResult
	execPath := h.opts.Path
	if h.opts.Resolve != nil {
		resolved, err := h.opts.Resolve(h.opts.Path)
		if err != nil {
			return nil, zero, err
		}
		execPath = resolved
	}

	// The process outlives the call that started it, so its lifetime is
	// detached from ctx's cancellation (Close ends it).
	procCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	// #nosec G204 -- execPath is the installed provider plugin, admitted by
	// the plugin signature policy (Resolve) before it ever runs.
	cmd := exec.CommandContext(procCtx, execPath)
	environ := h.opts.Environ
	if environ == nil {
		environ = os.Environ()
	}
	cmd.Env = pluginEnv(environ, authEnv)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, zero, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, zero, err
	}
	tail := &tailBuffer{max: stderrTailBytes}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, zero, fmt.Errorf("start %s: %w", h.opts.Path, err)
	}

	p := &process{cmd: cmd, stderr: tail, stdin: stdin, done: make(chan struct{}), cancel: cancel}
	p.conn = providerproto.NewConn(stdout, stdin, h.handle, providerproto.Options{
		OnProtocolError: func(err error) {
			h.logger.Warn("provider plugin protocol error", zap.String("path", h.opts.Path), zap.Error(err))
		},
	})
	go func() {
		_ = p.conn.Run(procCtx)
		_ = cmd.Wait()
		close(p.done)
	}()

	hctx, hcancel := context.WithTimeout(ctx, startTimeout)
	defer hcancel()
	var info providerproto.InitializeResult
	err = p.conn.Call(hctx, providerproto.MethodInitialize, providerproto.InitializeParams{
		ProtocolVersions: []string{providerproto.ProtocolVersion},
		Host:             providerproto.HostInfo{Name: "chatcli", Version: h.opts.HostVersion},
		Capabilities:     providerproto.HostCapabilities{InteractiveAuth: h.opts.Interactive},
	}, &info)
	if err == nil {
		err = validateInfo(info)
	}
	if err != nil {
		detail := strings.TrimSpace(tail.String())
		h.stop(ctx, p)
		if detail != "" {
			return nil, zero, fmt.Errorf("initialize: %w (stderr: %s)", err, lastLine(detail))
		}
		return nil, zero, fmt.Errorf("initialize: %w", err)
	}
	_ = p.conn.Notify(ctx, providerproto.MethodInitialized, struct{}{})
	return p, info, nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// stop runs shutdown → exit, then kills a plugin that ignores both. The
// grace period is detached from ctx's cancellation: a cleanup that runs on
// an already-cancelled context must still let the plugin exit cleanly.
func (h *Host) stop(ctx context.Context, p *process) {
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), exitGrace)
	defer cancel()
	_ = p.conn.Call(ctx, providerproto.MethodShutdown, nil, nil)
	_ = p.conn.Notify(ctx, providerproto.MethodExit, nil)
	select {
	case <-p.done:
	case <-time.After(exitGrace):
		_ = p.stdin.Close()
		select {
		case <-p.done:
		case <-time.After(exitGrace):
			p.cancel() // CommandContext kills the process
			<-p.done
		}
	}
	p.cancel()
}

// Close stops the plugin process; the host refuses further calls.
func (h *Host) Close(ctx context.Context) {
	h.mu.Lock()
	p := h.proc
	h.proc = nil
	h.closed = true
	h.mu.Unlock()
	h.stop(ctx, p)
}

// errSuffix renders an optional cause as ": cause" (empty for nil).
func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

// baseEnvKeys are inherited by every plugin: what a process needs to run
// and to reach the network from a corporate machine (proxies, CA bundles),
// nothing that carries another provider's credentials.
var baseEnvKeys = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
	"TMPDIR", "TEMP", "TMP", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS",
	"CHATCLI_CA_BUNDLE", "CHATCLI_LANG",
	// Windows essentials.
	"SystemRoot", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "APPDATA", "LOCALAPPDATA",
	"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "PROGRAMDATA", "ProgramFiles", "ProgramFiles(x86)",
}

// pluginEnv filters environ down to baseEnvKeys plus the plugin's declared
// authEnv. Matching is case-insensitive on Windows, exact elsewhere.
func pluginEnv(environ, authEnv []string) []string {
	allowed := make(map[string]bool, len(baseEnvKeys)+len(authEnv))
	norm := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	for _, k := range baseEnvKeys {
		allowed[norm(k)] = true
	}
	for _, k := range authEnv {
		if k = strings.TrimSpace(k); k != "" {
			allowed[norm(k)] = true
		}
	}
	out := make([]string, 0, len(allowed))
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if ok && allowed[norm(k)] {
			out = append(out, kv)
		}
	}
	return out
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// jitter returns a uniformly distributed duration in [0, limit].
func jitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(limit)+1))
	if err != nil {
		return limit / 2
	}
	return time.Duration(n.Int64())
}
