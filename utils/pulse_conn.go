/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for outbound HTTP. Every client built by NewHTTPClient* has
 * the logging transport on top, so measuring there sees every connection to
 * a provider, an auth server or a web tool target. Each host is one node on
 * the dashboard.
 *
 * Only the hostname, the method, the status code and byte counts leave the
 * process. Never the path or the query string — that is where keys and
 * tokens travel — and never a header or a body.
 */
package utils

import (
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/diillson/chatcli/pkg/pulse"
)

// connSpan measures one outbound request. A nil connSpan does nothing, which
// is what beginConnSpan returns while the dashboard is off.
type connSpan struct {
	span *pulse.Span
}

// beginConnSpan opens the span of a request about to be sent.
func beginConnSpan(req *http.Request) *connSpan {
	if !pulse.Enabled() || req == nil || req.URL == nil {
		return nil
	}
	host := req.URL.Hostname()
	if host == "" {
		return nil
	}
	span := pulse.Begin(pulse.KindConn, host, "").With("method", req.Method)
	if req.ContentLength > 0 {
		span = span.With("req_bytes", strconv.FormatInt(req.ContentLength, 10))
	}
	return &connSpan{span: span}
}

// fail closes the span of a request that never got a response.
func (c *connSpan) fail(err error) {
	if c == nil {
		return
	}
	c.span.EndErr(err)
}

// finish closes the span of a fully received response.
func (c *connSpan) finish(statusCode int, respBytes int64) {
	if c == nil {
		return
	}
	c.span.With("status", strconv.Itoa(statusCode)).With("resp_bytes", strconv.FormatInt(respBytes, 10)).End(connStatus(statusCode))
}

// connStatus maps an HTTP status code onto a bus status.
func connStatus(statusCode int) string {
	if statusCode >= http.StatusBadRequest {
		return pulse.StatusError
	}
	return pulse.StatusOK
}

// stream keeps the span open for as long as a streamed body is being read
// and closes it when the body ends or is closed, so a long token stream
// shows as one live connection with its true duration and size.
func (c *connSpan) stream(statusCode int, body io.ReadCloser) io.ReadCloser {
	if c == nil || body == nil {
		return body
	}
	return &meteredBody{ReadCloser: body, conn: c, statusCode: statusCode}
}

type meteredBody struct {
	io.ReadCloser
	conn       *connSpan
	statusCode int
	n          atomic.Int64
	once       sync.Once
}

// done reports the connection once. The reader hitting EOF and the owner
// calling Close may be different goroutines.
func (b *meteredBody) done() {
	b.once.Do(func() { b.conn.finish(b.statusCode, b.n.Load()) })
}

func (b *meteredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	if err != nil {
		b.done() // EOF or a broken stream: the connection is over
	}
	return n, err
}

func (b *meteredBody) Close() error {
	b.done()
	return b.ReadCloser.Close()
}
