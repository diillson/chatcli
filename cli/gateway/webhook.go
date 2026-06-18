/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

const webhookPlatform = "webhook"

// WebhookAdapter is a generic, platform-agnostic adapter: it runs an HTTP
// server that accepts inbound messages as JSON and delivers replies by
// POSTing to a configured callback URL. Any platform or custom integration
// that can send/receive an HTTP POST can use it — no per-platform code.
//
// Inbound  (POST <path>): {"chat_id":"...", "user_id":"...", "text":"..."}
// Outbound (POST callbackURL): {"chat_id":"...", "text":"..."}
//
// SecOps: an optional shared secret is required in the X-ChatCLI-Secret
// header and compared in constant time.
type WebhookAdapter struct {
	addr        string
	path        string
	secret      string
	callbackURL string
	http        *http.Client
	logger      *zap.Logger
}

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
	}
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
			rw.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !w.authorized(r) {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxAudioBytes()+(1<<20)))
		msg, ok := parseWebhookInbound(body)
		if !ok {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		w.hydrateAudio(ctx, &msg)
		w.hydrateImages(ctx, &msg)
		if strings.TrimSpace(msg.Text) == "" && msg.Audio == nil && msg.Image == nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case inbound <- msg:
			rw.WriteHeader(http.StatusAccepted)
		case <-ctx.Done():
			rw.WriteHeader(http.StatusServiceUnavailable)
		}
	}
}

// Start runs the HTTP server until ctx is canceled.
func (w *WebhookAdapter) Start(ctx context.Context, inbound chan<- InboundMessage) error {
	if w.secret == "" {
		w.logger.Warn("gateway/webhook: no CHATCLI_WEBHOOK_SECRET set — inbound endpoint is unauthenticated")
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

func (w *WebhookAdapter) authorized(r *http.Request) bool {
	if w.secret == "" {
		return true
	}
	got := r.Header.Get("X-ChatCLI-Secret")
	return subtle.ConstantTimeCompare([]byte(got), []byte(w.secret)) == 1
}

// Send POSTs the reply to the configured callback URL.
func (w *WebhookAdapter) Send(ctx context.Context, msg OutboundMessage) error {
	if w.callbackURL == "" {
		// No callback configured: nothing to deliver to (inbound-only mode).
		return nil
	}
	out := map[string]string{"chat_id": msg.ChatID, "text": msg.Text}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.callbackURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.secret != "" {
		req.Header.Set("X-ChatCLI-Secret", w.secret)
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook callback status %d", resp.StatusCode)
	}
	return nil
}

// parseWebhookInbound validates and normalizes an inbound payload. Inline
// base64 audio is decoded here (no network); an audio_url is recorded for the
// adapter to fetch. A payload is valid with text, inline audio, or an audio URL.
func parseWebhookInbound(body []byte) (InboundMessage, bool) {
	var in webhookInbound
	if err := json.Unmarshal(body, &in); err != nil {
		return InboundMessage{}, false
	}
	if strings.TrimSpace(in.ChatID) == "" {
		return InboundMessage{}, false
	}

	var audio *InboundAudio
	switch {
	case strings.TrimSpace(in.AudioB64) != "":
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.AudioB64))
		if err != nil || len(data) == 0 {
			return InboundMessage{}, false
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
			return InboundMessage{}, false
		}
		image = &InboundImage{Data: data, MimeType: in.ImageMime}
	case strings.TrimSpace(in.ImageURL) != "":
		image = &InboundImage{ref: strings.TrimSpace(in.ImageURL), MimeType: in.ImageMime}
	}

	if strings.TrimSpace(in.Text) == "" && audio == nil && image == nil {
		return InboundMessage{}, false
	}
	return InboundMessage{
		Platform: webhookPlatform,
		ChatID:   in.ChatID,
		UserID:   in.UserID,
		Text:     in.Text,
		Audio:    audio,
		Image:    image,
	}, true
}

// hydrateAudio fetches an audio_url attachment (no auth — the caller owns the
// URL). Inline base64 audio already has Data and is left untouched. On failure
// it clears Audio so the message is dropped downstream.
func (w *WebhookAdapter) hydrateAudio(ctx context.Context, msg *InboundMessage) {
	if msg.Audio == nil || len(msg.Audio.Data) > 0 {
		return
	}
	data, mime, err := fetchAudioBytes(ctx, w.http, msg.Audio.ref, "", maxAudioBytes())
	if err != nil {
		w.logger.Warn("gateway/webhook: audio download failed", zap.String("user", msg.UserID), zap.Error(err))
		msg.Audio = nil
		return
	}
	msg.Audio.Data = data
	if msg.Audio.MimeType == "" {
		msg.Audio.MimeType = mime
	}
}

// hydrateImages fetches an image_url attachment (no auth — the caller owns the
// URL). An inline base64 image already has Data and is left untouched. On
// failure it clears Image.
func (w *WebhookAdapter) hydrateImages(ctx context.Context, msg *InboundMessage) {
	if msg.Image == nil || len(msg.Image.Data) > 0 {
		return
	}
	data, mime, err := fetchAudioBytes(ctx, w.http, msg.Image.ref, "", maxImageBytes())
	if err != nil {
		w.logger.Warn("gateway/webhook: image download failed", zap.String("user", msg.UserID), zap.Error(err))
		msg.Image = nil
		return
	}
	msg.Image.Data = data
	if msg.Image.MimeType == "" {
		msg.Image.MimeType = mime
	}
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
