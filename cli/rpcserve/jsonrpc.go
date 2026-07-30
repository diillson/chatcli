/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Package rpcserve implements newline-delimited JSON-RPC 2.0 over stdio and
 * builds two protocol servers on top of it:
 *
 *   - MCP (Model Context Protocol): exposes ChatCLI's capabilities as tools to
 *     any MCP client (Claude Desktop, IDEs). ChatCLI is already an MCP client;
 *     this makes it an MCP server too.
 *   - ACP (Agent Client Protocol): lets editors such as Zed drive ChatCLI as
 *     an agent over stdio.
 *
 * The transport is dependency-free: each JSON-RPC message is a single line on
 * stdin/stdout, which is the MCP stdio framing and is equally usable for ACP.
 */
package rpcserve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Request is an incoming JSON-RPC request or notification (no ID).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the request carries no id (no reply expected).
func (r Request) IsNotification() bool { return len(r.ID) == 0 }

// Response is an outgoing JSON-RPC response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

// errf builds an RPCError.
func errf(code int, format string, args ...interface{}) *RPCError {
	return &RPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// handlerFunc processes a method call. For notifications the return values are
// ignored. Returning a non-nil *RPCError sends an error response.
type handlerFunc func(ctx context.Context, method string, params json.RawMessage) (interface{}, *RPCError)

// PostReplyResult wraps a handler result with a function the server runs
// AFTER the response is written. Handlers use it when a follow-up
// notification must be ordered behind the response on the wire (e.g. ACP's
// available_commands_update after session/new).
type PostReplyResult struct {
	Result interface{}
	Post   func()
}

// Server is a newline-delimited JSON-RPC server over an io pair.
type Server struct {
	in      io.Reader
	out     io.Writer
	handler handlerFunc
	writeMu sync.Mutex
	wg      sync.WaitGroup

	// Outgoing server→client requests: id sequence and pending responses.
	// inClosed marks the read loop's exit — no client response can arrive
	// anymore, so new requests fail fast instead of blocking.
	reqSeq    atomic.Int64
	pendingMu sync.Mutex
	pending   map[string]chan Response
	inClosed  bool
}

// NewServer builds a server reading requests from in and writing to out.
func NewServer(in io.Reader, out io.Writer, handler handlerFunc) *Server {
	return &Server{in: in, out: out, handler: handler, pending: map[string]chan Response{}}
}

// Serve reads and dispatches messages until ctx is canceled or in hits EOF.
func (s *Server) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // allow large payloads

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Dispatch concurrently: a long-running call (agent_task, a slow
		// tool) must not block the read loop, or cancellation
		// notifications could never arrive while a turn is in flight.
		// scanner.Bytes() reuses its buffer across Scan calls, so the
		// line is copied before it escapes to the goroutine. Response
		// writes are serialized by writeMu; heavyweight runs serialize
		// on their own backend lock.
		owned := append([]byte(nil), line...)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.dispatch(ctx, owned)
		}()
	}
	// Input is gone: no client response can ever arrive. Fail the pending
	// server→client requests so no dispatch wedges wg.Wait forever — but do
	// NOT cancel the dispatches themselves: piped one-shot usage legally
	// hits EOF while prompts are still finishing.
	s.failPending()
	s.wg.Wait()
	return scanner.Err()
}

// failPending unblocks every waiter registered by Request with an error and
// marks the input closed so later Request calls fail fast.
func (s *Server) failPending() {
	s.pendingMu.Lock()
	s.inClosed = true
	pend := s.pending
	s.pending = map[string]chan Response{}
	s.pendingMu.Unlock()
	for _, ch := range pend {
		ch <- Response{JSONRPC: "2.0", Error: errf(CodeInternalError, "client disconnected before responding")}
	}
}

func (s *Server) dispatch(ctx context.Context, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = s.write(Response{JSONRPC: "2.0", Error: errf(CodeParseError, "parse error: %v", err)})
		return
	}
	// A message with an id, no method, and a result/error member is the
	// client's RESPONSE to a server-initiated request (Server.Request):
	// route it to the waiter instead of the handler. A method-less frame
	// WITHOUT result/error stays a malformed request (invalid request per
	// JSON-RPC 2.0) so broken clients still get a diagnostic.
	if req.Method == "" && !req.IsNotification() {
		var probe struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(line, &probe)
		if len(probe.Result) > 0 || len(probe.Error) > 0 {
			s.routeClientResponse(line, req.ID)
			return
		}
		_ = s.write(Response{JSONRPC: "2.0", ID: req.ID, Error: errf(CodeInvalidRequest, "missing method")})
		return
	}
	result, rpcErr := s.handler(ctx, req.Method, req.Params)
	if req.IsNotification() {
		return // notifications get no response
	}
	var post func()
	if pr, ok := result.(PostReplyResult); ok {
		result = pr.Result
		post = pr.Post
	}
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	_ = s.write(resp)
	if post != nil && rpcErr == nil {
		post()
	}
}

// routeClientResponse delivers a client response line to the Request waiter
// registered under its id. Unknown ids are dropped (late/duplicate replies).
func (s *Server) routeClientResponse(line []byte, id json.RawMessage) {
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return
	}
	key := string(id)
	s.pendingMu.Lock()
	ch, ok := s.pending[key]
	if ok {
		delete(s.pending, key)
	}
	s.pendingMu.Unlock()
	if ok {
		ch <- resp // buffered(1); never blocks
	}
}

// Request sends a server-initiated request to the client and blocks until the
// client responds or ctx is done. Used by ACP's session/request_permission.
func (s *Server) Request(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf(`"srv-%d"`, s.reqSeq.Add(1))
	ch := make(chan Response, 1)
	s.pendingMu.Lock()
	if s.inClosed {
		s.pendingMu.Unlock()
		return nil, errf(CodeInternalError, "client disconnected")
	}
	s.pending[id] = ch
	s.pendingMu.Unlock()

	if err := s.write(Request{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		out, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, err
		}
		return out, nil
	case <-ctx.Done():
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return nil, ctx.Err()
	}
}

// Notify sends a server-initiated notification (no id).
func (s *Server) Notify(method string, params interface{}) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return s.write(Request{JSONRPC: "2.0", Method: method, Params: raw})
}

// write serializes v and writes it as one line. Concurrency-safe.
func (s *Server) write(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(data); err != nil {
		return err
	}
	_, err = s.out.Write([]byte("\n"))
	return err
}
