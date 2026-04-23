/*
 * HTTPStatus — evaluator that GETs a URL and matches status code /
 * body regex.
 *
 * Spec:
 *   url              string   — required
 *   method           string   — optional (default GET)
 *   expected         int      — optional (default 200)
 *   expected_regex   string   — optional; if set, the body must match
 *   headers          map      — optional request headers
 *   body             string   — optional request body (POST/PUT)
 *   timeout          duration — optional per-attempt timeout (default 10s)
 *   allow_insecure   bool     — optional, skip TLS verification
 */
package condition

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// HTTPStatus implements scheduler.ConditionEvaluator.
type HTTPStatus struct {
	// clients caches per-config http.Clients so we don't rebuild a
	// Transport on every poll. Keyed on "insecure?0|1" + timeout.
	mu      sync.Mutex
	clients map[string]*http.Client
}

// NewHTTPStatus builds the evaluator.
func NewHTTPStatus() *HTTPStatus {
	return &HTTPStatus{clients: make(map[string]*http.Client)}
}

// Type returns the Condition.Type literal.
func (*HTTPStatus) Type() string { return "http_status" }

// ValidateSpec enforces required fields.
func (*HTTPStatus) ValidateSpec(spec map[string]any) error {
	url := asString(spec, "url")
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("http_status: spec.url is required")
	}
	if !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
		return fmt.Errorf("http_status: spec.url must be http:// or https://")
	}
	if re := asString(spec, "expected_regex"); re != "" {
		if _, err := regexp.Compile(re); err != nil {
			return fmt.Errorf("http_status: invalid expected_regex: %w", err)
		}
	}
	return nil
}

// Evaluate issues the HTTP request.
func (h *HTTPStatus) Evaluate(ctx context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	url := cond.SpecString("url", "")
	method := cond.SpecString("method", "GET")
	expected := cond.SpecInt("expected", 200)
	regex := cond.SpecString("expected_regex", "")
	bodyStr := cond.SpecString("body", "")
	timeout := cond.SpecDuration("timeout", 10*time.Second)
	insecure := cond.SpecBool("allow_insecure", false)
	headers := asStringMap(cond.Spec, "headers")

	client := h.client(timeout, insecure)

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if bodyStr != "" {
		body = strings.NewReader(bodyStr)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, body)
	if err != nil {
		return scheduler.EvalOutcome{Err: err}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "chatcli-scheduler/1.0")
	}

	resp, err := client.Do(req)
	if err != nil {
		transient := reqCtx.Err() != nil || strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such host") || strings.Contains(err.Error(), "timeout")
		return scheduler.EvalOutcome{
			Err:       err,
			Transient: transient,
			Details:   fmt.Sprintf("%s %s: %v", method, url, err),
		}
	}
	defer resp.Body.Close()

	var payload []byte
	if regex != "" {
		// Only read the body when we actually need it.
		payload, _ = io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	}

	satisfied := resp.StatusCode == expected
	details := fmt.Sprintf("%s %s → %d (expected %d)", method, url, resp.StatusCode, expected)
	if regex != "" {
		re, err := regexp.Compile(regex)
		if err == nil {
			match := re.Match(payload)
			satisfied = satisfied && match
			details += fmt.Sprintf(", regex match=%v", match)
		}
	}
	return scheduler.EvalOutcome{
		Satisfied: satisfied,
		Details:   details,
	}
}

// client returns a cached http.Client for the given config.
func (h *HTTPStatus) client(timeout time.Duration, insecure bool) *http.Client {
	key := fmt.Sprintf("%d|%v", int(timeout.Seconds()), insecure)
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.clients[key]; ok {
		return c
	}
	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure}, //#nosec G402 -- opt-in via spec.allow_insecure
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       30 * time.Second,
	}
	c := &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
	h.clients[key] = c
	return c
}
