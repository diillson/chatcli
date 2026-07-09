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

// v3Client implements API against the organizations/enterprise generation
// (api.devin.ai/v3/organizations/{org_id}/*), authenticated with service-user
// credentials (`cog_…`) under RBAC.
type v3Client struct {
	core  *apiCore
	orgID string
}

// Version returns "v3".
func (*v3Client) Version() string { return "v3" }

// orgPath builds an organization-scoped path.
func (c *v3Client) orgPath(suffix string) string {
	return "/v3/organizations/" + url.PathEscape(c.orgID) + suffix
}

// v3Time converts the unix-seconds timestamps v3 uses.
func v3Time(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// v3MapState normalizes the v3 status + status_detail pair. status carries
// the machine lifecycle; status_detail refines it (waiting_for_user is the
// turn boundary even while the machine still reports running/suspended).
func v3MapState(status, detail string) SessionState {
	detail = strings.ToLower(strings.TrimSpace(detail))
	switch detail {
	case "waiting_for_user", "waiting_for_approval":
		return StateBlocked
	case "finished":
		return StateFinished
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "new", "claimed", "running", "resuming":
		return StateWorking
	case "exit":
		return StateFinished
	case "error":
		return StateError
	case "suspended":
		return StateSuspended
	case "":
		return StateUnknown
	default:
		return StateUnknown
	}
}

// v3Session is the wire shape of the v3 SessionResponse.
type v3Session struct {
	SessionID    string   `json:"session_id"`
	URL          string   `json:"url"`
	Status       string   `json:"status"`
	StatusDetail *string  `json:"status_detail"`
	Tags         []string `json:"tags"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
	ACUsConsumed float64  `json:"acus_consumed"`
	PullRequests []struct {
		PRURL   string  `json:"pr_url"`
		PRState *string `json:"pr_state"`
	} `json:"pull_requests"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Title            *string         `json:"title"`
	PlaybookID       *string         `json:"playbook_id"`
	IsArchived       bool            `json:"is_archived"`
}

// toSession converts the wire session into the normalized Session.
func (d v3Session) toSession() *Session {
	detail := ""
	if d.StatusDetail != nil {
		detail = *d.StatusDetail
	}
	s := &Session{
		ID:               d.SessionID,
		URL:              d.URL,
		State:            v3MapState(d.Status, detail),
		RawStatus:        d.Status,
		StatusDetail:     detail,
		Tags:             d.Tags,
		CreatedAt:        v3Time(d.CreatedAt),
		UpdatedAt:        v3Time(d.UpdatedAt),
		ACUsConsumed:     d.ACUsConsumed,
		StructuredOutput: d.StructuredOutput,
		IsArchived:       d.IsArchived,
	}
	if d.Title != nil {
		s.Title = *d.Title
	}
	if d.PlaybookID != nil {
		s.PlaybookID = *d.PlaybookID
	}
	for _, pr := range d.PullRequests {
		state := ""
		if pr.PRState != nil {
			state = *pr.PRState
		}
		s.PullRequests = append(s.PullRequests, PullRequest{URL: pr.PRURL, State: state})
	}
	return s
}

// CreateSession starts a new Devin session in the organization.
func (c *v3Client) CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error) {
	payload := map[string]any{"prompt": req.Prompt}
	if req.Title != "" {
		payload["title"] = req.Title
	}
	if len(req.Tags) > 0 {
		payload["tags"] = req.Tags
	}
	if req.PlaybookID != "" {
		payload["playbook_id"] = req.PlaybookID
	}
	if req.Mode != "" {
		payload["devin_mode"] = req.Mode
	}
	if len(req.AttachmentURLs) > 0 {
		payload["attachment_urls"] = req.AttachmentURLs
	}
	if req.CreateAsUserID != "" {
		payload["create_as_user_id"] = req.CreateAsUserID
	}
	if len(req.StructuredOutputSchema) > 0 {
		payload["structured_output_schema"] = req.StructuredOutputSchema
	}

	var out v3Session
	if err := c.core.doJSON(ctx, "POST", c.orgPath("/sessions"), payload, &out); err != nil {
		return nil, err
	}
	return out.toSession(), nil
}

// GetSession returns the normalized session detail.
func (c *v3Client) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	var out v3Session
	if err := c.core.doJSON(ctx, "GET", c.orgPath("/sessions/"+url.PathEscape(sessionID)), nil, &out); err != nil {
		return nil, err
	}
	return out.toSession(), nil
}

// ListSessions lists the org's sessions with cursor pagination collapsed to a
// single page (first = Limit, default 100).
func (c *v3Client) ListSessions(ctx context.Context, opts ListSessionsOptions) ([]Session, error) {
	params := url.Values{}
	if opts.Limit > 0 {
		params.Set("first", strconv.Itoa(opts.Limit))
	}
	for _, t := range opts.Tags {
		params.Add("tags", t)
	}
	path := c.orgPath("/sessions")
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out struct {
		Items []v3Session `json:"items"`
	}
	if err := c.core.doJSON(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(out.Items))
	for _, d := range out.Items {
		sessions = append(sessions, *d.toSession())
	}
	return sessions, nil
}

// SendMessage delivers a follow-up message; a suspended session is resumed
// automatically by the API.
func (c *v3Client) SendMessage(ctx context.Context, sessionID, message string, attachmentURLs []string) error {
	payload := map[string]any{"message": message}
	if len(attachmentURLs) > 0 {
		payload["attachment_urls"] = attachmentURLs
	}
	return c.core.doJSON(ctx, "POST", c.orgPath("/sessions/"+url.PathEscape(sessionID)+"/messages"), payload, nil)
}

// ListMessages walks the cursor-paginated message log in chronological order.
func (c *v3Client) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	base := c.orgPath("/sessions/" + url.PathEscape(sessionID) + "/messages")
	var all []Message
	cursor := ""
	// Hard cap of 50 pages (10k messages) protects against a pathological
	// pagination loop; real sessions are orders of magnitude smaller.
	for page := 0; page < 50; page++ {
		params := url.Values{}
		params.Set("first", "200")
		if cursor != "" {
			params.Set("after", cursor)
		}
		var out struct {
			Items []struct {
				EventID   string `json:"event_id"`
				Source    string `json:"source"`
				Message   string `json:"message"`
				CreatedAt int64  `json:"created_at"`
			} `json:"items"`
			EndCursor   string `json:"end_cursor"`
			HasNextPage bool   `json:"has_next_page"`
		}
		if err := c.core.doJSON(ctx, "GET", base+"?"+params.Encode(), nil, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Items {
			all = append(all, Message{
				EventID:   m.EventID,
				FromUser:  strings.EqualFold(m.Source, "user"),
				Text:      m.Message,
				CreatedAt: v3Time(m.CreatedAt),
			})
		}
		if !out.HasNextPage || out.EndCursor == "" {
			break
		}
		cursor = out.EndCursor
	}
	return all, nil
}

// TerminateSession permanently stops the session.
func (c *v3Client) TerminateSession(ctx context.Context, sessionID string) error {
	return c.core.doJSON(ctx, "DELETE", c.orgPath("/sessions/"+url.PathEscape(sessionID)), nil, nil)
}

// ArchiveSession archives the session (and puts it to sleep if running).
func (c *v3Client) ArchiveSession(ctx context.Context, sessionID string) error {
	return c.core.doJSON(ctx, "POST", c.orgPath("/sessions/"+url.PathEscape(sessionID)+"/archive"), nil, nil)
}

// SetSessionTags replaces the session's tags.
func (c *v3Client) SetSessionTags(ctx context.Context, sessionID string, tags []string) error {
	payload := map[string]any{"tags": tags}
	return c.core.doJSON(ctx, "PUT", c.orgPath("/sessions/"+url.PathEscape(sessionID)+"/tags"), payload, nil)
}

// UploadAttachment uploads a file; v3 answers {attachment_id, name, url}.
func (c *v3Client) UploadAttachment(ctx context.Context, filename string, content io.Reader) (*Attachment, error) {
	raw, err := c.core.uploadMultipart(ctx, c.orgPath("/attachments"), filename, content)
	if err != nil {
		return nil, err
	}
	var out Attachment
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("devin: decode attachment response: %w", err)
	}
	if out.URL == "" {
		return nil, fmt.Errorf("devin: attachment upload returned an empty URL")
	}
	if out.Name == "" {
		out.Name = filename
	}
	return &out, nil
}

// v3Secret is the wire shape of the v3 secret metadata.
type v3Secret struct {
	SecretID   string `json:"secret_id"`
	Key        string `json:"key"`
	Note       string `json:"note"`
	SecretType string `json:"secret_type"`
	CreatedAt  int64  `json:"created_at"`
}

// toSecret converts the wire secret into the normalized form.
func (s v3Secret) toSecret() Secret {
	return Secret{ID: s.SecretID, Key: s.Key, Type: s.SecretType, Note: s.Note, CreatedAt: v3Time(s.CreatedAt)}
}

// ListSecrets lists org-level secret metadata. Accepts either a bare array
// or a wrapped {"items": []} page.
func (c *v3Client) ListSecrets(ctx context.Context) ([]Secret, error) {
	var raw json.RawMessage
	if err := c.core.doJSON(ctx, "GET", c.orgPath("/secrets"), nil, &raw); err != nil {
		return nil, err
	}
	list, err := decodeListOrItems[v3Secret](raw)
	if err != nil {
		return nil, fmt.Errorf("devin: decode secrets: %w", err)
	}
	secrets := make([]Secret, 0, len(list))
	for _, s := range list {
		secrets = append(secrets, s.toSecret())
	}
	return secrets, nil
}

// CreateSecret stores a new org-level secret.
func (c *v3Client) CreateSecret(ctx context.Context, req CreateSecretRequest) (*Secret, error) {
	payload := map[string]any{
		"type":         req.Type,
		"key":          req.Key,
		"value":        req.Value,
		"is_sensitive": req.Sensitive,
	}
	if req.Note != "" {
		payload["note"] = req.Note
	}
	var out v3Secret
	if err := c.core.doJSON(ctx, "POST", c.orgPath("/secrets"), payload, &out); err != nil {
		return nil, err
	}
	secret := out.toSecret()
	return &secret, nil
}

// DeleteSecret removes an org-level secret.
func (c *v3Client) DeleteSecret(ctx context.Context, secretID string) error {
	return c.core.doJSON(ctx, "DELETE", c.orgPath("/secrets/"+url.PathEscape(secretID)), nil, nil)
}

// v3Note is the wire shape of the v3 KnowledgeNoteResponse.
type v3Note struct {
	NoteID     string  `json:"note_id"`
	Name       string  `json:"name"`
	Body       string  `json:"body"`
	Trigger    string  `json:"trigger"`
	FolderID   *string `json:"folder_id"`
	PinnedRepo *string `json:"pinned_repo"`
	CreatedAt  int64   `json:"created_at"`
}

// toNote converts the wire note into the normalized form.
func (n v3Note) toNote() KnowledgeNote {
	note := KnowledgeNote{
		ID:        n.NoteID,
		Name:      n.Name,
		Body:      n.Body,
		Trigger:   n.Trigger,
		CreatedAt: v3Time(n.CreatedAt),
	}
	if n.FolderID != nil {
		note.FolderID = *n.FolderID
	}
	if n.PinnedRepo != nil {
		note.PinnedRepo = *n.PinnedRepo
	}
	return note
}

// ListKnowledge lists org-level knowledge notes.
func (c *v3Client) ListKnowledge(ctx context.Context) ([]KnowledgeNote, error) {
	var raw json.RawMessage
	if err := c.core.doJSON(ctx, "GET", c.orgPath("/knowledge/notes"), nil, &raw); err != nil {
		return nil, err
	}
	list, err := decodeListOrItems[v3Note](raw)
	if err != nil {
		return nil, fmt.Errorf("devin: decode knowledge notes: %w", err)
	}
	notes := make([]KnowledgeNote, 0, len(list))
	for _, n := range list {
		notes = append(notes, n.toNote())
	}
	return notes, nil
}

// knowledgePayloadV3 maps the neutral request onto v3 field names.
func knowledgePayloadV3(req KnowledgeNoteRequest, includeEmpty bool) map[string]any {
	payload := map[string]any{}
	set := func(key, val string) {
		if val != "" || includeEmpty {
			payload[key] = val
		}
	}
	set("name", req.Name)
	set("body", req.Body)
	set("trigger", req.Trigger)
	if req.FolderID != "" {
		payload["folder_id"] = req.FolderID
	}
	if req.PinnedRepo != "" {
		payload["pinned_repo"] = req.PinnedRepo
	}
	return payload
}

// CreateKnowledge creates an org-level knowledge note.
func (c *v3Client) CreateKnowledge(ctx context.Context, req KnowledgeNoteRequest) (*KnowledgeNote, error) {
	var out v3Note
	if err := c.core.doJSON(ctx, "POST", c.orgPath("/knowledge/notes"), knowledgePayloadV3(req, true), &out); err != nil {
		return nil, err
	}
	note := out.toNote()
	return &note, nil
}

// UpdateKnowledge updates an org-level knowledge note.
func (c *v3Client) UpdateKnowledge(ctx context.Context, noteID string, req KnowledgeNoteRequest) (*KnowledgeNote, error) {
	var out v3Note
	if err := c.core.doJSON(ctx, "PUT", c.orgPath("/knowledge/notes/"+url.PathEscape(noteID)), knowledgePayloadV3(req, false), &out); err != nil {
		return nil, err
	}
	note := out.toNote()
	return &note, nil
}

// DeleteKnowledge removes an org-level knowledge note.
func (c *v3Client) DeleteKnowledge(ctx context.Context, noteID string) error {
	return c.core.doJSON(ctx, "DELETE", c.orgPath("/knowledge/notes/"+url.PathEscape(noteID)), nil, nil)
}

// v3Playbook is the wire shape of a v3 playbook.
type v3Playbook struct {
	PlaybookID string `json:"playbook_id"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// toPlaybook converts the wire playbook into the normalized form.
func (p v3Playbook) toPlaybook() Playbook {
	return Playbook{
		ID:        p.PlaybookID,
		Title:     p.Title,
		Body:      p.Body,
		CreatedAt: v3Time(p.CreatedAt),
		UpdatedAt: v3Time(p.UpdatedAt),
	}
}

// ListPlaybooks lists org-level playbooks.
func (c *v3Client) ListPlaybooks(ctx context.Context) ([]Playbook, error) {
	var raw json.RawMessage
	if err := c.core.doJSON(ctx, "GET", c.orgPath("/playbooks"), nil, &raw); err != nil {
		return nil, err
	}
	list, err := decodeListOrItems[v3Playbook](raw)
	if err != nil {
		return nil, fmt.Errorf("devin: decode playbooks: %w", err)
	}
	playbooks := make([]Playbook, 0, len(list))
	for _, p := range list {
		playbooks = append(playbooks, p.toPlaybook())
	}
	return playbooks, nil
}

// GetPlaybook fetches one org-level playbook by id.
func (c *v3Client) GetPlaybook(ctx context.Context, playbookID string) (*Playbook, error) {
	var out v3Playbook
	if err := c.core.doJSON(ctx, "GET", c.orgPath("/playbooks/"+url.PathEscape(playbookID)), nil, &out); err != nil {
		return nil, err
	}
	pb := out.toPlaybook()
	return &pb, nil
}

// CreatePlaybook creates an org-level playbook.
func (c *v3Client) CreatePlaybook(ctx context.Context, req PlaybookRequest) (*Playbook, error) {
	payload := map[string]any{"title": req.Title, "body": req.Body}
	var out v3Playbook
	if err := c.core.doJSON(ctx, "POST", c.orgPath("/playbooks"), payload, &out); err != nil {
		return nil, err
	}
	pb := out.toPlaybook()
	return &pb, nil
}

// UpdatePlaybook replaces an org-level playbook's content.
func (c *v3Client) UpdatePlaybook(ctx context.Context, playbookID string, req PlaybookRequest) (*Playbook, error) {
	payload := map[string]any{"title": req.Title, "body": req.Body}
	var out v3Playbook
	if err := c.core.doJSON(ctx, "PUT", c.orgPath("/playbooks/"+url.PathEscape(playbookID)), payload, &out); err != nil {
		return nil, err
	}
	pb := out.toPlaybook()
	return &pb, nil
}

// DeletePlaybook removes an org-level playbook.
func (c *v3Client) DeletePlaybook(ctx context.Context, playbookID string) error {
	return c.core.doJSON(ctx, "DELETE", c.orgPath("/playbooks/"+url.PathEscape(playbookID)), nil, nil)
}

// decodeListOrItems accepts either a bare JSON array or a paginated
// {"items": [...]} wrapper — the v3 list endpoints are documented with both
// shapes across resources.
func decodeListOrItems[T any](raw json.RawMessage) ([]T, error) {
	var list []T
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var wrapped struct {
		Items []T `json:"items"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Items, nil
}
