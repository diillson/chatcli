/*
 * ChatCLI - Memory extraction resilience: provider fallback + on-disk pending queue.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Memory extraction is how conversations become durable knowledge. Until now a
 * failing extraction LLM (provider outage, timeout) meant the segment was only
 * retried in-process — and lost for good on exit — while the user noticed
 * nothing for days. This file closes both gaps: extraction walks a fallback
 * provider chain before giving up, and a segment that still fails is persisted
 * to ~/.chatcli/memory/pending as a write-ahead queue, drained on later runs —
 * surviving restarts. Repeated failures surface a one-line notice so silent
 * memory loss cannot happen again.
 */
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

const (
	// pendingMaxFiles bounds the on-disk queue; beyond it the oldest segments
	// are dropped (with a warning) — the queue is a buffer, not an archive.
	pendingMaxFiles = 100
	// pendingDrainBatch caps how many queued segments one extraction run
	// retries, so a long backlog never monopolizes the worker.
	pendingDrainBatch = 3
	// memoryFailNoticeThreshold is how many consecutive extraction failures
	// trigger the user-visible notice.
	memoryFailNoticeThreshold = 2
)

// envMemoryFallbackProviders lists extraction fallback providers; when empty,
// the general CHATCLI_FALLBACK_PROVIDERS chain is used.
const (
	envMemoryFallbackProviders  = "CHATCLI_MEMORY_FALLBACK_PROVIDERS"
	envGeneralFallbackProviders = "CHATCLI_FALLBACK_PROVIDERS"
)

// pendingSegment is one conversation slice waiting for (re-)extraction.
type pendingSegment struct {
	CreatedAt time.Time        `json:"created_at"`
	Messages  []models.Message `json:"messages"`
}

// extractionClient pairs a provider name with its client for the fallback walk.
type extractionClient struct {
	name string
	llm  client.LLMClient
}

// extractionClients returns the clients to try for one extraction, in order:
// the session's active client, then each configured fallback provider (deduped
// against the active one). Lookup failures are skipped — extraction uses what
// is actually reachable.
func (mw *memoryWorker) extractionClients() []extractionClient {
	out := make([]extractionClient, 0, 4)
	active := strings.ToUpper(strings.TrimSpace(mw.cli.Provider))
	if c := mw.cli.getClient(); c != nil {
		out = append(out, extractionClient{name: active, llm: c})
	}
	raw := strings.TrimSpace(os.Getenv(envMemoryFallbackProviders))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(envGeneralFallbackProviders))
	}
	for _, p := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" || p == active {
			continue
		}
		c, err := mw.lookupFallback(p)
		if err != nil || c == nil {
			mw.logger.Debug("Memory worker: fallback provider unavailable",
				zap.String("provider", p), zap.Error(err))
			continue
		}
		out = append(out, extractionClient{name: p, llm: c})
	}
	return out
}

// callExtraction sends the extraction prompt through the client chain,
// returning the first success. Each attempt gets its own timeout so a hung
// provider cannot consume the whole budget of the ones behind it.
func (mw *memoryWorker) callExtraction(parent context.Context, prompt string, history []models.Message) (string, error) {
	clients := mw.extractionClients()
	if len(clients) == 0 {
		return "", fmt.Errorf("no LLM client available for memory extraction")
	}
	errs := make([]string, 0, len(clients))
	for i, ec := range clients {
		ctx, cancel := context.WithTimeout(parent, memoryExtractTimeout)
		response, err := ec.llm.SendPrompt(ctx, prompt, history, 0)
		if mw.cli.refreshClientOnAuthError(err) {
			if c := mw.cli.getClient(); c != nil {
				response, err = c.SendPrompt(ctx, prompt, history, 0)
			}
		}
		cancel()
		if err == nil {
			if i > 0 {
				mw.logger.Info("Memory worker: extraction served by fallback provider",
					zap.String("provider", ec.name))
			}
			return response, nil
		}
		mw.logger.Warn("Memory worker: extraction attempt failed",
			zap.String("provider", ec.name), zap.Error(err))
		errs = append(errs, ec.name+": "+err.Error())
	}
	return "", fmt.Errorf("memory extraction failed on all providers: %s", strings.Join(errs, " | "))
}

// persistPending writes a failed segment to the on-disk queue atomically and
// enforces the queue cap. Returns the stored path.
func (mw *memoryWorker) persistPending(messages []models.Message) (string, error) {
	dir := mw.pendingDir
	if dir == "" {
		return "", fmt.Errorf("pending dir not configured")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	data, err := json.Marshal(pendingSegment{CreatedAt: time.Now(), Messages: messages})
	if err != nil {
		return "", err
	}
	// Zero-padded so lexicographic file order is chronological order.
	path := filepath.Join(dir, fmt.Sprintf("seg-%020d.json", time.Now().UnixNano()))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	mw.enforcePendingCap()
	return path, nil
}

// enforcePendingCap drops the oldest queued segments beyond pendingMaxFiles.
func (mw *memoryWorker) enforcePendingCap() {
	files := mw.pendingFiles()
	if len(files) <= pendingMaxFiles {
		return
	}
	for _, f := range files[:len(files)-pendingMaxFiles] {
		_ = os.Remove(f)
	}
	mw.logger.Warn("Memory worker: pending queue over cap; dropped oldest segments",
		zap.Int("dropped", len(files)-pendingMaxFiles), zap.Int("cap", pendingMaxFiles))
}

// pendingFiles lists queued segments oldest-first (names embed creation time).
func (mw *memoryWorker) pendingFiles() []string {
	if mw.pendingDir == "" {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(mw.pendingDir, "seg-*.json"))
	sort.Strings(matches)
	return matches
}

// drainPending retries up to pendingDrainBatch queued segments and returns how
// many were processed. It stops at the first still-failing segment — the
// provider is likely still down, and order preserves conversation causality.
// Corrupt files are removed so one bad write can never wedge the queue.
func (mw *memoryWorker) drainPending(ctx context.Context) int {
	processed := 0
	for _, path := range mw.pendingFiles() {
		if processed >= pendingDrainBatch {
			break
		}
		data, err := os.ReadFile(path) // #nosec G304 -- our own queue dir under ~/.chatcli
		if err != nil {
			continue
		}
		var seg pendingSegment
		if err := json.Unmarshal(data, &seg); err != nil || len(seg.Messages) == 0 {
			mw.logger.Warn("Memory worker: removing corrupt pending segment", zap.String("file", path))
			_ = os.Remove(path)
			continue
		}
		if err := mw.extractAndSave(ctx, seg.Messages); err != nil {
			mw.logger.Warn("Memory worker: pending segment still failing; will retry later",
				zap.String("file", path), zap.Error(err))
			break
		}
		_ = os.Remove(path)
		processed++
	}
	if processed > 0 {
		mw.logger.Info("Memory worker: drained pending segments", zap.Int("processed", processed))
	}
	return processed
}

// defaultPendingDir resolves ~/.chatcli/memory/pending, or "" when the home
// directory cannot be determined (queue disabled, in-memory retry remains).
func defaultPendingDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".chatcli", "memory", "pending")
}
