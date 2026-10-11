/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rest

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/diillson/chatcli/operator/web"
)

// The dashboard is served with a strict policy: its own inline scripts are
// allowed by hash, nothing else runs, and no 'unsafe-inline' script source
// exists for an injected handler to ride on.
func TestDashboardServedWithStrictCSP(t *testing.T) {
	static, err := fs.Sub(web.StaticFiles, "static")
	if err != nil {
		t.Fatal(err)
	}
	csp, err := dashboardCSP(static)
	if err != nil {
		t.Fatal(err)
	}
	var scriptSrc string
	for _, d := range strings.Split(csp, "; ") {
		if strings.HasPrefix(d, "script-src ") {
			scriptSrc = d
		}
	}
	if scriptSrc == "" || strings.Contains(scriptSrc, "unsafe-inline") || strings.Contains(scriptSrc, "unsafe-eval") || strings.Contains(scriptSrc, "*") {
		t.Fatalf("script-src = %q, want hashes only", scriptSrc)
	}
	// The theme bootstrap and the application script; the JSON catalog is
	// data, not script.
	if n := strings.Count(scriptSrc, "'sha256-"); n != 2 {
		t.Fatalf("script-src allows %d hashes, want 2: %s", n, scriptSrc)
	}
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'none'", "base-uri 'none'", "connect-src 'self'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy lacks %q: %s", want, csp)
		}
	}

	rec := httptest.NewRecorder()
	withDashboardHeaders(csp, http.FileServer(http.FS(static))).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != csp {
		t.Fatalf("GET / = %d, CSP %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
}

// The hash covers the exact script text, so an edit to the script is
// allowed only through the embedded file itself.
func TestInlineScriptHashes(t *testing.T) {
	body := "\nconsole.log('a');\n"
	doc := `<html><head><script>` + body + `</script><script type="application/json">{"a":1}</script>` +
		`<script src="/x.js"></script></head></html>`
	got, err := inlineScriptHashes([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("hashes = %v, want [%s]", got, want)
	}
	if _, err := dashboardCSP(fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}); err == nil {
		t.Fatal("a dashboard without inline script must not yield a policy")
	}
}
