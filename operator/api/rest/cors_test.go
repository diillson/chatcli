/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func applyPolicy(t *testing.T, p CORSPolicy, method, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/incidents", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	p.apply(rec, req, "X-API-Key")
	return rec
}

// Deny-all stays the default: no origin configured, no headers written.
func TestCORS_DisabledByDefault(t *testing.T) {
	rec := applyPolicy(t, CORSPolicy{}, http.MethodGet, "https://dashboard.example.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("headers written with no policy configured: %q", got)
	}
}

// The allowlist echoes the request's own origin, because the header carries
// one value and echoing an unmatched one would allow any site.
func TestCORS_EchoesOnlyAllowedOrigins(t *testing.T) {
	p := CORSPolicy{
		AllowedOrigins: []string{"https://a.example.com", "https://b.example.com"},
		AllowedMethods: defaultCORSMethods,
	}

	for _, origin := range p.AllowedOrigins {
		rec := applyPolicy(t, p, http.MethodGet, origin)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("allowed origin %q got %q", origin, got)
		}
		if rec.Header().Get("Vary") != "Origin" {
			t.Errorf("Vary: Origin missing for %q", origin)
		}
	}

	rec := applyPolicy(t, p, http.MethodGet, "https://evil.example.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("an origin outside the allowlist was echoed: %q", got)
	}
}

func TestCORS_WildcardAndCredentials(t *testing.T) {
	plain := CORSPolicy{AllowedOrigins: []string{"*"}, AllowedMethods: defaultCORSMethods}
	rec := applyPolicy(t, plain, http.MethodGet, "https://anywhere.example.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("wildcard without credentials should send *, got %q", got)
	}

	// "*" is not a legal value alongside credentials; browsers reject it,
	// so the origin has to be echoed instead.
	withCreds := CORSPolicy{AllowedOrigins: []string{"*"}, AllowedMethods: defaultCORSMethods, AllowCredentials: true}
	rec = applyPolicy(t, withCreds, http.MethodGet, "https://anywhere.example.com")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://anywhere.example.com" {
		t.Errorf("wildcard with credentials should echo the origin, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q", got)
	}
}

func TestCORS_PreflightIsAnswered(t *testing.T) {
	p := CORSPolicy{AllowedOrigins: []string{"https://a.example.com"}, AllowedMethods: defaultCORSMethods}
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/incidents", nil)
	req.Header.Set("Origin", "https://a.example.com")
	rec := httptest.NewRecorder()

	if handled := p.apply(rec, req, "X-API-Key"); !handled {
		t.Fatal("preflight was not answered")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
}

// The variable the chart has been setting all along must keep working, and
// must now actually reach the policy.
func TestCORS_ReadsTheEnvironmentTheChartSets(t *testing.T) {
	t.Setenv("CHATCLI_CORS_ORIGIN", "https://legacy.example.com")
	t.Setenv("CHATCLI_CORS_ALLOWED_ORIGINS", "")
	t.Setenv("CHATCLI_CORS_ALLOWED_METHODS", "")
	t.Setenv("CHATCLI_CORS_ALLOW_CREDENTIALS", "")

	p := CORSPolicyFromEnv()
	if len(p.AllowedOrigins) != 1 || p.AllowedOrigins[0] != "https://legacy.example.com" {
		t.Fatalf("CHATCLI_CORS_ORIGIN did not reach the policy: %+v", p.AllowedOrigins)
	}
	if len(p.AllowedMethods) == 0 {
		t.Error("methods should fall back to the default verbs")
	}

	t.Setenv("CHATCLI_CORS_ALLOWED_ORIGINS", "https://a.example.com, https://b.example.com")
	t.Setenv("CHATCLI_CORS_ALLOWED_METHODS", "GET,POST")
	t.Setenv("CHATCLI_CORS_ALLOW_CREDENTIALS", "true")
	p = CORSPolicyFromEnv()
	if len(p.AllowedOrigins) != 3 {
		t.Errorf("list and single origin should compose: %+v", p.AllowedOrigins)
	}
	if len(p.AllowedMethods) != 2 {
		t.Errorf("methods = %+v", p.AllowedMethods)
	}
	if !p.AllowCredentials {
		t.Error("credentials flag did not reach the policy")
	}
}

func TestCORS_SetCORSOriginStillWorks(t *testing.T) {
	s := &APIServer{apiKeyHeader: "X-API-Key"}
	s.SetCORSOrigin("https://a.example.com")
	if got := s.CORSAllowedOrigins(); len(got) != 1 || got[0] != "https://a.example.com" {
		t.Fatalf("SetCORSOrigin = %+v", got)
	}
	s.SetCORSOrigin("")
	if got := s.CORSAllowedOrigins(); len(got) != 0 {
		t.Fatalf("clearing the origin left %+v", got)
	}
}
