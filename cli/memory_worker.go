package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-mw.stopCh:
			return
		case <-ticker.C:
			mw.maybeExtract()
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

	// Read existing long-term memory for context
	existingMemory := mw.cli.memoryStore.ReadLongTerm()

	// Build a single user prompt that combines instructions + context + conversation.
	// All providers use the `prompt` param as the user message fallback,
	// so we put everything in ONE user message to avoid duplication issues.
	var fullPrompt strings.Builder
	fullPrompt.WriteString(memoryExtractionPrompt)
	fullPrompt.WriteString("\n\n---\n\n")
	if existingMemory != "" {
		fullPrompt.WriteString("EXISTING LONG-TERM MEMORY (do NOT duplicate these facts):\n\n")
		fullPrompt.WriteString(existingMemory)
		fullPrompt.WriteString("\n\n---\n\n")
	}
	fullPrompt.WriteString("CONVERSATION SEGMENT TO ANALYZE:\n\n")
	fullPrompt.WriteString(sb.String())

	ctx, cancel := context.WithTimeout(context.Background(), memoryExtractTimeout)
	defer cancel()

	prompt := fullPrompt.String()

	// Pass prompt as both the prompt param and the last user message in history.
	// This ensures all providers handle it correctly:
	// - Providers that use history: see system + user messages
	// - Providers that use prompt param: see the full prompt
	// - The fallback check (prompt == history[last].Content) prevents duplication
	history := []models.Message{
		{Role: "system", Content: memoryExtractionPrompt},
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

	// Parse response: split into DAILY and LONGTERM sections
	dailyContent, longTermContent := parseMemoryResponse(response)

	saved := false
	// Write daily note
	if dailyContent != "" {
		if err := mw.cli.memoryStore.WriteDailyNote(dailyContent); err != nil {
			mw.logger.Warn("Failed to write daily note", zap.Error(err))
		} else {
			saved = true
			mw.logger.Debug("Memory worker: daily note written")
		}
	}

	// Append to long-term memory (only genuinely new facts)
	if longTermContent != "" {
		if err := mw.cli.memoryStore.AppendLongTerm("\n" + longTermContent); err != nil {
			mw.logger.Warn("Failed to append long-term memory", zap.Error(err))
		} else {
			saved = true
			mw.logger.Debug("Memory worker: long-term memory updated")
		}
	}

	if !saved && dailyContent == "" && longTermContent == "" {
		mw.logger.Debug("Memory worker: parser returned empty sections",
			zap.String("response_preview", truncateForLog(response, 300)),
		)
	}

	return nil
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// showStatus logs the memory worker status. Writing directly to the terminal
// from a background goroutine corrupts go-prompt's display on all platforms
// (the user would need to press Enter to get the prompt back), so we only log.
func (mw *memoryWorker) showStatus(msg string) {
	mw.logger.Debug("Memory worker: " + msg)
}

func (mw *memoryWorker) clearStatus() {
	// no-op: status is only logged, not written to terminal
}

// parseMemoryResponse splits the LLM response into daily notes and long-term facts.
// Handles variations LLMs may produce: "## DAILY", "## Daily", "##DAILY", "# DAILY", etc.
func parseMemoryResponse(response string) (daily string, longTerm string) {
	upper := strings.ToUpper(response)

	// Find section markers (case-insensitive, tolerant of spacing)
	dailyIdx := findSectionIndex(upper, "DAILY")
	longTermIdx := findSectionIndex(upper, "LONGTERM")
	if longTermIdx < 0 {
		longTermIdx = findSectionIndex(upper, "LONG-TERM")
	}
	if longTermIdx < 0 {
		longTermIdx = findSectionIndex(upper, "LONG_TERM")
	}

	// Extract content after the header line
	extractAfter := func(idx int) string {
		// Find the end of the header line
		nlIdx := strings.Index(response[idx:], "\n")
		if nlIdx < 0 {
			return ""
		}
		return strings.TrimSpace(response[idx+nlIdx+1:])
	}

	switch {
	case dailyIdx >= 0 && longTermIdx >= 0:
		if dailyIdx < longTermIdx {
			// DAILY comes first: content between DAILY header and LONGTERM marker
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
		// No markers — treat everything as daily note
		daily = response
	}

	// Filter out NOTHING_NEW from both sections
	if isNothingNew(daily) {
		daily = ""
	}
	if isNothingNew(longTerm) {
		longTerm = ""
	}

	return daily, longTerm
}

// isNothingNew checks if content is a variation of "NOTHING_NEW" that the LLM
// may produce (with extra whitespace, different casing, underscores, hyphens, dots, etc).
func isNothingNew(s string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(s))
	// Strip trailing punctuation the LLM sometimes adds
	normalized = strings.TrimRight(normalized, ".!,;:")
	normalized = strings.TrimSpace(normalized)
	switch normalized {
	case "NOTHING_NEW", "NOTHING NEW", "NOTHING-NEW", "N/A", "NONE", "NA":
		return true
	}
	return false
}

// findSectionIndex finds a markdown section header like "## KEYWORD" (case-insensitive).
// Tolerates "# KEYWORD", "## KEYWORD", "##KEYWORD", "**KEYWORD**".
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

const memoryExtractionPrompt = `You are a memory annotation system. Analyze this conversation segment and extract annotations.

OUTPUT FORMAT — use EXACTLY these section headers:

## DAILY
Write a brief log of what was done in this segment. Use bullet points. Include:
- Files read, modified or created (with paths)
- Commands executed and their outcomes
- Errors encountered and how they were resolved
- Tasks completed or in progress

## LONGTERM
Write ONLY genuinely new facts that should be remembered permanently. These are:
- Architectural decisions made
- Patterns or conventions discovered/established
- User preferences expressed
- Important file paths or project structure insights
- Technical constraints or gotchas learned
- User data with name, age, location, etc.

RULES:
- If nothing new was learned for LONGTERM, write "NOTHING_NEW" in that section
- If the conversation is trivial (greetings, simple questions), respond with just: NOTHING_NEW
- Keep each bullet to ONE line
- Use exact file paths, never paraphrase
- Do NOT repeat facts already in EXISTING LONG-TERM MEMORY
- Write in the same language the user is using in the conversation
- Be concise — this is metadata, not prose`
