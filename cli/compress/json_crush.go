/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// JSONCrusher reduces JSON payloads — the API responses, config dumps and
// tabular tool outputs an agent reads. It has two modes, applied in order:
//
//   - Lossless: re-canonicalize pretty-printed JSON, eliding insignificant
//     whitespace (json.Compact). Always reversible, never needs CCR.
//   - Lossy (arrays only): for a large top-level array, keep a representative
//     head and tail of elements and replace the dropped middle with a single
//     "_ccr_dropped" sentinel element carrying the @recall marker. The output
//     is still valid JSON; the full array is offloaded to CCR.
//
// The lossy path mirrors Headroom's SmartCrusher sentinel
// ({"_ccr_dropped":"<<ccr:HASH ...>>"}) so downstream consumers can skip the
// sentinel with isCCRSentinel.
type JSONCrusher struct {
	MinItemsToCrush int
	HeadItems       int
	TailItems       int
}

// NewJSONCrusher returns a crusher with sensible defaults.
func NewJSONCrusher() *JSONCrusher {
	return &JSONCrusher{MinItemsToCrush: 20, HeadItems: 8, TailItems: 2}
}

// Name implements Compressor.
func (*JSONCrusher) Name() string { return "json-crush" }

// ccrSentinelKey is the object key that marks a crushed-array sentinel element.
const ccrSentinelKey = "_ccr_dropped"

// isCCRSentinel reports whether a decoded JSON array element is a crush
// sentinel (an object whose only meaningful key is _ccr_dropped). Consumers
// iterating a crushed array should skip these.
func isCCRSentinel(elem any) bool {
	m, ok := elem.(map[string]any)
	if !ok {
		return false
	}
	_, has := m[ccrSentinelKey]
	return has
}

// Detect implements Compressor.
func (c *JSONCrusher) Detect(content string, h Hint) float64 {
	t := strings.TrimSpace(content)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return 0
	}
	if !json.Valid([]byte(t)) {
		return 0
	}
	if t[0] == '[' {
		return 0.85 // arrays are the crushable case
	}
	return 0.7 // objects: lossless compaction still helps
}

// Compress implements Compressor.
func (c *JSONCrusher) Compress(content string, opts Options) (Result, error) {
	t := strings.TrimSpace(content)
	if t == "" || !json.Valid([]byte(t)) {
		return passthrough(content), nil
	}

	// Lossy array crush takes priority when permitted and worthwhile.
	if canDrop(opts) && t[0] == '[' {
		if res, ok := c.crushArray(content, t, opts); ok {
			return res, nil
		}
	}

	// Lossless fallback: compact whitespace. Reversible without CCR.
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(t)); err != nil {
		return passthrough(content), nil
	}
	compact := buf.String()
	if len(compact) >= len(content) {
		return passthrough(content), nil
	}
	return Result{
		Compressed:     compact,
		OriginalSize:   len(content),
		CompressedSize: len(compact),
		Strategy:       c.Name(),
		Reversible:     true,
		Detail:         map[string]int{"mode_lossless": 1},
	}, nil
}

// crushArray keeps head+tail elements and replaces the middle with a sentinel.
// Returns ok=false when the array is too small or crushing would not help.
func (c *JSONCrusher) crushArray(original, trimmed string, opts Options) (Result, bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
		return Result{}, false
	}
	if len(arr) < c.MinItemsToCrush || c.HeadItems+c.TailItems >= len(arr) {
		return Result{}, false
	}

	marker, key := offload(original, opts)
	if marker == "" {
		return Result{}, false
	}
	dropped := len(arr) - c.HeadItems - c.TailItems

	kept := make([]json.RawMessage, 0, c.HeadItems+c.TailItems+1)
	kept = append(kept, arr[:c.HeadItems]...)
	sentinel, _ := json.Marshal(map[string]string{
		ccrSentinelKey: fmt.Sprintf("%s %d items offloaded — recall full array with @recall %s", marker, dropped, marker),
	})
	kept = append(kept, sentinel)
	kept = append(kept, arr[len(arr)-c.TailItems:]...)

	out, err := json.Marshal(kept)
	if err != nil || len(out) >= len(original) {
		return Result{}, false
	}
	return Result{
		Compressed:     string(out),
		OriginalSize:   len(original),
		CompressedSize: len(out),
		Strategy:       c.Name(),
		CacheKey:       key,
		Reversible:     true,
		Detail:         map[string]int{"items_kept": c.HeadItems + c.TailItems, "items_dropped": dropped},
	}, true
}
