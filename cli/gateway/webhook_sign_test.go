/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/netguard"
	"go.uber.org/zap"
)

func newSignedWebhookServer(t *testing.T, secret string) (*WebhookAdapter, *httptest.Server, chan InboundMessage) {
	t.Helper()
	ch := make(chan InboundMessage, 8)
	a := NewWebhookAdapter(":0", "/in", secret, "", zap.NewNop())
	srv := httptest.NewServer(a.inboundHandler(context.Background(), ch))
	t.Cleanup(srv.Close)
	return a, srv, ch
}

// The signature base string is "<timestamp>.<raw body>", HMAC-SHA256 keyed
// with the secret, hex-encoded behind "sha256=". Pinned with a fixed vector
// so an integration written from the documentation keeps verifying.
func TestWebhookSignatureFormat(t *testing.T) {
	got := signWebhook("demo-secret", "1760000000", []byte(`{"chat_id":"c","text":"hi"}`))
	// openssl: printf '%s' '1760000000.{"chat_id":"c","text":"hi"}' | openssl dgst -sha256 -hmac demo-secret
	const want = "sha256=4347fc376267e2ba759e363b020b9318c5864770a9e44dff812462cf802ccb53"
	if got != want {
		t.Fatalf("signature %q, want %q", got, want)
	}
	if !webhookSignatureValid("demo-secret", "1760000000", []byte(`{"chat_id":"c","text":"hi"}`), got) {
		t.Fatal("a signature must verify against its own inputs")
	}
	if webhookSignatureValid("demo-secret", "1760000001", []byte(`{"chat_id":"c","text":"hi"}`), got) {
		t.Error("the timestamp must be covered by the signature")
	}
	if webhookSignatureValid("other", "1760000000", []byte(`{"chat_id":"c","text":"hi"}`), got) {
		t.Error("the key must be covered by the signature")
	}
	if webhookSignatureValid("", "1760000000", []byte(`{}`), signWebhook("", "1760000000", []byte(`{}`))) {
		t.Error("an unset secret must verify nothing")
	}
}

func TestWebhookAcceptsAFreshSignedRequest(t *testing.T) {
	_, srv, ch := newSignedWebhookServer(t, "s")
	if status, body, _ := postWebhook(t, srv.URL, "s", `{"chat_id":"c","text":"hi"}`); status != http.StatusAccepted {
		t.Fatalf("got %d %s, want 202", status, body)
	}
	if m := recvWithin(t, ch, 2*time.Second); m.Text != "hi" {
		t.Errorf("text %q", m.Text)
	}
}

func TestWebhookRejectsUnsignedAndLegacySecretHeader(t *testing.T) {
	_, srv, ch := newSignedWebhookServer(t, "s")
	if status, _, _ := postWebhook(t, srv.URL, "", `{"chat_id":"c","text":"hi"}`); status != http.StatusUnauthorized {
		t.Errorf("unsigned request: got %d, want 401", status)
	}
	// The old static header is no longer a credential.
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"chat_id":"c","text":"hi"}`))
	req.Header.Set("X-ChatCLI-Secret", "s")
	if status, _, _ := doWebhookRequest(t, req); status != http.StatusUnauthorized {
		t.Errorf("raw secret header: got %d, want 401", status)
	}
	assertNothingQueued(t, ch)
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	_, srv, ch := newSignedWebhookServer(t, "s")
	if status, body, _ := postWebhook(t, srv.URL, "wrong", `{"chat_id":"c","text":"hi"}`); status != http.StatusUnauthorized || !strings.Contains(body, whErrUnauthorized) {
		t.Errorf("bad signature: got %d %s, want 401", status, body)
	}
	// A body changed after signing fails too.
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"chat_id":"c","text":"rm -rf"}`))
	req.Header.Set(webhookTimestampHeader, ts)
	req.Header.Set(webhookSignatureHeader, signWebhook("s", ts, []byte(`{"chat_id":"c","text":"hi"}`)))
	if status, _, _ := doWebhookRequest(t, req); status != http.StatusUnauthorized {
		t.Errorf("tampered body: got %d, want 401", status)
	}
	assertNothingQueued(t, ch)
}

// The signature is checked on the raw bytes before parsing: a forged
// request is refused as unauthorized, never answered with a parse error
// that would tell the caller anything about the payload rules.
func TestWebhookVerifiesBeforeParsing(t *testing.T) {
	_, srv, _ := newSignedWebhookServer(t, "s")
	if status, body, _ := postWebhook(t, srv.URL, "wrong", `{nope`); status != http.StatusUnauthorized {
		t.Errorf("forged malformed body: got %d %s, want 401", status, body)
	}
}

func TestWebhookRejectsStaleAndFutureTimestamps(t *testing.T) {
	_, srv, ch := newSignedWebhookServer(t, "s")
	for _, at := range []time.Time{time.Now().Add(-6 * time.Minute), time.Now().Add(6 * time.Minute)} {
		status, body, _ := postWebhookAt(t, srv.URL, "s", `{"chat_id":"c","text":"hi"}`, at)
		if status != http.StatusUnauthorized || !strings.Contains(body, whErrStale) {
			t.Errorf("timestamp %v: got %d %s, want 401 %s", at, status, body, whErrStale)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"chat_id":"c","text":"hi"}`))
	req.Header.Set(webhookTimestampHeader, "yesterday")
	req.Header.Set(webhookSignatureHeader, signWebhook("s", "yesterday", []byte(`{"chat_id":"c","text":"hi"}`)))
	if status, _, _ := doWebhookRequest(t, req); status != http.StatusUnauthorized {
		t.Errorf("non-numeric timestamp: got %d, want 401", status)
	}
	assertNothingQueued(t, ch)
}

// A captured request replayed verbatim is refused: inside the window as a
// duplicate, outside it as stale.
func TestWebhookRejectsReplays(t *testing.T) {
	a, srv, ch := newSignedWebhookServer(t, "s")
	body := `{"chat_id":"c","text":"once"}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := signWebhook("s", ts, []byte(body))
	send := func() (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
		req.Header.Set(webhookTimestampHeader, ts)
		req.Header.Set(webhookSignatureHeader, sig)
		status, resp, _ := doWebhookRequest(t, req)
		return status, resp
	}
	if status, resp := send(); status != http.StatusAccepted {
		t.Fatalf("first delivery: %d %s", status, resp)
	}
	recvWithin(t, ch, 2*time.Second)
	if status, resp := send(); status != http.StatusConflict || !strings.Contains(resp, whErrReplayed) {
		t.Errorf("replay inside the window: got %d %s, want 409 %s", status, resp, whErrReplayed)
	}
	// Six minutes later the same bytes are past the window.
	a.state.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if status, resp := send(); status != http.StatusUnauthorized || !strings.Contains(resp, whErrStale) {
		t.Errorf("replay outside the window: got %d %s, want 401 %s", status, resp, whErrStale)
	}
	assertNothingQueued(t, ch)
}

// The callback carries a valid signature over the exact body it posts, a
// current timestamp, and never the secret.
func TestWebhookCallbackIsSignedNotSecretBearing(t *testing.T) {
	rec := &callbackRecorder{}
	cb := httptest.NewServer(rec)
	defer cb.Close()
	a := NewWebhookAdapter(":0", "", "callback-key", cb.URL, zap.NewNop())
	if err := a.Send(context.Background(), OutboundMessage{ChatID: "c", Text: "pronto", Kind: OutboundFinal}); err != nil {
		t.Fatal(err)
	}
	if len(rec.hits) != 1 {
		t.Fatalf("hits %d", len(rec.hits))
	}
	h := rec.hits[0]
	for name, vals := range h {
		for _, v := range vals {
			if strings.Contains(v, "callback-key") {
				t.Fatalf("header %s carries the secret", name)
			}
		}
	}
	ts := h.Get(webhookTimestampHeader)
	if _, fresh := webhookTimestampFresh(ts, time.Now()); !fresh {
		t.Errorf("callback timestamp %q is not current", ts)
	}
	if !webhookSignatureValid("callback-key", ts, rec.raw[0], h.Get(webhookSignatureHeader)) {
		t.Errorf("callback signature %q does not verify", h.Get(webhookSignatureHeader))
	}
}

func TestWebhookRefusesToStartWithoutSecret(t *testing.T) {
	a := NewWebhookAdapter("127.0.0.1:0", "", "", "", zap.NewNop())
	err := a.Start(cancelledCtx(), make(chan InboundMessage))
	if err == nil || !strings.Contains(err.Error(), "CHATCLI_WEBHOOK_SECRET") {
		t.Fatalf("start without a secret must fail naming the variable, got %v", err)
	}
	if Preflight([]Adapter{a}) == nil {
		t.Error("preflight must report the missing secret")
	}
}

func TestWebhookCallbackMustBeHTTPSUnlessLoopback(t *testing.T) {
	for _, ok := range []string{"https://bridge.example.com/cb", "http://127.0.0.1:8090/cb", "http://localhost:8090/cb", "http://[::1]:8090/cb"} {
		if err := checkWebhookCallbackURL(ok); err != nil {
			t.Errorf("%s must be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://bridge.example.com/cb", "http://10.0.0.5/cb", "ftp://bridge.example.com/cb", "bridge.example.com/cb", ""} {
		if err := checkWebhookCallbackURL(bad); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}

	a := NewWebhookAdapter("127.0.0.1:0", "", "s", "http://bridge.example.com/cb?token=abc", zap.NewNop())
	err := a.Start(cancelledCtx(), make(chan InboundMessage))
	if err == nil || !strings.Contains(err.Error(), "CHATCLI_WEBHOOK_CALLBACK_URL") {
		t.Fatalf("start with a plain-http remote callback must fail naming the variable, got %v", err)
	}
	if strings.Contains(err.Error(), "token=abc") {
		t.Errorf("the refusal must not echo the URL's query: %v", err)
	}
	// @send reaches Send without Start: the rule holds there too.
	if err := a.Send(context.Background(), OutboundMessage{ChatID: "c", Text: "x"}); err == nil {
		t.Error("Send must refuse a plain-http remote callback")
	}
}

// Attachment URLs are chosen by the caller: anything but a public https
// host is refused, and the note the agent sees says nothing about why.
func TestWebhookMediaURLsAreGuarded(t *testing.T) {
	a := NewWebhookAdapter(":0", "", "s", "", zap.NewNop())
	for _, target := range []string{
		"https://127.0.0.1/a.png",
		"https://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"https://[::1]/a.png",
		"https://localhost/a.png",
		"https://metadata.google.internal/computeMetadata/v1/",
		"http://cdn.example.com/a.png",
	} {
		msg := InboundMessage{Text: "look", Image: &InboundImage{ref: target}}
		a.hydrateImages(context.Background(), &msg)
		if msg.Image != nil {
			t.Errorf("%s: must not be fetched", target)
		}
		if !strings.Contains(msg.Text, "could not be downloaded") || strings.Contains(msg.Text, "169.254") || strings.Contains(msg.Text, "127.0.0.1") {
			t.Errorf("%s: note must be generic, got %q", target, msg.Text)
		}
		if _, _, err := a.fetchAttachment(context.Background(), target, 1024); err == nil {
			t.Errorf("%s: fetch must be refused", target)
		}
	}
}

// The URL check stands on its own: even with a client that would reach any
// host (here every dial lands on a test server that accepts any name), an
// internal or plain-http attachment URL is never requested.
func TestWebhookMediaURLCheckRunsBeforeAnyRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("IMG"))
	}))
	defer srv.Close()
	a := NewWebhookAdapter(":0", "", "s", "", zap.NewNop())
	base := mediaVia(t, a, srv)
	tr, ok := a.state.media.Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected test transport")
	}
	tr.TLSClientConfig.InsecureSkipVerify = true // #nosec G402 -- test: make every name reachable so only the URL check can refuse
	port := base[strings.LastIndex(base, ":")+1:]

	// Sanity: a public https URL does go through this client.
	if _, _, err := a.fetchAttachment(context.Background(), base+"/ok.png", 1024); err != nil || hits.Load() != 1 {
		t.Fatalf("control request failed: err=%v hits=%d", err, hits.Load())
	}
	for _, target := range []string{
		"https://127.0.0.1:" + port + "/a.png",
		"https://169.254.169.254:" + port + "/latest/meta-data/",
		"https://localhost:" + port + "/a.png",
		"https://metadata.google.internal:" + port + "/computeMetadata/v1/",
		"http://example.com:" + port + "/a.png",
	} {
		if _, _, err := a.fetchAttachment(context.Background(), target, 1024); err == nil {
			t.Errorf("%s: must be refused", target)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("refused URLs reached the server %d time(s)", hits.Load()-1)
	}
}

// The default attachment client is the netguard one: a name that passes
// the URL check but resolves to loopback is stopped at dial time.
func TestWebhookMediaClientRefusesLoopbackAtDialTime(t *testing.T) {
	hit := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hit = true }))
	defer srv.Close()
	a := NewWebhookAdapter(":0", "", "s", "", zap.NewNop())
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	_, _, err := fetchAudioBytes(context.Background(), a.state.media, "https://localhost:"+port+"/x", "", 1024)
	if err == nil || hit {
		t.Fatalf("a loopback target must never be reached: err=%v hit=%v", err, hit)
	}
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("refusal should come from the guard, got %v", err)
	}
}
