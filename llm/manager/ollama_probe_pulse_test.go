/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package manager

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// The boot-time probe of a local Ollama shows as a connection on the live
// dashboard, so a slow or absent Ollama is visible instead of a silent pause.
func TestOllamaProbeIsMetered(t *testing.T) {
	i18n.Init()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()
	setupTestEnv(t, map[string]string{"OLLAMA_ENABLED": "true", "OLLAMA_BASE_URL": srv.URL})
	rec := pulsetest.Record(t)

	if _, err := NewLLMManager(zap.NewNop()); err != nil {
		t.Logf("manager: %v", err) // no provider configured is fine: only the probe matters
	}
	ev := rec.WaitFor(t, "metered probe", pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))
	if ev.Attrs["method"] != http.MethodGet || ev.Attrs["status"] != "200" {
		t.Fatalf("probe = %+v", ev)
	}
}
