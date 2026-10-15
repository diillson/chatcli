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
	if !hasMsg(out, "image.status.none", "no image backend") {
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
	if !strings.Contains(out, "zai") || !hasMsg(out, "image.status.gen_only", "generates only") {
		t.Fatalf("status = %q, want zai reported as generate-only", out)
	}
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "xai")
	t.Setenv("XAI_API_KEY", "xai-key")
	out, _ = NewBuiltinImagePlugin().Execute(context.Background(), []string{`{"cmd":"status"}`})
	if !strings.Contains(out, "xai") || !hasMsg(out, "image.status.gen_edit", "generates and edits") {
		t.Fatalf("status = %q, want xai reported as editing", out)
	}
}

// hasMsg matches a localized message whether or not the test binary loaded
// the catalogs (without them i18n.T returns the key).
func hasMsg(out, key, english string) bool {
	return strings.Contains(out, key) || strings.Contains(out, english)
}

func isolateImageCreds(t *testing.T) {
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

// The AI switches the image backend by asking for a model; the next call
// builds that backend.
func TestImage_UseSwitchesBackendAndModel(t *testing.T) {
	isolateImageCreds(t)
	t.Setenv("XAI_API_KEY", "xk")
	t.Setenv("ZAI_API_KEY", "zk")
	p := NewBuiltinImagePlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"use","args":{"model":"grok-imagine-image"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CHATCLI_IMAGE_PROVIDER") != "xai" || os.Getenv("CHATCLI_IMAGE_MODEL") != "grok-imagine-image" {
		t.Fatalf("env = %q/%q", os.Getenv("CHATCLI_IMAGE_PROVIDER"), os.Getenv("CHATCLI_IMAGE_MODEL"))
	}
	if !strings.Contains(out, "grok-imagine-image") {
		t.Fatalf("confirmation = %q", out)
	}
	status, _ := p.Execute(context.Background(), []string{`{"cmd":"status"}`})
	if !strings.Contains(status, "xai") || !strings.Contains(status, "grok-imagine-image") {
		t.Fatalf("status after use = %q", status)
	}

	// Provider only → its default; for=edit pins the edit backend.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"use","args":{"provider":"zai"}}`}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CHATCLI_IMAGE_MODEL") != "glm-image" {
		t.Fatalf("default model = %q", os.Getenv("CHATCLI_IMAGE_MODEL"))
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"use","args":{"provider":"xai","for":"edit"}}`}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CHATCLI_IMAGE_EDIT_PROVIDER") != "xai" || os.Getenv("CHATCLI_IMAGE_PROVIDER") != "zai" {
		t.Fatal("for=edit must pin the edit backend and keep the generation backend")
	}

	// A provider without credentials is refused and nothing changes.
	_, err = p.Execute(context.Background(), []string{`{"cmd":"use","args":{"model":"gemini-nano-banana-2.1"}}`})
	if err == nil || !strings.Contains(err.Error(), "GOOGLEAI_API_KEY") {
		t.Fatalf("err = %v, want the missing credential named", err)
	}
	if os.Getenv("CHATCLI_IMAGE_PROVIDER") != "zai" {
		t.Fatal("a refused switch must not change the backend")
	}
}

func TestImage_ModelsListsPerBackendOffline(t *testing.T) {
	isolateImageCreds(t)
	t.Setenv("ZAI_API_KEY", "zk")
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "zai")
	out, err := NewBuiltinImagePlugin().Execute(context.Background(), []string{`{"cmd":"models","args":{"live":false}}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"glm-image", "gpt-image-2.5-flare", "gemini-nano-banana-2.1", "stability.stable-image-inpaint-v1:0", "us-west-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("models output lacks %q", want)
		}
	}
	only, err := NewBuiltinImagePlugin().Execute(context.Background(), []string{`{"cmd":"models","args":{"provider":"bedrock","live":false}}`})
	if err != nil || strings.Contains(only, "glm-image") || !strings.Contains(only, "stability.") {
		t.Fatalf("provider filter: %v\n%s", err, only)
	}
	if _, err := NewBuiltinImagePlugin().Execute(context.Background(), []string{`{"cmd":"models","args":{"provider":"nope"}}`}); err == nil {
		t.Fatal("unknown provider must fail")
	}
}

func TestImage_EditFailsOnUnreadableMask(t *testing.T) {
	isolateImageCreds(t)
	t.Setenv("OPENAI_API_KEY", "ok")
	t.Setenv("CHATCLI_IMAGE_PROVIDER", "openai")
	img := filepath.Join(t.TempDir(), "in.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\n0000000000000000"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewBuiltinImagePlugin().Execute(context.Background(), []string{`{"cmd":"edit","args":{"prompt":"x","image":"` + filepath.ToSlash(img) + `","mask":"/nonexistent/mask.png"}}`})
	if err == nil || !strings.Contains(err.Error(), "mask") {
		t.Fatalf("err = %v, want the unreadable mask reported", err)
	}
}
