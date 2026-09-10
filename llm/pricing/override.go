/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pricing

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// OverrideEnv names the variable that pins per-model rates by hand. It is
// the operator's word on price and outranks every other source: the rate a
// provider lists for the account, the static tables, and the "known zero"
// of subscription or self-hosted backends. The case it exists for is a
// wrapper that meters tokens without telling ChatCLI the price — an
// enterprise Devin CLI whose model listing carries no cost_summary — but
// it applies to any provider.
//
// Format, one entry per ';' (newlines also separate entries):
//
//	PROVIDER:model=input/output[;PROVIDER:model=input/output…]
//
// where input and output are USD per million tokens. The provider is the
// ChatCLI provider name (DEVIN, OPENAI, OLLAMA…), case-insensitive; the
// model is matched case-insensitively and may be "*" to price every model
// of that provider that has no more specific entry. Whitespace around
// tokens is ignored. Example:
//
//	CHATCLI_MODEL_PRICING="DEVIN:claude-sonnet-4.6=3/15;DEVIN:*=1/5"
const OverrideEnv = "CHATCLI_MODEL_PRICING"

// Wildcard is the model token that prices a whole provider.
const Wildcard = "*"

// overrideSet is one parsed value of OverrideEnv.
type overrideSet struct {
	raw       string
	rates     map[string]Rate // key(provider, model) → rate
	malformed []string        // entries that did not parse, verbatim
}

var (
	overrideMu     sync.Mutex
	overrideCached *overrideSet
)

// ParseOverrides decodes a spec in the OverrideEnv format. Every entry is
// independent: a malformed one is reported and skipped, never taking the
// good ones down with it — a typo in one price must not silently zero
// every other override.
func ParseOverrides(spec string) (map[string]Rate, []string) {
	rates := map[string]Rate{}
	var malformed []string
	for _, entry := range strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		provider, model, rate, err := parseOverrideEntry(entry)
		if err != nil {
			malformed = append(malformed, entry)
			continue
		}
		rates[key(provider, model)] = rate
	}
	return rates, malformed
}

func parseOverrideEntry(entry string) (provider, model string, rate Rate, err error) {
	lhs, rhs, ok := strings.Cut(entry, "=")
	if !ok {
		return "", "", Rate{}, fmt.Errorf("missing '='")
	}
	provider, model, ok = strings.Cut(lhs, ":")
	if !ok {
		return "", "", Rate{}, fmt.Errorf("missing ':' between provider and model")
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return "", "", Rate{}, fmt.Errorf("empty provider or model")
	}
	inRaw, outRaw, ok := strings.Cut(rhs, "/")
	if !ok {
		return "", "", Rate{}, fmt.Errorf("missing '/' between input and output rate")
	}
	in, err := parseRate(inRaw)
	if err != nil {
		return "", "", Rate{}, err
	}
	out, err := parseRate(outRaw)
	if err != nil {
		return "", "", Rate{}, err
	}
	if in == 0 && out == 0 {
		// An explicit zero pair is a deliberate "this provider is free to
		// me" — accepted as a rate, unlike Register's "unlisted" zero.
		return provider, model, Rate{}, nil
	}
	return provider, model, Rate{InputPerMTok: in, OutputPerMTok: out}, nil
}

func parseRate(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, fmt.Errorf("negative rate")
	}
	return v, nil
}

// overrides returns the parsed value of OverrideEnv as it stands right
// now. The environment is read on every call and re-parsed only when the
// raw value changed, so a /reload that rewrites the variable takes effect
// on the next lookup with no registration step, and the common case (same
// value as last time) costs one string compare.
func overrides() *overrideSet {
	raw := os.Getenv(OverrideEnv)
	overrideMu.Lock()
	defer overrideMu.Unlock()
	if overrideCached != nil && overrideCached.raw == raw {
		return overrideCached
	}
	rates, malformed := ParseOverrides(raw)
	overrideCached = &overrideSet{raw: raw, rates: rates, malformed: malformed}
	return overrideCached
}

// LookupOverride returns the operator-pinned rate for provider+model: the
// exact entry first, then the provider's wildcard. ok=false when neither
// exists, in which case the caller falls through to its usual sources.
func LookupOverride(provider, model string) (Rate, bool) {
	set := overrides()
	if len(set.rates) == 0 {
		return Rate{}, false
	}
	if r, ok := set.rates[key(provider, model)]; ok {
		return r, true
	}
	if r, ok := set.rates[key(provider, Wildcard)]; ok {
		return r, true
	}
	return Rate{}, false
}

// OverrideStatus reports how many override entries are active and which
// entries of the current value failed to parse, for /config to show.
func OverrideStatus() (active int, malformed []string) {
	set := overrides()
	return len(set.rates), append([]string(nil), set.malformed...)
}
