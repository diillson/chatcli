/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package compress is ChatCLI's content-aware, reversible context-compression
// layer. It shrinks the verbose, structured payloads an agent reads — grep/
// ripgrep results, build/test logs, unified diffs, large JSON arrays, source
// code — before they reach the model, while preserving the information the
// model actually needs to act.
//
// Design goals (in priority order):
//
//  1. Never degrade. The default mode is lossless. Lossy reduction only ever
//     happens when the dropped bytes are first written to a CCR store
//     (Contextual Compression Retrieval) so the model can recover the
//     original verbatim with the @recall tool. Below a size threshold the
//     output is returned byte-identical to the input.
//  2. Keyless and self-hosted. Everything here is pure Go — no network, no
//     trained model, no cgo. (Prose/ML compression is a future pluggable
//     backend behind the Compressor interface; it is intentionally absent.)
//  3. Content-aware. A ContentRouter detects the payload type (or trusts a
//     source hint such as the originating tool name) and routes to the
//     compressor that understands that structure.
//
// The package is a leaf: it imports only the standard library so it can be
// used from cli/, cli/plugins/ and the history trimmer without import cycles.
package compress

// Mode selects how aggressively the layer reduces a payload. Backed by int32
// so it stores directly into the atomic on Layer without a widening conversion.
type Mode int32

const (
	// ModeOff disables compression entirely; Compress is a verbatim
	// passthrough. Used when the user sets CHATCLI_COMPRESSION=off.
	ModeOff Mode = iota

	// ModeLosslessOnly applies only reductions that lose no information
	// (e.g. JSON re-canonicalization, whitespace normalization). The output
	// is always fully reconstructable without a CCR lookup.
	ModeLosslessOnly

	// ModeLossyWithCCR additionally allows dropping low-value rows/lines/
	// hunks, but only after the original payload is persisted to the CCR
	// store and a retrieval marker is embedded in the output. Nothing is
	// truly lost — the model can @recall the original.
	ModeLossyWithCCR
)

// String renders the mode for /config and logs.
func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeLosslessOnly:
		return "lossless"
	case ModeLossyWithCCR:
		return "lossy-with-ccr"
	default:
		return "unknown"
	}
}

// ParseMode maps a config/env string to a Mode. Unknown values fall back to
// ModeLossyWithCCR (the recommended default) and report ok=false so callers
// can warn.
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "off", "none", "disabled":
		return ModeOff, true
	case "lossless", "lossless-only", "safe":
		return ModeLosslessOnly, true
	case "lossy", "lossy-with-ccr", "ccr", "":
		return ModeLossyWithCCR, true
	default:
		return ModeLossyWithCCR, false
	}
}

// Hint carries out-of-band signals about a payload's origin so the router can
// route with high confidence instead of guessing from content alone. All
// fields are optional.
type Hint struct {
	// ToolName is the originating tool, e.g. "@search", "@read", "git diff".
	// The router maps known tools straight to a compressor.
	ToolName string

	// Filename is the path the content came from, when known. Its extension
	// helps the code compressor pick a language.
	Filename string

	// MIME is an explicit content type when the caller already knows it.
	MIME string
}

// Options configures a single Compress call. The zero value is safe: it
// behaves as ModeOff with no store (verbatim passthrough).
type Options struct {
	// Mode selects the reduction strategy. See the Mode constants.
	Mode Mode

	// Store is the CCR backend used to offload dropped originals in
	// ModeLossyWithCCR. When nil, lossy compressors degrade to their
	// lossless behavior (no row/line dropping) so reversibility is never
	// violated.
	Store Store

	// Threshold is the minimum input size, in bytes, below which Compress is
	// a verbatim passthrough regardless of Mode. Guarantees small payloads
	// are byte-identical to today's behavior. A value <= 0 means "always
	// attempt compression".
	Threshold int

	// Metrics, when non-nil, receives per-call accounting. Safe for
	// concurrent use.
	Metrics *Metrics
}

// Result is the outcome of compressing one payload.
type Result struct {
	// Compressed is the reduced payload to send to the model. When no
	// reduction was applied it equals the input.
	Compressed string

	// OriginalSize and CompressedSize are byte lengths, for ratio reporting.
	OriginalSize   int
	CompressedSize int

	// Strategy names the compressor that handled the payload, e.g. "search",
	// "log", "diff", "json-crush", "code-ast", or "passthrough".
	Strategy string

	// CacheKey is the CCR key under which the full original was stored, or ""
	// when nothing was offloaded (lossless or passthrough). When set, the
	// Compressed payload contains a retrieval marker (see FormatMarker).
	CacheKey string

	// Reversible is true when the original is fully recoverable — either
	// because the reduction was lossless, or because the dropped bytes were
	// offloaded to CCR. The layer never returns an irreversible Result.
	Reversible bool

	// Detail carries compressor-specific counters (e.g. "matches_kept",
	// "lines_dropped") for diagnostics. May be nil.
	Detail map[string]int
}

// SavedBytes reports how many bytes the reduction removed from the prompt
// (never negative).
func (r Result) SavedBytes() int {
	if r.CompressedSize >= r.OriginalSize {
		return 0
	}
	return r.OriginalSize - r.CompressedSize
}

// Ratio is CompressedSize/OriginalSize in [0,1]; 1.0 means no reduction.
func (r Result) Ratio() float64 {
	if r.OriginalSize == 0 {
		return 1.0
	}
	return float64(r.CompressedSize) / float64(r.OriginalSize)
}

// Compressor reduces one class of content. Implementations must be safe for
// concurrent use — the router may invoke them from multiple goroutines.
type Compressor interface {
	// Name is the strategy identifier reported in Result.Strategy.
	Name() string

	// Detect returns the confidence in [0,1] that this compressor is the
	// right one for content, given an optional origin hint. The router picks
	// the highest scorer. A score of 0 means "not my content".
	Detect(content string, h Hint) float64

	// Compress reduces content under opts. It must honor opts.Mode and the
	// reversibility contract: in ModeLossyWithCCR with a nil Store it must
	// not drop information. Returning Reversible=false is a programming
	// error; the router will reject it and fall back to passthrough.
	Compress(content string, opts Options) (Result, error)
}

// passthrough is the always-safe identity Result for content that no
// compressor claims, or when Mode is off / below threshold.
func passthrough(content string) Result {
	return Result{
		Compressed:     content,
		OriginalSize:   len(content),
		CompressedSize: len(content),
		Strategy:       "passthrough",
		Reversible:     true,
	}
}
