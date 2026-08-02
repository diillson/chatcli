/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package mail is the squad's internal message bus: agents (and the user)
 * send short directed messages to each other, and each recipient drains its
 * inbox at the next safe turn boundary of its ReAct loop.
 *
 * This is what lets squad members talk without breaking the star topology:
 * a reviewer worker sends "tests missing in X, fix and resubmit" to the
 * coder; the coder's loop injects it as context on its next turn. The
 * orchestrator drains the "orchestrator" inbox between its own turns, and
 * the user can inject directives into any agent's inbox via /mail send.
 *
 * Recipients are logical names, not run IDs: worker agent type names
 * ("coder", "reviewer", …), "orchestrator" for the main loop, or any
 * ad-hoc topic the squad agrees on (e.g. a card ID). Messages are
 * in-memory, process-wide, mutex-serialized; a bounded history ring keeps
 * recent traffic inspectable via /mail.
 *
 * The package is a leaf: stdlib only, importable from cli, cli/agent/workers
 * and cli/plugins without cycles.
 */
package mail

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Message is one directed squad message.
type Message struct {
	ID     string
	From   string
	To     string
	CardID string // optional board card this message is about
	Text   string
	At     time.Time
}

// DefaultHistorySize bounds the delivered-message history ring.
const DefaultHistorySize = 200

// maxQueueetPerRecipient bounds any single inbox so a runaway sender cannot
// grow memory without limit; oldest messages are dropped first.
const maxQueuePerRecipient = 100

// Registry is the process-wide message bus.
type Registry struct {
	mu       sync.Mutex
	queues   map[string][]Message
	history  []Message // delivered or sent, oldest first
	histCap  int
	seq      uint64
	instance string          // random token making IDs globally unique across processes
	seen     map[string]bool // message IDs already enqueued (local or external)
	seenIDs  []string        // insertion order for bounded eviction of seen
	onSend   func(Message)   // optional persistence hook (hub bridge)
	onDrain  func([]Message) // optional ack hook (hub bridge)
}

// NewRegistry builds an empty bus (histCap<=0 = DefaultHistorySize).
func NewRegistry(histCap int) *Registry {
	if histCap <= 0 {
		histCap = DefaultHistorySize
	}
	return &Registry{
		queues:   make(map[string][]Message),
		histCap:  histCap,
		instance: newInstanceToken(),
		seen:     make(map[string]bool),
	}
}

// newInstanceToken returns a short random token embedded in message IDs so
// two processes sharing a persistence backend never collide.
func newInstanceToken() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Degraded uniqueness is acceptable — collisions only cost a
		// dropped duplicate delivery, never corruption.
		return "local"
	}
	return hex.EncodeToString(b[:])
}

// OnSend installs a hook invoked (outside the lock) after every successful
// local Send — the hub bridge uses it to persist messages. nil clears it.
func (r *Registry) OnSend(fn func(Message)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onSend = fn
}

// OnDrain installs a hook invoked (outside the lock) after every non-empty
// Drain — the hub bridge uses it to persist delivery acks. nil clears it.
func (r *Registry) OnDrain(fn func([]Message)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onDrain = fn
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default returns the process-wide bus.
func Default() *Registry {
	defaultOnce.Do(func() { defaultReg = NewRegistry(0) })
	return defaultReg
}

// normalizeRecipient canonicalizes a recipient name.
func normalizeRecipient(to string) string {
	return strings.ToLower(strings.TrimSpace(to))
}

// Send enqueues a message. From and To must be non-empty; Text must be
// non-empty. Returns the stored message with its assigned ID.
func (r *Registry) Send(from, to, cardID, text string) (Message, error) {
	if r == nil {
		return Message{}, errors.New("mail: nil registry")
	}
	from = strings.TrimSpace(from)
	toNorm := normalizeRecipient(to)
	text = strings.TrimSpace(text)
	if from == "" {
		return Message{}, errors.New("mail: sender must not be empty")
	}
	if toNorm == "" {
		return Message{}, errors.New("mail: recipient must not be empty")
	}
	if text == "" {
		return Message{}, errors.New("mail: message text must not be empty")
	}

	r.mu.Lock()
	r.seq++
	msg := Message{
		ID:     "msg-" + r.instance + "-" + strconv.FormatUint(r.seq, 10),
		From:   from,
		To:     toNorm,
		CardID: strings.TrimSpace(cardID),
		Text:   text,
		At:     time.Now(),
	}
	r.enqueueLocked(msg)
	hook := r.onSend
	r.mu.Unlock()

	if hook != nil {
		hook(msg)
	}
	return msg, nil
}

// enqueueLocked appends a message to its recipient queue and to the history
// ring, marking its ID as seen. Caller must hold r.mu.
func (r *Registry) enqueueLocked(msg Message) {
	r.markSeenLocked(msg.ID)
	q := append(r.queues[msg.To], msg)
	if overflow := len(q) - maxQueuePerRecipient; overflow > 0 {
		q = append(q[:0], q[overflow:]...)
	}
	r.queues[msg.To] = q

	r.history = append(r.history, msg)
	if overflow := len(r.history) - r.histCap; overflow > 0 {
		r.history = append(r.history[:0], r.history[overflow:]...)
	}
}

// markSeenLocked records a message ID with bounded eviction (caller holds mu).
func (r *Registry) markSeenLocked(id string) {
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	r.seenIDs = append(r.seenIDs, id)
	if limit := r.histCap * 4; len(r.seenIDs) > limit {
		evict := r.seenIDs[0]
		r.seenIDs = r.seenIDs[1:]
		delete(r.seen, evict)
	}
}

// Deliver enqueues a message that originated OUTSIDE this process (hub
// bridge hydration/polling). The message keeps its original identity; a
// message whose ID was already seen — including this process's own sends
// echoed back by the backend — is dropped. Returns whether it was enqueued.
func (r *Registry) Deliver(msg Message) bool {
	if r == nil {
		return false
	}
	msg.To = normalizeRecipient(msg.To)
	if msg.ID == "" || msg.From == "" || msg.To == "" || strings.TrimSpace(msg.Text) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[msg.ID] {
		return false
	}
	r.enqueueLocked(msg)
	return true
}

// Drain removes and returns all pending messages for a recipient, oldest
// first. An unknown/empty recipient returns nil.
func (r *Registry) Drain(recipient string) []Message {
	if r == nil {
		return nil
	}
	key := normalizeRecipient(recipient)
	if key == "" {
		return nil
	}
	r.mu.Lock()
	msgs := r.queues[key]
	if len(msgs) == 0 {
		r.mu.Unlock()
		return nil
	}
	delete(r.queues, key)
	hook := r.onDrain
	r.mu.Unlock()

	if hook != nil {
		hook(msgs)
	}
	return msgs
}

// Peek returns pending messages for a recipient without removing them.
func (r *Registry) Peek(recipient string) []Message {
	if r == nil {
		return nil
	}
	key := normalizeRecipient(recipient)
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Message, len(r.queues[key]))
	copy(out, r.queues[key])
	return out
}

// Pending returns the number of queued messages per recipient.
func (r *Registry) Pending() map[string]int {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.queues))
	for k, q := range r.queues {
		if len(q) > 0 {
			out[k] = len(q)
		}
	}
	return out
}

// Recent returns up to n messages from the history, newest first (n<=0 =
// all retained).
func (r *Registry) Recent(n int) []Message {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	total := len(r.history)
	if n <= 0 || n > total {
		n = total
	}
	out := make([]Message, 0, n)
	for i := total - 1; i >= total-n; i-- {
		out = append(out, r.history[i])
	}
	return out
}

// FormatInbox renders drained messages as the context block injected into
// the recipient's LLM history. English on purpose — models follow English
// instructions more reliably.
func FormatInbox(msgs []Message) string {
	if len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[SQUAD MAIL] You received %d message(s) from other agents. React to them before continuing your task:\n", len(msgs))
	for _, m := range msgs {
		fmt.Fprintf(&b, "- from=%s", m.From)
		if m.CardID != "" {
			fmt.Fprintf(&b, " card=%s", m.CardID)
		}
		fmt.Fprintf(&b, ": %s\n", m.Text)
	}
	return b.String()
}
