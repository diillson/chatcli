/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/ui/theme"
)

// The web UI runs as a child process, so it inherits the terminal theme only
// at spawn. A theme change in the terminal reaches it over the channel the
// terminal already holds: the child's own API, authenticated with the token
// in the address the child handed back. No second channel, and nothing for
// the page to poll beyond the status it already asks for.

const (
	// WebThemeResource is the child API resource that takes the theme.
	WebThemeResource = "theme"
	// webGuardHeader is the request header the child's API guard reads.
	webGuardHeader  = "X-Web-Token"
	webThemeTimeout = 3 * time.Second
)

// WebThemeRequest is the body the terminal posts to the child.
type WebThemeRequest struct {
	Theme string `json:"theme"`
}

// PostWebTheme tells the web UI served at pageURL (the address the child
// published, token included) which theme the terminal runs now.
func PostWebTheme(ctx context.Context, pageURL, name string) error {
	u, err := url.Parse(pageURL)
	if err != nil {
		return fmt.Errorf("web theme: %w", err)
	}
	token := u.Query().Get("t")
	if u.Host == "" || token == "" {
		return fmt.Errorf("web theme: address %q carries no host or token", u.Redacted())
	}
	body, err := json.Marshal(WebThemeRequest{Theme: name})
	if err != nil {
		return fmt.Errorf("web theme: %w", err)
	}
	endpoint := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/api/" + WebThemeResource}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("web theme: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webGuardHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("web theme: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("web theme: the web UI answered %d", resp.StatusCode)
	}
	return nil
}

// webThemePoster is the delivery; tests replace it.
var webThemePoster = PostWebTheme

// notifyWebTheme hands the active theme to a running web UI. It is the one
// call a place that changes the theme makes; without a web UI it does
// nothing. It only queues: a slow or dead child must never hold the prompt,
// so the post happens on the loop that /web started. Only the latest theme
// matters, so a pending older one is replaced.
func (cli *ChatCLI) notifyWebTheme() {
	if cli == nil {
		return
	}
	// Held across the send: shutdownWeb closes the queue under this lock.
	cli.web.mu.Lock()
	defer cli.web.mu.Unlock()
	queue := cli.web.theme
	if queue == nil {
		return
	}
	name := theme.Active().Name
	select {
	case queue <- name:
	default:
		select {
		case <-queue:
		default:
		}
		select {
		case queue <- name:
		default:
		}
	}
}

// webThemeLoop posts each queued theme to the child at pageURL until the
// queue is closed, which is how stopping the web UI ends it. ctx is the
// /web command's, detached from its cancellation: the child outlives the
// command that started it.
//
// A failed post is dropped, not retried: the only failures are a child that
// is gone or hung, and a retry would deliver a theme that the next change
// (or the next /web) supersedes anyway.
func webThemeLoop(ctx context.Context, pageURL string, queue <-chan string, logger *zap.Logger) {
	for name := range queue {
		postCtx, cancel := context.WithTimeout(ctx, webThemeTimeout)
		err := webThemePoster(postCtx, pageURL, name)
		cancel()
		if err != nil && logger != nil {
			logger.Debug("web: theme not delivered", zap.Error(err))
		}
	}
}
