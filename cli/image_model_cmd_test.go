/*
 * ChatCLI - /model-image shortcut tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	prompt "github.com/c-bata/go-prompt"
	"go.uber.org/zap"
)

func newTestCLI() *ChatCLI { return &ChatCLI{logger: zap.NewNop()} }

// getImageModelSuggestions lists the image-model catalog (not the text models).
func TestImageModelSuggestionsListCatalog(t *testing.T) {
	cli := newTestCLI()
	b := prompt.NewBuffer()
	b.InsertText("/model-image ", false, true)
	sugg := cli.getImageModelSuggestions(*b.Document())
	if len(sugg) == 0 {
		t.Fatal("expected image-model suggestions")
	}
	var foundZAI, foundMiniMax bool
	for _, s := range sugg {
		switch s.Text {
		case "glm-image", "cogview-4-250304":
			foundZAI = true
		case "image-01":
			foundMiniMax = true
		}
	}
	if !foundZAI || !foundMiniMax {
		t.Errorf("catalog missing new providers: zai=%v minimax=%v", foundZAI, foundMiniMax)
	}

	// Prefix filtering: typing part of an id narrows the list.
	b2 := prompt.NewBuffer()
	b2.InsertText("/model-image cogview", false, true)
	narrowed := cli.getImageModelSuggestions(*b2.Document())
	if len(narrowed) == 0 {
		t.Fatal("expected cogview matches")
	}
	for _, s := range narrowed {
		if !strings.HasPrefix(s.Text, "cogview") {
			t.Errorf("prefix filter leaked %q", s.Text)
		}
	}
}

// handleImageModelCommand with an id sets CHATCLI_IMAGE_MODEL at runtime.
func TestHandleImageModelCommandSetsEnv(t *testing.T) {
	t.Setenv("CHATCLI_IMAGE_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "") // keep imageModelsCatalog from hitting the network
	cli := newTestCLI()

	cli.handleImageModelCommand(context.Background(), "/model-image image-01")
	if got := os.Getenv("CHATCLI_IMAGE_MODEL"); got != "image-01" {
		t.Fatalf("CHATCLI_IMAGE_MODEL = %q, want image-01", got)
	}

	// Bare form prints the catalog and must not mutate the model.
	cli.handleImageModelCommand(context.Background(), "/model-image")
	if got := os.Getenv("CHATCLI_IMAGE_MODEL"); got != "image-01" {
		t.Fatalf("bare form changed the model to %q", got)
	}
}
