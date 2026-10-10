package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func postWebhook(t *testing.T, url, secret, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-ChatCLI-Secret", secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b)), resp.Header
}

// Every refusal names its reason, so a caller can tell a missing chat_id
// from bad base64 without guessing.
func TestWebhookRefusalsCarryAReason(t *testing.T) {
	a := NewWebhookAdapter(":0", "/in", "s", "", zap.NewNop())
	srv := httptest.NewServer(a.inboundHandler(context.Background(), make(chan InboundMessage, 1)))
	defer srv.Close()

	cases := []struct {
		name, secret, body string
		status             int
		code               string
	}{
		{"wrong secret", "x", `{"chat_id":"c","text":"t"}`, 401, whErrUnauthorized},
		{"bad json", "s", `{nope`, 400, whErrInvalidJSON},
		{"no chat", "s", `{"text":"t"}`, 400, whErrNoChatID},
		{"bad audio", "s", `{"chat_id":"c","audio_b64":"@@"}`, 400, whErrAudioB64},
		{"bad image", "s", `{"chat_id":"c","image_b64":"@@"}`, 400, whErrImageB64},
		{"empty", "s", `{"chat_id":"c","text":"  "}`, 400, whErrEmpty},
	}
	for _, c := range cases {
		status, body, _ := postWebhook(t, srv.URL, c.secret, c.body)
		if status != c.status || body != `{"error":"`+c.code+`"}` {
			t.Errorf("%s: got %d %s, want %d %s", c.name, status, body, c.status, c.code)
		}
	}

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
		t.Errorf("GET: got %d Allow=%q", resp.StatusCode, resp.Header.Get("Allow"))
	}
}

// A body past the cap is refused as too large, not as broken JSON.
func TestWebhookOversizedBodyIs413(t *testing.T) {
	t.Setenv("CHATCLI_GATEWAY_MAX_AUDIO_BYTES", "1024")
	t.Setenv("CHATCLI_GATEWAY_MAX_IMAGE_BYTES", "1024")
	a := NewWebhookAdapter(":0", "/in", "s", "", zap.NewNop())
	srv := httptest.NewServer(a.inboundHandler(context.Background(), make(chan InboundMessage, 1)))
	defer srv.Close()

	big := `{"chat_id":"c","text":"` + strings.Repeat("x", int(maxWebhookBodyBytes())) + `"}`
	if status, body, _ := postWebhook(t, srv.URL, "s", big); status != http.StatusRequestEntityTooLarge || !strings.Contains(body, whErrTooLarge) {
		t.Errorf("got %d %s, want 413", status, body)
	}
}

// The cap must fit the largest audio the gateway accepts, sent inline.
func TestWebhookBodyCapFitsBase64Media(t *testing.T) {
	if got, need := maxWebhookBodyBytes(), base64Len(maxAudioBytes())+base64Len(maxImageBytes()); got <= need {
		t.Errorf("body cap %d cannot hold inline audio and image (%d)", got, need)
	}
}

// An attachment URL is fetched after the 202, so a slow host never holds the
// caller; and a failed download reaches the agent as a note, not silence.
func TestWebhookAcceptsBeforeDownloadAndNotesFailures(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer slow.Close()

	ch := make(chan InboundMessage, 4)
	a := NewWebhookAdapter(":0", "/in", "s", "", zap.NewNop())
	srv := httptest.NewServer(a.inboundHandler(context.Background(), ch))
	defer srv.Close()

	t0 := time.Now()
	status, _, _ := postWebhook(t, srv.URL, "s", `{"chat_id":"c","text":"look","image_url":"`+slow.URL+`"}`)
	if status != http.StatusAccepted {
		t.Fatalf("status %d", status)
	}
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Errorf("202 took %v; the download must not hold the caller", d)
	}
	m := recvWithin(t, ch, 2*time.Second)
	if m.Image != nil || !strings.HasPrefix(m.Text, "look\n\n[The image") || !strings.Contains(m.Text, "404") {
		t.Errorf("failed download not noted: image=%v text=%q", m.Image, m.Text)
	}

	// No text at all: the note becomes the message.
	postWebhook(t, srv.URL, "s", `{"chat_id":"c","audio_url":"`+slow.URL+`"}`)
	if m := recvWithin(t, ch, 2*time.Second); !strings.HasPrefix(m.Text, "[The audio") {
		t.Errorf("audio-only failure not noted: %q", m.Text)
	}
}

// Messages of one chat reach the agent in the order they were accepted,
// even when an earlier one waits on a download; other chats are not held.
func TestWebhookKeepsPerChatOrder(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("PNG"))
	}))
	defer slow.Close()

	ch := make(chan InboundMessage, 4)
	a := NewWebhookAdapter(":0", "/in", "s", "", zap.NewNop())
	srv := httptest.NewServer(a.inboundHandler(context.Background(), ch))
	defer srv.Close()

	postWebhook(t, srv.URL, "s", `{"chat_id":"A","text":"first","image_url":"`+slow.URL+`"}`)
	postWebhook(t, srv.URL, "s", `{"chat_id":"A","text":"second"}`)
	postWebhook(t, srv.URL, "s", `{"chat_id":"B","text":"other"}`)

	var got []string
	for i := 0; i < 3; i++ {
		got = append(got, recvWithin(t, ch, 2*time.Second).Text)
	}
	if strings.Join(got, ",") != "other,first,second" {
		t.Errorf("delivery order %v, want other,first,second", got)
	}
}

// A chat whose lane is full is refused with 429 instead of queueing forever.
func TestWebhookFullLaneIs429(t *testing.T) {
	a := NewWebhookAdapter(":0", "/in", "s", "", zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(a.inboundHandler(ctx, make(chan InboundMessage))) // nobody reads
	defer srv.Close()

	var refused int
	for i := 0; i < webhookLaneDepth+3; i++ {
		status, body, _ := postWebhook(t, srv.URL, "s", `{"chat_id":"c","text":"m`+strconv.Itoa(i)+`"}`)
		if status == http.StatusTooManyRequests && strings.Contains(body, whErrQueueFull) {
			refused++
		}
	}
	if refused == 0 {
		t.Error("a full lane must refuse with 429 queue_full")
	}
}

// Without user_id the chat is the sender, so anonymous callers in different
// chats never collapse into one identity.
func TestWebhookAnonymousSenderIsTheChat(t *testing.T) {
	m, ok := parseWebhookInbound([]byte(`{"chat_id":"ticket-7","text":"hi"}`))
	if !ok || m.UserID != "ticket-7" {
		t.Errorf("UserID = %q, want the chat id", m.UserID)
	}
	m, _ = parseWebhookInbound([]byte(`{"chat_id":"ticket-7","user_id":"alice","text":"hi"}`))
	if m.UserID != "alice" {
		t.Errorf("explicit user_id lost: %q", m.UserID)
	}
}

type callbackRecorder struct {
	mu       sync.Mutex
	statuses []int // status to answer per attempt; 200 once exhausted
	hits     []http.Header
	bodies   []map[string]string
}

func (c *callbackRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.hits = append(c.hits, r.Header.Clone())
	c.bodies = append(c.bodies, body)
	status := http.StatusOK
	if n := len(c.hits); n <= len(c.statuses) {
		status = c.statuses[n-1]
	}
	w.WriteHeader(status)
}

func newRetryingAdapter(url string) *WebhookAdapter {
	a := NewWebhookAdapter(":0", "", "s", url, zap.NewNop())
	a.state.retryBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	return a
}

// A final reply survives a receiver hiccup: it is retried, and every attempt
// carries the same delivery id so the receiver can drop duplicates.
func TestWebhookRetriesTheFinalReply(t *testing.T) {
	rec := &callbackRecorder{statuses: []int{500, 503}}
	cb := httptest.NewServer(rec)
	defer cb.Close()

	if err := newRetryingAdapter(cb.URL).Send(context.Background(), OutboundMessage{ChatID: "c", Text: "done", Kind: OutboundFinal}); err != nil {
		t.Fatalf("third attempt should have succeeded: %v", err)
	}
	if len(rec.hits) != 3 {
		t.Fatalf("attempts = %d, want 3", len(rec.hits))
	}
	id := rec.hits[0].Get("X-ChatCLI-Delivery-ID")
	for i, h := range rec.hits {
		if h.Get("X-ChatCLI-Delivery-ID") != id || id == "" {
			t.Errorf("attempt %d delivery id %q, want %q", i+1, h.Get("X-ChatCLI-Delivery-ID"), id)
		}
		if h.Get("X-ChatCLI-Delivery-Attempt") != strconv.Itoa(i+1) {
			t.Errorf("attempt header %q", h.Get("X-ChatCLI-Delivery-Attempt"))
		}
		if h.Get("X-ChatCLI-Secret") != "s" {
			t.Error("every attempt must carry the secret")
		}
	}
	if rec.bodies[2]["kind"] != OutboundFinal || rec.bodies[2]["text"] != "done" {
		t.Errorf("body %v", rec.bodies[2])
	}
}

// Progress is best effort (one attempt), and a 4xx is the receiver's answer,
// not a hiccup: neither is retried.
func TestWebhookDoesNotRetryProgressOrClientErrors(t *testing.T) {
	rec := &callbackRecorder{statuses: []int{500}}
	cb := httptest.NewServer(rec)
	defer cb.Close()
	if err := newRetryingAdapter(cb.URL).Send(context.Background(), OutboundMessage{ChatID: "c", Text: "…", Kind: OutboundProgress}); err == nil {
		t.Error("a failed progress update must report the failure")
	}
	if len(rec.hits) != 1 {
		t.Errorf("progress attempts = %d, want 1", len(rec.hits))
	}

	rec2 := &callbackRecorder{statuses: []int{400}}
	cb2 := httptest.NewServer(rec2)
	defer cb2.Close()
	if err := newRetryingAdapter(cb2.URL).Send(context.Background(), OutboundMessage{ChatID: "c", Text: "x", Kind: OutboundFinal}); err == nil {
		t.Error("a 400 must be reported")
	}
	if len(rec2.hits) != 1 {
		t.Errorf("4xx attempts = %d, want 1", len(rec2.hits))
	}
}

// A message with no kind (a standalone send) carries no kind field.
func TestWebhookOmitsEmptyKind(t *testing.T) {
	rec := &callbackRecorder{}
	cb := httptest.NewServer(rec)
	defer cb.Close()
	if err := newRetryingAdapter(cb.URL).Send(context.Background(), OutboundMessage{ChatID: "c", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, has := rec.bodies[0]["kind"]; has {
		t.Errorf("unexpected kind in %v", rec.bodies[0])
	}
}

func TestWebhookMissingOutbound(t *testing.T) {
	var oc OutboundChecker = NewWebhookAdapter(":0", "", "s", "", zap.NewNop())
	if oc.MissingOutbound() != "CHATCLI_WEBHOOK_CALLBACK_URL" {
		t.Errorf("no callback must be reported, got %q", oc.MissingOutbound())
	}
	if NewWebhookAdapter(":0", "", "s", "http://cb", zap.NewNop()).MissingOutbound() != "" {
		t.Error("a configured callback must report nothing missing")
	}
}
