/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package devin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name string
		cfg  APIConfig
		want string
	}{
		{"explicit v1 wins over cog", APIConfig{APIKey: "cog_x", OrgID: "org-1", Version: "v1"}, "v1"},
		{"explicit v3", APIConfig{APIKey: "apk_x", Version: "V3"}, "v3"},
		{"auto cog with org", APIConfig{APIKey: "cog_x", OrgID: "org-1"}, "v3"},
		{"auto cog without org falls back to v1", APIConfig{APIKey: "cog_x"}, "v1"},
		{"auto personal key", APIConfig{APIKey: "apk_user_x", OrgID: "org-1"}, "v1"},
		{"auto service key", APIConfig{APIKey: "apk_x"}, "v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ResolveVersion(); got != tc.want {
				t.Fatalf("ResolveVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewAPIValidation(t *testing.T) {
	if _, err := NewAPI(APIConfig{}); err == nil {
		t.Fatal("expected error for missing API key")
	}
	if _, err := NewAPI(APIConfig{APIKey: "cog_x", Version: "v3"}); err == nil {
		t.Fatal("expected error for v3 without org id")
	}
	api, err := NewAPI(APIConfig{APIKey: "apk_x", Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("v1 config should be valid: %v", err)
	}
	if api.Version() != "v1" {
		t.Fatalf("Version() = %q, want v1", api.Version())
	}
	api, err = NewAPI(APIConfig{APIKey: "cog_x", OrgID: "org-1", Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("v3 config should be valid: %v", err)
	}
	if api.Version() != "v3" {
		t.Fatalf("Version() = %q, want v3", api.Version())
	}
}

func TestStateMapping(t *testing.T) {
	if got := v1MapState("blocked"); got != StateBlocked {
		t.Fatalf("v1 blocked → %s", got)
	}
	if got := v1MapState("finished"); got != StateFinished {
		t.Fatalf("v1 finished → %s", got)
	}
	if got := v1MapState("resume_requested"); got != StateWorking {
		t.Fatalf("v1 resume_requested → %s", got)
	}
	if got := v3MapState("running", "waiting_for_user"); got != StateBlocked {
		t.Fatalf("v3 waiting_for_user → %s", got)
	}
	if got := v3MapState("suspended", "finished"); got != StateFinished {
		t.Fatalf("v3 detail finished → %s", got)
	}
	if got := v3MapState("exit", ""); got != StateFinished {
		t.Fatalf("v3 exit → %s", got)
	}
	if got := v3MapState("error", ""); got != StateError {
		t.Fatalf("v3 error → %s", got)
	}
	if !StateBlocked.TurnBoundary() || StateWorking.TurnBoundary() {
		t.Fatal("TurnBoundary misclassifies")
	}
	if !StateFinished.Terminal() || StateBlocked.Terminal() {
		t.Fatal("Terminal misclassifies")
	}
}

// newV1TestAPI spins an httptest server and a v1 client pointed at it.
func newV1TestAPI(t *testing.T, handler http.Handler) API {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	api, err := NewAPI(APIConfig{APIKey: "apk_test", BaseURL: srv.URL, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("NewAPI: %v", err)
	}
	return api
}

// newV3TestAPI spins an httptest server and a v3 client pointed at it.
func newV3TestAPI(t *testing.T, handler http.Handler) API {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	api, err := NewAPI(APIConfig{APIKey: "cog_test", OrgID: "org-1", BaseURL: srv.URL, Logger: zap.NewNop()})
	if err != nil {
		t.Fatalf("NewAPI: %v", err)
	}
	return api
}

func TestV1SessionLifecycle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer apk_test" {
			t.Errorf("Authorization = %q", got)
		}
		// The client must send an identifiable User-Agent so an AWS-WAF edge
		// does not 403 the request as a default Go client.
		if ua := r.Header.Get("User-Agent"); ua == "" || strings.HasPrefix(ua, "Go-http-client") {
			t.Errorf("User-Agent = %q, want a non-default identifier", ua)
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		prompt, _ := payload["prompt"].(string)
		if !strings.Contains(prompt, `ATTACHMENT:"https://files/x"`) {
			t.Errorf("v1 create must inline attachments in the prompt, got %q", prompt)
		}
		if payload["max_acu_limit"] != float64(50) {
			t.Errorf("max_acu_limit = %v", payload["max_acu_limit"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "sess-1", "url": "https://app/sess-1"})
	})
	mux.HandleFunc("GET /v1/sessions/sess-1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"session_id":"sess-1","status":"blocked","status_enum":"blocked",
			"created_at":"2026-07-08T10:00:00Z","updated_at":"2026-07-08T10:05:00Z",
			"title":"T","tags":["chatcli"],
			"messages":[
				{"type":"user_message","message":"do it","timestamp":"2026-07-08T10:00:00Z"},
				{"type":"devin_message","message":"done, PR open","timestamp":"2026-07-08T10:04:00Z"}
			],
			"pull_request":{"url":"https://github.com/x/pull/1"},
			"structured_output":{"ok":true}
		}`))
	})
	mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("limit"); got != "5" {
			t.Errorf("limit = %q", got)
		}
		_, _ = w.Write([]byte(`{"sessions":[{"session_id":"sess-1","status_enum":"working","created_at":"2026-07-08T10:00:00Z","updated_at":"2026-07-08T10:00:00Z"}]}`))
	})
	mux.HandleFunc("POST /v1/sessions/sess-1/message", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["message"] != "keep going" {
			t.Errorf("message = %v", payload["message"])
		}
		_, _ = w.Write([]byte(`null`))
	})
	mux.HandleFunc("PUT /v1/sessions/sess-1/tags", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"detail":"ok"}`))
	})
	mux.HandleFunc("DELETE /v1/sessions/sess-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	api := newV1TestAPI(t, mux)
	ctx := context.Background()

	created, err := api.CreateSession(ctx, CreateSessionRequest{
		Prompt:         "do it",
		AttachmentURLs: []string{"https://files/x"},
		MaxACULimit:    50,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.ID != "sess-1" || created.URL != "https://app/sess-1" {
		t.Fatalf("created = %+v", created)
	}

	got, err := api.GetSession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.State != StateBlocked {
		t.Fatalf("State = %s, want blocked", got.State)
	}
	if len(got.Messages) != 2 || got.Messages[0].FromUser == got.Messages[1].FromUser {
		t.Fatalf("messages misparsed: %+v", got.Messages)
	}
	if len(got.PullRequests) != 1 || got.PullRequests[0].URL != "https://github.com/x/pull/1" {
		t.Fatalf("pull requests misparsed: %+v", got.PullRequests)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not parsed from RFC3339")
	}

	list, err := api.ListSessions(ctx, ListSessionsOptions{Limit: 5})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessions: %v %+v", err, list)
	}
	if err := api.SendMessage(ctx, "sess-1", "keep going", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if err := api.SetSessionTags(ctx, "sess-1", []string{"a"}); err != nil {
		t.Fatalf("SetSessionTags: %v", err)
	}
	if err := api.TerminateSession(ctx, "sess-1"); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}
	if err := api.ArchiveSession(ctx, "sess-1"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("v1 archive must be ErrNotSupported, got %v", err)
	}
}

func TestV1ResourceCRUD(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/secrets", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"sec-1","type":"key-value","key":"TOKEN","created_at":"2026-07-08T10:00:00Z"}]`))
	})
	mux.HandleFunc("POST /v1/secrets", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"sec-2"}`))
	})
	mux.HandleFunc("DELETE /v1/secrets/sec-1", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /v1/knowledge", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"knowledge":[{"id":"k-1","name":"Deploy","body":"…","trigger_description":"deploys","created_at":"2026-07-08T10:00:00Z"}],"folders":[]}`))
	})
	mux.HandleFunc("POST /v1/knowledge", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["trigger_description"] != "when x" {
			t.Errorf("trigger_description = %v", payload["trigger_description"])
		}
		_, _ = w.Write([]byte(`{"id":"k-2","name":"N","body":"B","trigger_description":"when x"}`))
	})
	mux.HandleFunc("GET /v1/playbooks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"playbook_id":"pb-1","title":"Review","body":"…"}]`))
	})
	mux.HandleFunc("POST /v1/attachments", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`"https://api.devin.ai/attachments/abc"`))
	})

	api := newV1TestAPI(t, mux)
	ctx := context.Background()

	secrets, err := api.ListSecrets(ctx)
	if err != nil || len(secrets) != 1 || secrets[0].Key != "TOKEN" {
		t.Fatalf("ListSecrets: %v %+v", err, secrets)
	}
	created, err := api.CreateSecret(ctx, CreateSecretRequest{Type: "key-value", Key: "K", Value: "V", Sensitive: true, Note: "n"})
	if err != nil || created.ID != "sec-2" {
		t.Fatalf("CreateSecret: %v %+v", err, created)
	}
	if err := api.DeleteSecret(ctx, "sec-1"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}

	notes, err := api.ListKnowledge(ctx)
	if err != nil || len(notes) != 1 || notes[0].Trigger != "deploys" {
		t.Fatalf("ListKnowledge: %v %+v", err, notes)
	}
	note, err := api.CreateKnowledge(ctx, KnowledgeNoteRequest{Name: "N", Body: "B", Trigger: "when x"})
	if err != nil || note.ID != "k-2" {
		t.Fatalf("CreateKnowledge: %v %+v", err, note)
	}

	playbooks, err := api.ListPlaybooks(ctx)
	if err != nil || len(playbooks) != 1 || playbooks[0].ID != "pb-1" {
		t.Fatalf("ListPlaybooks: %v %+v", err, playbooks)
	}

	att, err := api.UploadAttachment(ctx, "spec.md", strings.NewReader("hello"))
	if err != nil || att.URL != "https://api.devin.ai/attachments/abc" {
		t.Fatalf("UploadAttachment: %v %+v", err, att)
	}
}

func TestV3SessionLifecycle(t *testing.T) {
	const base = "/v3/organizations/org-1"
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+base+"/sessions", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cog_test" {
			t.Errorf("Authorization = %q", got)
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["devin_mode"] != "fast" {
			t.Errorf("devin_mode = %v", payload["devin_mode"])
		}
		if urls, _ := payload["attachment_urls"].([]any); len(urls) != 1 {
			t.Errorf("attachment_urls = %v", payload["attachment_urls"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": "devin-1", "url": "https://app.devin.ai/sessions/devin-1",
			"status": "new", "org_id": "org-1", "created_at": 1751970000, "updated_at": 1751970000,
			"acus_consumed": 0, "tags": []string{}, "pull_requests": []any{},
		})
	})
	mux.HandleFunc("GET "+base+"/sessions/devin-1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"session_id":"devin-1","url":"https://app.devin.ai/sessions/devin-1",
			"status":"running","status_detail":"waiting_for_user","tags":["x"],
			"org_id":"org-1","created_at":1751970000,"updated_at":1751970300,
			"acus_consumed":3.5,
			"pull_requests":[{"pr_url":"https://github.com/x/pull/2","pr_state":"open"}],
			"structured_output":null,"title":"T","is_archived":false
		}`))
	})
	pages := 0
	mux.HandleFunc("GET "+base+"/sessions/devin-1/messages", func(w http.ResponseWriter, r *http.Request) {
		pages++
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write([]byte(`{"items":[{"event_id":"e1","source":"user","message":"do it","created_at":1751970000}],"end_cursor":"c1","has_next_page":true,"total":2}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"event_id":"e2","source":"devin","message":"working on it","created_at":1751970060}],"end_cursor":"","has_next_page":false,"total":2}`))
	})
	mux.HandleFunc("POST "+base+"/sessions/devin-1/messages", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["message"] != "more" {
			t.Errorf("message = %v", payload["message"])
		}
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST "+base+"/sessions/devin-1/archive", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET "+base+"/sessions", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("first"); got != "7" {
			t.Errorf("first = %q", got)
		}
		_, _ = w.Write([]byte(`{"items":[{"session_id":"devin-1","status":"running","created_at":1,"updated_at":2,"acus_consumed":0,"tags":[],"pull_requests":[]}],"end_cursor":"","has_next_page":false,"total":1}`))
	})

	api := newV3TestAPI(t, mux)
	ctx := context.Background()

	created, err := api.CreateSession(ctx, CreateSessionRequest{Prompt: "p", Mode: "fast", AttachmentURLs: []string{"https://f/x"}})
	if err != nil || created.ID != "devin-1" {
		t.Fatalf("CreateSession: %v %+v", err, created)
	}

	got, err := api.GetSession(ctx, "devin-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.State != StateBlocked {
		t.Fatalf("State = %s, want blocked (waiting_for_user)", got.State)
	}
	if got.ACUsConsumed != 3.5 || len(got.PullRequests) != 1 || got.PullRequests[0].State != "open" {
		t.Fatalf("session misparsed: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not parsed from unix seconds")
	}

	messages, err := api.ListMessages(ctx, "devin-1")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(messages) != 2 || pages != 2 {
		t.Fatalf("pagination: %d messages across %d pages", len(messages), pages)
	}
	if messages[0].FromUser != true || messages[1].FromUser != false {
		t.Fatalf("source mapping wrong: %+v", messages)
	}

	if err := api.SendMessage(ctx, "devin-1", "more", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if err := api.ArchiveSession(ctx, "devin-1"); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	list, err := api.ListSessions(ctx, ListSessionsOptions{Limit: 7})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessions: %v %+v", err, list)
	}
}

func TestV3ResourceCRUD(t *testing.T) {
	const base = "/v3/organizations/org-1"
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+base+"/secrets", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["is_sensitive"] != true {
			t.Errorf("is_sensitive = %v", payload["is_sensitive"])
		}
		_, _ = w.Write([]byte(`{"secret_id":"sec-9","key":"K","secret_type":"key-value","created_at":1751970000}`))
	})
	mux.HandleFunc("GET "+base+"/secrets", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"secret_id":"sec-9","key":"K","secret_type":"key-value","created_at":1751970000}]}`))
	})
	mux.HandleFunc("GET "+base+"/knowledge/notes", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"note_id":"note-1","name":"Deploy","body":"…","trigger":"deploys","created_at":1751970000}]`))
	})
	mux.HandleFunc("POST "+base+"/knowledge/notes", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["trigger"] != "when x" {
			t.Errorf("trigger = %v", payload["trigger"])
		}
		_, _ = w.Write([]byte(`{"note_id":"note-2","name":"N","body":"B","trigger":"when x"}`))
	})
	mux.HandleFunc("GET "+base+"/playbooks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"playbook_id":"pb-9","title":"Review","body":"…","created_at":1751970000,"updated_at":1751970000}]}`))
	})
	mux.HandleFunc("POST "+base+"/attachments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"attachment_id":"att-1","name":"spec.md","url":"https://files/att-1"}`))
	})

	api := newV3TestAPI(t, mux)
	ctx := context.Background()

	secret, err := api.CreateSecret(ctx, CreateSecretRequest{Type: "key-value", Key: "K", Value: "V", Sensitive: true})
	if err != nil || secret.ID != "sec-9" {
		t.Fatalf("CreateSecret: %v %+v", err, secret)
	}
	secrets, err := api.ListSecrets(ctx)
	if err != nil || len(secrets) != 1 {
		t.Fatalf("ListSecrets (items wrapper): %v %+v", err, secrets)
	}
	notes, err := api.ListKnowledge(ctx)
	if err != nil || len(notes) != 1 || notes[0].ID != "note-1" {
		t.Fatalf("ListKnowledge (bare array): %v %+v", err, notes)
	}
	note, err := api.CreateKnowledge(ctx, KnowledgeNoteRequest{Name: "N", Body: "B", Trigger: "when x"})
	if err != nil || note.ID != "note-2" {
		t.Fatalf("CreateKnowledge: %v %+v", err, note)
	}
	playbooks, err := api.ListPlaybooks(ctx)
	if err != nil || len(playbooks) != 1 || playbooks[0].ID != "pb-9" {
		t.Fatalf("ListPlaybooks: %v %+v", err, playbooks)
	}
	att, err := api.UploadAttachment(ctx, "spec.md", strings.NewReader("hi"))
	if err != nil || att.ID != "att-1" || att.URL != "https://files/att-1" {
		t.Fatalf("UploadAttachment: %v %+v", err, att)
	}
}

func TestAPIErrorSurface(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Session not found"}`))
	})
	api := newV1TestAPI(t, mux)
	_, err := api.GetSession(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 APIError, got %v", err)
	}
	if strings.Contains(fmt.Sprintf("%v", err), "apk_test") {
		t.Fatal("error must never echo the credential")
	}
}
