/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// countingServer answers every request with a small body and counts hits,
// standing in for a platform's media host.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("IMG"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// cancelledCtx is already done, so a Start that wrongly proceeds returns
// instead of serving forever.
func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// assertNothingQueued fails when a message reached the inbound channel.
func assertNothingQueued(t *testing.T, ch chan InboundMessage) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("a refused message reached the agent: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// agentRecorder is an AgentFunc that records who reached it.
type agentRecorder struct {
	mu    sync.Mutex
	users []string
}

func (a *agentRecorder) fn(ctx context.Context, _ string, _ string) (string, error) {
	m, _ := InboundFromContext(ctx)
	a.mu.Lock()
	a.users = append(a.users, m.UserID)
	a.mu.Unlock()
	return "", nil
}

func (a *agentRecorder) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.users...)
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// runRunner runs adapters behind a Runner until the test ends.
func runRunner(t *testing.T, rec *agentRecorder, adapters ...Adapter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	r := NewRunner(adapters, rec.fn, zap.NewNop(), 2)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func slackSignedPost(t *testing.T, url, secret, body string) int {
	t.Helper()
	resp := postSlackSigned(t, url, secret, body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// End to end through the Runner: a Slack message from a sender who is not
// on the allowlist never reaches the agent and its file is never fetched;
// a listed sender's message does.
func TestSlackUnlistedSenderNeverReachesTheAgent(t *testing.T) {
	media, hits := countingServer(t)
	addr := freeAddr(t)
	a := NewSlackAdapter("xoxb", "sig", addr, "/ev", zap.NewNop()).WithAllowedUsers([]string{"UOK"})
	rec := &agentRecorder{}
	runRunner(t, rec, a)
	url := "http://" + addr + "/ev"
	waitFor(t, func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})

	stranger := fmt.Sprintf(`{"type":"event_callback","team_id":"T1","event":{"type":"message","subtype":"file_share","channel":"C1","user":"UBAD","text":"run id","files":[{"mimetype":"image/png","url_private":%q}]}}`, media.URL+"/x.png")
	if code := slackSignedPost(t, url, "sig", stranger); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	listed := `{"type":"event_callback","team_id":"T1","event":{"type":"message","channel":"C1","user":"UOK","text":"hi"}}`
	slackSignedPost(t, url, "sig", listed)

	waitFor(t, func() bool { return len(rec.seen()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := rec.seen(); len(got) != 1 || got[0] != "UOK" {
		t.Fatalf("agent reached by %v, want only UOK", got)
	}
	if hits.Load() != 0 {
		t.Errorf("a stranger's attachment was downloaded %d time(s)", hits.Load())
	}
}

// A Slack entry pinned to a workspace admits the user only from there.
func TestSlackAllowlistTeamPinning(t *testing.T) {
	ch := make(chan InboundMessage, 2)
	a := NewSlackAdapter("xoxb", "sig", ":0", "/ev", zap.NewNop()).WithAllowedUsers([]string{"T1:U1"})
	srv := httptest.NewServer(a.eventsHandler(context.Background(), ch))
	defer srv.Close()

	slackSignedPost(t, srv.URL, "sig", `{"type":"event_callback","team_id":"T2","event":{"type":"message","channel":"C","user":"U1","text":"other team"}}`)
	slackSignedPost(t, srv.URL, "sig", `{"type":"event_callback","team_id":"T1","event":{"type":"message","channel":"C","user":"U1","text":"home"}}`)
	m := recvWithin(t, ch, time.Second)
	if m.Text != "home" || !m.SenderListed {
		t.Fatalf("got %+v, want the home-team message, listed", m)
	}
	assertNothingQueued(t, ch)
}

func whatsappSignedPost(t *testing.T, url, secret, body string) int {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// End to end through the Runner for WhatsApp: the stranger's image is never
// looked up and the agent only sees the listed number.
func TestWhatsAppUnlistedSenderNeverReachesTheAgent(t *testing.T) {
	graph, hits := countingServer(t)
	addr := freeAddr(t)
	a := NewWhatsAppAdapter("tok", "ph", "v", addr, "/wa", zap.NewNop()).
		WithAppSecret("app").WithAllowedUsers([]string{"+5511900000001"})
	a.graphBase = graph.URL
	rec := &agentRecorder{}
	runRunner(t, rec, a)
	waitFor(t, func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})
	url := "http://" + addr + "/wa"

	stranger := `{"entry":[{"changes":[{"value":{"messages":[{"from":"5511900000009","type":"image","image":{"id":"M1","mime_type":"image/png"}}]}}]}]}`
	whatsappSignedPost(t, url, "app", stranger)
	listed := `{"entry":[{"changes":[{"value":{"messages":[{"from":"5511900000001","type":"text","text":{"body":"oi"}}]}}]}]}`
	whatsappSignedPost(t, url, "app", listed)

	waitFor(t, func() bool { return len(rec.seen()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := rec.seen(); len(got) != 1 || got[0] != "5511900000001" {
		t.Fatalf("agent reached by %v, want only the listed number", got)
	}
	// The Graph API is hit only for replies (the agent here returns no
	// reply), never for the stranger's media lookup.
	if hits.Load() != 0 {
		t.Errorf("the stranger's media was looked up %d time(s)", hits.Load())
	}
}

// discordServer serves a gateway that dispatches the given MESSAGE_CREATE
// payloads after IDENTIFY.
func discordServer(t *testing.T, msgs ...map[string]interface{}) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]interface{}{"op": dOpHello, "d": map[string]int{"heartbeat_interval": 45000}})
		_, _, _ = conn.ReadMessage()
		for i, m := range msgs {
			_ = conn.WriteJSON(map[string]interface{}{"op": dOpDispatch, "s": i + 1, "t": "MESSAGE_CREATE", "d": m})
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// End to end through the Runner for Discord.
func TestDiscordUnlistedSenderNeverReachesTheAgent(t *testing.T) {
	media, hits := countingServer(t)
	gw := discordServer(t,
		map[string]interface{}{"channel_id": "C", "content": "do it", "author": map[string]interface{}{"id": "666"},
			"attachments": []map[string]string{{"url": media.URL + "/a.png", "content_type": "image/png"}}},
		map[string]interface{}{"channel_id": "C", "content": "hi", "author": map[string]interface{}{"id": "42"}},
	)
	d := NewDiscordAdapter("tok", zap.NewNop()).WithAllowedUsers([]string{"42"})
	d.gatewayURL = "ws" + strings.TrimPrefix(gw.URL, "http")
	rec := &agentRecorder{}
	runRunner(t, rec, d)

	waitFor(t, func() bool { return len(rec.seen()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := rec.seen(); len(got) != 1 || got[0] != "42" {
		t.Fatalf("agent reached by %v, want only 42", got)
	}
	if hits.Load() != 0 {
		t.Errorf("a stranger's attachment was downloaded %d time(s)", hits.Load())
	}
}

// Telegram drops a stranger's update before resolving its voice note.
func TestTelegramUnlistedSenderMediaNeverDownloaded(t *testing.T) {
	var getFile atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") || strings.Contains(r.URL.Path, "/file/") {
			getFile.Add(1)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":5,"message":{"chat":{"id":1},"from":{"id":99},"voice":{"file_id":"F1","mime_type":"audio/ogg"}}}]}`))
	}))
	defer api.Close()
	a := NewTelegramAdapter("tok", []string{"7"}, zap.NewNop())
	a.baseURL = api.URL
	msgs, maxID, err := a.poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 || maxID != 5 {
		t.Fatalf("stranger admitted: %+v (maxID %d)", msgs, maxID)
	}
	if getFile.Load() != 0 {
		t.Errorf("the stranger's voice note was resolved %d time(s)", getFile.Load())
	}
}

// Every chat adapter refuses to start with an empty allowlist, naming the
// variable to set; Preflight and the Runner refuse before serving anything.
func TestAdaptersRefuseToStartWithoutAllowlist(t *testing.T) {
	cases := []struct {
		a   Adapter
		env string
	}{
		{NewTelegramAdapter("tok", nil, zap.NewNop()), "CHATCLI_TELEGRAM_ALLOWED_USERS"},
		{NewSlackAdapter("xoxb", "sig", "127.0.0.1:0", "", zap.NewNop()), "CHATCLI_SLACK_ALLOWED_USERS"},
		{NewWhatsAppAdapter("tok", "ph", "v", "127.0.0.1:0", "", zap.NewNop()).WithAppSecret("a"), "CHATCLI_WHATSAPP_ALLOWED_USERS"},
		{NewDiscordAdapter("tok", zap.NewNop()), "CHATCLI_DISCORD_ALLOWED_USERS"},
		{NewDiscordAdapter("tok", zap.NewNop()).WithAllowedUsers([]string{" ", ""}), "CHATCLI_DISCORD_ALLOWED_USERS"},
	}
	for _, c := range cases {
		// A cancelled context: an adapter that wrongly starts returns at
		// once with nil instead of blocking the test.
		err := c.a.Start(cancelledCtx(), make(chan InboundMessage))
		if !errors.Is(err, errAllowlistEmpty) || !strings.Contains(err.Error(), c.env) {
			t.Errorf("%s: Start must refuse naming %s, got %v", c.a.Name(), c.env, err)
		}
		if err := Preflight([]Adapter{c.a}); err == nil || !strings.Contains(err.Error(), c.env) {
			t.Errorf("%s: Preflight must refuse naming %s, got %v", c.a.Name(), c.env, err)
		}
		rec := &agentRecorder{}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		start := time.Now()
		err = NewRunner([]Adapter{c.a}, rec.fn, zap.NewNop(), 1).Run(ctx)
		cancel()
		if !errors.Is(err, errAllowlistEmpty) || time.Since(start) > time.Second {
			t.Errorf("%s: the runner must refuse to serve at once, got %v", c.a.Name(), err)
		}
	}
}

// The builders read the allowlist variables.
func TestBuildersReadAllowlists(t *testing.T) {
	t.Setenv("CHATCLI_SLACK_BOT_TOKEN", "xoxb")
	t.Setenv("CHATCLI_SLACK_ADDR", "127.0.0.1:0")
	t.Setenv("CHATCLI_SLACK_ALLOWED_USERS", "U1, T9:U2")
	t.Setenv("CHATCLI_DISCORD_BOT_TOKEN", "tok")
	t.Setenv("CHATCLI_DISCORD_ALLOWED_USERS", "42")
	t.Setenv("CHATCLI_WHATSAPP_ACCESS_TOKEN", "tok")
	t.Setenv("CHATCLI_WHATSAPP_PHONE_ID", "ph")
	t.Setenv("CHATCLI_WHATSAPP_ADDR", "127.0.0.1:0")
	t.Setenv("CHATCLI_WHATSAPP_ALLOWED_USERS", "*")
	t.Setenv("CHATCLI_TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("CHATCLI_TELEGRAM_ALLOWED_USERS", "7;8")
	built, err := BuildConfigured()
	if err != nil {
		t.Fatal(err)
	}
	var chat []Adapter
	for _, a := range built {
		if a.Name() != webhookPlatform {
			chat = append(chat, a)
		}
	}
	if len(chat) != 4 {
		t.Fatalf("built %d chat adapters, want 4", len(chat))
	}
	if err := Preflight(chat); err != nil {
		t.Fatalf("configured allowlists must pass preflight: %v", err)
	}
	for _, a := range chat {
		switch ad := a.(type) {
		case *SlackAdapter:
			if !ad.allowed.permits("U1") || !ad.allowed.permits(slackSenderKeys("T9", "U2")...) || ad.allowed.permits(slackSenderKeys("T1", "U2")...) {
				t.Error("slack allowlist not parsed")
			}
		case *DiscordAdapter:
			if !ad.allowed.permits("42") || ad.allowed.permits("43") {
				t.Error("discord allowlist not parsed")
			}
		case *WhatsAppAdapter:
			if !ad.allowed.permits("anyone") || ad.allowed.listed("anyone") {
				t.Error("whatsapp public opt-in not parsed")
			}
		case *TelegramAdapter:
			if !ad.allowed.permits("8") || ad.allowed.permits("9") {
				t.Error("telegram allowlist not parsed")
			}
		}
	}
}

// "*" is the explicit public opt-in: it starts, warns on every start, and
// the senders it admits are not "listed".
func TestPublicOptInStartsWithWarning(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	ch := make(chan InboundMessage, 1)
	a := NewSlackAdapter("xoxb", "sig", "127.0.0.1:0", "/ev", zap.New(core)).WithAllowedUsers([]string{"*"})
	if err := Preflight([]Adapter{a}); err != nil {
		t.Fatalf("\"*\" must pass preflight: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx, ch) }()
	waitFor(t, func() bool { return logs.FilterMessageSnippet("ANY user").Len() == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("start with \"*\": %v", err)
	}

	srv := httptest.NewServer(a.eventsHandler(context.Background(), ch))
	defer srv.Close()
	slackSignedPost(t, srv.URL, "sig", `{"type":"event_callback","event":{"type":"message","channel":"C","user":"UANY","text":"hello"}}`)
	m := recvWithin(t, ch, time.Second)
	if m.UserID != "UANY" || m.SenderListed {
		t.Errorf("public sender: %+v, want admitted and not listed", m)
	}
}
