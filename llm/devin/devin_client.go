/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * DevinClient — the DEVIN provider adapter.
 *
 * Devin is not a chat-completions LLM: it is an autonomous engineer driven by
 * asynchronous sessions. The adapter maps ChatCLI's turn model onto that
 * lifecycle so `/provider DEVIN` feels like any other provider:
 *
 *   - first user turn            → POST sessions (create, with the prompt)
 *   - follow-up turns            → POST sessions/{id}/message(s)
 *   - the "assistant reply"      → poll GET session until a turn boundary
 *     (blocked = waiting for user, finished, error/expired), then collect the
 *     Devin-authored messages that arrived after the user's last message.
 *
 * A cleared conversation (no assistant turns in history) starts a fresh
 * session; an existing conversation reuses the tracked session. When ChatCLI
 * restarts mid-conversation the session id is gone, so a new session is
 * created seeded with a compact transcript of the recent history.
 *
 * Model ids select the v3 devin_mode (devin, devin-fast, devin-lite,
 * devin-ultra, devin-fusion); the v1 generation has no mode parameter and
 * ignores it. Turn pacing is tunable via DEVIN_POLL_INTERVAL and
 * DEVIN_TURN_TIMEOUT.
 */
package devin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// transcriptPreambleBudget caps the transcript seeded into a recovery
// session (chars), keeping the create payload bounded.
const transcriptPreambleBudget = 8000

// DevinClient implements client.LLMClient over Devin sessions.
type DevinClient struct {
	api    API
	model  string
	logger *zap.Logger

	mu        sync.Mutex
	sessionID string
}

// NewDevinClient builds the provider adapter on top of an API client.
func NewDevinClient(api API, model string, logger *zap.Logger) *DevinClient {
	if model == "" {
		model = config.DefaultDevinModel
	}
	return &DevinClient{api: api, model: model, logger: logger}
}

// GetModelName returns the catalog display name of the selected Devin mode.
func (c *DevinClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderDevin, c.model)
}

// SessionID returns the Devin session currently backing the conversation
// (empty before the first turn).
func (c *DevinClient) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// devinMode maps the catalog model id onto the v3 devin_mode value. The
// default "devin" id maps to the API default (empty).
func devinMode(model string) string {
	mode := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "devin")
	mode = strings.TrimPrefix(mode, "-")
	switch mode {
	case "fast", "lite", "ultra", "fusion", "normal":
		return mode
	}
	return ""
}

// SendPrompt runs one conversational turn against the Devin session backing
// this conversation. maxTokens does not apply to Devin and is ignored.
func (c *DevinClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, _ int) (string, error) {
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()

	hasAssistantTurn := false
	for _, msg := range history {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			hasAssistantTurn = true
			break
		}
	}

	var (
		session    *Session
		announced  bool
		createdURL string
		err        error
	)
	switch {
	case !hasAssistantTurn || sessionID == "":
		// Fresh conversation — or a resumed one whose session id was lost
		// (restart/provider switch): seed the new session with a compact
		// transcript so Devin regains context.
		created := prompt
		if hasAssistantTurn {
			created = buildTranscriptPreamble(history) + prompt
		}
		session, err = c.createSession(ctx, created)
		if err != nil {
			return "", err
		}
		announced = true
		createdURL = session.URL
		c.mu.Lock()
		c.sessionID = session.ID
		c.mu.Unlock()
		sessionID = session.ID
	default:
		if err = c.api.SendMessage(ctx, sessionID, prompt, nil); err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.devin.send_failed", sessionID), err)
		}
	}

	reply, session, err := c.waitForTurn(ctx, sessionID)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	if announced {
		// The create response carries the web URL even when later session
		// snapshots omit it; prefer whichever is available.
		b.WriteString(i18n.T("llm.devin.session_started", session.ID, firstNonEmpty(session.URL, createdURL, session.ID)))
		b.WriteString("\n\n")
	}
	if reply == "" {
		reply = i18n.T("llm.devin.no_reply", string(session.State))
	}
	b.WriteString(reply)
	appendSessionArtifacts(&b, session)
	return b.String(), nil
}

// createSession starts the backing session with ChatCLI provenance tags.
func (c *DevinClient) createSession(ctx context.Context, prompt string) (*Session, error) {
	mode := devinMode(c.model)
	if mode != "" && c.api.Version() == "v1" {
		c.logger.Debug(i18n.T("llm.devin.mode_ignored_v1"), zap.String("mode", mode))
	}
	session, err := c.api.CreateSession(ctx, CreateSessionRequest{
		Prompt: prompt,
		Tags:   []string{"chatcli"},
		Mode:   mode,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.devin.create_failed"), err)
	}
	c.logger.Info(i18n.T("llm.devin.session_created_log"),
		zap.String("session_id", session.ID),
		zap.String("api_version", c.api.Version()))
	return session, nil
}

// waitForTurn polls the session until Devin reaches a turn boundary, using
// the operator-tunable pacing.
func (c *DevinClient) waitForTurn(ctx context.Context, sessionID string) (string, *Session, error) {
	interval := envDuration(config.DevinPollIntervalEnv, config.DefaultDevinPollInterval)
	timeout := envDuration(config.DevinTurnTimeoutEnv, config.DefaultDevinTurnTimeout)
	return WaitTurn(ctx, c.api, sessionID, interval, timeout, c.logger)
}

// WaitTurn polls the session until Devin reaches a turn boundary (blocked,
// finished, error/expired), then returns the Devin-authored messages that
// arrived after the user's last message plus the final session snapshot.
// Shared by the DEVIN provider, the @devin builtin and the scheduler action.
func WaitTurn(ctx context.Context, api API, sessionID string, interval, timeout time.Duration, logger *zap.Logger) (string, *Session, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = config.DefaultDevinPollInterval
	}
	if timeout <= 0 {
		timeout = config.DefaultDevinTurnTimeout
	}
	deadline := time.Now().Add(timeout)

	var last *Session
	for {
		session, err := api.GetSession(ctx, sessionID)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", i18n.T("llm.devin.poll_failed", sessionID), err)
		}
		last = session

		if session.State == StateError || session.State == StateExpired {
			detail := firstNonEmpty(session.StatusDetail, session.RawStatus)
			return "", nil, errors.New(i18n.T("llm.devin.session_error", sessionID, detail))
		}
		if session.State.TurnBoundary() {
			break
		}
		// A suspended VM with fresh Devin output means Devin spoke and went
		// to sleep waiting for us — that is a turn boundary too.
		if session.State == StateSuspended {
			if reply := CollectReply(ctx, api, session, logger); reply != "" {
				return reply, session, nil
			}
		}

		if time.Now().After(deadline) {
			return "", nil, errors.New(i18n.T("llm.devin.turn_timeout", sessionID, timeout.String(), sessionURLOrID(session)))
		}
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(interval):
		}
	}
	return CollectReply(ctx, api, last, logger), last, nil
}

// CollectReply extracts the Devin messages that follow the user's last
// message. v1 embeds messages in the session detail; v3 needs the messages
// endpoint.
func CollectReply(ctx context.Context, api API, session *Session, logger *zap.Logger) string {
	if session == nil {
		return ""
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	messages := session.Messages
	if len(messages) == 0 {
		fetched, err := api.ListMessages(ctx, session.ID)
		if err != nil {
			logger.Warn(i18n.T("llm.devin.messages_failed_log"), zap.String("session_id", session.ID), zap.Error(err))
			return ""
		}
		messages = fetched
	}
	lastUser := -1
	for i, m := range messages {
		if m.FromUser {
			lastUser = i
		}
	}
	var parts []string
	for _, m := range messages[lastUser+1:] {
		if !m.FromUser && strings.TrimSpace(m.Text) != "" {
			parts = append(parts, strings.TrimSpace(m.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

// appendSessionArtifacts appends PR links and structured output to the reply
// when the session produced them.
func appendSessionArtifacts(b *strings.Builder, session *Session) {
	if session == nil {
		return
	}
	if len(session.PullRequests) > 0 {
		b.WriteString("\n\n")
		b.WriteString(i18n.T("llm.devin.prs_header"))
		for _, pr := range session.PullRequests {
			b.WriteString("\n- ")
			b.WriteString(pr.URL)
			if pr.State != "" {
				b.WriteString(" (")
				b.WriteString(pr.State)
				b.WriteString(")")
			}
		}
	}
	if session.State == StateFinished && len(session.StructuredOutput) > 0 && string(session.StructuredOutput) != "null" {
		pretty := session.StructuredOutput
		var buf bytes.Buffer
		if err := json.Indent(&buf, session.StructuredOutput, "", "  "); err == nil {
			pretty = json.RawMessage(buf.Bytes())
		}
		b.WriteString("\n\n")
		b.WriteString(i18n.T("llm.devin.structured_header"))
		b.WriteString("\n```json\n")
		b.Write(pretty)
		b.WriteString("\n```")
	}
}

// buildTranscriptPreamble renders recent history as a bounded transcript so
// a recovery session regains conversational context.
func buildTranscriptPreamble(history []models.Message) string {
	var lines []string
	total := 0
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		line := role + ": " + strings.TrimSpace(msg.Content)
		if total+len(line) > transcriptPreambleBudget {
			break
		}
		total += len(line)
		lines = append([]string{line}, lines...)
	}
	if len(lines) == 0 {
		return ""
	}
	return i18n.T("llm.devin.transcript_preamble") + "\n\n" + strings.Join(lines, "\n") + "\n\n---\n\n"
}

// envDuration parses a duration env var with a default.
func envDuration(key string, def time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// sessionURLOrID prefers the web URL for user-facing references.
func sessionURLOrID(session *Session) string {
	if session == nil {
		return ""
	}
	return firstNonEmpty(session.URL, session.ID)
}
