/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * cdp.go — a deliberately small Chrome DevTools Protocol client.
 *
 * The @browser tool needs a handful of CDP domains (Target, Page, Runtime,
 * Network). A full protocol binding would add megabytes of generated code to
 * the binary; this client speaks the wire format directly over the
 * gorilla/websocket dependency ChatCLI already ships: JSON commands matched
 * to responses by id, plus an event callback for the session's subscriber.
 */
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// cdpMessage is the wire shape of everything CDP sends or receives.
type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message) }

// eventFunc receives every CDP event frame (method, params, sessionId).
type eventFunc func(method string, params json.RawMessage, sessionID string)

// cdpConn is one websocket connection to a browser's DevTools endpoint.
type cdpConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex

	nextID  atomic.Int64
	pending map[int64]chan cdpMessage
	pendMu  sync.Mutex

	onEvent eventFunc

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

// dialCDP connects to the DevTools websocket and starts the read loop.
func dialCDP(ctx context.Context, url string, onEvent eventFunc) (*cdpConn, error) {
	dialer := websocket.Dialer{
		// CDP snapshot/screenshot frames can be large; the default 32KB
		// read limit would sever the connection mid-session.
		ReadBufferSize:  1 << 20,
		WriteBufferSize: 1 << 20,
	}
	ws, resp, err := dialer.DialContext(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("connect to browser DevTools: %w", err)
	}
	c := &cdpConn{
		ws:      ws,
		pending: make(map[int64]chan cdpMessage),
		onEvent: onEvent,
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// readLoop dispatches responses to their waiters and events to the callback.
func (c *cdpConn) readLoop() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.shutdown(err)
			return
		}
		var msg cdpMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			c.pendMu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.pendMu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg.Method != "" && c.onEvent != nil {
			c.onEvent(msg.Method, msg.Params, msg.SessionID)
		}
	}
}

// shutdown fails every pending call and marks the connection closed.
func (c *cdpConn) shutdown(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
		close(c.closed)
		_ = c.ws.Close()
		c.pendMu.Lock()
		for id, ch := range c.pending {
			delete(c.pending, id)
			close(ch)
		}
		c.pendMu.Unlock()
	})
}

// close tears the connection down.
func (c *cdpConn) close() { c.shutdown(nil) }

// call sends one CDP command (optionally session-scoped) and waits for its
// response, bounded by ctx.
func (c *cdpConn) call(ctx context.Context, sessionID, method string, params interface{}) (json.RawMessage, error) {
	select {
	case <-c.closed:
		if c.closeErr != nil {
			return nil, fmt.Errorf("browser connection closed: %w", c.closeErr)
		}
		return nil, errors.New("browser connection closed")
	default:
	}

	id := c.nextID.Add(1)
	msg := cdpMessage{ID: id, Method: method, SessionID: sessionID}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		msg.Params = raw
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	ch := make(chan cdpMessage, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	c.writeMu.Lock()
	err = c.ws.WriteMessage(websocket.TextMessage, payload)
	c.writeMu.Unlock()
	if err != nil {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("browser connection closed during %s", method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s: %w", method, resp.Error)
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	case <-c.closed:
		return nil, fmt.Errorf("browser connection closed during %s", method)
	}
}
