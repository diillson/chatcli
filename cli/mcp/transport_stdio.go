package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// stdioCallTimeout caps how long Call() waits for a JSON-RPC response.
// 60s covers the worst case of an `npx -y <pkg>` cold start that has to
// download the MCP server package on its first invocation; subsequent
// runs hit the npm cache and respond in milliseconds.
const stdioCallTimeout = 60 * time.Second

// stdioTransport implements mcpTransport over stdin/stdout using
// newline-delimited JSON-RPC 2.0, as required by the MCP stdio
// transport spec. Each JSON message is written and read as a single
// line terminated by '\n' — no LSP-style Content-Length headers.
type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	mu      sync.Mutex // protects stdin writes
	nextID  int64
	pending map[int64]chan *jsonRPCResponse
	pendMu  sync.Mutex
	logger  *zap.Logger
	done    chan struct{}
	// onClose fires exactly once when the transport's read loop or
	// stdin write detects the process has gone away (EOF, EPIPE,
	// killed). The Manager registers it to flip Status.Connected back
	// to false so /mcp status reflects the real state without the
	// user having to /mcp restart.
	onClose     func(error)
	onCloseOnce sync.Once
}

// newStdioTransport spawns the MCP server process and starts the read loop.
func newStdioTransport(ctx context.Context, cfg ServerConfig, logger *zap.Logger) (*stdioTransport, error) {
	args := cfg.Args
	cmd := exec.CommandContext(ctx, cfg.Command, args...) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream

	// Set environment variables
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	// Capture stderr instead of discarding it so failures like "npm
	// 404", missing executable, or a server panic surface in the debug
	// log instead of vanishing.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server %q: %w", cfg.Command, err)
	}

	t := &stdioTransport{
		cmd:     cmd,
		stdin:   stdinPipe,
		stdout:  bufio.NewReaderSize(stdoutPipe, 64*1024),
		pending: make(map[int64]chan *jsonRPCResponse),
		logger:  logger,
		done:    make(chan struct{}),
	}

	go t.readLoop()
	go t.drainStderr(cfg.Name, stderrPipe)

	return t, nil
}

// Call sends a JSON-RPC request and waits for the response.
func (t *stdioTransport) Call(method string, params interface{}) (json.RawMessage, error) {
	id := atomic.AddInt64(&t.nextID, 1)

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	respCh := make(chan *jsonRPCResponse, 1)
	t.pendMu.Lock()
	t.pending[id] = respCh
	t.pendMu.Unlock()

	defer func() {
		t.pendMu.Lock()
		delete(t.pending, id)
		t.pendMu.Unlock()
	}()

	if err := t.send(req); err != nil {
		return nil, err
	}

	// Wait for response with timeout
	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("MCP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-time.After(stdioCallTimeout):
		return nil, fmt.Errorf("MCP call %q timed out", method)
	case <-t.done:
		return nil, fmt.Errorf("MCP transport closed")
	}
}

// send writes a JSON-RPC message as a single newline-terminated line,
// the framing required by the MCP stdio transport spec. The trailing
// '\n' is appended in-buffer so the entire frame goes out in one Write
// — splitting it lets a slow consumer interleave with another sender's
// Write under stdin contention.
//
// A failed write (typically EPIPE because the child process died)
// fires the onClose callback so the manager can mark the server as
// disconnected and surface the error in /mcp status.
func (t *stdioTransport) send(req jsonRPCRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	frame := append(data, '\n')

	t.mu.Lock()
	defer t.mu.Unlock()

	if _, err := t.stdin.Write(frame); err != nil {
		t.fireClose(fmt.Errorf("stdin write failed: %w", err))
		return err
	}
	return nil
}

// fireClose invokes the onClose callback at most once.
func (t *stdioTransport) fireClose(reason error) {
	if t.onClose == nil {
		return
	}
	t.onCloseOnce.Do(func() { t.onClose(reason) })
}

// readLoop reads newline-delimited JSON-RPC responses from stdout and
// dispatches them. Lines that don't parse as a JSON-RPC response are
// logged at debug and skipped — some servers print human-readable
// banners or warnings to stdout before the protocol stream begins.
//
// When the loop exits — typically on EOF because the child process
// died — it fires the onClose callback so the manager can mark the
// server as disconnected.
func (t *stdioTransport) readLoop() {
	var exitErr error
	defer func() {
		close(t.done)
		if exitErr == nil {
			exitErr = io.EOF
		}
		t.fireClose(exitErr)
	}()

	for {
		line, err := t.stdout.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				t.logger.Debug("MCP read loop error", zap.Error(err))
				exitErr = err
			}
			// Drain any final, unterminated line.
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				t.tryDispatch(trimmed)
			}
			return
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		t.tryDispatch(trimmed)
	}
}

// tryDispatch attempts to parse a line as a JSON-RPC response and
// route it to the waiting Call(). Non-JSON or unparseable lines are
// logged and dropped.
func (t *stdioTransport) tryDispatch(line string) {
	var resp jsonRPCResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.logger.Debug("MCP non-JSON line on stdout",
			zap.String("line", line),
			zap.Error(err))
		return
	}

	t.pendMu.Lock()
	ch, ok := t.pending[resp.ID]
	t.pendMu.Unlock()

	if ok {
		ch <- &resp
	}
}

// drainStderr forwards the server's stderr to the debug log so
// failures like "npm 404", missing executable or a panic are visible
// when the user runs with --debug.
func (t *stdioTransport) drainStderr(name string, r io.ReadCloser) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		t.logger.Debug("MCP stderr",
			zap.String("server", name),
			zap.String("line", scanner.Text()))
	}
}

// Close kills the MCP server process and cleans up.
func (t *stdioTransport) Close() error {
	t.mu.Lock()
	_ = t.stdin.Close()
	t.mu.Unlock()

	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}
