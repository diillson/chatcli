/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// Webhook request signing. Both directions use the same scheme, keyed with
// CHATCLI_WEBHOOK_SECRET, and the secret itself never travels:
//
//	X-ChatCLI-Timestamp: <unix seconds>
//	X-ChatCLI-Signature: sha256=<hex HMAC-SHA256(secret, "<timestamp>.<raw body>")>
//
// A request is accepted only when its timestamp is within webhookSkew of the
// receiver's clock and the signature matches in constant time. Within that
// window each signature is accepted once, so a captured request cannot be
// replayed either.
const (
	webhookTimestampHeader = "X-ChatCLI-Timestamp"
	webhookSignatureHeader = "X-ChatCLI-Signature"
	webhookSignaturePrefix = "sha256="
	webhookSkew            = 5 * time.Minute
	// webhookReplayCap bounds the remembered signatures. Past it a new
	// request is refused until old entries expire, instead of the memory
	// growing without limit.
	webhookReplayCap = 100000
)

// signWebhook returns the signature header value for body at timestamp ts.
func signWebhook(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return webhookSignaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// webhookTimestampFresh parses ts and reports whether it lies within the
// window around now.
func webhookTimestampFresh(ts string, now time.Time) (time.Time, bool) {
	sec, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	at := time.Unix(sec, 0)
	if d := now.Sub(at); d > webhookSkew || d < -webhookSkew {
		return time.Time{}, false
	}
	return at, true
}

// webhookSignatureValid checks sig against body and ts in constant time.
// An unset secret verifies nothing.
func webhookSignatureValid(secret, ts string, body []byte, sig string) bool {
	if secret == "" || ts == "" || sig == "" {
		return false
	}
	want := signWebhook(secret, strings.TrimSpace(ts), body)
	return subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimSpace(sig))) == 1
}

// webhookReplayGuard remembers the signatures accepted within the window.
type webhookReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time // signature -> when it stops being acceptable
}

func newWebhookReplayGuard() *webhookReplayGuard {
	return &webhookReplayGuard{seen: make(map[string]time.Time)}
}

// webhookReplayVerdict is the outcome of recording a signature.
type webhookReplayVerdict int

const (
	webhookFresh webhookReplayVerdict = iota
	webhookReplayed
	webhookReplayFull
)

// record marks sig as used until its timestamp leaves the window. It reports
// a replay when sig was already used, and a full guard when it cannot
// remember more.
func (g *webhookReplayGuard) record(sig string, at, now time.Time) webhookReplayVerdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	if exp, ok := g.seen[sig]; ok && now.Before(exp) {
		return webhookReplayed
	}
	if len(g.seen) >= webhookReplayCap {
		for k, exp := range g.seen {
			if !now.Before(exp) {
				delete(g.seen, k)
			}
		}
		if len(g.seen) >= webhookReplayCap {
			return webhookReplayFull
		}
	}
	g.seen[sig] = at.Add(webhookSkew + time.Second)
	return webhookFresh
}

// checkWebhookCallbackURL accepts an https callback, or plain http when the
// host is loopback (a receiver on the same machine). The callback carries
// every reply, so it is never sent in clear text across a network.
func checkWebhookCallbackURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && u.Host != "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			return nil
		case "http":
			if isLoopbackHost(u.Hostname()) {
				return nil
			}
		}
	}
	return fmt.Errorf("%s", i18n.T("gateway.webhook.callback_insecure", "CHATCLI_WEBHOOK_CALLBACK_URL", redactURL(raw)))
}

// isLoopbackHost reports whether host names this machine's loopback.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
