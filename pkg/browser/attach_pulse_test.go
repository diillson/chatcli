/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// Resolving an attach endpoint queries the browser's DevTools over HTTP: that
// request shows as a connection on the live dashboard.
func TestResolveDevToolsWSIsMetered(t *testing.T) {
	rec := pulsetest.Record(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/abc"}`))
	}))
	defer srv.Close()

	if _, err := resolveDevToolsWS(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	ev := rec.WaitFor(t, "metered DevTools query", pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))
	if ev.Status != pulse.StatusOK || ev.Attrs["method"] != http.MethodGet {
		t.Fatalf("conn = %+v", ev)
	}
}
