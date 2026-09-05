/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// QuarantineStateFile lives inside the plugins directory. The manager skips
// dotfiles when loading, so the state cannot be mistaken for a plugin.
const QuarantineStateFile = ".quarantine.json"

// DefaultQuarantineWindow is what CHATCLI_PLUGIN_QUARANTINE=on resolves to.
const DefaultQuarantineWindow = 24 * time.Hour

// Quarantine holds newly seen unsigned plugins out of the runtime for a
// configured window.
//
// It applies only to unsigned plugins, and only when
// CHATCLI_ALLOW_UNSIGNED_PLUGINS has already opened that door: a signature
// that verifies against a trusted key is a stronger statement than any
// waiting period, and is admitted at once. What quarantine buys is the gap
// between "a binary appeared in the plugins directory" and "that binary
// runs with the same permissions as ChatCLI" — long enough for a human, or
// an endpoint agent, to notice a plugin nobody installed on purpose.
//
// It is off by default. A delay between installing a plugin and being able
// to use it is a real cost, and imposing it on everyone to harden a mode
// that is itself opt-in would trade a certain annoyance for a speculative
// gain. Turn it on where unsigned plugins are tolerated but unreviewed
// ones are not.
type Quarantine struct {
	mu        sync.Mutex
	statePath string
	window    time.Duration
	state     quarantineState
	loaded    bool
}

type quarantineState struct {
	Plugins map[string]quarantineRecord `json:"plugins"`
}

type quarantineRecord struct {
	FirstSeen  time.Time `json:"first_seen"`
	SHA256     string    `json:"sha256"`
	Released   bool      `json:"released,omitempty"`
	ReleasedAt time.Time `json:"released_at,omitempty"`
}

// QuarantineEntry is one plugin's status, for display.
type QuarantineEntry struct {
	Name      string
	FirstSeen time.Time
	Remaining time.Duration
	Released  bool
}

// NewQuarantine builds the gate for a plugins directory, reading its window
// from CHATCLI_PLUGIN_QUARANTINE.
func NewQuarantine(pluginsDir string) *Quarantine {
	return &Quarantine{
		statePath: filepath.Join(pluginsDir, QuarantineStateFile),
		window:    quarantineWindowFromEnv(),
	}
}

// quarantineWindowFromEnv parses CHATCLI_PLUGIN_QUARANTINE.
//
// Accepted: a Go duration ("24h", "30m"), "on" for the default window, and
// "off"/"0"/unset to disable. An unparseable value disables rather than
// guesses, and the caller logs it — a typo must not silently impose a delay
// nobody asked for, nor silently skip one somebody did.
func quarantineWindowFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CHATCLI_PLUGIN_QUARANTINE"))
	switch strings.ToLower(raw) {
	case "", "off", "false", "0", "no":
		return 0
	case "on", "true", "yes":
		return DefaultQuarantineWindow
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// Enabled reports whether a quarantine window is configured.
func (q *Quarantine) Enabled() bool {
	return q != nil && q.window > 0
}

// Window returns the configured holding period.
func (q *Quarantine) Window() time.Duration {
	if q == nil {
		return 0
	}
	return q.window
}

// ConfiguredButUnparseable reports a CHATCLI_PLUGIN_QUARANTINE value that
// was set to something this package could not read, so the caller can warn
// instead of leaving the operator believing quarantine is on.
func ConfiguredButUnparseable() (raw string, bad bool) {
	raw = strings.TrimSpace(os.Getenv("CHATCLI_PLUGIN_QUARANTINE"))
	if raw == "" {
		return "", false
	}
	switch strings.ToLower(raw) {
	case "off", "false", "0", "no", "on", "true", "yes":
		return raw, false
	}
	if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
		return raw, false
	}
	return raw, true
}

// Admit decides whether an unsigned plugin may load now.
//
// A binary whose digest differs from the recorded one starts its wait over:
// replacing the file is installing a different plugin, whatever the name on
// disk stayed.
func (q *Quarantine) Admit(pluginPath string) (admitted bool, remaining time.Duration) {
	if !q.Enabled() {
		return true, 0
	}

	digest, err := PluginDigest(pluginPath)
	if err != nil {
		// A binary that cannot be read cannot be admitted; the manager
		// will fail to load it in a moment anyway.
		return false, q.window
	}
	sum := hex.EncodeToString(digest)
	name := filepath.Base(pluginPath)

	q.mu.Lock()
	defer q.mu.Unlock()
	q.loadLocked()

	rec, seen := q.state.Plugins[name]
	if !seen || rec.SHA256 != sum {
		q.state.Plugins[name] = quarantineRecord{FirstSeen: time.Now().UTC(), SHA256: sum}
		q.saveLocked()
		return false, q.window
	}

	if rec.Released {
		return true, 0
	}

	elapsed := time.Since(rec.FirstSeen)
	if elapsed >= q.window {
		return true, 0
	}
	return false, q.window - elapsed
}

// Release admits a plugin immediately, recording that a human vouched for
// this exact binary. A later replacement re-enters quarantine, because the
// release was for the bytes that were reviewed, not for the filename.
func (q *Quarantine) Release(name string) error {
	if q == nil {
		return fmt.Errorf("quarantine is not configured")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.loadLocked()

	rec, ok := q.state.Plugins[name]
	if !ok {
		return fmt.Errorf("no quarantined plugin named %q", name)
	}
	rec.Released = true
	rec.ReleasedAt = time.Now().UTC()
	q.state.Plugins[name] = rec
	q.saveLocked()
	return nil
}

// Forget drops a plugin's record, so an uninstalled plugin does not leave
// state behind that would admit its name again later.
func (q *Quarantine) Forget(name string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.loadLocked()
	if _, ok := q.state.Plugins[name]; ok {
		delete(q.state.Plugins, name)
		q.saveLocked()
	}
}

// List returns every recorded plugin, newest wait first.
func (q *Quarantine) List() []QuarantineEntry {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.loadLocked()

	out := make([]QuarantineEntry, 0, len(q.state.Plugins))
	for name, rec := range q.state.Plugins {
		e := QuarantineEntry{Name: name, FirstSeen: rec.FirstSeen, Released: rec.Released}
		if !rec.Released && q.window > 0 {
			if left := q.window - time.Since(rec.FirstSeen); left > 0 {
				e.Remaining = left
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// loadLocked reads the state file once. A missing or corrupt file starts an
// empty state rather than failing: quarantine must never be the reason
// ChatCLI cannot start, and an empty state is the safe direction — every
// plugin is treated as newly seen and waits.
func (q *Quarantine) loadLocked() {
	if q.loaded {
		return
	}
	q.loaded = true
	q.state.Plugins = map[string]quarantineRecord{}

	data, err := os.ReadFile(filepath.Clean(q.statePath)) // #nosec G304 G703 -- state file inside the managed plugins directory
	if err != nil {
		return
	}
	var parsed quarantineState
	if err := json.Unmarshal(data, &parsed); err != nil {
		return
	}
	if parsed.Plugins != nil {
		q.state.Plugins = parsed.Plugins
	}
}

func (q *Quarantine) saveLocked() {
	data, err := json.MarshalIndent(q.state, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(q.statePath, data, 0o600)
}
