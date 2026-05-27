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

// Start long-polls getUpdates until ctx is cancelled, pushing messages to
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
	return parseTelegramUpdates(body)
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

// --- pure parsing (unit-testable without network) ---

type tgUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgMessage struct {
	From *tgUser `json:"from"`
	Chat *tgChat `json:"chat"`
	Text string  `json:"text"`
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
	var msgs []InboundMessage
	var maxID int64
	for _, u := range r.Result {
		if u.UpdateID > maxID {
			maxID = u.UpdateID
		}
		if u.Message == nil || u.Message.Chat == nil || strings.TrimSpace(u.Message.Text) == "" {
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
		msgs = append(msgs, InboundMessage{
			Platform: telegramPlatform,
			ChatID:   strconv.FormatInt(u.Message.Chat.ID, 10),
			UserID:   userID,
			UserName: userName,
			Text:     u.Message.Text,
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
