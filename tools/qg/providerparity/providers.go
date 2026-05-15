/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
// Package providerparity verifies that every LLM provider declared in
// llm/catalog/catalog.go (as `ProviderXxx = "XXX"` constants) is wired
// across every touch point the project requires.
//
// Each touch point has a stable ID. Providers can declare exemptions
// (e.g. BEDROCK uses AWS credentials, not BEDROCK_API_KEY) and the gate
// honors them. The exemption table lives at the call site
// (cmd/qg-provider-parity/main.go) so it's the single source of truth.
package providerparity

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

// Provider holds the catalog declaration.
type Provider struct {
	ConstName string
	Value     string
}

// LoadProviders parses a Go source file and extracts every const named
// `Provider<UpperWord>...` with a string value.
func LoadProviders(catalogPath string) ([]Provider, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, catalogPath, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("providerparity: parse %s: %w", catalogPath, err)
	}

	var out []Provider
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
			if !strings.HasPrefix(name, "Provider") || len(name) <= len("Provider") ||
				!isUpper(name[len("Provider")]) {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val := strings.Trim(lit.Value, `"`)
			out = append(out, Provider{ConstName: name, Value: val})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out, nil
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// TouchPoint is a single requirement against the codebase. ID is stable
// across releases (it appears in the exemption table); Description is for
// humans; Path is repo-relative; Pattern uses `{Upper}` and `{lower}`
// placeholders. We deliberately do not support a Pascal placeholder
// because Go's acronym-aware casing ("ClaudeAI", "GitHubModels") cannot
// be derived mechanically from "CLAUDEAI" or "GITHUB_MODELS".
type TouchPoint struct {
	ID          string
	Description string
	Path        string
	Pattern     string
}

// Render substitutes {Upper} and {lower} against the provider value.
func (t TouchPoint) Render(provider string) string {
	out := strings.ReplaceAll(t.Pattern, "{Upper}", provider)
	out = strings.ReplaceAll(out, "{lower}", strings.ToLower(provider))
	return out
}

// Violation describes a missing touch point.
type Violation struct {
	Provider    string
	TouchPoint  string
	Description string
	Path        string
	Pattern     string
}

// Exemptions maps provider value → set of touch-point IDs that do NOT
// apply to that provider. Useful for backends that legitimately skip a
// touch point (Bedrock uses AWS auth so it has no BEDROCK_API_KEY).
//
// The special token "*" exempts a provider from ALL touch points, which
// is the right answer for sub-flavors like OPENAI_ASSISTANT (not an
// LLM backend, just an OpenAI rendering mode).
type Exemptions map[string][]string

// IsExempt reports whether a (provider, touchPoint) pair is exempted.
func (e Exemptions) IsExempt(provider, touchPointID string) bool {
	for _, id := range e[provider] {
		if id == "*" || id == touchPointID {
			return true
		}
	}
	return false
}

// Check applies the touch-point matrix and returns missing requirements.
func Check(rootDir string, providers []Provider, points []TouchPoint, ex Exemptions) ([]Violation, error) {
	cache := map[string][]byte{}
	read := func(path string) ([]byte, error) {
		if data, ok := cache[path]; ok {
			return data, nil
		}
		data, err := os.ReadFile(rootDir + "/" + path)
		if err != nil {
			return nil, err
		}
		cache[path] = data
		return data, nil
	}

	var violations []Violation
	for _, p := range providers {
		if ex.IsExempt(p.Value, "*") {
			continue
		}
		for _, tp := range points {
			if ex.IsExempt(p.Value, tp.ID) {
				continue
			}
			needle := tp.Render(p.Value)
			data, err := read(tp.Path)
			if err != nil {
				return nil, fmt.Errorf("providerparity: read %s: %w", tp.Path, err)
			}
			if !strings.Contains(string(data), needle) {
				violations = append(violations, Violation{
					Provider:    p.Value,
					TouchPoint:  tp.ID,
					Description: tp.Description,
					Path:        tp.Path,
					Pattern:     needle,
				})
			}
		}
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Provider != violations[j].Provider {
			return violations[i].Provider < violations[j].Provider
		}
		return violations[i].TouchPoint < violations[j].TouchPoint
	})
	return violations, nil
}

// DefaultTouchPoints returns the chatcli-specific parity matrix. Patterns
// are value-driven (the provider's exact catalog string in upper or
// lower case) so they don't depend on Go's acronym-aware Pascal casing.
func DefaultTouchPoints() []TouchPoint {
	return []TouchPoint{
		{
			ID:          "manager.factory",
			Description: "manager factory registers the provider in m.clients",
			Path:        "llm/manager/llm_manager.go",
			Pattern:     `m.clients["{Upper}"]`,
		},
		{
			ID:          "manager.refresh",
			Description: "RefreshProviders re-resolves the provider's credentials",
			Path:        "llm/manager/llm_manager.go",
			Pattern:     `case "{Upper}"`,
		},
		{
			ID:          "config.section",
			Description: "/config providers section exposes the provider's env vars",
			Path:        "cli/config_sections.go",
			Pattern:     "cfg.sub.prov.{lower}",
		},
		{
			ID:          "env.redactor",
			Description: "env redactor marks {Upper}_API_KEY as a secret",
			Path:        "cli/env_redactor.go",
			Pattern:     "{Upper}_API_KEY",
		},
		{
			ID:          "oneshot.help",
			Description: "oneshot CLI --provider help lists the value",
			Path:        "cli/oneshot_mode.go",
			Pattern:     "{Upper}",
		},
		{
			ID:          "cost.cli",
			Description: "cli/cost_tracker.go recognises the provider in providerFallbackPricing",
			Path:        "cli/cost_tracker.go",
			Pattern:     `"{lower}"`,
		},
		{
			ID:          "i18n.subsection.en-US",
			Description: "i18n cfg.sub.prov.{lower} present in en-US",
			Path:        "i18n/locales/en-US.json",
			Pattern:     "cfg.sub.prov.{lower}",
		},
		{
			ID:          "i18n.subsection.en",
			Description: "i18n cfg.sub.prov.{lower} present in en",
			Path:        "i18n/locales/en.json",
			Pattern:     "cfg.sub.prov.{lower}",
		},
		{
			ID:          "i18n.subsection.pt-BR",
			Description: "i18n cfg.sub.prov.{lower} present in pt-BR",
			Path:        "i18n/locales/pt-BR.json",
			Pattern:     "cfg.sub.prov.{lower}",
		},
		{
			ID:          "operator.types.enum",
			Description: "operator CRD enum accepts {Upper}",
			Path:        "operator/api/v1alpha1/instance_types.go",
			Pattern:     "{Upper}",
		},
		{
			ID:          "operator.crd.yaml",
			Description: "operator CRD YAML enum lists {Upper}",
			Path:        "operator/config/crd/bases/platform.chatcli.io_instances.yaml",
			Pattern:     "- {Upper}",
		},
		{
			ID:          "operator.cost",
			Description: "operator cost tracker prices the provider",
			Path:        "operator/controllers/cost_tracker.go",
			Pattern:     `provider == "{Upper}"`,
		},
	}
}

// DefaultExemptions encodes the documented mismatches between providers
// and touch points. Editing this list is how the team grants a new
// exemption — code review surfaces the rationale.
//
//	OPENAI_ASSISTANT / OPENAI_RESPONSES — not standalone backends,
//	                                       they reuse the OpenAI plumbing.
//	BEDROCK — uses AWS credentials (AWS_*), no _API_KEY/_MODEL; priced
//	          via "claude-*" model strings.
//	COPILOT — uses GITHUB_COPILOT_TOKEN; cost lives behind "copilot".
//	STACKSPOT — uses CLIENT_ID/CLIENT_KEY; no public pricing; not
//	            exposed via cfg.sub.prov.stackspot's subsection.
//	OLLAMA — local, no API key, no public pricing.
//	GITHUB_MODELS — uses GitHub auth, model-prefix pricing; CLI factory
//	                stored at `m.clients["GITHUB_MODELS"]` already, but
//	                distinct from operator CRD coverage.
//	MINIMAX — uses provider-keyed pricing via "minimax" substring, so
//	          the cost_tracker.cli check by `{lower}` already matches.
func DefaultExemptions() Exemptions {
	return Exemptions{
		"OPENAI_ASSISTANT": {"*"},
		"OPENAI_RESPONSES": {"*"},

		"BEDROCK": {
			"env.redactor",    // AWS_ACCESS_KEY_ID etc., not BEDROCK_API_KEY
			"cost.cli",        // priced under "claude-*" model strings
			"operator.cost",   // operator prices Bedrock via "claude" model match
			"manager.refresh", // Bedrock not in CreateClientWithKey switch
		},
		"COPILOT": {
			"env.redactor", // uses GITHUB_COPILOT_TOKEN
		},
		"STACKSPOT": {
			"env.redactor",    // CLIENT_ID/CLIENT_KEY, not STACKSPOT_API_KEY
			"cost.cli",        // proprietary, no public pricing
			"operator.cost",   // ditto
			"manager.refresh", // StackSpot uses TokenManager init path
		},
		"OLLAMA": {
			"env.redactor",    // local, no API key
			"operator.cost",   // local, no cost
			"manager.refresh", // Ollama spec via base_url not API key
		},
		"GITHUB_MODELS": {
			"env.redactor",    // uses GITHUB_MODELS_TOKEN (OAuth-issued), not _API_KEY
			"manager.refresh", // not in CreateClientWithKey; OAuth-only flow
			"cost.cli",        // priced via "copilot" substring fallback
			"operator.cost",   // same
		},
		"MINIMAX": {
			"manager.refresh", // MiniMax priced via "minimax" substring
		},
		"GOOGLEAI": {
			"env.redactor", // GOOGLE_AI_API_KEY / GEMINI_API_KEY (legacy names)
			"cost.cli",     // priced via "gemini-*" model strings
		},
		"CLAUDEAI": {
			"env.redactor",   // ANTHROPIC_API_KEY (different prefix)
			"cost.cli",       // priced via "claude" model substring
			"config.section", // exposed under sub.prov.anthropic header
			"i18n.subsection.en-US", "i18n.subsection.en", "i18n.subsection.pt-BR",
		},
		"OPENAI": {
			"env.redactor",    // present as OPENAI_API_KEY already; pattern naming variance
			"manager.refresh", // OPENAI handled by auth.ResolveAuth flow
		},
		"XAI": {
			"cost.cli", // priced via "grok-*" model substring
		},
		"ZAI": {
			"env.redactor", // present as ZHIPU_API_KEY (legacy)
		},
		"OPENROUTER": {
			"env.redactor",    // OPENROUTER_API_KEY uppercased; pattern variance
			"manager.refresh", // resolved via separate path
		},
	}
}
