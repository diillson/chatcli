/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package devin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// v1Client implements API against the individual/Teams generation
// (api.devin.ai/v1/*), authenticated with apk_user_/apk_ (or cog_) keys.
type v1Client struct {
	core *apiCore
}

// Version returns "v1".
func (*v1Client) Version() string { return "v1" }

// v1Time parses the RFC3339 timestamps v1 uses. Zero time on failure — the
// timestamps are informational, never load-bearing.
func v1Time(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// v1MapState normalizes the v1 status_enum vocabulary.
func v1MapState(statusEnum string) SessionState {
	switch strings.ToLower(strings.TrimSpace(statusEnum)) {
	case "working", "resumed", "resume_requested", "resume_requested_frontend":
		return StateWorking
	case "blocked":
		return StateBlocked
	case "finished":
		return StateFinished
	case "expired":
		return StateExpired
	case "suspend_requested", "suspend_requested_frontend":
		return StateSuspended
	case "":
		return StateUnknown
	default:
		return StateUnknown
	}
}

// v1SessionDetail is the wire shape of GET /v1/sessions/{id} (and each item
// of GET /v1/sessions).
type v1SessionDetail struct {
	SessionID  string   `json:"session_id"`
	Status     string   `json:"status"`
	StatusEnum string   `json:"status_enum"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
	Title      *string  `json:"title"`
	PlaybookID *string  `json:"playbook_id"`
	SnapshotID *string  `json:"snapshot_id"`
	Tags       []string `json:"tags"`
	Messages   []struct {
		Type      string `json:"type"`
		EventID   string `json:"event_id"`
		Message   string `json:"message"`
		Timestamp string `json:"timestamp"`
	} `json:"messages"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// toSession converts the wire detail into the normalized Session.
func (d v1SessionDetail) toSession() *Session {
	s := &Session{
		ID:               d.SessionID,
		State:            v1MapState(d.StatusEnum),
		RawStatus:        firstNonEmpty(d.StatusEnum, d.Status),
		Tags:             d.Tags,
		CreatedAt:        v1Time(d.CreatedAt),
		UpdatedAt:        v1Time(d.UpdatedAt),
		StructuredOutput: d.StructuredOutput,
	}
	if d.Title != nil {
		s.Title = *d.Title
	}
	if d.PlaybookID != nil {
		s.PlaybookID = *d.PlaybookID
	}
	if d.SnapshotID != nil {
		s.SnapshotID = *d.SnapshotID
	}
	if d.PullRequest != nil && d.PullRequest.URL != "" {
		s.PullRequests = []PullRequest{{URL: d.PullRequest.URL}}
	}
	for _, m := range d.Messages {
		s.Messages = append(s.Messages, Message{
			EventID:   m.EventID,
			FromUser:  strings.Contains(strings.ToLower(m.Type), "user"),
			Text:      m.Message,
			CreatedAt: v1Time(m.Timestamp),
		})
	}
	return s
}

// CreateSession starts a new Devin session. v1 has no attachment_urls field,
// so uploaded attachments are referenced via the documented ATTACHMENT:"url"
// prompt lines instead.
func (c *v1Client) CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error) {
	prompt := req.Prompt
	for _, u := range req.AttachmentURLs {
		prompt += "\nATTACHMENT:\"" + u + "\""
	}
	payload := map[string]any{"prompt": prompt}
	if req.Title != "" {
		payload["title"] = req.Title
	}
	if len(req.Tags) > 0 {
		payload["tags"] = req.Tags
	}
	if req.PlaybookID != "" {
		payload["playbook_id"] = req.PlaybookID
	}
	if req.SnapshotID != "" {
		payload["snapshot_id"] = req.SnapshotID
	}
	if req.MaxACULimit > 0 {
		payload["max_acu_limit"] = req.MaxACULimit
	}
	if req.Unlisted {
		payload["unlisted"] = true
	}
	if len(req.StructuredOutputSchema) > 0 {
		payload["structured_output_schema"] = req.StructuredOutputSchema
	}

	var out struct {
		SessionID string `json:"session_id"`
		URL       string `json:"url"`
	}
	if err := c.core.doJSON(ctx, "POST", "/v1/sessions", payload, &out); err != nil {
		return nil, err
	}
	return &Session{ID: out.SessionID, URL: out.URL, State: StateWorking, Title: req.Title, Tags: req.Tags}, nil
}

// GetSession returns the normalized session detail, messages included.
func (c *v1Client) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	var out v1SessionDetail
	if err := c.core.doJSON(ctx, "GET", "/v1/sessions/"+url.PathEscape(sessionID), nil, &out); err != nil {
		return nil, err
	}
	return out.toSession(), nil
}

// ListSessions lists the org's sessions, optionally filtered by tags.
func (c *v1Client) ListSessions(ctx context.Context, opts ListSessionsOptions) ([]Session, error) {
	params := url.Values{}
	if opts.Limit > 0 {
		params.Set("limit", strconv.Itoa(opts.Limit))
	}
	for _, t := range opts.Tags {
		params.Add("tags", t)
	}
	path := "/v1/sessions"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out struct {
		Sessions []v1SessionDetail `json:"sessions"`
	}
	if err := c.core.doJSON(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(out.Sessions))
	for _, d := range out.Sessions {
		sessions = append(sessions, *d.toSession())
	}
	return sessions, nil
}

// SendMessage delivers a follow-up message to a running session. v1 has no
// attachment_urls on messages, so attachments ride as ATTACHMENT lines.
func (c *v1Client) SendMessage(ctx context.Context, sessionID, message string, attachmentURLs []string) error {
	for _, u := range attachmentURLs {
		message += "\nATTACHMENT:\"" + u + "\""
	}
	payload := map[string]any{"message": message}
	return c.core.doJSON(ctx, "POST", "/v1/sessions/"+url.PathEscape(sessionID)+"/message", payload, nil)
}

// ListMessages returns the session's conversation (embedded in the v1 detail
// payload — there is no dedicated endpoint in this generation).
func (c *v1Client) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	s, err := c.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return s.Messages, nil
}

// TerminateSession permanently stops the session.
func (c *v1Client) TerminateSession(ctx context.Context, sessionID string) error {
	return c.core.doJSON(ctx, "DELETE", "/v1/sessions/"+url.PathEscape(sessionID), nil, nil)
}

// ArchiveSession is not available on the v1 generation.
func (c *v1Client) ArchiveSession(_ context.Context, _ string) error {
	return fmt.Errorf("%w: archive (v1)", ErrNotSupported)
}

// SetSessionTags replaces the session's tags.
func (c *v1Client) SetSessionTags(ctx context.Context, sessionID string, tags []string) error {
	payload := map[string]any{"tags": tags}
	return c.core.doJSON(ctx, "PUT", "/v1/sessions/"+url.PathEscape(sessionID)+"/tags", payload, nil)
}

// UploadAttachment uploads a file; v1 answers with the bare URL string
// (optionally JSON-quoted).
func (c *v1Client) UploadAttachment(ctx context.Context, filename string, content io.Reader) (*Attachment, error) {
	raw, err := c.core.uploadMultipart(ctx, "/v1/attachments", filename, content)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(string(raw))
	var quoted string
	if json.Unmarshal(raw, &quoted) == nil && quoted != "" {
		body = quoted
	}
	if body == "" {
		return nil, fmt.Errorf("devin: attachment upload returned an empty URL")
	}
	return &Attachment{Name: filename, URL: body}, nil
}

// v1Secret is the wire shape of the v1 secret metadata.
type v1Secret struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Key       string `json:"key"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

// ListSecrets lists secret metadata (never values).
func (c *v1Client) ListSecrets(ctx context.Context) ([]Secret, error) {
	var out []v1Secret
	if err := c.core.doJSON(ctx, "GET", "/v1/secrets", nil, &out); err != nil {
		return nil, err
	}
	secrets := make([]Secret, 0, len(out))
	for _, s := range out {
		secrets = append(secrets, Secret{ID: s.ID, Key: s.Key, Type: s.Type, Note: s.Note, CreatedAt: v1Time(s.CreatedAt)})
	}
	return secrets, nil
}

// CreateSecret stores a new encrypted secret.
func (c *v1Client) CreateSecret(ctx context.Context, req CreateSecretRequest) (*Secret, error) {
	payload := map[string]any{
		"type":      req.Type,
		"key":       req.Key,
		"value":     req.Value,
		"sensitive": req.Sensitive,
		"note":      req.Note,
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.core.doJSON(ctx, "POST", "/v1/secrets", payload, &out); err != nil {
		return nil, err
	}
	return &Secret{ID: out.ID, Key: req.Key, Type: req.Type, Note: req.Note}, nil
}

// DeleteSecret permanently deletes a secret.
func (c *v1Client) DeleteSecret(ctx context.Context, secretID string) error {
	return c.core.doJSON(ctx, "DELETE", "/v1/secrets/"+url.PathEscape(secretID), nil, nil)
}

// v1Knowledge is the wire shape of a v1 knowledge entry.
type v1Knowledge struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Body               string  `json:"body"`
	TriggerDescription string  `json:"trigger_description"`
	CreatedAt          string  `json:"created_at"`
	ParentFolderID     *string `json:"parent_folder_id"`
	PinnedRepo         *string `json:"pinned_repo"`
}

// toNote converts the wire knowledge entry into the normalized note.
func (k v1Knowledge) toNote() KnowledgeNote {
	n := KnowledgeNote{
		ID:        k.ID,
		Name:      k.Name,
		Body:      k.Body,
		Trigger:   k.TriggerDescription,
		CreatedAt: v1Time(k.CreatedAt),
	}
	if k.ParentFolderID != nil {
		n.FolderID = *k.ParentFolderID
	}
	if k.PinnedRepo != nil {
		n.PinnedRepo = *k.PinnedRepo
	}
	return n
}

// ListKnowledge lists all knowledge entries.
func (c *v1Client) ListKnowledge(ctx context.Context) ([]KnowledgeNote, error) {
	var out struct {
		Knowledge []v1Knowledge `json:"knowledge"`
	}
	if err := c.core.doJSON(ctx, "GET", "/v1/knowledge", nil, &out); err != nil {
		return nil, err
	}
	notes := make([]KnowledgeNote, 0, len(out.Knowledge))
	for _, k := range out.Knowledge {
		notes = append(notes, k.toNote())
	}
	return notes, nil
}

// knowledgePayloadV1 maps the neutral request onto v1 field names.
func knowledgePayloadV1(req KnowledgeNoteRequest, includeEmpty bool) map[string]any {
	payload := map[string]any{}
	set := func(key, val string) {
		if val != "" || includeEmpty {
			payload[key] = val
		}
	}
	set("name", req.Name)
	set("body", req.Body)
	set("trigger_description", req.Trigger)
	if req.FolderID != "" {
		payload["parent_folder_id"] = req.FolderID
	}
	if req.PinnedRepo != "" {
		payload["pinned_repo"] = req.PinnedRepo
	}
	return payload
}

// CreateKnowledge creates a knowledge entry.
func (c *v1Client) CreateKnowledge(ctx context.Context, req KnowledgeNoteRequest) (*KnowledgeNote, error) {
	var out v1Knowledge
	if err := c.core.doJSON(ctx, "POST", "/v1/knowledge", knowledgePayloadV1(req, true), &out); err != nil {
		return nil, err
	}
	note := out.toNote()
	return &note, nil
}

// UpdateKnowledge patches a knowledge entry; empty fields are left untouched.
func (c *v1Client) UpdateKnowledge(ctx context.Context, noteID string, req KnowledgeNoteRequest) (*KnowledgeNote, error) {
	var out v1Knowledge
	if err := c.core.doJSON(ctx, "PATCH", "/v1/knowledge/"+url.PathEscape(noteID), knowledgePayloadV1(req, false), &out); err != nil {
		return nil, err
	}
	note := out.toNote()
	return &note, nil
}

// DeleteKnowledge removes a knowledge entry.
func (c *v1Client) DeleteKnowledge(ctx context.Context, noteID string) error {
	return c.core.doJSON(ctx, "DELETE", "/v1/knowledge/"+url.PathEscape(noteID), nil, nil)
}

// v1Playbook is the wire shape of a v1 playbook.
type v1Playbook struct {
	PlaybookID string `json:"playbook_id"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// toPlaybook converts the wire playbook into the normalized form.
func (p v1Playbook) toPlaybook() Playbook {
	return Playbook{
		ID:        p.PlaybookID,
		Title:     p.Title,
		Body:      p.Body,
		CreatedAt: v1Time(p.CreatedAt),
		UpdatedAt: v1Time(p.UpdatedAt),
	}
}

// ListPlaybooks lists the org's playbooks. The endpoint may answer either a
// bare array or a wrapped {"playbooks": []} object; both are accepted.
func (c *v1Client) ListPlaybooks(ctx context.Context) ([]Playbook, error) {
	var raw json.RawMessage
	if err := c.core.doJSON(ctx, "GET", "/v1/playbooks", nil, &raw); err != nil {
		return nil, err
	}
	var list []v1Playbook
	if err := json.Unmarshal(raw, &list); err != nil {
		var wrapped struct {
			Playbooks []v1Playbook `json:"playbooks"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("devin: decode playbooks: %w", err)
		}
		list = wrapped.Playbooks
	}
	playbooks := make([]Playbook, 0, len(list))
	for _, p := range list {
		playbooks = append(playbooks, p.toPlaybook())
	}
	return playbooks, nil
}

// GetPlaybook fetches one playbook by id.
func (c *v1Client) GetPlaybook(ctx context.Context, playbookID string) (*Playbook, error) {
	var out v1Playbook
	if err := c.core.doJSON(ctx, "GET", "/v1/playbooks/"+url.PathEscape(playbookID), nil, &out); err != nil {
		return nil, err
	}
	pb := out.toPlaybook()
	return &pb, nil
}

// CreatePlaybook creates a playbook.
func (c *v1Client) CreatePlaybook(ctx context.Context, req PlaybookRequest) (*Playbook, error) {
	payload := map[string]any{"title": req.Title, "body": req.Body}
	var out v1Playbook
	if err := c.core.doJSON(ctx, "POST", "/v1/playbooks", payload, &out); err != nil {
		return nil, err
	}
	pb := out.toPlaybook()
	return &pb, nil
}

// UpdatePlaybook patches a playbook; empty fields are left untouched.
func (c *v1Client) UpdatePlaybook(ctx context.Context, playbookID string, req PlaybookRequest) (*Playbook, error) {
	payload := map[string]any{}
	if req.Title != "" {
		payload["title"] = req.Title
	}
	if req.Body != "" {
		payload["body"] = req.Body
	}
	var out v1Playbook
	if err := c.core.doJSON(ctx, "PATCH", "/v1/playbooks/"+url.PathEscape(playbookID), payload, &out); err != nil {
		return nil, err
	}
	pb := out.toPlaybook()
	return &pb, nil
}

// DeletePlaybook removes a playbook.
func (c *v1Client) DeletePlaybook(ctx context.Context, playbookID string) error {
	return c.core.doJSON(ctx, "DELETE", "/v1/playbooks/"+url.PathEscape(playbookID), nil, nil)
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
