package gateway

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// DefaultMaxConcurrent bounds how many messages are processed in parallel.
const DefaultMaxConcurrent = 4

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
// cancelled. It blocks until shutdown. Returns the first fatal adapter error,
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

// handle runs the agent for one message and delivers the reply.
func (r *Runner) handle(ctx context.Context, msg InboundMessage) {
	reply, err := r.agent(ctx, msg.SessionKey(), msg.Text)
	if err != nil {
		r.logger.Warn("gateway: agent error",
			zap.String("session", msg.SessionKey()), zap.Error(err))
		reply = "⚠️ " + err.Error()
	}
	if reply == "" {
		return
	}
	adapter, ok := r.adapters[msg.Platform]
	if !ok {
		r.logger.Error("gateway: no adapter to reply on", zap.String("platform", msg.Platform))
		return
	}
	if err := adapter.Send(ctx, OutboundMessage{ChatID: msg.ChatID, Text: reply}); err != nil {
		r.logger.Warn("gateway: failed to send reply",
			zap.String("platform", msg.Platform), zap.Error(err))
	}
}

func (r *Runner) firstErr(errCh chan error) error {
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
