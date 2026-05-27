package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Position is a zero-based line/character location.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Diagnostic is one problem reported by the server.
type Diagnostic struct {
	Range struct {
		Start Position `json:"start"`
		End   Position `json:"end"`
	} `json:"range"`
	Severity int    `json:"severity"` // 1 error, 2 warning, 3 info, 4 hint
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// SeverityLabel renders the numeric severity.
func (d Diagnostic) SeverityLabel() string {
	switch d.Severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "diagnostic"
	}
}

// Client is a minimal LSP client over a single server connection.
type Client struct {
	w       io.Writer
	r       *bufio.Reader
	cmd     *exec.Cmd
	logger  *zap.Logger
	writeMu sync.Mutex

	idMu    sync.Mutex
	nextID  int
	pending map[int]chan rpcMessage

	diagsMu  sync.Mutex
	diags    map[string][]Diagnostic
	received map[string]bool

	done chan struct{}
}

// New builds a client over an arbitrary reader/writer pair (used by tests).
func New(w io.Writer, r io.Reader, logger *zap.Logger) *Client {
	c := &Client{
		w:        w,
		r:        bufio.NewReader(r),
		logger:   logger,
		pending:  map[int]chan rpcMessage{},
		diags:    map[string][]Diagnostic{},
		received: map[string]bool{},
		done:     make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Spawn launches a language server subprocess and returns a connected client.
func Spawn(ctx context.Context, spec ServerSpec, logger *zap.Logger) (*Client, error) {
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("empty server command")
	}
	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...) //#nosec G204 -- command from curated presets / explicit env override
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.Command[0], err)
	}
	c := &Client{
		w:        stdin,
		r:        bufio.NewReader(stdout),
		cmd:      cmd,
		logger:   logger,
		pending:  map[int]chan rpcMessage{},
		diags:    map[string][]Diagnostic{},
		received: map[string]bool{},
		done:     make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		body, err := ReadMessage(c.r)
		if err != nil {
			return
		}
		var m rpcMessage
		if err := json.Unmarshal(body, &m); err != nil {
			continue
		}
		switch {
		case m.ID != nil && m.Method != "":
			// Server-to-client request — reply with null so handshakes that
			// expect a response (registerCapability, configuration) proceed.
			_ = c.writeRaw(map[string]interface{}{"jsonrpc": "2.0", "id": m.ID, "result": nil})
		case m.ID != nil:
			c.deliver(m)
		case m.Method == "textDocument/publishDiagnostics":
			c.handleDiagnostics(m.Params)
		}
	}
}

func (c *Client) deliver(m rpcMessage) {
	var id int
	if err := json.Unmarshal(*m.ID, &id); err != nil {
		return
	}
	c.idMu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.idMu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (c *Client) handleDiagnostics(params json.RawMessage) {
	var p struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	c.diagsMu.Lock()
	c.diags[p.URI] = p.Diagnostics
	c.received[p.URI] = true
	c.diagsMu.Unlock()
}

func (c *Client) call(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
	c.idMu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMessage, 1)
	c.pending[id] = ch
	c.idMu.Unlock()

	if err := c.writeRaw(map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("lsp %s: %s", method, m.Error.Message)
		}
		return m.Result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("lsp %s: timeout", method)
	case <-c.done:
		return nil, fmt.Errorf("lsp %s: connection closed", method)
	}
}

func (c *Client) notify(method string, params interface{}) error {
	return c.writeRaw(map[string]interface{}{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) writeRaw(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteMessage(c.w, v)
}

// Initialize performs the LSP initialize/initialized handshake.
func (c *Client) Initialize(rootURI string) error {
	_, err := c.call("initialize", map[string]interface{}{
		"processId": nil,
		"rootUri":   rootURI,
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"publishDiagnostics": map[string]interface{}{},
			},
		},
	}, 15*time.Second)
	if err != nil {
		return err
	}
	return c.notify("initialized", map[string]interface{}{})
}

// DidOpen notifies the server that a document is open with the given content.
func (c *Client) DidOpen(uri, languageID, text string) error {
	return c.notify("textDocument/didOpen", map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	})
}

// Diagnostics waits up to timeout for the first publishDiagnostics for uri and
// returns them. ok is false if none arrived in time. An empty slice with
// ok=true means the server reported no problems.
func (c *Client) Diagnostics(uri string, timeout time.Duration) (diags []Diagnostic, ok bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.diagsMu.Lock()
		if c.received[uri] {
			d := c.diags[uri]
			c.diagsMu.Unlock()
			return d, true
		}
		c.diagsMu.Unlock()
		select {
		case <-c.done:
			return nil, false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, false
}

// Shutdown requests an orderly shutdown and terminates the server process.
func (c *Client) Shutdown() {
	_, _ = c.call("shutdown", nil, 3*time.Second)
	_ = c.notify("exit", nil)
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}
