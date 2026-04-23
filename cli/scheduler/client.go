/*
 * ChatCLI - Scheduler: IPC client.
 *
 * RemoteClient wraps a UNIX-socket connection and exposes methods that
 * mirror the in-process Scheduler API. The chatcli CLI uses this when
 * a daemon is detected on Start.
 *
 * The connection is persistent — reads and writes are serialized. When
 * the connection drops, every pending request gets ErrNoDaemon and
 * the CLI falls back to in-process mode (configurable).
 */
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RemoteClient is a thin IPC client talking to a Daemon.
type RemoteClient struct {
	conn       *framedConn
	socketPath string

	mu      sync.Mutex
	closed  bool
	pending map[string]chan Frame

	// evCh carries server-sent events after Subscribe.
	evMu sync.Mutex
	evCh chan Event
}

// Dial connects to the daemon at socketPath.
func Dial(socketPath string) (*RemoteClient, error) {
	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDaemon, err)
	}
	c := &RemoteClient{
		conn:       &framedConn{conn: conn},
		socketPath: socketPath,
		pending:    make(map[string]chan Frame),
	}
	go c.readLoop()
	return c, nil
}

// Close terminates the connection.
func (c *RemoteClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.conn.Write(Frame{Kind: KindBye})
	return c.conn.Close()
}

// Ping confirms the daemon is responsive.
func (c *RemoteClient) Ping(ctx context.Context) error {
	_, err := c.call(ctx, Frame{Kind: KindPing})
	return err
}

// Enqueue sends a schedule_job request.
func (c *RemoteClient) Enqueue(ctx context.Context, owner Owner, in ToolInput) (*ToolOutput, error) {
	raw, _ := json.Marshal(in)
	reply, err := c.call(ctx, Frame{Kind: KindEnqueue, Owner: &owner, Input: raw})
	if err != nil {
		return nil, err
	}
	var out ToolOutput
	_ = json.Unmarshal(reply.Result, &out)
	return &out, nil
}

// Cancel cancels a job via IPC.
func (c *RemoteClient) Cancel(ctx context.Context, owner Owner, id JobID, reason string) error {
	in := ToolInput{ID: string(id), Reason: reason}
	raw, _ := json.Marshal(in)
	_, err := c.call(ctx, Frame{Kind: KindCancel, Owner: &owner, Input: raw})
	return err
}

// Pause pauses a job via IPC.
func (c *RemoteClient) Pause(ctx context.Context, owner Owner, id JobID, reason string) error {
	in := ToolInput{ID: string(id), Reason: reason}
	raw, _ := json.Marshal(in)
	_, err := c.call(ctx, Frame{Kind: KindPause, Owner: &owner, Input: raw})
	return err
}

// Resume resumes a paused job.
func (c *RemoteClient) Resume(ctx context.Context, owner Owner, id JobID) error {
	in := ToolInput{ID: string(id)}
	raw, _ := json.Marshal(in)
	_, err := c.call(ctx, Frame{Kind: KindResume, Owner: &owner, Input: raw})
	return err
}

// Query fetches a single job.
func (c *RemoteClient) Query(ctx context.Context, id JobID) (*Job, error) {
	in := ToolInput{ID: string(id)}
	raw, _ := json.Marshal(in)
	reply, err := c.call(ctx, Frame{Kind: KindQuery, Input: raw})
	if err != nil {
		return nil, err
	}
	var out ToolOutput
	_ = json.Unmarshal(reply.Result, &out)
	if out.Job == nil {
		return nil, fmt.Errorf("remote: no job in reply")
	}
	return out.Job, nil
}

// List fetches summaries.
func (c *RemoteClient) List(ctx context.Context, filter ListFilter) ([]JobSummary, error) {
	in := ToolInput{Filter: filter}
	raw, _ := json.Marshal(in)
	reply, err := c.call(ctx, Frame{Kind: KindList, Input: raw})
	if err != nil {
		return nil, err
	}
	var out ToolOutput
	_ = json.Unmarshal(reply.Result, &out)
	return out.Jobs, nil
}

// Snapshot forces the daemon to write a snapshot now.
func (c *RemoteClient) Snapshot(ctx context.Context) error {
	_, err := c.call(ctx, Frame{Kind: KindSnapshot})
	return err
}

// Stats returns the daemon's current runtime stats.
func (c *RemoteClient) Stats(ctx context.Context) (DaemonStats, error) {
	reply, err := c.call(ctx, Frame{Kind: KindStats})
	if err != nil {
		return DaemonStats{}, err
	}
	var s DaemonStats
	_ = json.Unmarshal(reply.Result, &s)
	return s, nil
}

// Subscribe registers the caller for server-sent events. The returned
// channel is closed when the connection drops.
func (c *RemoteClient) Subscribe(ctx context.Context) (<-chan Event, error) {
	_, err := c.call(ctx, Frame{Kind: KindSubscribe})
	if err != nil {
		return nil, err
	}
	c.evMu.Lock()
	if c.evCh == nil {
		c.evCh = make(chan Event, 64)
	}
	ch := c.evCh
	c.evMu.Unlock()
	return ch, nil
}

// call sends a request and waits for the matching reply.
func (c *RemoteClient) call(ctx context.Context, frame Frame) (Frame, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Frame{}, ErrNoDaemon
	}
	if frame.ID == "" {
		frame.ID = uuid.New().String()
	}
	ch := make(chan Frame, 1)
	c.pending[frame.ID] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, frame.ID)
		c.mu.Unlock()
	}()

	if err := c.conn.Write(frame); err != nil {
		return Frame{}, err
	}
	select {
	case reply := <-ch:
		if reply.Kind == KindError {
			return reply, fmt.Errorf("remote: %s", reply.Error)
		}
		return reply, nil
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-time.After(2 * time.Minute):
		return Frame{}, fmt.Errorf("remote: request timeout")
	}
}

// readLoop drains frames from the daemon, routing them to either the
// matching pending request or the event channel.
func (c *RemoteClient) readLoop() {
	for {
		frame, err := c.conn.Read()
		if err != nil {
			c.mu.Lock()
			c.closed = true
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			c.evMu.Lock()
			if c.evCh != nil {
				close(c.evCh)
				c.evCh = nil
			}
			c.evMu.Unlock()
			return
		}
		if frame.Kind == KindEvent && frame.Event != nil {
			c.evMu.Lock()
			ch := c.evCh
			c.evMu.Unlock()
			if ch != nil {
				select {
				case ch <- *frame.Event:
				default:
					// Drop on slow subscriber — the UI polls anyway.
				}
			}
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[frame.ID]
		c.mu.Unlock()
		if ok {
			select {
			case ch <- frame:
			default:
			}
		}
	}
}
