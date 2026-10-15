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

// isolateImageEnv clears every variable that would make the image commands
// reach a provider, so these tests stay offline.
func isolateImageEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_API", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL",
		"CHATCLI_IMAGE_EDIT_PROVIDER", "CHATCLI_IMAGE_EDIT_MODEL", "OPENAI_API_KEY", "GOOGLEAI_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "XAI_API_KEY", "ZAI_API_KEY", "MINIMAX_API_KEY", "AWS_PROFILE", "BEDROCK_PROFILE",
		"AWS_ACCESS_KEY_ID", "AWS_BEARER_TOKEN_BEDROCK", "AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		t.Setenv(k, "")
	}
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent/aws-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent/aws-credentials")
}

func TestRouteConfigImage_Setters(t *testing.T) {
	isolateImageEnv(t)
	cli := &ChatCLI{logger: zap.NewNop()}

	cli.routeConfigImage(context.Background(), []string{"provider", "sdwebui"})
	if os.Getenv("CHATCLI_IMAGE_PROVIDER") != "sdwebui" {
		t.Fatalf("provider not set: %q", os.Getenv("CHATCLI_IMAGE_PROVIDER"))
	}
	cli.routeConfigImage(context.Background(), []string{"api", "responses"})
	if os.Getenv("CHATCLI_IMAGE_API") != "responses" {
		t.Fatal("api not set")
	}
	// A model alone also selects the backend that serves it.
	t.Setenv("OPENAI_API_KEY", "k")
	cli.routeConfigImage(context.Background(), []string{"model", "gpt-5.5"})
	if os.Getenv("CHATCLI_IMAGE_MODEL") != "gpt-5.5" || os.Getenv("CHATCLI_IMAGE_PROVIDER") != "responses" || os.Getenv("CHATCLI_IMAGE_API") != "" {
		t.Fatalf("model/provider not set: model=%q provider=%q api=%q",
			os.Getenv("CHATCLI_IMAGE_MODEL"), os.Getenv("CHATCLI_IMAGE_PROVIDER"), os.Getenv("CHATCLI_IMAGE_API"))
	}
	cli.routeConfigImage(context.Background(), []string{"edit", "gpt-image-2.5-flare"})
	if os.Getenv("CHATCLI_IMAGE_EDIT_PROVIDER") != "openai" || os.Getenv("CHATCLI_IMAGE_EDIT_MODEL") != "gpt-image-2.5-flare" {
		t.Fatalf("edit pin not set: %q %q", os.Getenv("CHATCLI_IMAGE_EDIT_PROVIDER"), os.Getenv("CHATCLI_IMAGE_EDIT_MODEL"))
	}
	// A provider without its credential is refused and changes nothing.
	cli.routeConfigImage(context.Background(), []string{"model", "grok-imagine-image-2.0"})
	if os.Getenv("CHATCLI_IMAGE_MODEL") != "gpt-5.5" {
		t.Fatal("a model of an unconfigured provider must not be selected")
	}
	cli.routeConfigImage(context.Background(), []string{"url", "http://localhost:7860"})
	if os.Getenv("CHATCLI_IMAGE_URL") != "http://localhost:7860" {
		t.Fatal("url not set")
	}

	cli.routeConfigImage(context.Background(), []string{"reset"})
	if os.Getenv("CHATCLI_IMAGE_PROVIDER") != "" || os.Getenv("CHATCLI_IMAGE_MODEL") != "" || os.Getenv("CHATCLI_IMAGE_EDIT_PROVIDER") != "" {
		t.Fatal("reset did not clear overrides")
	}

	// Smoke: these must not panic.
	cli.routeConfigImage(context.Background(), nil)
	cli.routeConfigImage(context.Background(), []string{"models"})
	cli.routeConfigImage(context.Background(), []string{"bogus-sub"})
	cli.routeConfigImage(context.Background(), []string{"provider"}) // missing value
}

func TestImageModelsCatalog_Content(t *testing.T) {
	isolateImageEnv(t) // no credentials: catalog only, no live listing
	cli := &ChatCLI{logger: zap.NewNop()}
	out := cli.imageModelsCatalog(context.Background())
	for _, want := range []string{"gpt-image-2.5-flare", "gpt-5.5", "grok-imagine-image", "gemini-nano-banana-2.1", "stability.sd3-5-large-v1:0", "us-west-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog missing %q", want)
		}
	}
	if strings.Contains(out, "nova-canvas") {
		t.Error("Nova Canvas reached its end of life and must not be listed")
	}
}
