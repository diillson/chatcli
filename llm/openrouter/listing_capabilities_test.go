/*
 * ChatCLI - Tests for the capabilities an OpenRouter model listing registers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
)

// OpenRouter reports each model's input modalities: an image modality is
// vision, a listed text-only model is known blind, and an entry without
// modalities stays unknown.
func TestListModels_RegistersInputModalities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"zz-test/sees","name":"Sees","architecture":{"input_modalities":["text","image"]}},
			{"id":"zz-test/text-only","name":"Text","architecture":{"input_modalities":["text"]}},
			{"id":"zz-test/silent","name":"Silent"}]}`))
	}))
	defer server.Close()
	c := newTestClient(t, server.URL+"/chat/completions")
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id              string
		vision, unknown bool
	}{
		{"zz-test/sees", true, false},
		{"zz-test/text-only", false, false},
		{"zz-test/silent", false, true},
	} {
		meta, ok := catalog.ResolveExact(catalog.ProviderOpenRouter, tc.id)
		if !ok {
			t.Fatalf("%s not registered", tc.id)
		}
		if got := slices.Contains(meta.Capabilities, "vision"); got != tc.vision || meta.CapabilitiesUnknown != tc.unknown {
			t.Errorf("%s: vision=%v unknown=%v, want %v %v", tc.id, got, meta.CapabilitiesUnknown, tc.vision, tc.unknown)
		}
	}
}
