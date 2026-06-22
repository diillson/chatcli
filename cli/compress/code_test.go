/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const sampleGo = `package demo

import (
	"fmt"
	"strings"
)

// Greeter greets people.
type Greeter struct {
	Prefix string
}

const Answer = 42

// Hello returns a greeting. This doc and the signature must survive; the body
// must be elided.
func (g *Greeter) Hello(name string) string {
	parts := []string{g.Prefix, name}
	joined := strings.Join(parts, " ")
	fmt.Println(joined)
	for i := 0; i < 10; i++ {
		joined += "!"
	}
	return joined
}

func unexportedHelper(x int) int {
	y := x * 2
	return y + 1
}
`

func TestCodeCompressorNeverAutoFires(t *testing.T) {
	c := NewCodeCompressor()
	// Without an explicit code hint it must report zero confidence, so the
	// router never selects it for ordinary tool output.
	if got := c.Detect(sampleGo, Hint{}); got != 0 {
		t.Fatalf("code compressor must not auto-fire, got confidence %v", got)
	}
	if got := c.Detect(sampleGo, Hint{ToolName: "@read"}); got != 0 {
		t.Fatalf("code compressor must not fire on @read output, got %v", got)
	}
	if got := c.Detect(sampleGo, Hint{MIME: "code"}); got < 0.9 {
		t.Fatalf("explicit code hint should enable it, got %v", got)
	}
}

func TestCodeCompressorGoSkeleton(t *testing.T) {
	c := NewCodeCompressor()
	store := NewMemoryStore()
	res, err := c.Compress(sampleGo, Options{Mode: ModeLossyWithCCR, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompressedSize >= res.OriginalSize {
		t.Fatalf("skeleton should be smaller, got %d >= %d", res.CompressedSize, res.OriginalSize)
	}
	if !res.Reversible || res.CacheKey == "" {
		t.Fatalf("code skeleton must be reversible via CCR: %+v", res)
	}

	out := res.Compressed
	// Signatures + decls survive.
	for _, want := range []string{"package demo", "type Greeter struct", "const Answer = 42", "func (g *Greeter) Hello(name string) string", "func unexportedHelper(x int) int"} {
		if !strings.Contains(out, want) {
			t.Errorf("skeleton missing %q\n---\n%s", want, out)
		}
	}
	// Body statements must be gone.
	if strings.Contains(out, "strings.Join(parts") || strings.Contains(out, "x * 2") {
		t.Errorf("function bodies should be elided:\n%s", out)
	}
	// The skeleton (minus the trailing recall comment) must still be valid Go.
	skeleton := out[:strings.LastIndex(out, "// [code skeleton")]
	if _, perr := parser.ParseFile(token.NewFileSet(), "", skeleton, parser.AllErrors); perr != nil {
		t.Fatalf("emitted skeleton is not valid Go: %v\n---\n%s", perr, skeleton)
	}
	// Original recoverable byte-for-byte.
	if got, ok, _ := store.Get(res.CacheKey); !ok || got != sampleGo {
		t.Fatal("CCR did not preserve the byte-identical source")
	}
}

func TestCodeCompressorHeuristicNonGo(t *testing.T) {
	js := `import { foo } from "bar";

export function compute(a, b) {
	const x = a + b;
	const y = x * 2;
	if (y > 10) {
		console.log("big");
	}
	return y;
}

class Widget {
	render() {
		const el = document.createElement("div");
		el.textContent = "hi";
		return el;
	}
}
`
	c := NewCodeCompressor()
	res, err := c.Compress(js, Options{Mode: ModeLossyWithCCR, Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheKey == "" {
		t.Fatalf("non-Go code should still be skeletonized + offloaded: %+v", res)
	}
	if !strings.Contains(res.Compressed, "export function compute") || !strings.Contains(res.Compressed, "class Widget") {
		t.Errorf("declarations should survive:\n%s", res.Compressed)
	}
	if strings.Contains(res.Compressed, "createElement") {
		t.Errorf("deep body lines should be elided:\n%s", res.Compressed)
	}
}

func TestCodeCompressorLosslessWithoutStore(t *testing.T) {
	c := NewCodeCompressor()
	res, _ := c.Compress(sampleGo, Options{Mode: ModeLossyWithCCR, Store: nil})
	if res.Strategy != "passthrough" {
		t.Fatalf("no store => passthrough (skeleton is lossy), got %q", res.Strategy)
	}
}
