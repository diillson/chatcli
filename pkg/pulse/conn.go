/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

// Outbound HTTP on the live dashboard. Each host is one node. The metering
// lives here, in the stdlib-only leaf, because the clients that need it sit
// in every layer — including packages the shared HTTP helpers themselves
// import, which could never import those helpers back.
//
// Only the hostname, the method, the status code and byte counts leave the
// process. Never the path or the query string — that is where keys and
// tokens travel — and never a header or a body.

// ConnSpan measures one outbound request. A nil ConnSpan does nothing, which
// is what BeginConn returns while the dashboard is off.
type ConnSpan struct {
	span *Span
}

// BeginConn opens the span of a request about to be sent.
func BeginConn(req *http.Request) *ConnSpan {
	if !Enabled() || req == nil || req.URL == nil {
		return nil
	}
	host := req.URL.Hostname()
	if host == "" {
		return nil
	}
	span := Begin(KindConn, host, "").With("method", req.Method)
	if req.ContentLength > 0 {
		span = span.With("req_bytes", strconv.FormatInt(req.ContentLength, 10))
	}
	return &ConnSpan{span: span}
}

// Fail closes the span of a request that never got a response.
func (c *ConnSpan) Fail(err error) {
	if c == nil {
		return
	}
	c.span.EndErr(err)
}

// Finish closes the span of a fully received response.
func (c *ConnSpan) Finish(statusCode int, respBytes int64) {
	if c == nil {
		return
	}
	c.span.With("status", strconv.Itoa(statusCode)).With("resp_bytes", strconv.FormatInt(respBytes, 10)).End(ConnStatus(statusCode))
}

// ConnStatus maps an HTTP status code onto a bus status.
func ConnStatus(statusCode int) string {
	if statusCode >= http.StatusBadRequest {
		return StatusError
	}
	return StatusOK
}

// Stream keeps the span open for as long as a body is being read and closes
// it when the body ends or is closed, so a long token stream or a large
// download shows as one live connection with its true duration and size,
// without ever being buffered.
func (c *ConnSpan) Stream(statusCode int, body io.ReadCloser) io.ReadCloser {
	if c == nil || body == nil {
		return body
	}
	return &meteredBody{ReadCloser: body, conn: c, statusCode: statusCode}
}

type meteredBody struct {
	io.ReadCloser
	conn       *ConnSpan
	statusCode int
	n          atomic.Int64
	once       sync.Once
}

// done reports the connection once. The reader hitting EOF and the owner
// calling Close may be different goroutines.
func (b *meteredBody) done() {
	b.once.Do(func() { b.conn.Finish(b.statusCode, b.n.Load()) })
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

// MeterTransport wraps a transport so its requests show on the live
// dashboard. A nil base means http.DefaultTransport. The body of every
// response is metered as it is read.
func MeterTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &meterTransport{base: base}
}

type meterTransport struct {
	base http.RoundTripper
}

func (m *meterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	conn := BeginConn(req)
	resp, err := m.base.RoundTrip(req)
	if err != nil || resp == nil {
		conn.Fail(err)
		return resp, err
	}
	resp.Body = conn.Stream(resp.StatusCode, resp.Body)
	return resp, nil
}
