/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestJSONDetect(t *testing.T) {
	c := NewJSONCrusher()
	if got := c.Detect(`[1,2,3]`, Hint{}); got < 0.8 {
		t.Fatalf("array should detect strongly, got %v", got)
	}
	if got := c.Detect(`{"a":1}`, Hint{}); got < 0.6 {
		t.Fatalf("object should detect, got %v", got)
	}
	if got := c.Detect(`not json at all`, Hint{}); got != 0 {
		t.Fatalf("non-json should not detect, got %v", got)
	}
	if got := c.Detect(`{"broken": `, Hint{}); got != 0 {
		t.Fatalf("invalid json should not detect, got %v", got)
	}
}

func TestJSONLosslessCompaction(t *testing.T) {
	pretty := "{\n  \"name\": \"chatcli\",\n  \"count\": 3,\n  \"tags\": [\n    \"a\",\n    \"b\"\n  ]\n}"
	c := NewJSONCrusher()
	// No store: only the lossless path is available.
	res, _ := c.Compress(pretty, Options{Mode: ModeLossyWithCCR, Store: nil})
	if res.Strategy != "json-crush" {
		t.Fatalf("pretty JSON should compact losslessly, got %q", res.Strategy)
	}
	if res.CacheKey != "" {
		t.Fatal("lossless compaction must not use CCR")
	}
	// Compacted output must re-parse to the same value.
	var a, b any
	_ = json.Unmarshal([]byte(pretty), &a)
	if err := json.Unmarshal([]byte(res.Compressed), &b); err != nil {
		t.Fatalf("compacted output is not valid JSON: %v", err)
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatal("lossless compaction changed the JSON value")
	}
}

func TestJSONArrayCrushWithSentinel(t *testing.T) {
	var elems []string
	for i := 0; i < 100; i++ {
		elems = append(elems, fmt.Sprintf(`{"id":%d,"name":"item-%d"}`, i, i))
	}
	original := "[" + strings.Join(elems, ",") + "]"
	store := NewMemoryStore()
	c := NewJSONCrusher()
	res, err := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheKey == "" || !res.Reversible {
		t.Fatalf("large array crush must be reversible via CCR: %+v", res)
	}
	if res.CompressedSize >= res.OriginalSize {
		t.Fatal("crush should reduce size")
	}
	// Output must still be valid JSON and contain a sentinel.
	var arr []any
	if err := json.Unmarshal([]byte(res.Compressed), &arr); err != nil {
		t.Fatalf("crushed output is not valid JSON: %v", err)
	}
	sentinels := 0
	for _, e := range arr {
		if isCCRSentinel(e) {
			sentinels++
		}
	}
	if sentinels != 1 {
		t.Fatalf("expected exactly 1 CCR sentinel, got %d", sentinels)
	}
	// Original recoverable byte-for-byte.
	if got, ok, _ := store.Get(res.CacheKey); !ok || got != original {
		t.Fatal("CCR did not preserve the byte-identical array")
	}
}

func TestJSONSmallArrayNotCrushed(t *testing.T) {
	original := `[{"id":1},{"id":2},{"id":3}]`
	c := NewJSONCrusher()
	res, _ := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: NewMemoryStore()})
	// Too small to crush; compaction is already minimal, so passthrough.
	if res.CacheKey != "" {
		t.Fatalf("small array must not be crushed/offloaded, got key %q", res.CacheKey)
	}
}
