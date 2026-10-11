/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// Every message a chat adapter accepts runs through an agent that approves
// its own tool calls, so whoever can message the bot can run commands on the
// host. Each chat adapter therefore carries a sender allowlist keyed by the
// platform's stable user id, checked before anything else happens to the
// message (attachment downloads, queueing, hub writes).
//
// The allowlist fails closed: an adapter whose list is empty refuses to
// start. Accepting anyone is an explicit choice, written as the single entry
// "*", and logged at warn on every start.

// Allowlist variables, one per chat platform.
const (
	envTelegramAllowed = "CHATCLI_TELEGRAM_ALLOWED_USERS"
	envSlackAllowed    = "CHATCLI_SLACK_ALLOWED_USERS"
	envWhatsAppAllowed = "CHATCLI_WHATSAPP_ALLOWED_USERS"
	envDiscordAllowed  = "CHATCLI_DISCORD_ALLOWED_USERS"
)

// allowPublic is the entry that admits every sender.
const allowPublic = "*"

// senderAllowlist is an immutable set of allowed sender ids.
type senderAllowlist struct {
	public bool
	ids    map[string]bool
}

// newSenderAllowlist builds an allowlist from ids. Blank entries are
// dropped; the entry "*" admits everyone.
func newSenderAllowlist(ids []string) *senderAllowlist {
	a := &senderAllowlist{ids: make(map[string]bool, len(ids))}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		switch id {
		case "":
		case allowPublic:
			a.public = true
		default:
			a.ids[id] = true
		}
	}
	return a
}

// splitAllowlist splits a raw variable value on commas, semicolons and
// whitespace.
func splitAllowlist(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
}

// allowlistFromEnv reads an allowlist variable.
func allowlistFromEnv(name string) []string {
	return splitAllowlist(os.Getenv(name))
}

// configured reports whether the allowlist admits anyone at all.
func (a *senderAllowlist) configured() bool {
	return a != nil && (a.public || len(a.ids) > 0)
}

// listed reports whether one of keys is an explicit entry. "*" does not
// count: it admits a sender without naming them.
func (a *senderAllowlist) listed(keys ...string) bool {
	if a == nil {
		return false
	}
	for _, k := range keys {
		if k != "" && a.ids[k] {
			return true
		}
	}
	return false
}

// permits reports whether a sender identified by any of keys may talk to
// the bot.
func (a *senderAllowlist) permits(keys ...string) bool {
	if a == nil {
		return false
	}
	return a.public || a.listed(keys...)
}

// errAllowlistEmpty is wrapped by the start refusal of an adapter with no
// allowlist.
var errAllowlistEmpty = errors.New("gateway: sender allowlist is empty")

// checkStart is the start-time rule: an empty list refuses to start with an
// error naming the variable; a public list starts with a warning.
func (a *senderAllowlist) checkStart(platform, envName string, logger *zap.Logger) error {
	if err := a.preflight(platform, envName); err != nil {
		return err
	}
	if a.public && logger != nil {
		logger.Warn("gateway: sender allowlist is \"*\"; ANY user of the platform can drive the agent, which runs tools on this host. Keep CHATCLI_HUB_ISOLATE=true so senders do not share one conversation and store set",
			zap.String("platform", platform), zap.String("variable", envName))
	}
	return nil
}

// preflight is checkStart without the log line.
func (a *senderAllowlist) preflight(platform, envName string) error {
	if !a.configured() {
		return fmt.Errorf("%w: %s", errAllowlistEmpty, i18n.T("gateway.allowlist.empty", platform, envName))
	}
	return nil
}

// preflighter is implemented by adapters that can tell, before starting,
// that they would refuse to start.
type preflighter interface {
	preflight() error
}

// Preflight reports the first adapter that would refuse to start, so the
// daemon fails before it serves anything and the operator sees exactly which
// setting is missing.
func Preflight(adapters []Adapter) error {
	for _, a := range adapters {
		if p, ok := a.(preflighter); ok {
			if err := p.preflight(); err != nil {
				return err
			}
		}
	}
	return nil
}
