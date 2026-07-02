/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"fmt"
	"strings"
	"testing"
)

// syntheticLog builds an n-line build/test log with realistic level mixing —
// the shape of payload the agent loop routes on every tool result.
func syntheticLog(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		switch {
		case i%97 == 0:
			fmt.Fprintf(&b, "2026-07-02T12:00:%02d ERROR request failed: connection refused (attempt %d)\n", i%60, i)
		case i%31 == 0:
			fmt.Fprintf(&b, "2026-07-02T12:00:%02d WARN retrying with backoff category=%d\n", i%60, i%5)
		default:
			fmt.Fprintf(&b, "2026-07-02T12:00:%02d INFO handled request id=%d path=/api/v1/items status=200\n", i%60, i)
		}
	}
	return b.String()
}

// BenchmarkRouterDetectLargeLog measures the routing (detection) cost alone on
// a large hint-less payload — the agent-loop hot path before any compression
// work starts. Guards the "detection must not scan/allocate the whole payload"
// property.
func BenchmarkRouterDetectLargeLog(b *testing.B) {
	r := newDefaultRouter()
	content := syntheticLog(60_000) // ~4.5MB
	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if c := r.selectCompressor(content, Hint{}); c == nil {
			b.Fatal("no compressor selected for a log payload")
		}
	}
}
