/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

// ContentRouter is the entry point of the compression layer. It holds an
// ordered set of Compressors, detects (or is told via a Hint) which one fits a
// payload, runs it, and enforces the package-wide safety contract:
//
//   - ModeOff or below Threshold  -> verbatim passthrough.
//   - The chosen compressor must return Reversible=true. If it ever returns an
//     irreversible Result (a bug), the router discards it and falls back to
//     passthrough rather than silently degrading the model's context.
//   - A "reduction" that grew the payload (CompressedSize > OriginalSize) is
//     discarded in favor of the original — compression never makes prompts
//     bigger.
//
// The router is safe for concurrent use as long as its Compressors are.
type ContentRouter struct {
	compressors   []Compressor
	minConfidence float64
}

// defaultMinConfidence is the floor a compressor's Detect score must clear to
// be selected. Below it the router treats the content as generic and passes it
// through (the lossless trimmer/truncator downstream still applies).
const defaultMinConfidence = 0.5

// NewContentRouter builds a router over the given compressors, tried in
// descending Detect-confidence order. Pass them in any order; selection is by
// score, not position.
func NewContentRouter(compressors ...Compressor) *ContentRouter {
	return &ContentRouter{
		compressors:   compressors,
		minConfidence: defaultMinConfidence,
	}
}

// Compress reduces content according to opts, routing to the best-matching
// compressor. It always returns a usable Result — never an error to the
// caller's hot path — because a compression failure must degrade to
// passthrough, not break the agent turn. (Compressor errors are reflected by
// returning the passthrough Result.)
func (r *ContentRouter) Compress(content string, h Hint, opts Options) Result {
	res := r.route(content, h, opts)
	opts.Metrics.RecordCompression(res)
	return res
}

func (r *ContentRouter) route(content string, h Hint, opts Options) Result {
	if opts.Mode == ModeOff || len(r.compressors) == 0 {
		return passthrough(content)
	}
	if opts.Threshold > 0 && len(content) < opts.Threshold {
		return passthrough(content)
	}

	best := r.selectCompressor(content, h)
	if best == nil {
		return passthrough(content)
	}

	res, err := best.Compress(content, opts)
	if err != nil {
		// Degrade safely: a broken compressor must not cost the turn.
		return passthrough(content)
	}

	// Safety net: reject any Result that violates the reversibility contract
	// or that failed to actually shrink the payload.
	if !res.Reversible || res.CompressedSize >= res.OriginalSize {
		return passthrough(content)
	}
	return res
}

// selectCompressor returns the highest-confidence compressor for content, or
// nil when none clears the confidence floor.
func (r *ContentRouter) selectCompressor(content string, h Hint) Compressor {
	var best Compressor
	var bestScore float64
	for _, c := range r.compressors {
		score := c.Detect(content, h)
		if score > bestScore {
			best, bestScore = c, score
		}
	}
	if bestScore < r.minConfidence {
		return nil
	}
	return best
}
