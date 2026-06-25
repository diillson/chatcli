package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// memoryWorker runs in the background, periodically analyzing conversation
// history and writing structured annotations to the MemoryStore.
type memoryWorker struct {
	cli              *ChatCLI
	logger           *zap.Logger
	lastProcessedIdx int // index of last message processed for memory
	mu               sync.Mutex
	stopCh           chan struct{}

	// coord owns cadence, back-pressure and the circuit breaker for the
	// extraction pass; the worker keeps only the memory-specific watermark and
	// the failure-notice latch.
	coord *runCoordinator

	// Resilience: segments that fail every provider are queued on disk at
	// pendingDir and drained on later runs.
	pendingDir     string
	failNoticeSent bool // guarded by mu
	// lookupFallback resolves a fallback provider client; indirected so tests
	// exercise the chain without a full LLM manager.
	lookupFallback func(provider string) (client.LLMClient, error)
}

const (
	// Minimum number of new messages before triggering memory extraction.
	memoryMinNewMessages = 4
	// Minimum time between memory extraction runs.
	memoryCooldown = 2 * time.Minute
	// Timeout for the LLM call that extracts memory.
	memoryExtractTimeout = 60 * time.Second
	// How often to check for compaction (6 hours).
	compactionCheckInterval = 6 * time.Hour
	// How often to check for daily note cleanup (24 hours).
	dailyCleanupInterval = 24 * time.Hour
)

func newMemoryWorker(cli *ChatCLI) *memoryWorker {
	mw := &memoryWorker{
		cli:        cli,
		logger:     cli.logger,
		stopCh:     make(chan struct{}),
		pendingDir: defaultPendingDir(),
		coord:      newRunCoordinator(memoryCooldown, memoryMinNewMessages),
	}
	mw.lookupFallback = func(provider string) (client.LLMClient, error) {
		if cli.manager == nil {
			return nil, fmt.Errorf("no LLM manager")
		}
		return cli.manager.GetClient(provider, "")
	}
	return mw
}

// start begins the background memory worker loop. The passed context is
// detached (cancellation governed by stopCh) and inherited by every
// background-driven extraction/compaction.
func (mw *memoryWorker) start(ctx context.Context) {
	go mw.loop(context.WithoutCancel(ctx))
}

// stop signals the worker to stop.
func (mw *memoryWorker) stop() {
	select {
	case <-mw.stopCh:
		// already closed
	default:
		close(mw.stopCh)
	}
}

// nudge is called after each LLM response to check if memory extraction should run.
// It runs in a goroutine — non-blocking to the main flow.
func (mw *memoryWorker) nudge(ctx context.Context) {
	if mw.cli.memoryStore == nil {
		return
	}
	// Don't queue multiple runs
	if mw.coord.isRunning() {
		return
	}
	// The extraction goroutine outlives the triggering request; detach
	// cancellation while inheriting context values.
	detached := context.WithoutCancel(ctx)
	go mw.maybeExtract(detached)
}

func (mw *memoryWorker) loop(ctx context.Context) {
	extractTicker := time.NewTicker(3 * time.Minute)
	compactTicker := time.NewTicker(compactionCheckInterval)
	cleanupTicker := time.NewTicker(dailyCleanupInterval)
	defer extractTicker.Stop()
	defer compactTicker.Stop()
	defer cleanupTicker.Stop()

	for {
		select {
		case <-mw.stopCh:
			return
		case <-extractTicker.C:
			mw.maybeExtract(ctx)
		case <-compactTicker.C:
			mw.maybeCompact(ctx)
		case <-cleanupTicker.C:
			mw.cleanupDailyNotes()
		}
	}
}

func (mw *memoryWorker) maybeExtract(ctx context.Context) {
	mw.mu.Lock()
	lastIdx := mw.lastProcessedIdx
	mw.mu.Unlock()

	historyLen := len(mw.cli.history)
	newMessages := historyLen - lastIdx

	// One gate for back-pressure, cadence (cooldown + min new messages) and the
	// circuit breaker. Only release when it actually acquired.
	if !mw.coord.tryAcquire(newMessages) {
		return
	}
	defer mw.coord.release()

	// Extract the new messages (copy to avoid races)
	messagesToProcess := make([]models.Message, newMessages)
	copy(messagesToProcess, mw.cli.history[lastIdx:])

	mw.logger.Debug("Memory worker: extracting annotations",
		zap.Int("new_messages", newMessages),
		zap.Int("from_idx", lastIdx),
	)

	// Record interaction event
	if mw.cli.memoryStore != nil {
		mw.cli.memoryStore.RecordInteraction(memory.InteractionEvent{
			Timestamp: time.Now(),
			Feature:   mw.detectFeature(),
		})
	}

	// Show subtle status
	mw.showStatus("updating memory...")

	// Queued segments first (oldest dialog wins on causality), then the live one.
	mw.drainPending(ctx)

	err := mw.extractAndSave(ctx, messagesToProcess)

	if err != nil {
		mw.onExtractionFailure(err, messagesToProcess, historyLen)
	} else {
		mw.onExtractionSuccess(historyLen)
	}

	mw.clearStatus()
}

// onExtractionSuccess advances the watermark, resets the failure streak and
// invalidates the prompt cache so the next turn sees the new memory.
func (mw *memoryWorker) onExtractionSuccess(historyLen int) {
	mw.logger.Debug("Memory worker: annotations saved successfully")
	mw.mu.Lock()
	mw.lastProcessedIdx = historyLen
	mw.failNoticeSent = false
	mw.mu.Unlock()
	mw.coord.recordSuccess()
	if mw.cli.contextBuilder != nil {
		mw.cli.contextBuilder.InvalidateCache()
	}
}

// onExtractionFailure persists the segment to the on-disk queue (advancing the
// watermark — it is durably queued) and surfaces a one-line notice once the
// failure streak crosses the threshold. If even persisting fails, the
// watermark stays put so the in-memory retry path still covers the segment.
func (mw *memoryWorker) onExtractionFailure(err error, segment []models.Message, historyLen int) {
	mw.logger.Warn("Memory worker: extraction failed", zap.Error(err))

	queued := false
	if path, perr := mw.persistPending(segment); perr != nil {
		mw.logger.Warn("Memory worker: could not queue segment; will retry in memory", zap.Error(perr))
	} else {
		queued = true
		mw.logger.Info("Memory worker: segment queued for retry", zap.String("file", path))
	}

	fails := mw.coord.recordFailure()

	mw.mu.Lock()
	if queued {
		mw.lastProcessedIdx = historyLen
	}
	notify := fails >= memoryFailNoticeThreshold && !mw.failNoticeSent
	if notify {
		mw.failNoticeSent = true
	}
	mw.mu.Unlock()

	if notify {
		mw.cli.pushMemoryNotice(i18n.T("mem.notice.failing", fails))
	}
}

func (mw *memoryWorker) extractAndSave(ctx context.Context, messages []models.Message) error {
	if mw.cli.memoryStore == nil {
		return fmt.Errorf("memory store not available")
	}

	mgr := mw.cli.memoryStore.Manager()

	// Self-evolution piggybacks on this same extraction pass (no extra LLM
	// call): when enabled, the prompt asks for SKILL_CANDIDATES alongside the
	// memory sections, and we act on them after parsing the response.
	evolveMode := resolveSelfEvolveMode()
	instructions := memory.EnhancedExtractionPromptV2
	if evolveMode != selfEvolveOff {
		instructions += "\n" + selfEvolveSkillDirective
		// Inject only the compact skill index (names + descriptions), so the
		// model can target an existing skill for evolution without any bodies
		// bloating the per-turn prompt. The body is pulled on demand at merge.
		if idx := mw.cli.buildSkillIndex(); idx != "" {
			instructions += "\n\n" + idx
		}
	}

	// Build conversation snippet for extraction
	var sb strings.Builder
	for _, msg := range messages {
		content := msg.Content
		// Truncate very long messages to keep the extraction prompt small
		if len(content) > 1500 {
			content = content[:1200] + "\n... [truncated] ...\n" + content[len(content)-200:]
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n\n", msg.Role, content))
	}

	// Build enhanced prompt with existing context
	var fullPrompt strings.Builder
	fullPrompt.WriteString(instructions)
	fullPrompt.WriteString("\n\n---\n\n")

	// Include current workspace so the extraction LLM can distinguish session context
	if wsDir := mgr.WorkspaceDir(); wsDir != "" {
		fullPrompt.WriteString(fmt.Sprintf("CURRENT SESSION WORKSPACE: %s\n", wsDir))
		fullPrompt.WriteString("(All paths and facts from this conversation belong to this workspace.)\n\n---\n\n")
	}

	existingContext := mgr.FormatExistingContext()
	if existingContext != "" {
		fullPrompt.WriteString(existingContext)
		fullPrompt.WriteString("\n\n---\n\n")
	}

	fullPrompt.WriteString("CONVERSATION SEGMENT TO ANALYZE:\n\n")
	fullPrompt.WriteString(sb.String())

	prompt := fullPrompt.String()

	// Pass prompt as both the prompt param and the last user message in history.
	history := []models.Message{
		{Role: "system", Content: instructions},
		{Role: "user", Content: prompt},
	}

	// Walk the provider chain (active client first, then fallbacks) so one
	// provider's outage does not cost the conversation its memory.
	response, err := mw.callExtraction(ctx, prompt, history)
	if err != nil {
		return fmt.Errorf("memory extraction LLM call failed: %w", err)
	}

	response = strings.TrimSpace(response)
	if response == "" || isNothingNew(response) {
		mw.logger.Debug("Memory worker: LLM returned nothing new")
		return nil
	}

	mw.logger.Debug("Memory worker: LLM response received",
		zap.Int("response_len", len(response)),
		zap.String("response_preview", truncateForLog(response, 200)),
	)

	// Use enhanced processing that populates profile, topics, projects.
	// A non-empty summary becomes a visible one-line notice so the user
	// can tell the system actually learned something this turn.
	summary := mw.cli.memoryStore.ProcessExtractionResult(response)
	if !summary.IsEmpty() {
		mw.cli.pushMemoryNotice(formatMemoryNotice(summary))
	}

	// Same response, second harvest: author new skills, or evolve existing ones
	// (pulling only the targeted skill's body via mergeSkillBody on demand).
	if evolveMode != selfEvolveOff {
		if es := mw.cli.applySkillCandidates(ctx, response, evolveMode, mw.cli.mergeSkillBody); !es.isEmpty() {
			mw.cli.pushMemoryNotice(formatSelfEvolveNotice(es))
		}
	}

	mw.logger.Debug("Memory worker: enhanced extraction complete",
		zap.Int("facts_added", summary.FactsAdded),
		zap.Bool("profile_updated", summary.ProfileUpdated),
		zap.Int("topics", summary.TopicsRecorded),
	)
	return nil
}

// maybeCompact checks if memory compaction should run and executes it.
func (mw *memoryWorker) maybeCompact(ctx context.Context) {
	if mw.cli.memoryStore == nil {
		return
	}

	mgr := mw.cli.memoryStore.Manager()
	if !mgr.NeedsCompaction() {
		return
	}

	mw.logger.Info("Memory worker: starting compaction")

	llmClient := mw.cli.getClient()
	var sendPrompt func(ctx context.Context, prompt string) (string, error)

	if llmClient != nil {
		sendPrompt = func(ctx context.Context, prompt string) (string, error) {
			history := []models.Message{
				{Role: "user", Content: prompt},
			}
			return llmClient.SendPrompt(ctx, prompt, history, 0)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if err := mgr.RunCompaction(ctx, sendPrompt); err != nil {
		mw.logger.Warn("Memory worker: compaction failed", zap.Error(err))
	} else {
		mw.logger.Info("Memory worker: compaction complete")
		if mw.cli.contextBuilder != nil {
			mw.cli.contextBuilder.InvalidateCache()
		}
	}
}

// cleanupDailyNotes removes old daily notes.
func (mw *memoryWorker) cleanupDailyNotes() {
	if mw.cli.memoryStore == nil {
		return
	}

	mgr := mw.cli.memoryStore.Manager()
	deleted, err := mgr.CleanupDailyNotes()
	if err != nil {
		mw.logger.Warn("Memory worker: daily cleanup failed", zap.Error(err))
	} else if deleted > 0 {
		mw.logger.Info("Memory worker: cleaned up old daily notes", zap.Int("deleted", deleted))
	}
}

// detectFeature detects the current mode for usage stats.
func (mw *memoryWorker) detectFeature() string {
	// Check recent messages for mode hints
	histLen := len(mw.cli.history)
	if histLen == 0 {
		return "chat"
	}

	for i := histLen - 1; i >= 0 && i >= histLen-5; i-- {
		content := mw.cli.history[i].Content
		if strings.Contains(content, "/agent") || strings.Contains(content, "agent mode") {
			return "agent"
		}
		if strings.Contains(content, "/coder") || strings.Contains(content, "coder mode") {
			return "coder"
		}
		if strings.Contains(content, "/run") {
			return "run"
		}
	}
	return "chat"
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// showStatus logs the memory worker status.
func (mw *memoryWorker) showStatus(msg string) {
	mw.logger.Debug("Memory worker: " + msg)
}

func (mw *memoryWorker) clearStatus() {
	// no-op: status is only logged, not written to terminal
}

// --- Legacy parsing functions ---
// Deprecated: These are kept only for existing tests in memory_worker_test.go.
// Production code uses memory.Manager.ProcessExtraction() with the enhanced parser.

// parseMemoryResponse splits the LLM response into daily notes and long-term facts.
//
//nolint:unused // used by memory_worker_test; golangci run.tests=false hides the call site.
func parseMemoryResponse(response string) (daily string, longTerm string) {
	upper := strings.ToUpper(response)

	dailyIdx := findSectionIndex(upper, "DAILY")
	longTermIdx := findSectionIndex(upper, "LONGTERM")
	if longTermIdx < 0 {
		longTermIdx = findSectionIndex(upper, "LONG-TERM")
	}
	if longTermIdx < 0 {
		longTermIdx = findSectionIndex(upper, "LONG_TERM")
	}

	extractAfter := func(idx int) string {
		nlIdx := strings.Index(response[idx:], "\n")
		if nlIdx < 0 {
			return ""
		}
		return strings.TrimSpace(response[idx+nlIdx+1:])
	}

	switch {
	case dailyIdx >= 0 && longTermIdx >= 0:
		if dailyIdx < longTermIdx {
			nlIdx := strings.Index(response[dailyIdx:], "\n")
			if nlIdx >= 0 {
				daily = strings.TrimSpace(response[dailyIdx+nlIdx+1 : longTermIdx])
			}
			longTerm = extractAfter(longTermIdx)
		} else {
			nlIdx := strings.Index(response[longTermIdx:], "\n")
			if nlIdx >= 0 {
				longTerm = strings.TrimSpace(response[longTermIdx+nlIdx+1 : dailyIdx])
			}
			daily = extractAfter(dailyIdx)
		}
	case dailyIdx >= 0:
		daily = extractAfter(dailyIdx)
	case longTermIdx >= 0:
		longTerm = extractAfter(longTermIdx)
	default:
		daily = response
	}

	if isNothingNew(daily) {
		daily = ""
	}
	if isNothingNew(longTerm) {
		longTerm = ""
	}

	return daily, longTerm
}

func isNothingNew(s string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(s))
	normalized = strings.TrimRight(normalized, ".!,;:")
	normalized = strings.TrimSpace(normalized)
	switch normalized {
	case "NOTHING_NEW", "NOTHING NEW", "NOTHING-NEW", "N/A", "NONE", "NA":
		return true
	}
	return false
}

//nolint:unused // used by memory_worker_test via parseMemoryResponse.
func findSectionIndex(upperResponse string, keyword string) int {
	patterns := []string{
		"## " + keyword,
		"##" + keyword,
		"# " + keyword,
		"**" + keyword + "**",
	}
	best := -1
	for _, p := range patterns {
		idx := strings.Index(upperResponse, p)
		if idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	return best
}
