/*
 * ChatCLI - Tests for the capabilities a Copilot model listing registers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
)

// Copilot reports capabilities.supports.vision per model: true is vision,
// false is known blind, and an entry that does not say stays unknown.
func TestListModels_RegistersSupportsVision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"zz-test-sees","name":"Sees","capabilities":{"supports":{"vision":true,"tool_calls":true}}},
			{"id":"zz-test-blind","name":"Blind","capabilities":{"supports":{"vision":false}}},
			{"id":"zz-test-silent","name":"Silent"}]}`))
	}))
	defer server.Close()
	c := NewClient(testProvider("t"), "gpt-4o", testLogger(), 1, 0)
	c.baseURL = server.URL
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id              string
		vision, unknown bool
	}{
		{"zz-test-sees", true, false},
		{"zz-test-blind", false, false},
		{"zz-test-silent", false, true},
	} {
		meta, ok := catalog.ResolveExact(catalog.ProviderCopilot, tc.id)
		if !ok {
			t.Fatalf("%s not registered", tc.id)
		}
		if got := slices.Contains(meta.Capabilities, "vision"); got != tc.vision || meta.CapabilitiesUnknown != tc.unknown {
			t.Errorf("%s: vision=%v unknown=%v, want %v %v", tc.id, got, meta.CapabilitiesUnknown, tc.vision, tc.unknown)
		}
	}
}
