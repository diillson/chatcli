/*
 * ChatCLI - MCP transport: HTTP+SSE
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * SSE transport with a supervised connect loop:
 *
 *   - One goroutine holds the GET /sse open and parses events as they
 *     arrive. When the stream drops (network blip, server restart,
 *     idle timeout from a proxy), the supervisor re-establishes the
 *     connection using full-jitter exponential backoff so a server
 *     in a crash loop does not get hammered by every chatcli session
 *     on a customer's machine at once.
 *
 *   - Pending Call() requests survive a reconnect: they are not
 *     completed by the disconnect itself, only by a real timeout or
 *     by Close(). On reconnect, the next POST goes out against the
 *     fresh messages endpoint and the response arrives via the new
 *     SSE channel like normal.
 *
 *   - Close() cooperatively unwinds: cancel the ctx, wait for the
 *     listener to acknowledge, fail any still-pending Calls with a
 *     clean error so callers do not hang.
 */
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// Backoff bounds for the SSE supervisor. Tuned for the "MCP server
// restarted; chatcli should pick it up promptly without storming a
// flaky network" use case.
const (
	sseBackoffInitial = 500 * time.Millisecond
	sseBackoffMax     = 30 * time.Second
)

// sseTransport implements mcpTransport over HTTP+SSE.
// Requests are sent via HTTP POST; responses arrive via Server-Sent Events.
type sseTransport struct {
	baseURL    string
	httpClient *http.Client
	nextID     int64

	pendMu  sync.Mutex
	pending map[int64]chan *jsonRPCResponse

	endpointMu  sync.RWMutex
	messagesURL string
	ready       chan struct{} // closed on first endpoint discovery

	logger      *zap.Logger
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	channelMgr  *ChannelManager
	serverName  string
	callTimeout time.Duration
	initTimeout time.Duration
	headers     map[string]string
	auth        *AuthConfig

	closed atomic.Bool
}

// newSSETransport connects to the MCP server SSE endpoint and starts listening.
func newSSETransport(ctx context.Context, cfg ServerConfig, logger *zap.Logger, channelMgr *ChannelManager, serverName string) (*sseTransport, error) {
	tctx, cancel := context.WithCancel(ctx)

	callTimeout := cfg.RequestTimeout()
	initTimeout := cfg.InitializeTimeout()

	t := &sseTransport{
		baseURL: strings.TrimSuffix(cfg.URL, "/"),
		// The HTTP client timeout caps individual POSTs. The SSE
		// listener uses a per-request context (no Timeout on the
		// embedded http.Client) so a long-held GET stays open as
		// long as the supervisor wants it to. Generous timeout
		// here is just a safety net for the POST side.
		httpClient:  &http.Client{Timeout: maxDuration(callTimeout, initTimeout) * 2},
		pending:     make(map[int64]chan *jsonRPCResponse),
		logger:      logger,
		ctx:         tctx,
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

	go t.supervisor()

	// Wait for the server to send the messages endpoint URL on the
	// FIRST attempt. Subsequent disconnects must not block callers.
	select {
	case <-t.ready:
	case <-time.After(initTimeout):
		cancel()
		<-t.done
		return nil, fmt.Errorf("%s", i18n.T("mcp.transport.sse_endpoint_timeout", initTimeout))
	case <-ctx.Done():
		cancel()
		<-t.done
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

// maxDuration returns the greater of a and b.
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// supervisor runs the connect / read / wait-and-retry loop until the
// transport's context is canceled. Each iteration:
//
//  1. Opens GET /sse with the listener context (no client Timeout).
//  2. On success, streams events until EOF or error.
//  3. On disconnect, fails any pending requests would-be-stuck on the
//     dropped stream IF AND ONLY IF the transport is closing —
//     otherwise the requests stay queued because reconnect is
//     imminent and the server will replay them on the new stream.
//  4. Backs off and retries.
//
// done is closed exactly once when ctx fires AND the in-flight
// stream has been torn down, so Close()'s caller can wait on it.
func (t *sseTransport) supervisor() {
	defer close(t.done)
	defer t.failPendingLocked(errors.New("MCP SSE transport closed"))

	backoff := sseBackoffInitial
	for {
		if err := t.ctx.Err(); err != nil {
			return
		}
		err := t.runOnce(t.ctx)
		if t.ctx.Err() != nil {
			return
		}
		if err != nil {
			t.logger.Warn("MCP SSE stream ended; reconnecting",
				zap.String("server", t.serverName),
				zap.Duration("backoff", backoff),
				zap.Error(err))
		}
		// Sleep with cancellation. Full-jitter (random in [0, backoff))
		// flattens the reconnect storm when many clients see the same
		// server bounce.
		wait := time.Duration(rand.Int64N(int64(backoff))) //#nosec G404 -- jitter, not security
		select {
		case <-time.After(wait):
		case <-t.ctx.Done():
			return
		}
		backoff *= 2
		if backoff > sseBackoffMax {
			backoff = sseBackoffMax
		}
	}
}

// runOnce establishes a single SSE connection and reads from it until
// EOF, parse error, or context cancellation. Returns nil only when
// ctx canceled — every other exit path returns an error so the
// supervisor knows to back off.
func (t *sseTransport) runOnce(ctx context.Context) error {
	sseURL := t.baseURL + "/sse"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sseURL, nil)
	if err != nil {
		return fmt.Errorf("sse request build: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	t.applyHeaders(req)

	// Use the package's default Transport so the connection respects
	// HTTP_PROXY / NO_PROXY just like every other HTTP call. The
	// embedded Timeout on httpClient would kill the long-held GET,
	// so we issue this request through a dedicated client without
	// a Timeout field.
	c := &http.Client{Transport: t.httpClient.Transport}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("sse connect: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // streamed body, drained by scanner

	if resp.StatusCode != http.StatusOK {
		// 404/501 are signals that the server does not implement the
		// SSE transport — back off MORE aggressively (the supervisor
		// will retry anyway, but the warn log makes the dead-end
		// visible in /mcp logs).
		return fmt.Errorf("sse bad status: %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var eventType, eventData string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if eventType != "" && eventData != "" {
				t.handleSSEEvent(eventType, eventData)
			}
			eventType, eventData = "", ""
			continue
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if eventData == "" {
				eventData = strings.TrimPrefix(line, "data: ")
			} else {
				eventData += "\n" + strings.TrimPrefix(line, "data: ")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sse stream read: %w", err)
	}
	return io.EOF
}

// handleSSEEvent processes a single SSE event.
func (t *sseTransport) handleSSEEvent(eventType, data string) {
	switch eventType {
	case "endpoint":
		var resolved string
		if strings.HasPrefix(data, "/") {
			resolved = t.baseURL + data
		} else {
			resolved = data
		}
		t.endpointMu.Lock()
		t.messagesURL = resolved
		t.endpointMu.Unlock()
		select {
		case <-t.ready:
		default:
			close(t.ready)
		}

	case "message":
		var resp jsonRPCResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			t.logger.Debug("SSE message parse error",
				zap.String("server", t.serverName),
				zap.Error(err))
			// Forward unparseable payloads to the channel manager so
			// servers that emit free-form JSON on the SSE stream still
			// reach the user.
			if t.channelMgr != nil {
				t.channelMgr.ProcessSSENotification(t.serverName, []byte(data))
			}
			return
		}

		if resp.ID != 0 {
			t.pendMu.Lock()
			ch, ok := t.pending[resp.ID]
			t.pendMu.Unlock()
			if ok {
				ch <- &resp
			}
			return
		}
		if t.channelMgr != nil {
			t.channelMgr.ProcessSSENotification(t.serverName, []byte(data))
		}

	default:
		if t.channelMgr != nil && data != "" {
			t.channelMgr.ProcessSSENotification(t.serverName, []byte(data))
		}
	}
}

// failPendingLocked completes every pending request with err so the
// callers see a clean failure when Close fires. No-op when there are
// no pending requests.
func (t *sseTransport) failPendingLocked(err error) {
	t.pendMu.Lock()
	defer t.pendMu.Unlock()
	for id, ch := range t.pending {
		// Non-blocking send — Call() always allocates a buffer of 1,
		// but defending against a future change here is cheap.
		select {
		case ch <- &jsonRPCResponse{ID: id, Error: &jsonRPCError{Code: -32000, Message: err.Error()}}:
		default:
		}
	}
	t.pending = make(map[int64]chan *jsonRPCResponse)
}

// Call sends a JSON-RPC request via HTTP POST and waits for the SSE response.
func (t *sseTransport) Call(method string, params interface{}) (json.RawMessage, error) {
	if t.closed.Load() {
		return nil, errors.New("MCP SSE transport closed")
	}
	t.endpointMu.RLock()
	messagesURL := t.messagesURL
	t.endpointMu.RUnlock()
	if messagesURL == "" {
		return nil, errors.New("SSE transport not ready — no messages endpoint")
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

	deadline := t.callTimeout
	if method == "initialize" {
		deadline = t.initTimeout
	}
	postCtx, postCancel := context.WithTimeout(t.ctx, deadline)
	defer postCancel()

	httpReq, err := http.NewRequestWithContext(postCtx, http.MethodPost, messagesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	t.applyHeaders(httpReq)

	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		if postCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s", i18n.T("mcp.transport.call_timeout", method, deadline))
		}
		return nil, fmt.Errorf("MCP POST failed: %w", err)
	}
	_, _ = io.Copy(io.Discard, httpResp.Body)
	_ = httpResp.Body.Close()
	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("MCP POST returned %d", httpResp.StatusCode)
	}

	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("MCP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-time.After(deadline):
		return nil, fmt.Errorf("%s", i18n.T("mcp.transport.call_timeout", method, deadline))
	case <-t.ctx.Done():
		return nil, errors.New("MCP SSE transport closed")
	}
}

// Close cancels the SSE supervisor and waits for it to drain.
//
// ctx bounds how long we wait for the listener goroutine to exit
// after cancellation. A well-behaved server tears down within a
// few milliseconds; we honor ctx so a hung TCP connection during
// shutdown cannot stall the whole CLI exit path.
func (t *sseTransport) Close(ctx context.Context) error {
	t.closed.Store(true)
	t.cancel()
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		t.logger.Warn("MCP SSE Close: ctx fired before listener drained",
			zap.Error(ctx.Err()))
		return ctx.Err()
	}
}
