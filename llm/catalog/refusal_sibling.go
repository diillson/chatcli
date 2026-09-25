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
var refusalSiblings = []struct {
	family   string
	siblings []string
}{
	{"fable", []string{"claude-opus-5", "claude-sonnet-5"}},
	{"mythos", []string{"claude-opus-5", "claude-sonnet-5"}},
	{"opus", []string{"claude-sonnet-5"}},
	{"sonnet", []string{"claude-haiku-4-5-20251001"}},
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
			if _, known := Resolve(provider, sibling); known {
				return strings.ToUpper(provider) + ":" + sibling
			}
		}
		return ""
	}
	return ""
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
