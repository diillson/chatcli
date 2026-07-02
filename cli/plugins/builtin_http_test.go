/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * @http tests run against a real httptest server — the tool's whole job is
 * HTTP semantics (methods, headers, bodies, status), and the loopback path is
 * also exactly the @proc dev-server workflow it exists to serve.
 */
package plugins

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func httpFixture(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-123")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/items", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(body), `"name"`) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"name":"x","nested":{"deep":true}}`))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := strings.Repeat("é", 64*1024) // multi-byte content past every cap
		for i := 0; i < 8; i++ {
			_, _ = w.Write([]byte(chunk))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPGetRoundTrip(t *testing.T) {
	srv := httpFixture(t)
	p := NewBuiltinHTTPPlugin()

	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"get","args":{"url":"` + srv.URL + `/healthz"}}`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"GET " + srv.URL + "/healthz → 200 OK in ", "X-Request-Id: req-123", "ok"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestHTTPPostJSONPrettyPrinted(t *testing.T) {
	srv := httpFixture(t)
	p := NewBuiltinHTTPPlugin()

	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"post","args":{"url":"` + srv.URL + `/api/items","headers":{"Content-Type":"application/json"},"body":"{\"name\":\"x\"}"}}`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "201 Created") {
		t.Fatalf("status missing: %s", out)
	}
	if !strings.Contains(out, "\n  \"nested\": {") {
		t.Fatalf("JSON body must come back pretty-printed:\n%s", out)
	}
}

func TestHTTPResponseBoundsAreRuneSafe(t *testing.T) {
	srv := httpFixture(t)
	p := NewBuiltinHTTPPlugin()

	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"get","args":{"url":"` + srv.URL + `/big"}}`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) > httpMaxRenderedBody+2048 {
		t.Fatalf("rendered output not bounded: %d bytes", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Fatal("truncation must be flagged, never silent")
	}
	for _, r := range out {
		if r == 0xFFFD {
			t.Fatal("output contains U+FFFD — body cut mid-rune")
		}
	}
}

func TestHTTPRefusesMetadataAndBadInput(t *testing.T) {
	p := NewBuiltinHTTPPlugin()

	// Cloud metadata endpoint: always refused by the shared SSRF guard.
	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"get","args":{"url":"http://169.254.169.254/latest/meta-data/"}}`}); err == nil {
		t.Fatal("cloud metadata target must be refused")
	}
	// Unsupported scheme.
	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"get","args":{"url":"file:///etc/passwd"}}`}); err == nil {
		t.Fatal("non-http scheme must be refused")
	}
	// Method allowlist.
	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"request","args":{"method":"TRACE","url":"http://localhost:1/"}}`}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("TRACE must be refused, got %v", err)
	}
	// Missing url.
	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"get","args":{}}`}); err == nil || !strings.Contains(err.Error(), `"url" is required`) {
		t.Fatalf("missing url must error, got %v", err)
	}
	// Oversized request body.
	big := strings.Repeat("a", httpMaxRequestBody+1)
	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"post","args":{"url":"http://localhost:1/","body":"` + big + `"}}`}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized body must be refused, got %v", err)
	}
}

func TestHTTPProxyAuthorizationHeaderIsOperatorOwned(t *testing.T) {
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Proxy-Authorization")
	}))
	t.Cleanup(srv.Close)

	p := NewBuiltinHTTPPlugin()
	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"get","args":{"url":"` + srv.URL + `","headers":{"Proxy-Authorization":"Basic sneaky"}}}`}); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "" {
		t.Fatalf("tool argument must not set Proxy-Authorization, got %q", got)
	}
}

func TestHTTPCapsPerMethod(t *testing.T) {
	p := NewBuiltinHTTPPlugin()

	readOnly := [][]string{
		{`{"cmd":"get","args":{"url":"http://x/"}}`},
		{`{"cmd":"head","args":{"url":"http://x/"}}`},
		{`{"cmd":"request","args":{"method":"OPTIONS","url":"http://x/"}}`},
	}
	for _, args := range readOnly {
		if !p.IsReadOnly(args) {
			t.Fatalf("%v must be read-only", args)
		}
	}
	mutating := [][]string{
		{`{"cmd":"post","args":{"url":"http://x/"}}`},
		{`{"cmd":"delete","args":{"url":"http://x/"}}`},
		{`{not json`}, // fail closed
	}
	for _, args := range mutating {
		if p.IsReadOnly(args) {
			t.Fatalf("%v must NOT be read-only", args)
		}
	}
}

func TestHTTPArgvForm(t *testing.T) {
	srv := httpFixture(t)
	p := NewBuiltinHTTPPlugin()

	out, err := p.Execute(context.Background(), []string{"get", srv.URL + "/healthz"})
	if err != nil {
		t.Fatalf("argv form: %v", err)
	}
	if !strings.Contains(out, "200 OK") {
		t.Fatalf("argv get failed: %s", out)
	}
}
