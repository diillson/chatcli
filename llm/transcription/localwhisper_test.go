/*
 * ChatCLI - tests for local whisper.cpp model provisioning.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package transcription

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChooseWhisperCppModel(t *testing.T) {
	t.Setenv("WHISPER_MODEL", "")
	if got := chooseWhisperCppModel(""); got != "base" {
		t.Errorf("empty → %q, want base", got)
	}
	if got := chooseWhisperCppModel("small"); got != "small" {
		t.Errorf("size → %q, want small", got)
	}
	if got := chooseWhisperCppModel("whisper-1"); got != "base" {
		t.Errorf("cloud name → %q, want base (fallback)", got)
	}
	if got := chooseWhisperCppModel("/models/ggml-medium.bin"); got != "/models/ggml-medium.bin" {
		t.Errorf("path → %q, want the path", got)
	}
	t.Setenv("WHISPER_MODEL", "/env/ggml.bin")
	if got := chooseWhisperCppModel("small"); got != "/env/ggml.bin" {
		t.Errorf("WHISPER_MODEL must win, got %q", got)
	}
}

func TestIsWhisperSize(t *testing.T) {
	for _, s := range []string{"tiny", "base", "small", "large-v3", "medium.en"} {
		if !isWhisperSize(s) {
			t.Errorf("%q should be a size", s)
		}
	}
	for _, s := range []string{"", "whisper-1", "huge"} {
		if isWhisperSize(s) {
			t.Errorf("%q should not be a size", s)
		}
	}
}

func TestResolveModel_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(good, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newLocalWhisperCpp("whisper-cli", good, nil)
	got, err := l.resolveModel(context.Background())
	if err != nil || got != good {
		t.Errorf("explicit path: got %q err %v", got, err)
	}

	miss := newLocalWhisperCpp("whisper-cli", filepath.Join(dir, "nope.bin"), nil)
	if _, err := miss.resolveModel(context.Background()); err == nil {
		t.Error("missing explicit model must error")
	}
}

func TestResolveModel_CacheHit(t *testing.T) {
	l := newLocalWhisperCpp("whisper-cli", "", nil) // size → base
	l.cacheDir = t.TempDir()
	cached := filepath.Join(l.cacheDir, "ggml-base.bin")
	if err := os.WriteFile(cached, bytes.Repeat([]byte("M"), minModelBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := l.resolveModel(context.Background())
	if err != nil || got != cached {
		t.Errorf("cache hit: got %q err %v, want %q", got, err, cached)
	}
}

func TestResolveModel_Download(t *testing.T) {
	payload := bytes.Repeat([]byte("M"), minModelBytes+5)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	l := newLocalWhisperCpp("whisper-cli", "", nil) // size → base
	l.cacheDir = t.TempDir()
	l.baseURL = srv.URL
	got, err := l.resolveModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(l.cacheDir, "ggml-base.bin"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !fileExists(got) {
		t.Error("model should have been downloaded to the cache")
	}
}

func TestLocalWhisperCpp_Transcribe(t *testing.T) {
	// Use `echo` as the engine and an explicit (existing) model so neither
	// whisper-cli nor a download is needed: echo prints its args, standing in
	// for a transcript. ffmpeg, if present, fails on the fake audio and the
	// code falls back to the original — so this is deterministic either way.
	dir := t.TempDir()
	model := filepath.Join(dir, "m.bin")
	if err := os.WriteFile(model, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newLocalWhisperCpp("echo", model, nil)
	out, err := l.Transcribe(context.Background(), []byte("hello"), "audio/ogg", "", "")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output from the echo stand-in")
	}
	// Regression guard: with no language configured, whisper-cli must be told to
	// auto-detect (its built-in default is "en", which would force English).
	if !strings.Contains(out, "-l auto") {
		t.Errorf("expected '-l auto' in the whisper-cli args; got %q", out)
	}
	if !strings.HasPrefix(l.Name(), "local:echo/") {
		t.Errorf("Name = %q", l.Name())
	}
}

func TestLocalWhisperCpp_FfmpegTranscode(t *testing.T) {
	// Fake ffmpeg: write something to the last arg (the output path) so the
	// transcode path runs end to end without a real ffmpeg. `echo` then stands
	// in for whisper-cli, printing its args as the "transcript".
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakeffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do out=\"$a\"; done\nprintf 'wavdata' > \"$out\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := lookupFFmpeg
	lookupFFmpeg = func() string { return fake }
	t.Cleanup(func() { lookupFFmpeg = orig })

	model := filepath.Join(dir, "m.bin")
	if err := os.WriteFile(model, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newLocalWhisperCpp("echo", model, nil)
	out, err := l.Transcribe(context.Background(), []byte("opus-bytes"), "audio/ogg", "", "pt")
	if err != nil {
		t.Fatalf("transcribe with ffmpeg transcode: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output after transcode")
	}
}

func TestDownloadModel(t *testing.T) {
	payload := bytes.Repeat([]byte("M"), minModelBytes+10) // pass the size floor
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "ggml-base.bin")
	if err := downloadModel(context.Background(), srv.URL, dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dest)
	if err != nil || fi.Size() != int64(len(payload)) {
		t.Errorf("downloaded file wrong: size=%v err=%v", fi, err)
	}
}

func TestDownloadModel_RejectsTinyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("404 not found")) // below the size floor → rejected
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "ggml-base.bin")
	if err := downloadModel(context.Background(), srv.URL, dest); err == nil {
		t.Error("a tiny body must be rejected as not a real model")
	}
	if fileExists(dest) {
		t.Error("rejected download must not leave the dest file")
	}
}

func TestDownloadModel_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "m.bin")
	if err := downloadModel(context.Background(), srv.URL, dest); err == nil {
		t.Error("non-200 must error")
	}
}
