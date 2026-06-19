/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestRouteConfigImage_Setters(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_API", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL"} {
		t.Setenv(k, "")
	}
	cli := &ChatCLI{logger: zap.NewNop()}

	cli.routeConfigImage(context.Background(), []string{"provider", "sdwebui"})
	if os.Getenv("CHATCLI_IMAGE_PROVIDER") != "sdwebui" {
		t.Fatalf("provider not set: %q", os.Getenv("CHATCLI_IMAGE_PROVIDER"))
	}
	cli.routeConfigImage(context.Background(), []string{"api", "responses"})
	if os.Getenv("CHATCLI_IMAGE_API") != "responses" {
		t.Fatal("api not set")
	}
	cli.routeConfigImage(context.Background(), []string{"model", "gpt-5.5"})
	if os.Getenv("CHATCLI_IMAGE_MODEL") != "gpt-5.5" {
		t.Fatal("model not set")
	}
	cli.routeConfigImage(context.Background(), []string{"url", "http://localhost:7860"})
	if os.Getenv("CHATCLI_IMAGE_URL") != "http://localhost:7860" {
		t.Fatal("url not set")
	}

	cli.routeConfigImage(context.Background(), []string{"reset"})
	if os.Getenv("CHATCLI_IMAGE_PROVIDER") != "" || os.Getenv("CHATCLI_IMAGE_MODEL") != "" {
		t.Fatal("reset did not clear overrides")
	}

	// Smoke: these must not panic.
	cli.routeConfigImage(context.Background(), nil)
	cli.routeConfigImage(context.Background(), []string{"models"})
	cli.routeConfigImage(context.Background(), []string{"bogus-sub"})
	cli.routeConfigImage(context.Background(), []string{"provider"}) // missing value
}

func TestImageModelsCatalog_Content(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "") // skip live GET
	cli := &ChatCLI{logger: zap.NewNop()}
	out := cli.imageModelsCatalog(context.Background())
	for _, want := range []string{"gpt-image-1", "gpt-5.5", "grok-imagine-image", "nova-canvas"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog missing %q", want)
		}
	}
}
