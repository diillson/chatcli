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
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const whatsappPlatform = "whatsapp"

// WhatsAppAdapter integrates with the WhatsApp Cloud API: inbound via the
// Meta webhook (GET verification handshake + POST message events) and
// outbound via the Graph API messages endpoint. Plain HTTP, no SDK.
type WhatsAppAdapter struct {
	accessToken string
	phoneID     string
	verifyToken string
	addr        string
	path        string
	graphBase   string // overridable for tests; defaults to https://graph.facebook.com/v21.0
	http        *http.Client
	logger      *zap.Logger
}

// NewWhatsAppAdapter builds a WhatsApp Cloud API adapter.
func NewWhatsAppAdapter(accessToken, phoneID, verifyToken, addr, path string, logger *zap.Logger) *WhatsAppAdapter {
	if path == "" {
		path = "/whatsapp/webhook"
	}
	return &WhatsAppAdapter{
		accessToken: accessToken,
		phoneID:     phoneID,
		verifyToken: verifyToken,
		addr:        addr,
		path:        path,
		graphBase:   "https://graph.facebook.com/v21.0",
		http:        &http.Client{Timeout: 15 * time.Second},
		logger:      logger,
	}
}

// Name implements Adapter.
func (a *WhatsAppAdapter) Name() string { return whatsappPlatform }

// SetLogger implements LoggerAware: inject the daemon logger and trace the
// HTTP client's calls to the WhatsApp Cloud API.
func (a *WhatsAppAdapter) SetLogger(l *zap.Logger) {
	a.logger = l
	a.http = newLoggingClient(a.http, l, whatsappPlatform)
}

// webhookHandler builds the Cloud API webhook handler (GET verification +
// POST messages). Extracted from Start so it can be exercised via httptest.
func (a *WhatsAppAdapter) webhookHandler(ctx context.Context, inbound chan<- InboundMessage) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Meta verification handshake.
			q := r.URL.Query()
			if q.Get("hub.mode") == "subscribe" &&
				subtle.ConstantTimeCompare([]byte(q.Get("hub.verify_token")), []byte(a.verifyToken)) == 1 {
				// Meta's verification handshake: hub.challenge is an integer the
				// endpoint echoes back. Parsing it (and formatting it fresh)
				// matches the spec and breaks the request→response taint flow
				// that gosec flags as an XSS vector (G705). Served as plain text.
				if ch, convErr := strconv.Atoi(strings.TrimSpace(q.Get("hub.challenge"))); convErr == nil {
					rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
					rw.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(rw, strconv.Itoa(ch))
					return
				}
			}
			rw.WriteHeader(http.StatusForbidden)
		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			rw.WriteHeader(http.StatusOK) // ack fast; Meta retries on non-200
			for _, msg := range parseWhatsAppInbound(body) {
				a.hydrateAudio(ctx, &msg)
				a.hydrateImages(ctx, &msg)
				if strings.TrimSpace(msg.Text) == "" && msg.Audio == nil && len(msg.Images) == 0 {
					continue // audio/image download failed and there was no text
				}
				select {
				case inbound <- msg:
				case <-ctx.Done():
					return
				}
			}
		default:
			rw.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// Start runs the webhook HTTP server until ctx is canceled.
func (a *WhatsAppAdapter) Start(ctx context.Context, inbound chan<- InboundMessage) error {
	mux := http.NewServeMux()
	mux.HandleFunc(a.path, a.webhookHandler(ctx, inbound))

	srv := &http.Server{Addr: a.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		// Derive from ctx (preserving its values) but detach from its
		// cancellation — it's already done — so the 5s graceful-shutdown
		// window actually applies. Satisfies contextcheck / gosec G118.
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	a.logger.Info("gateway/whatsapp: listening", zap.String("addr", a.addr), zap.String("path", a.path))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Send delivers a reply via the Graph API.
func (a *WhatsAppAdapter) Send(ctx context.Context, msg OutboundMessage) error {
	// Image reply: when a picture is attached, upload it to the media endpoint
	// then send an image message referencing the returned id, with the text as
	// caption. Falls back to text on any failure so a reply is never lost.
	if msg.Image != nil && len(msg.Image.Data) > 0 {
		if err := a.sendPhoto(ctx, msg); err != nil {
			a.logger.Warn("whatsapp: photo send failed, falling back to text", zap.Error(err))
		} else {
			return nil
		}
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                msg.ChatID,
		"type":              "text",
		"text":              map[string]string{"body": msg.Text},
	})
	endpoint := fmt.Sprintf("%s/%s/messages", a.graphBase, a.phoneID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.accessToken)
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("whatsapp send status %d", resp.StatusCode)
	}
	return nil
}

// sendPhoto delivers an image reply via the two-step Cloud API flow: first
// upload the bytes to the media endpoint to obtain a media id, then send an
// image message referencing the id with the text as caption.
func (a *WhatsAppAdapter) sendPhoto(ctx context.Context, msg OutboundMessage) error {
	mediaID, err := a.uploadMedia(ctx, msg.Image)
	if err != nil {
		return err
	}
	image := map[string]string{"id": mediaID}
	if strings.TrimSpace(msg.Text) != "" {
		image["caption"] = msg.Text
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                msg.ChatID,
		"type":              "image",
		"image":             image,
	})
	endpoint := fmt.Sprintf("%s/%s/messages", a.graphBase, a.phoneID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.accessToken)
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("whatsapp send image status %d", resp.StatusCode)
	}
	return nil
}

// uploadMedia POSTs the image bytes to the /{phone-id}/media endpoint and
// returns the resulting media id. Filename defaults to "reply.png" and the
// content type to "image/png" when none is supplied.
func (a *WhatsAppAdapter) uploadMedia(ctx context.Context, img *OutboundImage) (string, error) {
	filename := img.FileName
	if filename == "" {
		filename = "reply.png"
	}
	mime := img.Mime
	if mime == "" {
		mime = "image/png"
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("messaging_product", "whatsapp")
	_ = w.WriteField("type", mime)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", mime)
	part, err := w.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(img.Data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf("%s/%s/media", a.graphBase, a.phoneID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+a.accessToken)
	resp, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("whatsapp media upload status %d", resp.StatusCode)
	}
	var up struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &up); err != nil {
		return "", err
	}
	if up.ID == "" {
		return "", fmt.Errorf("whatsapp media upload: empty id")
	}
	return up.ID, nil
}

// whatsAppWebhook mirrors the Cloud API webhook payload (text + audio).
type whatsAppWebhook struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Messages []struct {
					From string `json:"from"`
					Type string `json:"type"`
					Text struct {
						Body string `json:"body"`
					} `json:"text"`
					Audio *struct {
						ID       string `json:"id"`
						MimeType string `json:"mime_type"`
					} `json:"audio"`
					Image *struct {
						ID       string `json:"id"`
						MimeType string `json:"mime_type"`
					} `json:"image"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// parseWhatsAppInbound extracts text and audio messages from a Cloud API
// webhook body. Status callbacks and other message types are ignored. Audio
// bytes are NOT fetched here (the parser stays network-free); the adapter
// resolves the media id to bytes via hydrateAudio before dispatch.
func parseWhatsAppInbound(body []byte) []InboundMessage {
	var w whatsAppWebhook
	if err := json.Unmarshal(body, &w); err != nil {
		return nil
	}
	var out []InboundMessage
	for _, e := range w.Entry {
		for _, c := range e.Changes {
			for _, m := range c.Value.Messages {
				if m.From == "" {
					continue
				}
				switch {
				case m.Type == "text" && strings.TrimSpace(m.Text.Body) != "":
					out = append(out, InboundMessage{
						Platform: whatsappPlatform,
						ChatID:   m.From,
						UserID:   m.From,
						Text:     m.Text.Body,
					})
				case m.Type == "audio" && m.Audio != nil && m.Audio.ID != "":
					out = append(out, InboundMessage{
						Platform: whatsappPlatform,
						ChatID:   m.From,
						UserID:   m.From,
						Audio:    &InboundAudio{ref: m.Audio.ID, MimeType: m.Audio.MimeType},
					})
				case m.Type == "image" && m.Image != nil && m.Image.ID != "":
					out = append(out, InboundMessage{
						Platform: whatsappPlatform,
						ChatID:   m.From,
						UserID:   m.From,
						Images:   []*InboundImage{{ref: m.Image.ID, MimeType: m.Image.MimeType}},
					})
				}
			}
		}
	}
	return out
}

// hydrateAudio resolves a WhatsApp media id to its bytes via the two-step Graph
// flow (GET /{media-id} → media URL → GET URL), authenticated with the access
// token. On failure it clears Audio so the message is dropped downstream.
func (a *WhatsAppAdapter) hydrateAudio(ctx context.Context, msg *InboundMessage) {
	if msg.Audio == nil || len(msg.Audio.Data) > 0 {
		return
	}
	data, mime, err := a.downloadMedia(ctx, msg.Audio.ref, maxAudioBytes())
	if err != nil {
		a.logger.Warn("gateway/whatsapp: voice download failed", zap.String("user", msg.UserID), zap.Error(err))
		msg.Audio = nil
		return
	}
	msg.Audio.Data = data
	if msg.Audio.MimeType == "" {
		msg.Audio.MimeType = mime
	}
}

// hydrateImages resolves each WhatsApp image media id to its bytes via the same
// two-step Graph flow as audio. Attachments that can't be fetched are dropped.
func (a *WhatsAppAdapter) hydrateImages(ctx context.Context, msg *InboundMessage) {
	if len(msg.Images) == 0 {
		return
	}
	kept := msg.Images[:0]
	for _, img := range msg.Images {
		if len(img.Data) > 0 {
			kept = append(kept, img)
			continue
		}
		data, mime, err := a.downloadMedia(ctx, img.ref, maxImageBytes())
		if err != nil {
			a.logger.Warn("gateway/whatsapp: image download failed", zap.String("user", msg.UserID), zap.Error(err))
			continue
		}
		img.Data = data
		if img.MimeType == "" {
			img.MimeType = mime
		}
		kept = append(kept, img)
	}
	msg.Images = kept
}

// downloadMedia performs the Graph media lookup then fetches the bytes. limit
// caps the download (audio and image use different ceilings).
func (a *WhatsAppAdapter) downloadMedia(ctx context.Context, mediaID string, limit int64) ([]byte, string, error) {
	endpoint := fmt.Sprintf("%s/%s", a.graphBase, mediaID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.accessToken)
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("whatsapp media lookup status %d", resp.StatusCode)
	}
	var lookup struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if err := json.Unmarshal(body, &lookup); err != nil {
		return nil, "", err
	}
	if lookup.URL == "" {
		return nil, "", fmt.Errorf("whatsapp media lookup: empty url")
	}
	data, ctype, err := fetchAudioBytes(ctx, a.http, lookup.URL, a.accessToken, limit)
	if err != nil {
		return nil, "", err
	}
	if lookup.MimeType != "" {
		ctype = lookup.MimeType
	}
	return data, ctype, nil
}

func init() {
	RegisterBuilder(whatsappPlatform, func() (Adapter, error) {
		token := strings.TrimSpace(os.Getenv("CHATCLI_WHATSAPP_ACCESS_TOKEN"))
		phoneID := strings.TrimSpace(os.Getenv("CHATCLI_WHATSAPP_PHONE_ID"))
		addr := strings.TrimSpace(os.Getenv("CHATCLI_WHATSAPP_ADDR"))
		if token == "" || phoneID == "" || addr == "" {
			return nil, nil
		}
		return NewWhatsAppAdapter(
			token, phoneID,
			os.Getenv("CHATCLI_WHATSAPP_VERIFY_TOKEN"),
			addr,
			os.Getenv("CHATCLI_WHATSAPP_PATH"),
			zap.NewNop(),
		), nil
	})
}
