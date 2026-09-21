/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * attach.go — driving a browser the user already has open.
 *
 * Launching our own Chrome keeps the agent away from the user's everyday
 * sessions, but sometimes that is exactly what the user wants: "use the
 * Chrome I am logged into". CHATCLI_BROWSER_CDP_URL points at a browser
 * started with --remote-debugging-port; ChatCLI then opens ONE tab of its
 * own in that browser and closes only that tab on exit. The window is the
 * user's, so it is always visible, never killed, and never relaunched.
 */
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// CDPURLEnv attaches the session to an already running browser instead of
// launching one: http://127.0.0.1:9222 (the DevTools HTTP endpoint, resolved
// through /json/version) or a ws:// DevTools URL.
const CDPURLEnv = "CHATCLI_BROWSER_CDP_URL"

// attachURL returns the configured attach endpoint, "" when unset.
func attachURL() string {
	return strings.TrimSpace(os.Getenv(CDPURLEnv))
}

// resolveDevToolsWS turns an attach endpoint into the browser-level DevTools
// websocket URL. ws:// and wss:// pass through; http(s):// is queried for
// /json/version like every CDP client does.
func resolveDevToolsWS(ctx context.Context, endpoint string) (string, error) {
	switch {
	case strings.HasPrefix(endpoint, "ws://"), strings.HasPrefix(endpoint, "wss://"):
		return endpoint, nil
	case strings.HasPrefix(endpoint, "http://"), strings.HasPrefix(endpoint, "https://"):
	default:
		endpoint = "http://" + endpoint
	}
	versionURL := strings.TrimRight(endpoint, "/") + "/json/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req) // #nosec G107 -- operator-set CHATCLI_BROWSER_CDP_URL, local DevTools endpoint
	if err != nil {
		return "", fmt.Errorf("reach %s: %w (start the browser with --remote-debugging-port=9222)", versionURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.WS == "" {
		return "", fmt.Errorf("%s did not return a webSocketDebuggerUrl (HTTP %d)", versionURL, resp.StatusCode)
	}
	return v.WS, nil
}

// attachSession connects to the user's browser and opens the tab the session
// will drive. No process, no profile directory: nothing to kill or delete.
func attachSession(ctx context.Context, endpoint string) (*Session, error) {
	wsURL, err := resolveDevToolsWS(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("attach to browser via %s: %w", CDPURLEnv, err)
	}
	s := &Session{
		attached: true,
		headless: false,
		requests: make(map[string]NetworkEntry),
		loadCh:   make(chan struct{}),
	}
	conn, err := dialCDP(ctx, wsURL, s.handleEvent)
	if err != nil {
		return nil, err
	}
	s.conn = conn
	if err := s.attachFreshTarget(ctx); err != nil {
		s.conn.close()
		return nil, err
	}
	s.pulseBegin()
	return s, nil
}
