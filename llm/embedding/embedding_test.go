/*
 * ChatCLI - Embedding tests.
 */
package embedding

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestCosineSimilarity_IdenticalVectors(t *testing.T) {
	v := []float32{1, 2, 3}
	if got := CosineSimilarity(v, v); math.Abs(float64(got-1)) > 1e-6 {
		t.Errorf("identical vectors must yield 1; got %f", got)
	}
}

func TestCosineSimilarity_OrthogonalVectors(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if got := CosineSimilarity(a, b); math.Abs(float64(got)) > 1e-6 {
		t.Errorf("orthogonal vectors must yield 0; got %f", got)
	}
}

func TestCosineSimilarity_OppositeVectors(t *testing.T) {
	a := []float32{1, 1}
	b := []float32{-1, -1}
	if got := CosineSimilarity(a, b); got > -0.99 {
		t.Errorf("opposite vectors must yield ~-1; got %f", got)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	if got := CosineSimilarity([]float32{1, 0}, []float32{0}); got != 0 {
		t.Errorf("length mismatch must yield 0; got %f", got)
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	if got := CosineSimilarity([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("zero vector must yield 0; got %f", got)
	}
}

func TestCosineSimilarity_NaNOrInfReturnsZero(t *testing.T) {
	a := []float32{float32(math.NaN()), 1}
	b := []float32{1, 1}
	if got := CosineSimilarity(a, b); got != 0 {
		t.Errorf("NaN must yield 0; got %f", got)
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Voyage":  "voyage",
		" OpenAI": "openai",
		"":        "",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNewByName_NullProvider(t *testing.T) {
	p, err := NewByName("")
	if err != nil {
		t.Fatalf("empty name must return null provider; err=%v", err)
	}
	if !IsNull(p) {
		t.Fatalf("expected null provider; got %T", p)
	}
}

func TestNewByName_UnknownErrors(t *testing.T) {
	_, err := NewByName("nonexistent")
	if err == nil || !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown provider must return ErrUnknownProvider; got %v", err)
	}
}

func TestNewByName_VoyageRequiresKey(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	if _, err := NewByName("voyage"); err == nil {
		t.Fatalf("voyage must error without API key")
	}
}

func TestNewByName_OpenAIRequiresKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := NewByName("openai"); err == nil {
		t.Fatalf("openai must error without API key")
	}
}

func TestNullProvider_EmbedErrors(t *testing.T) {
	p := NewNull()
	if _, err := p.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("null provider Embed must error")
	}
	if p.Dimension() != 0 {
		t.Errorf("null provider dim must be 0; got %d", p.Dimension())
	}
	if p.Name() != "null" {
		t.Errorf("null provider name must be 'null'; got %s", p.Name())
	}
}

func TestIsNull(t *testing.T) {
	if !IsNull(nil) {
		t.Error("nil must be treated as null")
	}
	if !IsNull(NewNull()) {
		t.Error("Null{} must be detected")
	}
}
