/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pulsedash

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

func startDash(t *testing.T, opts Options) (*Server, string) {
	t.Helper()
	if opts.Root == "" {
		opts.Root = t.TempDir()
	}
	s, err := Start(opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s, opts.Root
}

// call issues a GET with full control over token and Host.
func call(t *testing.T, s *Server, path, token, host string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+s.host+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(tokenHeader, token)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func spoolEvents(t *testing.T, root, instance string, n int) {
	t.Helper()
	sp, err := pulse.OpenSpool(root, pulse.Meta{Instance: instance, PID: 99, Surface: "repl"})
	if err != nil {
		t.Fatal(err)
	}
	evs := make([]pulse.Event, 0, n)
	for i := 1; i <= n; i++ {
		evs = append(evs, pulse.Event{Seq: uint64(i), Instance: instance, Kind: pulse.KindTool, Phase: pulse.PhasePoint, ID: "e", Name: "@read"})
	}
	if err := sp.Append(evs...); err != nil {
		t.Fatal(err)
	}
}

func TestStartBindsLoopbackWithTokenURL(t *testing.T) {
	s, _ := startDash(t, Options{})
	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.Host, "127.0.0.1:") {
		t.Fatalf("host = %q, want loopback", u.Host)
	}
	if tok := u.Query().Get("t"); len(tok) != 32 || tok != s.token {
		t.Fatalf("url token = %q", tok)
	}
	other, _ := startDash(t, Options{})
	if other.token == s.token {
		t.Fatal("tokens must be unique per server")
	}
	if _, err := Start(Options{}); err == nil {
		t.Fatal("Start without a root must fail")
	}
}

func TestIndexServesPageWithBootAndSecurityHeaders(t *testing.T) {
	s, _ := startDash(t, Options{
		Lang:    "pt-BR",
		Self:    "inst-self",
		Theme:   map[string]string{"--bg": "#101010"},
		Strings: map[string]string{"title": `</script><script>alert(1)</script>`},
	})
	code, body, hdr := call(t, s, "/", "", "")
	if code != http.StatusOK {
		t.Fatalf("index = %d", code)
	}
	if !strings.Contains(body, "<canvas") || strings.Contains(body, bootPlaceholder) {
		t.Fatal("page must carry the canvas and have the boot placeholder replaced")
	}
	if !strings.Contains(body, `"lang":"pt-BR"`) || !strings.Contains(body, `"--bg":"#101010"`) || !strings.Contains(body, `"self":"inst-self"`) {
		t.Fatal("boot data (lang, theme, self) not injected")
	}
	if strings.Contains(body, "<script>alert(1)") {
		t.Fatal("a translated string broke out of the script element")
	}
	if strings.Contains(body, s.token) {
		t.Fatal("the token must never be embedded in the page")
	}
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Referrer-Policy"} {
		if hdr.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	if !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("CSP = %q", hdr.Get("Content-Security-Policy"))
	}
	if code, _, _ := call(t, s, "/nope", "", ""); code != http.StatusNotFound {
		t.Fatalf("unknown path = %d", code)
	}
}

func TestDefaultLangIsEnglish(t *testing.T) {
	s, _ := startDash(t, Options{})
	_, body, _ := call(t, s, "/", "", "")
	if !strings.Contains(body, `"lang":"en"`) {
		t.Fatal("empty Lang must fall back to en")
	}
}

// The API is reachable only with the token AND the exact bound Host. The
// Host check is what stops a web page that rebinds its DNS name to
// 127.0.0.1 from reading the spool through the user's browser.
func TestAPIRequiresTokenAndExactHost(t *testing.T) {
	s, root := startDash(t, Options{})
	spoolEvents(t, root, "inst-a", 1)

	for name, tc := range map[string]struct {
		token, host string
		want        int
	}{
		"valid":            {s.token, "", http.StatusOK},
		"no token":         {"", "", http.StatusForbidden},
		"wrong token":      {strings.Repeat("0", 32), "", http.StatusForbidden},
		"rebound hostname": {s.token, "evil.example:" + strings.Split(s.host, ":")[1], http.StatusForbidden},
		"localhost alias":  {s.token, "localhost:" + strings.Split(s.host, ":")[1], http.StatusForbidden},
	} {
		for _, path := range []string{"/api/instances", "/api/events?instance=inst-a"} {
			if code, _, _ := call(t, s, path, tc.token, tc.host); code != tc.want {
				t.Errorf("%s %s = %d, want %d", name, path, code, tc.want)
			}
		}
	}
	if code, _, _ := call(t, s, "/", "", "evil.example"); code != http.StatusForbidden {
		t.Errorf("index with a foreign Host = %d, want 403", code)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://"+s.host+"/api/instances", nil)
	req.Header.Set(tokenHeader, s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", resp.StatusCode)
	}
}

func TestInstancesAndEventCursor(t *testing.T) {
	s, root := startDash(t, Options{})
	spoolEvents(t, root, "inst-a", 5)

	_, body, _ := call(t, s, "/api/instances", s.token, "")
	var list struct {
		Instances []struct {
			Instance string `json:"instance"`
			Surface  string `json:"surface"`
			PID      int    `json:"pid"`
			Alive    bool   `json:"alive"`
		} `json:"instances"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("instances json: %v (%s)", err, body)
	}
	if len(list.Instances) != 1 || list.Instances[0].Instance != "inst-a" || !list.Instances[0].Alive || list.Instances[0].PID != 99 {
		t.Fatalf("instances = %+v", list.Instances)
	}

	var page struct {
		Events []pulse.Event `json:"events"`
		Next   uint64        `json:"next"`
	}
	_, body, _ = call(t, s, "/api/events?instance=inst-a&since=0", s.token, "")
	_ = json.Unmarshal([]byte(body), &page)
	if len(page.Events) != 5 || page.Next != 5 {
		t.Fatalf("since=0: %d events next %d", len(page.Events), page.Next)
	}
	_, body, _ = call(t, s, "/api/events?instance=inst-a&since=3", s.token, "")
	_ = json.Unmarshal([]byte(body), &page)
	if len(page.Events) != 2 || page.Events[0].Seq != 4 {
		t.Fatalf("since=3: %+v", page.Events)
	}
	_, body, _ = call(t, s, "/api/events?instance=inst-a&since=5", s.token, "")
	if !strings.Contains(body, `"events":[]`) {
		t.Fatalf("caught-up poll must return an empty array, not null: %s", body)
	}
}

func TestEventsRejectsUnknownAndPathLikeInstance(t *testing.T) {
	s, root := startDash(t, Options{})
	spoolEvents(t, root, "inst-a", 1)
	for _, bad := range []string{"nope", "../inst-a", "..%2F..%2Fetc", ""} {
		if code, _, _ := call(t, s, "/api/events?instance="+bad, s.token, ""); code != http.StatusNotFound {
			t.Errorf("instance %q = %d, want 404", bad, code)
		}
	}
}

// Recording must last only while someone is watching: the lease is taken at
// start, renewed by authenticated polls (rate limited), never by rejected
// ones, and released on shutdown.
func TestLeaseFollowsTheBrowser(t *testing.T) {
	s, root := startDash(t, Options{Holder: "test"})
	if !pulse.LeaseActive(root, time.Now()) {
		t.Fatal("Start must take the lease")
	}
	pulse.ReleaseLease(root)
	s.mu.Lock()
	s.lastRenew = time.Time{}
	s.mu.Unlock()

	call(t, s, "/api/instances", "bad-token", "")
	if pulse.LeaseActive(root, time.Now()) {
		t.Fatal("a rejected request must not renew the lease")
	}
	call(t, s, "/api/instances", s.token, "")
	if !pulse.LeaseActive(root, time.Now()) {
		t.Fatal("an authenticated poll must renew the lease")
	}
	pulse.ReleaseLease(root)
	call(t, s, "/api/instances", s.token, "")
	if pulse.LeaseActive(root, time.Now()) {
		t.Fatal("renewal must be rate limited between polls")
	}

	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if pulse.LeaseActive(root, time.Now()) {
		t.Fatal("Shutdown must release the lease")
	}
	s.renewLease()
	if pulse.LeaseActive(root, time.Now()) {
		t.Fatal("a closed server must never take the lease back")
	}
}
