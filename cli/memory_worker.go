package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// memoryWorker runs in the background, periodically analyzing conversation
// history and writing structured annotations to the MemoryStore.
type memoryWorker struct {
	cli              *ChatCLI
	logger           *zap.Logger
	lastProcessedIdx int       // index of last message processed for memory
	lastRunTime      time.Time // when we last ran the extraction
	mu               sync.Mutex
	running          atomic.Bool
	stopCh           chan struct{}
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
	return &memoryWorker{
		cli:    cli,
		logger: cli.logger,
		stopCh: make(chan struct{}),
	}
}

// start begins the background memory worker loop.
func (mw *memoryWorker) start() {
	go mw.loop()
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
func (mw *memoryWorker) nudge() {
	if mw.cli.memoryStore == nil {
		return
	}
	// Don't queue multiple runs
	if mw.running.Load() {
		return
	}
	go mw.maybeExtract()
}

func (mw *memoryWorker) loop() {
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
			mw.maybeExtract()
		case <-compactTicker.C:
			mw.maybeCompact()
		case <-cleanupTicker.C:
			mw.cleanupDailyNotes()
		}
	}
}

func (mw *memoryWorker) maybeExtract() {
	if !mw.running.CompareAndSwap(false, true) {
		return
	}
	defer mw.running.Store(false)

	mw.mu.Lock()
	lastIdx := mw.lastProcessedIdx
	lastRun := mw.lastRunTime
	mw.mu.Unlock()

	// Cooldown check
	if time.Since(lastRun) < memoryCooldown {
		return
	}

	// Check if enough new messages exist
	historyLen := len(mw.cli.history)
	newMessages := historyLen - lastIdx
	if newMessages < memoryMinNewMessages {
		return
	}

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

	err := mw.extractAndSave(messagesToProcess)

	if err != nil {
		mw.logger.Warn("Memory worker: extraction failed", zap.Error(err))
		// On failure, only update lastRunTime (cooldown) but NOT lastProcessedIdx,
		// so these messages will be retried on the next run.
		mw.mu.Lock()
		mw.lastRunTime = time.Now()
		mw.mu.Unlock()
	} else {
		mw.logger.Debug("Memory worker: annotations saved successfully")
		mw.mu.Lock()
		mw.lastProcessedIdx = historyLen
		mw.lastRunTime = time.Now()
		mw.mu.Unlock()
		// Invalidate context builder cache so next prompt picks up new memory
		if mw.cli.contextBuilder != nil {
			mw.cli.contextBuilder.InvalidateCache()
		}
	}

	mw.clearStatus()
}

func (mw *memoryWorker) extractAndSave(messages []models.Message) error {
	llmClient := mw.cli.getClient()
	if llmClient == nil || mw.cli.memoryStore == nil {
		return fmt.Errorf("client or memory store not available")
	}

	mgr := mw.cli.memoryStore.Manager()

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
	fullPrompt.WriteString(memory.EnhancedExtractionPrompt)
	fullPrompt.WriteString("\n\n---\n\n")

	existingContext := mgr.FormatExistingContext()
	if existingContext != "" {
		fullPrompt.WriteString(existingContext)
		fullPrompt.WriteString("\n\n---\n\n")
	}

	fullPrompt.WriteString("CONVERSATION SEGMENT TO ANALYZE:\n\n")
	fullPrompt.WriteString(sb.String())

	ctx, cancel := context.WithTimeout(context.Background(), memoryExtractTimeout)
	defer cancel()

	prompt := fullPrompt.String()

	// Pass prompt as both the prompt param and the last user message in history.
	history := []models.Message{
		{Role: "system", Content: memory.EnhancedExtractionPrompt},
		{Role: "user", Content: prompt},
	}

	response, err := llmClient.SendPrompt(ctx, prompt, history, 0)
	// Auto-retry on OAuth token expiration (401)
	if mw.cli.refreshClientOnAuthError(err) {
		llmClient = mw.cli.getClient()
		response, err = llmClient.SendPrompt(ctx, prompt, history, 0)
	}
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

	// Use enhanced processing that populates profile, topics, projects
	mw.cli.memoryStore.ProcessExtraction(response)

	mw.logger.Debug("Memory worker: enhanced extraction complete")
	return nil
}

// maybeCompact checks if memory compaction should run and executes it.
func (mw *memoryWorker) maybeCompact() {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
