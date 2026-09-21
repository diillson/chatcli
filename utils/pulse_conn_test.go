/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

func watchConns(t *testing.T) func(n int) []pulse.Event {
	t.Helper()
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(256)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	var seen []pulse.Event
	return func(n int) []pulse.Event {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for len(seen) < n {
			select {
			case ev := <-ch:
				if ev.Kind == pulse.KindConn {
					seen = append(seen, ev)
				}
			case <-deadline:
				t.Fatalf("got %d of %d connection events: %+v", len(seen), n, seen)
			}
		}
		return seen
	}
}

// The path and the query are where keys and tokens travel: a connection
// node may only ever carry the hostname, the method, the status and sizes.
func TestConnTelemetryCarriesHostAndSizesOnly(t *testing.T) {
	collect := watchConns(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/missing" {
			http.Error(w, "SECRET-BODY", http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, "SECRET-BODY-0123456789")
	}))
	defer srv.Close()
	c := NewHTTPClient(zap.NewNop(), 5*time.Second)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/SECRET-PATH?api_key=SECRET-QUERY", strings.NewReader("SECRET-REQUEST"))
	req.Header.Set("Authorization", "Bearer SECRET-HEADER")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	resp, err = c.Get(srv.URL + "/missing") // #nosec G107 -- local test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	evs := collect(4)
	if evs[0].Phase != pulse.PhaseStart || evs[0].Name != "127.0.0.1" || evs[0].Status != pulse.StatusRunning {
		t.Fatalf("start = %+v", evs[0])
	}
	end := evs[1]
	if end.Phase != pulse.PhaseEnd || end.ID != evs[0].ID || end.Status != pulse.StatusOK {
		t.Fatalf("end = %+v", end)
	}
	if end.Attrs["method"] != "POST" || end.Attrs["status"] != "200" || end.Attrs["req_bytes"] != "14" || end.Attrs["resp_bytes"] != "22" {
		t.Fatalf("end attrs = %+v", end.Attrs)
	}
	if evs[3].Status != pulse.StatusError || evs[3].Attrs["status"] != "404" {
		t.Fatalf("a 4xx must read as an error: %+v", evs[3])
	}
	wire, _ := json.Marshal(evs)
	for _, secret := range []string{"SECRET-PATH", "SECRET-QUERY", "SECRET-REQUEST", "SECRET-HEADER", "SECRET-BODY", "api_key"} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("%q leaked onto the bus: %s", secret, wire)
		}
	}
}

// A token stream is one live connection: the node stays open while the body
// is being read and closes with the real size when it ends.
func TestConnTelemetryFollowsAStreamToItsEnd(t *testing.T) {
	collect := watchConns(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
	}))
	defer srv.Close()

	resp, err := NewHTTPClient(zap.NewNop(), 5*time.Second).Get(srv.URL) // #nosec G107 -- local test server
	if err != nil {
		t.Fatal(err)
	}
	if start := collect(1); start[0].Phase != pulse.PhaseStart {
		t.Fatalf("start = %+v", start[0])
	}
	buf := make([]byte, 11)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond) // still streaming: no end event may arrive yet
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close() // EOF already reported it: Close must not report twice
	end := collect(2)[1]
	if end.Phase != pulse.PhaseEnd || end.Attrs["resp_bytes"] != "22" || len(rest) != 11 {
		t.Fatalf("end = %+v (rest %d bytes)", end, len(rest))
	}
	time.Sleep(60 * time.Millisecond)
	if got := collect(2); len(got) != 2 {
		t.Fatalf("stream reported %d events, want 2", len(got))
	}
}

func TestConnTelemetryReportsTransportFailure(t *testing.T) {
	collect := watchConns(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()                                                      // nothing listens any more
	resp, err := NewHTTPClient(zap.NewNop(), 2*time.Second).Get(url) // #nosec G107 -- local test server
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected a dial error")
	}
	evs := collect(2)
	if evs[1].Status != pulse.StatusError || evs[1].Attrs["status"] != "" {
		t.Fatalf("end = %+v", evs[1])
	}
}

func TestConnTelemetryIsFreeWhileOff(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/x", nil)
	if beginConnSpan(req) != nil {
		t.Fatal("dashboard off: no span")
	}
	var c *connSpan
	c.fail(io.EOF)
	c.finish(200, 1)
	body := io.NopCloser(strings.NewReader("x"))
	if c.stream(200, body) != body {
		t.Fatal("dashboard off: the body must not be wrapped")
	}
	if connStatus(399) != pulse.StatusOK || connStatus(500) != pulse.StatusError {
		t.Fatal("status mapping")
	}
}
