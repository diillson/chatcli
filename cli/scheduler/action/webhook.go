/*
 * Webhook — executor that POSTs (or any other HTTP method) to a URL.
 *
 * Payload:
 *   url              string — required
 *   method           string — optional (default POST)
 *   body             string — optional raw body
 *   json             any    — optional; when set, marshalled to JSON
 *                             and overrides body
 *   headers          map    — optional request headers
 *   expected_status  int    — optional (default any 2xx)
 *   timeout          duration — optional (default 30s)
 *   allow_insecure   bool   — optional
 *   max_response     int    — optional (default 8 KiB)
 */
package action

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// Webhook implements scheduler.ActionExecutor.
type Webhook struct{}

// NewWebhook builds the executor.
func NewWebhook() *Webhook { return &Webhook{} }

// Type returns the ActionType literal.
func (Webhook) Type() scheduler.ActionType { return scheduler.ActionWebhook }

// ValidateSpec enforces required fields.
func (Webhook) ValidateSpec(payload map[string]any) error {
	url := asString(payload, "url")
	if url == "" {
		return fmt.Errorf("webhook: payload.url is required")
	}
	if !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
		return fmt.Errorf("webhook: url must be http:// or https://")
	}
	return nil
}

// Execute issues the HTTP request.
func (Webhook) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	url := asString(action.Payload, "url")
	method := asString(action.Payload, "method")
	if method == "" {
		method = "POST"
	}
	timeoutMS := asInt(action.Payload, "timeout_ms")
	timeout := 30 * time.Second
	if timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	if v, ok := action.Payload["timeout"]; ok {
		if s, ok := v.(string); ok {
			if d, err := time.ParseDuration(s); err == nil {
				timeout = d
			}
		}
	}
	insecure := asBool(action.Payload, "allow_insecure")
	expected := asInt(action.Payload, "expected_status")
	maxResp := asInt(action.Payload, "max_response")
	if maxResp <= 0 {
		maxResp = 8 * 1024
	}

	var body io.Reader
	var ctype string
	if j, ok := action.Payload["json"]; ok {
		b, err := json.Marshal(j)
		if err != nil {
			return scheduler.ActionResult{Err: fmt.Errorf("webhook: marshal json: %w", err)}
		}
		body = bytes.NewReader(b)
		ctype = "application/json"
	} else if bs := asString(action.Payload, "body"); bs != "" {
		body = strings.NewReader(bs)
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, url, body)
	if err != nil {
		return scheduler.ActionResult{Err: err}
	}
	for k, v := range asStringMap(action.Payload, "headers") {
		req.Header.Set(k, v)
	}
	if ctype != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", ctype)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "chatcli-scheduler/1.0")
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure}, //#nosec G402 -- opt-in
			ResponseHeaderTimeout: timeout,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return scheduler.ActionResult{
			Err:       err,
			Transient: reqCtx.Err() != nil,
		}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxResp)))

	okExpected := resp.StatusCode / 100
	if expected > 0 {
		if resp.StatusCode != expected {
			return scheduler.ActionResult{
				Output: fmt.Sprintf("%s %s → %d (expected %d)\n%s", method, url, resp.StatusCode, expected, string(respBody)),
				Err:    fmt.Errorf("unexpected status %d", resp.StatusCode),
			}
		}
	} else if okExpected != 2 {
		return scheduler.ActionResult{
			Output: fmt.Sprintf("%s %s → %d\n%s", method, url, resp.StatusCode, string(respBody)),
			Err:    fmt.Errorf("non-2xx status %d", resp.StatusCode),
		}
	}
	_ = env
	return scheduler.ActionResult{
		Output: fmt.Sprintf("%s %s → %d\n%s", method, url, resp.StatusCode, string(respBody)),
	}
}
