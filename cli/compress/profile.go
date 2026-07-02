/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

// Profile selects how much content the lossy compressors keep before
// offloading the rest to CCR. It tunes the caps, never the safety contract:
// every profile remains fully reversible via @recall, and lossless-only or
// off modes ignore the profile entirely.
//
// Backed by int32 so it stores directly into the atomic on Layer.
type Profile int32

const (
	// ProfileDefault is the Headroom-equivalent tuning the ratios eval was
	// calibrated against. The recommended setting.
	ProfileDefault Profile = iota

	// ProfileConservative keeps roughly twice as much context per payload.
	// For users who prefer fewer @recall round-trips over maximum savings.
	ProfileConservative

	// ProfileAggressive keeps roughly half as much context per payload.
	// For long unattended agent runs where context headroom is the priority.
	ProfileAggressive
)

// String renders the profile for /config and logs.
func (p Profile) String() string {
	switch p {
	case ProfileConservative:
		return "conservative"
	case ProfileAggressive:
		return "aggressive"
	case ProfileDefault:
		return "default"
	default:
		return "unknown"
	}
}

// ParseProfile maps a config/env string to a Profile. Unknown values fall
// back to ProfileDefault and report ok=false so callers can warn.
func ParseProfile(s string) (Profile, bool) {
	switch s {
	case "conservative", "safe", "light":
		return ProfileConservative, true
	case "aggressive", "max", "deep":
		return ProfileAggressive, true
	case "default", "balanced", "":
		return ProfileDefault, true
	default:
		return ProfileDefault, false
	}
}

// newRouterFor builds a router over the full built-in set tuned for p. Order
// is irrelevant — the router selects by detection confidence.
func newRouterFor(p Profile) *ContentRouter {
	return NewContentRouter(
		NewSearchCompressorFor(p),
		NewLogCompressorFor(p),
		NewDiffCompressorFor(p),
		NewJSONCrusherFor(p),
		// Never auto-fires (Detect returns 0 unless Hint.MIME=="code"); kept in
		// the router so explicit code compression flows through the same path.
		NewCodeCompressor(),
		// Auto-fires only on web/reference tool output (not local file reads)
		// or an explicit prose/markdown hint.
		NewProseCompressorFor(p),
	)
}
