/*
 * ChatCLI - MCP channel trigger plumbing
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Glue between cli.mcp.ChannelManager + cli.mcp.triggers.Engine and
 * the user-facing CLI loop. Three responsibilities:
 *
 *   1. Boot the trigger engine, load rules from
 *      ~/.chatcli/mcp/triggers.json, and wire OnMessage into it.
 *   2. Consume engine.Actions(), separating them by Mode:
 *        - notify   → push onto pendingNotify ring, emit one-line
 *                     toast on stderr.
 *        - confirm  → push onto pendingConfirm map, emit toast with
 *                     /channel confirm <id> hint.
 *        - auto     → push onto pendingAuto queue; drained at the
 *                     top of the next executor tick by
 *                     drainPendingAutoTriggers (analogous to
 *                     drainPendingResumes for parked agents).
 *   3. Render the inbox banner at the top of each executor cycle.
 *
 * The /channel command file (channel_command.go) reads from the
 * pending stores to implement list/ack/pause/resume/rules/confirm/run.
 */
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/mcp/triggers"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// manualRunPromptFmt is the agent prompt used by `/channel run <seq>`
// when the user manually fires the agent on a stored message that
// has no rule attached. English on purpose — LLMs follow English
// instructions more reliably than translations.
const manualRunPromptFmt = "Investigate this MCP channel event from %s/%s:\n\n%s"

// channelTriggerState holds the runtime state for MCP channel
// reactive triggers. Lives on *ChatCLI as cli.channelTriggers.
//
// Concurrency: every public field is guarded by its own mutex —
// the consumer goroutine, the executor tick, and arbitrary
// /channel subcommands all touch this state without coordinating
// elsewhere.
type channelTriggerState struct {
	engine    *triggers.Engine
	rulesPath string

	mu             sync.Mutex
	pendingNotify  []triggers.Action          // FIFO, drained by /channel ack
	pendingConfirm map[uint64]triggers.Action // keyed by action ID
	pendingAuto    []triggers.Action          // drained by the executor tick
	processedIDs   map[uint64]struct{}        // dedup against double-drain
}

// initChannelTriggers boots the trigger engine for the active session.
// Safe to call repeatedly; the second call is a no-op so MCP hot-reload
// paths can invoke it without leaking goroutines.
//
// Engine wiring:
//   - Rules loaded from ~/.chatcli/mcp/triggers.json (best-effort,
//     missing file is fine — engine starts with zero rules).
//   - ChannelManager.OnMessage feeds every incoming push event.
//   - A consumer goroutine drains engine.Actions() into the three
//     pending stores. It exits when the engine closes.
func (cli *ChatCLI) initChannelTriggers() {
	if cli.mcpManager == nil {
		return
	}
	if cli.channelTriggers != nil {
		return
	}

	st := &channelTriggerState{
		engine:         triggers.NewEngine(cli.logger),
		pendingConfirm: make(map[uint64]triggers.Action),
		processedIDs:   make(map[uint64]struct{}),
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		st.rulesPath = filepath.Join(home, ".chatcli", "mcp", "triggers.json")
	}

	if err := cli.loadChannelTriggerRules(st); err != nil {
		cli.logger.Warn("MCP trigger rules load failed (rules disabled)",
			zap.String("path", st.rulesPath),
			zap.Error(err))
	}

	// Bridge: ChannelManager.OnMessage → engine.Dispatch.
	cli.mcpManager.Channels().OnMessage(func(msg mcp.ChannelMessage) {
		st.engine.Dispatch(triggers.ChannelEvent{
			ServerName: msg.ServerName,
			Channel:    msg.Channel,
			Content:    msg.Content,
			Metadata:   msg.Metadata,
			Timestamp:  msg.Timestamp,
			Seq:        msg.Seq,
		})
	})

	cli.channelTriggers = st
	go cli.runChannelTriggerConsumer(st)
}

// loadChannelTriggerRules reads the rules file (if present), parses
// the JSON, and applies it to the engine. Returns the first error
// encountered so the caller can log it; the engine remains usable
// with whatever rules it had before the failed call.
func (cli *ChatCLI) loadChannelTriggerRules(st *channelTriggerState) error {
	if st.rulesPath == "" {
		return nil
	}
	data, err := os.ReadFile(st.rulesPath) //#nosec G304 -- user-controlled config path inside ~/.chatcli
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read triggers file: %w", err)
	}
	var parsed struct {
		Rules []triggers.Rule `json:"rules"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("parse triggers file: %w", err)
	}
	if err := st.engine.SetRules(parsed.Rules); err != nil {
		return fmt.Errorf("apply triggers: %w", err)
	}
	cli.logger.Info("MCP trigger rules loaded",
		zap.String("path", st.rulesPath),
		zap.Int("rules", len(parsed.Rules)))
	return nil
}

// reloadChannelTriggerRules is the public-facing reload — used by
// the /channel rules reload subcommand. Returns the count of active
// rules so the command handler can render a confirmation line.
func (cli *ChatCLI) reloadChannelTriggerRules() (int, error) {
	if cli.channelTriggers == nil {
		return 0, errors.New("MCP triggers not initialized")
	}
	if err := cli.loadChannelTriggerRules(cli.channelTriggers); err != nil {
		return 0, err
	}
	return len(cli.channelTriggers.engine.Rules()), nil
}

// runChannelTriggerConsumer is the bridge between engine.Actions()
// and the pending stores. Runs for the lifetime of the engine —
// terminates when the channel is closed by engine.Close().
func (cli *ChatCLI) runChannelTriggerConsumer(st *channelTriggerState) {
	for action := range st.engine.Actions() {
		cli.handleChannelTriggerAction(st, action)
	}
}

// handleChannelTriggerAction routes a single Action into the
// appropriate pending store and emits an immediate toast on stderr.
// Stderr is chosen on purpose: go-prompt owns stdout for its input
// rendering, and writing to stdout from a background goroutine
// fights the prompt redraw. Stderr stays out of go-prompt's way
// while still landing on the user's terminal.
func (cli *ChatCLI) handleChannelTriggerAction(st *channelTriggerState, action triggers.Action) {
	st.mu.Lock()
	if _, seen := st.processedIDs[action.ID]; seen {
		st.mu.Unlock()
		return
	}
	st.processedIDs[action.ID] = struct{}{}

	switch action.Mode {
	case triggers.ModeNotify:
		st.pendingNotify = append(st.pendingNotify, action)
	case triggers.ModeConfirm:
		st.pendingConfirm[action.ID] = action
	case triggers.ModeAuto:
		st.pendingAuto = append(st.pendingAuto, action)
	}
	st.mu.Unlock()

	cli.emitChannelTriggerToast(action)
}

// emitChannelTriggerToast prints a one-line summary of an action
// to stderr. Kept concise so it does not visually disrupt typing.
func (cli *ChatCLI) emitChannelTriggerToast(action triggers.Action) {
	preview := truncateStr(strings.ReplaceAll(action.Event.Content, "\n", " "), 80)
	header := i18n.T("chan.trigger.toast_header", action.Mode, action.Rule.Name)
	switch action.Mode {
	case triggers.ModeNotify:
		_, _ = fmt.Fprintf(os.Stderr, "\n%s [%s/%s] %s\n",
			colorize("📡 "+header, ColorPurple),
			action.Event.ServerName, action.Event.Channel, preview)
	case triggers.ModeConfirm:
		_, _ = fmt.Fprintf(os.Stderr, "\n%s [%s/%s] %s\n  %s\n",
			colorize("⚠ "+header, ColorYellow),
			action.Event.ServerName, action.Event.Channel, preview,
			colorize(i18n.T("chan.trigger.toast_confirm_hint", action.ID), ColorGray))
	case triggers.ModeAuto:
		_, _ = fmt.Fprintf(os.Stderr, "\n%s [%s/%s] %s\n  %s\n",
			colorize("🤖 "+header, ColorCyan),
			action.Event.ServerName, action.Event.Channel, preview,
			colorize(i18n.T("chan.trigger.toast_auto_hint"), ColorGray))
	}
}

// renderChannelTriggerBanner prints the inbox-style banner of
// pending notify + confirm actions, plus the unread channel count.
// Called from the executor at the very top of each cycle so the
// user sees the queue before they decide what to do next.
//
// Returns true when anything was printed so the caller can flush
// stdout before go-prompt redraws.
func (cli *ChatCLI) renderChannelTriggerBanner() bool {
	if cli.channelTriggers == nil {
		return false
	}
	st := cli.channelTriggers
	st.mu.Lock()
	notify := append([]triggers.Action(nil), st.pendingNotify...)
	confirm := make([]triggers.Action, 0, len(st.pendingConfirm))
	for _, a := range st.pendingConfirm {
		confirm = append(confirm, a)
	}
	st.mu.Unlock()

	unread := cli.mcpManager.Channels().Unread()
	if len(notify) == 0 && len(confirm) == 0 && unread == 0 {
		return false
	}

	fmt.Println()
	fmt.Println(uiBox("📡", i18n.T("chan.banner.title"), ColorPurple))
	p := uiPrefix(ColorPurple)
	if unread > 0 {
		fmt.Println(p + colorize(fmt.Sprintf(i18n.T("chan.banner.unread"), unread), ColorYellow))
	}
	for _, a := range notify {
		fmt.Println(p + renderTriggerLine(a))
	}
	for _, a := range confirm {
		fmt.Println(p + colorize("⚠ ", ColorYellow) + renderTriggerLine(a) +
			colorize(fmt.Sprintf("  /channel confirm %d", a.ID), ColorGray))
	}
	if len(notify)+len(confirm) > 0 {
		fmt.Println(p)
		fmt.Println(p + colorize(i18n.T("chan.banner.hint_ack"), ColorGray))
	}
	fmt.Println(uiBoxEnd(ColorPurple))
	fmt.Println()
	return true
}

// renderTriggerLine formats a single action for the inbox banner.
func renderTriggerLine(a triggers.Action) string {
	preview := truncateStr(strings.ReplaceAll(a.Event.Content, "\n", " "), 70)
	return fmt.Sprintf("[%s/%s] %s%s%s %s",
		a.Event.ServerName, a.Event.Channel,
		ColorGray, a.Rule.Name, ColorReset,
		preview)
}

// drainPendingAutoTriggers fires every queued auto-trigger by
// running the agent on its rendered prompt. Returns true when at
// least one auto-trigger was processed so the executor can skip
// the redraw cycle for one tick (mirrors drainPendingResumes).
//
// Important: this runs FOREGROUND. The agent loop owns the
// terminal for the duration. This is intentional — auto triggers
// are explicit opt-in (the user wrote a rule with Mode=auto and a
// tool whitelist) and the user expects to see the work happen.
func (cli *ChatCLI) drainPendingAutoTriggers() bool {
	if cli.channelTriggers == nil {
		return false
	}
	st := cli.channelTriggers
	st.mu.Lock()
	if len(st.pendingAuto) == 0 {
		st.mu.Unlock()
		return false
	}
	queued := st.pendingAuto
	st.pendingAuto = nil
	st.mu.Unlock()

	processed := false
	for _, action := range queued {
		cli.runAutoTriggerAction(action)
		processed = true
	}
	return processed
}

// runAutoTriggerAction renders the auto-run envelope and feeds the
// agent loop with the rendered prompt. The envelope makes it visually
// unambiguous that the work was started by a trigger, not by user
// input.
func (cli *ChatCLI) runAutoTriggerAction(action triggers.Action) {
	header := fmt.Sprintf(i18n.T("chan.trigger.auto_header"),
		action.Rule.Name, action.Event.ServerName, action.Event.Channel)
	fmt.Println()
	fmt.Println(uiBox("🤖", header, ColorCyan))
	p := uiPrefix(ColorCyan)
	fmt.Println(p + colorize(truncateStr(action.Event.Content, 200), ColorGray))
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()

	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}
	cli.agentMode.isOneShot = false // preserve prior behavior (Run used to force this)
	cli.runWithCancellation("MCP Auto-Trigger", func(ctx context.Context) error {
		// Auto triggers always run in agent mode (CoderSystemPrompt
		// would constrain too heavily for free-form investigation).
		// Tool whitelist is enforced upstream via the agent's tool
		// approval gate when the rule declares one.
		return cli.agentMode.Run(ctx, action.Prompt, "", "")
	})
}

// channelTriggerConfirm marks a pending confirm action as accepted
// (or denied) and, on acceptance, runs the agent on the rule's
// prompt. id matches Action.ID; the second arg is true for "yes".
func (cli *ChatCLI) channelTriggerConfirm(id uint64, accept bool) error {
	if cli.channelTriggers == nil {
		return errors.New("MCP triggers not initialized")
	}
	st := cli.channelTriggers
	st.mu.Lock()
	action, ok := st.pendingConfirm[id]
	if ok {
		delete(st.pendingConfirm, id)
	}
	st.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending confirm action with id %d", id)
	}
	if !accept {
		cli.logger.Info("MCP confirm action declined",
			zap.Uint64("action_id", id),
			zap.String("rule", action.Rule.Name))
		return nil
	}
	// Promote to an auto-style execution so the user sees the
	// auto-run envelope around the agent's work.
	cli.runAutoTriggerAction(action)
	return nil
}

// channelTriggerRun forces a /channel run <seq> — the user asks
// chatcli to investigate a specific channel message manually, using
// the default rule prompt (since no rule matched in `notify` form).
// Returns an error when the seq does not exist in the ring.
func (cli *ChatCLI) channelTriggerRun(seq uint64) error {
	if cli.mcpManager == nil {
		return errors.New("MCP not enabled")
	}
	msg, ok := cli.mcpManager.Channels().GetBySeq(seq)
	if !ok {
		return fmt.Errorf("no channel message with seq %d", seq)
	}
	action := triggers.Action{
		ID: 0,
		Rule: triggers.Rule{
			Name: "manual",
			Mode: triggers.ModeAuto,
		},
		Event: triggers.ChannelEvent{
			ServerName: msg.ServerName,
			Channel:    msg.Channel,
			Content:    msg.Content,
			Metadata:   msg.Metadata,
			Timestamp:  msg.Timestamp,
			Seq:        msg.Seq,
		},
		Mode:     triggers.ModeAuto,
		IssuedAt: time.Now().UTC(),
		Prompt:   fmt.Sprintf(manualRunPromptFmt, msg.ServerName, msg.Channel, msg.Content),
	}
	cli.runAutoTriggerAction(action)
	return nil
}

// channelTriggerAck drops the pending notify ring and acknowledges
// every unread message. Returns the number of items cleared so the
// command can render a one-line summary.
func (cli *ChatCLI) channelTriggerAck() (int, int) {
	notifyCleared := 0
	if cli.channelTriggers != nil {
		cli.channelTriggers.mu.Lock()
		notifyCleared = len(cli.channelTriggers.pendingNotify)
		cli.channelTriggers.pendingNotify = nil
		cli.channelTriggers.mu.Unlock()
	}
	unread := 0
	if cli.mcpManager != nil {
		unread = cli.mcpManager.Channels().Ack()
	}
	return notifyCleared, unread
}

// channelTriggerPause / channelTriggerResume toggle the engine.
// Idempotent — repeating a call has no extra effect.
func (cli *ChatCLI) channelTriggerPause() {
	if cli.channelTriggers != nil {
		cli.channelTriggers.engine.Pause()
	}
}

func (cli *ChatCLI) channelTriggerResume() {
	if cli.channelTriggers != nil {
		cli.channelTriggers.engine.Resume()
	}
}

// channelTriggerIsPaused reports the engine's pause state. nil-safe.
func (cli *ChatCLI) channelTriggerIsPaused() bool {
	if cli.channelTriggers == nil {
		return false
	}
	return cli.channelTriggers.engine.IsPaused()
}

// channelTriggerRules returns a snapshot of the active rule set.
func (cli *ChatCLI) channelTriggerRules() []triggers.Rule {
	if cli.channelTriggers == nil {
		return nil
	}
	return cli.channelTriggers.engine.Rules()
}

// shutdownChannelTriggers closes the engine and waits briefly for the
// consumer goroutine to drain. Idempotent. Mirrors the shutdown
// pattern used by other CLI subsystems (scheduler, mcp).
func (cli *ChatCLI) shutdownChannelTriggers() {
	if cli.channelTriggers == nil {
		return
	}
	cli.channelTriggers.engine.Close()
}
