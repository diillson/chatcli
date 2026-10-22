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
	"os"
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

// A body the model asked to keep (or one read whole past the inline cap)
// lands in the session scratch dir with a note saying how to read it.
func TestHTTPSaveToFileAndMaxBody(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_TMPDIR", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/export.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rows":[`))
		for i := 0; i < 5000; i++ {
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = w.Write([]byte(`{"id":` + strings.Repeat("7", 10) + `}`))
		}
		_, _ = w.Write([]byte(`]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := NewBuiltinHTTPPlugin()

	// ~100KB read whole, over the 64KiB inline cap → saved automatically.
	out, err := p.Execute(context.Background(), []string{`{"cmd":"get","args":{"url":"` + srv.URL + `/export.json"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[full body saved to ") || !strings.Contains(out, ".json — ") || !strings.Contains(out, `"from_line":1`) {
		t.Fatalf("auto-save note missing: %s", out[len(out)-400:])
	}
	path := strings.TrimSpace(strings.SplitN(strings.SplitN(out, "[full body saved to ", 2)[1], " — ", 2)[0])
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 90_000 || !strings.HasPrefix(string(data), `{"rows":[`) {
		t.Fatalf("saved body: %d bytes, %v", len(data), err)
	}

	// max_body shrinks the inline part; save_path names the file.
	out, err = p.Execute(context.Background(), []string{`{"cmd":"get","args":{"url":"` + srv.URL + `/export.json","max_body":2048,"save_to_file":true,"save_path":"../../export.json"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 2048+1500 || !strings.Contains(out, "body truncated") || !strings.Contains(out, "export.json — ") {
		t.Fatalf("max_body + save_path: %d chars\n%s", len(out), out[len(out)-300:])
	}
	if strings.Contains(out, "../../") {
		t.Fatal("save_path must be confined to the scratch dir")
	}

	// A small body is not saved.
	fix := httpFixture(t)
	out, err = p.Execute(context.Background(), []string{`{"cmd":"get","args":{"url":"` + fix.URL + `/healthz"}}`})
	if err != nil || strings.Contains(out, "saved to") {
		t.Fatalf("small body must stay inline: %q %v", out, err)
	}
	// A body cut by the read cap is not presented as a whole save.
	out, err = p.Execute(context.Background(), []string{`{"cmd":"get","args":{"url":"` + fix.URL + `/big"}}`})
	if err != nil || strings.Contains(out, "full body saved") {
		t.Fatalf("partial read must not claim a full save: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatal("partial read stays flagged")
	}
	if NewBuiltinHTTPPlugin().MaxResultChars() < httpMaxRenderedBody {
		t.Fatal("the per-tool budget must fit a full inline body")
	}
	if httpBodyExt("text/html; charset=utf-8") != ".html" || httpBodyExt("application/octet-stream") != ".txt" {
		t.Fatal("extension by content type")
	}
}
