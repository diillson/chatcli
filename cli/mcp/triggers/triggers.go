/*
 * ChatCLI - MCP channel reactive triggers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The trigger engine turns inbound MCP channel messages into actionable
 * events for the CLI. Three modes are supported:
 *
 *   notify  — show a discreet banner above the next prompt and bump
 *             the unread counter. Default mode. Zero side effects.
 *   confirm — same banner, plus a one-line yes/no prompt the user
 *             resolves manually (via /channel confirm <id> [yes|no]).
 *   auto    — when the session is idle, schedule the configured prompt
 *             to run as a synthetic agent turn, rendered inside a
 *             clearly-marked auto-run envelope. Tool whitelist and
 *             rate-limit per rule enforce guard-rails.
 *
 * This package is intentionally agnostic of the CLI UI: the engine
 * emits a stream of Action values; the CLI subscribes and decides
 * how/where to render and how to gate on session state.
 *
 * Concurrency: the engine is safe for concurrent rule reloads and
 * concurrent Dispatch calls. Per-rule rate-limit state lives in
 * sync.Map so adding/removing rules at runtime never invalidates
 * other rules' state.
 */
package triggers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// defaultInvestigatePromptFmt is the agent prompt template used when
// a Rule omits its own Prompt. Kept as a package-level constant so
// the fallback can be tested independently and so the default
// stays in English — LLMs follow English instructions more reliably
// than translated copies.
const defaultInvestigatePromptFmt = "Investigate this %s/%s event:\n\n%s"

// Mode declares how the CLI should surface a fired trigger to the user.
type Mode string

const (
	// ModeNotify — banner only. Default and safest.
	ModeNotify Mode = "notify"
	// ModeConfirm — banner plus an explicit user yes/no.
	ModeConfirm Mode = "confirm"
	// ModeAuto — fully autonomous when session is idle.
	ModeAuto Mode = "auto"
)

// IsValid reports whether m is one of the three supported modes.
// Used by config validation so a typo in the rules file becomes
// a startup-time error rather than a silent fallback.
func (m Mode) IsValid() bool {
	switch m {
	case ModeNotify, ModeConfirm, ModeAuto:
		return true
	}
	return false
}

// Rule declares how a single trigger reacts to incoming events.
//
// Match semantics: an empty Server / Channel / ContentRegex matches
// everything. When multiple fields are specified, all must match
// (AND). When a Rule produces a fire, its Prompt template is
// rendered against the matched ChannelEvent.
//
// Tools is the optional whitelist of MCP/native tool names the
// auto-run session may invoke. Empty list means "no restriction"
// for backward-compatible behavior in notify/confirm modes; in
// auto mode an empty list is rejected by Validate because running
// the agent without a tool floor is a foot-gun.
type Rule struct {
	Name         string        `json:"name"`
	Server       string        `json:"server,omitempty"`
	Channel      string        `json:"channel,omitempty"`
	ContentRegex string        `json:"contentRegex,omitempty"`
	Mode         Mode          `json:"mode,omitempty"`
	Prompt       string        `json:"prompt,omitempty"`
	Tools        []string      `json:"tools,omitempty"`
	RateLimit    time.Duration `json:"-"` // populated from RateLimitText
	RateLimitTxt string        `json:"rateLimit,omitempty"`
	DedupWindow  time.Duration `json:"-"`
	DedupTxt     string        `json:"dedupWindow,omitempty"`

	// compiled state — populated by Validate / Compile, never written
	// from JSON.
	contentRe *regexp.Regexp
}

// ChannelEvent is the trigger engine's view of an incoming MCP message.
// Decoupled from cli/mcp.ChannelMessage so the engine has no import
// dependency back into the manager — keeps the package boundary clean.
type ChannelEvent struct {
	ServerName string
	Channel    string
	Content    string
	Metadata   map[string]string
	Timestamp  time.Time
	Seq        uint64
}

// Action is what the engine emits when a rule fires. The CLI
// listens on Engine.Actions() and decides UI behavior per mode.
//
// ID is unique per emitted Action (not per Rule) — the CLI uses it
// to address pending confirm actions when the user responds with
// /channel confirm <id>.
type Action struct {
	ID         uint64
	Rule       Rule
	Event      ChannelEvent
	Mode       Mode
	Prompt     string // already-rendered template, ready to feed the agent
	IssuedAt   time.Time
	ExpiresAt  time.Time // for confirm actions; zero means no expiration
	ToolFilter []string  // copy of Rule.Tools so consumers do not race
}

// Engine evaluates rules against incoming events and dispatches
// actions on a buffered channel for downstream UI/agent consumers.
//
// Lifecycle:
//
//	e := triggers.NewEngine(logger)
//	e.SetRules(initial)        // safe to call again on /config reload
//	e.Dispatch(event)          // wired to ChannelManager.OnMessage
//	for a := range e.Actions() { ... }
//	e.Close()                  // drains and closes the actions chan
type Engine struct {
	logger     *zap.Logger
	mu         sync.RWMutex
	rules      []Rule
	actions    chan Action
	paused     atomic.Bool
	actionSeq  atomic.Uint64
	rateLimit  sync.Map // rule name -> *atomic.Int64 holding lastFireUnixNano
	dedup      sync.Map // dedup key -> int64 lastFireUnixNano
	closeOnce  sync.Once
	closedFlag atomic.Bool
}

// EngineOptions configures the engine. All fields optional.
type EngineOptions struct {
	// ActionBuffer caps how many pending actions can be queued before
	// Dispatch starts dropping the oldest. Zero → 64, which is more
	// than enough for the bursty CI/Prometheus use case.
	ActionBuffer int
}

// NewEngine constructs an engine with default options.
func NewEngine(logger *zap.Logger) *Engine {
	return NewEngineWithOptions(logger, EngineOptions{})
}

// NewEngineWithOptions constructs an engine with explicit options.
func NewEngineWithOptions(logger *zap.Logger, opts EngineOptions) *Engine {
	buf := opts.ActionBuffer
	if buf <= 0 {
		buf = 64
	}
	return &Engine{
		logger:  logger,
		actions: make(chan Action, buf),
	}
}

// Actions exposes the read end of the action stream. Consumers MUST
// keep up — when the channel fills, the engine logs a warning and
// drops the newest action. (Confirm/Auto actions are not retried;
// the next matching event will produce a fresh Action.)
func (e *Engine) Actions() <-chan Action {
	return e.actions
}

// SetRules replaces the active rule set atomically. Returns the
// first validation error encountered, with no partial application
// — either every rule is accepted or none are.
//
// Safe to call concurrently with Dispatch. The next Dispatch picks
// up the new rules; in-flight ones continue with the snapshot they
// captured at entry.
func (e *Engine) SetRules(rules []Rule) error {
	compiled := make([]Rule, 0, len(rules))
	seenNames := make(map[string]struct{}, len(rules))
	for i, r := range rules {
		if err := r.validate(); err != nil {
			return fmt.Errorf("rule #%d (%q): %w", i, r.Name, err)
		}
		if _, dup := seenNames[r.Name]; dup {
			return fmt.Errorf("duplicate rule name %q", r.Name)
		}
		seenNames[r.Name] = struct{}{}
		cr, err := r.compile()
		if err != nil {
			return fmt.Errorf("rule %q: %w", r.Name, err)
		}
		compiled = append(compiled, cr)
	}
	e.mu.Lock()
	e.rules = compiled
	e.mu.Unlock()
	e.logger.Info("MCP trigger rules updated", zap.Int("count", len(compiled)))
	return nil
}

// Rules returns a snapshot of the active rule set. Useful for
// /channel rules introspection.
func (e *Engine) Rules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Rule, len(e.rules))
	copy(out, e.rules)
	return out
}

// Pause suspends all trigger evaluation until Resume is called.
// Dispatch keeps draining events so the channel never blocks, but
// no Actions are emitted while paused.
func (e *Engine) Pause()  { e.paused.Store(true) }
func (e *Engine) Resume() { e.paused.Store(false) }

// IsPaused reports whether the engine is currently suspended.
func (e *Engine) IsPaused() bool { return e.paused.Load() }

// Dispatch runs each rule against the event and emits an Action for
// every match (subject to rate-limit and dedup). Safe to call from
// multiple goroutines — typically wired directly to
// ChannelManager.OnMessage.
func (e *Engine) Dispatch(event ChannelEvent) {
	if e.closedFlag.Load() {
		return
	}
	if e.paused.Load() {
		return
	}
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()

	for _, rule := range rules {
		if !rule.matches(event) {
			continue
		}
		if !e.allowAfterRateLimit(rule) {
			e.logger.Debug("trigger rate-limited",
				zap.String("rule", rule.Name),
				zap.Duration("window", rule.RateLimit))
			continue
		}
		if !e.allowAfterDedup(rule, event) {
			e.logger.Debug("trigger deduped",
				zap.String("rule", rule.Name),
				zap.Uint64("seq", event.Seq))
			continue
		}
		e.emit(rule, event)
	}
}

// emit constructs an Action and pushes it onto the actions channel.
// When the channel is full we drop the action and log at warn — the
// alternative (block in Dispatch) would back-pressure the entire
// ChannelManager handler chain, which is a worse failure mode.
func (e *Engine) emit(rule Rule, event ChannelEvent) {
	rendered := rule.renderPrompt(event)
	action := Action{
		ID:         e.actionSeq.Add(1),
		Rule:       rule,
		Event:      event,
		Mode:       rule.effectiveMode(),
		Prompt:     rendered,
		IssuedAt:   time.Now().UTC(),
		ToolFilter: append([]string(nil), rule.Tools...),
	}
	if action.Mode == ModeConfirm {
		// Confirm prompts expire so a forgotten one does not sit in
		// the queue forever. 30 min is generous for "user looked
		// away briefly" while still bounding state growth.
		action.ExpiresAt = action.IssuedAt.Add(30 * time.Minute)
	}

	select {
	case e.actions <- action:
		e.logger.Info("MCP trigger fired",
			zap.String("rule", rule.Name),
			zap.String("mode", string(action.Mode)),
			zap.Uint64("event_seq", event.Seq),
			zap.Uint64("action_id", action.ID))
	default:
		e.logger.Warn("MCP trigger action dropped (queue full)",
			zap.String("rule", rule.Name),
			zap.String("mode", string(action.Mode)))
	}
}

// allowAfterRateLimit reports whether the rule's rate-limit window
// has elapsed since the last fire. Zero RateLimit means "no limit".
// Implementation: atomic compare-and-swap on a unixNano timestamp;
// no lock needed even under heavy concurrent dispatch.
func (e *Engine) allowAfterRateLimit(rule Rule) bool {
	if rule.RateLimit <= 0 {
		return true
	}
	now := time.Now().UnixNano()
	v, _ := e.rateLimit.LoadOrStore(rule.Name, new(atomic.Int64))
	last := v.(*atomic.Int64)
	prev := last.Load()
	if prev > 0 && time.Duration(now-prev) < rule.RateLimit {
		return false
	}
	return last.CompareAndSwap(prev, now)
}

// allowAfterDedup reports whether this (rule, event content)
// combination has been seen within the dedup window. Uses a hash-
// like key over rule name + content prefix to keep memory bounded
// regardless of message variety.
func (e *Engine) allowAfterDedup(rule Rule, event ChannelEvent) bool {
	if rule.DedupWindow <= 0 {
		return true
	}
	key := rule.Name + "\x00" + truncateForKey(event.Content)
	now := time.Now().UnixNano()
	v, loaded := e.dedup.LoadOrStore(key, now)
	if !loaded {
		return true
	}
	prev := v.(int64)
	if time.Duration(now-prev) < rule.DedupWindow {
		return false
	}
	e.dedup.Store(key, now)
	return true
}

// truncateForKey caps the content portion of a dedup key so a
// kilobyte-long payload doesn't dominate memory.
func truncateForKey(s string) string {
	const maxKeyLen = 256
	if len(s) <= maxKeyLen {
		return s
	}
	return s[:maxKeyLen]
}

// Close drains and closes the actions channel. Idempotent — safe to
// call from the CLI shutdown path even if the engine was never
// started.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		e.closedFlag.Store(true)
		close(e.actions)
	})
}

// --- Rule helpers ---------------------------------------------------------

// effectiveMode resolves the rule's mode, defaulting to notify
// when the user omitted the field in the rule file.
func (r Rule) effectiveMode() Mode {
	if r.Mode == "" {
		return ModeNotify
	}
	return r.Mode
}

// matches reports whether a single ChannelEvent satisfies every
// non-empty field on the rule.
func (r Rule) matches(event ChannelEvent) bool {
	if r.Server != "" && r.Server != event.ServerName && r.Server != "*" {
		return false
	}
	if r.Channel != "" && !channelMatches(r.Channel, event.Channel) {
		return false
	}
	if r.contentRe != nil && !r.contentRe.MatchString(event.Content) {
		return false
	}
	return true
}

// channelMatches supports literal name match, "*" wildcard, and a
// trailing "*" prefix glob (e.g. "alerts/*"). The prefix glob keeps
// configs concise when a server emits many sub-channels.
func channelMatches(pattern, channel string) bool {
	if pattern == "*" || pattern == channel {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		return strings.HasPrefix(channel, prefix+"/") || channel == prefix
	}
	return false
}

// renderPrompt expands the rule's Prompt template against the
// event. Supported variables: {{content}}, {{channel}}, {{server}},
// {{seq}}, {{timestamp}}. Unknown variables are left as-is so a
// typo is visible rather than silently dropped.
func (r Rule) renderPrompt(event ChannelEvent) string {
	if r.Prompt == "" {
		// Default prompt: ask the agent to investigate. Mode and
		// tool filter still apply.
		return fmt.Sprintf(defaultInvestigatePromptFmt,
			event.ServerName, event.Channel, event.Content)
	}
	replacer := strings.NewReplacer(
		"{{content}}", event.Content,
		"{{channel}}", event.Channel,
		"{{server}}", event.ServerName,
		"{{seq}}", fmt.Sprintf("%d", event.Seq),
		"{{timestamp}}", event.Timestamp.Format(time.RFC3339),
	)
	return replacer.Replace(r.Prompt)
}

// validate checks structural correctness of the rule. Compile-time
// errors (bad regex) are deferred to compile() so we can report
// them with consistent wrapping.
func (r *Rule) validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("rule name is required")
	}
	mode := r.effectiveMode()
	if !mode.IsValid() {
		return fmt.Errorf("invalid mode %q (want notify|confirm|auto)", r.Mode)
	}
	if mode == ModeAuto && len(r.Tools) == 0 {
		return errors.New(`mode "auto" requires a non-empty tools whitelist`)
	}
	if mode != ModeNotify && strings.TrimSpace(r.Prompt) == "" {
		return fmt.Errorf("mode %q requires a prompt template", mode)
	}
	return nil
}

// compile finalizes parsed text fields. Currently only ContentRegex
// needs compilation; future additions slot in here.
func (r Rule) compile() (Rule, error) {
	out := r
	if out.RateLimitTxt != "" && out.RateLimit == 0 {
		d, err := time.ParseDuration(out.RateLimitTxt)
		if err != nil {
			return out, fmt.Errorf("invalid rateLimit %q: %w", out.RateLimitTxt, err)
		}
		out.RateLimit = d
	}
	if out.DedupTxt != "" && out.DedupWindow == 0 {
		d, err := time.ParseDuration(out.DedupTxt)
		if err != nil {
			return out, fmt.Errorf("invalid dedupWindow %q: %w", out.DedupTxt, err)
		}
		out.DedupWindow = d
	}
	if out.ContentRegex != "" {
		re, err := regexp.Compile(out.ContentRegex)
		if err != nil {
			return out, fmt.Errorf("invalid contentRegex: %w", err)
		}
		out.contentRe = re
	}
	return out, nil
}
