/*
 * Snapshot — durable view of an agent's state at park time.
 *
 * The snapshot is written atomically (write-tmp + fsync + rename) and
 * read once at resume time. Schema is versioned; readers reject foreign
 * versions with a clear error so future bumps don't crash older binaries
 * on the same machine.
 */
package park

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// SchemaVersion bumps when the on-disk shape of Snapshot changes in a
// way that older readers cannot tolerate. Increment in lockstep with
// readers; never repurpose old fields silently.
const SchemaVersion = 1

// Snapshot captures everything the agent ReAct loop needs to resume in
// the user's interactive session. The chat history is the dominant
// payload and is the reason snapshots can grow MB-scale.
type Snapshot struct {
	Version   int       `json:"version"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`

	// History is the cli.history at park time. Restored verbatim.
	History []models.Message `json:"history"`

	// Interactive-loop counters, restored so /jobs and metrics keep
	// counting accurately across the park boundary.
	AgentsLaunched int `json:"agents_launched,omitempty"`
	ToolCallsExecd int `json:"tool_calls_execd,omitempty"`

	// Mode flags select the loop entrypoint at resume time.
	IsCoderMode bool `json:"is_coder_mode,omitempty"`
	IsOneShot   bool `json:"is_one_shot,omitempty"`

	// Provider/Model/Skill hints capture which client to use at resume.
	// If empty, the CLI's current selection wins.
	Provider        string                `json:"provider,omitempty"`
	Model           string                `json:"model,omitempty"`
	SkillModelHint  string                `json:"skill_model_hint,omitempty"`
	SkillEffortHint llmclient.SkillEffort `json:"skill_effort_hint,omitempty"`

	// OriginalQuery is the user's original /coder or /agent prompt,
	// retained for /parked rendering and audit trails.
	OriginalQuery string `json:"original_query,omitempty"`

	// SystemPrompt was passed at original Run() time (CoderSystemPrompt
	// or empty). The resume re-applies it identically.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// AdditionalContext is the auxiliary context block (file attachments
	// etc.) the original Run received. Re-applied at resume.
	AdditionalContext string `json:"additional_context,omitempty"`

	// Park describes what we're waiting for. Drives the scheduler.
	Park Request `json:"park"`

	// SchedulerJobID is the JobID of the resume job spawned in the
	// scheduler. Stored so /cancel-park can target it cleanly.
	SchedulerJobID string `json:"scheduler_job_id,omitempty"`

	// PendingToolCallID is the native (Anthropic-style) tool_use ID that
	// the @park invocation reserved. Empty for XML-mode parks. Resume
	// uses this to synthesize a Role=tool message that closes the call
	// pair so the next API request validates against Anthropic's strict
	// tool_use/tool_result pairing.
	PendingToolCallID string `json:"pending_tool_call_id,omitempty"`
	PendingToolName   string `json:"pending_tool_name,omitempty"`

	// LastResumeAt is set when the snapshot is consumed by a resume.
	// Useful for forensics if the loop crashes mid-resume.
	LastResumeAt time.Time `json:"last_resume_at,omitempty"`
}

var (
	// ErrSnapshotNotFound is returned by Load when the token has no file.
	ErrSnapshotNotFound = errors.New("park: snapshot not found")
	// ErrInvalidToken is returned for empty / malformed tokens.
	ErrInvalidToken = errors.New("park: invalid token")
	// ErrSchemaMismatch is returned when on-disk Version is incompatible.
	ErrSchemaMismatch = errors.New("park: snapshot schema mismatch")
)

// tokenRegexp matches the safe charset for snapshot tokens — kept
// narrow so untrusted input (e.g. /resume <token> from a shared tail of
// scrollback) cannot path-traverse out of Dir().
var tokenRegexp = regexp.MustCompile(`^[a-zA-Z0-9._-]{8,128}$`)

// validateToken rejects empty / unsafe tokens.
func validateToken(t string) error {
	if !tokenRegexp.MatchString(t) {
		return fmt.Errorf("%w: %q", ErrInvalidToken, t)
	}
	return nil
}

// NewToken returns a random 16-byte hex token, suitable for use as a
// filename component and for /parked listing.
func NewToken() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		// Crypto rand can only fail under extreme OS failure; fall back
		// to a deterministic but still namespaced token rather than
		// panicking — losing the park to an OS error helps no one.
		return "park-fallback-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

// Save writes the snapshot atomically. The file is written with mode
// 0o600 because it embeds the chat history.
//
// The implementation uses the standard write-tmp + fsync + rename
// pattern: a partial write (e.g. crash mid-write) leaves the *.tmp
// behind and the canonical file untouched, so the next Load either
// reads the prior snapshot or returns NotFound.
func (s *Snapshot) Save() error {
	if err := validateToken(s.Token); err != nil {
		return err
	}
	if s.Version == 0 {
		s.Version = SchemaVersion
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}

	path, err := snapshotPath(s.Token)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("park: marshal snapshot: %w", err)
	}

	// #nosec G304 -- tmp path is built from s.Token which is validated
	// against tokenRegexp ([a-zA-Z0-9._-]{8,128}) before reaching here;
	// directory traversal is not possible.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("park: open tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("park: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("park: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("park: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("park: rename: %w", err)
	}
	return nil
}

// Load reads a snapshot by token. ErrSnapshotNotFound when missing,
// ErrSchemaMismatch when on-disk Version is unsupported.
func Load(token string) (*Snapshot, error) {
	if err := validateToken(token); err != nil {
		return nil, err
	}
	path, err := snapshotPath(token)
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- path is built from token which is validated against
	// tokenRegexp ([a-zA-Z0-9._-]{8,128}) before reaching here.
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSnapshotNotFound
		}
		return nil, fmt.Errorf("park: read: %w", err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("park: unmarshal: %w", err)
	}
	if s.Version != SchemaVersion {
		return nil, fmt.Errorf("%w: file=%d expected=%d", ErrSchemaMismatch, s.Version, SchemaVersion)
	}
	return &s, nil
}

// Delete removes the snapshot file. Best-effort: a missing file is not
// an error. Used by /cancel-park and post-resume cleanup.
func Delete(token string) error {
	if err := validateToken(token); err != nil {
		return err
	}
	path, err := snapshotPath(token)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("park: delete: %w", err)
	}
	return nil
}

// List returns every snapshot currently on disk, sorted by CreatedAt
// descending (newest first). Files that fail to parse are skipped with
// a returned aggregate of read errors so /parked can surface partial
// failures without blocking the listing.
func List() ([]*Snapshot, []error) {
	dir, err := Dir()
	if err != nil {
		return nil, []error{err}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{err}
	}
	var (
		snaps  = make([]*Snapshot, 0, len(entries))
		errs   []error
		suffix = ".json"
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		token := strings.TrimSuffix(name, suffix)
		if err := validateToken(token); err != nil {
			continue
		}
		s, err := Load(token)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", token, err))
			continue
		}
		snaps = append(snaps, s)
	}
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.After(snaps[j].CreatedAt)
	})
	return snaps, errs
}

// PathFor returns the on-disk path for a token. Useful for callers that
// want to display where the snapshot lives. Validates the token.
func PathFor(token string) (string, error) {
	if err := validateToken(token); err != nil {
		return "", err
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, token+".json"), nil
}
