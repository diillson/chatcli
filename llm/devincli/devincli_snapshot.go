/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package devincli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/pricing"
)

// snapshotFileName is the on-disk copy of the last successful model listing
// (~/.chatcli/devin_models.json). The listing only runs when a surface asks
// for models, but the cost tracker needs the per-account rates from the
// very first turn — including one-shot runs that never list anything — so
// each successful listing is persisted and restored at client construction.
const snapshotFileName = "devin_models.json"

// snapshotPath resolves the snapshot file; a variable so tests redirect it.
var snapshotPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chatcli", snapshotFileName), nil
}

type modelSnapshot struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Models    []snapshotModel `json:"models"`
}

type snapshotModel struct {
	ID              string   `json:"id"`
	DisplayName     string   `json:"display_name,omitempty"`
	Aliases         []string `json:"aliases,omitempty"`
	ContextWindow   int      `json:"context_window,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	InputUSDPerMTok float64  `json:"input_usd_per_mtok,omitempty"`
	OutputUSDPerMTk float64  `json:"output_usd_per_mtok,omitempty"`
}

// saveSnapshot persists the listing atomically (temp file + rename) with
// owner-only permissions. Failures are the caller's to log: the listing
// itself already succeeded and must not be reported as failed.
func saveSnapshot(entries []snapshotModel) error {
	if len(entries) == 0 {
		return nil
	}
	path, err := snapshotPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(modelSnapshot{FetchedAt: time.Now().UTC(), Models: entries}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".devin_models-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

var restoreOnce sync.Once

// restoreSnapshotOnce replays the persisted listing into the catalog and
// the pricing registry once per process. A live listing later overwrites
// every entry it re-reports, so the snapshot can only ever be behind the
// CLI, never ahead of it.
func restoreSnapshotOnce() {
	restoreOnce.Do(func() { _ = restoreSnapshot() })
}

// restoreSnapshot loads the snapshot file; a missing file is not an error.
func restoreSnapshot() error {
	path, err := snapshotPath()
	if err != nil {
		return err
	}
	// #nosec G304 -- fixed file name under the user's own ~/.chatcli state dir.
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var snap modelSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}
	for _, m := range snap.Models {
		if m.ID == "" {
			continue
		}
		registerDevinModel(m.ID, m.DisplayName, m.Aliases, m.ContextWindow, m.MaxOutputTokens)
		pricing.Register(catalog.ProviderDevin, m.ID, pricing.Rate{InputPerMTok: m.InputUSDPerMTok, OutputPerMTok: m.OutputUSDPerMTk})
	}
	return nil
}
