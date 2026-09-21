/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"context"
	"sync"
	"time"
)

// DefaultPollInterval is how often a process checks the lease. One stat
// every two seconds is the entire cost of the feature while it is off.
const DefaultPollInterval = 2 * time.Second

// Options configures a Controller.
type Options struct {
	Bus    *Bus   // defaults to Default()
	Root   string // defaults to DefaultRoot()
	Meta   Meta   // Instance is filled from the bus
	Forced bool   // record regardless of the lease
	// ForcedFn, when set, is asked on every check whether to record
	// regardless of the lease. It lets a setting that can change while the
	// process runs (an env reloaded from .env) take effect without a restart.
	ForcedFn func() bool
	Poll     time.Duration
	OnError  func(error) // optional; spool failures are reported, never fatal
}

// Controller owns the recording lifecycle of one process: it turns the bus
// on while a lease is live (or recording is forced), spools what the bus
// publishes, keeps the heartbeat fresh and turns everything back off when
// the lease runs out.
type Controller struct {
	opts Options
	bus  *Bus

	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	recording bool
	surface   string
}

// Start launches the controller. It returns nil when no spool root can be
// resolved (no home directory): telemetry is then simply unavailable.
func Start(ctx context.Context, opts Options) *Controller {
	if opts.Bus == nil {
		opts.Bus = Default()
	}
	if opts.Root == "" {
		root, err := DefaultRoot()
		if err != nil {
			return nil
		}
		opts.Root = root
	}
	if opts.Poll <= 0 {
		opts.Poll = DefaultPollInterval
	}
	opts.Meta.Instance = opts.Bus.Instance()

	runCtx, cancel := context.WithCancel(ctx)
	c := &Controller{
		opts:    opts,
		bus:     opts.Bus,
		wake:    make(chan struct{}, 1),
		cancel:  cancel,
		done:    make(chan struct{}),
		surface: opts.Meta.Surface,
	}
	go c.run(runCtx)
	return c
}

// Root returns the spool root this controller records under.
func (c *Controller) Root() string {
	if c == nil {
		return ""
	}
	return c.opts.Root
}

// Recording reports whether this process is currently spooling.
func (c *Controller) Recording() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recording
}

// SetSurface renames the process role (repl, acp, gateway, ...). Entry points
// learn their surface after the shared bootstrap has started the controller;
// the new name reaches meta.json on the next heartbeat.
func (c *Controller) SetSurface(surface string) {
	if c == nil || surface == "" {
		return
	}
	c.mu.Lock()
	c.surface = surface
	c.mu.Unlock()
	c.Wake()
}

func (c *Controller) currentSurface() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.surface
}

// Wake makes the controller re-check the lease now instead of at the next
// poll, so the process that just opened a dashboard records immediately.
func (c *Controller) Wake() {
	if c == nil {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Close stops recording and waits for the spool to be flushed.
func (c *Controller) Close() {
	if c == nil {
		return
	}
	c.cancel()
	<-c.done
}

func (c *Controller) report(err error) {
	if err != nil && c.opts.OnError != nil {
		c.opts.OnError(err)
	}
}

func (c *Controller) setRecording(on bool) {
	c.mu.Lock()
	c.recording = on
	c.mu.Unlock()
}

func (c *Controller) run(ctx context.Context) {
	defer close(c.done)

	PruneInstances(c.opts.Root, DefaultRetention, c.bus.Instance())

	poll := time.NewTicker(c.opts.Poll)
	defer poll.Stop()
	beat := time.NewTicker(HeartbeatInterval)
	defer beat.Stop()

	var (
		spool  *Spool
		events <-chan Event
		unsub  func()
	)
	stop := func() {
		if spool == nil {
			return
		}
		c.bus.SetEnabled(false)
		unsub()
		// Drain what the pump already delivered so the tail of the run is
		// not lost on shutdown.
		for ev := range events {
			c.report(spool.Append(ev))
		}
		c.report(spool.Close())
		spool, events, unsub = nil, nil, nil
		c.setRecording(false)
	}
	defer stop()

	reconcile := func() {
		want := c.opts.Forced || (c.opts.ForcedFn != nil && c.opts.ForcedFn()) || LeaseActive(c.opts.Root, time.Now())
		switch {
		case want && spool == nil:
			meta := c.opts.Meta
			meta.Surface = c.currentSurface()
			s, err := OpenSpool(c.opts.Root, meta)
			if err != nil {
				c.report(err)
				return
			}
			spool = s
			// Subscribe before enabling so the snapshot emitted on the
			// rising edge is the first thing the spool records.
			events, unsub = c.bus.Subscribe(queueSize)
			c.bus.SetEnabled(true)
			c.setRecording(true)
		case !want && spool != nil:
			stop()
		case spool != nil && spool.meta.Surface != c.currentSurface():
			spool.meta.Surface = c.currentSurface()
			c.report(spool.Beat())
		}
	}
	reconcile()

	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			reconcile()
		case <-c.wake:
			reconcile()
		case <-beat.C:
			if spool != nil {
				c.report(spool.Beat())
			}
		case ev, ok := <-events:
			if !ok {
				continue
			}
			batch := []Event{ev}
		drain:
			for len(batch) < 256 {
				select {
				case more, ok := <-events:
					if !ok {
						break drain
					}
					batch = append(batch, more)
				default:
					break drain
				}
			}
			c.report(spool.Append(batch...))
		}
	}
}
