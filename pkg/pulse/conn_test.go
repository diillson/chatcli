/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pulse

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConnMeteringIsFreeWhileOff(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/x", nil)
	if BeginConn(req) != nil {
		t.Fatal("dashboard off: no span")
	}
	var c *ConnSpan
	c.Fail(io.EOF)
	c.Finish(200, 1)
	body := io.NopCloser(strings.NewReader("x"))
	if c.Stream(200, body) != body {
		t.Fatal("dashboard off: the body must not be wrapped")
	}
	if ConnStatus(399) != StatusOK || ConnStatus(400) != StatusError || ConnStatus(500) != StatusError {
		t.Fatal("status mapping")
	}
}

func connEvents(t *testing.T, ch <-chan Event, n int) []Event {
	t.Helper()
	var out []Event
	deadline := time.After(3 * time.Second)
	for len(out) < n {
		select {
		case ev := <-ch:
			if ev.Kind == KindConn {
				out = append(out, ev)
			}
		case <-deadline:
			t.Fatalf("got %d of %d connection events: %+v", len(out), n, out)
		}
	}
	return out
}

// The path and the query are where keys and tokens travel: a connection node
// may only ever carry the hostname, the method, the status and sizes. The body
// is metered while the caller reads it, never buffered.
func TestMeterTransportShowsHostAndSizesOnly(t *testing.T) {
	bus := Default()
	ch, cancel := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	payload := strings.Repeat("x", 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/missing" {
			http.Error(w, "SECRET-BODY", http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()
	c := &http.Client{Transport: MeterTransport(nil), Timeout: 5 * time.Second}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/SECRET-PATH?api_key=SECRET-QUERY", strings.NewReader("SECRET-REQUEST"))
	req.Header.Set("Authorization", "Bearer SECRET-HEADER")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if evs := connEvents(t, ch, 1); evs[0].Phase != PhaseStart || evs[0].Name != "127.0.0.1" {
		t.Fatalf("only the start may exist before the body is read: %+v", evs)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close() // EOF already reported it: Close must not report twice
	if string(body) != payload {
		t.Fatal("the caller must receive the body untouched")
	}
	end := connEvents(t, ch, 1)[0]
	if end.Phase != PhaseEnd || end.Status != StatusOK || end.Attrs["status"] != "200" || end.Attrs["resp_bytes"] != "2048" || end.Attrs["req_bytes"] != "14" || end.Attrs["method"] != "POST" {
		t.Fatalf("end = %+v", end)
	}

	resp, err = c.Get(srv.URL + "/missing") // #nosec G107 -- local test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	miss := connEvents(t, ch, 2)
	if miss[1].Status != StatusError || miss[1].Attrs["status"] != "404" {
		t.Fatalf("a 4xx must read as an error: %+v", miss[1])
	}

	wire, _ := json.Marshal([]Event{end, miss[0], miss[1]})
	for _, secret := range []string{"SECRET-PATH", "SECRET-QUERY", "SECRET-REQUEST", "SECRET-HEADER", "SECRET-BODY", "api_key"} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("%q leaked onto the bus: %s", secret, wire)
		}
	}
}

func TestMeterTransportReportsADialFailure(t *testing.T) {
	bus := Default()
	ch, cancel := bus.Subscribe(16)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	resp, err := (&http.Client{Transport: MeterTransport(http.DefaultTransport), Timeout: 2 * time.Second}).Get(url) // #nosec G107 -- local test server
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected a dial error")
	}
	if evs := connEvents(t, ch, 2); evs[1].Status != StatusError || evs[1].Attrs["status"] != "" {
		t.Fatalf("end = %+v", evs[1])
	}
}
