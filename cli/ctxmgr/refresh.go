/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Incremental re-indexing of contexts.
 *
 * A context remembers the paths it was built from and a stamp (size,
 * mtime, content hash) per file. RefreshContext re-scans those paths and
 * diffs against the stamps: an unchanged corpus leaves the context — and
 * therefore its retrieval caches and vectors — untouched; a changed one
 * is rebuilt with only the changed passages re-embedded (segment ids are
 * content hashes, so the vector index reuses everything that did not
 * move). The watcher (watch.go) drives the same path from fsnotify.
 */
package ctxmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// FileStamp is what RefreshContext compares to decide whether a file
// changed: size and mtime first (a stat), the content hash when those
// differ (a touch without an edit is not a change).
type FileStamp struct {
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime,omitempty"` // unix nanoseconds; 0 for synthetic entries
	Hash    string `json:"hash"`            // sha256 prefix of the content
}

// RefreshReport summarizes what a refresh found.
type RefreshReport struct {
	Changed   int
	Added     int
	Removed   int
	Unchanged int
	// Resealed marks a refresh that moved the context onto the current
	// segmenter and situating versions. It makes the refresh dirty on its
	// own: not one file has to have moved for the passages to be cut
	// differently and indexed by different text.
	Resealed bool
}

// Dirty reports whether anything changed.
func (r RefreshReport) Dirty() bool { return r.Changed+r.Added+r.Removed > 0 || r.Resealed }

// String renders the report compactly (for notices and logs).
func (r RefreshReport) String() string {
	base := fmt.Sprintf("changed=%d added=%d removed=%d unchanged=%d", r.Changed, r.Added, r.Removed, r.Unchanged)
	if r.Resealed {
		base += " resealed=true"
	}
	return base
}

// contentHash is the stamp hash of a file body.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}

// stampFiles builds the stamp map for scanned files. Real files are
// stat'ed for mtime; synthetic entries (docs-flatten chunks) carry size
// and hash only.
func stampFiles(files []utils.FileInfo) map[string]FileStamp {
	stamps := make(map[string]FileStamp, len(files))
	for _, f := range files {
		st := FileStamp{Size: f.Size, Hash: contentHash(f.Content)}
		if !strings.Contains(f.Path, "#") {
			if info, err := os.Stat(f.Path); err == nil {
				st.ModTime = info.ModTime().UnixNano()
			}
		}
		stamps[f.Path] = st
	}
	return stamps
}

// diffStamps compares a fresh scan against the recorded stamps.
func diffStamps(old map[string]FileStamp, fresh []utils.FileInfo, freshStamps map[string]FileStamp) RefreshReport {
	var rep RefreshReport
	seen := make(map[string]bool, len(fresh))
	for _, f := range fresh {
		seen[f.Path] = true
		prev, ok := old[f.Path]
		if !ok {
			rep.Added++
			continue
		}
		now := freshStamps[f.Path]
		if prev.Size == now.Size && prev.ModTime != 0 && prev.ModTime == now.ModTime {
			rep.Unchanged++
			continue
		}
		if prev.Hash == now.Hash {
			rep.Unchanged++
			continue
		}
		rep.Changed++
	}
	for p := range old {
		if !seen[p] {
			rep.Removed++
		}
	}
	return rep
}

// scanSources rescans a context's source paths in its mode: JSONL
// corpora through the knowledge parser, everything else through the
// directory scanner (the same intake CreateContext/UpdateContext use).
func (m *Manager) scanSources(ctx context.Context, paths []string, mode ProcessingMode) ([]utils.FileInfo, map[string]string, utils.DirectoryScanOptions, error) {
	var files []utils.FileInfo
	knowledgeMeta := map[string]string{}
	var scanPaths []string
	if mode == ModeKnowledge {
		for _, p := range paths {
			if !isJSONLPath(p) {
				scanPaths = append(scanPaths, p)
				continue
			}
			expanded, expandErr := utils.ExpandPath(p)
			if expandErr != nil {
				expanded = p
			}
			kfiles, kmeta, ingestErr := ingestKnowledgeJSONL(expanded, m.logger)
			if ingestErr != nil {
				return nil, nil, utils.DirectoryScanOptions{}, ingestErr
			}
			files = append(files, kfiles...)
			for k, v := range kmeta {
				knowledgeMeta[k] = v
			}
		}
	} else {
		scanPaths = paths
	}
	scanOpts := utils.DefaultDirectoryScanOptions(m.logger)
	if len(scanPaths) > 0 {
		scanned, opts, err := m.processor.ProcessPaths(ctx, scanPaths, mode)
		if err != nil {
			return nil, nil, utils.DirectoryScanOptions{}, fmt.Errorf("erro ao processar arquivos: %w", err)
		}
		files = append(files, scanned...)
		scanOpts = opts
	}
	return files, knowledgeMeta, scanOpts, nil
}

// expandSourcePaths records the paths a context was built from, expanded
// so a later refresh resolves them regardless of the working directory.
func expandSourcePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if expanded, err := utils.ExpandPath(p); err == nil {
			p = expanded
		}
		out = append(out, p)
	}
	return out
}

// RefreshContext re-scans a context's source paths and rebuilds it only
// when something changed. Contexts created before source paths were
// recorded cannot refresh: re-run /context update <name> <paths> once.
func (m *Manager) RefreshContext(ctx context.Context, name string) (*FileContext, RefreshReport, error) {
	return m.rescanContext(ctx, name, true)
}

// rescanContext re-scans a context's sources, with the reseal made an
// explicit choice of the caller rather than a side effect.
//
// Adopting a seal re-cuts the corpus and makes it re-earn its vectors.
// That is the right price for a command the user typed, and the wrong one
// for a file save: the watcher refreshes on its own schedule, and a
// corpus that silently re-embedded because someone touched a file would
// spend on a migration nobody asked for, at a moment nobody chose. The
// watcher refreshes content only; /context refresh is what moves a
// corpus onto the current seals.
func (m *Manager) rescanContext(ctx context.Context, name string, reseal bool) (*FileContext, RefreshReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var fc *FileContext
	for _, c := range m.contexts {
		if c.Name == name {
			fc = c
			break
		}
	}
	if fc == nil {
		return nil, RefreshReport{}, fmt.Errorf("contexto '%s' não encontrado", name)
	}
	if len(fc.SourcePaths) == 0 {
		// Contexts saved before source paths were recorded: the files they
		// hold still name where they came from. Adopt those paths (each
		// file, not their directories — the original selection is what
		// the context meant) and persist them so the next refresh and the
		// watcher find them.
		inferred := inferSourcePaths(fc)
		if len(inferred) == 0 {
			return fc, RefreshReport{}, ErrNoSourcePaths
		}
		fc.SourcePaths = inferred
		if err := m.Storage.SaveContext(fc); err == nil {
			m.logger.Info("context: source paths migrated from its files", zap.String("context", name), zap.Int("paths", len(inferred)))
		}
	}
	files, knowledgeMeta, scanOpts, err := m.scanSources(ctx, fc.SourcePaths, fc.Mode)
	if err != nil {
		return fc, RefreshReport{}, err
	}
	var totalSize int64
	for _, f := range files {
		totalSize += f.Size
	}
	if err := m.validator.ValidateTotalSize(totalSize); err != nil {
		return fc, RefreshReport{}, err
	}
	freshStamps := stampFiles(files)
	rep := diffStamps(fc.FileStamps, files, freshStamps)
	// A refresh is also how a context adopts the seals it was created
	// before. Creation tags every new context with the current segmenter
	// and situating versions and leaves older corpora on the shape they
	// already paid vectors for; the way off that shape is this command,
	// and until now it never wrote them — a context created before either
	// seal stayed on the old cut and the bare indexed text no matter how
	// often it was refreshed.
	// Checked, not written: the seal only becomes true once the context it
	// belongs to is on disk. Writing it here and failing to save below
	// would leave memory claiming a shape the stored corpus does not have
	// — the retrieval cache would invalidate and re-embed, and the next
	// process would read the old seal off disk and re-embed again.
	rep.Resealed = reseal && indexSealsDiffer(fc)
	if !rep.Dirty() && len(fc.FileStamps) > 0 {
		// Nothing moved: keep UpdatedAt so retrieval caches and the
		// vector index stay valid as they are.
		return fc, rep, nil
	}

	fc.Files = files
	fc.FileStamps = freshStamps
	fc.TotalSize = totalSize
	fc.FileCount = len(files)
	fc.ScanOptions = scanOpts
	fc.ScanOptionsMetadata = ScanOptionsMetadata{
		MaxTotalSize:      scanOpts.MaxTotalSize,
		MaxFilesToProcess: scanOpts.MaxFilesToProcess,
		Extensions:        scanOpts.Extensions,
		ExcludeDirs:       scanOpts.ExcludeDirs,
		ExcludePatterns:   scanOpts.ExcludePatterns,
		IncludeHidden:     scanOpts.IncludeHidden,
	}
	if len(knowledgeMeta) > 0 {
		if fc.Metadata == nil {
			fc.Metadata = map[string]string{}
		}
		for k, v := range knowledgeMeta {
			fc.Metadata[k] = v
		}
	}
	if fc.Mode == ModeChunked {
		chunks, err := NewChunker(m.logger).DivideIntoChunks(files, ChunkSmart)
		if err != nil {
			return fc, rep, fmt.Errorf("erro ao dividir em chunks: %w", err)
		}
		fc.Chunks = chunks
		fc.IsChunked = true
		fc.ChunkStrategy = string(ChunkSmart)
	}
	fc.UpdatedAt = time.Now()
	sealedNow := false
	if rep.Resealed {
		sealedNow = applyIndexSeals(fc)
	}
	if err := m.Storage.SaveContext(fc); err != nil {
		if sealedNow {
			// The corpus on disk never gained the seal, so neither does
			// the one in memory: both stay on the shape whose vectors are
			// already paid for, and the next refresh tries again.
			clearIndexSeals(fc)
			rep.Resealed = false
		}
		return fc, rep, fmt.Errorf("erro ao salvar contexto: %w", err)
	}
	m.logger.Info("context refreshed", zap.String("context", name), zap.String("report", rep.String()))
	// Changed passages get new ids; embed them now rather than in the next turn.
	if fc.Mode == ModeKnowledge {
		m.warmContextCtx(context.WithoutCancel(ctx), fc.ID)
	}
	return fc, rep, nil
}

// inferSourcePaths derives source paths for a legacy context from its
// files: absolute paths that still exist (synthetic "#chunk" entries and
// relative paths are skipped). Empty when nothing usable remains.
func inferSourcePaths(fc *FileContext) []string {
	if fc == nil {
		return nil
	}
	seen := make(map[string]bool, len(fc.Files))
	out := make([]string, 0, len(fc.Files))
	for _, f := range fc.Files {
		p := f.Path
		if i := strings.Index(p, "#"); i >= 0 {
			p = p[:i]
		}
		if p == "" || !filepath.IsAbs(p) || seen[p] {
			continue
		}
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// ErrNoSourcePaths marks a context that predates source-path recording.
var ErrNoSourcePaths = fmt.Errorf("context has no recorded source paths")

// SourcePathsOf returns the recorded source paths of a context by name,
// inferring (and persisting) them for a legacy context.
func (m *Manager) SourcePathsOf(name string) ([]string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.contexts {
		if c.Name != name {
			continue
		}
		if len(c.SourcePaths) == 0 {
			if inferred := inferSourcePaths(c); len(inferred) > 0 {
				c.SourcePaths = inferred
				_ = m.Storage.SaveContext(c)
			}
		}
		return append([]string(nil), c.SourcePaths...), true
	}
	return nil, false
}

// RegisterLegacyForTest registers an in-memory context without source
// paths, the shape of contexts saved before refresh existed. Test-only
// helper (no storage write).
func (m *Manager) RegisterLegacyForTest(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.contexts["legacy-"+name] = &FileContext{ID: "legacy-" + name, Name: name, Mode: ModeFull}
}
