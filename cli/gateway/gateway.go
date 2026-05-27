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
	Text     string // message body
}

// SessionKey is the stable conversation identity used to scope history.
func (m InboundMessage) SessionKey() string {
	return m.Platform + ":" + m.ChatID
}

// OutboundMessage is a reply to deliver back to a conversation.
type OutboundMessage struct {
	ChatID string
	Text   string
}

// Adapter is a platform integration. Implementations must be safe to Start
// once; Start blocks until ctx is canceled, pushing received messages to
// inbound. Send delivers a reply. Name identifies the platform.
type Adapter interface {
	Name() string
	Start(ctx context.Context, inbound chan<- InboundMessage) error
	Send(ctx context.Context, msg OutboundMessage) error
}

// AgentFunc turns an inbound user message into a reply. It receives the
// session key so the implementation can maintain per-conversation history.
// Implementations must be safe for concurrent calls across sessions.
type AgentFunc func(ctx context.Context, session string, text string) (string, error)

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
