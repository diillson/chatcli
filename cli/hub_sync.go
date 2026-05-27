/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// hubChannelLocal is the channel name the connected CLI tags its own turns
// with. Tailed events on this channel are our own writes and are not
// re-rendered (they're already on screen and in local history).
const hubChannelLocal = "local"

// HubClient is the subset of the remote client the CLI needs to share a
// conversation through the hub. *client/remote.Client satisfies it; defining it
// here keeps the cli package free of a hard dependency on the remote package.
type HubClient interface {
	ResolveActiveConversation(ctx context.Context, principal string) (convID, principal2 string, err error)
	NewConversation(ctx context.Context, principal string) (string, error)
	AppendEvent(ctx context.Context, ev models.ConversationEvent) (models.ConversationEvent, error)
	ReadConversation(ctx context.Context, convID string, sinceSeq int64, limit int) ([]models.ConversationEvent, error)
	SubscribeConversation(ctx context.Context, convID string, sinceSeq int64) (<-chan models.ConversationEvent, error)
	SetBinding(ctx context.Context, platform, userID, principal string) error
	ListBindings(ctx context.Context, principal string) ([]models.HubBinding, error)
}

// HubSync keeps a connected CLI in lock-step with the shared cross-channel
// conversation: it hydrates history on connect, appends each local turn, live-
// tails turns that arrive from other channels (Telegram/Slack/…), and rotates
// the conversation on /newsession. All methods are safe to call when the hub is
// unavailable — they degrade to no-ops so the REPL never blocks on the hub.
type HubSync struct {
	client HubClient
	logger *zap.Logger
	render func(models.ConversationEvent) // prints a tailed event from another channel

	mu        sync.Mutex
	convID    string
	principal string
	lastSeq   int64
	subCancel context.CancelFunc // cancels the in-flight subscription (used to restart on rotation)
}

func newHubSync(client HubClient, logger *zap.Logger, render func(models.ConversationEvent)) *HubSync {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &HubSync{client: client, logger: logger, render: render}
}

// hydrate resolves the principal's active conversation and returns its history
// as messages to seed cli.history. It records the conversation id and the
// highest seq seen so the tail resumes from there.
func (hs *HubSync) hydrate(ctx context.Context) ([]models.Message, error) {
	convID, principal, err := hs.client.ResolveActiveConversation(ctx, "")
	if err != nil {
		return nil, err
	}
	events, err := hs.client.ReadConversation(ctx, convID, 0, 0)
	if err != nil {
		return nil, err
	}

	hs.mu.Lock()
	hs.convID = convID
	hs.principal = principal
	hs.lastSeq = 0
	msgs := make([]models.Message, 0, len(events))
	for _, ev := range events {
		if ev.Seq > hs.lastSeq {
			hs.lastSeq = ev.Seq
		}
		msgs = append(msgs, ev.ToMessage())
	}
	hs.mu.Unlock()
	return msgs, nil
}

// afterChatTurn records a completed local chat turn on the shared conversation
// so other channels (and future sessions) see it.
func (hs *HubSync) afterChatTurn(ctx context.Context, userText, assistantText string) {
	hs.mu.Lock()
	convID := hs.convID
	hs.mu.Unlock()
	if convID == "" {
		return
	}
	hs.append(ctx, convID, models.ConvRoleUser, userText)
	hs.append(ctx, convID, models.ConvRoleAssistant, assistantText)
}

func (hs *HubSync) append(ctx context.Context, convID, role, content string) {
	if content == "" {
		return
	}
	stored, err := hs.client.AppendEvent(ctx, models.ConversationEvent{
		ConvID:  convID,
		Channel: hubChannelLocal,
		Role:    role,
		Content: content,
	})
	if err != nil {
		hs.logger.Warn("hub sync: append failed", zap.String("role", role), zap.Error(err))
		return
	}
	hs.mu.Lock()
	if stored.Seq > hs.lastSeq {
		hs.lastSeq = stored.Seq
	}
	hs.mu.Unlock()
}

// newSession rotates the shared conversation to a fresh thread, propagated to
// every channel resolving the same principal. The live tail restarts on the new
// conversation.
func (hs *HubSync) newSession(ctx context.Context) error {
	convID, err := hs.client.NewConversation(ctx, "")
	if err != nil {
		return err
	}
	hs.mu.Lock()
	hs.convID = convID
	hs.lastSeq = 0
	cancel := hs.subCancel
	hs.mu.Unlock()
	if cancel != nil {
		cancel() // break the current subscription so the tail resubscribes on the new conversation
	}
	return nil
}

// startTail runs the live-tail loop until ctx is cancelled, re-subscribing on
// stream end (server resync signal) and on conversation rotation. It renders
// only events that arrived from other channels.
func (hs *HubSync) startTail(ctx context.Context) {
	go func() {
		for ctx.Err() == nil {
			hs.mu.Lock()
			convID := hs.convID
			since := hs.lastSeq
			subCtx, cancel := context.WithCancel(ctx)
			hs.subCancel = cancel
			hs.mu.Unlock()

			if convID == "" {
				cancel()
				return
			}

			stream, err := hs.client.SubscribeConversation(subCtx, convID, since)
			if err != nil {
				cancel()
				if !sleepCtx(ctx, 2*time.Second) {
					return
				}
				continue
			}

			for ev := range stream {
				hs.mu.Lock()
				if ev.Seq > hs.lastSeq {
					hs.lastSeq = ev.Seq
				}
				current := hs.convID
				hs.mu.Unlock()
				if ev.ConvID != current || ev.Channel == hubChannelLocal {
					continue // stale (rotated) or our own local turn
				}
				if hs.render != nil {
					hs.render(ev)
				}
			}
			cancel()
			// Stream ended: either ctx cancellation, rotation, or an overflow
			// resync. A short pause avoids a hot loop before resubscribing.
			if !sleepCtx(ctx, 200*time.Millisecond) {
				return
			}
		}
	}()
}

// sleepCtx sleeps for d, returning false if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// status reports the live sync state for display in /config hub.
func (hs *HubSync) status() (convID, principal string) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return hs.convID, hs.principal
}

// bind maps a channel identity to a principal (empty principal = self) via the
// hub server.
func (hs *HubSync) bind(ctx context.Context, platform, userID, principal string) error {
	return hs.client.SetBinding(ctx, platform, userID, principal)
}

// bindings returns the channel→principal bindings visible to the caller.
func (hs *HubSync) bindings(ctx context.Context, principal string) ([]models.HubBinding, error) {
	return hs.client.ListBindings(ctx, principal)
}

// --- ChatCLI integration ---

// EnableHubSync wires a connected CLI to the shared conversation hub. Call it
// after setting cli.Client to the remote client and before Start. A render
// function prints tailed events from other channels.
func (cli *ChatCLI) EnableHubSync(client HubClient) {
	cli.hubSync = newHubSync(client, cli.logger, cli.renderTailedEvent)
}

// startHubSync hydrates the shared conversation into local history and starts
// the live tail. Invoked from Start when hub sync is enabled. Hydration
// failures degrade to a fresh local session (the hub may simply be disabled
// server-side).
func (cli *ChatCLI) startHubSync(ctx context.Context) {
	if cli.hubSync == nil {
		return
	}
	msgs, err := cli.hubSync.hydrate(ctx)
	if err != nil {
		cli.logger.Warn("hub sync: hydrate failed; starting fresh", zap.Error(err))
		cli.hubSync = nil // hub unavailable: disable sync for this session
		return
	}
	if len(msgs) > 0 {
		// Seed local history so the model has the cross-channel context.
		cli.history = append(cli.history, msgs...)
		fmt.Println(colorize("  "+i18n.T("hub.resumed", len(msgs)), ColorGray))
	}
	cli.hubSync.startTail(ctx)
}

// renderTailedEvent prints a conversation turn that arrived from another channel
// while the user is at the local prompt, tagged with its origin.
func (cli *ChatCLI) renderTailedEvent(ev models.ConversationEvent) {
	cli.history = append(cli.history, ev.ToMessage())
	label := i18n.T("hub.via_channel", ev.Channel)
	role := ev.Role
	fmt.Printf("\n%s %s\n", colorize(label, ColorYellow), colorize(role+":", ColorGray))
	fmt.Println("  " + ev.Content)
}
