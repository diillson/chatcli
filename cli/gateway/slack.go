/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const slackPlatform = "slack"

// SlackAdapter integrates with Slack via the Events API (inbound HTTP) and
// chat.postMessage (outbound), over plain HTTP — no SDK. Inbound requests are
// verified with the Slack signing secret (HMAC-SHA256) and a timestamp
// freshness check, per Slack's security guidance.
type SlackAdapter struct {
	botToken      string
	signingSecret string
	addr          string
	path          string
	apiBase       string // overridable for tests; defaults to https://slack.com/api
	http          *http.Client
	logger        *zap.Logger
}

// NewSlackAdapter builds a Slack adapter.
func NewSlackAdapter(botToken, signingSecret, addr, path string, logger *zap.Logger) *SlackAdapter {
	if path == "" {
		path = "/slack/events"
	}
	return &SlackAdapter{
		botToken:      botToken,
		signingSecret: signingSecret,
		addr:          addr,
		path:          path,
		apiBase:       "https://slack.com/api",
		http:          &http.Client{Timeout: 15 * time.Second},
		logger:        logger,
	}
}

// Name implements Adapter.
func (s *SlackAdapter) Name() string { return slackPlatform }

// SetLogger implements LoggerAware: inject the daemon logger and trace the
// HTTP client's calls to the Slack API.
func (s *SlackAdapter) SetLogger(l *zap.Logger) {
	s.logger = l
	s.http = newLoggingClient(s.http, l, slackPlatform)
}

// eventsHandler builds the HTTP handler for the Slack Events API. Extracted
// from Start so it can be exercised directly via httptest.
func (s *SlackAdapter) eventsHandler(ctx context.Context, inbound chan<- InboundMessage) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			rw.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

		// SecOps: verify the request actually came from Slack.
		if s.signingSecret != "" {
			ts := r.Header.Get("X-Slack-Request-Timestamp")
			sig := r.Header.Get("X-Slack-Signature")
			if !verifySlackSignature(s.signingSecret, ts, body, sig) {
				rw.WriteHeader(http.StatusUnauthorized)
				return
			}
		}

		challenge, msg, hasMsg, err := parseSlackEvent(body)
		if err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		if challenge != "" { // url_verification handshake
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"challenge":"` + challenge + `"}`))
			return
		}
		rw.WriteHeader(http.StatusOK) // ack fast; Slack retries on slow/!200
		if hasMsg {
			s.hydrateAudio(ctx, &msg)
			s.hydrateImages(ctx, &msg)
			if strings.TrimSpace(msg.Text) == "" && msg.Audio == nil && len(msg.Images) == 0 {
				return // audio/image download failed and there was no text
			}
			select {
			case inbound <- msg:
			case <-ctx.Done():
			}
		}
	}
}

// Start runs the Events API HTTP server until ctx is canceled.
func (s *SlackAdapter) Start(ctx context.Context, inbound chan<- InboundMessage) error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.eventsHandler(ctx, inbound))

	srv := &http.Server{Addr: s.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		// Derive from ctx (preserving its values) but detach from its
		// cancellation — it's already done — so the 5s graceful-shutdown
		// window actually applies. Satisfies contextcheck / gosec G118.
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	s.logger.Info("gateway/slack: listening", zap.String("addr", s.addr), zap.String("path", s.path))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Send posts a reply via chat.postMessage.
func (s *SlackAdapter) Send(ctx context.Context, msg OutboundMessage) error {
	// Image reply: when a picture is attached, upload it via files.upload with
	// the text as the initial comment. Falls back to text on any failure so a
	// reply is never lost.
	if msg.Image != nil && len(msg.Image.Data) > 0 {
		if err := s.sendPhoto(ctx, msg); err != nil {
			s.logger.Warn("slack: photo send failed, falling back to text", zap.Error(err))
		} else {
			return nil
		}
	}
	payload, _ := json.Marshal(map[string]string{"channel": msg.ChatID, "text": msg.Text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase+"/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.botToken)
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack chat.postMessage status %d", resp.StatusCode)
	}
	return nil
}

// sendPhoto uploads the image via the files.upload API: a multipart request
// carrying the channel, the file bytes, a filename and the text as the
// initial_comment. The bot token authenticates the call. Filename defaults to
// "reply.png" when none is supplied.
func (s *SlackAdapter) sendPhoto(ctx context.Context, msg OutboundMessage) error {
	filename := msg.Image.FileName
	if filename == "" {
		filename = "reply.png"
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("channels", msg.ChatID)
	if strings.TrimSpace(msg.Text) != "" {
		_ = w.WriteField("initial_comment", msg.Text)
	}
	_ = w.WriteField("filename", filename)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(msg.Image.Data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase+"/files.upload", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.botToken)
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack files.upload status %d", resp.StatusCode)
	}
	// Slack always returns 200; success is signaled by the "ok" field.
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("slack files.upload error: %s", res.Error)
	}
	return nil
}

// verifySlackSignature checks the X-Slack-Signature HMAC and rejects stale
// timestamps (replay protection, 5-minute window).
func verifySlackSignature(secret, timestamp string, body []byte, signature string) bool {
	if timestamp == "" || signature == "" {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if d := time.Since(time.Unix(ts, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

type slackEventEnvelope struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Event     struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		User    string `json:"user"`
		Text    string `json:"text"`
		BotID   string `json:"bot_id"`
		SubType string `json:"subtype"`
		Files   []struct {
			Mimetype   string `json:"mimetype"`
			URLPrivate string `json:"url_private"`
			Name       string `json:"name"`
		} `json:"files"`
	} `json:"event"`
}

// parseSlackEvent handles url_verification and event_callback message events.
// For a handshake it returns the challenge string. For a user message it
// returns hasMsg=true with the normalized message. Bot messages and non-text
// subtypes are ignored to prevent reply loops.
func parseSlackEvent(body []byte) (challenge string, msg InboundMessage, hasMsg bool, err error) {
	var env slackEventEnvelope
	if uerr := json.Unmarshal(body, &env); uerr != nil {
		return "", InboundMessage{}, false, uerr
	}
	if env.Type == "url_verification" {
		return env.Challenge, InboundMessage{}, false, nil
	}
	e := env.Event
	if e.Type != "message" || e.BotID != "" {
		return "", InboundMessage{}, false, nil
	}
	// A voice memo / audio file arrives as a "file_share" subtype carrying
	// files[]; pick the first audio file. Image files (photos, screenshots) are
	// collected too. Their bytes are fetched later.
	var audio *InboundAudio
	var images []*InboundImage
	for _, f := range e.Files {
		if f.URLPrivate == "" {
			continue
		}
		switch {
		case audio == nil && isAudioMime(f.Mimetype):
			audio = &InboundAudio{ref: f.URLPrivate, MimeType: f.Mimetype, FileName: f.Name}
		case isImageMime(f.Mimetype):
			images = append(images, &InboundImage{ref: f.URLPrivate, MimeType: f.Mimetype, FileName: f.Name})
		}
	}
	// Reject other subtypes (edits, joins, …) and empty messages unless they
	// carry audio or an image.
	if audio == nil && len(images) == 0 && (e.SubType != "" || strings.TrimSpace(e.Text) == "") {
		return "", InboundMessage{}, false, nil
	}
	return "", InboundMessage{
		Platform: slackPlatform,
		ChatID:   e.Channel,
		UserID:   e.User,
		Text:     e.Text,
		Audio:    audio,
		Images:   images,
	}, true, nil
}

// hydrateAudio downloads a Slack file (url_private requires the bot token as a
// bearer). On failure it clears Audio so the message is dropped downstream.
func (s *SlackAdapter) hydrateAudio(ctx context.Context, msg *InboundMessage) {
	if msg.Audio == nil || len(msg.Audio.Data) > 0 {
		return
	}
	data, mime, err := fetchAudioBytes(ctx, s.http, msg.Audio.ref, s.botToken, maxAudioBytes())
	if err != nil {
		s.logger.Warn("gateway/slack: voice download failed", zap.String("user", msg.UserID), zap.Error(err))
		msg.Audio = nil
		return
	}
	msg.Audio.Data = data
	if msg.Audio.MimeType == "" {
		msg.Audio.MimeType = mime
	}
}

// hydrateImages downloads each Slack image file (url_private requires the bot
// token as a bearer, same as audio). Attachments that fail to download are
// dropped.
func (s *SlackAdapter) hydrateImages(ctx context.Context, msg *InboundMessage) {
	if len(msg.Images) == 0 {
		return
	}
	kept := msg.Images[:0]
	for _, img := range msg.Images {
		if len(img.Data) > 0 {
			kept = append(kept, img)
			continue
		}
		data, mime, err := fetchAudioBytes(ctx, s.http, img.ref, s.botToken, maxImageBytes())
		if err != nil {
			s.logger.Warn("gateway/slack: image download failed", zap.String("user", msg.UserID), zap.Error(err))
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

func init() {
	RegisterBuilder(slackPlatform, func() (Adapter, error) {
		token := strings.TrimSpace(os.Getenv("CHATCLI_SLACK_BOT_TOKEN"))
		addr := strings.TrimSpace(os.Getenv("CHATCLI_SLACK_ADDR"))
		if token == "" || addr == "" {
			return nil, nil
		}
		return NewSlackAdapter(
			token,
			os.Getenv("CHATCLI_SLACK_SIGNING_SECRET"),
			addr,
			os.Getenv("CHATCLI_SLACK_PATH"),
			zap.NewNop(),
		), nil
	})
}
