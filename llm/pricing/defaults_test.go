package pricing

import (
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/stretchr/testify/assert"
)

// The model a provider falls back to when no <PROVIDER>_MODEL is set must be
// a model the catalog knows, so it is sized and priced like any other. This
// keeps a default from pointing at an id the catalog dropped; whether the
// vendor still serves it is checked live when a default changes.
func TestProviderDefaultsAreCataloged(t *testing.T) {
	defaults := []struct {
		provider, model string
		priced          bool // false where the plan, not the token, is billed
	}{
		{catalog.ProviderOpenAI, config.DefaultOpenAIModel, true},
		{catalog.ProviderClaudeAI, config.DefaultClaudeAIModel, true},
		{catalog.ProviderGoogleAI, config.DefaultGoogleAIModel, true},
		{catalog.ProviderXAI, config.DefaultXAIModel, true},
		{catalog.ProviderZAI, config.DefaultZAIModel, true},
		{catalog.ProviderMiniMax, config.DefaultMiniMaxModel, true},
		{catalog.ProviderMoonshot, config.DefaultMoonshotModel, true},
		{catalog.ProviderOpenRouter, config.DefaultOpenRouterModel, true},
		{catalog.ProviderBedrock, config.DefaultBedrockModel, true},
		{catalog.ProviderCopilot, config.DefaultCopilotModel, false},
	}
	for _, d := range defaults {
		meta, ok := catalog.Resolve(d.provider, d.model)
		if !assert.True(t, ok, "%s default %q is not in the catalog", d.provider, d.model) {
			continue
		}
		assert.Positive(t, meta.ContextWindow, "%s default %q has no context window", d.provider, d.model)
		in, out, known := ListPrice(d.provider, d.model)
		assert.True(t, known, "%s default %q has no list price", d.provider, d.model)
		if d.priced {
			assert.True(t, in > 0 && out > 0, "%s default %q is priced at zero", d.provider, d.model)
		}
	}
}

// OpenAI-compatible defaults move together: OpenRouter and Copilot fall back
// to the model the OpenAI provider itself defaults to.
func TestOpenAIFamilyDefaultsAgree(t *testing.T) {
	assert.Equal(t, config.DefaultOpenAIModel, config.DefaultCopilotModel)
	assert.Equal(t, "openai/"+config.DefaultOpenAIModel, config.DefaultOpenRouterModel)
}
