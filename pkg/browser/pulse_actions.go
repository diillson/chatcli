/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Page actions on the live dashboard. Each action the session performs —
 * navigate, click, type, snapshot, screenshot and the rest — is one node
 * under the browser's own node, named by the action, so the dashboard
 * shows what the browser is doing and how long each step takes. A
 * navigation carries the host it landed on; nothing else about the page
 * travels: no path, no query, no selector, no typed text, no script, no
 * file name.
 */
package browser

import (
	"context"
	neturl "net/url"

	"github.com/diillson/chatcli/pkg/pulse"
)

// beginAction opens the node of one page action under the browser's node
// (the innermost parent in ctx when the browser has none, as when it came
// up while the dashboard was off).
func (s *Session) beginAction(ctx context.Context, action string) *pulse.Span {
	if !pulse.Enabled() {
		return nil
	}
	parent := s.pulse.ID()
	if parent == "" {
		parent = pulse.ParentFrom(ctx)
	}
	return pulse.Begin(pulse.KindTool, "browser:"+action, parent).With("action", action)
}

// endNavigation closes a navigation's node with the host it reached: the
// final URL's when there is one, the requested one's otherwise.
func endNavigation(span *pulse.Span, requested, final string, err error) {
	if span == nil {
		return
	}
	host := pageHost(final)
	if host == "" {
		host = pageHost(requested)
	}
	span.With("host", host).EndErr(err)
}

// pageHost is the hostname of a page URL, "" when it has none (about:,
// data:, file:, or not a URL).
func pageHost(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
