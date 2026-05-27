/*
 * Package lsp is a minimal Language Server Protocol client. It spawns a
 * language server (gopls, pyright, ...), performs the initialize handshake,
 * opens a document, and collects the diagnostics the server publishes — so
 * the agent can see compiler/linter errors for a file without shelling out.
 *
 * LSP frames JSON-RPC 2.0 messages with HTTP-style Content-Length headers
 * (distinct from the newline-delimited MCP/ACP framing in cli/rpcserve). This
 * file implements that framing; it is dependency-free and unit-tested.
 */
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// WriteMessage marshals v and writes it with an LSP Content-Length header.
func WriteMessage(w io.Writer, v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// ReadMessage reads one Content-Length-framed message body from r.
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" { // blank line terminates headers
			break
		}
		if name, value, ok := strings.Cut(trimmed, ":"); ok {
			if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				n, perr := strconv.Atoi(strings.TrimSpace(value))
				if perr != nil {
					return nil, fmt.Errorf("invalid Content-Length: %q", value)
				}
				contentLength = n
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// rpcMessage is a decoded JSON-RPC frame (response or notification).
type rpcMessage struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}
