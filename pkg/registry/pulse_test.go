/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// Every registry, and the skills.sh audit call, reach the network through a
// metered client: each request is a connection node named by host only.
func TestRegistryClientsAreMetered(t *testing.T) {
	rec := pulsetest.Record(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	entry := RegistryEntry{Name: "t", URL: srv.URL, IsActive: true}
	clients := map[string]*http.Client{
		"chatcli":  NewChatCLIRegistry(entry, zap.NewNop()).httpClient,
		"clawhub":  NewClawHubRegistry(entry, zap.NewNop()).httpClient,
		"custom":   NewCustomRegistry(entry, zap.NewNop()).httpClient,
		"skillssh": NewSkillsShRegistry(entry, zap.NewNop()).httpClient,
		"audit":    newRegistryHTTPClient(time.Second),
	}
	for name, c := range clients {
		resp, err := c.Get(srv.URL + "/search?q=SECRET-QUERY")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = resp.Body.Close()
	}
	rec.WaitFor(t, "five metered requests", func(pulse.Event) bool {
		return len(rec.Match(pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))) >= len(clients)
	})
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "/search") {
		t.Fatalf("the path leaked onto the bus: %s", wire)
	}
}
