/*
 * ChatCLI - Command Line Interface for LLM interaction
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
	"strings"
	"sync/atomic"
	"testing"
)

// staleVoiceTable makes the cached voices.bin look like a model with n
// speakers. The file is sparse: only its size is read.
func staleVoiceTable(t *testing.T, root string, n int) string {
	t.Helper()
	p, ok := locateProvisioned(root)
	if !ok {
		t.Fatal("fixture cache is not complete")
	}
	if err := os.Truncate(p.voices, int64(n)*kokoroVoiceRecordBytes); err != nil {
		t.Fatal(err)
	}
	return p.voices
}

func TestCachedVoiceCount(t *testing.T) {
	root, _ := provisionFakeCache(t)
	voices := staleVoiceTable(t, root, 53)
	if n, ok := cachedVoiceCount(voices); !ok || n != 53 {
		t.Fatalf("53 records read as %d, ok=%v", n, ok)
	}
	// A size that is not a whole number of records says nothing.
	if err := os.Truncate(voices, 53*kokoroVoiceRecordBytes+7); err != nil {
		t.Fatal(err)
	}
	if _, ok := cachedVoiceCount(voices); ok {
		t.Fatal("a ragged file must not be read as a voice table")
	}
	if _, ok := cachedVoiceCount(filepath.Join(root, "missing.bin")); ok {
		t.Fatal("a missing file must not be read as a voice table")
	}
}

// A cache provisioned before upstream added a voice has no row for it. A
// voice the cache holds must never cost a download; the missing one fetches
// the pinned model once, and the engine is left alone.
func TestEmbedded_ModelIsRefreshedOnlyForAVoiceTheCacheLacks(t *testing.T) {
	root, capture := provisionFakeCache(t)
	staleVoiceTable(t, root, 53)
	kokoroTar, err := os.ReadFile(filepath.Join("testdata", "fake-kokoro.tar.bz2"))
	if err != nil {
		t.Fatal(err)
	}
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if !strings.Contains(r.URL.Path, "kokoro") {
			t.Errorf("only the model may be fetched, got %s", r.URL.Path)
		}
		_, _ = w.Write(kokoroTar)
	}))
	defer srv.Close()

	e := newTestEmbedded(t, root)
	e.binBaseURL = srv.URL + "/engine/"
	e.modelURL = srv.URL + "/kokoro.tar.bz2"
	e.minModelBytes = 16
	e.pins = pinFixtures(nil, kokoroTar)

	if _, err := e.Synthesize(context.Background(), "A voice the cache holds.", "zm_yunyang", "wav"); err != nil {
		t.Fatalf("voice 52 of 53: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("a voice the cache holds cost %d downloads", got)
	}

	if _, err := e.Synthesize(context.Background(), "La voz nueva.", "em_santa", "wav"); err != nil {
		t.Fatalf("voice 53 after refresh: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("the missing voice must fetch the model once, got %d", got)
	}
	args, _ := os.ReadFile(capture)
	if !strings.Contains(string(args), "--sid=53") {
		t.Fatalf("the engine was not asked for speaker 53:\n%s", args)
	}
	if _, err := os.Stat(filepath.Join(root, "kokoro.previous")); !os.IsNotExist(err) {
		t.Fatalf("the model set aside must be gone after a refresh: %v", err)
	}
	// The refreshed paths are kept: asking again fetches nothing.
	if _, err := e.Synthesize(context.Background(), "Otra vez.", "em_santa", "wav"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("a second request fetched again: %d", got)
	}
}

// A refresh that cannot complete puts the old model back: the new voice is
// refused with the reason, and the voices installed still work offline.
func TestEmbedded_FailedRefreshKeepsTheCachedModel(t *testing.T) {
	root, _ := provisionFakeCache(t)
	voices := staleVoiceTable(t, root, 53)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not the pinned archive, and long enough to pass the size floor"))
	}))
	defer srv.Close()

	e := newTestEmbedded(t, root)
	e.modelURL = srv.URL + "/kokoro.tar.bz2"
	e.minModelBytes = 16
	e.pins = pinFixtures(nil, []byte("the real archive"))

	_, err := e.Synthesize(context.Background(), "La voz nueva.", "em_santa", "wav")
	if err == nil || !strings.Contains(err.Error(), "em_santa") || !strings.Contains(err.Error(), "53 voices") {
		t.Fatalf("the refusal must name the voice and what the cache holds: %v", err)
	}
	if n, ok := cachedVoiceCount(voices); !ok || n != 53 {
		t.Fatalf("the cached model was not put back: %d, ok=%v", n, ok)
	}
	if _, err := os.Stat(filepath.Join(root, "kokoro.previous")); !os.IsNotExist(err) {
		t.Fatalf("nothing may be left aside after a failed refresh: %v", err)
	}
	if _, err := e.Synthesize(context.Background(), "Still here.", "bm_george", "wav"); err != nil {
		t.Fatalf("an installed voice stopped working after a failed refresh: %v", err)
	}
}
