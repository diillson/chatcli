/*
 * ChatCLI - Image generation tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func b64png() string {
	return base64.StdEncoding.EncodeToString([]byte("\x89PNGfake"))
}

func TestOpenAICompatible_Generate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/images/generations") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + b64png() + `"}]}`))
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(srv.URL, "", "dall-e-3", "selfhosted", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := p.Generate(context.Background(), "a fox", Options{Size: "512x512"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(imgs) != 1 || string(imgs[0].Data) != "\x89PNGfake" || imgs[0].Ext != "png" {
		t.Fatalf("unexpected image %+v", imgs)
	}
}

func TestAutomatic1111_Generate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sdapi/v1/txt2img") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"images":["` + b64png() + `"]}`))
	}))
	defer srv.Close()

	p, err := NewAutomatic1111(srv.URL, 20, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := p.Generate(context.Background(), "a fox", Options{Size: "768x768"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(imgs) != 1 || string(imgs[0].Data) != "\x89PNGfake" {
		t.Fatalf("unexpected image %+v", imgs)
	}
}

func TestAutomatic1111_StripsDataURI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"images":["data:image/png;base64,` + b64png() + `"]}`))
	}))
	defer srv.Close()
	p, _ := NewAutomatic1111(srv.URL, 20, zap.NewNop())
	imgs, err := p.Generate(context.Background(), "x", Options{})
	if err != nil || len(imgs) != 1 || string(imgs[0].Data) != "\x89PNGfake" {
		t.Fatalf("data-uri not stripped: %+v err=%v", imgs, err)
	}
}

func TestFactory_Selection(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_URL", "CHATCLI_IMAGE_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	if !IsNull(NewFromEnv(zap.NewNop())) {
		t.Fatal("expected Null with no config")
	}

	t.Setenv("CHATCLI_IMAGE_PROVIDER", "sdwebui")
	if _, ok := NewFromEnv(zap.NewNop()).(*Automatic1111); !ok {
		t.Fatal("expected sdwebui backend")
	}

	t.Setenv("CHATCLI_IMAGE_PROVIDER", "")
	t.Setenv("CHATCLI_IMAGE_URL", "http://localhost:1234/v1")
	if _, ok := NewFromEnv(zap.NewNop()).(*OpenAICompatible); !ok {
		t.Fatal("expected openai-compatible backend from URL")
	}

	t.Setenv("CHATCLI_IMAGE_URL", "")
	t.Setenv("OPENAI_API_KEY", "sk-x")
	if NewFromEnv(zap.NewNop()).Name() != "openai" {
		t.Fatal("expected openai fallback")
	}
}

func TestGoogle_Generate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":predict") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("key") != "gkey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"predictions":[{"bytesBase64Encoded":"` + b64png() + `","mimeType":"image/png"}]}`))
	}))
	defer srv.Close()

	g, err := NewGoogle("gkey", "imagen-3.0-generate-002", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	g.baseURL = srv.URL // redirect to mock
	imgs, err := g.Generate(context.Background(), "a fox", Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(imgs) != 1 || string(imgs[0].Data) != "\x89PNGfake" {
		t.Fatalf("unexpected image %+v", imgs)
	}
}

func TestFactory_GooglePin(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_URL", "OPENAI_API_KEY", "GOOGLEAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "google")
	t.Setenv("GOOGLEAI_API_KEY", "gkey")
	if _, ok := NewFromEnv(zap.NewNop()).(*Google); !ok {
		t.Fatal("expected Google backend")
	}
}

func TestXAI_OmitsSize(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + b64png() + `"}]}`))
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(srv.URL, "xkey", "grok-2-image", "xai", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	p.omitSize = true
	if _, err := p.Generate(context.Background(), "a fox", Options{Size: "1024x1024"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, present := gotBody["size"]; present {
		t.Fatalf("xAI request must not include 'size', got body %v", gotBody)
	}
}

func TestFactory_XAIPin(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_URL", "OPENAI_API_KEY", "GOOGLEAI_API_KEY", "XAI_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "xai")
	t.Setenv("XAI_API_KEY", "xkey")
	p := NewFromEnv(zap.NewNop())
	if p.Name() != "xai" {
		t.Fatalf("expected xai backend, got %s", p.Name())
	}
}

func TestParseSize(t *testing.T) {
	if w, h := parseSize("800x600"); w != 800 || h != 600 {
		t.Fatalf("parseSize 800x600 = %d,%d", w, h)
	}
	if w, h := parseSize("garbage"); w != 1024 || h != 1024 {
		t.Fatalf("parseSize default = %d,%d", w, h)
	}
}
