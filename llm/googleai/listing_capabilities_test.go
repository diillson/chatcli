/*
 * ChatCLI - Tests for the capabilities a Gemini model listing registers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package googleai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"go.uber.org/zap"
)

// A model only the listing knows is registered with its capabilities
// marked unknown: the listing says nothing about input modalities, so the
// missing "vision" must not read as "blind" (gemini-4-argon had every
// image described by another model, never probed).
func TestListModels_RegistersOffCatalogModelWithUnknownCapabilities(t *testing.T) {
	const id = "gemini-test-listing-only-zeta"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"models/` + id + `","displayName":"Zeta","supportedGenerationMethods":["generateContent"]}]}`))
	}))
	defer server.Close()

	c := NewGeminiClient(testProvider("k"), "gemini-3.8-flash", zap.NewNop(), 1, 0)
	c.baseURL = server.URL
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	meta, ok := catalog.ResolveExact(catalog.ProviderGoogleAI, id)
	if !ok {
		t.Fatal("the listed model was not registered")
	}
	if !meta.CapabilitiesUnknown {
		t.Fatal("a listing-only model must be registered with unknown capabilities")
	}
}
