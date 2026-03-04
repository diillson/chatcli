package bus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// MessageHandler is invoked when a message arrives.
type MessageHandler func(msg interface{})

// ErrorHandler is called when an error occurs during dispatch.
type ErrorHandler func(err error)

// Subscription tracks a subscriber.
type Subscription struct {
	ID          string
	Handler     MessageHandler
	MessageType *MessageType // nil = catch-all
	Channel     string       // empty = all channels
}

// Metrics tracks bus activity counters.
type Metrics struct {
	Published int64
	Delivered int64
	Dropped   int64
}

// MessageBus is the central event bus for decoupling agent I/O.
type MessageBus struct {
	subscriptions map[string]Subscription
	mu            sync.RWMutex
	errorHandler  ErrorHandler
	bufferSize    int
	closed        atomic.Bool

	published atomic.Int64
	delivered atomic.Int64
	dropped   atomic.Int64

	pendingReplies map[string]chan *OutboundMessage
	replyMu        sync.Mutex
}

var (
	ErrBusClosed = errors.New("message bus is closed")
	ErrTimeout   = errors.New("request timed out")
)

// New creates a new MessageBus.
func New(bufferSize int) *MessageBus {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	return &MessageBus{
		subscriptions:  make(map[string]Subscription),
		bufferSize:     bufferSize,
		pendingReplies: make(map[string]chan *OutboundMessage),
	}
}

// SetErrorHandler sets the callback for dispatch errors.
func (mb *MessageBus) SetErrorHandler(fn ErrorHandler) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.errorHandler = fn
}

// Subscribe registers a handler and returns a subscription ID.
func (mb *MessageBus) Subscribe(sub Subscription) string {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if sub.ID == "" {
		sub.ID = uuid.New().String()
	}
	mb.subscriptions[sub.ID] = sub
	return sub.ID
}

// Unsubscribe removes a subscriber by ID.
func (mb *MessageBus) Unsubscribe(id string) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	delete(mb.subscriptions, id)
}

// PublishInbound dispatches an inbound message to matching subscribers.
func (mb *MessageBus) PublishInbound(ctx context.Context, msg InboundMessage) error {
	if mb.closed.Load() {
		return ErrBusClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	mb.published.Add(1)
	mb.dispatch(msg, msg.Type, msg.Channel)
	return nil
}

// PublishOutbound dispatches an outbound message to matching subscribers.
func (mb *MessageBus) PublishOutbound(ctx context.Context, msg OutboundMessage) error {
	if mb.closed.Load() {
		return ErrBusClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	mb.published.Add(1)

	if msg.ReplyToID != "" {
		mb.replyMu.Lock()
		ch, ok := mb.pendingReplies[msg.ReplyToID]
		mb.replyMu.Unlock()
		if ok {
			select {
			case ch <- &msg:
			default:
			}
		}
	}

	mb.dispatch(msg, msg.Type, msg.Channel)
	return nil
}

// PublishOutboundMedia dispatches a media message.
func (mb *MessageBus) PublishOutboundMedia(ctx context.Context, msg OutboundMediaMessage) error {
	if mb.closed.Load() {
		return ErrBusClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	mb.published.Add(1)
	mb.dispatch(msg, msg.Type, msg.Channel)
	return nil
}

// Request sends an inbound message and waits for a correlated outbound reply.
func (mb *MessageBus) Request(ctx context.Context, msg InboundMessage, timeout time.Duration) (*OutboundMessage, error) {
	if mb.closed.Load() {
		return nil, ErrBusClosed
	}
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}

	replyCh := make(chan *OutboundMessage, 1)
	mb.replyMu.Lock()
	mb.pendingReplies[msg.ID] = replyCh
	mb.replyMu.Unlock()

	defer func() {
		mb.replyMu.Lock()
		delete(mb.pendingReplies, msg.ID)
		mb.replyMu.Unlock()
	}()

	if err := mb.PublishInbound(ctx, msg); err != nil {
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case reply := <-replyCh:
		return reply, nil
	case <-timer.C:
		return nil, ErrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GetMetrics returns current bus metrics.
func (mb *MessageBus) GetMetrics() Metrics {
	return Metrics{
		Published: mb.published.Load(),
		Delivered: mb.delivered.Load(),
		Dropped:   mb.dropped.Load(),
	}
}

// Close shuts down the message bus.
func (mb *MessageBus) Close() error {
	if mb.closed.Swap(true) {
		return nil
	}
	mb.mu.Lock()
	mb.subscriptions = make(map[string]Subscription)
	mb.mu.Unlock()

	mb.replyMu.Lock()
	for id, ch := range mb.pendingReplies {
		close(ch)
		delete(mb.pendingReplies, id)
	}
	mb.replyMu.Unlock()
	return nil
}

func (mb *MessageBus) dispatch(msg interface{}, msgType MessageType, channel string) {
	mb.mu.RLock()
	subs := make([]Subscription, 0, len(mb.subscriptions))
	for _, sub := range mb.subscriptions {
		if mb.matches(sub, msgType, channel) {
			subs = append(subs, sub)
		}
	}
	errHandler := mb.errorHandler
	mb.mu.RUnlock()

	for _, sub := range subs {
		handler := sub.Handler
		go func() {
			defer func() {
				if r := recover(); r != nil {
					mb.dropped.Add(1)
					if errHandler != nil {
						errHandler(fmt.Errorf("handler panic: %v", r))
					}
				}
			}()
			handler(msg)
			mb.delivered.Add(1)
		}()
	}
}

func (mb *MessageBus) matches(sub Subscription, msgType MessageType, channel string) bool {
	if sub.MessageType != nil && *sub.MessageType != msgType {
		return false
	}
	if sub.Channel != "" && sub.Channel != channel {
		return false
	}
	return true
}
