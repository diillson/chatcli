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
	mu      sync.Mutex
	queues  map[string][]Message
	history []Message // delivered or sent, oldest first
	histCap int
	seq     uint64
}

// NewRegistry builds an empty bus (histCap<=0 = DefaultHistorySize).
func NewRegistry(histCap int) *Registry {
	if histCap <= 0 {
		histCap = DefaultHistorySize
	}
	return &Registry{queues: make(map[string][]Message), histCap: histCap}
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
	defer r.mu.Unlock()
	r.seq++
	msg := Message{
		ID:     "msg-" + strconv.FormatUint(r.seq, 10),
		From:   from,
		To:     toNorm,
		CardID: strings.TrimSpace(cardID),
		Text:   text,
		At:     time.Now(),
	}
	q := append(r.queues[toNorm], msg)
	if overflow := len(q) - maxQueuePerRecipient; overflow > 0 {
		q = append(q[:0], q[overflow:]...)
	}
	r.queues[toNorm] = q

	r.history = append(r.history, msg)
	if overflow := len(r.history) - r.histCap; overflow > 0 {
		r.history = append(r.history[:0], r.history[overflow:]...)
	}
	return msg, nil
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
	defer r.mu.Unlock()
	msgs := r.queues[key]
	if len(msgs) == 0 {
		return nil
	}
	delete(r.queues, key)
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
