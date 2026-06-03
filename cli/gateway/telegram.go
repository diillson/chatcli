/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const telegramPlatform = "telegram"

// TelegramAdapter integrates with Telegram via the plain HTTP Bot API
// (getUpdates long-polling + sendMessage). No third-party SDK.
type TelegramAdapter struct {
	token   string
	baseURL string // overridable for tests; defaults to https://api.telegram.org
	http    *http.Client
	allowed map[string]bool // allowlisted user ids; empty = open (with warning)
	logger  *zap.Logger
	offset  int64
}

// NewTelegramAdapter builds an adapter from explicit config.
func NewTelegramAdapter(token string, allowedUserIDs []string, logger *zap.Logger) *TelegramAdapter {
	allowed := make(map[string]bool, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = true
		}
	}
	return &TelegramAdapter{
		token:   token,
		baseURL: "https://api.telegram.org",
		http:    &http.Client{Timeout: 70 * time.Second}, // > long-poll timeout
		allowed: allowed,
		logger:  logger,
	}
}

// Name implements Adapter.
func (t *TelegramAdapter) Name() string { return telegramPlatform }

// SetLogger implements LoggerAware: the daemon injects its real logger and we
// route the HTTP client through it so every Bot API call is traced.
func (t *TelegramAdapter) SetLogger(l *zap.Logger) {
	t.logger = l
	t.http = newLoggingClient(t.http, l, telegramPlatform)
}

// Start long-polls getUpdates until ctx is canceled, pushing messages to
// inbound. Transient errors are logged and retried with a short backoff.
func (t *TelegramAdapter) Start(ctx context.Context, inbound chan<- InboundMessage) error {
	if len(t.allowed) == 0 {
		t.logger.Warn("gateway/telegram: no CHATCLI_TELEGRAM_ALLOWED_USERS set — accepting messages from ANY user")
	}
	t.logger.Info("gateway/telegram: started")

	for {
		if ctx.Err() != nil {
			return nil
		}
		msgs, maxID, err := t.poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			t.logger.Warn("gateway/telegram: poll error", zap.Error(err))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		if maxID >= t.offset {
			t.offset = maxID + 1
		}
		for _, m := range msgs {
			if !t.permitted(m.UserID) {
				t.logger.Warn("gateway/telegram: dropped message from non-allowlisted user",
					zap.String("user", m.UserID))
				continue
			}
			select {
			case <-ctx.Done():
				return nil
			case inbound <- m:
			}
		}
	}
}

func (t *TelegramAdapter) permitted(userID string) bool {
	if len(t.allowed) == 0 {
		return true
	}
	return t.allowed[userID]
}

// poll performs one getUpdates long-poll and returns parsed messages plus the
// highest update id seen.
func (t *TelegramAdapter) poll(ctx context.Context) ([]InboundMessage, int64, error) {
	q := url.Values{}
	q.Set("timeout", "30")
	q.Set("offset", strconv.FormatInt(t.offset, 10))
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?%s", t.baseURL, t.token, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("telegram getUpdates status %d", resp.StatusCode)
	}
	msgs, maxID, err := parseTelegramUpdates(body)
	if err != nil {
		return nil, 0, err
	}
	return t.hydrateAudio(ctx, msgs), maxID, nil
}

// hydrateAudio downloads the bytes for any voice/audio attachment and drops
// attachments (or whole messages) that can't be fetched, so a failed download
// degrades gracefully instead of dispatching an empty audio ref.
func (t *TelegramAdapter) hydrateAudio(ctx context.Context, msgs []InboundMessage) []InboundMessage {
	out := msgs[:0]
	for _, m := range msgs {
		if m.Audio != nil && len(m.Audio.Data) == 0 {
			data, mime, err := t.downloadFile(ctx, m.Audio.ref)
			if err != nil {
				t.logger.Warn("gateway/telegram: voice download failed",
					zap.String("user", m.UserID), zap.Error(err))
				m.Audio = nil
			} else {
				m.Audio.Data = data
				if m.Audio.MimeType == "" {
					m.Audio.MimeType = mime
				}
			}
		}
		if strings.TrimSpace(m.Text) == "" && m.Audio == nil {
			continue // download failed and there was no text — nothing to do
		}
		out = append(out, m)
	}
	return out
}

// downloadFile resolves a Telegram file_id to its bytes: getFile returns the
// storage path, then the file is fetched from the bot file endpoint.
func (t *TelegramAdapter) downloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", t.baseURL, t.token, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("telegram getFile status %d", resp.StatusCode)
	}
	var gf struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &gf); err != nil {
		return nil, "", err
	}
	if !gf.OK || gf.Result.FilePath == "" {
		return nil, "", fmt.Errorf("telegram getFile: no file_path")
	}
	fileURL := fmt.Sprintf("%s/file/bot%s/%s", t.baseURL, t.token, gf.Result.FilePath)
	return fetchAudioBytes(ctx, t.http, fileURL, "", maxAudioBytes())
}

// Send delivers a reply via sendMessage.
func (t *TelegramAdapter) Send(ctx context.Context, msg OutboundMessage) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"chat_id": msg.ChatID,
		"text":    msg.Text,
	})
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram sendMessage status %d", resp.StatusCode)
	}
	return nil
}

// SendTyping shows the native "typing…" indicator in the chat. It expires after
// ~5s, so the Runner refreshes it while the agent works. Implements TypingAware.
func (t *TelegramAdapter) SendTyping(ctx context.Context, chatID string) error {
	payload, _ := json.Marshal(map[string]interface{}{"chat_id": chatID, "action": "typing"})
	endpoint := fmt.Sprintf("%s/bot%s/sendChatAction", t.baseURL, t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram sendChatAction status %d", resp.StatusCode)
	}
	return nil
}

// --- pure parsing (unit-testable without network) ---

type tgUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

// tgFile is the shared shape of Telegram's voice/audio objects.
type tgFile struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

type tgMessage struct {
	From    *tgUser `json:"from"`
	Chat    *tgChat `json:"chat"`
	Text    string  `json:"text"`
	Caption string  `json:"caption"` // text attached to a voice/audio note
	Voice   *tgFile `json:"voice"`   // voice note (Opus/OGG)
	Audio   *tgFile `json:"audio"`   // music / audio file
}

// audioFile returns the voice note or audio file on the message, if any.
// Voice notes take precedence — they are the common "send a voice message" case.
func (m *tgMessage) audioFile() *tgFile {
	if m.Voice != nil && m.Voice.FileID != "" {
		return m.Voice
	}
	if m.Audio != nil && m.Audio.FileID != "" {
		return m.Audio
	}
	return nil
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgGetUpdatesResp struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

// parseTelegramUpdates converts a getUpdates body into normalized messages
// and the highest update id seen. Non-text and chatless updates are skipped.
func parseTelegramUpdates(body []byte) ([]InboundMessage, int64, error) {
	var r tgGetUpdatesResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, 0, err
	}
	if !r.OK {
		return nil, 0, fmt.Errorf("telegram response not ok")
	}
	msgs := make([]InboundMessage, 0, len(r.Result))
	var maxID int64
	for _, u := range r.Result {
		if u.UpdateID > maxID {
			maxID = u.UpdateID
		}
		if u.Message == nil || u.Message.Chat == nil {
			continue
		}
		af := u.Message.audioFile()
		text := u.Message.Text
		if af != nil && strings.TrimSpace(text) == "" {
			text = u.Message.Caption // a caption on a voice/audio note
		}
		// Skip only when there is nothing usable — neither text nor audio.
		if strings.TrimSpace(text) == "" && af == nil {
			continue
		}
		userID, userName := "", ""
		if u.Message.From != nil {
			userID = strconv.FormatInt(u.Message.From.ID, 10)
			userName = u.Message.From.Username
			if userName == "" {
				userName = u.Message.From.FirstName
			}
		}
		var audio *InboundAudio
		if af != nil {
			audio = &InboundAudio{ref: af.FileID, MimeType: af.MimeType, FileName: af.FileName}
		}
		msgs = append(msgs, InboundMessage{
			Platform: telegramPlatform,
			ChatID:   strconv.FormatInt(u.Message.Chat.ID, 10),
			UserID:   userID,
			UserName: userName,
			Text:     text,
			Audio:    audio,
		})
	}
	return msgs, maxID, nil
}

// init registers the Telegram builder. It returns (nil, nil) when no token is
// configured so the runner simply skips it.
func init() {
	RegisterBuilder(telegramPlatform, func() (Adapter, error) {
		token := strings.TrimSpace(os.Getenv("CHATCLI_TELEGRAM_BOT_TOKEN"))
		if token == "" {
			return nil, nil
		}
		var allowed []string
		if raw := os.Getenv("CHATCLI_TELEGRAM_ALLOWED_USERS"); raw != "" {
			allowed = strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
		}
		return NewTelegramAdapter(token, allowed, zap.NewNop()), nil
	})
}
