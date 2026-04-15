package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// stdioTransport implements mcpTransport over stdin/stdout using
// Content-Length framed JSON-RPC 2.0 (LSP-style framing).
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

	// Discard stderr to avoid blocking
	cmd.Stderr = nil

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
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("MCP call %q timed out", method)
	case <-t.done:
		return nil, fmt.Errorf("MCP transport closed")
	}
}

// send writes a JSON-RPC message with Content-Length framing.
func (t *stdioTransport) send(req jsonRPCRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(data), data)

	t.mu.Lock()
	defer t.mu.Unlock()

	_, err = io.WriteString(t.stdin, msg)
	return err
}

// readLoop reads JSON-RPC responses from stdout and dispatches them.
func (t *stdioTransport) readLoop() {
	defer close(t.done)

	for {
		// Read Content-Length header
		contentLen, err := t.readContentLength()
		if err != nil {
			if err != io.EOF {
				t.logger.Debug("MCP read loop error", zap.Error(err))
			}
			return
		}

		// Read body
		body := make([]byte, contentLen)
		if _, err := io.ReadFull(t.stdout, body); err != nil {
			t.logger.Debug("MCP read body error", zap.Error(err))
			return
		}

		var resp jsonRPCResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			t.logger.Debug("MCP unmarshal error", zap.Error(err))
			continue
		}

		// Dispatch to waiting caller
		t.pendMu.Lock()
		ch, ok := t.pending[resp.ID]
		t.pendMu.Unlock()

		if ok {
			ch <- &resp
		}
	}
}

// readContentLength reads the Content-Length header from the stream.
func (t *stdioTransport) readContentLength() (int, error) {
	for {
		line, err := t.stdout.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)

		if line == "" {
			continue // skip blank lines between messages
		}

		if strings.HasPrefix(line, "Content-Length:") {
			valStr := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLen, err := strconv.Atoi(valStr)
			if err != nil {
				return 0, fmt.Errorf("invalid Content-Length: %q", valStr)
			}

			// Read until empty line (end of headers)
			for {
				hdr, err := t.stdout.ReadString('\n')
				if err != nil {
					return 0, err
				}
				if strings.TrimSpace(hdr) == "" {
					break
				}
			}

			return contentLen, nil
		}

		// Some servers send JSON directly without Content-Length framing.
		// Try to parse as JSON.
		if strings.HasPrefix(line, "{") {
			var resp jsonRPCResponse
			if err := json.Unmarshal([]byte(line), &resp); err == nil {
				t.pendMu.Lock()
				ch, ok := t.pending[resp.ID]
				t.pendMu.Unlock()
				if ok {
					ch <- &resp
				}
				continue
			}
		}
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
