package imagegen

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// A model configured through the environment after its shutdown day is
// refused by the factory with the date and the replacement, for generation
// and for editing, without building the provider's backend.
func TestFactory_RefusesRetiredModelFromEnv(t *testing.T) {
	isolate(t)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("CHATCLI_IMAGE_MODEL", "gpt-image-1")
	atDate(t, "2026-10-23")

	p := NewFromEnv(zap.NewNop())
	if p.Name() != "openai" || ModelOf(p) != "gpt-image-1" {
		t.Fatalf("retired backend should still name openai/gpt-image-1, got %s/%s", p.Name(), ModelOf(p))
	}
	_, err := p.Generate(context.Background(), "a fox", Options{})
	if err == nil || !strings.Contains(err.Error(), "2026-10-23") || !strings.Contains(err.Error(), "gpt-image-2.5") {
		t.Fatalf("generate must name the shutdown date and the replacement, got %v", err)
	}
	ed, ok := AsEditor(p)
	if !ok {
		t.Fatal("a retired model must answer @image edit with its shutdown, not with 'does not edit'")
	}
	if _, err := ed.Edit(context.Background(), "x", []Image{{Data: []byte("p")}}, EditOptions{}); err == nil || !strings.Contains(err.Error(), "2026-10-23") {
		t.Fatalf("edit must name the shutdown date, got %v", err)
	}
}

// The Bedrock Nova Canvas id no longer reaches AWS: the factory refuses it
// before any credential lookup.
func TestFactory_RefusesRetiredBedrockModel(t *testing.T) {
	isolate(t)
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "bedrock")
	t.Setenv("CHATCLI_IMAGE_MODEL", "amazon.nova-canvas-v1:0")
	atDate(t, "2026-11-03")

	p := NewFromEnv(zap.NewNop())
	if _, isBedrock := p.(*Bedrock); isBedrock {
		t.Fatal("a retired Bedrock model must not build the Bedrock backend")
	}
	if _, err := p.Generate(context.Background(), "a fox", Options{}); err == nil || !strings.Contains(err.Error(), "2026-09-30") {
		t.Fatalf("want the 2026-09-30 shutdown, got %v", err)
	}
}

// The day before the shutdown the model still runs on its real backend, and
// a pinned edit provider follows the same rule.
func TestFactory_RetiredGuardOnlyFromShutdownDay(t *testing.T) {
	isolate(t)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("CHATCLI_IMAGE_MODEL", "gpt-image-1")
	atDate(t, "2026-10-22")
	if _, ok := NewFromEnv(zap.NewNop()).(*retiredModel); ok {
		t.Fatal("gpt-image-1 is still in service on 2026-10-22")
	}

	atDate(t, "2026-10-23")
	if _, ok := buildEditProvider(context.Background(), "openai", "gpt-image-1", zap.NewNop()).(*retiredModel); !ok {
		t.Fatal("a retired edit model must be refused too")
	}
	if _, ok := buildEditProvider(context.Background(), "openai", "", zap.NewNop()).(*retiredModel); ok {
		t.Fatal("an empty edit model falls back to the provider default, not to a retired entry")
	}
	if retiredOrNil("some-model-the-catalog-does-not-know") != nil {
		t.Fatal("an unknown id is passed through, not refused")
	}
}
