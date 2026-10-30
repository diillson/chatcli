/*
 * ChatCLI - Tests for the wire a provider plugin's models are spoken on
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package providerhost

import (
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/pricing"
	"github.com/diillson/chatcli/pkg/providerproto"
)

// A dialect plugin's models are spoken on the dialect's wire, per model
// when a model overrides it. The Gemini wire reports thinking outside the
// output count: a plugin model on it must bill thinking as output, like
// GOOGLEAI does, and a model of the same plugin on another wire must not.
func TestPreferredAPI_FollowsDialect(t *testing.T) {
	const name = "ZZTEST_GEMINI_GW"
	p := &Plugin{info: providerproto.InitializeResult{
		Provider: providerproto.ProviderInfo{Name: name},
		Mode:     providerproto.ModeDialect,
		Dialect:  &providerproto.Dialect{API: providerproto.DialectGemini, BaseURL: "https://gw.invalid"},
	}}
	p.registerModels([]providerproto.Model{
		{ID: "zz-gw-gemini-model"},
		{ID: "zz-gw-chat-model", Dialect: providerproto.DialectOpenAIChat},
	})
	if got := catalog.GetPreferredAPI(name, "zz-gw-gemini-model"); got != catalog.APIGemini {
		t.Fatalf("plugin dialect: %q", got)
	}
	if got := catalog.GetPreferredAPI(name, "zz-gw-chat-model"); got != catalog.APIChatCompletions {
		t.Fatalf("a model's own dialect wins: %q", got)
	}
	if !pricing.ReasoningTokensAdditiveFor(name, "zz-gw-gemini-model") {
		t.Fatal("thinking on the Gemini wire must bill as output")
	}
	if pricing.ReasoningTokensAdditiveFor(name, "zz-gw-chat-model") {
		t.Fatal("reasoning on the chat completions wire is already inside output")
	}
	if got := p.Descriptor().PreferredAPI; got != catalog.APIGemini {
		t.Fatalf("descriptor: %q", got)
	}

	for api, want := range map[providerproto.DialectAPI]catalog.PreferredAPI{
		providerproto.DialectOpenAIChat:        catalog.APIChatCompletions,
		providerproto.DialectOpenAIResponses:   catalog.APIResponses,
		providerproto.DialectAnthropicMessages: catalog.APIAnthropicMessages,
		providerproto.DialectGemini:            catalog.APIGemini,
	} {
		if got := p.preferredAPI(api); got != want {
			t.Errorf("%s → %q, want %q", api, got, want)
		}
	}
	full := &Plugin{info: providerproto.InitializeResult{Provider: providerproto.ProviderInfo{Name: "ZZTEST_FULL"}, Mode: providerproto.ModeFull}}
	if got := full.preferredAPI(""); got != catalog.APIChatCompletions {
		t.Fatalf("a full-mode plugin reports reasoning inside output: %q", got)
	}
}
