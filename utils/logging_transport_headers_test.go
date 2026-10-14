/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"net/http"
	"strings"
	"testing"
)

// Gateway credentials often travel in vendor headers whose names carry
// "apikey" without a separator; they must never reach the request log.
func TestHeadersToStringRedactsAPIKeyVariants(t *testing.T) {
	h := http.Header{}
	h.Set("X-Acme-Apikey", "client-123")
	h.Set("X-Custom-Api-Key", "k-456")
	h.Set("Authorization", "Bearer abc")
	h.Set("X-Request-Id", "req-1")
	out := headersToString(h)
	for _, secret := range []string{"client-123", "k-456", "abc"} {
		if strings.Contains(out, secret) {
			t.Errorf("header value %q leaked: %s", secret, out)
		}
	}
	if !strings.Contains(out, "req-1") {
		t.Errorf("non-sensitive header should stay visible: %s", out)
	}
}

func TestNewDefaultTransportHonorsEnvironmentProxy(t *testing.T) {
	tr := NewDefaultTransport()
	if tr.Proxy == nil || !tr.ForceAttemptHTTP2 {
		t.Fatalf("default transport lost proxy or HTTP/2 settings: %+v", tr)
	}
}
