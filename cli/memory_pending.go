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
	"github.com/diillson/chatcli/pkg/atrest"
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
	// Attempts counts drains that failed on this segment; it is dropped
	// after pendingMaxAttempts so an unparseable reply cannot wedge the
	// queue forever (one requeue, then gone).
	Attempts int `json:"attempts,omitempty"`
	// Workspace is the project the segment was recorded in, so a drain in
	// another directory labels episodes and resolves paths for the right
	// project.
	Workspace string `json:"workspace,omitempty"`
}

// pendingMaxAttempts is how many failed drains a queued segment survives.
const pendingMaxAttempts = 2

// extractionClient pairs a provider name with its client for the fallback walk.
type extractionClient struct {
	name string // provider name, for the fallback dedup and the logs
	llm  client.LLMClient
	// provider/model the usage is booked under ("" = the session's)
	provider, model string
}

// extractionClients returns the clients to try for one extraction, in order:
// the session's active client, then each configured fallback provider (deduped
// against the active one). Lookup failures are skipped — extraction uses what
// is actually reachable.
func (mw *memoryWorker) extractionClients() []extractionClient {
	out := make([]extractionClient, 0, 4)
	active := strings.ToUpper(strings.TrimSpace(mw.cli.Provider))
	if c, provider, model := mw.backgroundClient(); c != nil {
		out = append(out, extractionClient{name: active, llm: c, provider: provider, model: model})
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
	// Background spend is spend: the same budget gate the interactive turns
	// and the scheduler honor.
	if err := mw.cli.budgetBlockedErr(); err != nil {
		return "", err
	}
	clients := mw.extractionClients()
	if len(clients) == 0 {
		return "", fmt.Errorf("no LLM client available for memory extraction")
	}
	errs := make([]string, 0, len(clients))
	for i, ec := range clients {
		ctx, cancel := context.WithTimeout(parent, memoryExtractTimeout)
		before := usageSnapshot(ec.llm)
		response, err := ec.llm.SendPrompt(ctx, prompt, history, 0)
		mw.recordUsage(ec, usageOfCall(ec.llm, before))
		if mw.cli.refreshClientOnAuthError(err) {
			if c := mw.cli.getClient(); c != nil {
				before = usageSnapshot(c)
				response, err = c.SendPrompt(ctx, prompt, history, 0)
				mw.recordUsage(ec, usageOfCall(c, before))
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// The queue holds raw conversation on disk until extraction runs:
	// redact secrets first (extraction redacts again, harmlessly) and seal
	// when encryption at rest is on, exactly like every other store.
	redacted := make([]models.Message, len(messages))
	for i, m := range messages {
		redacted[i] = m
		redacted[i].Content = redactSecretsForLLM(m.Content)
	}
	workspace := ""
	if mw.store != nil && mw.store.Manager() != nil {
		workspace = mw.store.Manager().WorkspaceDir()
	}
	data, err := json.Marshal(pendingSegment{CreatedAt: time.Now(), Messages: redacted, Workspace: workspace})
	if err != nil {
		return "", err
	}
	if data, err = atrest.Seal(data); err != nil {
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

// rewritePending persists an updated segment (attempt counter) in place.
func (mw *memoryWorker) rewritePending(path string, seg pendingSegment) {
	data, err := json.Marshal(seg)
	if err != nil {
		return
	}
	if data, err = atrest.Seal(data); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil { // #nosec G304 -- our own queue dir
		return
	}
	_ = os.Rename(tmp, path)
}

// backgroundClient returns the client the memory worker's own calls use:
// the configured compaction/summarizer model (CHATCLI_COMPACT_MODEL) when
// there is one, else a DEDICATED instance of the session's provider/model.
// Never the interactive session's client: provider clients keep sticky
// usage state, and a background call sharing it clobbers (and is
// clobbered by) the concurrent interactive turn's usage.
func (mw *memoryWorker) backgroundClient() (client.LLMClient, string, string) {
	cli := mw.cli
	if cli == nil {
		return nil, "", ""
	}
	if c := cli.compactSummarizerClient(); c != nil {
		return c, cli.compactSummarizerProvider, cli.compactSummarizerModel
	}
	mw.mu.Lock()
	key := cli.Provider + ":" + cli.Model
	if mw.dedicated != nil && mw.dedicatedKey == key {
		c := mw.dedicated
		mw.mu.Unlock()
		return c, cli.Provider, cli.Model
	}
	mw.mu.Unlock()
	var c client.LLMClient
	if cli.manager != nil {
		if fresh, err := cli.manager.GetClient(cli.Provider, cli.Model); err == nil && fresh != nil {
			c = fresh
		}
	}
	if c == nil {
		c = cli.getClient() // no manager (tests, degraded boot): share, but still record
	}
	mw.mu.Lock()
	mw.dedicated, mw.dedicatedKey = c, key
	mw.mu.Unlock()
	return c, cli.Provider, cli.Model
}

// recordUsage books a background call under the memory slice of the cost
// tracker (provider:model parsed from the chain entry's name).
func (mw *memoryWorker) recordUsage(ec extractionClient, usage *models.UsageInfo) {
	if usage == nil || mw.cli == nil || mw.cli.costTracker == nil {
		return
	}
	provider, model := ec.provider, ec.model
	if provider == "" || model == "" {
		provider, model = mw.cli.Provider, mw.cli.Model
	}
	mw.cli.costTracker.RecordMemoryUsage(provider, model, usage)
}

// enterSegmentWorkspace points the store at the segment's workspace for
// the extraction and returns the restore; a no-op when it is unknown or
// already current.
func (mw *memoryWorker) enterSegmentWorkspace(workspace string) func() {
	if workspace == "" || mw.store == nil || mw.store.Manager() == nil {
		return func() {}
	}
	mgr := mw.store.Manager()
	prev := mgr.WorkspaceDir()
	if prev == workspace {
		return func() {}
	}
	mgr.SetWorkspaceDir(workspace)
	return func() { mgr.SetWorkspaceDir(prev) }
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
		if err == nil {
			data, err = atrest.Open(data)
		}
		if err != nil {
			continue
		}
		var seg pendingSegment
		if err := json.Unmarshal(data, &seg); err != nil || len(seg.Messages) == 0 {
			mw.logger.Warn("Memory worker: removing corrupt pending segment", zap.String("file", path))
			_ = os.Remove(path)
			continue
		}
		restore := mw.enterSegmentWorkspace(seg.Workspace)
		err = mw.extractAndSave(ctx, seg.Messages)
		restore()
		if err != nil {
			seg.Attempts++
			if seg.Attempts >= pendingMaxAttempts {
				mw.logger.Warn("Memory worker: pending segment failed repeatedly; dropping it",
					zap.String("file", path), zap.Int("attempts", seg.Attempts), zap.Error(err))
				_ = os.Remove(path)
				continue
			}
			mw.rewritePending(path, seg)
			mw.logger.Warn("Memory worker: pending segment still failing; will retry later",
				zap.String("file", path), zap.Int("attempts", seg.Attempts), zap.Error(err))
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
