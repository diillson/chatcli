/*
 * ChatCLI - Per-conversation voice reply preferences.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Users control voice replies in the conversation itself ("answer me in
 * audio" / "stop sending audio"): the model calls the @voice tool, which
 * stores a per-session preference here. The preference outranks the global
 * CHATCLI_GATEWAY_VOICE_REPLY mode and persists as JSON under ~/.chatcli, so
 * a daemon restart keeps every conversation's choice.
 *
 * Concurrency model: the gateway serializes agent runs under a mutex, so the
 * "active session" stamped around each run cannot interleave; the map itself
 * is still lock-protected because the Runner reads preferences from delivery
 * goroutines.
 */
package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Per-session voice preference values. Empty (unset) defers to the global mode.
const (
	VoicePrefAlways = "always" // user asked for audio replies in this chat
	VoicePrefNever  = "never"  // user asked to stop audio replies
)

// VoicePrefs stores per-session voice reply preferences with JSON persistence.
type VoicePrefs struct {
	mu     sync.Mutex
	path   string
	prefs  map[string]string // session key → VoicePrefAlways | VoicePrefNever
	active string            // session currently being served by the agent loop
}

var (
	sharedVoicePrefs     *VoicePrefs
	sharedVoicePrefsOnce sync.Once
)

// SharedVoicePrefs returns the process-wide store backed by
// ~/.chatcli/gateway_voice_prefs.json. The Runner, the gateway agent loop and
// the @voice tool all share it.
func SharedVoicePrefs() *VoicePrefs {
	sharedVoicePrefsOnce.Do(func() {
		path := ""
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".chatcli", "gateway_voice_prefs.json")
		}
		sharedVoicePrefs = NewVoicePrefs(path)
	})
	return sharedVoicePrefs
}

// NewVoicePrefs builds a store persisted at path. An empty path keeps the
// store memory-only (used by tests); a missing or corrupt file starts empty —
// losing a preference degrades to the global default, never to a crash.
func NewVoicePrefs(path string) *VoicePrefs {
	v := &VoicePrefs{path: path, prefs: map[string]string{}}
	v.load()
	return v
}

// Get returns the stored preference for session, or "" when unset.
func (v *VoicePrefs) Get(session string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.prefs[session]
}

// Set stores mode for session ("" deletes, returning the session to the
// global default) and persists the file atomically.
func (v *VoicePrefs) Set(session, mode string) error {
	if session == "" {
		return fmt.Errorf("voice prefs: empty session")
	}
	switch mode {
	case VoicePrefAlways, VoicePrefNever, "":
	default:
		return fmt.Errorf("voice prefs: invalid mode %q", mode)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if mode == "" {
		delete(v.prefs, session)
	} else {
		v.prefs[session] = mode
	}
	return v.persistLocked()
}

// SetActiveSession stamps the session the agent loop is serving right now.
// Gateway runs are serialized, so there is exactly one at a time.
func (v *VoicePrefs) SetActiveSession(session string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.active = session
}

// ActiveSession returns the session currently being served, or "" outside a
// gateway run (the @voice tool uses this to refuse REPL invocations).
func (v *VoicePrefs) ActiveSession() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.active
}

// load reads the JSON file; absent or unreadable files leave the store empty.
func (v *VoicePrefs) load() {
	if v.path == "" {
		return
	}
	data, err := os.ReadFile(v.path) // #nosec G304 -- our own state file under ~/.chatcli
	if err != nil {
		return
	}
	prefs := map[string]string{}
	if err := json.Unmarshal(data, &prefs); err != nil {
		return
	}
	v.prefs = prefs
}

// persistLocked writes the file atomically. Callers hold v.mu.
func (v *VoicePrefs) persistLocked() error {
	if v.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(v.prefs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.path), 0o750); err != nil {
		return err
	}
	tmp := v.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, v.path)
}
