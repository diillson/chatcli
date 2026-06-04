/*
 * ChatCLI - Local command TTS tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewCommandSynthesizer_Validation(t *testing.T) {
	if _, err := NewCommandSynthesizer("", "wav", "x"); err == nil {
		t.Fatal("empty template should error")
	}
	if _, err := NewCommandSynthesizer("say {text}", "wav", "x"); err == nil {
		t.Fatal("template without {output} should error")
	}
	if _, err := NewCommandSynthesizer("say {output}", "wav", "x"); err == nil {
		t.Fatal("template without {text} should error")
	}
	p, err := NewCommandSynthesizer("say {text} -o {output}", "aiff", "local")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "local:say" {
		t.Fatalf("Name = %q", p.Name())
	}
}

// TestCommandSynthesize_RealRun runs an actual command that writes {text} to
// {output}, proving the substitution + file read path. Unix-only (uses a small
// shell script); the logic is OS-independent.
func TestCommandSynthesize_RealRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX helper script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fakedtts.sh")
	// $1 = text, $2 = output path
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$2\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	p, err := NewCommandSynthesizer(script+" {text} {output}", "wav", "local")
	if err != nil {
		t.Fatal(err)
	}
	audio, err := p.Synthesize(context.Background(), "spoken text", "", "")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(audio.Data) != "spoken text" {
		t.Fatalf("audio data = %q", audio.Data)
	}
	if audio.Ext != "wav" || audio.Mime != "audio/wav" {
		t.Fatalf("unexpected mime/ext: %+v", audio)
	}
}

func TestCommandSynthesize_EmptyText(t *testing.T) {
	p, _ := NewCommandSynthesizer("say {text} -o {output}", "aiff", "x")
	if _, err := p.Synthesize(context.Background(), "  ", "", ""); err == nil {
		t.Fatal("empty text should error")
	}
}
