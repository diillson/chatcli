/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/netguard"
	"go.uber.org/zap"
)

const webhookPlatform = "webhook"

// WebhookAdapter is a generic, platform-agnostic adapter: it runs an HTTP
// server that accepts inbound messages as JSON and delivers replies by
// POSTing to a configured callback URL. Any platform or custom integration
// that can send/receive an HTTP POST can use it — no per-platform code.
//
// Inbound  (POST <path>): {"chat_id":"...", "user_id":"...", "text":"..."}
// Outbound (POST callbackURL): {"chat_id":"...", "kind":"final", "text":"..."}
//
// SecOps: every request, in both directions, is signed with the shared
// secret (see webhook_sign.go): X-ChatCLI-Timestamp carries unix seconds and
// X-ChatCLI-Signature carries "sha256=" plus the hex HMAC-SHA256 of
// "<timestamp>.<raw body>". Inbound requests are verified before the body is
// parsed, within a five-minute window, once each. The secret itself is never
// sent. The adapter refuses to start without a secret, and with a callback
// URL that is not https (plain http only for a loopback host). Attachment
// URLs are fetched over https from public addresses only.
//
// Delivery model: an accepted request is answered 202 right away and queued
// on a per-chat lane, so attachment downloads never hold the caller and the
// messages of one chat reach the agent in the order they were accepted.
// Final and proactive callbacks are retried on transient failures and carry a
// stable X-ChatCLI-Delivery-ID so the receiver can drop duplicates.
type WebhookAdapter struct {
	addr        string
	path        string
	secret      string
	callbackURL string
	http        *http.Client
	logger      *zap.Logger

	// state is held by pointer so the adapter stays comparable.
	state *webhookState
}

// webhookState is the adapter's mutable delivery state.
type webhookState struct {
	lanesMu sync.Mutex
	lanes   map[string]chan InboundMessage
	// retryBackoff is the wait before each callback retry; len+1 attempts.
	retryBackoff []time.Duration
	// replay remembers the inbound signatures accepted within the window.
	replay *webhookReplayGuard
	// media fetches attachment URLs; it reaches public https hosts only.
	media *http.Client
	// now is the clock the signature window is checked against.
	now func() time.Time
}

// Webhook error codes, returned as {"error":"<code>"} so a caller can tell
// one refusal from another.
const (
	whErrMethod       = "method_not_allowed"
	whErrUnauthorized = "unauthorized"
	whErrStale        = "stale_timestamp"
	whErrReplayed     = "replayed"
	whErrTooLarge     = "payload_too_large"
	whErrInvalidJSON  = "invalid_json"
	whErrNoChatID     = "missing_chat_id"
	whErrAudioB64     = "invalid_audio_b64"
	whErrImageB64     = "invalid_image_b64"
	whErrEmpty        = "empty_message"
	whErrQueueFull    = "queue_full"
	whErrShutdown     = "shutting_down"
)

// webhookLaneDepth bounds the messages waiting on one chat's lane; past it
// the request is refused with 429 instead of growing without limit.
const webhookLaneDepth = 32

// defaultWebhookRetryBackoff spaces the retries of a final or proactive
// callback: three attempts in all, about four seconds end to end when the
// receiver refuses fast.
var defaultWebhookRetryBackoff = []time.Duration{time.Second, 3 * time.Second}

// NewWebhookAdapter builds a generic webhook adapter.
func NewWebhookAdapter(addr, path, secret, callbackURL string, logger *zap.Logger) *WebhookAdapter {
	if path == "" {
		path = "/inbound"
	}
	return &WebhookAdapter{
		addr:        addr,
		path:        path,
		secret:      secret,
		callbackURL: callbackURL,
		http:        &http.Client{Timeout: 15 * time.Second},
		logger:      logger,

		state: &webhookState{
			lanes:        make(map[string]chan InboundMessage),
			retryBackoff: defaultWebhookRetryBackoff,
			replay:       newWebhookReplayGuard(),
			media:        netguard.NewClient(30 * time.Second),
			now:          time.Now,
		},
	}
}

// preflight implements preflighter: a webhook without a secret cannot tell
// its caller from anyone else, and a callback over plain http to another
// host would carry every reply in clear text.
func (w *WebhookAdapter) preflight() error {
	if strings.TrimSpace(w.secret) == "" {
		return fmt.Errorf("%s", i18n.T("gateway.webhook.no_secret", "CHATCLI_WEBHOOK_SECRET"))
	}
	if strings.TrimSpace(w.callbackURL) != "" {
		return checkWebhookCallbackURL(w.callbackURL)
	}
	return nil
}

// MissingOutbound implements OutboundChecker: without a callback URL there
// is nowhere to deliver a message.
func (w *WebhookAdapter) MissingOutbound() string {
	if strings.TrimSpace(w.callbackURL) == "" {
		return "CHATCLI_WEBHOOK_CALLBACK_URL"
	}
	return ""
}

// Name implements Adapter.
func (w *WebhookAdapter) Name() string { return webhookPlatform }

// SetLogger implements LoggerAware: inject the daemon logger and trace the
// HTTP client's calls to the configured callback URL.
func (w *WebhookAdapter) SetLogger(l *zap.Logger) {
	w.logger = l
	w.http = newLoggingClient(w.http, l, webhookPlatform)
}

type webhookInbound struct {
	ChatID string `json:"chat_id"`
	UserID string `json:"user_id"`
	Text   string `json:"text"`
	// Optional audio: inline base64 (audio_b64, decoded here) or a URL
	// (audio_url, fetched by the adapter). audio_mime is best-effort.
	AudioB64  string `json:"audio_b64"`
	AudioURL  string `json:"audio_url"`
	AudioMime string `json:"audio_mime"`
	// Optional image: inline base64 (image_b64, decoded here) or a URL
	// (image_url, fetched by the adapter). image_mime is best-effort.
	ImageB64  string `json:"image_b64"`
	ImageURL  string `json:"image_url"`
	ImageMime string `json:"image_mime"`
}

// inboundHandler builds the HTTP handler. Extracted from Start so it can be
// exercised directly via httptest.
func (w *WebhookAdapter) inboundHandler(ctx context.Context, inbound chan<- InboundMessage) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			rw.Header().Set("Allow", http.MethodPost)
			writeWebhookError(rw, http.StatusMethodNotAllowed, whErrMethod)
			return
		}
		// Cheap checks first, so an unsigned or stale request is refused
		// before its body is read.
		ts, sig := r.Header.Get(webhookTimestampHeader), r.Header.Get(webhookSignatureHeader)
		if w.secret == "" || ts == "" || sig == "" {
			writeWebhookError(rw, http.StatusUnauthorized, whErrUnauthorized)
			return
		}
		now := w.state.now()
		at, fresh := webhookTimestampFresh(ts, now)
		if !fresh {
			writeWebhookError(rw, http.StatusUnauthorized, whErrStale)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxWebhookBodyBytes()))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeWebhookError(rw, http.StatusRequestEntityTooLarge, whErrTooLarge)
				return
			}
			writeWebhookError(rw, http.StatusBadRequest, whErrInvalidJSON)
			return
		}
		// The signature is verified over the raw bytes before any of them
		// is parsed.
		if !webhookSignatureValid(w.secret, ts, body, sig) {
			writeWebhookError(rw, http.StatusUnauthorized, whErrUnauthorized)
			return
		}
		switch w.state.replay.record(strings.TrimSpace(sig), at, now) {
		case webhookReplayed:
			writeWebhookError(rw, http.StatusConflict, whErrReplayed)
			return
		case webhookReplayFull:
			writeWebhookError(rw, http.StatusTooManyRequests, whErrQueueFull)
			return
		}
		msg, reason := parseWebhookRequest(body)
		if reason != "" {
			writeWebhookError(rw, http.StatusBadRequest, reason)
			return
		}
		if ctx.Err() != nil {
			writeWebhookError(rw, http.StatusServiceUnavailable, whErrShutdown)
			return
		}
		if !w.enqueue(ctx, msg, inbound) {
			writeWebhookError(rw, http.StatusTooManyRequests, whErrQueueFull)
			return
		}
		rw.WriteHeader(http.StatusAccepted)
	}
}

// writeWebhookError answers a refused request with its status and a small
// JSON body naming the reason.
func writeWebhookError(rw http.ResponseWriter, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(map[string]string{"error": code})
}

// maxWebhookBodyBytes bounds a request body: the largest audio and image the
// gateway accepts, both base64-encoded inline, plus 1 MB for the text and
// the JSON around them.
func maxWebhookBodyBytes() int64 {
	return base64Len(maxAudioBytes()) + base64Len(maxImageBytes()) + 1<<20
}

func base64Len(n int64) int64 { return (n + 2) / 3 * 4 }

// enqueue hands an accepted message to its chat's lane, starting the lane
// when it is idle. It reports false when the lane is full.
func (w *WebhookAdapter) enqueue(ctx context.Context, msg InboundMessage, inbound chan<- InboundMessage) bool {
	w.state.lanesMu.Lock()
	defer w.state.lanesMu.Unlock()
	lane, running := w.state.lanes[msg.ChatID]
	if !running {
		lane = make(chan InboundMessage, webhookLaneDepth)
		w.state.lanes[msg.ChatID] = lane
	}
	select {
	case lane <- msg:
	default:
		return false
	}
	if !running {
		go w.drainLane(ctx, msg.ChatID, lane, inbound)
	}
	return true
}

// drainLane delivers one chat's messages in order: it downloads any
// attachment by URL, then passes the message to the gateway. It exits once
// the lane is empty; the next message for the chat starts a new one.
func (w *WebhookAdapter) drainLane(ctx context.Context, chatID string, lane chan InboundMessage, inbound chan<- InboundMessage) {
	for {
		w.state.lanesMu.Lock()
		if len(lane) == 0 {
			delete(w.state.lanes, chatID)
			w.state.lanesMu.Unlock()
			return
		}
		w.state.lanesMu.Unlock()

		msg := <-lane
		w.hydrateAudio(ctx, &msg)
		w.hydrateImages(ctx, &msg)
		select {
		case inbound <- msg:
		case <-ctx.Done():
			w.state.lanesMu.Lock()
			delete(w.state.lanes, chatID)
			w.state.lanesMu.Unlock()
			return
		}
	}
}

// Start runs the HTTP server until ctx is canceled.
func (w *WebhookAdapter) Start(ctx context.Context, inbound chan<- InboundMessage) error {
	if err := w.preflight(); err != nil {
		return err
	}
	if w.callbackURL == "" {
		w.logger.Warn("gateway/webhook: CHATCLI_WEBHOOK_CALLBACK_URL is not set; requests are accepted but replies are not delivered anywhere")
	}
	mux := http.NewServeMux()
	mux.HandleFunc(w.path, w.inboundHandler(ctx, inbound))

	srv := &http.Server{Addr: w.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		// Derive from ctx (preserving its values) but detach from its
		// cancellation — it's already done — so the 5s graceful-shutdown
		// window actually applies. Satisfies contextcheck / gosec G118.
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	w.logger.Info("gateway/webhook: listening", zap.String("addr", w.addr), zap.String("path", w.path))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Send POSTs the reply to the configured callback URL. A final or proactive
// message is retried on transient failures (network errors, 408, 429, 5xx);
// the "working" notice and progress updates get one attempt, so a slow
// receiver never holds the agent back. Every attempt of one message carries
// the same X-ChatCLI-Delivery-ID.
func (w *WebhookAdapter) Send(ctx context.Context, msg OutboundMessage) error {
	if w.callbackURL == "" {
		// No callback configured: nothing to deliver to (inbound-only mode).
		return nil
	}
	// Send is also reached without Start (the @send tool), so the transport
	// and signing rules are enforced here too.
	if err := w.preflight(); err != nil {
		return err
	}
	out := map[string]string{"chat_id": msg.ChatID, "text": msg.Text}
	if msg.Kind != "" {
		out["kind"] = msg.Kind
	}
	// Image reply: deliver the picture inline as base64 alongside the text so a
	// generic consumer can render it. The text is never dropped.
	if msg.Image != nil && len(msg.Image.Data) > 0 {
		out["image_b64"] = base64.StdEncoding.EncodeToString(msg.Image.Data)
		if msg.Image.Mime != "" {
			out["image_mime"] = msg.Image.Mime
		}
		if msg.Image.FileName != "" {
			out["image_filename"] = msg.Image.FileName
		}
	}
	payload, _ := json.Marshal(out)
	deliveryID := newDeliveryID()

	var backoff []time.Duration
	if msg.Kind != OutboundThinking && msg.Kind != OutboundProgress {
		backoff = w.state.retryBackoff
	}
	for attempt := 1; ; attempt++ {
		retry, err := w.postCallback(ctx, payload, deliveryID, attempt)
		if err == nil || !retry || attempt > len(backoff) {
			return err
		}
		w.logger.Warn("gateway/webhook: callback failed, retrying",
			zap.String("chat_id", msg.ChatID),
			zap.String("delivery_id", deliveryID),
			zap.Int("attempt", attempt),
			zap.Error(err))
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff[attempt-1]):
		}
	}
}

// postCallback makes one delivery attempt. retry reports whether the failure
// is worth another try.
func (w *WebhookAdapter) postCallback(ctx context.Context, payload []byte, deliveryID string, attempt int) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.callbackURL, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Signed, never the raw secret: the secret stays off the wire, so a
	// proxy, a load balancer or an access log in front of the receiver
	// never sees the credential that authenticates inbound requests. Each
	// attempt is signed afresh so a retry carries a current timestamp.
	ts := strconv.FormatInt(w.state.now().Unix(), 10)
	req.Header.Set(webhookTimestampHeader, ts)
	req.Header.Set(webhookSignatureHeader, signWebhook(w.secret, ts, payload))
	req.Header.Set("X-ChatCLI-Delivery-ID", deliveryID)
	req.Header.Set("X-ChatCLI-Delivery-Attempt", strconv.Itoa(attempt))
	resp, err := doHTTP(w.http, req)
	if err != nil {
		return ctx.Err() == nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		transient := resp.StatusCode >= 500 ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusRequestTimeout
		return transient, fmt.Errorf("webhook callback status %d", resp.StatusCode)
	}
	return false, nil
}

// newDeliveryID returns a random id shared by every attempt of one callback.
func newDeliveryID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// parseWebhookInbound validates and normalizes an inbound payload. Inline
// base64 audio is decoded here (no network); an audio_url is recorded for the
// adapter to fetch. A payload is valid with text, inline audio, or an audio URL.
func parseWebhookInbound(body []byte) (InboundMessage, bool) {
	msg, reason := parseWebhookRequest(body)
	return msg, reason == ""
}

// parseWebhookRequest is parseWebhookInbound with the refusal reason: an
// empty reason means the message is valid. A request without user_id is
// attributed to its chat_id, so anonymous callers in different chats are told
// apart wherever the gateway keys conversations by sender.
func parseWebhookRequest(body []byte) (InboundMessage, string) {
	var in webhookInbound
	if err := json.Unmarshal(body, &in); err != nil {
		return InboundMessage{}, whErrInvalidJSON
	}
	if strings.TrimSpace(in.ChatID) == "" {
		return InboundMessage{}, whErrNoChatID
	}

	var audio *InboundAudio
	switch {
	case strings.TrimSpace(in.AudioB64) != "":
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.AudioB64))
		if err != nil || len(data) == 0 {
			return InboundMessage{}, whErrAudioB64
		}
		audio = &InboundAudio{Data: data, MimeType: in.AudioMime}
	case strings.TrimSpace(in.AudioURL) != "":
		audio = &InboundAudio{ref: strings.TrimSpace(in.AudioURL), MimeType: in.AudioMime}
	}

	var image *InboundImage
	switch {
	case strings.TrimSpace(in.ImageB64) != "":
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.ImageB64))
		if err != nil || len(data) == 0 {
			return InboundMessage{}, whErrImageB64
		}
		image = &InboundImage{Data: data, MimeType: in.ImageMime}
	case strings.TrimSpace(in.ImageURL) != "":
		image = &InboundImage{ref: strings.TrimSpace(in.ImageURL), MimeType: in.ImageMime}
	}

	if strings.TrimSpace(in.Text) == "" && audio == nil && image == nil {
		return InboundMessage{}, whErrEmpty
	}
	userID := strings.TrimSpace(in.UserID)
	if userID == "" {
		userID = in.ChatID
	}
	return InboundMessage{
		Platform: webhookPlatform,
		ChatID:   in.ChatID,
		UserID:   userID,
		Text:     in.Text,
		Audio:    audio,
		Image:    image,
	}, ""
}

// hydrateAudio fetches an audio_url attachment (no auth — the caller owns the
// URL). Inline base64 audio already has Data and is left untouched. On failure
// it clears Audio and notes the failure in the text for the agent.
func (w *WebhookAdapter) hydrateAudio(ctx context.Context, msg *InboundMessage) {
	if msg.Audio == nil || len(msg.Audio.Data) > 0 {
		return
	}
	data, mime, err := w.fetchAttachment(ctx, msg.Audio.ref, maxAudioBytes())
	if err != nil {
		w.logger.Warn("gateway/webhook: audio download failed", zap.String("user", msg.UserID), zap.Error(err))
		msg.Audio = nil
		noteFailedAttachment(msg, "audio")
		return
	}
	msg.Audio.Data = data
	if msg.Audio.MimeType == "" {
		msg.Audio.MimeType = mime
	}
}

// hydrateImages fetches an image_url attachment (no auth — the caller owns the
// URL). An inline base64 image already has Data and is left untouched. On
// failure it clears Image and notes the failure in the text for the agent.
func (w *WebhookAdapter) hydrateImages(ctx context.Context, msg *InboundMessage) {
	if msg.Image == nil || len(msg.Image.Data) > 0 {
		return
	}
	data, mime, err := w.fetchAttachment(ctx, msg.Image.ref, maxImageBytes())
	if err != nil {
		w.logger.Warn("gateway/webhook: image download failed", zap.String("user", msg.UserID), zap.Error(err))
		msg.Image = nil
		noteFailedAttachment(msg, "image")
		return
	}
	msg.Image.Data = data
	if msg.Image.MimeType == "" {
		msg.Image.MimeType = mime
	}
}

// fetchAttachment downloads an attachment URL the caller supplied. The URL
// is chosen by whoever sends the request, so it is held to the netguard
// rules: https only, public addresses only (checked again after DNS
// resolution and on every redirect hop), no proxy.
func (w *WebhookAdapter) fetchAttachment(ctx context.Context, ref string, limit int64) ([]byte, string, error) {
	if _, err := netguard.CheckURL(ref); err != nil {
		return nil, "", err
	}
	return fetchAudioBytes(ctx, w.state.media, ref, "", limit)
}

// noteFailedAttachment tells the agent that an attachment sent by URL never
// arrived, so the reply says so instead of answering as if nothing had been
// attached. The message still runs: a request was already accepted with 202,
// and the reply is the only channel left to report the problem. The note is
// deliberately generic: the reason (refused address, status, network error)
// goes to the operator's log only, so the reply cannot be used to map what
// the host can reach.
func noteFailedAttachment(msg *InboundMessage, kind string) {
	note := fmt.Sprintf("[The %s attached to this message by URL could not be downloaded. Tell the user it was not received and ask them to send it again, inline or from a public https URL.]", kind)
	if strings.TrimSpace(msg.Text) == "" {
		msg.Text = note
		return
	}
	msg.Text += "\n\n" + note
}

func init() {
	RegisterBuilder(webhookPlatform, func() (Adapter, error) {
		addr := strings.TrimSpace(os.Getenv("CHATCLI_WEBHOOK_ADDR"))
		if addr == "" {
			return nil, nil
		}
		return NewWebhookAdapter(
			addr,
			os.Getenv("CHATCLI_WEBHOOK_PATH"),
			os.Getenv("CHATCLI_WEBHOOK_SECRET"),
			os.Getenv("CHATCLI_WEBHOOK_CALLBACK_URL"),
			zap.NewNop(),
		), nil
	})
}
