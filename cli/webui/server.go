/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

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
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/imagegen"
	"github.com/diillson/chatcli/llm/transcription"
	"github.com/diillson/chatcli/llm/tts"
)

const (
	tokenHeader       = "X-Web-Token" // #nosec G101 -- the header name that carries the token, not a credential
	readHeaderTimeout = 10 * time.Second
	// maxJSONBody bounds ordinary requests; maxMediaBody bounds audio and
	// image uploads.
	maxJSONBody  = 8 << 20
	maxMediaBody = 32 << 20
	// heartbeat keeps a quiet stream alive through proxies and lets the page
	// tell a slow model from a dead server.
	heartbeat = 15 * time.Second
	// defaultPermissionTimeout is how long a permission dialog waits for the
	// browser before the action is denied once.
	defaultPermissionTimeout = 10 * time.Minute
	bootPlaceholder          = "__CHATCLI_WEB_BOOT__"
)

// assets holds the application page. A single file, no build step, no
// external resources: the strict CSP forbids them and the UI must work
// offline next to the terminal.
//
//go:embed assets/app.html
var assets embed.FS

// Page returns the bundled application HTML.
func Page() []byte {
	raw, err := assets.ReadFile("assets/app.html")
	if err != nil {
		return nil
	}
	return raw
}

// Options configure a web server.
type Options struct {
	Backend Backend
	// Page is the application HTML; the boot placeholder in it is replaced
	// with the boot JSON. Nil serves the bundled page.
	Page []byte
	// Addr is the listen address; empty binds 127.0.0.1 on a free port.
	Addr string
	// Lang and Strings are the UI language and its translated strings.
	Lang    string
	Strings map[string]string
	// ThemeName and ThemeVars describe the process theme for the page.
	ThemeName string
	ThemeVars map[string]string
	Version   string
	// Voice, STT and Images are optional media providers; a null provider
	// disables the matching feature on the page.
	Voice  tts.Provider
	STT    transcription.Provider
	Images imagegen.Provider
	// PermissionTimeout bounds each permission dialog; 0 uses the default.
	PermissionTimeout time.Duration
	Logger            *zap.Logger
}

// Server is a running web UI.
type Server struct {
	opts  Options
	token string
	host  string
	url   string
	page  []byte
	srv   *http.Server
	log   *zap.Logger

	mu     sync.Mutex
	active *run
	closed bool
}

type run struct {
	id     string
	sink   *runSink
	cancel context.CancelFunc
	done   chan struct{}
}

// Start binds the server and begins serving. Only loopback addresses are
// accepted: the token guards the API, the address guards the network.
func Start(opts Options) (*Server, error) {
	if opts.Backend == nil {
		return nil, fmt.Errorf("webui: backend is required")
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.PermissionTimeout <= 0 {
		opts.PermissionTimeout = defaultPermissionTimeout
	}
	if len(opts.Page) == 0 {
		opts.Page = Page()
	}
	if opts.Strings == nil {
		opts.Strings = UIStrings()
	}
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	if host, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("webui: invalid address %q: %w", addr, err)
	} else if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("webui: %s", i18n.T("web.loopback_only", addr))
	}

	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, fmt.Errorf("webui: minting token: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("webui: listening: %w", err)
	}

	s := &Server{opts: opts, token: hex.EncodeToString(tok[:]), host: ln.Addr().String(), log: opts.Logger}
	s.url = "http://" + s.host + "/?t=" + s.token
	s.page = s.renderPage()

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/", s.guard(s.routeAPI))
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// URL is the address the browser opens, token included.
func (s *Server) URL() string { return s.url }

// Host is the bound host:port.
func (s *Server) Host() string { return s.host }

// Token authenticates API calls (the X-Web-Token header).
func (s *Server) Token() string { return s.token }

// Shutdown cancels a running turn and stops serving.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	r := s.active
	s.mu.Unlock()
	if r != nil {
		r.cancel()
	}
	return s.srv.Shutdown(ctx)
}

func secureHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
}

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

// guard authenticates every API call: exact Host (DNS rebinding) and the
// per-run token in a header. The token is never read from the query so it
// does not land in logs.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secureHeaders(w)
		got := r.Header.Get(tokenHeader)
		if !s.hostOK(r) || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// boot is what the page receives at load.
type boot struct {
	Lang      string            `json:"lang"`
	Strings   map[string]string `json:"strings"`
	ThemeName string            `json:"theme"`
	ThemeVars map[string]string `json:"theme_vars,omitempty"`
	Version   string            `json:"version"`
	Token     string            `json:"token"`
	Features  features          `json:"features"`
}

type features struct {
	Voice  bool `json:"voice"`
	STT    bool `json:"stt"`
	Images bool `json:"images"`
	LLM    bool `json:"llm"`
}

func (s *Server) features() features {
	o := s.opts
	return features{
		Voice:  o.Voice != nil && !tts.IsNull(o.Voice),
		STT:    o.STT != nil && !transcription.IsNull(o.STT),
		Images: o.Images != nil && !imagegen.IsNull(o.Images),
		LLM:    o.Backend.HasLLM(),
	}
}

func (s *Server) bootData() boot {
	lang := s.opts.Lang
	if lang == "" {
		lang = "en"
	}
	return boot{Lang: lang, Strings: s.opts.Strings, ThemeName: s.opts.ThemeName, ThemeVars: s.opts.ThemeVars, Version: s.opts.Version, Token: s.token, Features: s.features()}
}

func (s *Server) renderPage() []byte {
	payload, _ := json.Marshal(s.bootData())
	if len(s.opts.Page) == 0 {
		return []byte("<!doctype html><meta charset=\"utf-8\"><title>ChatCLI</title><body style=\"font-family:system-ui;padding:2rem\"><h1>ChatCLI web</h1><p>" +
			i18n.T("web.page_missing") + "</p><script id=\"boot\" type=\"application/json\">" + string(payload) + "</script></body>")
	}
	return []byte(strings.Replace(string(s.opts.Page), bootPlaceholder, string(payload), 1))
}
