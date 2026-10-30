/*
 * ChatCLI - Tests for models that serve implicit caching only
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package googleai

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

// Gemini 4 Argon refuses explicit cache resources: with explicit caching
// on, the client must not try to create one (it would fail and retry after
// every backoff) and must keep the system instruction inline.
func TestExplicitCache_SkippedForImplicitOnlyModel(t *testing.T) {
	t.Setenv(client.PromptCacheExplicitEnv, "true")
	f := &fakeGemini{tokens: 5000}
	c := newExplicitTestClient(t, f)
	c.model = "gemini-4-argon"
	for i := 0; i < 3; i++ {
		if _, err := c.SendPrompt(context.Background(), "hi", bigSystemHistory(""), 100); err != nil {
			t.Fatal(err)
		}
	}
	if f.creates != 0 {
		t.Fatalf("no cache resource may be created for an implicit-only model; creates=%d", f.creates)
	}
	for _, g := range f.generates {
		if _, ok := g["system_instruction"]; !ok {
			t.Fatal("the system instruction must travel inline")
		}
	}
}
