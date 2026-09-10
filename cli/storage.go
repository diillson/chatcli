/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Storage curation. The boot retention pass (retention.go) expires stores by
 * age and stays invisible; this file is the on-demand, explainable side of
 * the same policies: what every store holds, what rule governs it, what
 * would go right now, and — only when asked — removing it. Two rules exist
 * only here because they are heuristics a boot pass must not apply on its
 * own: cost snapshots created in a burst (a test suite, never a person) and
 * the size/TTL sweep of the CCR archive and the hub, which need their own
 * stores opened.
 */
package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/taskgraph"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

// Store names, as typed on /storage prune <store>.
const (
	StoreCosts       = "costs"
	StoreCheckpoints = "checkpoints"
	StoreSessions    = "sessions"
	StoreTranscripts = "transcripts"
	StoreParked      = "parked"
	StorePending     = "memory-pending"
	StoreTaskGraph   = "taskgraph"
	StoreCCR         = "ccr"
	StoreHub         = "hub"
	StoreMemory      = "memory"
	StoreSkills      = "skills"
	StorePlugins     = "plugins"
	StoreContexts    = "contexts"
	StoreAgents      = "agents"
	StoreCommands    = "commands"
	StoreReflexion   = "reflexion"
	StoreScheduler   = "scheduler"
	StoreTokenizers  = "tokenizers"
	StoreMCP         = "mcp"
	StoreCache       = "cache"
	StoreLogs        = "logs"
)

// Removal reasons, keyed for i18n (storage.reason.<reason>).
const (
	ReasonTTL    = "ttl"    // untouched for longer than the policy window
	ReasonOrphan = "orphan" // the workspace it belonged to no longer exists
	ReasonBurst  = "burst"  // created in a burst no person produces
	ReasonStray  = "stray"  // a temp file a crash left behind
)

// Policy kinds, keyed for i18n (storage.policy.<kind>).
const (
	PolicyTTL      = "ttl"       // session TTL on last write
	PolicyOrphan   = "orphan"    // TTL + orphaned workspace
	PolicyBurst    = "burst"     // TTL + burst heuristic on demand
	PolicyRuns     = "runs"      // task graph retention window
	PolicyCap      = "cap"       // TTL + size cap, swept by its own store
	PolicyIdle     = "idle"      // idle conversations by hub TTL
	PolicyMachine  = "machine"   // machine sessions only, named ones never
	PolicyReadOnly = "protected" // never pruned: user content or live state
)

// StorageStore is one store's line in the inventory.
type StorageStore struct {
	Name          string         `json:"name"`
	Dir           string         `json:"dir"`
	Files         int            `json:"files"`
	Bytes         int64          `json:"bytes"`
	Policy        string         `json:"policy"`
	Protected     bool           `json:"protected"`
	OnApplyOnly   bool           `json:"on_apply_only,omitempty"` // count known only when applied
	Prunable      int            `json:"prunable"`
	PrunableBytes int64          `json:"prunable_bytes"`
	Reasons       map[string]int `json:"reasons,omitempty"`
	Removed       int            `json:"removed,omitempty"`
	BytesFreed    int64          `json:"bytes_freed,omitempty"`
}

// StorageOptions parameterizes one inventory or prune run.
type StorageOptions struct {
	Root   string        // state root; "" = ~/.chatcli
	TTL    time.Duration // machine-session TTL; <= 0 keeps everything age-based
	Now    time.Time     // zero = time.Now()
	Only   string        // restrict to one store name; "" = every store
	Apply  bool          // remove instead of reporting
	Logger *zap.Logger   // nil = nop
	// SkipTaskGraphRun is the session's active run id, never removed.
	SkipTaskGraphRun string
	// Burst enables the cost-snapshot burst heuristic (on-demand only).
	Burst bool
}

// StorageResult is the inventory plus, after Apply, what was removed.
type StorageResult struct {
	Root       string         `json:"root"`
	Stores     []StorageStore `json:"stores"`
	Bytes      int64          `json:"bytes"`
	Prunable   int            `json:"prunable"`
	Removed    int            `json:"removed"`
	BytesFreed int64          `json:"bytes_freed"`
	Applied    bool           `json:"applied"`
}

// candidate is one item a rule would remove.
type candidate struct {
	path   string
	bytes  int64
	isDir  bool
	reason string
}

// StorageStoreNames lists every store, in display order.
func StorageStoreNames() []string {
	return []string{
		StoreCosts, StoreCheckpoints, StoreSessions, StoreTranscripts, StoreParked,
		StorePending, StoreTaskGraph, StoreCCR, StoreHub,
		StoreMemory, StoreSkills, StorePlugins, StoreContexts, StoreAgents, StoreCommands,
		StoreReflexion, StoreScheduler, StoreTokenizers, StoreMCP, StoreCache, StoreLogs,
	}
}

// IsStorageStore reports whether name is a known store.
func IsStorageStore(name string) bool {
	for _, n := range StorageStoreNames() {
		if n == name {
			return true
		}
	}
	return false
}

// RunStorage builds the inventory of every store under the root and, when
// opts.Apply is set, removes what the policies allow. Every step is
// best-effort: an unreadable store is reported empty, a failed removal is
// simply not counted.
func RunStorage(ctx context.Context, opts StorageOptions) (StorageResult, error) {
	if opts.Root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return StorageResult{}, err
		}
		opts.Root = filepath.Join(home, ".chatcli")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.Only != "" && !IsStorageStore(opts.Only) {
		return StorageResult{}, errUnknownStore(opts.Only)
	}

	res := StorageResult{Root: opts.Root, Applied: opts.Apply}
	for _, name := range StorageStoreNames() {
		if opts.Only != "" && opts.Only != name {
			continue
		}
		st := inventoryStore(opts, name)
		cands := storeCandidates(opts, name)
		st.Prunable = len(cands)
		st.Reasons = map[string]int{}
		for _, c := range cands {
			st.PrunableBytes += c.bytes
			st.Reasons[c.reason]++
		}
		if len(st.Reasons) == 0 {
			st.Reasons = nil
		}
		if opts.Apply {
			for _, c := range cands {
				if removeCandidate(c) {
					st.Removed++
					st.BytesFreed += c.bytes
				}
			}
			removed, freed := applyOnDemand(ctx, opts, name)
			st.Removed += removed
			st.BytesFreed += freed
		}
		res.Stores = append(res.Stores, st)
		res.Bytes += st.Bytes
		res.Prunable += st.Prunable
		res.Removed += st.Removed
		res.BytesFreed += st.BytesFreed
	}
	if opts.Apply && res.Removed > 0 {
		opts.Logger.Info("storage prune", zap.Int("removed", res.Removed), zap.Int64("bytes_freed", res.BytesFreed), zap.String("only", opts.Only))
	}
	return res, nil
}

type unknownStoreError struct{ name string }

func (e unknownStoreError) Error() string { return "unknown store: " + e.name }
func errUnknownStore(name string) error   { return unknownStoreError{name: name} }

// storeDir is the on-disk location of a store under root.
func storeDir(root, name string) string {
	switch name {
	case StorePending:
		return filepath.Join(root, "memory", "pending")
	case StoreHub:
		return filepath.Join(root, "hub.db")
	case StoreLogs:
		return root // app.log and gateway.log sit at the root
	default:
		return filepath.Join(root, name)
	}
}

// inventoryStore measures a store and names its policy.
func inventoryStore(opts StorageOptions, name string) StorageStore {
	st := StorageStore{Name: name, Dir: storeDir(opts.Root, name)}
	switch name {
	case StoreCosts:
		st.Policy = PolicyBurst
	case StoreCheckpoints:
		st.Policy = PolicyOrphan
	case StoreSessions:
		st.Policy = PolicyMachine
	case StoreTranscripts, StoreParked, StorePending:
		st.Policy = PolicyTTL
	case StoreTaskGraph:
		st.Policy = PolicyRuns
	case StoreCCR:
		st.Policy, st.OnApplyOnly = PolicyCap, true
	case StoreHub:
		st.Policy, st.OnApplyOnly = PolicyIdle, true
	default:
		st.Policy, st.Protected = PolicyReadOnly, true
	}
	switch name {
	case StoreLogs:
		for _, f := range []string{"app.log", "gateway.log"} {
			if info, err := os.Stat(filepath.Join(opts.Root, f)); err == nil && !info.IsDir() {
				st.Files++
				st.Bytes += info.Size()
			}
		}
		// Rotated, compressed logs sit beside them.
		entries, _ := os.ReadDir(opts.Root)
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "app-") && strings.HasSuffix(e.Name(), ".log.gz") {
				if info, err := e.Info(); err == nil {
					st.Files++
					st.Bytes += info.Size()
				}
			}
		}
	case StoreHub:
		if info, err := os.Stat(st.Dir); err == nil && !info.IsDir() {
			st.Files, st.Bytes = 1, info.Size()
		}
	case StoreMemory:
		// Everything under memory/ except the pending queue, which is its
		// own store.
		st.Files, st.Bytes = dirUsage(st.Dir, filepath.Join(st.Dir, "pending"))
	default:
		st.Files, st.Bytes = dirUsage(st.Dir, "")
	}
	return st
}

// dirUsage counts regular files and bytes under dir, skipping the subtree
// at skip when set. A missing dir is 0/0.
func dirUsage(dir, skip string) (int, int64) {
	var files int
	var bytes int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if skip != "" && path == skip && d.IsDir() {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}

// storeCandidates lists what the rules would remove from one store now.
func storeCandidates(opts StorageOptions, name string) []candidate {
	dir := storeDir(opts.Root, name)
	var cutoff time.Time
	if opts.TTL > 0 {
		cutoff = opts.Now.Add(-opts.TTL)
	}
	switch name {
	case StoreCosts:
		return costSnapshotCandidates(dir, cutoff, opts.Now, opts.Burst)
	case StoreCheckpoints:
		return checkpointCandidates(dir, cutoff)
	case StoreSessions:
		return fileCandidates(dir, cutoff, ".json", func(n string) bool {
			return isMachineSession(strings.TrimSuffix(n, ".json"))
		})
	case StoreTranscripts:
		return fileCandidates(dir, cutoff, ".jsonl", nil)
	case StoreParked:
		return fileCandidates(dir, cutoff, "", nil)
	case StorePending:
		return fileCandidates(dir, cutoff, ".json", nil)
	case StoreTaskGraph:
		return taskGraphCandidates(dir, opts.Now.Add(-taskgraph.DefaultRetention), opts.SkipTaskGraphRun)
	}
	return nil
}

// fileCandidates is the age rule shared by the flat stores: regular files
// with the extension (any when ""), accepted by keep when given, last
// written before cutoff. A zero cutoff (TTL disabled) selects nothing.
func fileCandidates(dir string, cutoff time.Time, ext string, accept func(string) bool) []candidate {
	if cutoff.IsZero() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]candidate, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || (ext != "" && filepath.Ext(e.Name()) != ext) {
			continue
		}
		if accept != nil && !accept(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		out = append(out, candidate{path: filepath.Join(dir, e.Name()), bytes: info.Size(), reason: ReasonTTL})
	}
	return out
}

// costSessionFile matches a per-session cost snapshot and captures its
// start minute; daily-spend.json and anything else in the dir is never a
// candidate.
var costSessionFile = regexp.MustCompile(`^(\d{8}-\d{4})\d{2}-\d+\.json$`)

// burstMinSessions is how many sessions must start within one minute for
// the burst rule to consider them machine-made.
const burstMinSessions = 4

// burstMaxRequests is the most requests a burst session may have recorded
// and still count as a burst: a person's session grows past it.
const burstMaxRequests = 2

// costSnapshotCandidates applies the cost store's rules: snapshots older
// than the TTL, stray .tmp files older than an hour, and — when asked —
// snapshots that started in a burst of at least burstMinSessions within
// the same minute with at most burstMaxRequests requests each.
func costSnapshotCandidates(dir string, cutoff, now time.Time, burst bool) []candidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	tmpCutoff := now.Add(-time.Hour)
	out := make([]candidate, 0, len(entries))
	seen := map[string]bool{}
	byMinute := map[string][]os.DirEntry{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, name)
		if strings.HasSuffix(name, ".tmp") {
			if info.ModTime().Before(tmpCutoff) {
				out = append(out, candidate{path: path, bytes: info.Size(), reason: ReasonStray})
			}
			continue
		}
		m := costSessionFile.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
			out = append(out, candidate{path: path, bytes: info.Size(), reason: ReasonTTL})
			seen[path] = true
			continue
		}
		byMinute[m[1]] = append(byMinute[m[1]], e)
	}
	if !burst {
		return out
	}
	for _, group := range byMinute {
		if len(group) < burstMinSessions {
			continue
		}
		for _, e := range group {
			path := filepath.Join(dir, e.Name())
			if seen[path] || costSnapshotRequests(path) > burstMaxRequests {
				continue
			}
			if info, err := e.Info(); err == nil {
				out = append(out, candidate{path: path, bytes: info.Size(), reason: ReasonBurst})
			}
		}
	}
	return out
}

// costSnapshotRequests sums the requests a snapshot recorded; unreadable
// snapshots count as many, so the burst rule never removes what it cannot
// read.
func costSnapshotRequests(path string) int {
	data, err := os.ReadFile(path) // #nosec G304 -- path enumerated from the store dir
	if err != nil {
		return int(^uint(0) >> 1)
	}
	var doc struct {
		ModelUsage map[string]struct {
			Requests int `json:"requests"`
		} `json:"model_usage"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return int(^uint(0) >> 1)
	}
	n := 0
	for _, rec := range doc.ModelUsage {
		n += rec.Requests
	}
	return n
}

// checkpointWorktree matches the core.worktree line of a shadow repo config.
var checkpointWorktree = regexp.MustCompile(`(?m)^\s*worktree\s*=\s*(.+?)\s*$`)

// checkpointCandidates applies the checkpoint store's rules to each shadow
// git repository: orphaned when its worktree is a temporary directory or a
// directory that no longer exists, expired when nothing in it was written
// since the cutoff. A directory without a git config is not a checkpoint
// and is left alone.
func checkpointCandidates(dir string, cutoff time.Time) []candidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]candidate, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repo := filepath.Join(dir, e.Name())
		cfg, err := os.ReadFile(filepath.Join(repo, "config")) // #nosec G304 -- path enumerated from the store dir
		if err != nil {
			continue
		}
		worktree := ""
		if m := checkpointWorktree.FindSubmatch(cfg); m != nil {
			worktree = string(m[1])
		}
		_, size := dirUsage(repo, "")
		switch {
		case worktree == "" || isTempPath(worktree) || !isDir(worktree):
			out = append(out, candidate{path: repo, bytes: size, isDir: true, reason: ReasonOrphan})
		case !cutoff.IsZero() && newestWrite(repo).Before(cutoff):
			out = append(out, candidate{path: repo, bytes: size, isDir: true, reason: ReasonTTL})
		}
	}
	return out
}

// tempRoots lists the locations whose subtrees are throwaway workspaces —
// what test suites and one-shot tools create and discard. A variable so a
// test can point it at a directory of its own, since on macOS every
// t.TempDir() lives under /var/folders itself.
var tempRoots = func() []string {
	return []string{os.TempDir(), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}
}

// isTempPath reports whether p lives under one of tempRoots.
func isTempPath(p string) bool {
	clean := filepath.Clean(p)
	for _, root := range tempRoots() {
		root = filepath.Clean(root)
		if root == "" || root == string(filepath.Separator) {
			continue
		}
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isDir reports whether p is an existing directory. The path comes from a
// shadow repo's own config (its recorded worktree) and is only stat'ed —
// nothing is opened, read or written through it.
func isDir(p string) bool {
	info, err := os.Stat(filepath.Clean(p)) // #nosec G703 -- existence check only, see above
	return err == nil && info.IsDir()
}

// newestWrite is the latest modification time among the top-level entries
// of a shadow repo: HEAD, index and COMMIT_EDITMSG move on every
// checkpoint, so this is when the workspace was last snapshotted.
func newestWrite(repo string) time.Time {
	var newest time.Time
	entries, err := os.ReadDir(repo)
	if err != nil {
		return newest
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// taskGraphCandidates mirrors taskgraph.PruneRuns: run directories whose
// state was last written before cutoff, the active run excepted.
func taskGraphCandidates(dir string, cutoff time.Time, skipRunID string) []candidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]candidate, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "tg-") || e.Name() == skipRunID {
			continue
		}
		run := filepath.Join(dir, e.Name())
		info, err := os.Stat(filepath.Join(run, "state.json"))
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_, size := dirUsage(run, "")
		out = append(out, candidate{path: run, bytes: size, isDir: true, reason: ReasonTTL})
	}
	return out
}

func removeCandidate(c candidate) bool {
	if c.isDir {
		return os.RemoveAll(c.path) == nil
	}
	return os.Remove(c.path) == nil
}

// applyOnDemand runs the sweeps that live inside their own stores and only
// report what they removed once run: the CCR archive (TTL + size cap) and
// the hub's idle conversations.
func applyOnDemand(ctx context.Context, opts StorageOptions, name string) (removed int, freed int64) {
	switch name {
	case StoreCCR:
		if !isDir(storeDir(opts.Root, StoreCCR)) {
			return 0, 0
		}
		layer := compress.NewLayerFromEnv(opts.Root)
		r := layer.Prune()
		return r.Removed, r.BytesFreed
	case StoreHub:
		dbPath := storeDir(opts.Root, StoreHub)
		info, err := os.Stat(dbPath)
		if err != nil || info.IsDir() {
			return 0, 0
		}
		store, err := hub.OpenSQLiteStore(ctx, dbPath, opts.Logger)
		if err != nil {
			opts.Logger.Debug("storage: hub open failed", zap.Error(err))
			return 0, 0
		}
		defer func() { _ = store.Close() }()
		n, err := store.PurgeIdle(ctx, resolveHubTTL(ctx, store))
		if err != nil {
			opts.Logger.Debug("storage: hub purge failed", zap.Error(err))
			return 0, 0
		}
		if after, err := os.Stat(dbPath); err == nil && info.Size() > after.Size() {
			freed = info.Size() - after.Size()
		}
		return n, freed
	}
	return 0, 0
}

// sortedReasons renders a store's reasons deterministically.
func sortedReasons(reasons map[string]int) []string {
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
