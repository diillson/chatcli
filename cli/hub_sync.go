/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"sync"

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
// conversation: it hydrates history on connect, mirrors each local turn, and
// pulls turns that arrived on other channels (Telegram/Slack/…) into history at
// the start of the next turn so the model has context — without printing them,
// which would fight the prompt. /newsession rotates the conversation. All
// methods are safe to call when the hub is unavailable (no-ops), so the REPL
// never blocks on the hub.
type HubSync struct {
	client HubClient
	logger *zap.Logger

	mu        sync.Mutex
	convID    string
	principal string
	lastSeq   int64
}

func newHubSync(client HubClient, logger *zap.Logger) *HubSync {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &HubSync{client: client, logger: logger}
}

// startFresh begins a new shared conversation for this CLI session (ephemeral
// model): it rotates the principal to a fresh thread — which prunes the prior
// one — so we never load an old conversation or let the database grow. The hub
// is a momentary cross-channel bridge; long-term recall lives in the memory
// system, not here.
func (hs *HubSync) startFresh(ctx context.Context) error {
	_, principal, err := hs.client.ResolveActiveConversation(ctx, "")
	if err != nil {
		return err
	}
	convID, err := hs.client.NewConversation(ctx, "")
	if err != nil {
		return err
	}
	hs.mu.Lock()
	hs.convID = convID
	hs.principal = principal
	hs.lastSeq = 0
	hs.mu.Unlock()
	return nil
}

// mirrorTurn records a completed local turn (chat, agent or coder) on the shared
// conversation so other channels — and future sessions — continue from it.
func (hs *HubSync) mirrorTurn(ctx context.Context, userText, assistantText string) {
	hs.mu.Lock()
	convID := hs.convID
	hs.mu.Unlock()
	if convID == "" {
		return
	}
	hs.append(ctx, convID, models.ConvRoleUser, userText)
	hs.append(ctx, convID, models.ConvRoleAssistant, assistantText)
}

// pull fetches turns appended since the last sync (by any channel except our
// own local writes) so the caller can splice them into history before the next
// turn. It advances the seen-sequence watermark. Safe to call on the turn's
// goroutine; returns nil when nothing is new or the hub is idle.
func (hs *HubSync) pull(ctx context.Context) []models.Message {
	hs.mu.Lock()
	convID := hs.convID
	since := hs.lastSeq
	hs.mu.Unlock()
	if convID == "" {
		return nil
	}
	events, err := hs.client.ReadConversation(ctx, convID, since, 0)
	if err != nil {
		hs.logger.Warn("hub sync: pull failed", zap.Error(err))
		return nil
	}
	msgs := make([]models.Message, 0, len(events))
	hs.mu.Lock()
	for _, ev := range events {
		if ev.Seq > hs.lastSeq {
			hs.lastSeq = ev.Seq
		}
		if ev.Channel == hubChannelLocal {
			continue // our own turns are already in local history
		}
		msgs = append(msgs, ev.ToMessage())
	}
	hs.mu.Unlock()
	return msgs
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
// every channel resolving the same principal on their next turn.
func (hs *HubSync) newSession(ctx context.Context) error {
	convID, err := hs.client.NewConversation(ctx, "")
	if err != nil {
		return err
	}
	hs.mu.Lock()
	hs.convID = convID
	hs.lastSeq = 0
	hs.mu.Unlock()
	return nil
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

// errHubSettingsLocalOnly is returned when runtime settings are mutated on a
// remote-connected session; settings live in the local/server db.
var errHubSettingsLocalOnly = errors.New("hub settings can only be changed in local hub mode")

// hubSettingsClient is the optional capability of a store-backed HubClient:
// runtime settings persisted in the shared db (read live by the gateway). The
// remote client does not implement it, so /config hub mutation is local-only.
type hubSettingsClient interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
	DeleteSetting(ctx context.Context, key string) error
	AllSettings(ctx context.Context) (map[string]string, error)
}

// allSettings returns the stored runtime settings, or (nil,false) when the
// session can't manage them (remote mode).
func (hs *HubSync) allSettings(ctx context.Context) (map[string]string, bool) {
	sc, ok := hs.client.(hubSettingsClient)
	if !ok {
		return nil, false
	}
	m, err := sc.AllSettings(ctx)
	if err != nil {
		hs.logger.Warn("hub: read settings failed", zap.Error(err))
		return nil, false
	}
	return m, true
}

func (hs *HubSync) setSetting(ctx context.Context, key, value string) error {
	sc, ok := hs.client.(hubSettingsClient)
	if !ok {
		return errHubSettingsLocalOnly
	}
	return sc.SetSetting(ctx, key, value)
}

func (hs *HubSync) resetSetting(ctx context.Context, key string) error {
	sc, ok := hs.client.(hubSettingsClient)
	if !ok {
		return errHubSettingsLocalOnly
	}
	return sc.DeleteSetting(ctx, key)
}

// --- ChatCLI integration ---

// EnableHubSync wires a connected CLI to the shared conversation hub. Call it
// after setting cli.Client to the remote client and before Start.
func (cli *ChatCLI) EnableHubSync(client HubClient) {
	cli.hubSync = newHubSync(client, cli.logger)
}

// startHubSync establishes a fresh shared conversation for this CLI session.
// Invoked from Start. A standalone CLI (no /connect) joins the on-disk hub when
// local mode is enabled, sharing the conversation with a co-running gateway
// daemon. The conversation is ephemeral per session: nothing old is loaded and
// the previous thread is pruned, so the database stays bounded.
func (cli *ChatCLI) startHubSync(ctx context.Context) {
	if cli.hubSync == nil {
		cli.hubLocalClose = cli.maybeEnableLocalHub(ctx)
	}
	if cli.hubSync == nil {
		return
	}
	if err := cli.hubSync.startFresh(ctx); err != nil {
		cli.logger.Warn("hub sync: could not start shared session; disabling", zap.Error(err))
		cli.hubSync = nil
	}
}

// syncHubContext pulls turns that arrived on other channels since the last turn
// into local history, so the model has cross-channel context without anything
// being printed. Called at the start of each local turn (chat/agent/coder) on
// that turn's goroutine, so it never races the REPL.
func (cli *ChatCLI) syncHubContext(ctx context.Context) {
	if cli.hubSync == nil {
		return
	}
	if msgs := cli.hubSync.pull(ctx); len(msgs) > 0 {
		cli.history = append(cli.history, msgs...)
	}
}

// mirrorHubTurn records a completed local turn on the shared conversation, so a
// thread continued in the CLI shows up as context on Telegram/Slack/WhatsApp.
func (cli *ChatCLI) mirrorHubTurn(ctx context.Context, userText, assistantText string) {
	if cli.hubSync == nil {
		return
	}
	cli.hubSync.mirrorTurn(ctx, userText, assistantText)
}
