/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerproto

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// MaxMessageBytes bounds one framed message. Chat payloads carry whole
// conversations (and base64 images), so the cap is generous, but a peer
// that never sends a newline cannot grow the reader without limit.
const MaxMessageBytes = 32 << 20

// maxAbandoned bounds the set of cancelled call ids awaiting a late reply.
const maxAbandoned = 1024

// writeQueue is how many framed messages may wait for the writer.
const writeQueue = 64

// flushTimeout bounds how long Run waits for queued messages to be written
// at shutdown, so a peer that stopped reading cannot hang the close.
const flushTimeout = 2 * time.Second

// DefaultMaxInflight bounds concurrently handled incoming requests; excess
// requests wait for a slot (back-pressure) instead of spawning goroutines
// without limit.
const DefaultMaxInflight = 32

// ErrClosed is returned by calls on a connection that has shut down.
var ErrClosed = errors.New("providerproto: connection closed")

// Request is an incoming request or notification.
type Request struct {
	Method string
	Params json.RawMessage
	// ID is nil for notifications.
	ID json.RawMessage
}

// IsNotification reports whether the peer expects no response.
func (r *Request) IsNotification() bool { return len(r.ID) == 0 }

// Handler serves incoming messages. For notifications the returned values
// are ignored. Returning an *Error sends it verbatim; any other error is
// reported as ErrCodeInternal.
//
// Notifications are handled inline, in arrival order, so a chat/delta is
// always observed before the chat/send result that follows it; a
// notification handler must therefore return quickly (hand work off to a
// channel). Requests run concurrently, bounded by Options.MaxInflight.
type Handler func(ctx context.Context, req *Request) (json.RawMessage, error)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	requestSeqKey
)

// RequestIDFromContext returns the JSON-RPC id of the request a handler is
// serving (for chat/delta's RequestID), or nil.
func RequestIDFromContext(ctx context.Context) json.RawMessage {
	id, _ := ctx.Value(requestIDKey).(json.RawMessage)
	return id
}

// RequestSeqFromContext returns the arrival order of the message a handler
// is serving (1-based, assigned synchronously by the reader), or 0. Requests
// run concurrently, so this — not the moment a handler starts — is what
// tells "received before shutdown" from "received after".
func RequestSeqFromContext(ctx context.Context) uint64 {
	seq, _ := ctx.Value(requestSeqKey).(uint64)
	return seq
}

// Options tunes a Conn.
type Options struct {
	MaxInflight int
	// OnProtocolError observes malformed input that was skipped.
	OnProtocolError func(error)
}

// Conn is one end of a newline-delimited JSON-RPC 2.0 session.
type Conn struct {
	r       io.Reader
	w       io.Writer
	handler Handler
	opts    Options

	// out feeds the single writer goroutine, so a peer that stops reading
	// can stall only that goroutine: callers enqueue with ctx/close
	// awareness instead of blocking forever inside Write.
	out    chan []byte
	nextID atomic.Int64
	seq    uint64 // arrival counter, touched only by the reader loop

	mu       sync.Mutex
	pending  map[string]chan *wireIn
	inflight map[string]context.CancelFunc
	// abandoned remembers ids of calls cancelled locally, so the peer's late
	// reply (usually ErrCodeCancelled) is dropped quietly instead of being
	// reported as a protocol error. Bounded by maxAbandoned.
	abandoned map[string]struct{}
	closed    bool
	closeErr  error

	sem        chan struct{}
	done       chan struct{}
	writerDone chan struct{}
	wg         sync.WaitGroup
}

type wireIn struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type wireRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  any             `json:"params,omitempty"`
}

type wireResult struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
}

type wireError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *Error          `json:"error"`
}

// NewConn wires a connection; call Run to start reading.
func NewConn(r io.Reader, w io.Writer, handler Handler, opts Options) *Conn {
	if opts.MaxInflight <= 0 {
		opts.MaxInflight = DefaultMaxInflight
	}
	if handler == nil {
		handler = func(context.Context, *Request) (json.RawMessage, error) {
			return nil, NewError(ErrCodeMethodNotFound, "no handler")
		}
	}
	c := &Conn{
		r:          r,
		w:          w,
		handler:    handler,
		opts:       opts,
		out:        make(chan []byte, writeQueue),
		pending:    map[string]chan *wireIn{},
		inflight:   map[string]context.CancelFunc{},
		abandoned:  map[string]struct{}{},
		sem:        make(chan struct{}, opts.MaxInflight),
		done:       make(chan struct{}),
		writerDone: make(chan struct{}),
	}
	go c.writeLoop()
	return c
}

func (c *Conn) writeLoop() {
	defer close(c.writerDone)
	for {
		select {
		case b := <-c.out:
			if _, err := c.w.Write(b); err != nil {
				c.shutdown(fmt.Errorf("providerproto: write: %w", err))
				return
			}
		case <-c.done:
			// Flush what was queued before the close (error replies to the
			// last requests, a final notification) — best effort.
			for {
				select {
				case b := <-c.out:
					if _, err := c.w.Write(b); err != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// Done is closed once the connection stops.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns why the connection stopped (nil while running or on EOF).
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

// Run reads until EOF, a fatal framing error or ctx cancellation, then
// fails every pending call with ErrClosed and waits for handlers to return.
func (c *Conn) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sc := bufio.NewScanner(c.r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxMessageBytes)

	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
		scanErr <- sc.Err()
	}()

	var runErr error
loop:
	for {
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
			break loop
		case line, ok := <-lines:
			if !ok {
				select {
				case runErr = <-scanErr:
				default:
				}
				break loop
			}
			c.dispatch(ctx, line)
		}
	}

	if errors.Is(runErr, bufio.ErrTooLong) {
		runErr = fmt.Errorf("providerproto: message exceeds %d bytes: %w", MaxMessageBytes, runErr)
	}
	c.shutdown(runErr)
	cancel()
	c.wg.Wait()
	select {
	case <-c.writerDone:
	case <-time.After(flushTimeout):
	}
	return runErr
}

func (c *Conn) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	pending := c.pending
	c.pending = map[string]chan *wireIn{}
	for _, cancel := range c.inflight {
		cancel()
	}
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	close(c.done)
}

func (c *Conn) protocolError(err error) {
	if c.opts.OnProtocolError != nil {
		c.opts.OnProtocolError(err)
	}
}

func (c *Conn) dispatch(ctx context.Context, line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var msg wireIn
	if err := json.Unmarshal(line, &msg); err != nil {
		c.protocolError(fmt.Errorf("providerproto: invalid JSON: %w", err))
		_ = c.writeError(nil, NewError(ErrCodeParse, "invalid JSON"))
		return
	}
	if msg.JSONRPC != "2.0" {
		c.protocolError(fmt.Errorf("providerproto: jsonrpc %q, want 2.0", msg.JSONRPC))
		if msg.Method != "" && len(msg.ID) > 0 {
			_ = c.writeError(msg.ID, NewError(ErrCodeInvalidRequest, "jsonrpc must be \"2.0\""))
		}
		return
	}

	if msg.Method == "" {
		c.deliverResponse(&msg)
		return
	}
	if msg.Method == MethodCancelRequest {
		var p struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(msg.Params, &p) == nil {
			c.mu.Lock()
			if cancel, ok := c.inflight[idKey(p.ID)]; ok {
				cancel()
			}
			c.mu.Unlock()
		}
		return
	}
	c.serve(ctx, &Request{Method: msg.Method, Params: msg.Params, ID: msg.ID})
}

func (c *Conn) deliverResponse(msg *wireIn) {
	key := idKey(msg.ID)
	c.mu.Lock()
	ch, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	_, late := c.abandoned[key]
	delete(c.abandoned, key)
	c.mu.Unlock()
	if late {
		return
	}
	if !ok {
		c.protocolError(fmt.Errorf("providerproto: response for unknown id %s", msg.ID))
		return
	}
	ch <- msg
}

func (c *Conn) serve(ctx context.Context, req *Request) {
	c.seq++
	ctx = context.WithValue(ctx, requestSeqKey, c.seq)
	if req.IsNotification() {
		_, _ = c.callHandler(ctx, req)
		return
	}
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	reqCtx, cancel := context.WithCancel(ctx)
	key := idKey(req.ID)
	reqCtx = context.WithValue(reqCtx, requestIDKey, req.ID)
	c.mu.Lock()
	c.inflight[key] = cancel
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() { <-c.sem }()
		defer cancel()
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()
		}()

		result, err := c.callHandler(reqCtx, req)
		if err != nil {
			var perr *Error
			if !errors.As(err, &perr) {
				if errors.Is(reqCtx.Err(), context.Canceled) {
					perr = NewError(ErrCodeCancelled, "request cancelled")
				} else {
					perr = NewError(ErrCodeInternal, fmt.Sprintf("%v", err))
				}
			}
			_ = c.writeError(req.ID, perr)
			return
		}
		if len(result) == 0 {
			result = json.RawMessage("null")
		}
		_ = c.write(context.Background(), wireResult{JSONRPC: "2.0", ID: req.ID, Result: result})
	}()
}

// callHandler converts a handler panic into an internal error so one bad
// request cannot take the whole session down.
func (c *Conn) callHandler(ctx context.Context, req *Request) (result json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = NewError(ErrCodeInternal, fmt.Sprintf("handler panic: %v", r))
		}
	}()
	return c.handler(ctx, req)
}

// Call sends a request and decodes its result into out (which may be nil).
// Cancelling ctx sends $/cancelRequest to the peer and returns ctx.Err().
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	return c.CallObserved(ctx, method, params, out, nil)
}

// CallObserved is Call that first hands the request's id to onID (when not
// nil), before the request is written — so notifications the peer sends
// for this request (chat/delta carries it as requestId) can be routed even
// if they arrive before Call would return.
func (c *Conn) CallObserved(ctx context.Context, method string, params, out any, onID func(json.RawMessage)) error {
	id := json.RawMessage(strconv.FormatInt(c.nextID.Add(1), 10))
	if onID != nil {
		onID(id)
	}
	key := idKey(id)
	ch := make(chan *wireIn, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.pending[key] = ch
	c.mu.Unlock()

	if err := c.write(ctx, wireRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		return err
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return errorFromRaw(msg.Error)
		}
		if out == nil || len(msg.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(msg.Result, out); err != nil {
			return fmt.Errorf("providerproto: decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, key)
		if len(c.abandoned) >= maxAbandoned {
			c.abandoned = map[string]struct{}{}
		}
		c.abandoned[key] = struct{}{}
		c.mu.Unlock()
		_ = c.Notify(context.Background(), MethodCancelRequest, CancelParams{ID: id})
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}
}

// Notify sends a notification.
func (c *Conn) Notify(ctx context.Context, method string, params any) error {
	return c.write(ctx, wireRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Conn) writeError(id json.RawMessage, e *Error) error {
	if id == nil {
		id = json.RawMessage("null")
	}
	return c.write(context.Background(), wireError{JSONRPC: "2.0", ID: id, Error: e})
}

// write frames one message and queues it for the writer. json.Marshal never
// emits a raw newline (it escapes them inside strings), so one message is
// always one line.
func (c *Conn) write(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("providerproto: encode: %w", err)
	}
	if len(b)+1 > MaxMessageBytes {
		return fmt.Errorf("providerproto: message of %d bytes exceeds %d", len(b), MaxMessageBytes)
	}
	b = append(b, '\n')
	select {
	case c.out <- b:
		return nil
	case <-c.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// idKey normalizes an id for map lookups (1 and 1.0 aside, peers echo ids
// byte-for-byte; trimming covers incidental whitespace).
func idKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}
