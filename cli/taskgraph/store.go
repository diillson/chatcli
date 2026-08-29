/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Run persistence: each task-graph run owns a directory under
 * ~/.chatcli/taskgraph/<runID>/ holding state.json (the full Graph, written
 * atomically on every transition — the engine is the sole writer) and
 * events.ndjson (append-only audit trail; the dashboard's feed). A state
 * file that fails to parse is quarantined aside, never left in place to be
 * overwritten by the next save.
 */
package taskgraph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	stateFileName  = "state.json"
	eventsFileName = "events.ndjson"
	dirPerm        = 0o750
	filePerm       = 0o600
)

// DefaultBaseDir is where runs live unless the caller overrides it.
func DefaultBaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".chatcli", "taskgraph"), nil
}

// RunStore is the on-disk home of one run.
type RunStore struct {
	dir   string
	runID string
}

// RunID returns the store's run identifier.
func (s *RunStore) RunID() string { return s.runID }

// Dir returns the run directory path.
func (s *RunStore) Dir() string { return s.dir }

// EventsPath returns the run's append-only event log path (read by the
// dashboard's incremental feed).
func (s *RunStore) EventsPath() string { return filepath.Join(s.dir, eventsFileName) }

// CreateRun allocates a fresh run directory, stamps the graph with the run id
// and persists the initial state.
func CreateRun(baseDir string, g *Graph) (*RunStore, error) {
	if err := os.MkdirAll(baseDir, dirPerm); err != nil {
		return nil, fmt.Errorf("create taskgraph dir: %w", err)
	}
	base := time.Now().Format("20060102-150405")
	runID := "tg-" + base
	dir := filepath.Join(baseDir, runID)
	for n := 2; ; n++ {
		if err := os.Mkdir(dir, dirPerm); err == nil {
			break
		} else if !os.IsExist(err) {
			return nil, fmt.Errorf("create run dir: %w", err)
		}
		runID = fmt.Sprintf("tg-%s-%d", base, n)
		dir = filepath.Join(baseDir, runID)
	}
	g.RunID = runID
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	s := &RunStore{dir: dir, runID: runID}
	if err := s.SaveState(g); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenRun binds a store to an existing run directory.
func OpenRun(baseDir, runID string) (*RunStore, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || runID != filepath.Base(runID) {
		return nil, fmt.Errorf("invalid run id %q", runID)
	}
	dir := filepath.Join(baseDir, runID)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("no task graph run %q", runID)
	}
	return &RunStore{dir: dir, runID: runID}, nil
}

// SaveState persists the full graph atomically.
func (s *RunStore) SaveState(g *Graph) error {
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return atomicWriteFile(filepath.Join(s.dir, stateFileName), data, filePerm)
}

// LoadState reads the persisted graph back. A corrupt file is quarantined
// (renamed aside) and reported, so its bytes stay recoverable.
func (s *RunStore) LoadState() (*Graph, error) {
	path := filepath.Join(s.dir, stateFileName)
	data, err := os.ReadFile(path) // #nosec G304 -- path is built from the validated run id under the taskgraph base dir, not raw user input
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var g Graph
	if err := json.Unmarshal(data, &g); err != nil {
		if qPath, qErr := quarantineCorrupt(path); qErr == nil {
			return nil, fmt.Errorf("state file corrupt (quarantined as %s): %w", filepath.Base(qPath), err)
		}
		return nil, fmt.Errorf("state file corrupt: %w", err)
	}
	return &g, nil
}

// AppendEvent appends one event line to events.ndjson (best-effort trail:
// an append failure must never abort the run, so callers log and continue).
func (s *RunStore) AppendEvent(ev Event) error {
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, eventsFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(line, '\n'))
	return err
}

// RunSummary is one row of ListRuns.
type RunSummary struct {
	RunID     string
	Name      string
	Status    Status
	Tasks     int
	Done      int
	Failed    int
	CreatedAt time.Time
}

// ListRuns enumerates runs under baseDir, newest first. Unreadable run dirs
// are skipped — listing must degrade, not fail.
func ListRuns(baseDir string) ([]RunSummary, error) {
	entries, err := os.ReadDir(baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]RunSummary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "tg-") {
			continue
		}
		s := &RunStore{dir: filepath.Join(baseDir, e.Name()), runID: e.Name()}
		g, err := s.LoadState()
		if err != nil {
			continue
		}
		counts := g.CountByStatus()
		out = append(out, RunSummary{
			RunID:     g.RunID,
			Name:      g.Name,
			Status:    g.Status,
			Tasks:     len(g.Tasks),
			Done:      counts[StatusDone],
			Failed:    counts[StatusFailed] + counts[StatusBlocked],
			CreatedAt: g.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// DefaultRetention is how long runs are kept before the automatic prune
// removes them (age = last state write). Matches the house pattern of
// bounded on-disk stores (cost snapshots, hub purge).
const DefaultRetention = 30 * 24 * time.Hour

// PruneRuns removes run directories whose state was last written before
// olderThan ago. skipRunID (the session's active run) is never removed.
// olderThan <= 0 means "prune every run except skipRunID". Returns how many
// runs were removed; unreadable entries are skipped, not fatal.
func PruneRuns(baseDir string, olderThan time.Duration, skipRunID string) (int, error) {
	entries, err := os.ReadDir(baseDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "tg-") || e.Name() == skipRunID {
			continue
		}
		if olderThan > 0 {
			info, statErr := os.Stat(filepath.Join(baseDir, e.Name(), stateFileName))
			if statErr != nil || info.ModTime().After(cutoff) {
				continue
			}
		}
		if err := os.RemoveAll(filepath.Join(baseDir, e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// atomicWriteFile writes via a same-directory temp file and rename, so a
// crash mid-write can never leave a torn state.json under the real name.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// quarantineCorrupt renames an unparseable state file aside so the original
// bytes remain recoverable while the caller reports the corruption.
func quarantineCorrupt(path string) (string, error) {
	dst := path + ".corrupt"
	if _, err := os.Stat(dst); err == nil {
		dst = fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	}
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}
