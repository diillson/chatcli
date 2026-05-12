package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// sseTransport implements mcpTransport over HTTP+SSE.
// Requests are sent via HTTP POST; responses arrive via Server-Sent Events.
type sseTransport struct {
	baseURL     string
	messagesURL string // discovered from SSE endpoint event
	httpClient  *http.Client
	nextID      int64
	pending     map[int64]chan *jsonRPCResponse
	pendMu      sync.Mutex
	logger      *zap.Logger
	cancel      context.CancelFunc
	done        chan struct{}
	ready       chan struct{}   // closed when messagesURL is discovered
	channelMgr  *ChannelManager // receives push notifications
	serverName  string          // for channel routing
	callTimeout time.Duration   // resolved from ServerConfig.RequestTimeout()
	initTimeout time.Duration   // resolved from ServerConfig.InitializeTimeout()
	headers     map[string]string
	auth        *AuthConfig
}

// newSSETransport connects to the MCP server SSE endpoint and starts listening.
func newSSETransport(ctx context.Context, cfg ServerConfig, logger *zap.Logger, channelMgr *ChannelManager, serverName string) (*sseTransport, error) {
	ctx, cancel := context.WithCancel(ctx)

	callTimeout := cfg.RequestTimeout()
	initTimeout := cfg.InitializeTimeout()

	t := &sseTransport{
		baseURL: strings.TrimSuffix(cfg.URL, "/"),
		// The HTTP client timeout caps individual POSTs. We deliberately
		// keep it generous (max of the configured call and init values)
		// so the SSE listener — which holds the GET open for the lifetime
		// of the connection — does not get killed mid-stream by a short
		// per-server Timeout.
		httpClient:  &http.Client{Timeout: maxDuration(callTimeout, initTimeout)},
		pending:     make(map[int64]chan *jsonRPCResponse),
		logger:      logger,
		cancel:      cancel,
		done:        make(chan struct{}),
		ready:       make(chan struct{}),
		channelMgr:  channelMgr,
		serverName:  serverName,
		callTimeout: callTimeout,
		initTimeout: initTimeout,
		headers:     cfg.ResolveHeaders(),
		auth:        cfg.Auth,
	}

	// Connect to SSE endpoint
	go t.connectSSE(ctx)

	// Wait for the server to send the messages endpoint URL
	select {
	case <-t.ready:
		// good — messages URL discovered
	case <-time.After(initTimeout):
		cancel()
		return nil, fmt.Errorf("SSE server did not send endpoint event within %s", initTimeout)
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}

	return t, nil
}

// applyHeaders attaches the configured custom headers and auth to a
// request. Safe on nil receivers — used for both the initial SSE GET
// and the per-call POST so users only declare the header set once.
func (t *sseTransport) applyHeaders(req *http.Request) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.auth.ApplyAuth(req)
}

// maxDuration returns the greater of a and b. Helper kept private to
// this package because we only need it for the http.Client timeout
// floor above.
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// connectSSE connects to the SSE stream and processes events.
func (t *sseTransport) connectSSE(ctx context.Context) {
	defer close(t.done)

	sseURL := t.baseURL + "/sse"
	req, err := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	if err != nil {
		t.logger.Error("SSE request creation failed", zap.Error(err))
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	t.applyHeaders(req)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		t.logger.Error("SSE connection failed", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.logger.Error("SSE bad status", zap.Int("status", resp.StatusCode))
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventType, eventData string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// End of event — process it
			if eventType != "" && eventData != "" {
				t.handleSSEEvent(eventType, eventData)
			}
			eventType = ""
			eventData = ""
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			eventData = strings.TrimPrefix(line, "data: ")
		}
	}
}

// handleSSEEvent processes a single SSE event.
func (t *sseTransport) handleSSEEvent(eventType, data string) {
	switch eventType {
	case "endpoint":
		// Server tells us where to POST messages
		if strings.HasPrefix(data, "/") {
			t.messagesURL = t.baseURL + data
		} else {
			t.messagesURL = data
		}
		// Signal that we're ready
		select {
		case <-t.ready:
			// already closed
		default:
			close(t.ready)
		}

	case "message":
		var resp jsonRPCResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			t.logger.Debug("SSE message parse error", zap.Error(err))
			return
		}

		// If message has an ID, it's a response to a pending request
		if resp.ID != 0 {
			t.pendMu.Lock()
			ch, ok := t.pending[resp.ID]
			t.pendMu.Unlock()

			if ok {
				ch <- &resp
			}
		} else {
			// No ID = push notification from server → route to ChannelManager
			if t.channelMgr != nil {
				t.channelMgr.ProcessSSENotification(t.serverName, []byte(data))
			}
		}

	default:
		// Unknown event types may be server-specific notifications
		if t.channelMgr != nil && data != "" {
			t.channelMgr.ProcessSSENotification(t.serverName, []byte(data))
		}
	}
}

// Call sends a JSON-RPC request via HTTP POST and waits for the SSE response.
func (t *sseTransport) Call(method string, params interface{}) (json.RawMessage, error) {
	if t.messagesURL == "" {
		return nil, fmt.Errorf("SSE transport not ready — no messages endpoint")
	}

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

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", t.messagesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	t.applyHeaders(httpReq)

	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("MCP POST failed: %w", err)
	}
	// Read and discard body (response comes via SSE)
	_, _ = io.Copy(io.Discard, httpResp.Body)
	_ = httpResp.Body.Close()

	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("MCP POST returned %d", httpResp.StatusCode)
	}

	// Wait for SSE response
	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("MCP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-time.After(t.callTimeout):
		return nil, fmt.Errorf("MCP call %q timed out after %s", method, t.callTimeout)
	case <-t.done:
		return nil, fmt.Errorf("SSE transport closed")
	}
}

// Close cancels the SSE connection.
func (t *sseTransport) Close() error {
	t.cancel()
	<-t.done
	return nil
}
