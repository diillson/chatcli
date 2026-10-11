/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestExchangeTokenForm_RejectsOversizedBody pins that a token endpoint, which
// the MCP OAuth flow lets a remote server choose, cannot exhaust memory with a
// huge response: the body is capped and the exchange fails instead of reading
// it all in.
func TestExchangeTokenForm_RejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","token_type":"Bearer","padding":"`))
		chunk := strings.Repeat("a", 1<<20)
		for i := 0; i < 3; i++ { // ~3 MiB, over the 1 MiB cap
			_, _ = w.Write([]byte(chunk))
		}
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()

	_, err := ExchangeTokenForm(context.Background(), zap.NewNop(), srv.URL, TokenExchangeRequest{
		GrantType: "authorization_code", ClientID: "c", Code: "x",
	})
	if err == nil {
		t.Fatal("oversized token response was accepted")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", maxTokenResponseBytes)) {
		t.Errorf("error does not mention the limit: %v", err)
	}
}

// TestExchangeTokenForm_AcceptsNormalBody is the control: an ordinary token
// response still parses.
func TestExchangeTokenForm_AcceptsNormalBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	tr, err := ExchangeTokenForm(context.Background(), zap.NewNop(), srv.URL, TokenExchangeRequest{
		GrantType: "authorization_code", ClientID: "c", Code: "x",
	})
	if err != nil || tr.AccessToken != "tok" {
		t.Fatalf("normal token response = %+v, err=%v", tr, err)
	}
}
