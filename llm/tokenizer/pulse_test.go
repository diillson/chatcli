/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tokenizer

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

// connEnds turns the process-wide bus on and returns a collector of the
// connection end events seen so far.
func connEnds(t *testing.T) func(n int) []pulse.Event {
	t.Helper()
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	var seen []pulse.Event
	return func(n int) []pulse.Event {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for len(seen) < n {
			select {
			case ev := <-ch:
				if ev.Kind == pulse.KindConn && ev.Phase == pulse.PhaseEnd {
					seen = append(seen, ev)
				}
			case <-deadline:
				t.Fatalf("got %d of %d connection end events: %+v", len(seen), n, seen)
			}
		}
		return seen
	}
}

// The vocabulary is downloaded once, in the background, the first time a GPT
// model needs an exact token count. The client the package builds for that
// is the one under test here, not a test double.
func TestVocabularyDownloadShowsAsAConnection(t *testing.T) {
	collect := connEnds(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte("a")) + " 1\n"))
	}))
	defer srv.Close()

	setup()
	prod := loader.client // the production client, metered transport and all
	l := &cacheLoader{dir: filepath.Join(t.TempDir(), "tok"), client: prod}
	if ranks, err := l.LoadTiktokenBpe(srv.URL + "/o200k_base.tiktoken?sig=SECRET"); err != nil || ranks["a"] != 1 {
		t.Fatalf("load: %v %v", ranks, err)
	}
	end := collect(1)[0]
	if end.Name != "127.0.0.1" || end.Attrs["status"] != "200" || end.Attrs["resp_bytes"] == "" {
		t.Fatalf("end = %+v", end)
	}
}
