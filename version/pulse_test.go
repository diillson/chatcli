/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package version

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// The release check runs in the background at boot: on the live dashboard it
// is a connection to the release host, with nothing of the URL but the host.
func TestReleaseCheckShowsAsAConnection(t *testing.T) {
	collect := connEnds(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","body":""}`))
	}))
	defer srv.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", srv.URL+"/repos/x/releases/latest?token=SECRET")

	_, _ = FetchLatestReleaseImpl(context.Background())

	end := collect(1)[0]
	if end.Name != "127.0.0.1" || end.Attrs["status"] != "200" || end.Attrs["method"] != "GET" {
		t.Fatalf("end = %+v", end)
	}
	for k, v := range end.Attrs {
		if v == "" || len(v) > 12 {
			t.Fatalf("attribute %s=%q looks like more than a method, a status or a size", k, v)
		}
	}
}
