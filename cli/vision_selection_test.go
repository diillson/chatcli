/*
 * ChatCLI - Tests for vision of listing-registered models and describer choice
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// gemini-4-argon: the Gemini listing registered it without capabilities,
// and the catalog's missing "vision" was read as "blind" — the model was
// never probed and every image went to another model to be described.
func TestListingRegisteredModel_IsProbedNotBlind(t *testing.T) {
	const argon = "gemini-test-listed-argon"
	catalog.Register(catalog.ModelMeta{ID: argon, Aliases: []string{argon}, Provider: catalog.ProviderGoogleAI, CapabilitiesUnknown: true})
	c, m := visionTestCLI(t, catalog.ProviderGoogleAI)
	c.Provider, c.Model = catalog.ProviderGoogleAI, argon
	if _, known := catalogKnowsVision(catalog.ProviderGoogleAI, argon); known {
		t.Fatal("a listing-registered model without modalities must not be a catalog verdict")
	}
	img := []models.ImageContent{{MediaType: "image/png", Data: probePNG}}
	out, desc := c.gateImagesForModel(context.Background(), img)
	if len(out) != 1 || desc != "" || m.callsTo(argon) != 1 {
		t.Fatalf("the model must be probed and, accepting, see the image: out=%d desc=%q probes=%d", len(out), desc, m.callsTo(argon))
	}
	// Negative control: a curated text-only entry stays a verdict.
	catalog.Register(catalog.ModelMeta{ID: "gemini-test-curated-blind", Provider: catalog.ProviderGoogleAI, Capabilities: []string{"tools"}})
	if sees, known := catalogKnowsVision(catalog.ProviderGoogleAI, "gemini-test-curated-blind"); !known || sees {
		t.Fatal("a curated entry without vision is a known blind model")
	}
}

// The probe leaves room for a reasoning model to think: a cap of a few
// tokens drew an empty Gemini reply and an OpenAI Responses rejection
// (minimum 16), so the probe never concluded.
func TestVisionProbe_LeavesRoomToThink(t *testing.T) {
	c, m := visionTestCLI(t, "BEDROCK")
	if _, err := c.probeVision(context.Background(), "BEDROCK", profileARN); err != nil {
		t.Fatal(err)
	}
	if got := m.calls[0].maxTokens; got < 256 {
		t.Fatalf("probe max tokens %d leaves a reasoning model no room", got)
	}
}

// The Gemini listing put a model Google no longer serves first and offered
// text-to-speech variants as describers (a loose match lent them
// gemini-2.5-pro's vision): every attempt failed.
func TestDescribeCandidates_ExactVisionModelsInCatalogOrder(t *testing.T) {
	c, m := visionTestCLI(t, catalog.ProviderGoogleAI)
	c.Provider, c.Model = catalog.ProviderGoogleAI, "gemini-test-unknown"
	m.listing[catalog.ProviderGoogleAI] = apiListing("gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.5-flash-preview-tts",
		"gemini-2.5-pro-preview-tts", "gemini-embedding-001", "gemini-3.8-flash")
	got := c.describeCandidates(context.Background())
	if len(got) < 3 || got[0].model != "gemini-3.8-flash" || got[1].model != "gemini-2.5-pro" || got[2].model != "gemini-2.5-flash" {
		t.Fatalf("listed vision models must come newest-first: %+v", got)
	}
	for _, r := range got {
		if r.model == "gemini-2.5-flash-preview-tts" || r.model == "gemini-2.5-pro-preview-tts" || r.model == "gemini-embedding-001" {
			t.Fatalf("a non-vision variant became a describer: %+v", got)
		}
	}
}

// Another provider is listed only when the candidates before it are
// exhausted: the fallback listed every configured provider (Devin,
// Moonshot, OpenAI, OpenRouter, xAI, Z.AI...) before its first attempt.
func TestDescribeFallback_ListsOtherProvidersOnlyWhenNeeded(t *testing.T) {
	c, m := visionTestCLI(t, catalog.ProviderGoogleAI, catalog.ProviderOpenRouter, catalog.ProviderXAI)
	c.Provider, c.Model = catalog.ProviderGoogleAI, "gemini-test-unknown"
	m.listing[catalog.ProviderGoogleAI] = apiListing("gemini-3.8-flash")
	img := []models.ImageContent{{MediaType: "image/png", Data: probePNG}}
	if _, ok := c.describeImagesFallback(context.Background(), img); !ok {
		t.Fatal("the first candidate should have described the image")
	}
	if !slices.Equal(m.listed, []string{catalog.ProviderGoogleAI}) {
		t.Fatalf("listed %v; only the session's provider was needed", m.listed)
	}
}

// A provider whose wire carries text only answers a probe without ever
// seeing the image; it must neither be probed nor describe.
func TestTextOnlyProvider_NeverProbedNorDescriber(t *testing.T) {
	c, m := visionTestCLI(t, catalog.ProviderOpenAIAssistant, catalog.ProviderGoogleAI)
	c.Provider, c.Model = catalog.ProviderOpenAIAssistant, "gpt-test-assistant"
	t.Setenv("CHATCLI_VISION_INPUT", "native")
	if got := c.resolveVisionModeCtx(context.Background(), c.Provider, c.Model, true); got != visionDescribe {
		t.Fatalf("a text-only transport must describe even when native is forced: %v", got)
	}
	t.Setenv("CHATCLI_VISION_INPUT", "")
	if got := c.resolveVisionModeCtx(context.Background(), c.Provider, c.Model, true); got != visionDescribe || m.callsTo(c.Model) != 0 {
		t.Fatalf("a text-only transport was probed: mode %v, calls %d", got, m.callsTo(c.Model))
	}
	t.Setenv("CHATCLI_VISION_PROVIDER", catalog.ProviderOpenAIAssistant)
	t.Setenv("CHATCLI_VISION_MODEL", "gpt-4o")
	for _, r := range c.describeCandidates(context.Background()) {
		if r.provider == catalog.ProviderOpenAIAssistant {
			t.Fatalf("a text-only provider became a describer: %+v", r)
		}
	}
	if slices.Contains(m.listed, catalog.ProviderOpenAIAssistant) {
		t.Fatal("a text-only provider was listed (building its client has side effects)")
	}
}

// The describer gets room for its reasoning, within the model's ceiling.
func TestDescribeTokens_HeadroomWithinCeiling(t *testing.T) {
	if got := describeTokensFor(catalog.ProviderGoogleAI, "gemini-test-unknown"); got != describeMaxTokens || got < 4096 {
		t.Fatalf("unknown model: %d", got)
	}
	catalog.Register(catalog.ModelMeta{ID: "zz-test-small-out", Provider: catalog.ProviderOllama, MaxOutputTokens: 2048, Capabilities: []string{"vision"}})
	if got := describeTokensFor(catalog.ProviderOllama, "zz-test-small-out"); got != 2048 {
		t.Fatalf("the model's output ceiling must cap the request: %d", got)
	}
}

// A Gemini modality variant refusing an image is an image rejection: the
// describer learns it is blind instead of retrying it.
func TestImageRejection_GeminiModalityNotEnabled(t *testing.T) {
	err := errors.New(`API error: status 400 - {"error":{"code":400,"message":"Image input modality is not enabled for models/gemini-2.5-pro-preview-tts","status":"INVALID_ARGUMENT"}}`)
	if !isImageInputRejected(err) {
		t.Fatal("not recognized as an image rejection")
	}
}

func apiListing(ids ...string) []client.ModelInfo {
	out := make([]client.ModelInfo, len(ids))
	for i, id := range ids {
		out[i] = client.ModelInfo{ID: id, Source: client.ModelSourceAPI}
	}
	return out
}

// @docs-flatten used one model with no fallback and never probed the
// active model: an off-catalog active model that sees describes, and a
// failing describer gives way to the next candidate.
func TestDocsDescriber_ProbesActiveAndFallsBack(t *testing.T) {
	const argon = "gemini-test-docs-argon"
	catalog.Register(catalog.ModelMeta{ID: argon, Aliases: []string{argon}, Provider: catalog.ProviderGoogleAI, CapabilitiesUnknown: true})
	c, m := visionTestCLI(t, catalog.ProviderGoogleAI)
	c.Provider, c.Model = catalog.ProviderGoogleAI, argon
	d := &docsImageDescriber{cli: c}
	img := models.ImageContent{MediaType: "image/png", Data: probePNG}
	if _, by, err := d.DescribeImage(context.Background(), "describe", img); err != nil || by != "googleai/"+argon {
		t.Fatalf("the active model sees and must describe: by=%q err=%v", by, err)
	}
	if m.callsTo(argon) != 2 {
		t.Fatalf("want one probe and one description, got %d calls", m.callsTo(argon))
	}

	m.errs[argon] = errors.New("503 overloaded")
	m.listing[catalog.ProviderGoogleAI] = apiListing("gemini-3.8-flash")
	if _, by, err := d.DescribeImage(context.Background(), "describe", img); err != nil || by != "googleai/gemini-3.8-flash" {
		t.Fatalf("a failing describer must give way to the next: by=%q err=%v", by, err)
	}
}
