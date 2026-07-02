/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// TestLogCompressorNeverDropsErrorLines is the log compressor's core value
// proposition as a property test: across randomized log shapes (seeded, so
// failures reproduce), every ERROR line within the MaxErrors budget survives
// compression verbatim. Losing an error line would defeat the whole point of
// keeping "the signal".
func TestLogCompressorNeverDropsErrorLines(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) // deterministic: failures reproduce
	c := NewLogCompressor()

	for trial := 0; trial < 50; trial++ {
		var b strings.Builder
		var errorLines []string
		total := 200 + rng.Intn(400)
		for i := 0; i < total; i++ {
			switch rng.Intn(20) {
			case 0:
				line := fmt.Sprintf("ERROR trial=%d line=%d failure mode %d", trial, i, rng.Intn(1000))
				if len(errorLines) < c.MaxErrors {
					errorLines = append(errorLines, line)
				}
				b.WriteString(line)
			case 1:
				fmt.Fprintf(&b, "WARN category%d: something odd %d", rng.Intn(3), i)
			default:
				fmt.Fprintf(&b, "INFO routine operation %d ok", i)
			}
			b.WriteByte('\n')
		}

		opts := Options{Mode: ModeLossyWithCCR, Store: NewMemoryStore()}
		res, err := c.Compress(b.String(), opts)
		if err != nil {
			t.Fatalf("trial %d: Compress: %v", trial, err)
		}
		if res.Strategy == "passthrough" {
			continue // nothing dropped, nothing to verify
		}
		for _, el := range errorLines {
			if !strings.Contains(res.Compressed, el) {
				t.Fatalf("trial %d: compressed log lost error line %q", trial, el)
			}
		}
	}
}
