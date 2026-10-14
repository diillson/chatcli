/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImage_StatusDisabled(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_URL", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	p := NewBuiltinImagePlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"status"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no image backend") {
		t.Fatalf("expected disabled status, got %q", out)
	}
}

func TestImage_MissingPrompt(t *testing.T) {
	p := NewBuiltinImagePlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"gen","args":{}}`}); err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func TestImage_GenWritesFile(t *testing.T) {
	// Stand up a fake SD WebUI and point the tool at it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// base64 of "PNGDATA"
		_, _ = w.Write([]byte(`{"images":["UE5HREFUQQ=="]}`))
	}))
	defer srv.Close()

	t.Setenv("CHATCLI_IMAGE_PROVIDER", "sdwebui")
	t.Setenv("CHATCLI_IMAGE_URL", srv.URL)

	dir := t.TempDir()
	outPath := filepath.Join(dir, "pic.png")
	p := NewBuiltinImagePlugin()
	res, err := p.Execute(context.Background(), []string{`{"cmd":"gen","args":{"prompt":"a cat","out":"` + outPath + `"}}`})
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if !strings.Contains(res, outPath) {
		t.Fatalf("result should mention path, got %q", res)
	}
	data, err := os.ReadFile(outPath)
	if err != nil || string(data) != "PNGDATA" {
		t.Fatalf("output = %q err=%v", data, err)
	}
}

func TestCanonicalImageCmd(t *testing.T) {
	for _, in := range []string{"gen", "generate", "create", "draw"} {
		if canonicalImageCmd(in) != "gen" {
			t.Errorf("%q != gen", in)
		}
	}
	if canonicalImageCmd("status") != "status" || canonicalImageCmd("zz") != "" {
		t.Fatal("status/unknown wrong")
	}
}

func TestImage_StatusEnabled(t *testing.T) {
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "sdwebui")
	t.Setenv("CHATCLI_IMAGE_URL", "http://localhost:7860")
	p := NewBuiltinImagePlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"status"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sdwebui") {
		t.Fatalf("status should name backend, got %q", out)
	}
}

func TestImage_GenMultipleToDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"images":["UE5HMQ==","UE5HMg=="]}`)) // PNG1, PNG2
	}))
	defer srv.Close()
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "sdwebui")
	t.Setenv("CHATCLI_IMAGE_URL", srv.URL)

	dir := t.TempDir()
	p := NewBuiltinImagePlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"gen","args":{"prompt":"two","n":2,"out":"` + dir + `"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 image") {
		t.Logf("note: i18n key fallback in tests; out=%q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "image-1.png")); err != nil {
		t.Fatalf("image-1 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "image-2.png")); err != nil {
		t.Fatalf("image-2 missing: %v", err)
	}
}

func TestImage_ArgvGenNoBackend(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_PROVIDER", "CHATCLI_IMAGE_URL", "OPENAI_API_KEY", "GOOGLEAI_API_KEY", "GEMINI_API_KEY", "XAI_API_KEY"} {
		t.Setenv(k, "")
	}
	p := NewBuiltinImagePlugin()
	if _, err := p.Execute(context.Background(), []string{"gen", "a", "cat"}); err == nil {
		t.Fatal("expected ErrDisabled with no backend")
	}
}

func TestImage_EmptyAndUnknown(t *testing.T) {
	p := NewBuiltinImagePlugin()
	if _, err := p.Execute(context.Background(), nil); err == nil {
		t.Fatal("empty args should error")
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"explode"}`}); err == nil {
		t.Fatal("unknown cmd should error")
	}
}

func TestImage_SchemaAndMeta(t *testing.T) {
	p := NewBuiltinImagePlugin()
	if p.Name() != "@image" || p.Version() == "" || p.Path() != "" {
		t.Fatal("meta wrong")
	}
	if !strings.Contains(p.Schema(), "gen") || !strings.Contains(p.Usage(), "gen") {
		t.Fatal("schema/usage missing gen")
	}
	if p.Description() == "" {
		t.Fatal("empty description")
	}
}

// An edit on a backend that can't edit fails with a message naming it, and
// never reaches another account whose key happens to be set (the edit used
// to fall through to OpenAI and die there on a 429).
func TestImage_EditOnGenerateOnlyBackendFailsClearly(t *testing.T) {
	for _, k := range []string{"CHATCLI_IMAGE_URL", "CHATCLI_IMAGE_MODEL", "CHATCLI_IMAGE_API", "CHATCLI_IMAGE_EDIT_PROVIDER", "CHATCLI_IMAGE_EDIT_MODEL"} {
		t.Setenv(k, "")
	}
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "zai")
	t.Setenv("ZAI_API_KEY", "zai-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	img := filepath.Join(t.TempDir(), "in.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\n0000000000000000"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewBuiltinImagePlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"edit","args":{"prompt":"winter","image":"` + filepath.ToSlash(img) + `"}}`})
	if err == nil {
		t.Fatal("an edit on Z.AI must fail")
	}
	if !strings.Contains(err.Error(), "image.edit.unsupported") && !strings.Contains(err.Error(), "does not edit images") {
		t.Fatalf("error should be the localized edit-unsupported message, got %q", err)
	}
	if !strings.Contains(err.Error(), "zai") {
		t.Fatalf("error should name the active backend, got %q", err)
	}
}

func TestImage_StatusSaysWhetherTheBackendEdits(t *testing.T) {
	t.Setenv("CHATCLI_IMAGE_URL", "")
	t.Setenv("CHATCLI_IMAGE_MODEL", "")
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "zai")
	t.Setenv("ZAI_API_KEY", "zai-key")
	out, err := NewBuiltinImagePlugin().Execute(context.Background(), []string{`{"cmd":"status"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "zai") || !strings.Contains(out, "generates only") {
		t.Fatalf("status = %q, want zai reported as generate-only", out)
	}
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "xai")
	t.Setenv("XAI_API_KEY", "xai-key")
	out, _ = NewBuiltinImagePlugin().Execute(context.Background(), []string{`{"cmd":"status"}`})
	if !strings.Contains(out, "xai") || !strings.Contains(out, "generates and edits") {
		t.Fatalf("status = %q, want xai reported as editing", out)
	}
}
