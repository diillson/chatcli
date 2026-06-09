/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLoggingMiddleware_EscapesRequestControlledFields(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	h := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	// A crafted path with an embedded newline would forge a second log
	// line if the middleware printed it raw.
	req.URL.Path = "/api/v1/status\n[REST] FORGED 200"
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	if strings.Contains(out, "\n[REST] FORGED") {
		t.Errorf("raw newline leaked into the access log:\n%s", out)
	}
	if !strings.Contains(out, `\n[REST] FORGED`) {
		t.Errorf("expected %%q-escaped path in the access log, got:\n%s", out)
	}
	if !strings.Contains(out, "418") {
		t.Errorf("expected captured status code 418 in the access log, got:\n%s", out)
	}
	if !strings.Contains(out, "role=-") {
		t.Errorf("expected placeholder role for unauthenticated request, got:\n%s", out)
	}
}
