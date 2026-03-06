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
	memoryExtractTimeout = 30 * time.Second
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

	mw.mu.Lock()
	mw.lastProcessedIdx = historyLen
	mw.lastRunTime = time.Now()
	mw.mu.Unlock()

	if err != nil {
		mw.logger.Warn("Memory worker: extraction failed", zap.Error(err))
	} else {
		mw.logger.Debug("Memory worker: annotations saved successfully")
		// Invalidate context builder cache so next prompt picks up new memory
		if mw.cli.contextBuilder != nil {
			mw.cli.contextBuilder.InvalidateCache()
		}
	}

	mw.clearStatus()
}

func (mw *memoryWorker) extractAndSave(messages []models.Message) error {
	if mw.cli.Client == nil || mw.cli.memoryStore == nil {
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

	extractPrompt := memoryExtractionPrompt
	if existingMemory != "" {
		extractPrompt += "\n\nEXISTING LONG-TERM MEMORY (do NOT duplicate these facts):\n\n" + existingMemory
	}
	extractPrompt += "\n\nCONVERSATION SEGMENT TO ANALYZE:\n\n" + sb.String()

	ctx, cancel := context.WithTimeout(context.Background(), memoryExtractTimeout)
	defer cancel()

	history := []models.Message{
		{Role: "user", Content: extractPrompt},
	}

	response, err := mw.cli.Client.SendPrompt(ctx, extractPrompt, history, 0)
	if err != nil {
		return fmt.Errorf("memory extraction LLM call failed: %w", err)
	}

	response = strings.TrimSpace(response)
	if response == "" || response == "NOTHING_NEW" {
		return nil
	}

	// Parse response: split into DAILY and LONGTERM sections
	dailyContent, longTermContent := parseMemoryResponse(response)

	// Write daily note
	if dailyContent != "" {
		if err := mw.cli.memoryStore.WriteDailyNote(dailyContent); err != nil {
			mw.logger.Warn("Failed to write daily note", zap.Error(err))
		}
	}

	// Append to long-term memory (only genuinely new facts)
	if longTermContent != "" {
		if err := mw.cli.memoryStore.AppendLongTerm("\n" + longTermContent); err != nil {
			mw.logger.Warn("Failed to append long-term memory", zap.Error(err))
		}
	}

	return nil
}

// showStatus shows a subtle gray message (non-blocking, no newline disruption).
func (mw *memoryWorker) showStatus(msg string) {
	// Only show if user is idle (not mid-execution)
	if mw.cli.isExecuting.Load() {
		return
	}
	fmt.Printf("\r  %s", colorize("⟳ "+msg, ColorGray))
}

func (mw *memoryWorker) clearStatus() {
	if mw.cli.isExecuting.Load() {
		return
	}
	// Clear the status line
	fmt.Print("\r\033[K")
}

// parseMemoryResponse splits the LLM response into daily notes and long-term facts.
func parseMemoryResponse(response string) (daily string, longTerm string) {
	// Look for section markers
	dailyIdx := strings.Index(response, "## DAILY")
	longTermIdx := strings.Index(response, "## LONGTERM")

	switch {
	case dailyIdx >= 0 && longTermIdx >= 0:
		if dailyIdx < longTermIdx {
			daily = strings.TrimSpace(response[dailyIdx+len("## DAILY"):longTermIdx])
			longTerm = strings.TrimSpace(response[longTermIdx+len("## LONGTERM"):])
		} else {
			longTerm = strings.TrimSpace(response[longTermIdx+len("## LONGTERM"):dailyIdx])
			daily = strings.TrimSpace(response[dailyIdx+len("## DAILY"):])
		}
	case dailyIdx >= 0:
		daily = strings.TrimSpace(response[dailyIdx+len("## DAILY"):])
	case longTermIdx >= 0:
		longTerm = strings.TrimSpace(response[longTermIdx+len("## LONGTERM"):])
	default:
		// No markers — treat everything as daily note
		daily = response
	}

	return daily, longTerm
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

RULES:
- If nothing new was learned for LONGTERM, write "NOTHING_NEW" in that section
- If the conversation is trivial (greetings, simple questions), respond with just: NOTHING_NEW
- Keep each bullet to ONE line
- Use exact file paths, never paraphrase
- Do NOT repeat facts already in EXISTING LONG-TERM MEMORY
- Write in the same language the user is using in the conversation
- Be concise — this is metadata, not prose`
