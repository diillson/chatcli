/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * selfevolve_manifest.go — edit-safety ledger for engine-authored skills.
 *
 * Mirrors pkg/persona/builtin/seed.go's manifest discipline: each skill the
 * engine writes is recorded with the SHA-256 of the exact bytes it wrote. On a
 * later pass the engine only evolves a skill whose current on-disk hash still
 * matches — i.e. one it owns and the user has not hand-edited. A skill the user
 * authored (never recorded) or has since changed (hash drift) is treated as
 * user-owned and never overwritten. The ledger lives beside the skills it
 * tracks so it travels and is cleaned up with them.
 */
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/diillson/chatcli/cli/plugins"
)

const selfEvolveManifestFile = ".selfevolve-manifest.json"

// selfEvolveManifest maps a skill name to the hash of the content the engine
// last wrote for it.
type selfEvolveManifest struct {
	path   string
	Skills map[string]string `json:"skills"`
}

// loadSelfEvolveManifest reads the ledger from the skills directory, returning
// an empty (but usable) ledger on any error so authoring still proceeds.
func loadSelfEvolveManifest() *selfEvolveManifest {
	m := &selfEvolveManifest{Skills: map[string]string{}}
	dir, err := plugins.SkillsDir()
	if err != nil {
		return m
	}
	m.path = filepath.Join(dir, selfEvolveManifestFile)
	data, err := os.ReadFile(m.path) // #nosec G304 -- path derived from the fixed skills dir
	if err != nil {
		return m
	}
	if err := json.Unmarshal(data, m); err != nil || m.Skills == nil {
		m.Skills = map[string]string{}
	}
	return m
}

// owns reports whether the engine wrote currentFileContent for name and the
// user has not changed it since.
func (m *selfEvolveManifest) owns(name, currentFileContent string) bool {
	h, ok := m.Skills[name]
	return ok && h == hashContent(currentFileContent)
}

// record stamps the hash of what is now on disk for name. Call it after a
// successful write so the engine recognizes its own copy next pass.
func (m *selfEvolveManifest) record(name string) {
	content, ok := plugins.ReadSkillContent(name)
	if !ok {
		return
	}
	if m.Skills == nil {
		m.Skills = map[string]string{}
	}
	m.Skills[name] = hashContent(content)
}

// save persists the ledger. Best-effort: a write failure only costs edit-safety
// recognition on the next pass, never the turn.
func (m *selfEvolveManifest) save() {
	if m.path == "" {
		return
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(m.path, data, 0o600)
}

func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
