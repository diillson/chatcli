/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package pulsedash serves the live telemetry dashboard: a single embedded
// page that draws, as a graph that lights up while things happen, what every
// chatcli process on the machine is doing.
//
// The server is a read-only observer of the pulse spool. It holds no
// per-client state: the page polls with a sequence cursor, so a reload or a
// second tab replays from disk for free, and killing the dashboard never
// affects a running session.
//
// It is local but not open. It binds to 127.0.0.1 on an ephemeral port, and
// on top of that every API call needs the random token minted at start (it
// travels in the URL the user is handed), and every request must carry the
// exact Host it was bound to, which closes DNS rebinding from a web page.
//
// The recording lease is renewed only while a browser is actually polling.
// Close the tab and, one lease TTL later, every process stops recording on
// its own.
package pulsedash

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

//go:embed assets/dash.html
var assets embed.FS

const (
	readHeaderTimeout = 10 * time.Second
	bootPlaceholder   = "__PULSE_BOOT__"
	tokenHeader       = "X-Pulse-Token" // #nosec G101 -- the NAME of the request header, not a credential; the token itself is random per server
	maxEventsPerPoll  = 2000
)

// Options configures the dashboard.
type Options struct {
	Root    string            // pulse spool root; required
	Holder  string            // who renews the lease (shown in lease.json)
	Lang    string            // html lang attribute, e.g. "pt-BR"
	Theme   map[string]string // CSS custom property name -> color, e.g. "--bg": "#1e1e2e"
	Strings map[string]string // translated UI strings by key
	Self    string            // instance token of the serving process, highlighted in the page
}

// Server is a running dashboard.
type Server struct {
	opts  Options
	token string
	host  string
	url   string
	page  []byte
	srv   *http.Server

	mu        sync.Mutex
	closed    bool
	lastRenew time.Time
}

// Start binds the dashboard to 127.0.0.1 on an ephemeral port and takes the
// recording lease so the processes start reporting right away.
func Start(opts Options) (*Server, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("pulsedash: spool root is required")
	}
	raw, err := assets.ReadFile("assets/dash.html")
	if err != nil {
		return nil, fmt.Errorf("pulsedash: reading page: %w", err)
	}
	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, fmt.Errorf("pulsedash: minting token: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("pulsedash: listening: %w", err)
	}

	s := &Server{opts: opts, token: hex.EncodeToString(tok[:]), host: ln.Addr().String()}
	s.url = "http://" + s.host + "/?t=" + s.token
	if s.page, err = renderPage(raw, opts); err != nil {
		_ = ln.Close()
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/instances", s.guard(s.handleInstances))
	mux.HandleFunc("/api/events", s.guard(s.handleEvents))
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}

	s.renewLease()
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// boot is what the page receives at load, injected into the HTML so the
// first paint already has the theme and the language.
type boot struct {
	Lang    string            `json:"lang"`
	Theme   map[string]string `json:"theme"`
	Strings map[string]string `json:"strings"`
	Self    string            `json:"self"`
}

func renderPage(raw []byte, opts Options) ([]byte, error) {
	lang := opts.Lang
	if lang == "" {
		lang = "en"
	}
	// json.Marshal escapes <, > and & as < etc., so the payload cannot
	// close the script element it is injected into.
	payload, err := json.Marshal(boot{Lang: lang, Theme: opts.Theme, Strings: opts.Strings, Self: opts.Self})
	if err != nil {
		return nil, fmt.Errorf("pulsedash: encoding boot data: %w", err)
	}
	return []byte(strings.Replace(string(raw), bootPlaceholder, string(payload), 1)), nil
}

// URL is the address to open, token included.
func (s *Server) URL() string { return s.url }

// Shutdown stops the server and releases the lease. Safe to call twice.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	pulse.ReleaseLease(s.opts.Root)
	return s.srv.Shutdown(ctx)
}

// renewLease extends recording, at most once every few seconds however fast
// the page polls.
func (s *Server) renewLease() {
	s.mu.Lock()
	due := !s.closed && time.Since(s.lastRenew) > pulse.DefaultLeaseTTL/6
	if due {
		s.lastRenew = time.Now()
	}
	s.mu.Unlock()
	if due {
		_ = pulse.RenewLease(s.opts.Root, s.opts.Holder, pulse.DefaultLeaseTTL)
	}
}

func secureHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
}

// hostOK accepts only the exact authority the server is bound to. A page on
// another origin that rebinds its DNS name to 127.0.0.1 still sends its own
// name in Host, and is refused here.
func (s *Server) hostOK(r *http.Request) bool { return r.Host == s.host }

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	if r.URL.Path != "/" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if !s.hostOK(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.page)
}

// guard enforces method, Host and token on the API, and counts a valid call
// as "a browser is watching", which is what keeps the lease alive.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secureHeaders(w)
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		got := r.Header.Get(tokenHeader)
		if !s.hostOK(r) || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.renewLease()
		next(w, r)
	}
}

type instanceRow struct {
	pulse.Meta
	Alive bool `json:"alive"`
}

func (s *Server) handleInstances(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	metas := pulse.ListInstances(s.opts.Root)
	rows := make([]instanceRow, 0, len(metas))
	for _, m := range metas {
		rows = append(rows, instanceRow{Meta: m, Alive: m.Alive(now)})
	}
	writeJSON(w, map[string]interface{}{"instances": rows, "now": now})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, _ := strconv.ParseUint(q.Get("since"), 10, 64)
	// The instance value is only compared against enumerated spool
	// directories inside ReadSince; no path is ever built from it.
	events, next, err := pulse.ReadSince(s.opts.Root, q.Get("instance"), since, maxEventsPerPoll)
	if err != nil {
		http.Error(w, "unknown instance", http.StatusNotFound)
		return
	}
	if events == nil {
		events = []pulse.Event{}
	}
	writeJSON(w, map[string]interface{}{"events": events, "next": next})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
