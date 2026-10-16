/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// The OAuth token exchange and the profile lookup show as connections on the
// live dashboard — host, method and status only: the tokens they carry in
// bodies and headers never reach the bus.
func TestAnthropicOAuthCallsAreMetered(t *testing.T) {
	rec := pulsetest.Record(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"access_token":"SECRET-AT","refresh_token":"SECRET-RT","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"account":{"email":"person@example.com"}}`))
	}))
	defer srv.Close()

	if _, err := exchangeAnthropicToken(context.Background(), srv.URL+"/v1/oauth/token", map[string]any{"code": "SECRET-CODE"}); err != nil {
		t.Fatal(err)
	}
	_ = fetchAnthropicEmailFrom(context.Background(), srv.URL+"/api/profile", "SECRET-TOKEN", zap.NewNop())

	rec.WaitFor(t, "two metered calls", func(pulse.Event) bool {
		return len(rec.Match(pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))) >= 2
	})
	ends := rec.Match(pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))
	if ends[0].Attrs["method"] != http.MethodPost || ends[1].Attrs["method"] != http.MethodGet {
		t.Fatalf("ends = %+v", ends)
	}
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "oauth") || strings.Contains(string(wire), "person@") {
		t.Fatalf("token, path or account leaked onto the bus: %s", wire)
	}
}
