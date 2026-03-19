package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// NotificationMessage is the structured message sent through channels.
type NotificationMessage struct {
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	Severity  string            `json:"severity"`
	IssueName string            `json:"issueName"`
	Namespace string            `json:"namespace"`
	Resource  string            `json:"resource"`
	State     string            `json:"state"`
	Timestamp time.Time         `json:"timestamp"`
	Fields    map[string]string `json:"fields,omitempty"`
	Color     string            `json:"color"`
	URL       string            `json:"url,omitempty"`
}

// ChannelSender defines the interface for notification delivery.
type ChannelSender interface {
	Send(ctx context.Context, msg *NotificationMessage) error
	ValidateConfig() error
	Type() string
}

// NewSender creates a ChannelSender for the given type and config.
func NewSender(channelType string, config map[string]string) (ChannelSender, error) {
	switch channelType {
	case "slack":
		return &SlackSender{config: config}, nil
	case "pagerduty":
		return &PagerDutySender{config: config}, nil
	case "opsgenie":
		return &OpsGenieSender{config: config}, nil
	case "email":
		return &EmailSender{config: config}, nil
	case "webhook":
		return &WebhookSender{config: config}, nil
	case "teams":
		return &TeamsSender{config: config}, nil
	default:
		return nil, fmt.Errorf("unsupported channel type: %s", channelType)
	}
}

// SeverityColor returns a hex color for the severity level.
func SeverityColor(severity string) string {
	switch severity {
	case "critical":
		return "#FF0000"
	case "high":
		return "#FF8C00"
	case "medium":
		return "#FFD700"
	case "low":
		return "#00CC00"
	default:
		return "#808080"
	}
}

// SeverityEmoji returns an emoji for the severity level.
func SeverityEmoji(severity string) string {
	switch severity {
	case "critical":
		return "🔴"
	case "high":
		return "🟠"
	case "medium":
		return "🟡"
	case "low":
		return "🟢"
	default:
		return "⚪"
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// --- Slack ---

// SlackSender sends notifications via Slack Incoming Webhooks.
type SlackSender struct {
	config map[string]string
}

func (s *SlackSender) Type() string { return "slack" }

func (s *SlackSender) ValidateConfig() error {
	if s.config["webhook_url"] == "" {
		return fmt.Errorf("slack: webhook_url is required")
	}
	return nil
}

func (s *SlackSender) Send(ctx context.Context, msg *NotificationMessage) error {
	if err := s.ValidateConfig(); err != nil {
		return err
	}

	color := msg.Color
	if color == "" {
		color = SeverityColor(msg.Severity)
	}

	fields := []map[string]interface{}{
		{"type": "mrkdwn", "text": fmt.Sprintf("*Severity:* %s %s", SeverityEmoji(msg.Severity), msg.Severity)},
		{"type": "mrkdwn", "text": fmt.Sprintf("*Resource:* %s", msg.Resource)},
		{"type": "mrkdwn", "text": fmt.Sprintf("*Namespace:* %s", msg.Namespace)},
		{"type": "mrkdwn", "text": fmt.Sprintf("*State:* %s", msg.State)},
	}

	for k, v := range msg.Fields {
		fields = append(fields, map[string]interface{}{"type": "mrkdwn", "text": fmt.Sprintf("*%s:* %s", k, v)})
	}

	blocks := []map[string]interface{}{
		{
			"type": "header",
			"text": map[string]string{"type": "plain_text", "text": msg.Title},
		},
		{
			"type":   "section",
			"text":   map[string]string{"type": "mrkdwn", "text": msg.Body},
			"fields": fields,
		},
		{
			"type": "context",
			"elements": []map[string]string{
				{"type": "mrkdwn", "text": fmt.Sprintf("Issue: *%s* | %s", msg.IssueName, msg.Timestamp.Format(time.RFC3339))},
			},
		},
	}

	payload := map[string]interface{}{
		"blocks": blocks,
		"attachments": []map[string]interface{}{
			{"color": color, "blocks": []map[string]interface{}{}},
		},
	}

	if ch := s.config["channel"]; ch != "" {
		payload["channel"] = ch
	}
	if u := s.config["username"]; u != "" {
		payload["username"] = u
	}

	return s.post(ctx, s.config["webhook_url"], payload)
}

func (s *SlackSender) post(ctx context.Context, url string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// --- PagerDuty ---

// PagerDutySender sends notifications via PagerDuty Events API v2.
type PagerDutySender struct {
	config map[string]string
}

func (p *PagerDutySender) Type() string { return "pagerduty" }

func (p *PagerDutySender) ValidateConfig() error {
	if p.config["routing_key"] == "" {
		return fmt.Errorf("pagerduty: routing_key is required")
	}
	return nil
}

func (p *PagerDutySender) Send(ctx context.Context, msg *NotificationMessage) error {
	if err := p.ValidateConfig(); err != nil {
		return err
	}

	pdSeverity := "warning"
	switch msg.Severity {
	case "critical":
		pdSeverity = "critical"
	case "high":
		pdSeverity = "error"
	case "medium":
		pdSeverity = "warning"
	case "low":
		pdSeverity = "info"
	}

	eventAction := "trigger"
	if msg.State == "Resolved" {
		eventAction = "resolve"
	}

	dedupKey := fmt.Sprintf("chatcli-%s-%s", msg.Namespace, msg.IssueName)

	customDetails := map[string]string{
		"resource":  msg.Resource,
		"namespace": msg.Namespace,
		"state":     msg.State,
		"severity":  msg.Severity,
	}
	for k, v := range msg.Fields {
		customDetails[k] = v
	}

	payload := map[string]interface{}{
		"routing_key":  p.config["routing_key"],
		"event_action": eventAction,
		"dedup_key":    dedupKey,
		"payload": map[string]interface{}{
			"summary":        msg.Title,
			"source":         fmt.Sprintf("chatcli/%s/%s", msg.Namespace, msg.Resource),
			"severity":       pdSeverity,
			"timestamp":      msg.Timestamp.Format(time.RFC3339),
			"component":      msg.Resource,
			"group":          msg.Namespace,
			"class":          msg.Severity,
			"custom_details": customDetails,
		},
	}

	if msg.URL != "" {
		payload["links"] = []map[string]string{
			{"href": msg.URL, "text": "View Incident"},
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pagerduty: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://events.pagerduty.com/v2/enqueue", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("pagerduty: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pagerduty: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pagerduty: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// --- OpsGenie ---

// OpsGenieSender sends notifications via OpsGenie Alert API.
type OpsGenieSender struct {
	config map[string]string
}

func (o *OpsGenieSender) Type() string { return "opsgenie" }

func (o *OpsGenieSender) ValidateConfig() error {
	if o.config["api_key"] == "" {
		return fmt.Errorf("opsgenie: api_key is required")
	}
	return nil
}

func (o *OpsGenieSender) Send(ctx context.Context, msg *NotificationMessage) error {
	if err := o.ValidateConfig(); err != nil {
		return err
	}

	priority := "P3"
	switch msg.Severity {
	case "critical":
		priority = "P1"
	case "high":
		priority = "P2"
	case "medium":
		priority = "P3"
	case "low":
		priority = "P4"
	}

	alias := fmt.Sprintf("chatcli-%s-%s", msg.Namespace, msg.IssueName)

	// Close alert on resolution
	if msg.State == "Resolved" {
		return o.closeAlert(ctx, alias)
	}

	payload := map[string]interface{}{
		"message":     msg.Title,
		"alias":       alias,
		"description": msg.Body,
		"priority":    priority,
		"source":      "ChatCLI AIOps",
		"entity":      msg.Resource,
		"details": map[string]string{
			"resource":  msg.Resource,
			"namespace": msg.Namespace,
			"severity":  msg.Severity,
			"state":     msg.State,
			"issue":     msg.IssueName,
		},
	}

	if tags := o.config["tags"]; tags != "" {
		payload["tags"] = strings.Split(tags, ",")
	}

	if responders := o.config["responders"]; responders != "" {
		var resp []map[string]string
		if err := json.Unmarshal([]byte(responders), &resp); err == nil {
			payload["responders"] = resp
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("opsgenie: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.opsgenie.com/v2/alerts", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("opsgenie: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "GenieKey "+o.config["api_key"])

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("opsgenie: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("opsgenie: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (o *OpsGenieSender) closeAlert(ctx context.Context, alias string) error {
	url := fmt.Sprintf("https://api.opsgenie.com/v2/alerts/%s/close?identifierType=alias", alias)
	payload := map[string]string{"source": "ChatCLI AIOps", "note": "Issue resolved automatically"}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("opsgenie: close request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "GenieKey "+o.config["api_key"])

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("opsgenie: close send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("opsgenie: close HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// --- Email ---

// EmailSender sends notifications via SMTP.
type EmailSender struct {
	config map[string]string
}

func (e *EmailSender) Type() string { return "email" }

func (e *EmailSender) ValidateConfig() error {
	for _, key := range []string{"smtp_host", "smtp_port", "from", "to"} {
		if e.config[key] == "" {
			return fmt.Errorf("email: %s is required", key)
		}
	}
	return nil
}

func (e *EmailSender) Send(ctx context.Context, msg *NotificationMessage) error {
	if err := e.ValidateConfig(); err != nil {
		return err
	}

	color := msg.Color
	if color == "" {
		color = SeverityColor(msg.Severity)
	}

	var fieldsHTML strings.Builder
	fieldsHTML.WriteString(fmt.Sprintf(`<tr><td style="padding:4px 8px;font-weight:bold">Severity</td><td style="padding:4px 8px">%s</td></tr>`, msg.Severity))
	fieldsHTML.WriteString(fmt.Sprintf(`<tr><td style="padding:4px 8px;font-weight:bold">Resource</td><td style="padding:4px 8px">%s</td></tr>`, msg.Resource))
	fieldsHTML.WriteString(fmt.Sprintf(`<tr><td style="padding:4px 8px;font-weight:bold">Namespace</td><td style="padding:4px 8px">%s</td></tr>`, msg.Namespace))
	fieldsHTML.WriteString(fmt.Sprintf(`<tr><td style="padding:4px 8px;font-weight:bold">State</td><td style="padding:4px 8px">%s</td></tr>`, msg.State))
	fieldsHTML.WriteString(fmt.Sprintf(`<tr><td style="padding:4px 8px;font-weight:bold">Issue</td><td style="padding:4px 8px">%s</td></tr>`, msg.IssueName))
	for k, v := range msg.Fields {
		fieldsHTML.WriteString(fmt.Sprintf(`<tr><td style="padding:4px 8px;font-weight:bold">%s</td><td style="padding:4px 8px">%s</td></tr>`, k, v))
	}

	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;margin:0;padding:0">
<div style="background-color:%s;color:white;padding:16px;font-size:18px;font-weight:bold">
%s %s
</div>
<div style="padding:16px">
<p>%s</p>
<table style="border-collapse:collapse;width:100%%">
%s
</table>
<p style="color:#666;font-size:12px;margin-top:16px">Generated by ChatCLI AIOps Platform at %s</p>
</div>
</body>
</html>`, color, SeverityEmoji(msg.Severity), msg.Title, msg.Body, fieldsHTML.String(), msg.Timestamp.Format(time.RFC3339))

	recipients := strings.Split(e.config["to"], ",")
	for i := range recipients {
		recipients[i] = strings.TrimSpace(recipients[i])
	}

	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: [%s] %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=\"UTF-8\"\r\n\r\n",
		e.config["from"],
		strings.Join(recipients, ", "),
		strings.ToUpper(msg.Severity),
		msg.Title,
	)

	addr := fmt.Sprintf("%s:%s", e.config["smtp_host"], e.config["smtp_port"])
	var auth smtp.Auth
	if e.config["smtp_user"] != "" {
		auth = smtp.PlainAuth("", e.config["smtp_user"], e.config["smtp_password"], e.config["smtp_host"])
	}

	tlsConfig := &tls.Config{
		ServerName: e.config["smtp_host"],
	}
	if e.config["tls_skip_verify"] == "true" {
		tlsConfig.InsecureSkipVerify = true
	}

	conn, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("email: dial: %w", err)
	}
	defer conn.Close()

	if ok, _ := conn.Extension("STARTTLS"); ok {
		if err := conn.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("email: starttls: %w", err)
		}
	}

	if auth != nil {
		if err := conn.Auth(auth); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}

	if err := conn.Mail(e.config["from"]); err != nil {
		return fmt.Errorf("email: mail from: %w", err)
	}
	for _, rcpt := range recipients {
		if err := conn.Rcpt(rcpt); err != nil {
			return fmt.Errorf("email: rcpt %s: %w", rcpt, err)
		}
	}

	w, err := conn.Data()
	if err != nil {
		return fmt.Errorf("email: data: %w", err)
	}
	if _, err := w.Write([]byte(headers + htmlBody)); err != nil {
		return fmt.Errorf("email: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: close: %w", err)
	}

	return conn.Quit()
}

// --- Webhook ---

// WebhookSender sends notifications to a generic HTTP webhook.
type WebhookSender struct {
	config map[string]string
}

func (w *WebhookSender) Type() string { return "webhook" }

func (w *WebhookSender) ValidateConfig() error {
	if w.config["url"] == "" {
		return fmt.Errorf("webhook: url is required")
	}
	return nil
}

func (w *WebhookSender) Send(ctx context.Context, msg *NotificationMessage) error {
	if err := w.ValidateConfig(); err != nil {
		return err
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("webhook: marshal: %w", err)
	}

	method := w.config["method"]
	if method == "" {
		method = http.MethodPost
	}

	req, err := http.NewRequestWithContext(ctx, method, w.config["url"], bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ChatCLI-AIOps/1.0")

	// Custom headers
	if headersJSON := w.config["headers"]; headersJSON != "" {
		var headers map[string]string
		if err := json.Unmarshal([]byte(headersJSON), &headers); err == nil {
			for k, v := range headers {
				req.Header.Set(k, v)
			}
		}
	}

	// HMAC signature
	if secret := w.config["secret"]; secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Signature-256", "sha256="+sig)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// --- Microsoft Teams ---

// TeamsSender sends notifications via Microsoft Teams Incoming Webhooks (Adaptive Cards).
type TeamsSender struct {
	config map[string]string
}

func (t *TeamsSender) Type() string { return "teams" }

func (t *TeamsSender) ValidateConfig() error {
	if t.config["webhook_url"] == "" {
		return fmt.Errorf("teams: webhook_url is required")
	}
	return nil
}

func (t *TeamsSender) Send(ctx context.Context, msg *NotificationMessage) error {
	if err := t.ValidateConfig(); err != nil {
		return err
	}

	color := msg.Color
	if color == "" {
		color = SeverityColor(msg.Severity)
	}
	// Teams uses color without #
	teamsColor := strings.TrimPrefix(color, "#")

	facts := []map[string]string{
		{"title": "Severity", "value": fmt.Sprintf("%s %s", SeverityEmoji(msg.Severity), msg.Severity)},
		{"title": "Resource", "value": msg.Resource},
		{"title": "Namespace", "value": msg.Namespace},
		{"title": "State", "value": msg.State},
		{"title": "Issue", "value": msg.IssueName},
	}
	for k, v := range msg.Fields {
		facts = append(facts, map[string]string{"title": k, "value": v})
	}

	card := map[string]interface{}{
		"type":       "message",
		"summary":    msg.Title,
		"themeColor": teamsColor,
		"attachments": []map[string]interface{}{
			{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": map[string]interface{}{
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"type":    "AdaptiveCard",
					"version": "1.4",
					"body": []map[string]interface{}{
						{
							"type":   "TextBlock",
							"text":   msg.Title,
							"size":   "Large",
							"weight": "Bolder",
							"color":  "Attention",
						},
						{
							"type": "TextBlock",
							"text": msg.Body,
							"wrap": true,
						},
						{
							"type":  "FactSet",
							"facts": facts,
						},
						{
							"type":      "TextBlock",
							"text":      fmt.Sprintf("Generated at %s", msg.Timestamp.Format(time.RFC3339)),
							"size":      "Small",
							"isSubtle":  true,
							"separator": true,
						},
					},
				},
			},
		},
	}

	body, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("teams: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.config["webhook_url"], bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("teams: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("teams: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("teams: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
