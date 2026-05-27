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
	"net/http"
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

// whatsAppWebhook mirrors the Cloud API webhook payload (text messages only).
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
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// parseWhatsAppInbound extracts text messages from a Cloud API webhook body.
// Non-text messages and status callbacks are ignored.
func parseWhatsAppInbound(body []byte) []InboundMessage {
	var w whatsAppWebhook
	if err := json.Unmarshal(body, &w); err != nil {
		return nil
	}
	var out []InboundMessage
	for _, e := range w.Entry {
		for _, c := range e.Changes {
			for _, m := range c.Value.Messages {
				if m.Type != "text" || strings.TrimSpace(m.Text.Body) == "" || m.From == "" {
					continue
				}
				out = append(out, InboundMessage{
					Platform: whatsappPlatform,
					ChatID:   m.From,
					UserID:   m.From,
					Text:     m.Text.Body,
				})
			}
		}
	}
	return out
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
