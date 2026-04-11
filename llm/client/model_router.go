/*
 * ChatCLI - Standalone model/provider resolver
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Pure resolution logic for skill/agent `model:` frontmatter hints. Lives
 * in the client package so both the cli package (chat/agent modes) and the
 * workers package (per-agent dispatch) can use it without a cyclic import
 * on llm/manager.
 *
 * The resolver depends on three signals, tried in order:
 *   1. API-cached model list for the user's active provider (authoritative
 *      when available — populated by refreshModelCache via /models endpoint)
 *   2. The static catalog (exact → alias → prefix match)
 *   3. A family-prefix heuristic for well-known model names
 *
 * If none of these identify the hint's provider, the resolver optimistically
 * tries the user's active provider as-is (covers exotic/custom deployments
 * that only the API knows about). On hard failure it falls back to the user
 * client with a human-readable UserMessage so callers can surface it.
 *
 * The resolver never mutates any caller state. It returns a plain value
 * describing the decision.
 */
package client

import (
	"strings"

	"github.com/diillson/chatcli/llm/catalog"
	"go.uber.org/zap"
)

// ProviderRouter is the tiny subset of manager.LLMManager that the resolver
// needs. Keeping it local here breaks the import cycle between llm/client
// and llm/manager; the real manager satisfies this interface implicitly.
type ProviderRouter interface {
	GetClient(provider, model string) (LLMClient, error)
	GetAvailableProviders() []string
}

// ModelRoutingResolution is the decision produced by ResolveModelRouting.
type ModelRoutingResolution struct {
	// Client is the LLMClient to use for the turn. Always non-nil; on
	// fallback it points to the UserClient input.
	Client LLMClient
	// Provider and Model reflect the effective provider/model id for
	// cost tracking and logging.
	Provider string
	Model    string

	// Changed is true iff the resolver picked a client different from
	// UserClient (same-provider-swap or cross-provider swap).
	Changed bool

	// CrossProvider is true iff the swap crossed provider boundaries.
	CrossProvider bool

	// Note is a short machine-readable tag describing the branch taken.
	//
	//   ""                        — no hint / hint == user model
	//   "api-cached"              — matched user provider's API cache
	//   "catalog-same-provider"   — catalog hit on user's provider
	//   "catalog-cross-provider"  — catalog hit on a different provider
	//   "family-same-provider"    — family heuristic on user's provider
	//   "family-cross-provider"   — family heuristic on a different provider
	//   "optimistic-user-provider"— hint unresolved, used user provider
	//   "fallback-unavailable"    — hint's provider is not configured
	//   "fallback-build-failed"   — GetClient errored
	Note string

	// UserMessage is a human-readable notice for display when Changed is
	// false but a hint was supplied (explains *why* the preference was
	// not honored so users never see a silent fallback).
	UserMessage string
}

// ResolveModelRoutingInput groups the arguments to ResolveModelRouting so
// callers can pass optional bits (apiCache, logger) without positional
// argument bloat.
type ResolveModelRoutingInput struct {
	Router       ProviderRouter
	UserProvider string
	UserModel    string
	UserClient   LLMClient
	// APICache is the cached list of API-discovered models for the
	// user's provider (may be nil — catalog/heuristic still work).
	APICache []ModelInfo
	// Hint is the skill/agent `model:` frontmatter value.
	Hint string
	// Logger is optional; when nil the resolver stays silent.
	Logger *zap.Logger
}

// ResolveModelRouting runs the resolution pipeline. See file header for
// the branch order and contracts.
func ResolveModelRouting(in ResolveModelRoutingInput) ModelRoutingResolution {
	log := in.Logger
	if log == nil {
		log = zap.NewNop()
	}

	fallback := ModelRoutingResolution{
		Client:   in.UserClient,
		Provider: in.UserProvider,
		Model:    in.UserModel,
		Changed:  false,
	}

	hint := strings.TrimSpace(in.Hint)
	if hint == "" {
		return fallback
	}
	if strings.EqualFold(hint, in.UserModel) {
		return fallback
	}

	buildOnUserProvider := func(note string) (ModelRoutingResolution, bool) {
		hinted, herr := in.Router.GetClient(in.UserProvider, hint)
		if herr != nil {
			log.Warn("model router: same-provider build failed",
				zap.String("provider", in.UserProvider),
				zap.String("hint", hint),
				zap.Error(herr))
			fb := fallback
			fb.Note = "fallback-build-failed"
			fb.UserMessage = "wanted model \"" + hint + "\" but the current provider refused it — using your active model instead"
			return fb, false
		}
		return ModelRoutingResolution{
			Client:   hinted,
			Provider: in.UserProvider,
			Model:    hint,
			Changed:  true,
			Note:     note,
		}, true
	}

	buildOnForeignProvider := func(provider, note string) (ModelRoutingResolution, bool) {
		available := in.Router.GetAvailableProviders()
		if !ContainsProvider(available, provider) {
			log.Warn("model router: target provider unavailable",
				zap.String("hinted_provider", provider),
				zap.String("hint", hint))
			fb := fallback
			fb.Note = "fallback-unavailable"
			fb.UserMessage = "wanted \"" + hint + "\" on " + provider +
				" but that provider is not configured (missing API key) — using your active model instead"
			return fb, false
		}
		hinted, herr := in.Router.GetClient(provider, hint)
		if herr != nil {
			log.Warn("model router: cross-provider build failed",
				zap.String("hinted_provider", provider),
				zap.String("hint", hint),
				zap.Error(herr))
			fb := fallback
			fb.Note = "fallback-build-failed"
			fb.UserMessage = "wanted \"" + hint + "\" on " + provider +
				" but its client could not be built — using your active model instead"
			return fb, false
		}
		return ModelRoutingResolution{
			Client:        hinted,
			Provider:      provider,
			Model:         hint,
			Changed:       true,
			CrossProvider: true,
			Note:          note,
		}, true
	}

	// 1. API-cached model list (authoritative when available).
	for _, m := range in.APICache {
		if strings.EqualFold(m.ID, hint) {
			r, _ := buildOnUserProvider("api-cached")
			return r
		}
	}

	// 2. Catalog on the user's provider.
	if _, ok := catalog.Resolve(in.UserProvider, hint); ok {
		r, _ := buildOnUserProvider("catalog-same-provider")
		return r
	}

	// 3. Catalog scan across every known provider.
	if foreign := CatalogProviderOf(hint); foreign != "" && !strings.EqualFold(foreign, in.UserProvider) {
		r, _ := buildOnForeignProvider(foreign, "catalog-cross-provider")
		return r
	}

	// 4. Family-prefix heuristic.
	if fam := FamilyProviderOf(hint); fam != "" {
		if strings.EqualFold(fam, in.UserProvider) {
			r, _ := buildOnUserProvider("family-same-provider")
			return r
		}
		r, _ := buildOnForeignProvider(fam, "family-cross-provider")
		return r
	}

	// 5. Optimistic: try user provider with the hint as-is.
	r, _ := buildOnUserProvider("optimistic-user-provider")
	return r
}

// CatalogProviderOf scans every known provider in the static catalog to
// identify which one owns the given model id. Exported because callers (the
// cli and workers packages) also use it for logging/UI purposes.
func CatalogProviderOf(model string) string {
	if model == "" {
		return ""
	}
	for _, p := range KnownProviders {
		if _, ok := catalog.Resolve(p, model); ok {
			return p
		}
	}
	return ""
}

// FamilyProviderOf is a best-effort prefix/substring heuristic for well-
// known model family names. Returns "" for unrecognized inputs — callers
// then treat the hint as provider-agnostic and fall through.
//
// The matcher is intentionally loose so dated variants and custom-named
// deployments still route to the right provider.
func FamilyProviderOf(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(m, "claude-"),
		strings.Contains(m, "sonnet"),
		strings.Contains(m, "opus"),
		strings.Contains(m, "haiku"):
		return "CLAUDEAI"
	case strings.HasPrefix(m, "gpt-"),
		strings.HasPrefix(m, "chatgpt-"),
		strings.HasPrefix(m, "o1"),
		strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"),
		strings.HasPrefix(m, "text-embedding-"):
		return "OPENAI"
	case strings.HasPrefix(m, "gemini-"):
		return "GOOGLEAI"
	case strings.HasPrefix(m, "grok-"):
		return "XAI"
	case strings.HasPrefix(m, "glm-"):
		return "ZAI"
	case strings.Contains(m, "minimax"):
		return "MINIMAX"
	case strings.HasPrefix(m, "llama"),
		strings.HasPrefix(m, "mistral"),
		strings.HasPrefix(m, "qwen"),
		strings.HasPrefix(m, "deepseek"),
		strings.HasPrefix(m, "phi"):
		return "OLLAMA"
	}
	return ""
}

// KnownProviders enumerates the canonical provider ids used by the manager
// registry. Exported so cli and workers packages can share the same list.
var KnownProviders = []string{
	"OPENAI", "OPENAI_ASSISTANT", "CLAUDEAI", "GOOGLEAI", "XAI", "ZAI",
	"MINIMAX", "STACKSPOT", "OLLAMA", "COPILOT", "GITHUB_MODELS",
	"OPENROUTER",
}

// ContainsProvider returns true iff `p` is present in the list (case
// insensitive).
func ContainsProvider(list []string, p string) bool {
	for _, x := range list {
		if strings.EqualFold(x, p) {
			return true
		}
	}
	return false
}
