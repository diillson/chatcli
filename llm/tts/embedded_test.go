/*
 * ChatCLI - Embedded TTS provider tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

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

// provisionFakeCache builds a complete cache layout whose "engine" is a shell
// script that records its argv and environment, then writes fake WAV bytes to
// the --output-filename target. Returns the cache root and the capture file.
func provisionFakeCache(t *testing.T) (root, capture string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake engine is a shell script")
	}
	root = t.TempDir()
	capture = filepath.Join(root, "capture.txt")

	script := `#!/bin/sh
out=""
for a in "$@"; do
  echo "$a" >> "` + capture + `"
  case "$a" in --output-filename=*) out="${a#--output-filename=}" ;; esac
done
echo "DYLD_LIBRARY_PATH=$DYLD_LIBRARY_PATH" >> "` + capture + `"
echo "LD_LIBRARY_PATH=$LD_LIBRARY_PATH" >> "` + capture + `"
printf 'RIFFfakewav' > "$out"
`
	bin := filepath.Join(root, "sherpa-v"+sherpaVersion, "bin", sherpaBinName)
	mustWrite(t, bin, script)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "sherpa-v"+sherpaVersion, "lib", "libfake.so"), "lib")
	mustWrite(t, filepath.Join(root, "kokoro", "voices.bin"), "v")
	mustWrite(t, filepath.Join(root, "kokoro", "tokens.txt"), "t")
	mustWrite(t, filepath.Join(root, "kokoro", "model.int8.onnx"), "m")
	if err := os.MkdirAll(filepath.Join(root, "kokoro", "espeak-ng-data"), 0o750); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, readyMarker(root), "")
	return root, capture
}

func newTestEmbedded(t *testing.T, root string) *embeddedSynth {
	t.Helper()
	e := NewEmbedded("", "", nil)
	e.cacheDir = root
	return e
}

func TestEmbedded_SynthesizeRoutesEnglish(t *testing.T) {
	root, capture := provisionFakeCache(t)
	e := newTestEmbedded(t, root)

	audio, err := e.Synthesize(context.Background(), "Good evening sir, the system is ready.", "", "wav")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(audio.Data) != "RIFFfakewav" || audio.Ext != "wav" {
		t.Fatalf("audio = %q ext %q, want fake wav", audio.Data, audio.Ext)
	}
	argv := readCapture(t, capture)
	assertContains(t, argv, "--sid=26")         // bm_george
	assertContains(t, argv, "--kokoro-lang=en") // English G2P
	assertContains(t, argv, "Good evening sir, the system is ready.")
}

func TestEmbedded_SynthesizeRoutesPortuguese(t *testing.T) {
	root, capture := provisionFakeCache(t)
	e := newTestEmbedded(t, root)

	if _, err := e.Synthesize(context.Background(), "Boa noite senhor, está tudo pronto para você.", "", "wav"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	argv := readCapture(t, capture)
	assertContains(t, argv, "--sid=43")            // pm_alex
	assertContains(t, argv, "--kokoro-lang=pt-br") // Portuguese G2P
}

func TestEmbedded_ExplicitVoiceWins(t *testing.T) {
	root, capture := provisionFakeCache(t)
	e := newTestEmbedded(t, root)

	if _, err := e.Synthesize(context.Background(), "Boa noite senhor, está tudo pronto.", "bm_daniel", "wav"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	argv := readCapture(t, capture)
	assertContains(t, argv, "--sid=24") // bm_daniel beats language routing
}

func TestEmbedded_UnknownExplicitVoiceFallsBackToRouting(t *testing.T) {
	root, capture := provisionFakeCache(t)
	e := newTestEmbedded(t, root)

	if _, err := e.Synthesize(context.Background(), "All systems are ready for you now.", "hal_9000", "wav"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	argv := readCapture(t, capture)
	assertContains(t, argv, "--sid=26") // routed to the English default
}

func TestEmbedded_OpusTranscodeViaFakeFFmpeg(t *testing.T) {
	root, _ := provisionFakeCache(t)
	e := newTestEmbedded(t, root)
	installFakeFFmpeg(t, "OggSfake")

	audio, err := e.Synthesize(context.Background(), "Voice note please, everything is done.", "", "ogg")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(audio.Data) != "OggSfake" || audio.Ext != "ogg" || audio.Mime != "audio/ogg" {
		t.Fatalf("audio = %q ext %q mime %q, want transcoded ogg", audio.Data, audio.Ext, audio.Mime)
	}
}

func TestEmbedded_NoFFmpegDegradesToWav(t *testing.T) {
	root, _ := provisionFakeCache(t)
	e := newTestEmbedded(t, root)
	stubNoFFmpeg(t)

	audio, err := e.Synthesize(context.Background(), "Voice note please, everything is done.", "", "ogg")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if audio.Ext != "wav" || string(audio.Data) != "RIFFfakewav" {
		t.Fatalf("audio ext = %q data %q, want wav passthrough", audio.Ext, audio.Data)
	}
}

func TestEmbedded_EmptyTextErrors(t *testing.T) {
	e := newTestEmbedded(t, t.TempDir())
	if _, err := e.Synthesize(context.Background(), "   ", "", "wav"); err == nil {
		t.Fatal("empty text must error")
	}
}

func TestEmbedded_NameAndDefaults(t *testing.T) {
	e := NewEmbedded("", "", nil)
	if e.Name() != "embedded:kokoro/bm_george" {
		t.Fatalf("Name = %q", e.Name())
	}
	if e.enVoice != defaultEmbeddedEnVoice || e.ptVoice != defaultEmbeddedPtVoice {
		t.Fatalf("defaults = %s/%s", e.enVoice, e.ptVoice)
	}
	custom := NewEmbedded("bm_lewis", "pm_santa", nil)
	if custom.enVoice != "bm_lewis" || custom.ptVoice != "pm_santa" {
		t.Fatalf("custom voices not honored: %s/%s", custom.enVoice, custom.ptVoice)
	}
}

func TestEmbedded_IsProvisioned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses shell script")
	}
	root, _ := provisionFakeCache(t)
	if !newTestEmbedded(t, root).isProvisioned() {
		t.Fatal("complete cache must report provisioned")
	}
	if newTestEmbedded(t, t.TempDir()).isProvisioned() {
		t.Fatal("empty cache must not report provisioned")
	}
}

// TestEmbedded_ProvisionFromHTTP exercises the full first-use path: download
// both archives from an httptest server, extract, locate, write the marker,
// then synthesize with the engine that came out of the tarball. A second
// provider over the same cache must not hit the network again.
func TestEmbedded_ProvisionFromHTTP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture engine is a shell script")
	}
	sherpaTar, err := os.ReadFile(filepath.Join("testdata", "fake-sherpa.tar.bz2"))
	if err != nil {
		t.Fatal(err)
	}
	kokoroTar, err := os.ReadFile(filepath.Join("testdata", "fake-kokoro.tar.bz2"))
	if err != nil {
		t.Fatal(err)
	}
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if strings.Contains(r.URL.Path, "kokoro") {
			_, _ = w.Write(kokoroTar)
			return
		}
		_, _ = w.Write(sherpaTar)
	}))
	defer srv.Close()

	root := t.TempDir()
	e := newTestEmbedded(t, root)
	e.binBaseURL = srv.URL + "/"
	e.modelURL = srv.URL + "/kokoro.tar.bz2"
	e.minBinBytes, e.minModelBytes = 16, 16

	audio, err := e.Synthesize(context.Background(), "Provisioning end to end now.", "", "wav")
	if err != nil {
		t.Fatalf("Synthesize after provision: %v", err)
	}
	if string(audio.Data) != "RIFFfromtarball" {
		t.Fatalf("audio = %q, want engine output from tarball", audio.Data)
	}
	if !fileExistsTTS(readyMarker(root)) {
		t.Fatal("ready marker missing after provision")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 downloads, got %d", got)
	}

	// A fresh provider over the same cache reuses it without network access.
	e2 := newTestEmbedded(t, root)
	e2.binBaseURL = "http://127.0.0.1:0/unreachable/" // any hit would fail
	e2.modelURL = "http://127.0.0.1:0/unreachable"
	if _, err := e2.Synthesize(context.Background(), "Cached run, no downloads.", "", "wav"); err != nil {
		t.Fatalf("Synthesize from cache: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("cached run must not download, got %d hits", got)
	}
}

func TestEmbedded_ProvisionRejectsUndersizedDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()

	e := newTestEmbedded(t, t.TempDir())
	e.binBaseURL = srv.URL + "/"
	e.modelURL = srv.URL + "/kokoro.tar.bz2"
	e.minBinBytes, e.minModelBytes = 1024, 1024

	if _, err := e.Synthesize(context.Background(), "This must fail to provision.", "", "wav"); err == nil {
		t.Fatal("undersized archives must fail provisioning")
	}
}

func readCapture(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func assertContains(t *testing.T, lines []string, want string) {
	t.Helper()
	for _, l := range lines {
		if l == want {
			return
		}
	}
	t.Errorf("argv missing %q in %q", want, lines)
}
