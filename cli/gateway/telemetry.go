/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * telemetry.go — observability for the messaging daemon's external comms.
 *
 * The daemon talks to third-party services (Telegram/Slack/Discord/WhatsApp
 * APIs) over plain HTTP. loggingTransport wraps each adapter's http.Client so
 * every outbound request to those services is recorded — method, host,
 * secret-stripped path, status and latency — giving the operator a full audit
 * trail of the external communication. Idle long-polls (Telegram getUpdates)
 * log at Debug so the steady-state log stays readable; sends and errors log at
 * Info/Warn so they're always visible.
 */
package gateway

import (
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// LoggerAware lets the daemon inject its real logger into an adapter that a
// registry builder created with a no-op logger (builders run at import time,
// before the daemon's logger exists). Implementations should also route their
// HTTP client through newLoggingClient so external requests are traced.
type LoggerAware interface {
	SetLogger(*zap.Logger)
}

// loggingTransport is an http.RoundTripper that records every request an
// adapter sends to its platform API.
type loggingTransport struct {
	base     http.RoundTripper
	logger   *zap.Logger
	platform string
}

// newLoggingClient returns c with its transport wrapped so requests are logged.
// A nil client yields a fresh one; a nil logger leaves the client untouched.
func newLoggingClient(c *http.Client, logger *zap.Logger, platform string) *http.Client {
	if logger == nil {
		return c
	}
	if c == nil {
		c = &http.Client{}
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	// Don't double-wrap if SetLogger is called more than once.
	if _, already := base.(*loggingTransport); already {
		base = base.(*loggingTransport).base
	}
	c.Transport = &loggingTransport{base: base, logger: logger, platform: platform}
	return c
}

func (lt *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := lt.base.RoundTrip(req)
	dur := time.Since(start)

	fields := []zap.Field{
		zap.String("platform", lt.platform),
		zap.String("method", req.Method),
		zap.String("host", req.URL.Host),
		zap.String("path", sanitizeAPIPath(req.URL.Path)),
		zap.Duration("dur", dur),
	}
	if err != nil {
		lt.logger.Warn("gateway: external request failed", append(fields, zap.Error(err))...)
		return resp, err
	}
	fields = append(fields, zap.Int("status", resp.StatusCode))
	// Long-poll receive loops (e.g. Telegram getUpdates) fire continuously
	// while idle — keep them at Debug so they don't drown the steady-state log,
	// but still capture them when the operator turns on debug logging.
	if isPollPath(req.URL.Path) {
		lt.logger.Debug("gateway: external poll", fields...)
	} else {
		lt.logger.Info("gateway: external request", fields...)
	}
	return resp, err
}

// sanitizeAPIPath strips embedded secrets (Telegram puts the bot token in the
// path as /bot<token>/method) so tokens never reach the log.
func sanitizeAPIPath(p string) string {
	const marker = "/bot"
	if i := strings.Index(p, marker); i >= 0 {
		rest := p[i+len(marker):]
		if j := strings.Index(rest, "/"); j >= 0 {
			return p[:i+len(marker)] + "***" + rest[j:]
		}
		return p[:i+len(marker)] + "***"
	}
	return p
}

// isPollPath reports whether a path is a long-poll receive endpoint whose
// requests are steady-state noise rather than discrete events.
func isPollPath(p string) bool {
	return strings.Contains(p, "getUpdates")
}
