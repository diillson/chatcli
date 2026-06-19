/*
 * ChatCLI - MiniMax + Z.AI image backend tests.
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
)

// 1x1 transparent PNG, base64 — a valid decodable image payload for tests.
const tinyPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

func TestMiniMaxGenerate(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/image_generation") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer k" {
			t.Errorf("auth = %q", auth)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":      map[string]interface{}{"image_base64": []string{tinyPNGB64}},
			"base_resp": map[string]interface{}{"status_code": 0, "status_msg": "success"},
		})
	}))
	defer srv.Close()

	m, err := NewMiniMax(srv.URL, "k", "image-01", nil)
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := m.Generate(context.Background(), "a cat", Options{Size: "1920x1080"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(imgs) != 1 || imgs[0].Ext != "png" {
		t.Fatalf("imgs = %+v", imgs)
	}
	if gotBody["model"] != "image-01" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if gotBody["aspect_ratio"] != "16:9" {
		t.Errorf("aspect_ratio = %v (want 16:9 for 1920x1080)", gotBody["aspect_ratio"])
	}
	if _, hasSize := gotBody["size"]; hasSize {
		t.Errorf("MiniMax payload must not carry a pixel 'size' field")
	}
}

func TestMiniMaxBaseRespError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"base_resp": map[string]interface{}{"status_code": 1008, "status_msg": "insufficient balance"},
		})
	}))
	defer srv.Close()

	m, _ := NewMiniMax(srv.URL, "k", "", nil)
	_, err := m.Generate(context.Background(), "x", Options{})
	if err == nil || !strings.Contains(err.Error(), "insufficient balance") {
		t.Fatalf("expected base_resp error, got %v", err)
	}
}

func TestMiniMaxRequiresKey(t *testing.T) {
	if _, err := NewMiniMax("", "", "", nil); err == nil {
		t.Fatal("MiniMax should require an API key")
	}
}

func TestMiniMaxEmptyPrompt(t *testing.T) {
	m, _ := NewMiniMax("http://localhost:1", "k", "", nil)
	if _, err := m.Generate(context.Background(), "   ", Options{}); err == nil {
		t.Fatal("empty prompt should error")
	}
}

func TestMiniMaxHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream boom"))
	}))
	defer srv.Close()
	m, _ := NewMiniMax(srv.URL, "k", "", nil)
	_, err := m.Generate(context.Background(), "x", Options{})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected HTTP 500 error, got %v", err)
	}
}

func TestMiniMaxNoDecodableImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// status_code 0 but a non-base64 payload -> no decodable images.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":      map[string]interface{}{"image_base64": []string{"!!!not-base64!!!"}},
			"base_resp": map[string]interface{}{"status_code": 0},
		})
	}))
	defer srv.Close()
	m, _ := NewMiniMax(srv.URL, "k", "", nil)
	if _, err := m.Generate(context.Background(), "x", Options{}); err == nil {
		t.Fatal("undecodable payload should error")
	}
}

func TestMiniMaxAspect(t *testing.T) {
	cases := []struct {
		w, h int
		want string
	}{
		{1024, 1024, "1:1"},
		{0, 0, "1:1"},
		{2560, 1080, "21:9"},
		{1920, 1080, "16:9"},
		{1500, 1000, "3:2"},
		{1024, 768, "4:3"},
		{1080, 1920, "9:16"},
		{1000, 1500, "2:3"},
		{768, 1024, "3:4"},
		{1000, 1100, "1:1"}, // near-square, no specific ratio
	}
	for _, c := range cases {
		if got := minimaxAspect(c.w, c.h); got != c.want {
			t.Errorf("minimaxAspect(%d,%d) = %q, want %q", c.w, c.h, got, c.want)
		}
	}
}

// Z.AI CogView/GLM-Image: OpenAI-shaped, returns a URL, rejects "n".
func TestZAIViaOpenAICompatibleURLAndOmitN(t *testing.T) {
	raw, _ := base64.StdEncoding.DecodeString(tinyPNGB64)
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(raw)
	}))
	defer imgSrv.Close()

	var gotBody map[string]interface{}
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/images/generations") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"url": imgSrv.URL + "/x.png"}},
		})
	}))
	defer apiSrv.Close()

	p, err := NewOpenAICompatible(apiSrv.URL, "zk", "glm-image", "zai", nil)
	if err != nil {
		t.Fatal(err)
	}
	p.omitN = true
	imgs, err := p.Generate(context.Background(), "a dog", Options{Size: "1024x1024"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("want 1 image, got %d", len(imgs))
	}
	if _, hasN := gotBody["n"]; hasN {
		t.Errorf("omitN provider must not send 'n'")
	}
	if gotBody["model"] != "glm-image" {
		t.Errorf("model = %v", gotBody["model"])
	}
}

func TestProviderFromModel(t *testing.T) {
	cases := map[string]string{
		"glm-image":                        "zai",
		"cogview-4-250304":                 "zai",
		"cogview-4-250999":                 "zai", // future snapshot, heuristic
		"image-01":                         "minimax",
		"gpt-image-1":                      "openai",
		"gpt-5.5":                          "openai-responses",
		"grok-imagine-image":               "xai",
		"imagen-4.0-generate-001":          "google",
		"gemini-2.5-flash-image":           "google",
		"stability.stable-image-core-v1:1": "bedrock",
		"amazon.nova-canvas-v1:0":          "bedrock",
		"":                                 "",
		"some-unknown-model":               "",
	}
	for model, want := range cases {
		if got := providerFromModel(model); got != want {
			t.Errorf("providerFromModel(%q) = %q, want %q", model, got, want)
		}
	}
}

// auto mode (no CHATCLI_IMAGE_PROVIDER): the model alone picks the backend.
func TestFactoryAutoInfersProviderFromModel(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL", "CHATCLI_IMAGE_API", "OPENAI_API_KEY", "GOOGLEAI_API_KEY", "GEMINI_API_KEY", "XAI_API_KEY", "ZAI_API_KEY", "MINIMAX_API_KEY"} {
		t.Setenv(k, "")
	}
	// Provider stays unset (auto). Only ZAI_API_KEY present, model = glm-image.
	t.Setenv("ZAI_API_KEY", "zk")
	t.Setenv("CHATCLI_IMAGE_MODEL", "glm-image")
	if p := NewFromEnv(nil); p.Name() != "zai" {
		t.Errorf("auto+glm-image: got %q, want zai", p.Name())
	}

	// Model = image-01 but only MINIMAX key -> minimax (not the present ZAI key path).
	t.Setenv("MINIMAX_API_KEY", "mk")
	t.Setenv("CHATCLI_IMAGE_MODEL", "image-01")
	if p := NewFromEnv(nil); p.Name() != "minimax" {
		t.Errorf("auto+image-01: got %q, want minimax", p.Name())
	}

	// Recognized model whose key is absent -> Null, does NOT silently use the
	// other present keys.
	t.Setenv("CHATCLI_IMAGE_MODEL", "image-01")
	t.Setenv("MINIMAX_API_KEY", "")
	if p := NewFromEnv(nil); !IsNull(p) {
		t.Errorf("auto+image-01 without MINIMAX key should be Null, got %q", p.Name())
	}
}

// Exercises the remaining auto-inference branches so a chosen model routes to
// the matching cloud backend purely from its id.
func TestFactoryAutoRoutesEachProvider(t *testing.T) {
	clear := func() {
		for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL", "CHATCLI_IMAGE_API", "OPENAI_API_KEY", "GOOGLEAI_API_KEY", "GEMINI_API_KEY", "XAI_API_KEY", "ZAI_API_KEY", "MINIMAX_API_KEY"} {
			t.Setenv(k, "")
		}
	}
	cases := []struct {
		model, keyEnv, keyVal, wantNamePart string
	}{
		{"gpt-image-1", "OPENAI_API_KEY", "ok", "openai"},
		{"gpt-5.5", "OPENAI_API_KEY", "ok", ""}, // responses backend; just must not be Null
		{"imagen-4.0-generate-001", "GOOGLEAI_API_KEY", "gk", "google"},
		{"grok-imagine-image", "XAI_API_KEY", "xk", "xai"},
	}
	for _, c := range cases {
		clear()
		t.Setenv("CHATCLI_IMAGE_MODEL", c.model)
		t.Setenv(c.keyEnv, c.keyVal)
		p := NewFromEnv(nil)
		if IsNull(p) {
			t.Errorf("model %s with %s set should route to a backend, got Null", c.model, c.keyEnv)
			continue
		}
		if c.wantNamePart != "" && !strings.Contains(p.Name(), c.wantNamePart) {
			t.Errorf("model %s routed to %q, want name containing %q", c.model, p.Name(), c.wantNamePart)
		}
	}
}

func TestFactoryZAIAndMiniMaxSelection(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_URL", "OPENAI_API_KEY", "ZAI_API_KEY", "MINIMAX_API_KEY"} {
		t.Setenv(k, "")
	}

	t.Setenv("CHATCLI_IMAGE_PROVIDER", "zai")
	t.Setenv("ZAI_API_KEY", "zk")
	if p := NewFromEnv(nil); p.Name() != "zai" {
		t.Errorf("zai provider: got %q", p.Name())
	}

	t.Setenv("CHATCLI_IMAGE_PROVIDER", "minimax")
	t.Setenv("MINIMAX_API_KEY", "mk")
	if p := NewFromEnv(nil); p.Name() != "minimax" {
		t.Errorf("minimax provider: got %q", p.Name())
	}

	// Pinned but no key -> degrades to Null, never silently switches.
	t.Setenv("MINIMAX_API_KEY", "")
	if p := NewFromEnv(nil); !IsNull(p) {
		t.Errorf("minimax without key should be Null, got %q", p.Name())
	}
}
