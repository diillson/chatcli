/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/devin"
	"go.uber.org/zap"
)

func TestParseDevinArgsJSON(t *testing.T) {
	args, err := parseDevinArgs([]string{`{"cmd":"run","prompt":"fix it","tags":["a","b"],"mode":"fast","files":["./spec.md"]}`})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if args.Cmd != "run" || args.Prompt != "fix it" || args.Mode != "fast" {
		t.Fatalf("parsed = %+v", args)
	}
	if len(args.Tags) != 2 || len(args.Files) != 1 {
		t.Fatalf("lists misparsed: %+v", args)
	}
}

func TestParseDevinArgsEnvelope(t *testing.T) {
	args, err := parseDevinArgs([]string{`{"cmd":"message","args":{"session":"devin-1","message":"go on"}}`})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if args.Cmd != "message" || args.Session != "devin-1" || args.Message != "go on" {
		t.Fatalf("parsed = %+v", args)
	}
}

func TestParseDevinArgsArgv(t *testing.T) {
	args, err := parseDevinArgs([]string{"status", "--session", "devin-9"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if args.Cmd != "status" || args.Session != "devin-9" {
		t.Fatalf("parsed = %+v", args)
	}
	args, err = parseDevinArgs([]string{"list", "--limit", "7", "--tags", "a,b"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if args.Limit != 7 || len(args.Tags) != 2 {
		t.Fatalf("parsed = %+v", args)
	}
	if _, err := parseDevinArgs([]string{"--session", "x"}); err == nil {
		t.Fatal("missing cmd must error")
	}
}

func TestDevinIsReadOnly(t *testing.T) {
	p := NewBuiltinDevinPlugin()
	readOnly := [][]string{
		{`{"cmd":"status","session":"x"}`},
		{`{"cmd":"list"}`},
		{`{"cmd":"messages","session":"x"}`},
		{`{"cmd":"wait","session":"x"}`},
		{`{"cmd":"info"}`},
		{`{"cmd":"secrets","action":"list"}`},
		{`{"cmd":"knowledge"}`},
		{`{"cmd":"playbooks","action":"get","id":"pb-1"}`},
	}
	for _, args := range readOnly {
		if !p.IsReadOnly(args) {
			t.Errorf("IsReadOnly(%v) = false, want true", args)
		}
	}
	mutating := [][]string{
		{`{"cmd":"run","prompt":"x"}`},
		{`{"cmd":"message","session":"x","message":"y"}`},
		{`{"cmd":"terminate","session":"x"}`},
		{`{"cmd":"secrets","action":"create","key":"k","value":"v"}`},
		{`{"cmd":"knowledge","action":"delete","id":"k-1"}`},
		{`{"cmd":"attach","files":["f"]}`},
	}
	for _, args := range mutating {
		if p.IsReadOnly(args) {
			t.Errorf("IsReadOnly(%v) = true, want false", args)
		}
	}
}

// withFakeDevinServer points devinNewAPI at an httptest server (v1 flavor)
// for the duration of the test.
func withFakeDevinServer(t *testing.T, handler http.Handler) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := devinNewAPI
	devinNewAPI = func() (devin.API, error) {
		return devin.NewAPI(devin.APIConfig{APIKey: "apk_test", BaseURL: srv.URL, Logger: zap.NewNop()})
	}
	t.Cleanup(func() { devinNewAPI = orig })
}

func TestDevinExecuteRunAndStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["prompt"] != "fix bug" {
			t.Errorf("prompt = %v", payload["prompt"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "sess-42", "url": "https://app/sess-42"})
	})
	mux.HandleFunc("GET /v1/sessions/sess-42", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"session_id":"sess-42","status_enum":"working","created_at":"2026-07-08T10:00:00Z","updated_at":"2026-07-08T10:00:00Z","tags":["chatcli"]}`))
	})
	withFakeDevinServer(t, mux)

	p := NewBuiltinDevinPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"run","prompt":"fix bug"}`})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "sess-42") {
		t.Fatalf("run output must carry the session id: %q", out)
	}

	out, err = p.Execute(context.Background(), []string{"status", "--session", "sess-42"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "sess-42") || !strings.Contains(out, "working") {
		t.Fatalf("status output = %q", out)
	}
}

func TestDevinExecuteInfoAndErrors(t *testing.T) {
	withFakeDevinServer(t, http.NewServeMux())
	p := NewBuiltinDevinPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"info"}`})
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if !strings.Contains(out, "v1") {
		t.Fatalf("info must report the generation: %q", out)
	}

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"status"}`}); err == nil {
		t.Fatal("status without session must error")
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"bogus"}`}); err == nil {
		t.Fatal("unknown cmd must error")
	}
}

func TestDevinNotConfigured(t *testing.T) {
	orig := devinNewAPI
	devinNewAPI = func() (devin.API, error) {
		return devin.NewAPI(devin.APIConfig{Logger: zap.NewNop()})
	}
	t.Cleanup(func() { devinNewAPI = orig })

	p := NewBuiltinDevinPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Fatal("missing credentials must surface a setup error")
	}
}
