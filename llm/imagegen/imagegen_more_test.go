/*
 * ChatCLI - Image generation abstraction tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package imagegen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestNullProvider(t *testing.T) {
	n := NewNull()
	if n.Name() != "null" {
		t.Fatalf("name = %q", n.Name())
	}
	if !IsNull(n) || !IsNull(nil) {
		t.Fatal("IsNull should be true for Null and nil")
	}
	_, err := n.Generate(context.Background(), "x", Options{})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestEmptyPromptErrors(t *testing.T) {
	oc, _ := NewOpenAICompatible("http://localhost:1", "", "m", "x", nil)
	if _, err := oc.Generate(context.Background(), "  ", Options{}); err == nil {
		t.Fatal("openai: empty prompt should error")
	}
	sd, _ := NewAutomatic1111("http://localhost:1", 10, nil)
	if _, err := sd.Generate(context.Background(), "", Options{}); err == nil {
		t.Fatal("sdwebui: empty prompt should error")
	}
	g, _ := NewGoogle("k", "", nil)
	if _, err := g.Generate(context.Background(), "", Options{}); err == nil {
		t.Fatal("google: empty prompt should error")
	}
}

func TestConstructorValidation(t *testing.T) {
	if _, err := NewOpenAICompatible("", "", "", "", nil); err == nil {
		t.Fatal("empty baseURL should error")
	}
	if _, err := NewOpenAICompatible("ftp://x", "", "", "", nil); err == nil {
		t.Fatal("non-http baseURL should error")
	}
	if _, err := NewGoogle("", "", nil); err == nil {
		t.Fatal("empty key should error")
	}
	if _, err := NewAutomatic1111("ftp://x", 0, nil); err == nil {
		t.Fatal("non-http baseURL should error")
	}
}

func TestOpenAIResponses_Generate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"output":[{"type":"reasoning"},{"type":"image_generation_call","result":"UE5HREFUQQ=="}]}`))
	}))
	defer srv.Close()
	p, err := NewOpenAIResponses(srv.URL, "k", "gpt-5.5", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := p.Generate(context.Background(), "a fox", Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(imgs) != 1 || string(imgs[0].Data) != "PNGDATA" {
		t.Fatalf("unexpected image %+v", imgs)
	}
	if p.Name() != "openai-responses" {
		t.Fatalf("name = %q", p.Name())
	}
}

func TestKnownModels(t *testing.T) {
	models := KnownModels()
	if len(models) == 0 {
		t.Fatal("empty catalog")
	}
	var hasGptImage, hasResponses bool
	for _, m := range models {
		if m.Name == "gpt-image-1" && m.API == "images" {
			hasGptImage = true
		}
		if m.API == "responses" {
			hasResponses = true
		}
	}
	if !hasGptImage || !hasResponses {
		t.Fatalf("catalog missing key entries: %+v", models)
	}
}

func TestIsImageCapableID(t *testing.T) {
	for _, id := range []string{"gpt-image-1", "dall-e-3", "gpt-5.5", "gpt-4.1", "gpt-4o"} {
		if !isImageCapableID(id) {
			t.Errorf("%q should be image-capable", id)
		}
	}
	if isImageCapableID("text-embedding-3-small") {
		t.Error("embedding model should not be image-capable")
	}
}

func TestFactory_ResponsesSelection(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_URL", "CHATCLI_IMAGE_API", "OPENAI_API_KEY", "GOOGLEAI_API_KEY", "XAI_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "responses")
	t.Setenv("OPENAI_API_KEY", "sk-x")
	if _, ok := NewFromEnv(zap.NewNop()).(*OpenAIResponses); !ok {
		t.Fatal("expected Responses backend for provider=responses")
	}

	// provider=openai + API=responses also routes to Responses
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "openai")
	t.Setenv("CHATCLI_IMAGE_API", "responses")
	if _, ok := NewFromEnv(zap.NewNop()).(*OpenAIResponses); !ok {
		t.Fatal("expected Responses backend for openai + API=responses")
	}
}

func TestParseBedrockImages(t *testing.T) {
	imgs, err := parseBedrockImages([]byte(`{"images":["UE5HREFUQQ=="]}`)) // PNGDATA
	if err != nil || len(imgs) != 1 || string(imgs[0].Data) != "PNGDATA" {
		t.Fatalf("nova/titan parse: %v err=%v", imgs, err)
	}
	if _, err := parseBedrockImages([]byte(`{"error":"content filtered"}`)); err == nil {
		t.Fatal("expected error when bedrock returns error field")
	}
	if _, err := parseBedrockImages([]byte(`{"images":[]}`)); err == nil {
		t.Fatal("expected error for no images")
	}
}

func TestAspectRatio(t *testing.T) {
	cases := map[[2]int]string{
		{1024, 1024}: "1:1",
		{1920, 1080}: "16:9",
		{1080, 1920}: "9:16",
		{0, 0}:       "1:1",
	}
	for wh, want := range cases {
		if got := aspectRatio(wh[0], wh[1]); got != want {
			t.Errorf("aspectRatio(%d,%d)=%q want %q", wh[0], wh[1], got, want)
		}
	}
}

func TestKnownModels_NewIDs(t *testing.T) {
	want := map[string]bool{"gpt-image-2": false, "gpt-image-1-mini": false, "grok-imagine-image-quality": false, "amazon.nova-canvas-v1:0": false, "stability.sd3-5-large-v1:0": false}
	for _, m := range KnownModels() {
		if _, ok := want[m.Name]; ok {
			want[m.Name] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("catalog missing %q", id)
		}
	}
}
