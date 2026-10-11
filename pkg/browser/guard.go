/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// navigationGuard holds the policy every document load of a session is
// checked against: the URL Navigate is given, every redirect hop, every
// frame and link the page loads, and the page a tab switch lands on.
var navigationGuard atomic.Pointer[func(rawURL string) error]

// SetNavigationGuard installs the policy that decides which URLs a session
// may load as a document. A nil guard removes it. Sessions created after
// the call intercept document requests with it; Navigate and SwitchTab
// consult it on every call.
func SetNavigationGuard(guard func(rawURL string) error) {
	if guard == nil {
		navigationGuard.Store(nil)
		return
	}
	navigationGuard.Store(&guard)
}

// checkNavigation applies the installed guard, if any.
func checkNavigation(rawURL string) error {
	g := navigationGuard.Load()
	if g == nil {
		return nil
	}
	if err := (*g)(rawURL); err != nil {
		return fmt.Errorf("navigation to %q refused: %w", rawURL, err)
	}
	return nil
}

// enableDocumentGuard asks the browser to pause every document request of
// the page session, so redirects, frames and page-initiated navigations
// pass the guard before they reach the network. Sub-resources are not
// paused: a page cannot read a cross-origin response without CORS, and
// pausing every image and script would slow every page down.
func (s *Session) enableDocumentGuard(ctx context.Context, sessionID string) error {
	if navigationGuard.Load() == nil {
		return nil
	}
	_, err := s.conn.call(ctx, sessionID, "Fetch.enable", map[string]interface{}{
		"patterns": []map[string]interface{}{
			{"urlPattern": "*", "resourceType": "Document", "requestStage": "Request"},
		},
	})
	return err
}

// resolvePausedRequest answers one Fetch.requestPaused event: continue
// when the guard accepts the URL, fail it as blocked otherwise. It runs
// off the read loop because the guard may resolve DNS and the answer is
// itself a call that waits on the read loop.
func (s *Session) resolvePausedRequest(params json.RawMessage, sessionID string) {
	var ev struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.RequestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := checkNavigation(ev.Request.URL); err != nil {
		s.appendConsole(ConsoleEntry{Kind: "error", Text: err.Error()})
		_, _ = s.conn.call(ctx, sessionID, "Fetch.failRequest", map[string]interface{}{
			"requestId": ev.RequestID, "errorReason": "BlockedByClient",
		})
		return
	}
	_, _ = s.conn.call(ctx, sessionID, "Fetch.continueRequest", map[string]interface{}{
		"requestId": ev.RequestID,
	})
}
