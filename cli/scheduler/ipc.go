/*
 * ChatCLI - Scheduler: daemon IPC protocol.
 *
 * The daemon exposes the scheduler over a UNIX socket. The protocol is
 * length-prefixed JSON frames (so a stream can carry both requests and
 * server-initiated events cleanly).
 *
 * Frame:  [4-byte uint32 big-endian length] [JSON payload]
 *
 * Every request has an ID; every response carries the same ID. Server-
 * initiated events use ID="" and Kind="event".
 *
 * Kinds:
 *
 *   client → server:
 *     "ping"
 *     "enqueue"        Input: ToolInput + Owner
 *     "cancel"         Input: id, reason
 *     "pause"/"resume" Input: id
 *     "query"          Input: id
 *     "list"           Input: filter
 *     "snapshot"       Input: none
 *     "subscribe"      — enter event-stream mode for this connection
 *     "stats"          Input: none — returns scheduler + daemon stats
 *     "bye"            — orderly close
 *
 *   server → client:
 *     "ok"       Result: ToolOutput / bytes
 *     "error"    Error: string
 *     "event"    Event: scheduler.Event (only after subscribe)
 *
 * Thread safety: one goroutine per connection handles reads+writes
 * under a single writer mutex. Readers decode frames sequentially.
 */
package scheduler

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// FrameKind classifies a frame.
type FrameKind string

const (
	KindPing      FrameKind = "ping"
	KindEnqueue   FrameKind = "enqueue"
	KindCancel    FrameKind = "cancel"
	KindPause     FrameKind = "pause"
	KindResume    FrameKind = "resume"
	KindQuery     FrameKind = "query"
	KindList      FrameKind = "list"
	KindSnapshot  FrameKind = "snapshot"
	KindSubscribe FrameKind = "subscribe"
	KindStats     FrameKind = "stats"
	KindBye       FrameKind = "bye"

	KindOK    FrameKind = "ok"
	KindError FrameKind = "error"
	KindEvent FrameKind = "event"
)

// Frame is the on-wire envelope.
type Frame struct {
	ID     string          `json:"id,omitempty"`
	Kind   FrameKind       `json:"kind"`
	Owner  *Owner          `json:"owner,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Event  *Event          `json:"event,omitempty"`
	TS     time.Time       `json:"ts,omitempty"`
}

// maxFrameSize caps per-frame payload to prevent OOM on malformed or
// hostile input.
const maxFrameSize = 16 << 20 // 16 MiB

// writeFrame serializes and writes a framed message to w.
func writeFrame(w io.Writer, mu *sync.Mutex, f Frame) error {
	if f.TS.IsZero() {
		f.TS = time.Now()
	}
	payload, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("ipc marshal: %w", err)
	}
	if len(payload) > maxFrameSize {
		return fmt.Errorf("%w: frame too large (%d)", ErrIPCProtocol, len(payload))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))

	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return nil
}

// readFrame reads one framed message.
func readFrame(r io.Reader) (Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > maxFrameSize {
		return Frame{}, fmt.Errorf("%w: bad length %d", ErrIPCProtocol, length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Frame{}, err
	}
	var f Frame
	if err := json.Unmarshal(buf, &f); err != nil {
		return Frame{}, fmt.Errorf("%w: %v", ErrIPCProtocol, err)
	}
	return f, nil
}

// DaemonStats is returned for Kind=stats.
type DaemonStats struct {
	Version     string    `json:"version"`
	Started     time.Time `json:"started_at"`
	Uptime      string    `json:"uptime"`
	JobsActive  int       `json:"jobs_active"`
	QueueDepth  int       `json:"queue_depth"`
	WALSegments int       `json:"wal_segments"`
	Connections int       `json:"connections"`
	Breakers    map[string]BreakerState `json:"breakers,omitempty"`
}

// DefaultSocketPath returns the platform-default daemon socket path.
// Honors CHATCLI_SCHEDULER_DAEMON_SOCKET when set.
func DefaultSocketPath(cfg Config) string {
	if cfg.DaemonSocket != "" {
		return cfg.DaemonSocket
	}
	return "/tmp/chatcli-scheduler.sock"
}

// CheckDaemon tries to Ping the daemon at socketPath; returns nil iff
// a daemon is up and responsive.
func CheckDaemon(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoDaemon, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := writeFrame(conn, nil, Frame{Kind: KindPing, ID: "ping-1"}); err != nil {
		return err
	}
	reply, err := readFrame(conn)
	if err != nil {
		return err
	}
	if reply.Kind != KindOK {
		return fmt.Errorf("%w: ping got %s", ErrIPCProtocol, reply.Kind)
	}
	return nil
}

// Ensure concurrent-safe writes on Conn use a sync.Mutex; helper.
type framedConn struct {
	conn net.Conn
	mu   sync.Mutex
}

func (f *framedConn) Write(frame Frame) error { return writeFrame(f.conn, &f.mu, frame) }
func (f *framedConn) Read() (Frame, error)    { return readFrame(f.conn) }
func (f *framedConn) Close() error             { return f.conn.Close() }

var _ atomic.Value // Keep import in case future metrics want atomic state.
