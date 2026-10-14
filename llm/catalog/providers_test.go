/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package catalog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// providerConstValues parses this package's catalog.go for the Provider*
// string constants — the same declaration the provider-parity gate reads —
// so a new constant without a descriptor fails here, not in production.
func providerConstValues(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "catalog.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			name := vs.Names[0].Name
			if !strings.HasPrefix(name, "Provider") || len(name) <= len("Provider") {
				continue
			}
			if lit, ok := vs.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				out = append(out, strings.Trim(lit.Value, `"`))
			}
		}
	}
	return out
}

func TestEveryProviderConstHasABuiltinDescriptor(t *testing.T) {
	consts := providerConstValues(t)
	require.NotEmpty(t, consts)
	for _, name := range consts {
		d, ok := LookupProvider(name)
		if !assert.True(t, ok, "provider const %q has no descriptor in builtinProviders", name) {
			continue
		}
		assert.True(t, d.Builtin(), name)
		assert.NotEmpty(t, d.DisplayName, "%s needs a DisplayName", name)
	}
	assert.Len(t, builtinProviders, len(consts), "builtinProviders must list exactly the Provider* consts")
}

func TestBuiltinProviderDescriptorsAreConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range builtinProviders {
		assert.False(t, seen[d.Name], "duplicate descriptor %s", d.Name)
		seen[d.Name] = true
		assert.Equal(t, strings.ToUpper(d.Name), d.Name)
		if d.ModelEnv != "" {
			assert.NotEmpty(t, d.DefaultModel, "%s has a model env but no default model", d.Name)
		}
	}
}

// TestProviderOrderKeepsClaudeBeforeBedrock pins the routing order bare
// model hints rely on: shared claude-* ids resolve to the direct API.
func TestProviderOrderKeepsClaudeBeforeBedrock(t *testing.T) {
	names := ProviderNames()
	idx := func(n string) int {
		for i, v := range names {
			if v == n {
				return i
			}
		}
		return -1
	}
	assert.Less(t, idx(ProviderClaudeAI), idx(ProviderBedrock))
}

// TestFallbackSizingUnchanged freezes the per-provider sizing the catalog
// applied before the rules moved onto the descriptors (models absent from
// the model registry).
func TestFallbackSizingUnchanged(t *testing.T) {
	t.Setenv("CHATCLI_CONTEXT_WINDOW", "")
	const unknown = "zz-unlisted-model"
	cases := []struct {
		provider, model string
		maxTokens, ctx  int
		api             PreferredAPI
	}{
		{ProviderOpenAI, unknown, 16384, 128000, APIChatCompletions},
		{ProviderOpenAIAssistant, unknown, 50000, 128000, APIAssistants},
		{ProviderClaudeAI, unknown, 64000, 200000, APIAnthropicMessages},
		{ProviderBedrock, "zz.anthropic.zz", 64000, 1000000, APIChatCompletions},
		{ProviderBedrock, "zz.nova-zz", 5120, 128000, APIChatCompletions},
		{ProviderGoogleAI, unknown, 65536, 1000000, PreferredAPI("gemini_api")},
		{ProviderXAI, unknown, 16384, 128000, APIChatCompletions},
		{ProviderZAI, unknown, 65535, 202752, APIChatCompletions},
		{ProviderMiniMax, unknown, 131072, 204800, APIChatCompletions},
		{ProviderMoonshot, unknown, 131072, 262144, APIChatCompletions},
		{ProviderStackSpot, unknown, 128000, 128000, APIChatCompletions},
		{ProviderOllama, unknown, 8192, 8192, APIChatCompletions},
		{ProviderCopilot, unknown, 16384, 128000, APIChatCompletions},
		{ProviderOpenRouter, unknown, 16384, 128000, APIChatCompletions},
		{ProviderDevin, unknown, 32000, 200000, APIChatCompletions},
		{"NOT_A_PROVIDER", unknown, 50000, 50000, APIChatCompletions},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			assert.Equal(t, tc.maxTokens, GetMaxTokens(tc.provider, tc.model, 0), "max tokens")
			assert.Equal(t, tc.ctx, GetContextWindow(tc.provider, tc.model), "context window")
			assert.Equal(t, tc.api, GetPreferredAPI(tc.provider, tc.model), "preferred api")
		})
	}
}

// The OpenAI family rules are reachable only for ids the model registry's
// prefix match misses, so exercise them on the descriptor directly.
func TestOpenAIFallbackFamilies(t *testing.T) {
	d, ok := LookupProvider(ProviderOpenAI)
	require.True(t, ok)
	for model, want := range map[string]int{
		"gpt-5-zz": 128000, "gpt-4-zz": 32000, "gpt-4o": 32000,
		"o3-zz": 100000, "o4-zz": 100000, "zz": 16384,
	} {
		assert.Equal(t, want, d.maxTokensFor(model), model)
	}
}

func TestRegisterProviderValidation(t *testing.T) {
	t.Cleanup(func() { UnregisterProvider("ACME") })

	assert.Error(t, RegisterProvider(ProviderDescriptor{Name: "acme corp", DisplayName: "x"}), "spaces")
	assert.Error(t, RegisterProvider(ProviderDescriptor{Name: "1ACME", DisplayName: "x"}), "leading digit")
	assert.Error(t, RegisterProvider(ProviderDescriptor{Name: "ACME"}), "missing display name")
	assert.Error(t, RegisterProvider(ProviderDescriptor{Name: "openai", DisplayName: "fake"}), "shadowing a built-in")

	require.NoError(t, RegisterProvider(ProviderDescriptor{
		Name:         "acme",
		DisplayName:  "Acme Gateway",
		MaxOutputEnv: "ACME_MAX_TOKENS",
		AuthEnv:      []string{"ACME_CLIENT_SECRET"},
	}))
	d, ok := LookupProvider("Acme")
	require.True(t, ok)
	assert.Equal(t, "ACME", d.Name)
	assert.False(t, d.Builtin())

	names := ProviderNames()
	assert.Equal(t, "ACME", names[len(names)-1], "runtime providers follow the built-ins")
	env, ok := MaxTokensEnvVar("ACME")
	assert.True(t, ok)
	assert.Equal(t, "ACME_MAX_TOKENS", env)
	assert.Equal(t, defaultFallbackTokens, GetContextWindow("ACME", "anything"))

	// Returned copies never alias the registry.
	d.AuthEnv[0] = "MUTATED"
	again, _ := LookupProvider("ACME")
	assert.Equal(t, "ACME_CLIENT_SECRET", again.AuthEnv[0])

	UnregisterProvider("ACME")
	_, ok = LookupProvider("ACME")
	assert.False(t, ok)
	UnregisterProvider(ProviderOpenAI)
	_, ok = LookupProvider(ProviderOpenAI)
	assert.True(t, ok, "built-ins cannot be unregistered")
}
