package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestParseTelegramUpdates(t *testing.T) {
	body := []byte(`{"ok":true,"result":[
		{"update_id":10,"message":{"chat":{"id":555},"from":{"id":7,"username":"neo"},"text":"hi"}},
		{"update_id":11,"message":{"chat":{"id":555},"from":{"id":7},"text":""}},
		{"update_id":12,"message":{"chat":{"id":556},"from":{"id":8,"first_name":"Trinity"},"text":"yo"}}
	]}`)
	msgs, maxID, err := parseTelegramUpdates(body)
	if err != nil {
		t.Fatal(err)
	}
	if maxID != 12 {
		t.Errorf("maxID = %d, want 12", maxID)
	}
	if len(msgs) != 2 { // empty-text update skipped
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].ChatID != "555" || msgs[0].UserID != "7" || msgs[0].UserName != "neo" || msgs[0].Text != "hi" {
		t.Errorf("msg0 wrong: %+v", msgs[0])
	}
	if msgs[1].UserName != "Trinity" { // falls back to first_name
		t.Errorf("expected first_name fallback, got %q", msgs[1].UserName)
	}
}

func TestParseTelegramUpdates_NotOK(t *testing.T) {
	if _, _, err := parseTelegramUpdates([]byte(`{"ok":false}`)); err == nil {
		t.Error("expected error when ok=false")
	}
}

func TestTelegramPermitted(t *testing.T) {
	open := NewTelegramAdapter("tok", nil, zap.NewNop())
	if !open.permitted("anyone") {
		t.Error("empty allowlist should permit all")
	}
	gated := NewTelegramAdapter("tok", []string{"7"}, zap.NewNop())
	if !gated.permitted("7") || gated.permitted("8") {
		t.Error("allowlist gating wrong")
	}
}

func TestTelegramSend(t *testing.T) {
	var gotChat, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/sendMessage") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(b, &payload)
		gotChat, _ = payload["chat_id"].(string)
		gotText, _ = payload["text"].(string)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	a := NewTelegramAdapter("tok", nil, zap.NewNop())
	a.baseURL = srv.URL
	if err := a.Send(context.Background(), OutboundMessage{ChatID: "555", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if gotChat != "555" || gotText != "hello" {
		t.Errorf("send payload wrong: chat=%q text=%q", gotChat, gotText)
	}
}

func TestTelegramPollViaHTTPTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":1,"message":{"chat":{"id":9},"from":{"id":2,"username":"a"},"text":"ping"}}]}`))
	}))
	defer srv.Close()

	a := NewTelegramAdapter("tok", nil, zap.NewNop())
	a.baseURL = srv.URL
	msgs, maxID, err := a.poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text != "ping" || maxID != 1 {
		t.Errorf("poll result wrong: %+v maxID=%d", msgs, maxID)
	}
}
