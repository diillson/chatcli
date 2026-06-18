/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const discordPlatform = "discord"

// Discord Gateway opcodes (v10).
const (
	dOpDispatch       = 0
	dOpHeartbeat      = 1
	dOpIdentify       = 2
	dOpReconnect      = 7
	dOpInvalidSession = 9
	dOpHello          = 10
	dOpHeartbeatACK   = 11
)

// Gateway intents: GUILD_MESSAGES | DIRECT_MESSAGES | MESSAGE_CONTENT.
const discordIntents = (1 << 9) | (1 << 12) | (1 << 15)

// DiscordAdapter implements a real Discord Gateway (v10) client over
// WebSocket for receiving messages, plus the REST API for sending. It handles
// the HELLO/heartbeat(+ACK)/IDENTIFY lifecycle and reconnects on failure.
type DiscordAdapter struct {
	token      string
	gatewayURL string // overridable for tests; defaults to wss://gateway.discord.gg
	restBase   string // overridable for tests; defaults to https://discord.com/api/v10
	http       *http.Client
	logger     *zap.Logger
	dialer     *websocket.Dialer
}

// NewDiscordAdapter builds a Discord adapter from a bot token.
func NewDiscordAdapter(token string, logger *zap.Logger) *DiscordAdapter {
	return &DiscordAdapter{
		token:      token,
		gatewayURL: "wss://gateway.discord.gg/?v=10&encoding=json",
		restBase:   "https://discord.com/api/v10",
		http:       &http.Client{Timeout: 15 * time.Second},
		logger:     logger,
		dialer:     websocket.DefaultDialer,
	}
}

// Name implements Adapter.
func (d *DiscordAdapter) Name() string { return discordPlatform }

// SetLogger implements LoggerAware: inject the daemon logger and trace the
// HTTP client's calls to the Discord API.
func (d *DiscordAdapter) SetLogger(l *zap.Logger) {
	d.logger = l
	d.http = newLoggingClient(d.http, l, discordPlatform)
}

// Start connects to the gateway and streams messages until ctx is canceled,
// reconnecting with backoff on transient failures.
func (d *DiscordAdapter) Start(ctx context.Context, inbound chan<- InboundMessage) error {
	d.logger.Info("gateway/discord: starting")
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := d.runSession(ctx, inbound)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			d.logger.Warn("gateway/discord: session ended, reconnecting", zap.Error(err), zap.Duration("backoff", backoff))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// cryptoFloat64 returns a uniform float64 in [0,1) sourced from crypto/rand.
// Used for heartbeat jitter so the weak-RNG check (gosec G404) stays satisfied
// without importing math/rand.
func cryptoFloat64() float64 {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return 0.5 // jitter is best-effort; fall back to mid-interval
	}
	return float64(binary.BigEndian.Uint64(b[:])>>11) / float64(uint64(1)<<53)
}

// runSession runs one connection lifecycle: connect, hello, identify,
// heartbeat, dispatch — returning when the connection drops or ctx ends.
func (d *DiscordAdapter) runSession(ctx context.Context, inbound chan<- InboundMessage) error {
	conn, resp, err := d.dialer.DialContext(ctx, d.gatewayURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close() // handshake response body; unused
	}
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var writeMu sync.Mutex // gorilla forbids concurrent writers
	writeJSON := func(v interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(v)
	}

	// First frame must be HELLO.
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	hb, err := parseDiscordHelloInterval(raw)
	if err != nil {
		return err
	}

	// IDENTIFY.
	if err := writeJSON(discordIdentify(d.token)); err != nil {
		return fmt.Errorf("identify: %w", err)
	}

	// Heartbeat loop.
	var seq seqHolder
	go func() {
		ticker := time.NewTicker(time.Duration(hb) * time.Millisecond)
		defer ticker.Stop()
		// Jitter the first beat per Discord guidance.
		select {
		case <-sessionCtx.Done():
			return
		case <-time.After(time.Duration(float64(hb)*cryptoFloat64()) * time.Millisecond):
		}
		for {
			if err := writeJSON(map[string]interface{}{"op": dOpHeartbeat, "d": seq.get()}); err != nil {
				cancel()
				return
			}
			select {
			case <-sessionCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	// Dispatch loop.
	for {
		if sessionCtx.Err() != nil {
			return nil
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		op, s, t, dData := decodeDiscordFrame(raw)
		if s != nil {
			seq.set(*s)
		}
		switch op {
		case dOpReconnect, dOpInvalidSession:
			return fmt.Errorf("gateway requested reconnect (op %d)", op)
		case dOpDispatch:
			if t == "MESSAGE_CREATE" {
				if msg, ok := parseDiscordMessage(dData); ok {
					d.hydrateAudio(sessionCtx, &msg)
					d.hydrateImages(sessionCtx, &msg)
					if strings.TrimSpace(msg.Text) == "" && msg.Audio == nil && len(msg.Images) == 0 {
						continue // audio/image download failed and there was no text
					}
					select {
					case inbound <- msg:
					case <-sessionCtx.Done():
						return nil
					}
				}
			}
		}
	}
}

// Send delivers a reply via the REST API.
func (d *DiscordAdapter) Send(ctx context.Context, msg OutboundMessage) error {
	// Image reply: when a picture is attached, upload it as a file with the text
	// as the message content. Falls back to text on any failure so a reply is
	// never lost.
	if msg.Image != nil && len(msg.Image.Data) > 0 {
		if err := d.sendPhoto(ctx, msg); err != nil {
			d.logger.Warn("discord: photo send failed, falling back to text", zap.Error(err))
		} else {
			return nil
		}
	}
	payload, _ := json.Marshal(map[string]string{"content": msg.Text})
	endpoint := fmt.Sprintf("%s/channels/%s/messages", d.restBase, msg.ChatID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+d.token)
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord send status %d", resp.StatusCode)
	}
	return nil
}

// sendPhoto uploads the image to the channel via a multipart message: a
// files[0] part carries the bytes and a payload_json part carries the text as
// the message content. Filename defaults to "reply.png" when none is supplied.
func (d *DiscordAdapter) sendPhoto(ctx context.Context, msg OutboundMessage) error {
	filename := msg.Image.FileName
	if filename == "" {
		filename = "reply.png"
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	jsonPayload, _ := json.Marshal(map[string]string{"content": msg.Text})
	if err := w.WriteField("payload_json", string(jsonPayload)); err != nil {
		return err
	}
	part, err := w.CreateFormFile("files[0]", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(msg.Image.Data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/channels/%s/messages", d.restBase, msg.ChatID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bot "+d.token)
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord send photo status %d", resp.StatusCode)
	}
	return nil
}

// seqHolder is the last sequence number, shared with the heartbeat goroutine.
type seqHolder struct {
	mu sync.Mutex
	s  int64
	ok bool
}

func (h *seqHolder) set(v int64) { h.mu.Lock(); h.s, h.ok = v, true; h.mu.Unlock() }
func (h *seqHolder) get() interface{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ok {
		return nil
	}
	return h.s
}

// --- pure helpers (unit-testable) ---

func discordIdentify(token string) map[string]interface{} {
	return map[string]interface{}{
		"op": dOpIdentify,
		"d": map[string]interface{}{
			"token":   token,
			"intents": discordIntents,
			"properties": map[string]string{
				"os":      "linux",
				"browser": "chatcli",
				"device":  "chatcli",
			},
		},
	}
}

type discordFrame struct {
	Op int             `json:"op"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
	D  json.RawMessage `json:"d"`
}

func decodeDiscordFrame(raw []byte) (op int, s *int64, t string, d json.RawMessage) {
	var f discordFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return -1, nil, "", nil
	}
	return f.Op, f.S, f.T, f.D
}

func parseDiscordHelloInterval(raw []byte) (int, error) {
	op, _, _, d := decodeDiscordFrame(raw)
	if op != dOpHello {
		return 0, fmt.Errorf("expected HELLO (op 10), got op %d", op)
	}
	var hello struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(d, &hello); err != nil || hello.HeartbeatInterval <= 0 {
		return 0, fmt.Errorf("invalid hello payload")
	}
	return hello.HeartbeatInterval, nil
}

type discordMessageData struct {
	ChannelID string `json:"channel_id"`
	Content   string `json:"content"`
	Author    struct {
		ID       string `json:"id"`
		Bot      bool   `json:"bot"`
		Username string `json:"username"`
	} `json:"author"`
	Attachments []struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
		Filename    string `json:"filename"`
	} `json:"attachments"`
}

// parseDiscordMessage extracts a normalized message from a MESSAGE_CREATE
// payload. Bot-authored messages are skipped to prevent loops. A message with
// no text but an audio attachment is kept (the attachment is fetched later).
func parseDiscordMessage(d json.RawMessage) (InboundMessage, bool) {
	var m discordMessageData
	if err := json.Unmarshal(d, &m); err != nil {
		return InboundMessage{}, false
	}
	if m.Author.Bot || m.ChannelID == "" {
		return InboundMessage{}, false
	}
	var audio *InboundAudio
	var images []*InboundImage
	for _, at := range m.Attachments {
		if at.URL == "" {
			continue
		}
		switch {
		case audio == nil && isAudioMime(at.ContentType):
			audio = &InboundAudio{ref: at.URL, MimeType: at.ContentType, FileName: at.Filename}
		case isImageMime(at.ContentType):
			images = append(images, &InboundImage{ref: at.URL, MimeType: at.ContentType, FileName: at.Filename})
		}
	}
	if strings.TrimSpace(m.Content) == "" && audio == nil && len(images) == 0 {
		return InboundMessage{}, false
	}
	return InboundMessage{
		Platform: discordPlatform,
		ChatID:   m.ChannelID,
		UserID:   m.Author.ID,
		UserName: m.Author.Username,
		Text:     m.Content,
		Audio:    audio,
		Images:   images,
	}, true
}

// hydrateAudio downloads a Discord audio attachment from its CDN URL (already
// signed — no auth header needed). On failure it clears Audio.
func (d *DiscordAdapter) hydrateAudio(ctx context.Context, msg *InboundMessage) {
	if msg.Audio == nil || len(msg.Audio.Data) > 0 {
		return
	}
	data, mime, err := fetchAudioBytes(ctx, d.http, msg.Audio.ref, "", maxAudioBytes())
	if err != nil {
		d.logger.Warn("gateway/discord: voice download failed", zap.String("user", msg.UserID), zap.Error(err))
		msg.Audio = nil
		return
	}
	msg.Audio.Data = data
	if msg.Audio.MimeType == "" {
		msg.Audio.MimeType = mime
	}
}

// hydrateImages downloads each Discord image attachment from its CDN URL
// (already signed — no auth header needed). Attachments that fail to download
// are dropped.
func (d *DiscordAdapter) hydrateImages(ctx context.Context, msg *InboundMessage) {
	if len(msg.Images) == 0 {
		return
	}
	kept := msg.Images[:0]
	for _, img := range msg.Images {
		if len(img.Data) > 0 {
			kept = append(kept, img)
			continue
		}
		data, mime, err := fetchAudioBytes(ctx, d.http, img.ref, "", maxImageBytes())
		if err != nil {
			d.logger.Warn("gateway/discord: image download failed", zap.String("user", msg.UserID), zap.Error(err))
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
	RegisterBuilder(discordPlatform, func() (Adapter, error) {
		token := strings.TrimSpace(os.Getenv("CHATCLI_DISCORD_BOT_TOKEN"))
		if token == "" {
			return nil, nil
		}
		return NewDiscordAdapter(token, zap.NewNop()), nil
	})
}
