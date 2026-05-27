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

// Errf builds an RPCError.
func Errf(code int, format string, args ...interface{}) *RPCError {
	return &RPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Handler processes a method call. For notifications the return values are
// ignored. Returning a non-nil *RPCError sends an error response.
type Handler func(ctx context.Context, method string, params json.RawMessage) (interface{}, *RPCError)

// Server is a newline-delimited JSON-RPC server over an io pair.
type Server struct {
	in      io.Reader
	out     io.Writer
	handler Handler
	writeMu sync.Mutex
}

// NewServer builds a server reading requests from in and writing to out.
func NewServer(in io.Reader, out io.Writer, handler Handler) *Server {
	return &Server{in: in, out: out, handler: handler}
}

// Serve reads and dispatches messages until ctx is cancelled or in hits EOF.
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
		s.dispatch(ctx, line)
	}
	return scanner.Err()
}

func (s *Server) dispatch(ctx context.Context, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = s.write(Response{JSONRPC: "2.0", Error: Errf(CodeParseError, "parse error: %v", err)})
		return
	}
	result, rpcErr := s.handler(ctx, req.Method, req.Params)
	if req.IsNotification() {
		return // notifications get no response
	}
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	_ = s.write(resp)
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
