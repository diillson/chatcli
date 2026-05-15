/*
 * ChatCLI - MCP Streamable HTTP transport
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements the MCP "Streamable HTTP" transport introduced by the
 * 2025-03-26 spec revision (replacement for the older HTTP+SSE pair).
 *
 * Wire shape:
 *
 *   - Single endpoint (e.g. https://server/mcp). Each JSON-RPC
 *     request is a POST to that endpoint with
 *     `Accept: text/event-stream, application/json` (SSE first so
 *     strict-first servers do not reject with HTTP 406).
 *
 *   - The server responds with EITHER:
 *       * Content-Type: application/json — single response body, or
 *       * Content-Type: text/event-stream — SSE stream that
 *         contains the response (and possibly server-initiated
 *         notifications interleaved before it), or
 *       * Status 202 Accepted with empty body — typical for
 *         JSON-RPC notifications such as notifications/initialized.
 *
 *   - On the response to the first `initialize` call the server MAY
 *     return a `Mcp-Session-Id` header; if present the client must
 *     echo it on every subsequent request so the server can route
 *     to the same session.
 *
 * Server-initiated push (GET on the same endpoint, optional per
 * spec) is NOT implemented yet — most servers in the wild don't
 * support it, and adding it would require a long-lived listener
 * goroutine analogous to the SSE transport. Filed as a follow-up.
 */
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

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// maxHTTPResponseBody caps how much body we will read from a single
// JSON response. Streaming responses (SSE) are bounded by the
// per-call context deadline instead. 10 MiB is well past any sane
// MCP payload while still preventing a misbehaving server from
// driving us OOM.
const maxHTTPResponseBody = 10 << 20

// httpTransport implements mcpTransport over the MCP 2025-03-26
// Streamable HTTP transport. Each Call is a self-contained POST,
// so there is no background listener goroutine — the only shared
// state across calls is the optional Mcp-Session-Id and the
// underlying *http.Client.
type httpTransport struct {
	endpoint   string
	httpClient *http.Client
	nextID     int64
	logger     *zap.Logger
	ctx        context.Context
	cancel     context.CancelFunc

	// sessionID is set after the initialize response if the server
	// returned a Mcp-Session-Id header. RWMutex over sync.Map keeps
	// the read path (every Call) lock-free in the common case where
	// the value never changes after init.
	sessionID string
	sessionMu sync.RWMutex

	channelMgr  *ChannelManager
	serverName  string
	callTimeout time.Duration
	initTimeout time.Duration
	headers     map[string]string
	auth        *AuthConfig

	closed atomic.Bool
}

// newHTTPTransport constructs the transport. Unlike SSE, there is no
// upfront handshake — the first Call (initialize) doubles as the
// connection probe, so any auth/URL error surfaces synchronously to
// the manager's startServer path.
func newHTTPTransport(ctx context.Context, cfg ServerConfig, logger *zap.Logger, channelMgr *ChannelManager, serverName string) (*httpTransport, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("%s", i18n.T("mcp.transport.http_url_required"))
	}

	tctx, cancel := context.WithCancel(ctx)
	callTimeout := cfg.RequestTimeout()
	initTimeout := cfg.InitializeTimeout()

	t := &httpTransport{
		// Preserve the URL path exactly as configured (whitespace
		// trimmed). Trailing slash is SIGNIFICANT in HTTP path
		// semantics — some MCP servers (FastMCP, ASGI mounts behind
		// Starlette/FastAPI routers) answer only on /mcp/ and
		// 307-redirect /mcp → /mcp/. Go's http.Client does follow
		// 307 with POST when GetBody is set, but corporate proxies
		// that sit between the client and the server frequently
		// drop the body or custom headers across the redirect,
		// manifesting as a 10s deadline hang on initialize.
		// Stripping the slash here would force that redirect path
		// on every call; preserving it lets the operator point
		// straight at the canonical URL.
		endpoint: strings.TrimSpace(cfg.URL),
		// The http.Client timeout caps the entire request lifetime
		// including streaming SSE bodies. We size it at max(call,
		// init) so a slow tools/call doesn't get killed by a short
		// initialize budget, and an initialize that legitimately
		// takes longer than callTimeout still completes.
		httpClient:  &http.Client{Timeout: maxDuration(callTimeout, initTimeout)},
		logger:      logger,
		ctx:         tctx,
		cancel:      cancel,
		channelMgr:  channelMgr,
		serverName:  serverName,
		callTimeout: callTimeout,
		initTimeout: initTimeout,
		headers:     cfg.ResolveHeaders(),
		auth:        cfg.Auth,
	}

	logger.Debug("MCP streamable HTTP transport ready",
		zap.String("server", serverName),
		zap.String("endpoint", t.endpoint))

	return t, nil
}

// Call sends one JSON-RPC request and returns its result. The wire
// layer (POST + response decode) is shared with notifications — for
// notification-style methods the server returns 202 with no body, in
// which case we return (nil, nil) so the manager's fire-and-forget
// callers (notifications/initialized) treat it as success.
func (t *httpTransport) Call(method string, params interface{}) (json.RawMessage, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("%s", i18n.T("mcp.transport.http_closed"))
	}

	id := atomic.AddInt64(&t.nextID, 1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// Per-call deadline. initialize gets the larger initTimeout
	// because credential bootstrap and cold starts often blow past
	// the per-call budget.
	deadline := t.callTimeout
	if method == "initialize" {
		deadline = t.initTimeout
	}
	reqCtx, cancel := context.WithTimeout(t.ctx, deadline)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Order matters in the wild. The 2025-03-26 spec just requires
	// both media types to be listed and a substring/`includes` check
	// on the server is enough — but several real implementations
	// (FastMCP, some Cloudflare Workers MCP gateways) interpret the
	// FIRST listed type as the client's preference and return HTTP
	// 406 / JSON-RPC -32600 ("Not Acceptable: Client must Accept
	// text/event-stream") when SSE is not first. Putting SSE first
	// is compatible with both naive first-match and spec-correct
	// includes-match servers.
	httpReq.Header.Set("Accept", "text/event-stream, application/json")
	t.applyHeaders(httpReq)
	t.attachSession(httpReq)

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		// Distinguish context-driven cancellation from a real
		// network error so the caller sees a localized timeout
		// message instead of a raw "context deadline exceeded".
		if reqCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s", i18n.T("mcp.transport.call_timeout", method, deadline))
		}
		return nil, fmt.Errorf("%s: %w", i18n.T("mcp.transport.http_post_failed"), err)
	}
	defer resp.Body.Close()

	// Capture session ID on the initialize response. The spec says
	// the server MAY issue one at any time, but in practice it's
	// always on the initialize reply — checking on every response
	// keeps us correct without an extra branch on method name.
	if sid := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); sid != "" {
		t.setSession(sid)
	}

	if resp.StatusCode == http.StatusAccepted {
		// 202 with no body — JSON-RPC notification path.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	if resp.StatusCode >= 400 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s", i18n.T("mcp.transport.http_status_error", resp.StatusCode, strings.TrimSpace(string(preview))))
	}

	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	switch contentType {
	case "text/event-stream":
		return t.readSSEResponse(resp.Body, id, method, deadline)
	case "application/json", "":
		// Empty Content-Type is treated as JSON for resilience with
		// servers that omit the header on small responses.
		return t.readJSONResponse(resp.Body, id)
	default:
		return nil, fmt.Errorf("%s", i18n.T("mcp.transport.http_bad_content_type", contentType))
	}
}

// readJSONResponse decodes a single JSON-RPC response from the body.
// Bounded by maxHTTPResponseBody to keep a misbehaving server from
// streaming us into OOM.
func (t *httpTransport) readJSONResponse(body io.Reader, expectedID int64) (json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxHTTPResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("read MCP HTTP response: %w", err)
	}
	if int64(len(raw)) > maxHTTPResponseBody {
		return nil, fmt.Errorf("%s", i18n.T("mcp.transport.http_body_too_large", maxHTTPResponseBody))
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse MCP HTTP response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s", i18n.T("mcp.transport.rpc_error", resp.Error.Code, resp.Error.Message))
	}
	if resp.ID != expectedID {
		t.logger.Debug("MCP HTTP response id mismatch",
			zap.String("server", t.serverName),
			zap.Int64("expected", expectedID),
			zap.Int64("got", resp.ID))
	}
	return resp.Result, nil
}

// readSSEResponse scans the SSE body for the JSON-RPC response that
// matches expectedID. Any messages without IDs (or with mismatching
// IDs that look like server-initiated notifications) are forwarded
// to the channel manager so subscribers still see them.
//
// The deadline is enforced by the per-call context the caller wired
// into the request, so we don't need a separate timer here — when
// the deadline fires the scanner observes EOF on the canceled body.
func (t *httpTransport) readSSEResponse(body io.Reader, expectedID int64, method string, deadline time.Duration) (json.RawMessage, error) {
	scanner := bufio.NewScanner(body)
	// Generous buffer so a single large JSON payload encoded as one
	// SSE event doesn't trip the default 64KB scanner limit.
	scanner.Buffer(make([]byte, 0, 64*1024), maxHTTPResponseBody)

	var eventType, eventData string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if eventData != "" {
				if resp, done := t.dispatchSSEEvent(eventType, eventData, expectedID); done {
					return resp.Result, resp.err()
				}
			}
			eventType, eventData = "", ""
			continue
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			// Per SSE spec, multi-line data fields concatenate with
			// '\n'. Most MCP servers emit single-line JSON so the
			// simple append is correct; the rare multi-line case
			// still round-trips.
			if eventData == "" {
				eventData = strings.TrimPrefix(line, "data: ")
			} else {
				eventData += "\n" + strings.TrimPrefix(line, "data: ")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("mcp.transport.http_sse_read_failed"), err)
	}
	// Stream closed before we saw the response for our ID.
	return nil, fmt.Errorf("%s", i18n.T("mcp.transport.http_stream_closed", method, deadline))
}

// sseResult holds either a JSON-RPC result or an error so the inner
// dispatch loop can signal both via a single return.
type sseResult struct {
	Result json.RawMessage
	Err    error
}

func (r sseResult) err() error { return r.Err }

// dispatchSSEEvent processes one decoded SSE event. Returns done=true
// when the event carries the response to expectedID; the caller then
// stops scanning. Events without IDs (or with mismatching IDs) are
// routed to the channel manager.
func (t *httpTransport) dispatchSSEEvent(eventType, data string, expectedID int64) (sseResult, bool) {
	var resp jsonRPCResponse
	if err := json.Unmarshal([]byte(data), &resp); err != nil {
		// Not a JSON-RPC envelope — could be a vendor-specific
		// notification. Forward verbatim and keep scanning.
		if t.channelMgr != nil {
			t.channelMgr.ProcessSSENotification(t.serverName, []byte(data))
		}
		t.logger.Debug("MCP HTTP SSE non-JSON-RPC event",
			zap.String("server", t.serverName),
			zap.String("event", eventType))
		return sseResult{}, false
	}
	if resp.ID == expectedID {
		if resp.Error != nil {
			return sseResult{Err: fmt.Errorf("%s", i18n.T("mcp.transport.rpc_error", resp.Error.Code, resp.Error.Message))}, true
		}
		return sseResult{Result: resp.Result}, true
	}
	// Different ID or no ID → notification. Forward to channels.
	if t.channelMgr != nil {
		t.channelMgr.ProcessSSENotification(t.serverName, []byte(data))
	}
	return sseResult{}, false
}

// applyHeaders mirrors the SSE transport: custom headers first, then
// auth. The order matches so a user who sets Authorization in
// Headers (legacy) is still overridden by Auth (typed).
func (t *httpTransport) applyHeaders(req *http.Request) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.auth.ApplyAuth(req)
}

func (t *httpTransport) attachSession(req *http.Request) {
	t.sessionMu.RLock()
	sid := t.sessionID
	t.sessionMu.RUnlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
}

func (t *httpTransport) setSession(sid string) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	if t.sessionID == sid {
		return
	}
	if t.sessionID != "" {
		t.logger.Warn("MCP session rotated",
			zap.String("server", t.serverName),
			zap.String("old", t.sessionID),
			zap.String("new", sid))
	}
	t.sessionID = sid
}

// Close cancels in-flight requests and marks the transport as
// closed. Idempotent — safe to call from manager teardown plus a
// deferred cleanup in startHTTPServer's error path.
//
// ctx scopes the courtesy DELETE that releases the server-side
// session. The caller's ctx may already be canceled (it usually
// is during shutdown), so we derive a fresh deadline from it via
// context.WithoutCancel + WithTimeout — that preserves request-
// scoped values (auth, tracing) while giving the DELETE a real
// chance to land. We cap the DELETE at 2 seconds regardless of
// caller-supplied deadline; this is cleanup, not a payload call.
func (t *httpTransport) Close(ctx context.Context) error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	t.cancel()
	t.sessionMu.RLock()
	sid := t.sessionID
	t.sessionMu.RUnlock()
	if sid == "" {
		return nil
	}
	delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	t.sendSessionDelete(delCtx, sid)
	return nil
}

// sendSessionDelete fires a DELETE with the session header so the
// server can release per-session state immediately. Takes the
// context as a parameter so the deadline is callsite-controlled.
// All errors are ignored on purpose; the server-side session times
// out anyway and this is purely a courtesy to the remote side.
func (t *httpTransport) sendSessionDelete(ctx context.Context, sid string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", sid)
	t.applyHeaders(req)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		t.logger.Debug("MCP session DELETE failed",
			zap.String("server", t.serverName),
			zap.Error(err))
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
