/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Persistence and scoping of the token calibrator.
 *
 * A learned chars-per-token ratio is worth keeping: it takes several
 * turns to converge and every new process started at 4.0 again. Ratios
 * are saved to <state root>/calibration.json (atomic write, debounced)
 * and loaded on first use. The state root is the tenant root when a
 * gateway tenant is active, so tenants never share (or leak) ratios; the
 * global root serves the REPL, the MCP server and one-shot runs.
 */
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/utils"
)

// calibrationFileName is the per-root persistence file.
const calibrationFileName = "calibration.json"

// calibrationSaveDelay debounces writes: a burst of turns lands one write.
const calibrationSaveDelay = 2 * time.Second

// calibrationFile is the on-disk shape.
type calibrationFile struct {
	Version   int                `json:"version"`
	UpdatedAt time.Time          `json:"updated_at"`
	Ratios    map[string]float64 `json:"ratios"`
	Samples   map[string]int     `json:"samples"`
}

var (
	calibratorsMu     sync.Mutex
	calibratorsByRoot = map[string]*tokenCalibrator{}
)

// calibratorFor returns the calibrator scoped to a state root, loading
// its file on first use. An empty root is the process-wide calibrator.
func calibratorFor(root string) *tokenCalibrator {
	if root == "" {
		return globalTokenCalibrator
	}
	calibratorsMu.Lock()
	defer calibratorsMu.Unlock()
	if c, ok := calibratorsByRoot[root]; ok {
		return c
	}
	c := newTokenCalibrator()
	c.path = filepath.Join(root, calibrationFileName)
	c.load()
	calibratorsByRoot[root] = c
	return c
}

// calibrator returns this CLI's calibrator (tenant-scoped when a tenant
// is active) and keeps the ctxmgr estimator pointed at it.
func (cli *ChatCLI) calibrator() *tokenCalibrator {
	if cli == nil {
		return globalTokenCalibrator
	}
	return calibratorFor(cli.stateRoot)
}

// charsPerToken is the learned ratio for the session's provider/model.
func (cli *ChatCLI) charsPerToken() float64 {
	if cli == nil {
		return defaultCharsPerToken
	}
	ratio, _ := cli.calibrator().CharsPerToken(cli.Provider, cli.Model)
	return ratio
}

// estimateTokens converts characters to tokens with the learned ratio.
func (cli *ChatCLI) estimateTokens(chars int) int {
	if cli == nil {
		return int(float64(chars)/defaultCharsPerToken + 0.5)
	}
	return cli.calibrator().EstimateTokens(cli.Provider, cli.Model, chars)
}

// estimateTokens64 is estimateTokens for byte sizes.
func (cli *ChatCLI) estimateTokens64(chars int64) int64 {
	if chars <= 0 {
		return 0
	}
	if cli == nil {
		return int64(float64(chars)/defaultCharsPerToken + 0.5)
	}
	return int64(float64(chars)/cli.charsPerToken() + 0.5)
}

// installTokenEstimator points the ctxmgr package (contexts, chunker,
// validator, digests) at this CLI's live ratio, read lazily so a model
// switch or a new sample is reflected without a sync point.
func (cli *ChatCLI) installTokenEstimator() {
	ctxmgr.SetCharsPerTokenSource(cli.charsPerToken)
}

// load reads the persisted ratios (missing or corrupt file = empty).
func (c *tokenCalibrator) load() {
	if c == nil || c.path == "" {
		return
	}
	data, err := os.ReadFile(c.path) // #nosec G304 -- path under the state root
	if err != nil {
		return
	}
	var f calibrationFile
	if err := json.Unmarshal(data, &f); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, r := range f.Ratios {
		if r >= calibrationMinRatio && r <= calibrationMaxRatio {
			c.ratios[k] = r
			c.samples[k] = f.Samples[k]
		}
	}
}

// scheduleSave debounces a write of the current ratios. Called with the
// calibrator's lock held by Observe.
func (c *tokenCalibrator) scheduleSave() {
	if c == nil || c.path == "" {
		return
	}
	if c.saveTimer != nil {
		c.saveTimer.Stop()
	}
	c.saveTimer = time.AfterFunc(calibrationSaveDelay, c.save)
}

// save writes the ratios atomically; best-effort (a read-only home just
// loses persistence, never a turn).
func (c *tokenCalibrator) save() {
	if c == nil || c.path == "" {
		return
	}
	c.mu.RLock()
	f := calibrationFile{Version: 1, UpdatedAt: time.Now(), Ratios: make(map[string]float64, len(c.ratios)), Samples: make(map[string]int, len(c.samples))}
	for k, r := range c.ratios {
		f.Ratios[k] = r
		f.Samples[k] = c.samples[k]
	}
	c.mu.RUnlock()
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	_ = utils.AtomicWriteFile(c.path, data, 0o600)
}

// flushCalibration writes pending ratios now (session end, tenant release).
func (c *tokenCalibrator) flushCalibration() {
	if c == nil || c.path == "" {
		return
	}
	c.mu.Lock()
	if c.saveTimer != nil {
		c.saveTimer.Stop()
		c.saveTimer = nil
	}
	c.mu.Unlock()
	c.save()
}
