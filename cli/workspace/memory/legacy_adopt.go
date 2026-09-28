/*
 * ChatCLI - Long-term memory: one-time adoption of a legacy memory directory.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The Helm chart used to mount memory at the root of the sessions PVC, the
 * same directory as the session files. It now mounts memory from its own
 * directory of that PVC and points LegacyDirEnv at the old shared location,
 * so the first start on the new layout carries the user's memory over
 * instead of starting empty.
 *
 * The adoption only ever COPIES: the legacy directory is read, never
 * written or pruned, and a file already present in the memory directory is
 * never overwritten. Each file lands atomically (temp file + rename).
 */
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/utils"
)

// LegacyDirEnv names the directory an older layout kept memory in. When
// set, the memory directory adopts the memory files found there on its
// first start (see AdoptLegacyDir).
const LegacyDirEnv = "CHATCLI_MEMORY_LEGACY_DIR"

const (
	// legacyAdoptedMarker records a completed adoption: it never runs again.
	legacyAdoptedMarker = ".migrated-from-legacy"
	// legacyAdoptingMarker records an adoption that has to finish on a
	// later start (a sealed file could not be opened yet).
	legacyAdoptingMarker = ".migrating-from-legacy"

	legacyDirPerm  = 0o750
	legacyFilePerm = 0o600
)

// legacyStoreFiles are the files the memory layers persist at the top of
// the memory directory (facts.go, episodes.go, profile.go, topics.go,
// projects.go, patterns.go, graph_cache.go, vector_store.go, compactor.go,
// migration.go). Keep in sync when a store adds a file.
var legacyStoreFiles = []string{
	"MEMORY.md",
	"memory_index.json",
	"memory_tombstones.json",
	"episodes.json",
	"user_profile.json",
	"topics.json",
	"projects.json",
	"usage_stats.json",
	"graph.json",
	"vector_index.json",
	"memory_archive.json",
	compactorStateFile,
}

var (
	// MEMORY.md backups written by the MEMORY.md migration.
	legacyMemoryBackup = regexp.MustCompile(`^MEMORY\.md\.bak(\.[0-9]+)?$`)
	// Quarantined store files (persist.go quarantineCorrupt).
	legacyQuarantine = regexp.MustCompile(`^(.+)\.corrupt(-[0-9]+)?$`)
	// Daily notes live in YYYYMM directories (daily.go).
	legacyDailyDir = regexp.MustCompile(`^[0-9]{6}$`)
)

// legacyTreeDirs are memory subdirectories copied with their files: weekly
// and monthly rollups (rollup.go) and the pending extraction queue
// (cli/memory_pending.go, <memory>/pending).
var legacyTreeDirs = []string{"weekly", "monthly", "pending"}

// LegacyAdoptReport says what AdoptLegacyDir did.
type LegacyAdoptReport struct {
	// Ran is false when there was nothing to do: no legacy directory, the
	// adoption already completed, or the memory directory was in use.
	Ran bool
	// Copied lists the paths copied, relative to the memory directory.
	Copied []string
	// Skipped lists the legacy paths left behind, with the reason.
	Skipped []string
	// Pending is true when a sealed file could not be opened (encryption
	// key missing or wrong): the adoption resumes on the next start.
	Pending bool
}

// AdoptLegacyDir copies memory's own files from legacyDir into memoryDir
// once. It runs when memoryDir holds none of memory's files yet (or an
// earlier adoption left it pending) and no completion marker exists; it
// then writes the marker, so a restart never copies again.
//
// Only memory's known files and directories are considered, so the session
// files sharing the legacy directory stay where they are. Existing files in
// memoryDir are never overwritten, and the legacy directory is never
// modified. A file that cannot be read is skipped with a warning. A sealed
// file is resealed for its new path (its encryption is bound to the path);
// when it cannot be opened yet the adoption stays pending and resumes on the
// next start.
func AdoptLegacyDir(legacyDir, memoryDir string, logger *zap.Logger) LegacyAdoptReport {
	var rep LegacyAdoptReport
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(legacyDir) == "" || strings.TrimSpace(memoryDir) == "" {
		return rep
	}
	legacyDir, memoryDir = filepath.Clean(legacyDir), filepath.Clean(memoryDir)
	if legacySameDir(legacyDir, memoryDir) {
		return rep
	}
	if info, err := os.Stat(legacyDir); err != nil || !info.IsDir() {
		return rep
	}
	if legacyExists(filepath.Join(memoryDir, legacyAdoptedMarker)) {
		return rep
	}
	resuming := legacyExists(filepath.Join(memoryDir, legacyAdoptingMarker))
	if !resuming && holdsMemoryFiles(memoryDir) {
		// Memory already lives here: adopting now could mix two histories.
		logger.Info("memory: legacy directory not adopted, the memory directory is already in use",
			zap.String("legacy_dir", legacyDir), zap.String("memory_dir", memoryDir))
		return rep
	}
	if err := os.MkdirAll(memoryDir, legacyDirPerm); err != nil {
		logger.Warn("memory: cannot create the memory directory to adopt the legacy one",
			zap.String("memory_dir", memoryDir), zap.Error(err))
		return rep
	}
	rep.Ran = true
	_ = os.WriteFile(filepath.Join(memoryDir, legacyAdoptingMarker), []byte(legacyDir+"\n"), legacyFilePerm) // #nosec G306 G703 -- marker under the memory dir; content is the legacy path only

	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		logger.Warn("memory: cannot list the legacy memory directory", zap.String("legacy_dir", legacyDir), zap.Error(err))
		rep.Pending = true
		return rep
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && (legacyDailyDir.MatchString(name) || legacyHas(legacyTreeDirs, name)):
			adoptLegacyTree(legacyDir, memoryDir, name, &rep, logger)
		case !e.IsDir() && isLegacyMemoryFile(name):
			adoptLegacyFile(filepath.Join(legacyDir, name), filepath.Join(memoryDir, name), name, &rep, logger)
		}
	}

	if rep.Pending {
		logger.Warn("memory: legacy memory adoption incomplete, it resumes on the next start",
			zap.String("legacy_dir", legacyDir), zap.Strings("copied", rep.Copied), zap.Strings("skipped", rep.Skipped))
		return rep
	}
	if err := os.WriteFile(filepath.Join(memoryDir, legacyAdoptedMarker), []byte(legacyDir+"\n"), legacyFilePerm); err != nil { // #nosec G306 G703 -- marker under the memory dir; content is the legacy path only
		logger.Warn("memory: cannot write the legacy adoption marker", zap.Error(err))
	}
	_ = os.Remove(filepath.Join(memoryDir, legacyAdoptingMarker))
	logger.Info("memory: adopted the legacy memory directory",
		zap.String("legacy_dir", legacyDir), zap.String("memory_dir", memoryDir),
		zap.Int("copied", len(rep.Copied)), zap.Strings("files", rep.Copied), zap.Strings("skipped", rep.Skipped))
	return rep
}

// isLegacyMemoryFile reports whether a top-level file name belongs to
// memory: a store file, a MEMORY.md backup, or a quarantined store file.
func isLegacyMemoryFile(name string) bool {
	if legacyHas(legacyStoreFiles, name) || legacyMemoryBackup.MatchString(name) {
		return true
	}
	if m := legacyQuarantine.FindStringSubmatch(name); m != nil {
		return legacyHas(legacyStoreFiles, m[1])
	}
	return false
}

// holdsMemoryFiles reports whether dir already carries any of memory's own
// files or directories.
func holdsMemoryFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if legacyDailyDir.MatchString(name) || legacyHas(legacyTreeDirs, name) {
				return true
			}
			continue
		}
		if isLegacyMemoryFile(name) {
			return true
		}
	}
	return false
}

// adoptLegacyTree copies the regular files of one memory subdirectory
// (one level deep, which is all the memory layers write).
func adoptLegacyTree(legacyDir, memoryDir, sub string, rep *LegacyAdoptReport, logger *zap.Logger) {
	src := filepath.Join(legacyDir, sub)
	entries, err := os.ReadDir(src)
	if err != nil {
		rep.Skipped = append(rep.Skipped, sub+"/ ("+err.Error()+")")
		logger.Warn("memory: legacy directory unreadable, skipped", zap.String("dir", src), zap.Error(err))
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasSuffix(e.Name(), ".tmp") || strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		rel := filepath.Join(sub, e.Name())
		adoptLegacyFile(filepath.Join(src, e.Name()), filepath.Join(memoryDir, rel), rel, rep, logger)
	}
}

// adoptLegacyFile copies one file when the destination does not exist yet.
func adoptLegacyFile(src, dst, rel string, rep *LegacyAdoptReport, logger *zap.Logger) {
	if _, err := os.Lstat(dst); err == nil {
		rep.Skipped = append(rep.Skipped, rel+" (already in the memory directory)")
		return
	}
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		rep.Skipped = append(rep.Skipped, rel+" (not a regular file)")
		return
	}
	data, err := os.ReadFile(src) // #nosec G304 -- fixed memory file names under the operator-set legacy dir
	if err != nil {
		rep.Skipped = append(rep.Skipped, rel+" ("+err.Error()+")")
		logger.Warn("memory: legacy memory file unreadable, skipped", zap.String("file", src), zap.Error(err))
		return
	}
	// A quarantined file is kept byte for byte: it is recovery material
	// that may not open at all, and the original stays in the legacy dir.
	if !legacyQuarantine.MatchString(filepath.Base(src)) {
		data, err = resealForPath(src, dst, data)
	}
	if err != nil {
		rep.Pending = true
		rep.Skipped = append(rep.Skipped, rel+" ("+err.Error()+")")
		logger.Warn("memory: sealed legacy memory file cannot be opened yet, left for the next start",
			zap.String("file", src), zap.Error(err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), legacyDirPerm); err != nil {
		rep.Skipped = append(rep.Skipped, rel+" ("+err.Error()+")")
		logger.Warn("memory: cannot create the memory subdirectory", zap.String("dir", filepath.Dir(dst)), zap.Error(err))
		return
	}
	// Checked again right before the rename: never replace a file that
	// appeared meanwhile (another replica adopting the same directory).
	if _, err := os.Lstat(dst); err == nil {
		rep.Skipped = append(rep.Skipped, rel+" (already in the memory directory)")
		return
	}
	if err := utils.AtomicWriteFile(dst, data, legacyFilePerm); err != nil {
		rep.Skipped = append(rep.Skipped, rel+" ("+err.Error()+")")
		logger.Warn("memory: cannot write the adopted memory file", zap.String("file", dst), zap.Error(err))
		return
	}
	rep.Copied = append(rep.Copied, rel)
}

// resealForPath returns data ready to live at dst. Plaintext and payloads
// that already open at dst (sealed without a path binding) pass unchanged;
// a payload bound to src is opened there and sealed again for dst.
func resealForPath(src, dst string, data []byte) ([]byte, error) {
	if !atrest.IsEncrypted(data) {
		return data, nil
	}
	if _, err := atrest.OpenAt(dst, data); err == nil {
		return data, nil
	}
	plain, err := atrest.OpenAt(src, data)
	if err != nil {
		return nil, fmt.Errorf("sealed: %w", err)
	}
	sealed, err := atrest.SealAt(dst, plain)
	if err != nil {
		return nil, fmt.Errorf("reseal: %w", err)
	}
	if !atrest.IsEncrypted(sealed) {
		return nil, errors.New("reseal: encryption at rest is off, refusing to write the file in plaintext")
	}
	return sealed, nil
}

func legacySameDir(a, b string) bool {
	if a == b {
		return true
	}
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

func legacyExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func legacyHas(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
