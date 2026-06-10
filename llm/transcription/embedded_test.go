/*
 * ChatCLI - Embedded STT provider tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package transcription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// provisionFakeSTTCache builds a complete cache layout whose "engine" is a
// shell script that records its argv, then prints a sherpa-style JSON result.
// Returns the cache root and the capture file.
func provisionFakeSTTCache(t *testing.T) (root, capture string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake engine is a shell script")
	}
	root = t.TempDir()
	capture = filepath.Join(root, "capture.txt")

	script := `#!/bin/sh
for a in "$@"; do
  echo "$a" >> "` + capture + `"
done
echo "log line from engine" >&2
echo '{"lang": "<|pt|>", "emotion": "", "event": "", "text": " olá mundo ", "timestamps": [], "tokens": [], "words": []}'
`
	bin := filepath.Join(root, "sherpa-v0test", "bin", asrBinName)
	mustWriteSTT(t, bin, script)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteSTT(t, filepath.Join(root, "sherpa-v0test", "lib", "libfake.so"), "lib")
	modelDir := filepath.Join(root, "whisper-base", "sherpa-onnx-whisper-base")
	mustWriteSTT(t, filepath.Join(modelDir, "base-encoder.int8.onnx"), "e")
	mustWriteSTT(t, filepath.Join(modelDir, "base-decoder.int8.onnx"), "d")
	mustWriteSTT(t, filepath.Join(modelDir, "base-tokens.txt"), "t")
	mustWriteSTT(t, sttReadyMarker(root, "base"), "")
	return root, capture
}

func newTestEmbeddedSTT(t *testing.T, root string) *embeddedWhisper {
	t.Helper()
	e := NewEmbeddedWhisper("", nil)
	e.cacheDir = root
	return e
}

// stubFFmpegMissing makes the provider see no ffmpeg so WAV passthrough and
// the actionable non-WAV error are deterministic regardless of the host.
func stubFFmpegMissing(t *testing.T) {
	t.Helper()
	orig := lookupFFmpeg
	lookupFFmpeg = func() string { return "" }
	t.Cleanup(func() { lookupFFmpeg = orig })
}

func TestEmbeddedWhisper_TranscribeWAV(t *testing.T) {
	root, capture := provisionFakeSTTCache(t)
	e := newTestEmbeddedSTT(t, root)
	stubFFmpegMissing(t)

	got, err := e.Transcribe(context.Background(), []byte("RIFFfakewav"), "audio/wav", "note.wav", "")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "olá mundo" {
		t.Fatalf("transcript = %q, want %q", got, "olá mundo")
	}
	argv := readSTTCapture(t, capture)
	assertHasPrefix(t, argv, "--whisper-encoder=")
	assertHasPrefix(t, argv, "--whisper-decoder=")
	assertHasPrefix(t, argv, "--tokens=")
	for _, a := range argv {
		if strings.HasPrefix(a, "--whisper-language=") {
			t.Errorf("no language hint configured, but argv has %q", a)
		}
	}
}

func TestEmbeddedWhisper_LanguageHintIsForwarded(t *testing.T) {
	root, capture := provisionFakeSTTCache(t)
	e := newTestEmbeddedSTT(t, root)
	stubFFmpegMissing(t)

	if _, err := e.Transcribe(context.Background(), []byte("RIFFfakewav"), "audio/wav", "note.wav", "pt"); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	assertHasPrefix(t, readSTTCapture(t, capture), "--whisper-language=pt")
}

func TestEmbeddedWhisper_NonWavWithoutFFmpegErrors(t *testing.T) {
	root, _ := provisionFakeSTTCache(t)
	e := newTestEmbeddedSTT(t, root)
	stubFFmpegMissing(t)

	_, err := e.Transcribe(context.Background(), []byte("OggSopus"), "audio/ogg", "note.ogg", "")
	if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("opus without ffmpeg must return the actionable ffmpeg error, got %v", err)
	}
}

func TestEmbeddedWhisper_IsProvisioned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses shell script")
	}
	root, _ := provisionFakeSTTCache(t)
	if !newTestEmbeddedSTT(t, root).isProvisioned() {
		t.Fatal("complete cache must report provisioned")
	}
	if newTestEmbeddedSTT(t, t.TempDir()).isProvisioned() {
		t.Fatal("empty cache must not report provisioned")
	}
}

func TestLocateSTT_PrefersInt8AndScopesToSize(t *testing.T) {
	root, _ := provisionFakeSTTCache(t)
	// fp32 siblings must lose to the int8 pair.
	modelDir := filepath.Join(root, "whisper-base", "sherpa-onnx-whisper-base")
	mustWriteSTT(t, filepath.Join(modelDir, "base-encoder.onnx"), "E")
	mustWriteSTT(t, filepath.Join(modelDir, "base-decoder.onnx"), "D")
	// Artifacts of another size must never satisfy this size's lookup.
	otherDir := filepath.Join(root, "whisper-tiny", "sherpa-onnx-whisper-tiny")
	mustWriteSTT(t, filepath.Join(otherDir, "tiny-encoder.int8.onnx"), "x")

	p, ok := locateSTT(root, "base")
	if !ok {
		t.Fatal("complete layout not located")
	}
	if filepath.Base(p.encoder) != "base-encoder.int8.onnx" || filepath.Base(p.decoder) != "base-decoder.int8.onnx" {
		t.Errorf("int8 pair not preferred: %s / %s", p.encoder, p.decoder)
	}
	if _, ok := locateSTT(root, "tiny"); ok {
		t.Error("tiny lookup must not pass with only an encoder present")
	}
}

func TestChooseEmbeddedWhisperSize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", defaultEmbeddedWhisperSize},
		{"whisper-1", defaultEmbeddedWhisperSize}, // cloud default leaks in: ignore
		{"/some/path/ggml-base.bin", defaultEmbeddedWhisperSize},
		{"tiny", "tiny"},
		{"TINY", "tiny"},
		{"small.en", "small.en"},
		{"large-v3", "large-v3"},
	}
	for _, tt := range tests {
		if got := chooseEmbeddedWhisperSize(tt.in); got != tt.want {
			t.Errorf("chooseEmbeddedWhisperSize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseSherpaResult(t *testing.T) {
	out := []byte("some log\n/tmp/x.wav\n{\"lang\": \"<|en|>\", \"text\": \" hello \", \"tokens\": []}\ntrailer")
	text, ok := parseSherpaResult(out)
	if !ok || text != " hello " {
		t.Fatalf("parse = %q, %v", text, ok)
	}
	if _, ok := parseSherpaResult([]byte("no json here")); ok {
		t.Fatal("output without a result object must not parse")
	}
	// A result with empty text is still a result (silent clip).
	if text, ok := parseSherpaResult([]byte("{\"text\": \"\"}")); !ok || text != "" {
		t.Fatalf("empty-text result must parse, got %q, %v", text, ok)
	}
}

// TestEmbeddedWhisper_ProvisionFromHTTP exercises the full first-use path:
// download both archives from an httptest server, extract, locate, write the
// marker, then transcribe with the engine that came out of the tarball. A
// second provider over the same cache must not hit the network again.
func TestEmbeddedWhisper_ProvisionFromHTTP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture engine is a shell script")
	}
	engineTar, err := os.ReadFile(filepath.Join("testdata", "fake-sherpa-asr.tar.bz2"))
	if err != nil {
		t.Fatal(err)
	}
	whisperTar, err := os.ReadFile(filepath.Join("testdata", "fake-whisper.tar.bz2"))
	if err != nil {
		t.Fatal(err)
	}
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if strings.Contains(r.URL.Path, "whisper") {
			_, _ = w.Write(whisperTar)
			return
		}
		_, _ = w.Write(engineTar)
	}))
	defer srv.Close()

	root := t.TempDir()
	e := newTestEmbeddedSTT(t, root)
	e.binBaseURL = srv.URL + "/"
	e.modelBaseURL = srv.URL + "/"
	e.minBinBytes, e.minModelBytes = 16, 16
	stubFFmpegMissing(t)

	got, err := e.Transcribe(context.Background(), []byte("RIFFfakewav"), "audio/wav", "note.wav", "")
	if err != nil {
		t.Fatalf("Transcribe after provision: %v", err)
	}
	if got != "from tarball" {
		t.Fatalf("transcript = %q, want engine output from tarball", got)
	}
	if !fileExists(sttReadyMarker(root, "base")) {
		t.Fatal("ready marker missing after provision")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 downloads, got %d", got)
	}

	// A fresh provider over the same cache reuses it without network access.
	e2 := newTestEmbeddedSTT(t, root)
	e2.binBaseURL = "http://127.0.0.1:0/unreachable/" // any hit would fail
	e2.modelBaseURL = "http://127.0.0.1:0/unreachable/"
	if _, err := e2.Transcribe(context.Background(), []byte("RIFFfakewav"), "audio/wav", "note.wav", ""); err != nil {
		t.Fatalf("Transcribe from cache: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("cached run must not download, got %d hits", got)
	}
}

func TestEmbeddedWhisper_ProvisionRejectsUndersizedDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()

	e := newTestEmbeddedSTT(t, t.TempDir())
	e.binBaseURL = srv.URL + "/"
	e.modelBaseURL = srv.URL + "/"
	e.minBinBytes, e.minModelBytes = 1024, 1024

	if err := e.EnsureReady(context.Background()); err == nil {
		t.Fatal("undersized archives must fail provisioning")
	}
}

func TestEmbeddedWhisper_Name(t *testing.T) {
	if got := NewEmbeddedWhisper("", nil).Name(); got != "embedded:whisper/base" {
		t.Fatalf("Name = %q", got)
	}
	if got := NewEmbeddedWhisper("tiny", nil).Name(); got != "embedded:whisper/tiny" {
		t.Fatalf("Name = %q", got)
	}
}

func readSTTCapture(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func assertHasPrefix(t *testing.T, lines []string, prefix string) {
	t.Helper()
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return
		}
	}
	t.Errorf("argv missing %q* in %q", prefix, lines)
}

func mustWriteSTT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
