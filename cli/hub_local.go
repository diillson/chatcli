/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

// defaultHubPollInterval is how often local hub mode re-reads the shared
// database for turns written by another process (the gateway daemon). It trades
// latency for I/O; tune with CHATCLI_HUB_POLL_MS.
const defaultHubPollInterval = time.Second

// defaultHubPrincipal is the single-user identity used when CHATCLI_HUB_PRINCIPAL
// is unset. It makes cross-channel continuity work with zero configuration:
// the local CLI and the gateway's unbound senders both resolve to it, so a
// thread started in the CLI continues on Telegram/Slack/WhatsApp out of the box.
// The hub database lives under the invoking user's ~/.chatcli, so this constant
// is already scoped per OS user.
const defaultHubPrincipal = "default"

// hubEnabled reports whether the conversation hub is active. It is on by default
// and disabled only by CHATCLI_HUB_ENABLED=false (the same switch the server
// honors), so a user who wants the classic fresh-start CLI can opt out.
func hubEnabled() bool {
	return !strings.EqualFold(os.Getenv("CHATCLI_HUB_ENABLED"), "false")
}

// defaultHubTTL is how long an idle conversation lingers before PurgeIdle
// reclaims it. The hub is a momentary bridge, not an archive, so abandoned
// threads are swept; tune with CHATCLI_HUB_TTL_HOURS.
const defaultHubTTL = 24 * time.Hour

// hubTTL returns the idle-conversation retention window (0 disables purging).
func hubTTL() time.Duration {
	if v := os.Getenv("CHATCLI_HUB_TTL_HOURS"); v != "" {
		if h, err := strconv.Atoi(v); err == nil && h >= 0 {
			return time.Duration(h) * time.Hour
		}
	}
	return defaultHubTTL
}

// hubIsolate reports whether the gateway should keep conversations isolated per
// channel identity instead of collapsing unbound senders into the shared
// principal. Off by default (single-user convenience); set CHATCLI_HUB_ISOLATE=
// true when running a multi-user or public bot so one user can't see another's
// thread.
func hubIsolate() bool {
	return strings.EqualFold(os.Getenv("CHATCLI_HUB_ISOLATE"), "true")
}

// localHubClient implements HubClient against the on-disk hub database directly,
// so a standalone CLI (no /connect) shares the conversation with the gateway
// daemon running on the same machine. Both processes open the same hub.db.
//
// Live tailing is done by polling: the in-memory fan-out only sees writes from
// its own process, so to surface a Telegram message in an open notebook prompt
// we re-read the log on an interval. Appends from this process flow straight
// through to disk, where the gateway reads them on its next turn.
type localHubClient struct {
	store     hub.Store
	principal string
	poll      time.Duration
}

func newLocalHubClient(store hub.Store, principal string) *localHubClient {
	poll := defaultHubPollInterval
	if v := os.Getenv("CHATCLI_HUB_POLL_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			poll = time.Duration(ms) * time.Millisecond
		}
	}
	return &localHubClient{store: store, principal: principal, poll: poll}
}

func (l *localHubClient) ResolveActiveConversation(ctx context.Context, principal string) (string, string, error) {
	p := l.principalOr(principal)
	convID, err := l.store.Resolve(ctx, p)
	return convID, p, err
}

func (l *localHubClient) NewConversation(ctx context.Context, principal string) (string, error) {
	return l.store.NewConversation(ctx, l.principalOr(principal))
}

func (l *localHubClient) AppendEvent(ctx context.Context, ev models.ConversationEvent) (models.ConversationEvent, error) {
	if ev.Principal == "" {
		ev.Principal = l.principal
	}
	return l.store.Append(ctx, ev)
}

func (l *localHubClient) ReadConversation(ctx context.Context, convID string, sinceSeq int64, limit int) ([]models.ConversationEvent, error) {
	return l.store.Read(ctx, convID, sinceSeq, limit)
}

// SubscribeConversation tails the conversation by polling the database, so it
// observes turns appended by other processes (the gateway). The channel closes
// when ctx is canceled.
func (l *localHubClient) SubscribeConversation(ctx context.Context, convID string, sinceSeq int64) (<-chan models.ConversationEvent, error) {
	out := make(chan models.ConversationEvent)
	go func() {
		defer close(out)
		last := sinceSeq
		ticker := time.NewTicker(l.poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				events, err := l.store.Read(ctx, convID, last, 0)
				if err != nil {
					continue
				}
				for _, ev := range events {
					select {
					case out <- ev:
						last = ev.Seq
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out, nil
}

func (l *localHubClient) SetBinding(ctx context.Context, platform, userID, principal string) error {
	return l.store.Bind(ctx, platform, userID, l.principalOr(principal))
}

func (l *localHubClient) ListBindings(ctx context.Context, principal string) ([]models.HubBinding, error) {
	bindings, err := l.store.ListBindings(ctx, principal)
	if err != nil {
		return nil, err
	}
	out := make([]models.HubBinding, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, models.HubBinding{Platform: b.Platform, UserID: b.UserID, Principal: b.Principal})
	}
	return out, nil
}

func (l *localHubClient) principalOr(p string) string {
	if p == "" {
		return l.principal
	}
	return p
}

// LocalHubPrincipal returns the principal for local hub mode: CHATCLI_HUB_PRINCIPAL
// when set, otherwise the shared default. It is never empty, so cross-channel
// continuity works with zero configuration.
func LocalHubPrincipal() string {
	if p := strings.TrimSpace(os.Getenv("CHATCLI_HUB_PRINCIPAL")); p != "" {
		return p
	}
	return defaultHubPrincipal
}

// maybeEnableLocalHub wires a standalone CLI to the on-disk hub (on by default,
// off when CHATCLI_HUB_ENABLED=false) unless the session is already connected to
// a remote hub. It returns a closer for the opened database, or nil when local
// mode is off.
func (cli *ChatCLI) maybeEnableLocalHub(ctx context.Context) func() {
	if cli.hubSync != nil || cli.isRemote {
		return nil // a /connect session already owns hub sync
	}
	if !hubEnabled() {
		return nil // opted out
	}
	principal := LocalHubPrincipal()
	dbPath, err := hub.DefaultDBPath()
	if err != nil {
		cli.logger.Warn("local hub: cannot resolve db path; disabling", zap.Error(err))
		return nil
	}
	store, err := hub.OpenSQLiteStore(ctx, dbPath, cli.logger)
	if err != nil {
		cli.logger.Warn("local hub: cannot open db; disabling", zap.Error(err))
		return nil
	}
	if n, err := store.PurgeIdle(ctx, hubTTL()); err != nil {
		cli.logger.Warn("local hub: purge idle failed", zap.Error(err))
	} else if n > 0 {
		cli.logger.Info("local hub: purged idle conversations", zap.Int("count", n))
	}
	cli.hubSync = newHubSync(newLocalHubClient(store, principal), cli.logger)
	cli.logger.Info("local hub mode enabled", zap.String("principal", principal), zap.String("db", dbPath))
	return func() { _ = store.Close() }
}
