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

// defaultHubTTLHours is how long an idle conversation lingers before PurgeIdle
// reclaims it. The hub is a momentary bridge, not an archive, so abandoned
// threads are swept.
const defaultHubTTLHours = 24

// Runtime setting keys (stored in hub.db) and their env-var fallbacks. Every
// hub knob can be set live via `/config hub set <key> <value>` — read from the
// shared db so the change reaches the gateway too — or via the env var, or it
// falls back to the default. Precedence: db setting > env > default.
const (
	hubKeyEnabled   = "enabled"
	hubKeyPrincipal = "principal"
	hubKeyIsolate   = "isolate"
	hubKeyTTLHours  = "ttl_hours"

	envHubEnabled   = "CHATCLI_HUB_ENABLED"
	envHubPrincipal = "CHATCLI_HUB_PRINCIPAL"
	envHubIsolate   = "CHATCLI_HUB_ISOLATE"
	envHubTTLHours  = "CHATCLI_HUB_TTL_HOURS"
)

// settingStr resolves a hub setting: db value (if store has it) wins, else the
// env var, else def. A nil store skips the db layer (env > def).
func settingStr(ctx context.Context, store hub.Store, key, env, def string) string {
	if store != nil {
		if v, ok, err := store.GetSetting(ctx, key); err == nil && ok && v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return def
}

// resolveHubEnabled reports whether the hub is active (default on; off only when
// explicitly set to "false").
func resolveHubEnabled(ctx context.Context, store hub.Store) bool {
	return !strings.EqualFold(settingStr(ctx, store, hubKeyEnabled, envHubEnabled, "true"), "false")
}

// resolveHubPrincipal returns the shared single-user principal.
func resolveHubPrincipal(ctx context.Context, store hub.Store) string {
	return settingStr(ctx, store, hubKeyPrincipal, envHubPrincipal, defaultHubPrincipal)
}

// resolveHubIsolate reports whether the gateway keeps each channel identity in
// its own conversation (multi-user/public bot) instead of collapsing unbound
// senders into the shared principal.
func resolveHubIsolate(ctx context.Context, store hub.Store) bool {
	return strings.EqualFold(settingStr(ctx, store, hubKeyIsolate, envHubIsolate, "false"), "true")
}

// resolveHubTTL returns the idle-conversation retention window (0 disables).
func resolveHubTTL(ctx context.Context, store hub.Store) time.Duration {
	s := settingStr(ctx, store, hubKeyTTLHours, envHubTTLHours, strconv.Itoa(defaultHubTTLHours))
	if h, err := strconv.Atoi(s); err == nil && h >= 0 {
		return time.Duration(h) * time.Hour
	}
	return defaultHubTTLHours * time.Hour
}

// LocalHubPrincipal returns the shared principal from env/default (no db layer).
// Used where no store is at hand; store-backed resolution uses resolveHubPrincipal.
func LocalHubPrincipal() string {
	return resolveHubPrincipal(context.Background(), nil)
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

// Settings delegate to the shared store, so /config hub changes persist and are
// read live by the gateway.
func (l *localHubClient) GetSetting(ctx context.Context, key string) (string, bool, error) {
	return l.store.GetSetting(ctx, key)
}
func (l *localHubClient) SetSetting(ctx context.Context, key, value string) error {
	return l.store.SetSetting(ctx, key, value)
}
func (l *localHubClient) DeleteSetting(ctx context.Context, key string) error {
	return l.store.DeleteSetting(ctx, key)
}
func (l *localHubClient) AllSettings(ctx context.Context) (map[string]string, error) {
	return l.store.AllSettings(ctx)
}

// maybeEnableLocalHub wires a standalone CLI to the on-disk hub unless the
// session is already connected to a remote hub. The hub is on by default; the
// `enabled` setting (or CHATCLI_HUB_ENABLED=false) opts out. Returns a closer
// for the opened database, or nil when local mode is off.
func (cli *ChatCLI) maybeEnableLocalHub(ctx context.Context) func() {
	if cli.hubSync != nil || cli.isRemote {
		return nil // a /connect session already owns hub sync
	}
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
	// The enabled setting lives in the db (so /config hub set enabled off reaches
	// the gateway too); it can only be read once the store is open.
	if !resolveHubEnabled(ctx, store) {
		_ = store.Close()
		return nil
	}
	if n, err := store.PurgeIdle(ctx, resolveHubTTL(ctx, store)); err != nil {
		cli.logger.Warn("local hub: purge idle failed", zap.Error(err))
	} else if n > 0 {
		cli.logger.Info("local hub: purged idle conversations", zap.Int("count", n))
	}
	principal := resolveHubPrincipal(ctx, store)
	cli.hubSync = newHubSync(newLocalHubClient(store, principal), cli.logger)
	cli.logger.Info("local hub mode enabled", zap.String("principal", principal), zap.String("db", dbPath))
	return func() { _ = store.Close() }
}
