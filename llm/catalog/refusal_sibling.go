/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package catalog

import "strings"

// refusalSiblings is the "auto" refusal fallback by model family, most
// capable sibling first. A refused turn (stop_reason: refusal from a safety
// classifier) is resent on a sibling of the same provider; resending on the
// same model does not clear the classifier. Looked up on the provider that
// refused, through the catalog, so a provider that lacks the sibling gets
// none.
//
// The 5.5 releases lead each list (Opus 5.5 Sep 22, Sonnet 5.5 Sep 28,
// Haiku 5.5 Oct 7 2026) and the older id stays behind them for providers
// that do not carry the 5.5 yet. A sibling the refused id already
// contains counts as "already on it" — so Sonnet 5.5 skips claude-sonnet-5
// rather than resending to its predecessor. Haiku 5.5 has no server-side
// fallback of its own; Haiku 4.5 is the one step down, and Haiku 4.5
// itself still has none.
var refusalSiblings = []struct {
	family   string
	siblings []string
}{
	{"fable", []string{"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5", "claude-sonnet-5"}},
	{"mythos", []string{"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5", "claude-sonnet-5"}},
	{"opus", []string{"claude-sonnet-5-5", "claude-sonnet-5"}},
	{"sonnet", []string{"claude-haiku-5-5", "claude-haiku-4-5-20251001"}},
	{"haiku", []string{"claude-haiku-4-5-20251001"}},
}

// RefusalSibling returns the "PROVIDER:model" route handle of the sibling a
// refused turn should be resent on, or "" when the family has no sibling
// the provider knows. Shared by the interactive agent loop and the gRPC
// server so both surfaces fall back to the same model.
func RefusalSibling(provider, model string) string {
	lower := strings.ToLower(model)
	for _, f := range refusalSiblings {
		if !strings.Contains(lower, f.family) {
			continue
		}
		for _, sibling := range f.siblings {
			if strings.Contains(lower, sibling) {
				continue // already on it
			}
			if id, ok := resolveSiblingExactly(provider, sibling); ok {
				return strings.ToUpper(provider) + ":" + id
			}
		}
		return ""
	}
	return ""
}

// resolveSiblingExactly returns the provider's canonical id for sibling
// when the provider really carries that model: an entry whose id or alias
// names the sibling, dots and dashes alike ("claude-opus-5.5" on DEVIN is
// "claude-opus-5-5"). Resolve is deliberately not used: its prefix pass
// maps an id the provider lacks onto an older entry ("claude-sonnet-5-5"
// onto DEVIN's claude-sonnet-5) and misses dotted slugs, so the fallback
// would resend to a slug the provider does not serve or skip one it does.
func resolveSiblingExactly(provider, sibling string) (string, bool) {
	p := strings.ToUpper(provider)
	want := dotsAsDashes(sibling)
	mu.RLock()
	defer mu.RUnlock()
	for _, meta := range registry {
		if meta.Provider != p {
			continue
		}
		if dotsAsDashes(meta.ID) == want {
			return meta.ID, true
		}
		for _, alias := range meta.Aliases {
			if dotsAsDashes(alias) == want {
				return meta.ID, true
			}
		}
	}
	return "", false
}

func dotsAsDashes(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), ".", "-")
}

// SplitRouteHandle splits a "PROVIDER:model" handle as produced by
// RefusalSibling. ok is false when the handle has no provider prefix.
func SplitRouteHandle(handle string) (provider, model string, ok bool) {
	i := strings.Index(handle, ":")
	if i <= 0 || i == len(handle)-1 {
		return "", "", false
	}
	return strings.ToUpper(handle[:i]), handle[i+1:], true
}
