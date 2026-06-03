/*
 * ChatCLI - Skill usage analytics.
 * pkg/persona/usage/usage.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Tracks which skills actually get activated, so the agent (and the user) can
 * see what's earning its keep and what's dead weight — the data side of the
 * "skills that evolve" loop. Counts and last-used timestamps are persisted to
 * ~/.chatcli/skill-usage.json. Recording is best-effort and must never break a
 * turn: every error degrades to a no-op.
 */
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is the recorded usage for one skill.
type Entry struct {
	Count    int    `json:"count"`
	LastUsed string `json:"last_used"` // RFC3339 UTC
}

// Store persists skill activation counts. Safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	now  func() time.Time // injectable for tests
}

// New returns a store backed by path.
func New(path string) *Store {
	return &Store{path: path, now: time.Now}
}

// Default returns the store at ~/.chatcli/skill-usage.json, or nil if the home
// directory can't be resolved (recording then becomes a no-op).
func Default() *Store {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return New(filepath.Join(home, ".chatcli", "skill-usage.json"))
}

// Record increments the activation count for each name and stamps LastUsed.
// Best-effort: any I/O error is swallowed so a turn is never broken.
func (s *Store) Record(names ...string) {
	if s == nil || len(names) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data := s.loadLocked()
	stamp := s.now().UTC().Format(time.RFC3339)
	for _, n := range names {
		if n == "" {
			continue
		}
		e := data[n]
		e.Count++
		e.LastUsed = stamp
		data[n] = e
	}
	s.saveLocked(data)
}

// Stats returns a copy of all recorded usage.
func (s *Store) Stats() map[string]Entry {
	if s == nil {
		return map[string]Entry{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Ranked is one entry paired with its skill name, for ordered display.
type Ranked struct {
	Name string
	Entry
}

// Ranking returns usage sorted by count descending (ties broken by name).
func (s *Store) Ranking() []Ranked {
	stats := s.Stats()
	out := make([]Ranked, 0, len(stats))
	for n, e := range stats {
		out = append(out, Ranked{Name: n, Entry: e})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *Store) loadLocked() map[string]Entry {
	out := map[string]Entry{}
	raw, err := os.ReadFile(s.path) // #nosec G304 -- fixed path under user home
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]Entry{}
	}
	return out
}

func (s *Store) saveLocked(data map[string]Entry) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, raw, 0o600)
}
