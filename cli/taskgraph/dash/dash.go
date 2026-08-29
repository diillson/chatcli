/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package dash serves the live task-graph dashboard: a single-file canvas
 * UI (embedded, zero CDN) over three read-only endpoints that read the
 * persisted run state from disk on every request. The engine keeps writing
 * state.json/events.ndjson; this server only ever reads — killing the
 * dashboard never affects a run, and finished runs render just as well.
 *
 * Bound to 127.0.0.1 on an ephemeral port (no configuration, no new envs);
 * the caller receives the URL to print/open.
 */
package dash

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/taskgraph"
)

//go:embed assets/dash.html
var dashHTML []byte

// readHeaderTimeout bounds header reads on the local listener.
const readHeaderTimeout = 10 * time.Second

// Server is one running dashboard over a taskgraph base directory.
type Server struct {
	baseDir string
	srv     *http.Server
	url     string

	mu     sync.Mutex
	closed bool
}

// Start binds 127.0.0.1:0 and serves the dashboard for baseDir. The caller
// owns Shutdown.
func Start(baseDir string) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("dashboard listen: %w", err)
	}
	s := &Server{baseDir: baseDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/events", s.handleEvents)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	s.url = "http://" + ln.Addr().String() + "/"
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// URL returns the dashboard's address.
func (s *Server) URL() string { return s.url }

// Shutdown stops the server. Idempotent.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(dashHTML)
}

// handleRuns lists persisted runs, newest first.
func (s *Server) handleRuns(w http.ResponseWriter, _ *http.Request) {
	rows, err := taskgraph.ListRuns(s.baseDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type runRow struct {
		RunID  string `json:"run_id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	out := make([]runRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, runRow{RunID: r.RunID, Name: r.Name, Status: string(r.Status)})
	}
	writeJSON(w, out)
}

// resolveRun maps the run parameter to a store (default: latest run). The
// requested id is only ever COMPARED against ids enumerated from disk —
// no path is built from request input, so a hostile "run" value can at
// most fail to match.
func (s *Server) resolveRun(r *http.Request) (*taskgraph.RunStore, error) {
	want := r.URL.Query().Get("run")
	rows, err := taskgraph.ListRuns(s.baseDir)
	if err != nil || len(rows) == 0 {
		return nil, fmt.Errorf("no task graph runs")
	}
	for _, row := range rows {
		if want == "" || row.RunID == want {
			return taskgraph.OpenRun(s.baseDir, row.RunID)
		}
	}
	return nil, fmt.Errorf("no task graph run %q", want)
}

// handleState returns the run's persisted graph verbatim.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	store, err := s.resolveRun(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	g, err := store.LoadState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, g)
}

// handleEvents returns event lines after the "since" line index, plus the
// next index — the incremental feed the page polls.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	store, err := s.resolveRun(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	if since < 0 {
		since = 0
	}
	data, err := os.ReadFile(store.EventsPath()) // path derives from disk-enumerated run ids, never from the request
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	events := make([]json.RawMessage, 0)
	next := since
	for i, line := range lines {
		if i < since || len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if json.Valid(line) {
			events = append(events, json.RawMessage(line))
		}
		next = i + 1
	}
	writeJSON(w, map[string]interface{}{"events": events, "next": next})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
