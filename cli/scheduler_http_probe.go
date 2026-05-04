/*
 * httpProbeClient — bounded, redirect-aware HTTP client used by the
 * scheduler's ParkPoll action via CLIBridge.RunHTTPProbe. Centralizing
 * timeout, redirect, and body-cap policy here keeps the action
 * executor terse and the security posture auditable in one place.
 */
package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpProbeBodyCap bounds the response body returned to the agent so a
// large download cannot blow up either the scheduler's Event payload or
// the LLM's context window at resume time.
const httpProbeBodyCap = 64 * 1024

// httpProbeMaxRedirects caps the redirect chain — guards against
// redirect loops and exfiltration attempts via redirected POSTs.
const httpProbeMaxRedirects = 5

// httpProbeClient is a thin facade over net/http with the policy
// constants above baked in.
type httpProbeClient struct {
	Timeout time.Duration
}

// Do issues the probe and returns (status, body, err). Non-2xx is NOT
// an error — the action's success_when DSL classifies acceptable codes.
// True I/O errors (DNS failure, TLS handshake) ARE returned as err.
func (c *httpProbeClient) Do(ctx context.Context, method, url string, headers map[string]string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, "", fmt.Errorf("http probe: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "chatcli-park-poll/1.0")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}

	hc := &http.Client{
		Timeout: c.Timeout,
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			ResponseHeaderTimeout: c.Timeout,
			DisableKeepAlives:     true,
			MaxIdleConns:          1,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= httpProbeMaxRedirects {
				return fmt.Errorf("http probe: too many redirects (>%d)", httpProbeMaxRedirects)
			}
			return nil
		},
	}

	resp, err := hc.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(httpProbeBodyCap+1)))
	if readErr != nil && readErr != io.EOF {
		return resp.StatusCode, string(body), readErr
	}
	bodyStr := string(body)
	if len(bodyStr) > httpProbeBodyCap {
		bodyStr = bodyStr[:httpProbeBodyCap] + "\n…[truncated]…"
	}
	// Trim trailing whitespace so probes that return "OK\n" match
	// success_when:body=OK without LLM authors having to remember
	// exact byte boundaries.
	bodyStr = strings.TrimRight(bodyStr, " \t\r\n")
	return resp.StatusCode, bodyStr, nil
}
