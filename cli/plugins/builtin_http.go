/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @http — structured HTTP client for the agent. Where @webfetch turns a page
 * into readable markdown, @http is for testing APIs: explicit method, headers
 * and body in; status, timing, relevant headers and a bounded body out. The
 * natural pair of @proc: start the dev server, hit its endpoints, assert on
 * what came back.
 *
 * Self-contained in this package (the @webfetch precedent): every request
 * flows through the shared hardened client — corporate-proxy tolerant,
 * TLS-trust aware, SSRF-guarded (cloud metadata and link-local always
 * refused; loopback/private allowed by default so local dev servers work,
 * blockable via CHATCLI_WEBFETCH_BLOCK_PRIVATE) with every redirect hop
 * re-validated.
 */
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// httpDefaultTimeout / httpMaxTimeout bound one request.
	httpDefaultTimeout = 30 * time.Second
	httpMaxTimeout     = 120 * time.Second

	// httpMaxRequestBody bounds the outgoing body.
	httpMaxRequestBody = 1 << 20 // 1MiB

	// httpMaxResponseBody bounds how much of the response is read.
	httpMaxResponseBody = 256 * 1024 // 256KiB read cap

	// httpMaxRenderedBody bounds how much body lands in the conversation.
	httpMaxRenderedBody = 64 * 1024 // 64KiB rendered
)

// httpAllowedMethods is the closed set of supported methods; the read-only
// subset skips the orchestrator's confirmation policy.
var httpAllowedMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true,
	http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

var httpReadOnlyMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true,
}

// BuiltinHTTPPlugin is the @http tool.
type BuiltinHTTPPlugin struct{}

// NewBuiltinHTTPPlugin returns a ready-to-register plugin.
func NewBuiltinHTTPPlugin() *BuiltinHTTPPlugin { return &BuiltinHTTPPlugin{} }

// Name returns "@http".
func (*BuiltinHTTPPlugin) Name() string { return "@http" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinHTTPPlugin) Description() string {
	return "Structured HTTP client for testing APIs: send a request with explicit method, headers and body; get back status, latency, relevant headers and a bounded body. Use it to exercise endpoints (including local dev servers started with @proc) — for reading web PAGES as markdown, use @webfetch instead."
}

// Usage explains the canonical invocation forms.
func (*BuiltinHTTPPlugin) Usage() string {
	return `<tool_call name="@http" args='{"cmd":"request","args":{"method":"POST","url":"http://localhost:8080/api/items","headers":{"Content-Type":"application/json"},"body":"{\"name\":\"x\"}"}}' />

Subcommands (cmd + args):
  request {method, url, headers?, body?, timeout_seconds?:1-120}
  get|head|post|put|patch|delete {url, headers?, body?, timeout_seconds?}   method shortcuts

Notes: GET/HEAD/OPTIONS run without confirmation; mutating methods go through
the approval policy. Response bodies are bounded; JSON bodies come back
pretty-printed. Cloud-metadata and link-local targets are always refused.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinHTTPPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinHTTPPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinHTTPPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "request",
				"description": "send one HTTP request and get status, latency, headers and a bounded body",
				"flags": []map[string]interface{}{
					{"name": "method", "description": "GET|HEAD|OPTIONS|POST|PUT|PATCH|DELETE", "type": "string", "required": true},
					{"name": "url", "description": "http(s) target URL", "type": "string", "required": true},
					{"name": "headers", "description": "request headers as a string map", "type": "object"},
					{"name": "body", "description": "request body (string; pair with a Content-Type header)", "type": "string"},
					{"name": "timeout_seconds", "description": "request timeout", "type": "int", "default": "30"},
				},
				"examples": []string{
					`{"cmd":"request","args":{"method":"GET","url":"http://localhost:8080/healthz"}}`,
					`{"cmd":"post","args":{"url":"http://localhost:8080/api/items","headers":{"Content-Type":"application/json"},"body":"{\"name\":\"x\"}"}}`,
				},
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// Execute implements the plugin contract.
func (p *BuiltinHTTPPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — no incremental output.
func (p *BuiltinHTTPPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@http: empty args. Example: <tool_call name="@http" args='{"cmd":"get","args":{"url":"http://localhost:8080/healthz"}}' />`)
	}
	in, err := parseHTTPInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@http: %w", err)
	}
	return p.perform(ctx, in)
}

// httpArgs is the parsed request specification.
type httpArgs struct {
	Method         string    `json:"method"`
	URL            string    `json:"url"`
	Headers        headerMap `json:"headers"`
	Body           string    `json:"body"`
	TimeoutSeconds int       `json:"timeout_seconds"`
}

// headerMap unmarshals from an object or from a JSON-encoded object string —
// the agent flattener stringifies nested objects when it converts the
// envelope to "--flag value" argv.
type headerMap map[string]string

func (h *headerMap) UnmarshalJSON(b []byte) error {
	var direct map[string]string
	if err := json.Unmarshal(b, &direct); err == nil {
		*h = direct
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New(`"headers" must be an object of string values`)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*h = nil
		return nil
	}
	var inner map[string]string
	if err := json.Unmarshal([]byte(s), &inner); err != nil {
		return fmt.Errorf(`"headers" must be an object of string values: %w`, err)
	}
	*h = inner
	return nil
}

// perform executes the request through the shared hardened client.
func (p *BuiltinHTTPPlugin) perform(ctx context.Context, in httpArgs) (string, error) {
	if len(in.Body) > httpMaxRequestBody {
		return "", fmt.Errorf("@http: request body too large (%d bytes, limit %d)", len(in.Body), httpMaxRequestBody)
	}

	safeURL, err := validateWebTarget(in.URL)
	if err != nil {
		return "", fmt.Errorf("@http: refusing %q: %w", in.URL, err)
	}

	timeout := httpDefaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
		if timeout > httpMaxTimeout {
			timeout = httpMaxTimeout
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if in.Body != "" {
		bodyReader = strings.NewReader(in.Body)
	}
	req, err := http.NewRequestWithContext(reqCtx, in.Method, safeURL, bodyReader) //#nosec G704 -- URL validated by validateWebTarget + ssrfDialControl (metadata/link-local refused, redirects re-validated)
	if err != nil {
		return "", fmt.Errorf("@http: build request: %w", err)
	}
	req.Header.Set("User-Agent", fallbackUserAgent)
	for k, v := range in.Headers {
		// The proxy credential channel is operator-owned configuration; a tool
		// argument must never override it.
		if strings.EqualFold(k, "Proxy-Authorization") {
			continue
		}
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := webHTTPClient().Do(req) //#nosec G704 -- see request annotation above
	if err != nil {
		return "", fmt.Errorf("@http: %s %s failed after %s: %w", in.Method, in.URL, time.Since(start).Round(time.Millisecond), err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, httpMaxResponseBody+1))
	if err != nil {
		return "", fmt.Errorf("@http: read response: %w", err)
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	return renderHTTPResponse(in, resp, raw, elapsed), nil
}

// renderHTTPResponse formats the model-facing response summary.
func renderHTTPResponse(in httpArgs, resp *http.Response, raw []byte, elapsed time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s → %s in %s\n", in.Method, in.URL, resp.Status, elapsed)
	if final := resp.Request.URL.String(); final != in.URL && final != "" {
		fmt.Fprintf(&b, "final URL (after redirects): %s\n", final)
	}

	// Headers the model actually acts on, deterministic order.
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		switch strings.ToLower(k) {
		case "content-type", "content-length", "location", "retry-after",
			"www-authenticate", "x-request-id", "cache-control", "allow":
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(resp.Header.Values(k), ", "))
	}

	truncatedRead := false
	if len(raw) > httpMaxResponseBody {
		raw, truncatedRead = raw[:httpMaxResponseBody], true
	}
	body := renderHTTPBody(raw, resp.Header.Get("Content-Type"))
	if body == "" {
		b.WriteString("(empty body)")
		return strings.TrimRight(b.String(), "\n")
	}
	b.WriteString("---\n")
	b.WriteString(body)
	if truncatedRead {
		fmt.Fprintf(&b, "\n… (response larger than %d bytes; truncated)", httpMaxResponseBody)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderHTTPBody bounds the body and pretty-prints JSON so the model reads
// structure instead of a single wrapped line. Cuts land on rune boundaries.
func renderHTTPBody(raw []byte, contentType string) string {
	if len(raw) == 0 {
		return ""
	}
	if strings.Contains(strings.ToLower(contentType), "json") && json.Valid(raw) {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err == nil {
			return boundRuneSafe(pretty.String(), httpMaxRenderedBody)
		}
	}
	return boundRuneSafe(string(raw), httpMaxRenderedBody)
}

// boundRuneSafe truncates s to limit bytes on a rune boundary, flagging the cut.
func boundRuneSafe(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + "\n… (body truncated)"
}

// httpInvocationMethod extracts the resolved method for the caps layer.
func httpInvocationMethod(args []string) string {
	in, err := parseHTTPInvocation(args)
	if err != nil {
		return ""
	}
	return in.Method
}

// parseHTTPInvocation accepts the JSON envelope (cmd request|get|post|...)
// or the argv form ("get http://..."). Method shortcuts fold into the method
// field; explicit method wins over the shortcut only when they agree.
func parseHTTPInvocation(args []string) (httpArgs, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	var in httpArgs

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return in, fmt.Errorf(`parse envelope: %w. Expected {"cmd":"get","args":{"url":"..."}}`, err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		methodFromCmd, ok := canonicalHTTPCmd(cmdStr)
		if !ok {
			return in, fmt.Errorf("missing or unknown cmd %q (valid: request|get|head|options|post|put|patch|delete)", cmdStr)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		if err := json.Unmarshal([]byte(inner), &in); err != nil {
			return in, fmt.Errorf("parse args: %w", err)
		}
		if methodFromCmd != "" {
			in.Method = methodFromCmd
		}
	} else {
		methodFromCmd, ok := canonicalHTTPCmd(args[0])
		if !ok {
			return in, fmt.Errorf("unknown cmd %q (valid: request|get|head|options|post|put|patch|delete)", args[0])
		}
		tail := args[1:]
		// Legacy positional "request GET <url>" before the flag scan.
		if methodFromCmd == "" && len(tail) >= 2 &&
			!strings.HasPrefix(tail[0], "-") && httpAllowedMethods[strings.ToUpper(tail[0])] {
			methodFromCmd = strings.ToUpper(tail[0])
			tail = tail[1:]
		}
		// The agent flattener delivers the {cmd,args} envelope as "--flag
		// value" argv; a bare positional is the URL.
		inner := argvInner(tail, "url", nil, map[string]bool{"timeout_seconds": true})
		if err := json.Unmarshal([]byte(inner), &in); err != nil {
			return in, fmt.Errorf("parse args: %w", err)
		}
		if methodFromCmd != "" {
			in.Method = methodFromCmd
		}
	}

	in.Method = strings.ToUpper(strings.TrimSpace(in.Method))
	if in.Method == "" {
		return in, errors.New(`"method" is required (or use a method shortcut cmd like "get")`)
	}
	if !httpAllowedMethods[in.Method] {
		return in, fmt.Errorf("method %q not allowed (valid: GET|HEAD|OPTIONS|POST|PUT|PATCH|DELETE)", in.Method)
	}
	if strings.TrimSpace(in.URL) == "" {
		return in, errors.New(`"url" is required`)
	}
	return in, nil
}

// canonicalHTTPCmd resolves a cmd token: "request" keeps the args-supplied
// method (returns ""); method shortcuts return the concrete method.
func canonicalHTTPCmd(cmd string) (method string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "request", "req", "call":
		return "", true
	case "get":
		return http.MethodGet, true
	case "head":
		return http.MethodHead, true
	case "options":
		return http.MethodOptions, true
	case "post":
		return http.MethodPost, true
	case "put":
		return http.MethodPut, true
	case "patch":
		return http.MethodPatch, true
	case "delete", "del":
		return http.MethodDelete, true
	default:
		return "", false
	}
}
