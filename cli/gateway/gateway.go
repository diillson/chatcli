/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Package gateway lets ChatCLI run as a long-lived daemon that talks to the
 * user from messaging platforms (Telegram today; Discord/Slack/etc. plug in
 * through the same Adapter interface). It is intentionally dependency-free:
 * adapters use the platforms' plain HTTP APIs, so no third-party SDKs enter
 * go.mod.
 *
 * Architecture:
 *
 *   Adapter (per platform) --inbound chan--> Runner --AgentFunc--> reply
 *                          <-------- Send(reply) --------/
 *
 * The Runner owns session routing (one conversation per platform:chat),
 * bounded concurrency, and graceful shutdown. Adapters only know how to
 * receive and send on their platform.
 */
package gateway

import (
	"context"
	"sync"
)

// InboundMessage is a normalized message received from any platform.
type InboundMessage struct {
	Platform string // "telegram", "discord", ...
	ChatID   string // platform-specific conversation id
	UserID   string // platform-specific sender id
	UserName string // display name, best-effort
	Text     string // message body (may be empty for a voice-only message)

	// Audio carries a voice/audio attachment when the message has one. The
	// pure parser fills its metadata (and the unexported download handle); the
	// owning adapter then downloads the bytes into Data before dispatch.
	// Consumers read Data/MimeType/FileName only.
	Audio *InboundAudio
}

// InboundAudio is a voice/audio attachment on an inbound message.
type InboundAudio struct {
	Data     []byte // the raw audio, populated by the adapter after download
	MimeType string // e.g. "audio/ogg" — best-effort, may be empty
	FileName string // best-effort original name, may be empty

	// ref is a platform-opaque download handle (Telegram file_id, WhatsApp
	// media id, Discord/Slack URL) that the owning adapter resolves to Data.
	// It never leaves the gateway package.
	ref string
}

// SessionKey is the stable conversation identity used to scope history.
func (m InboundMessage) SessionKey() string {
	return m.Platform + ":" + m.ChatID
}

// OutboundMessage is a reply to deliver back to a conversation. When Audio is
// set, adapters that support voice replies send the clip (using Text as the
// caption); adapters that don't simply send Text, so a reply is never lost.
type OutboundMessage struct {
	ChatID string
	Text   string
	Audio  *OutboundAudio
}

// OutboundAudio is a synthesized voice reply to deliver alongside/instead of
// text on adapters that support it.
type OutboundAudio struct {
	Data     []byte
	Mime     string // e.g. "audio/ogg", "audio/mpeg"
	FileName string // e.g. "reply.ogg"
}

// Adapter is a platform integration. Implementations must be safe to Start
// once; Start blocks until ctx is canceled, pushing received messages to
// inbound. Send delivers a reply. Name identifies the platform.
type Adapter interface {
	Name() string
	Start(ctx context.Context, inbound chan<- InboundMessage) error
	Send(ctx context.Context, msg OutboundMessage) error
}

// AgentFunc turns an inbound user message into a final reply. It receives the
// session key so the implementation can keep per-conversation context. To
// stream progress while it works, it can pull a throttled emitter from ctx via
// Progress(ctx) and call it zero or more times; the returned string is the
// final reply delivered after the work finishes. Implementations must be safe
// for concurrent calls across sessions.
//
// The full InboundMessage (Platform/UserID, for cross-channel identity) is
// carried on ctx — use InboundFromContext(ctx) to recover it.
type AgentFunc func(ctx context.Context, session string, text string) (string, error)

// inboundKey scopes the originating InboundMessage carried on a context, so an
// AgentFunc can recover the sender's Platform/UserID without a signature change.
type inboundKey struct{}

// WithInbound returns a context carrying the originating message. The Runner
// installs it per inbound message before invoking the AgentFunc.
func WithInbound(ctx context.Context, msg InboundMessage) context.Context {
	return context.WithValue(ctx, inboundKey{}, msg)
}

// InboundFromContext returns the originating message on ctx, if any.
func InboundFromContext(ctx context.Context) (InboundMessage, bool) {
	m, ok := ctx.Value(inboundKey{}).(InboundMessage)
	return m, ok
}

// progressKey scopes the streamed-progress emitter carried on a context.
type progressKey struct{}

// WithProgress returns a context carrying a progress emitter that an AgentFunc
// may call to stream intermediate updates back to the user. The Runner installs
// one per inbound message and throttles delivery; a nil emit leaves ctx as-is.
func WithProgress(ctx context.Context, emit func(string)) context.Context {
	if emit == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, emit)
}

// Progress returns the progress emitter on ctx, or a no-op when none is set, so
// callers can always emit unconditionally.
func Progress(ctx context.Context) func(string) {
	if emit, ok := ctx.Value(progressKey{}).(func(string)); ok && emit != nil {
		return emit
	}
	return func(string) {}
}

// --- adapter registry (for discovery/config) ---

var (
	regMu    sync.RWMutex
	builders = map[string]func() (Adapter, error){}
)

// RegisterBuilder registers a named adapter builder. A builder returns
// (nil, nil) when the platform is not configured (e.g. missing token), so the
// runner can skip it without treating it as an error.
func RegisterBuilder(name string, build func() (Adapter, error)) {
	regMu.Lock()
	builders[name] = build
	regMu.Unlock()
}

// BuildConfigured instantiates every registered adapter that is configured.
// Builders returning (nil, nil) are skipped. The first hard error aborts.
func BuildConfigured() ([]Adapter, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	var out []Adapter
	for _, build := range builders {
		a, err := build()
		if err != nil {
			return nil, err
		}
		if a != nil {
			out = append(out, a)
		}
	}
	return out, nil
}

// RegisteredNames returns the names of all registered builders.
func RegisteredNames() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	names := make([]string, 0, len(builders))
	for n := range builders {
		names = append(names, n)
	}
	return names
}
