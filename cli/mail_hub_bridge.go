/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * mail_hub_bridge — persists the squad message bus through the Conversation
 * Hub's SQLite store, making mail durable across restarts and shared across
 * processes (REPL and gateway daemon poll the same hub.db, WAL mode).
 *
 * Wire format: one hub event per message on a DEDICATED conversation ID
 * ("squad-mail") so squad traffic never leaks into the user-facing
 * conversation preamble or LLM history. Role "squad_mail" carries a JSON
 * payload; role "squad_mail_ack" records delivery (the drain) so other
 * processes — and this one after a restart — do not redeliver consumed
 * messages. ClientMsgID = the bus message ID gives idempotent appends.
 *
 * Delivery semantics are at-least-once ACROSS processes (two processes that
 * poll between a drain and its ack may both deliver) and exactly-once
 * WITHIN a process (Registry.Deliver dedups by globally unique ID). Squad
 * mail is directive text for LLM agents, so duplicate delivery is benign.
 */
package cli

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

const (
	// squadMailConvID is the dedicated hub conversation for squad mail. It
	// is intentionally NOT registered in the conversations table: PurgeIdle
	// never sweeps it, so queued directives survive quiet periods. Volume
	// is tiny (human/agent directives), so append-only growth is fine.
	squadMailConvID  = "squad-mail"
	squadMailRole    = "squad_mail"
	squadMailAckRole = "squad_mail_ack"
	squadMailChannel = "squad"

	// squadMailOpTimeout bounds each hub write so a slow disk can never
	// stall an agent turn.
	squadMailOpTimeout = 2 * time.Second

	// defaultSquadMailPollInterval matches the hub sync poll cadence;
	// overridable via the existing CHATCLI_HUB_POLL_MS.
	defaultSquadMailPollInterval = time.Second
)

// squadMailPayload is the JSON body of a squad_mail hub event.
type squadMailPayload struct {
	ID     string    `json:"id"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	CardID string    `json:"card_id,omitempty"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// squadMailAckPayload is the JSON body of a squad_mail_ack hub event.
type squadMailAckPayload struct {
	IDs []string `json:"ids"`
}

// mailBridgeActive guards against double-starting the bridge in one process
// (e.g. both the local hub path and a future caller).
var mailBridgeActive atomic.Bool

// squadMailPollInterval resolves the poll cadence from CHATCLI_HUB_POLL_MS.
func squadMailPollInterval() time.Duration {
	if v := os.Getenv("CHATCLI_HUB_POLL_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultSquadMailPollInterval
}

// startMailHubBridge wires the squad mail registry to the hub store and
// starts the cross-process poller. Returns a stop func (never nil). When a
// bridge is already active in this process, it is a no-op.
func startMailHubBridge(ctx context.Context, store hub.Store, reg *mail.Registry, logger *zap.Logger) func() {
	if store == nil || reg == nil {
		return func() {}
	}
	if !mailBridgeActive.CompareAndSwap(false, true) {
		return func() {}
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	bridgeCtx, cancel := context.WithCancel(ctx)

	// Persist every local send.
	reg.OnSend(func(msg mail.Message) {
		payload, err := json.Marshal(squadMailPayload{
			ID: msg.ID, From: msg.From, To: msg.To, CardID: msg.CardID, Text: msg.Text, At: msg.At,
		})
		if err != nil {
			return
		}
		opCtx, opCancel := context.WithTimeout(bridgeCtx, squadMailOpTimeout)
		defer opCancel()
		if _, err := store.Append(opCtx, models.ConversationEvent{
			ConvID:      squadMailConvID,
			Principal:   "squad",
			Channel:     squadMailChannel,
			Role:        squadMailRole,
			Content:     string(payload),
			ClientMsgID: msg.ID,
		}); err != nil {
			logger.Warn("squad mail: persist failed (message stays in-memory)", zap.Error(err))
		}
	})

	// Persist an ack for every local drain so other processes (and this one
	// after a restart) skip already-consumed messages.
	reg.OnDrain(func(msgs []mail.Message) {
		ids := make([]string, 0, len(msgs))
		for _, m := range msgs {
			ids = append(ids, m.ID)
		}
		payload, err := json.Marshal(squadMailAckPayload{IDs: ids})
		if err != nil {
			return
		}
		opCtx, opCancel := context.WithTimeout(bridgeCtx, squadMailOpTimeout)
		defer opCancel()
		if _, err := store.Append(opCtx, models.ConversationEvent{
			ConvID:    squadMailConvID,
			Principal: "squad",
			Channel:   squadMailChannel,
			Role:      squadMailAckRole,
			Content:   string(payload),
		}); err != nil {
			logger.Warn("squad mail: ack persist failed", zap.Error(err))
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		runSquadMailPoller(bridgeCtx, store, reg, logger)
	}()

	return func() {
		cancel()
		<-done
		reg.OnSend(nil)
		reg.OnDrain(nil)
		mailBridgeActive.Store(false)
	}
}

// runSquadMailPoller replays the squad-mail conversation from the start
// (hydration) and then follows it, delivering unacked messages to the local
// registry. Registry.Deliver drops duplicates and this process's own echoes.
func runSquadMailPoller(ctx context.Context, store hub.Store, reg *mail.Registry, logger *zap.Logger) {
	var lastSeq int64
	// Unacked candidates in seq order. Re-delivering on every pass is safe
	// (Deliver dedups); entries leave the map when their ack arrives.
	candidates := make(map[string]mail.Message)
	var order []string

	poll := func() {
		opCtx, opCancel := context.WithTimeout(ctx, squadMailOpTimeout)
		events, err := store.Read(opCtx, squadMailConvID, lastSeq, 0)
		opCancel()
		if err != nil {
			logger.Debug("squad mail: poll read failed", zap.Error(err))
			return
		}
		for _, ev := range events {
			lastSeq = ev.Seq
			switch ev.Role {
			case squadMailRole:
				var p squadMailPayload
				if err := json.Unmarshal([]byte(ev.Content), &p); err != nil || p.ID == "" {
					continue
				}
				if _, exists := candidates[p.ID]; !exists {
					candidates[p.ID] = mail.Message{
						ID: p.ID, From: p.From, To: p.To, CardID: p.CardID, Text: p.Text, At: p.At,
					}
					order = append(order, p.ID)
				}
			case squadMailAckRole:
				var a squadMailAckPayload
				if err := json.Unmarshal([]byte(ev.Content), &a); err != nil {
					continue
				}
				for _, id := range a.IDs {
					delete(candidates, id)
				}
			}
		}
		// Deliver surviving candidates in arrival order, compacting order.
		kept := order[:0]
		for _, id := range order {
			msg, ok := candidates[id]
			if !ok {
				continue
			}
			kept = append(kept, id)
			reg.Deliver(msg)
		}
		order = kept
	}

	poll() // hydration pass before the first tick
	ticker := time.NewTicker(squadMailPollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}
