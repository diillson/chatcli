/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package devin integrates Cognition's Devin (https://docs.devin.ai) into
 * ChatCLI, covering BOTH public API generations behind one normalized surface:
 *
 *   - v1  — the individual/Teams API (api.devin.ai/v1/*), authenticated with
 *     personal (`apk_user_…`) or org service (`apk_…`) API keys. Legacy but
 *     fully functional; it is what standard Teams users have today.
 *   - v3  — the organizations/enterprise API (api.devin.ai/v3/organizations/
 *     {org_id}/*), authenticated with service-user credentials (`cog_…`) and
 *     governed by RBAC.
 *
 * The two generations expose the same workflow primitives (sessions,
 * messages, attachments, secrets, knowledge, playbooks, tags) with different
 * paths, auth and wire types (RFC3339 strings vs unix timestamps, enums with
 * different vocabularies). types.go defines the version-neutral model every
 * caller sees; v1.go / v3.go translate to and from the wire.
 */
package devin

import (
	"encoding/json"
	"time"
)

// SessionState is the normalized lifecycle state of a Devin session, mapped
// from the version-specific status vocabularies (v1 status_enum vs v3
// status/status_detail).
type SessionState string

const (
	// StateWorking — Devin is actively executing the task.
	StateWorking SessionState = "working"
	// StateBlocked — Devin stopped and is waiting for user input. This is the
	// natural "turn boundary" the provider adapter waits for.
	StateBlocked SessionState = "blocked"
	// StateFinished — the session completed its task.
	StateFinished SessionState = "finished"
	// StateExpired — the session expired or was terminated and cannot resume.
	StateExpired SessionState = "expired"
	// StateSuspended — the VM is asleep; sending a message resumes it.
	StateSuspended SessionState = "suspended"
	// StateError — the session ended with an error.
	StateError SessionState = "error"
	// StateUnknown — the API reported a status this client does not know.
	StateUnknown SessionState = "unknown"
)

// TurnBoundary reports whether the state ends a conversational turn: Devin is
// either waiting for the user or will never produce further output.
func (s SessionState) TurnBoundary() bool {
	switch s {
	case StateBlocked, StateFinished, StateExpired, StateError:
		return true
	}
	return false
}

// Terminal reports whether the session can never make progress again.
func (s SessionState) Terminal() bool {
	return s == StateFinished || s == StateExpired || s == StateError
}

// PullRequest is a PR produced by a session.
type PullRequest struct {
	URL   string `json:"url"`
	State string `json:"state,omitempty"`
}

// Session is the normalized view of a Devin session across v1 and v3.
type Session struct {
	ID               string          `json:"session_id"`
	URL              string          `json:"url,omitempty"`
	State            SessionState    `json:"state"`
	RawStatus        string          `json:"raw_status,omitempty"`
	StatusDetail     string          `json:"status_detail,omitempty"`
	Title            string          `json:"title,omitempty"`
	Tags             []string        `json:"tags,omitempty"`
	CreatedAt        time.Time       `json:"created_at,omitzero"`
	UpdatedAt        time.Time       `json:"updated_at,omitzero"`
	PlaybookID       string          `json:"playbook_id,omitempty"`
	SnapshotID       string          `json:"snapshot_id,omitempty"`
	PullRequests     []PullRequest   `json:"pull_requests,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	ACUsConsumed     float64         `json:"acus_consumed,omitempty"`
	IsArchived       bool            `json:"is_archived,omitempty"`
	// Messages is populated by GetSession on v1 (embedded in the detail
	// payload) and by ListMessages on both versions.
	Messages []Message `json:"messages,omitempty"`
}

// Message is one event in a session's conversation.
type Message struct {
	EventID   string    `json:"event_id,omitempty"`
	FromUser  bool      `json:"from_user"`
	Text      string    `json:"message"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// CreateSessionRequest carries the version-neutral session creation options.
// Fields that exist in only one generation are documented as such and are
// silently ignored by the other (never an error: the caller writes one code
// path for both personas).
type CreateSessionRequest struct {
	Prompt string
	Title  string
	Tags   []string
	// PlaybookID applies to both generations.
	PlaybookID string
	// SnapshotID (v1) restores machine state from a snapshot.
	SnapshotID string
	// Mode (v3) selects the agent mode: normal|fast|lite|ultra|fusion.
	Mode string
	// AttachmentURLs (v3) attaches previously uploaded files. On v1 the same
	// effect is achieved by ATTACHMENT:"url" lines in the prompt, which the
	// v1 client appends automatically.
	AttachmentURLs []string
	// CreateAsUserID (v3) attributes the session to another user; requires
	// the ImpersonateOrgSessions permission.
	CreateAsUserID string
	// MaxACULimit (v1) caps session cost in ACUs.
	MaxACULimit int
	// Unlisted (v1) hides the session from the org list.
	Unlisted bool
	// StructuredOutputSchema is a JSON Schema the session output must follow.
	StructuredOutputSchema json.RawMessage
}

// ListSessionsOptions filters ListSessions. Limit caps the page size; Tags
// filters by tag on both generations.
type ListSessionsOptions struct {
	Limit int
	Tags  []string
}

// Attachment is an uploaded file reference. v1 returns only a URL; v3 also
// returns an ID and echoes the name.
type Attachment struct {
	ID   string `json:"attachment_id,omitempty"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url"`
}

// Secret is normalized secret metadata (values are never returned by either
// API generation).
type Secret struct {
	ID        string    `json:"id"`
	Key       string    `json:"key,omitempty"`
	Type      string    `json:"type,omitempty"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// CreateSecretRequest creates an org-level secret. Type is one of
// key-value|cookie|totp (both generations use the same vocabulary).
type CreateSecretRequest struct {
	Type      string
	Key       string
	Value     string
	Sensitive bool
	Note      string
}

// KnowledgeNote is a normalized knowledge entry (v1 "knowledge", v3 "note").
type KnowledgeNote struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Body       string    `json:"body,omitempty"`
	Trigger    string    `json:"trigger,omitempty"`
	FolderID   string    `json:"folder_id,omitempty"`
	PinnedRepo string    `json:"pinned_repo,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitzero"`
}

// KnowledgeNoteRequest carries create/update fields for a knowledge entry.
// Zero-valued fields are omitted on update.
type KnowledgeNoteRequest struct {
	Name       string
	Body       string
	Trigger    string
	FolderID   string
	PinnedRepo string
}

// Playbook is a normalized playbook across both generations.
type Playbook struct {
	ID        string    `json:"playbook_id"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// PlaybookRequest carries create/update fields for a playbook.
type PlaybookRequest struct {
	Title string
	Body  string
}
