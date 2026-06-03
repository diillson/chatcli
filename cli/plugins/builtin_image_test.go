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
