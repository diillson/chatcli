package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

func recvWithin(t *testing.T, ch <-chan InboundMessage, d time.Duration) InboundMessage {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(d):
		t.Fatal("timed out waiting for inbound message")
		return InboundMessage{}
	}
}

func TestSlackEventsHandler(t *testing.T) {
	ch := make(chan InboundMessage, 1)
	a := NewSlackAdapter("tok", "", ":0", "/slack/events", zap.NewNop()) // empty secret skips verify
	srv := httptest.NewServer(a.eventsHandler(context.Background(), ch))
	defer srv.Close()

	// url_verification handshake echoes the challenge.
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"type":"url_verification","challenge":"abc123"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "abc123") {
		t.Errorf("challenge not echoed: %s", body)
	}

	// A message event reaches inbound.
	_, err = http.Post(srv.URL, "application/json", strings.NewReader(
		`{"type":"event_callback","event":{"type":"message","channel":"C1","user":"U1","text":"hi slack"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m := recvWithin(t, ch, time.Second); m.Text != "hi slack" || m.ChatID != "C1" {
		t.Errorf("inbound wrong: %+v", m)
	}

	// GET is rejected.
	r, _ := http.Get(srv.URL)
	if r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET should be 405, got %d", r.StatusCode)
	}
}

func TestSlackEventsHandler_BadSignature(t *testing.T) {
	ch := make(chan InboundMessage, 1)
	a := NewSlackAdapter("tok", "secret", ":0", "/x", zap.NewNop())
	srv := httptest.NewServer(a.eventsHandler(context.Background(), ch))
	defer srv.Close()
	// No signature headers -> 401.
	resp, _ := http.Post(srv.URL, "application/json", strings.NewReader(`{"type":"event_callback"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing signature, got %d", resp.StatusCode)
	}
}

func TestSlackSend(t *testing.T) {
	var gotChannel, gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var p map[string]string
		_ = json.Unmarshal(b, &p)
		gotChannel = p["channel"]
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	a := NewSlackAdapter("xoxb-tok", "", ":0", "/x", zap.NewNop())
	a.apiBase = api.URL
	if err := a.Send(context.Background(), OutboundMessage{ChatID: "C9", Text: "yo"}); err != nil {
		t.Fatal(err)
	}
	if gotChannel != "C9" || gotAuth != "Bearer xoxb-tok" {
		t.Errorf("send wrong: channel=%q auth=%q", gotChannel, gotAuth)
	}
}

func TestWhatsAppHandler(t *testing.T) {
	ch := make(chan InboundMessage, 1)
	a := NewWhatsAppAdapter("tok", "phone1", "verifytok", ":0", "/wa", zap.NewNop())
	srv := httptest.NewServer(a.webhookHandler(context.Background(), ch))
	defer srv.Close()

	// Verification handshake.
	resp, _ := http.Get(srv.URL + "?hub.mode=subscribe&hub.verify_token=verifytok&hub.challenge=ping")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ping" {
		t.Errorf("verify challenge not echoed: %q", body)
	}
	// Wrong token -> 403.
	r2, _ := http.Get(srv.URL + "?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=x")
	if r2.StatusCode != http.StatusForbidden {
		t.Errorf("wrong verify token should be 403, got %d", r2.StatusCode)
	}
	// Message event.
	_, _ = http.Post(srv.URL, "application/json", strings.NewReader(
		`{"entry":[{"changes":[{"value":{"messages":[{"from":"55119","type":"text","text":{"body":"ola"}}]}}]}]}`))
	if m := recvWithin(t, ch, time.Second); m.Text != "ola" || m.ChatID != "55119" {
		t.Errorf("inbound wrong: %+v", m)
	}
}

func TestWhatsAppSend(t *testing.T) {
	var gotTo string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p map[string]interface{}
		_ = json.Unmarshal(b, &p)
		gotTo, _ = p["to"].(string)
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	a := NewWhatsAppAdapter("tok", "phone1", "v", ":0", "/wa", zap.NewNop())
	a.graphBase = api.URL
	if err := a.Send(context.Background(), OutboundMessage{ChatID: "55119", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if gotTo != "55119" {
		t.Errorf("send to wrong: %q", gotTo)
	}
}

func TestWebhookHandlerAndSend(t *testing.T) {
	// Outbound callback target.
	var delivered string
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p map[string]string
		_ = json.Unmarshal(b, &p)
		delivered = p["text"]
		w.WriteHeader(http.StatusOK)
	}))
	defer cb.Close()

	ch := make(chan InboundMessage, 1)
	a := NewWebhookAdapter(":0", "/inbound", "s3cret", cb.URL, zap.NewNop())
	srv := httptest.NewServer(a.inboundHandler(context.Background(), ch))
	defer srv.Close()

	// Missing secret -> 401.
	r1, _ := http.Post(srv.URL, "application/json", strings.NewReader(`{"chat_id":"c","text":"x"}`))
	if r1.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing secret should be 401, got %d", r1.StatusCode)
	}
	// With secret -> accepted + inbound.
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"chat_id":"c1","text":"hello hook"}`))
	req.Header.Set("X-ChatCLI-Secret", "s3cret")
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202, got %d", r2.StatusCode)
	}
	if m := recvWithin(t, ch, time.Second); m.Text != "hello hook" {
		t.Errorf("inbound wrong: %+v", m)
	}

	// Send delivers to the callback.
	if err := a.Send(context.Background(), OutboundMessage{ChatID: "c1", Text: "reply!"}); err != nil {
		t.Fatal(err)
	}
	if delivered != "reply!" {
		t.Errorf("callback not delivered: %q", delivered)
	}
}

func TestDiscordSend(t *testing.T) {
	var gotAuth, gotContent string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		var p map[string]string
		_ = json.Unmarshal(b, &p)
		gotContent = p["content"]
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	d := NewDiscordAdapter("bottok", zap.NewNop())
	d.restBase = api.URL
	if err := d.Send(context.Background(), OutboundMessage{ChatID: "123", Text: "hey"}); err != nil {
		t.Fatal(err)
	}
	if gotContent != "hey" || gotAuth != "Bot bottok" {
		t.Errorf("discord send wrong: content=%q auth=%q", gotContent, gotAuth)
	}
}

// TestDiscordGatewaySession runs a real Gateway handshake against an in-process
// WebSocket server: HELLO -> IDENTIFY -> MESSAGE_CREATE dispatch.
func TestDiscordGatewaySession(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// HELLO
		_ = conn.WriteJSON(map[string]interface{}{"op": dOpHello, "d": map[string]int{"heartbeat_interval": 45000}})
		// Expect IDENTIFY
		_, _, _ = conn.ReadMessage()
		// Dispatch a message.
		_ = conn.WriteJSON(map[string]interface{}{
			"op": dOpDispatch, "s": 1, "t": "MESSAGE_CREATE",
			"d": map[string]interface{}{"channel_id": "C42", "content": "hello discord", "author": map[string]interface{}{"id": "U1", "username": "neo"}},
		})
		// Keep the connection open until the client disconnects.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	d := NewDiscordAdapter("tok", zap.NewNop())
	d.gatewayURL = "ws" + strings.TrimPrefix(srv.URL, "http") // ws://127.0.0.1:port

	ch := make(chan InboundMessage, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.runSession(ctx, ch) }()

	if m := recvWithin(t, ch, 3*time.Second); m.Text != "hello discord" || m.ChatID != "C42" {
		t.Errorf("discord inbound wrong: %+v", m)
	}
}

func TestRegistry(t *testing.T) {
	names := RegisteredNames()
	// All five builders register at init.
	for _, want := range []string{"telegram", "slack", "discord", "whatsapp", "webhook"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("builder %q not registered", want)
		}
	}
	// With no env configured, BuildConfigured yields nothing (builders return nil).
	got, err := BuildConfigured()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if a == nil {
			t.Error("BuildConfigured must not return nil adapters")
		}
	}
}

func TestTelegramBuilderConfigured(t *testing.T) {
	t.Setenv("CHATCLI_TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("CHATCLI_TELEGRAM_ALLOWED_USERS", "1,2")
	a, err := BuildConfigured()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ad := range a {
		if ad.Name() == "telegram" {
			found = true
		}
	}
	if !found {
		t.Error("telegram should be configured when token is set")
	}
}

// sign builds a valid Slack signature so the signed-path branch is exercised.
func TestSlackSignedPath(t *testing.T) {
	ch := make(chan InboundMessage, 1)
	secret := "shh"
	a := NewSlackAdapter("tok", secret, ":0", "/x", zap.NewNop())
	srv := httptest.NewServer(a.eventsHandler(context.Background(), ch))
	defer srv.Close()

	body := `{"type":"event_callback","event":{"type":"message","channel":"C","user":"U","text":"signed"}}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	if m := recvWithin(t, ch, time.Second); m.Text != "signed" {
		t.Errorf("signed message not received: %+v", m)
	}
}
