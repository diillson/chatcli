/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package hub

import (
	"context"
	"sync"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/models"
)

// Broker is a Store that also supports live tailing of a conversation.
type Broker interface {
	Store
	// Subscribe returns a stream of a conversation's events with Seq > sinceSeq:
	// first the persisted backlog, then live events as they are appended. The
	// stream closes when ctx is cancelled or when the consumer falls too far
	// behind (see Manager overflow handling), at which point the caller should
	// resubscribe with the last Seq it saw to resync.
	Subscribe(ctx context.Context, convID string, sinceSeq int64) (<-chan models.ConversationEvent, error)
}

// Manager wraps a Store with an in-memory fan-out layer so multiple frontends
// can live-tail the same conversation. Appends are persisted first, then
// published to subscribers; durability never depends on the fan-out.
type Manager struct {
	Store
	logger *zap.Logger

	mu     sync.Mutex
	subs   map[string]map[int64]*subscriber // convID → set of live subscribers
	nextID int64
	buf    int
}

type subscriber struct {
	ch       chan models.ConversationEvent
	overflow bool // set when a slow consumer was dropped; channel is then closed
}

// NewManager wraps store. bufSize bounds each subscriber's live buffer; a
// consumer that lets it fill is dropped and must resync (back-pressure that
// protects the Hub from a stalled client). bufSize <= 0 defaults to 256.
func NewManager(store Store, logger *zap.Logger, bufSize int) *Manager {
	if logger == nil {
		logger = zap.NewNop()
	}
	if bufSize <= 0 {
		bufSize = 256
	}
	return &Manager{
		Store:  store,
		logger: logger,
		subs:   make(map[string]map[int64]*subscriber),
		buf:    bufSize,
	}
}

// Append persists the event via the wrapped Store, then publishes it to live
// subscribers. A persistence failure is returned without publishing.
func (m *Manager) Append(ctx context.Context, ev models.ConversationEvent) (models.ConversationEvent, error) {
	stored, err := m.Store.Append(ctx, ev)
	if err != nil {
		return stored, err
	}
	m.publish(stored)
	return stored, nil
}

func (m *Manager) publish(ev models.ConversationEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sub := range m.subs[ev.ConvID] {
		select {
		case sub.ch <- ev:
		default:
			// Slow consumer: drop it and close its channel. Its stream goroutine
			// ends and the caller resubscribes from its last Seq to catch up.
			sub.overflow = true
			close(sub.ch)
			delete(m.subs[ev.ConvID], id)
			m.logger.Warn("hub: subscriber overflow, dropped for resync",
				zap.String("conv_id", ev.ConvID), zap.Int64("sub_id", id))
		}
	}
	if len(m.subs[ev.ConvID]) == 0 {
		delete(m.subs, ev.ConvID)
	}
}

// Subscribe implements Broker.
func (m *Manager) Subscribe(ctx context.Context, convID string, sinceSeq int64) (<-chan models.ConversationEvent, error) {
	// Register the live subscriber before reading the backlog so no append that
	// lands in between is lost; the maxSeq dedup below drops any overlap.
	m.mu.Lock()
	id := m.nextID
	m.nextID++
	sub := &subscriber{ch: make(chan models.ConversationEvent, m.buf)}
	if m.subs[convID] == nil {
		m.subs[convID] = make(map[int64]*subscriber)
	}
	m.subs[convID][id] = sub
	m.mu.Unlock()

	out := make(chan models.ConversationEvent)
	go func() {
		defer close(out)
		defer m.unsubscribe(convID, id)

		backlog, err := m.Store.Read(ctx, convID, sinceSeq, 0)
		if err != nil {
			m.logger.Warn("hub: subscribe backlog read failed", zap.String("conv_id", convID), zap.Error(err))
			return
		}
		maxSeq := sinceSeq
		for _, ev := range backlog {
			select {
			case out <- ev:
				maxSeq = ev.Seq
			case <-ctx.Done():
				return
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub.ch:
				if !ok {
					return // overflow: stream ends, caller resyncs
				}
				if ev.Seq <= maxSeq {
					continue // already delivered via backlog
				}
				select {
				case out <- ev:
					maxSeq = ev.Seq
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (m *Manager) unsubscribe(convID string, id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if subs := m.subs[convID]; subs != nil {
		delete(subs, id) // publish closes the channel on overflow; normal exit just deregisters
		if len(subs) == 0 {
			delete(m.subs, convID)
		}
	}
}

var _ Broker = (*Manager)(nil)
