/*
 * ChatCLI - Persona System
 * pkg/persona/builtin/seed.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package builtin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// manifestName records, per builtin skill, the content hash we last wrote to
// disk. It lets Seed distinguish "user never touched this" (current hash ==
// recorded hash) — safe to update on a new release — from "user edited it"
// (current hash != recorded hash) — must be preserved.
const manifestName = ".builtin-manifest.json"

// SeedResult summarizes what Seed did, for logging/telemetry.
type SeedResult struct {
	Installed []string // skills written for the first time
	Updated   []string // builtin skills refreshed to a newer embedded version
	Preserved []string // skills left untouched because the user edited them
	Unchanged []string // already up to date
}

// Seed materializes the embedded essential skills into skillsDir.
//
// Behavior is idempotent and edit-safe:
//   - missing on disk            → write it (Installed)
//   - on disk, unedited, stale   → overwrite with embedded (Updated)
//   - on disk, user-edited       → leave as-is (Preserved)
//   - on disk, already current   → no write (Unchanged)
//
// "Unedited" means the on-disk content still hashes to what we recorded in the
// manifest the last time we wrote it. The directory and all paths are built
// with filepath.Join, so it is correct on Windows, macOS, and Linux.
func Seed(skillsDir string, logger *zap.Logger) (*SeedResult, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(skillsDir) == "" {
		return nil, errors.New("builtin.Seed: empty skillsDir")
	}
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		return nil, fmt.Errorf("builtin.Seed: create skills dir: %w", err)
	}

	manifest := loadManifest(skillsDir)
	res := &SeedResult{}

	entries, err := embeddedSkills()
	if err != nil {
		return nil, err
	}

	for name, content := range entries {
		embeddedHash := hashBytes(content)
		destDir := filepath.Join(skillsDir, name)
		destFile := filepath.Join(destDir, "SKILL.md")

		cur, readErr := os.ReadFile(destFile) // #nosec G304 -- destFile derived from skillsDir + embedded name
		switch {
		case errors.Is(readErr, os.ErrNotExist):
			if err := writeSkill(destDir, destFile, content); err != nil {
				return nil, err
			}
			manifest[name] = embeddedHash
			res.Installed = append(res.Installed, name)
		case readErr != nil:
			logger.Warn("builtin.Seed: cannot read existing skill, skipping", zap.String("skill", name), zap.Error(readErr))
		default:
			curHash := hashBytes(cur)
			recorded := manifest[name]
			switch {
			case curHash == embeddedHash:
				manifest[name] = embeddedHash // keep manifest honest
				res.Unchanged = append(res.Unchanged, name)
			case recorded != "" && curHash == recorded:
				// Unedited since our last seed → safe to refresh.
				if err := writeSkill(destDir, destFile, content); err != nil {
					return nil, err
				}
				manifest[name] = embeddedHash
				res.Updated = append(res.Updated, name)
			default:
				// User-authored content (or a pre-manifest install) → preserve.
				res.Preserved = append(res.Preserved, name)
			}
		}
	}

	if err := saveManifest(skillsDir, manifest); err != nil {
		logger.Warn("builtin.Seed: failed to persist manifest", zap.Error(err))
	}

	logger.Debug("builtin skills seeded",
		zap.Strings("installed", res.Installed),
		zap.Strings("updated", res.Updated),
		zap.Strings("preserved", res.Preserved),
		zap.Int("unchanged", len(res.Unchanged)),
	)
	return res, nil
}

// embeddedSkills returns a map of skill name → SKILL.md bytes from the embed FS.
func embeddedSkills() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(FS, skillsRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		// p == "skills/<name>/SKILL.md" (embed paths are always forward-slash).
		rel := strings.TrimPrefix(p, skillsRoot+"/")
		name := path.Dir(rel)
		if name == "." || name == "" {
			return nil
		}
		data, rerr := FS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[name] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("builtin.Seed: walk embedded skills: %w", err)
	}
	return out, nil
}

func writeSkill(dir, file string, content []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("builtin.Seed: create %s: %w", dir, err)
	}
	if err := os.WriteFile(file, content, 0o600); err != nil {
		return fmt.Errorf("builtin.Seed: write %s: %w", file, err)
	}
	return nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func loadManifest(skillsDir string) map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(filepath.Join(skillsDir, manifestName)) // #nosec G304 -- fixed name under skillsDir
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	return m
}

func saveManifest(skillsDir string, m map[string]string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(skillsDir, manifestName), data, 0o600)
}
