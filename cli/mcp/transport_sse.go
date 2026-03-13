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
	ready       chan struct{} // closed when messagesURL is discovered
}

// newSSETransport connects to the MCP server SSE endpoint and starts listening.
func newSSETransport(ctx context.Context, cfg ServerConfig, logger *zap.Logger) (*sseTransport, error) {
	ctx, cancel := context.WithCancel(ctx)

	t := &sseTransport{
		baseURL:    strings.TrimSuffix(cfg.URL, "/"),
		httpClient: &http.Client{Timeout: 60 * time.Second},
		pending:    make(map[int64]chan *jsonRPCResponse),
		logger:     logger,
		cancel:     cancel,
		done:       make(chan struct{}),
		ready:      make(chan struct{}),
	}

	// Connect to SSE endpoint
	go t.connectSSE(ctx)

	// Wait for the server to send the messages endpoint URL
	select {
	case <-t.ready:
		// good — messages URL discovered
	case <-time.After(10 * time.Second):
		cancel()
		return nil, fmt.Errorf("SSE server did not send endpoint event within 10s")
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}

	return t, nil
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

		t.pendMu.Lock()
		ch, ok := t.pending[resp.ID]
		t.pendMu.Unlock()

		if ok {
			ch <- &resp
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

	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("MCP POST failed: %w", err)
	}
	// Read and discard body (response comes via SSE)
	io.Copy(io.Discard, httpResp.Body)
	httpResp.Body.Close()

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
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("MCP call %q timed out", method)
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
