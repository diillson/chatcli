/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DefaultMaxConcurrent bounds how many messages are processed in parallel.
const DefaultMaxConcurrent = 4

// progressFlushInterval throttles streamed progress so a chatty agent does not
// flood the platform's send API (Telegram/WhatsApp rate-limit per chat). Lines
// emitted within a window are coalesced into a single message.
const progressFlushInterval = 3 * time.Second

// maxMessageRunes caps an outbound message to stay under platform limits
// (Telegram is 4096; WhatsApp ~4096). Longer messages are clipped.
const maxMessageRunes = 3500

// Runner wires adapters to the agent: it fans inbound messages out to the
// AgentFunc (bounded concurrency) and delivers replies through the adapter
// that received them.
type Runner struct {
	adapters      map[string]Adapter // by platform name, for reply routing
	order         []Adapter          // start order
	agent         AgentFunc
	logger        *zap.Logger
	maxConcurrent int
}

// NewRunner builds a runner. maxConcurrent <= 0 uses DefaultMaxConcurrent.
func NewRunner(adapters []Adapter, agent AgentFunc, logger *zap.Logger, maxConcurrent int) *Runner {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	byName := make(map[string]Adapter, len(adapters))
	for _, a := range adapters {
		byName[a.Name()] = a
	}
	return &Runner{
		adapters:      byName,
		order:         adapters,
		agent:         agent,
		logger:        logger,
		maxConcurrent: maxConcurrent,
	}
}

// Run starts every adapter and processes inbound messages until ctx is
// canceled. It blocks until shutdown. Returns the first fatal adapter error,
// or nil on clean ctx cancellation.
func (r *Runner) Run(ctx context.Context) error {
	if len(r.order) == 0 {
		return fmt.Errorf("gateway: no adapters configured")
	}
	if r.agent == nil {
		return fmt.Errorf("gateway: no agent function configured")
	}

	inbound := make(chan InboundMessage, 64)
	sem := make(chan struct{}, r.maxConcurrent)

	var adapterWG sync.WaitGroup
	errCh := make(chan error, len(r.order))
	for _, a := range r.order {
		adapterWG.Add(1)
		go func(a Adapter) {
			defer adapterWG.Done()
			if err := a.Start(ctx, inbound); err != nil && ctx.Err() == nil {
				r.logger.Error("gateway: adapter stopped with error",
					zap.String("platform", a.Name()), zap.Error(err))
				errCh <- fmt.Errorf("%s: %w", a.Name(), err)
			}
		}(a)
	}

	// Close inbound once all adapters have returned (after ctx cancel).
	go func() {
		adapterWG.Wait()
		close(inbound)
	}()

	var workerWG sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			workerWG.Wait()
			return r.firstErr(errCh)
		case msg, ok := <-inbound:
			if !ok {
				workerWG.Wait()
				return r.firstErr(errCh)
			}
			workerWG.Add(1)
			sem <- struct{}{}
			go func(msg InboundMessage) {
				defer workerWG.Done()
				defer func() { <-sem }()
				r.handle(ctx, msg)
			}(msg)
		}
	}
}

// handle runs the agent for one message, streaming throttled progress back to
// the originating chat, then delivers the final reply.
func (r *Runner) handle(ctx context.Context, msg InboundMessage) {
	adapter, ok := r.adapters[msg.Platform]
	if !ok {
		r.logger.Error("gateway: no adapter to reply on", zap.String("platform", msg.Platform))
		return
	}
	send := func(text string) {
		if err := adapter.Send(ctx, OutboundMessage{ChatID: msg.ChatID, Text: clip(text, maxMessageRunes)}); err != nil {
			r.logger.Warn("gateway: failed to send",
				zap.String("platform", msg.Platform), zap.Error(err))
		}
	}

	sink := newProgressSink(send)
	reply, err := r.agent(WithProgress(ctx, sink.emit), msg.SessionKey(), msg.Text)
	sink.flush() // deliver any progress buffered since the last flush
	if err != nil {
		r.logger.Warn("gateway: agent error",
			zap.String("session", msg.SessionKey()), zap.Error(err))
		reply = "⚠️ " + err.Error()
	}
	if reply == "" {
		return
	}
	send(reply)
}

// progressSink coalesces streamed progress lines and flushes them as a single
// message at most once per progressFlushInterval, so the platform send API is
// not flooded. It is safe for concurrent emit calls (the capture reader runs in
// its own goroutine).
type progressSink struct {
	mu        sync.Mutex
	buf       []string
	lastFlush time.Time
	send      func(string)
}

func newProgressSink(send func(string)) *progressSink {
	return &progressSink{lastFlush: time.Now(), send: send}
}

// emit buffers a progress line and flushes if the throttle window has elapsed.
func (p *progressSink) emit(line string) {
	p.mu.Lock()
	p.buf = append(p.buf, line)
	due := time.Since(p.lastFlush) >= progressFlushInterval
	p.mu.Unlock()
	if due {
		p.flush()
	}
}

// flush sends any buffered progress as one message and resets the window.
func (p *progressSink) flush() {
	p.mu.Lock()
	if len(p.buf) == 0 {
		p.mu.Unlock()
		return
	}
	text := strings.Join(p.buf, "\n")
	p.buf = p.buf[:0]
	p.lastFlush = time.Now()
	p.mu.Unlock()
	p.send(text)
}

// clip truncates s to at most maxRunes runes, appending an ellipsis when cut.
func clip(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func (r *Runner) firstErr(errCh chan error) error {
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
