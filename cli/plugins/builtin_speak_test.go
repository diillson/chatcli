/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSpeak_StatusDisabled(t *testing.T) {
	for _, k := range []string{"CHATCLI_TTS_PROVIDER", "CHATCLI_TTS_CMD", "CHATCLI_TTS_URL", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	// Pin to a backend that needs config we don't provide → null.
	t.Setenv("CHATCLI_TTS_PROVIDER", "openai")
	p := NewBuiltinSpeakPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"status"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no TTS backend") {
		t.Fatalf("expected disabled status, got %q", out)
	}
}

func TestSpeak_MissingText(t *testing.T) {
	p := NewBuiltinSpeakPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"say","args":{}}`}); err == nil {
		t.Fatal("expected error for missing text")
	}
}

func TestSpeak_SayWritesFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX helper script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "tts.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$2\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_TTS_PROVIDER", "command")
	t.Setenv("CHATCLI_TTS_CMD", script+" {text} {output}")
	t.Setenv("CHATCLI_TTS_CMD_EXT", "wav")

	outPath := filepath.Join(dir, "out.wav")
	p := NewBuiltinSpeakPlugin()
	res, err := p.Execute(context.Background(), []string{`{"cmd":"say","args":{"text":"hello audio","out":"` + outPath + `"}}`})
	if err != nil {
		t.Fatalf("say: %v", err)
	}
	if !strings.Contains(res, outPath) {
		t.Fatalf("result should mention output path, got %q", res)
	}
	data, err := os.ReadFile(outPath)
	if err != nil || string(data) != "hello audio" {
		t.Fatalf("output file = %q err=%v", data, err)
	}
}

func TestCanonicalSpeakCmd(t *testing.T) {
	if canonicalSpeakCmd("speak") != "say" || canonicalSpeakCmd("tts") != "say" {
		t.Fatal("say aliases wrong")
	}
	if canonicalSpeakCmd("status") != "status" || canonicalSpeakCmd("zz") != "" {
		t.Fatal("status/unknown wrong")
	}
}

func TestSpeak_ArgvSay(t *testing.T) {
	// argv form folds the remainder into text; with no backend it should fail
	// at synthesis (ErrDisabled), proving parsing reached the say branch.
	t.Setenv("CHATCLI_TTS_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "")
	p := NewBuiltinSpeakPlugin()
	_, err := p.Execute(context.Background(), []string{"say", "hello", "there"})
	if err == nil {
		t.Fatal("expected ErrDisabled with no backend")
	}
}
