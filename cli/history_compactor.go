package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// CompactStage identifies which level of the pipeline is running.
// Used by the UI to render meaningful progress messages instead of a
// generic "Processando..." while the compactor is working.
type CompactStage string

const (
	CompactStageStart     CompactStage = "start"
	CompactStageTrim      CompactStage = "trim"
	CompactStageSummarize CompactStage = "summarize"
	CompactStageEmergency CompactStage = "emergency"
	CompactStageDone      CompactStage = "done"
)

// StatusCallback is invoked by the compactor at the start/end of each level
// so callers can update spinners, status bars, or animation messages. It must
// be non-blocking and safe to call from any goroutine.
type StatusCallback func(stage CompactStage, msg string)

// HistoryCompactor manages conversation history size through a 3-level pipeline:
//
//	Level 1: Near-lossless trimming (strip reasoning, compact XML, dedup)
//	Level 2: Structured summarization (extract facts, not prose)
//	Level 3: Emergency truncation (last resort)
type HistoryCompactor struct {
	logger   *zap.Logger
	trimmer  *MessageTrimmer
	compress *compress.Layer
	statusMu sync.RWMutex
	onStatus StatusCallback
}

// CompactConfig holds parameters for a compaction operation.
type CompactConfig struct {
	Provider      string
	Model         string
	BudgetRatio   float64 // fraction of context window to use (default 0.75)
	MinKeepRecent int     // minimum recent messages to keep verbatim (default 10)
	CharsPerToken int     // character-to-token ratio estimate (default 4)
	// CharsPerTokenPrecise, when > 0, is the learned chars-per-token ratio
	// for the provider/model (see tokenCalibrator) and takes precedence
	// over the integer default in the budget math.
	CharsPerTokenPrecise float64

	// ReservedChars is request weight that lives OUTSIDE the history slice
	// (native tool definitions, wire overhead) and still counts against the
	// model window. Subtracted from the history budget, floored at a quarter
	// of it so a huge tool catalog can never starve the conversation.
	ReservedChars int

	// SummarizerClient, when set, serves the Level 2 structured summary
	// instead of the session client — a cheaper/faster model configured via
	// CHATCLI_COMPACT_MODEL. Nil keeps the session client.
	SummarizerClient client.LLMClient
	// SummarizerProvider/SummarizerModel name the summarizer's route so its
	// input can be sized against its own window (empty = session model).
	SummarizerProvider string
	SummarizerModel    string

	// ExternalSummarizer, when set, is the context engine that produces
	// the Level 2 summary (CHATCLI_CONTEXT_ENGINE); an error or empty
	// output falls back to the embedded summarizer.
	ExternalSummarizer ExternalSummarizer

	// MaxPayloadBytes caps the serialized request body size in bytes.
	// When > 0, overrides the context-window budget if it would yield
	// a larger payload than the corporate proxy / gateway accepts
	// (many enterprise proxies cap POST bodies at 1-5 MB). 0 disables
	// the cap. Honors env CHATCLI_MAX_PAYLOAD (human-friendly: "5MB",
	// "512KB", "5"=5MB when unit is omitted).
	MaxPayloadBytes int
}

// ParsePayloadSize accepts human-friendly size strings and returns bytes.
// A bare number is interpreted as MB (the most common unit users think
// in for proxy caps). Explicit suffixes: B, KB/K, MB/M, GB/G (case
// insensitive, whitespace tolerated: "5 MB" works). Returns 0 for any
// non-positive or unparseable input.
func ParsePayloadSize(s string) int {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}

	var mult int64 = 1024 * 1024 // default unit: MB
	switch {
	case strings.HasSuffix(s, "GB"):
		s, mult = strings.TrimSuffix(s, "GB"), 1024*1024*1024
	case strings.HasSuffix(s, "MB"):
		s, mult = strings.TrimSuffix(s, "MB"), 1024*1024
	case strings.HasSuffix(s, "KB"):
		s, mult = strings.TrimSuffix(s, "KB"), 1024
	case strings.HasSuffix(s, "G"):
		s, mult = strings.TrimSuffix(s, "G"), 1024*1024*1024
	case strings.HasSuffix(s, "M"):
		s, mult = strings.TrimSuffix(s, "M"), 1024*1024
	case strings.HasSuffix(s, "K"):
		s, mult = strings.TrimSuffix(s, "K"), 1024
	case strings.HasSuffix(s, "B"):
		s, mult = strings.TrimSuffix(s, "B"), 1
	}
	s = strings.TrimSpace(s)

	// Support fractional sizes like "2.5MB"
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
		return int(f * float64(mult))
	}
	return 0
}

// FormatPayloadSize returns a human-readable size for display.
func FormatPayloadSize(bytes int) string {
	switch {
	case bytes >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// DefaultCompactConfig returns sensible defaults for chat mode.
func DefaultCompactConfig(provider, model string) CompactConfig {
	cfg := CompactConfig{
		Provider:      provider,
		Model:         model,
		BudgetRatio:   0.75,
		MinKeepRecent: 10,
		CharsPerToken: 4,
	}
	// Catalog-declared threshold for this model (the session /autocompact
	// override is applied by the caller and wins).
	if r := catalog.GetCompactRatio(provider, model); r > 0 {
		cfg.BudgetRatio = r
	}
	// Learned ratio from the provider's own token counts, when a session
	// has produced one — the budget then measures what the model measures.
	if ratio, samples := globalTokenCalibrator.CharsPerToken(provider, model); samples > 0 {
		cfg.CharsPerTokenPrecise = ratio
	}
	// Human-friendly env: CHATCLI_MAX_PAYLOAD=5MB / 512KB / 2.5MB.
	if v := os.Getenv("CHATCLI_MAX_PAYLOAD"); v != "" {
		if n := ParsePayloadSize(v); n > 0 {
			cfg.MaxPayloadBytes = n
		}
	}
	return cfg
}

// NewHistoryCompactor creates a new HistoryCompactor with its embedded trimmer.
func NewHistoryCompactor(logger *zap.Logger) *HistoryCompactor {
	return &HistoryCompactor{
		logger:  logger,
		trimmer: NewMessageTrimmer(logger),
	}
}

// SetCompressionLayer wires the content-aware compression layer into the
// embedded trimmer so oversized tool feedback and injected context are reduced
// reversibly (CCR) during compaction instead of being byte-truncated.
func (hc *HistoryCompactor) SetCompressionLayer(l *compress.Layer) {
	hc.compress = l
	if hc.trimmer != nil {
		hc.trimmer.SetCompressionLayer(l)
	}
}

// SetStatusCallback registers a progress callback for UI feedback.
// Pass nil to clear. Safe to call concurrently.
func (hc *HistoryCompactor) SetStatusCallback(cb StatusCallback) {
	hc.statusMu.Lock()
	hc.onStatus = cb
	hc.statusMu.Unlock()
}

// emitStatus invokes the current status callback (if any). Never panics
// if the callback is nil or the caller has cleared it mid-flight.
func (hc *HistoryCompactor) emitStatus(stage CompactStage, msg string) {
	hc.statusMu.RLock()
	cb := hc.onStatus
	hc.statusMu.RUnlock()
	if cb != nil {
		cb(stage, msg)
	}
}

// CharBudget returns the character budget based on the model's context window,
// additionally capped by MaxPayloadBytes if set (corporate-proxy scenarios).
// A safety factor leaves headroom for JSON overhead, system prompt and tools.
func (hc *HistoryCompactor) CharBudget(cfg CompactConfig) int {
	contextWindow := catalog.GetContextWindow(cfg.Provider, cfg.Model)
	tokenBudget := int(float64(contextWindow) * cfg.BudgetRatio)
	charsPerToken := float64(cfg.CharsPerToken)
	if cfg.CharsPerTokenPrecise > 0 {
		charsPerToken = cfg.CharsPerTokenPrecise
	}
	budget := int(float64(tokenBudget) * charsPerToken)
	if cfg.ReservedChars > 0 {
		floor := budget / 4
		if budget-cfg.ReservedChars < floor {
			budget = floor
		} else {
			budget -= cfg.ReservedChars
		}
	}

	// Proxy / gateway payload cap. Leave 30% headroom for system prompt,
	// tool definitions and JSON serialization overhead.
	if cfg.MaxPayloadBytes > 0 {
		payloadBudget := int(float64(cfg.MaxPayloadBytes) * 0.7)
		if payloadBudget < budget {
			budget = payloadBudget
		}
	}
	return budget
}

// weightCharsPerToken converts token-priced payload (images) back into the
// same character currency the budget math uses. Keep in lock-step with
// CompactConfig.CharsPerToken's default (DefaultCompactConfig): the budget
// multiplies tokens by 4 to get chars, so token-priced weight must do the same.
const weightCharsPerToken = 4

// messageWeight returns the request-payload weight of a message in characters.
// It is a proxy for what the message actually costs on the wire, not an exact
// token count: besides Content it charges native tool-call arguments and
// vision input, which len(Content) alone is blind to. A tool-heavy or
// image-heavy history must trigger compaction just like a text-heavy one.
func messageWeight(msg models.Message) int {
	w := len(msg.Content)
	for _, tc := range msg.ToolCalls {
		w += len(tc.Name) + len(tc.ArgumentsJSON())
	}
	for _, img := range msg.Images {
		w += models.EstimateImageTokens(img) * weightCharsPerToken
	}
	// SystemParts normally duplicate Content (Content is the flattened join
	// of the parts), so charge only the excess — max(Content, parts), never
	// the sum of both.
	if len(msg.SystemParts) > 0 {
		partsSum := 0
		for _, p := range msg.SystemParts {
			partsSum += len(p.Text)
		}
		if partsSum > len(msg.Content) {
			w += partsSum - len(msg.Content)
		}
	}
	return w
}

// totalChars sums the payload weight of all messages. See messageWeight for
// what counts toward the weight beyond len(Content).
func totalChars(history []models.Message) int {
	total := 0
	for _, msg := range history {
		total += messageWeight(msg)
	}
	return total
}

// NeedsCompaction returns true if the total character count exceeds the budget.
func (hc *HistoryCompactor) NeedsCompaction(history []models.Message, cfg CompactConfig) bool {
	return totalChars(history) > hc.CharBudget(cfg)
}

// Compact runs the 3-level compaction pipeline.
// Each level is progressively more aggressive. Most of the time, Level 1 (trim) suffices.
func (hc *HistoryCompactor) Compact(
	ctx context.Context,
	history []models.Message,
	llmClient client.LLMClient,
	cfg CompactConfig,
) ([]models.Message, error) {
	budget := hc.CharBudget(cfg)
	before := totalChars(history)
	beforeMsgs := len(history)

	hc.logger.Info("History compaction triggered",
		zap.Int("budget_chars", budget),
		zap.Int("current_chars", before),
		zap.Int("messages", beforeMsgs),
		zap.Int("max_payload_bytes", cfg.MaxPayloadBytes),
	)
	hc.emitStatus(CompactStageStart, i18n.T("compact.status.start",
		beforeMsgs, FormatPayloadSize(before), FormatPayloadSize(budget)))

	// LEVEL 1: Near-lossless trimming — pure Go, no network
	hc.emitStatus(CompactStageTrim, i18n.T("compact.status.trim"))
	history = hc.trimmer.TrimHistory(history)
	current := totalChars(history)
	if current <= budget {
		hc.logger.Info("Level 1 (trim) sufficient",
			zap.Int("before_chars", before),
			zap.Int("after_chars", current),
		)
		hc.emitStatus(CompactStageDone, i18n.T("compact.status.trim_sufficient",
			FormatPayloadSize(before), FormatPayloadSize(current)))
		return history, nil
	}

	// LEVEL 2: Structured summarization of old messages (requires LLM call)
	hc.emitStatus(CompactStageSummarize, i18n.T("compact.status.summarize"))
	summarized, err := hc.structuredSummarize(ctx, history, llmClient, cfg)
	if err != nil {
		// A cancellation from the user should propagate, not silently fall
		// through to emergency truncation — the user's own choice to abort
		// must not mangle their history.
		if ctx.Err() != nil {
			hc.emitStatus(CompactStageDone, i18n.T("compact.status.canceled"))
			return nil, ctx.Err()
		}
		hc.logger.Warn("Level 2 (summarization) failed, falling back",
			zap.Error(err),
		)
		hc.emitStatus(CompactStageSummarize,
			i18n.T("compact.status.summarize_failed", err))
	} else {
		history = summarized
		current = totalChars(history)
		if current <= budget {
			hc.logger.Info("Level 2 (structured summarization) sufficient",
				zap.Int("before_chars", before),
				zap.Int("after_chars", current),
				zap.Int("before_msgs", beforeMsgs),
				zap.Int("after_msgs", len(history)),
			)
			hc.emitStatus(CompactStageDone, i18n.T("compact.status.summarize_applied",
				beforeMsgs, len(history), FormatPayloadSize(before), FormatPayloadSize(current)))
			return history, nil
		}
	}

	// LEVEL 3: Emergency truncation (last resort)
	hc.emitStatus(CompactStageEmergency, i18n.T("compact.status.emergency"))
	history = hc.emergencyTruncate(history, cfg)

	// Whole-message dropping is a no-op when the history is short (system +
	// a handful of huge tool results) — exactly the shape that trips
	// proxy/WAF payload caps. Shrink message CONTENT until the budget is
	// met; system messages are never touched.
	if totalChars(history) > budget {
		history = shrinkToBudget(history, budget, hc.compress)
	}
	hc.logger.Warn("Level 3 (emergency truncation) used",
		zap.Int("before_chars", before),
		zap.Int("after_chars", totalChars(history)),
		zap.Int("before_msgs", beforeMsgs),
		zap.Int("after_msgs", len(history)),
	)
	hc.emitStatus(CompactStageDone, i18n.T("compact.status.truncated", beforeMsgs, len(history)))

	return history, nil
}

// structuredSummarize summarizes the "middle" block of messages using
// a structured fact-extraction prompt.
func (hc *HistoryCompactor) structuredSummarize(
	ctx context.Context,
	history []models.Message,
	llmClient client.LLMClient,
	cfg CompactConfig,
) ([]models.Message, error) {
	// Find boundaries: [system messages | middle (to summarize) | recent (keep verbatim)]
	systemEnd := 0
	for i, msg := range history {
		if msg.Role == "system" && i == systemEnd {
			systemEnd = i + 1
		} else {
			break
		}
	}

	recentStart := len(history) - cfg.MinKeepRecent
	recentStart = snapToToolBlockBoundary(history, recentStart, systemEnd)
	if recentStart <= systemEnd {
		// Not enough messages to split — nothing to summarize
		return history, nil
	}

	middleMessages := history[systemEnd:recentStart]
	if len(middleMessages) < 4 {
		return history, nil
	}

	// Build input for the summarizer: budgeted against the summarizer's
	// own window, with CCR stubs restored to their originals when they fit
	// (the summary then extracts from what actually happened, not from a
	// one-line stub).
	segment := renderSegmentForSummary(hc.compress, middleMessages, summarizerInputBudget(cfg))

	prompt := structuredSummaryPrompt + "\n\nCONVERSATION SEGMENT TO EXTRACT FROM:\n\n" + segment

	summaryHistory := []models.Message{
		{Role: "user", Content: prompt},
	}

	// Derive from parent ctx so that a user-initiated cancel (Ctrl+C / ESC)
	// propagates and aborts the long summarization. We add our OWN generous
	// timeout (10 min) to protect against ambient turn-level deadlines that
	// might be shorter than the summary LLM call.
	summarizeCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	summarizer := llmClient
	if cfg.SummarizerClient != nil {
		summarizer = cfg.SummarizerClient
	}
	var response string
	var err error
	if cfg.ExternalSummarizer != nil {
		response, err = cfg.ExternalSummarizer(summarizeCtx, segment, summarizerInputBudget(cfg), "")
		if err != nil && hc.logger != nil {
			hc.logger.Warn("context engine failed; falling back to the embedded summarizer", zap.Error(err))
		}
	}
	if strings.TrimSpace(response) == "" {
		response, err = summarizer.SendPrompt(summarizeCtx, prompt, summaryHistory, 0)
		if err != nil {
			return nil, fmt.Errorf("structured summarization LLM call failed: %w", err)
		}
	}

	// Archive the FULL middle segment (untruncated, unlike the summarizer
	// input above) before replacing it: level 1 (trim) and level 3
	// (emergency) already preserve what they drop via CCR, and this was the
	// pipeline's only irreversible cut. Best-effort — a disabled layer or an
	// over-cap segment keeps the legacy lossy behavior.
	recallNote := ""
	if key, ok := hc.compress.Archive(renderMessagesForArchive(middleMessages)); ok {
		recallNote = "\n\n[full transcript of the summarized segment recoverable via @recall " +
			compress.FormatMarker(key) + "]"
	}

	// Reconstruct: system + summary message + recent messages
	result := make([]models.Message, 0, systemEnd+1+cfg.MinKeepRecent)
	result = append(result, history[:systemEnd]...)
	result = append(result, models.Message{
		Role:    "user",
		Content: fmt.Sprintf("[STRUCTURED SUMMARY — covering %d earlier messages]\n\n%s%s", len(middleMessages), response, recallNote),
		Meta: &models.MessageMeta{
			IsSummary: true,
			SummaryOf: len(middleMessages),
		},
	})
	result = append(result, history[recentStart:]...)

	return result, nil
}

// Summarizer input sizing: the segment text may take this share of the
// summarizer model's window (chars), every message gets a fair allowance
// of it (never below summaryMinPerMessage), and an over-long message keeps
// its head and tail in summaryHeadShare proportion.
const (
	summaryInputShare    = 0.5
	summaryInputFloor    = 20000
	summaryMinPerMessage = 600
	summaryHeadShare     = 0.75
	summaryCutMarker     = "\n... [truncated for summarization] ...\n"
)

// summarizerInputBudget is the character budget for the segment text sent
// to the summarizer: sized against the summarizer's window when one is
// configured, the session model's otherwise.
func summarizerInputBudget(cfg CompactConfig) int {
	provider, model := cfg.SummarizerProvider, cfg.SummarizerModel
	if provider == "" || model == "" {
		provider, model = cfg.Provider, cfg.Model
	}
	window := catalog.GetContextWindow(provider, model)
	cpt := float64(cfg.CharsPerToken)
	if cfg.CharsPerTokenPrecise > 0 {
		cpt = cfg.CharsPerTokenPrecise
	}
	if cpt <= 0 {
		cpt = 4
	}
	budget := int(float64(window)*summaryInputShare*cpt) - len(structuredSummaryPrompt) - 2000
	if budget < summaryInputFloor {
		budget = summaryInputFloor
	}
	return budget
}

// renderSegmentForSummary renders a message segment for the summarizer
// within budget. Each message gets budget/len (at least
// summaryMinPerMessage); a message that is a CCR stub is restored from the
// archive when the original fits its allowance; anything longer keeps its
// head and tail. Native tool calls are named so the summary can list the
// commands executed.
func renderSegmentForSummary(layer *compress.Layer, msgs []models.Message, budget int) string {
	if len(msgs) == 0 {
		return ""
	}
	allowance := budget / len(msgs)
	if allowance < summaryMinPerMessage {
		allowance = summaryMinPerMessage
	}
	var sb strings.Builder
	for _, msg := range msgs {
		content := restoreStubForSummary(layer, msg.Content, allowance)
		if len(content) > allowance {
			head := int(float64(allowance) * summaryHeadShare)
			tail := allowance - head - len(summaryCutMarker)
			if tail < 0 {
				tail = 0
			}
			content = content[:head] + summaryCutMarker + content[len(content)-tail:]
		}
		if len(msg.ToolCalls) > 0 {
			names := make([]string, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				names = append(names, tc.Name)
			}
			content += "\n[tool_calls: " + strings.Join(names, ", ") + "]"
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n\n", msg.Role, content))
	}
	return sb.String()
}

// restoreStubForSummary swaps a CCR stub for the archived original when the
// original fits the allowance; otherwise the stub (which already carries a
// preview) stands.
func restoreStubForSummary(layer *compress.Layer, content string, allowance int) string {
	if layer == nil {
		return content
	}
	for _, key := range compress.ExtractKeys(content) {
		full, ok := layer.Recall(key)
		if ok && len(full) > len(content) && len(full) <= allowance {
			return full
		}
	}
	return content
}

// renderMessagesForArchive renders a message segment for verbatim CCR
// archival: role-tagged, full content, no truncation.
func renderMessagesForArchive(msgs []models.Message) string {
	var sb strings.Builder
	for _, msg := range msgs {
		sb.WriteString("[")
		sb.WriteString(msg.Role)
		sb.WriteString("]: ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// snapToToolBlockBoundary moves a keep-recent cut point so it never splits an
// assistant tool_calls message from its tool results. A cut landing on a
// role:"tool" message would summarize/drop the owning assistant message and
// leave orphan results at the head of the kept tail — which the agent-mode
// pairing repair then deletes (losing the freshest real outputs) and which
// chat-mode WithTools serialization has no repair for at all. The cut is
// moved LEFT to the owning assistant-with-calls message so the whole block
// stays in the verbatim tail; anything interposed between them belongs to
// the block and moves with it.
func snapToToolBlockBoundary(history []models.Message, recentStart, systemEnd int) int {
	if recentStart <= systemEnd || recentStart >= len(history) {
		return recentStart
	}
	if history[recentStart].Role != "tool" {
		return recentStart
	}
	for i := recentStart - 1; i >= systemEnd; i-- {
		if history[i].Role == "assistant" {
			if len(history[i].ToolCalls) > 0 {
				return i
			}
			break // plain assistant: the tool tail is already orphaned upstream
		}
	}
	return recentStart
}

// emergencyTruncate is the last resort: drops middle messages without summarization.
func (hc *HistoryCompactor) emergencyTruncate(history []models.Message, cfg CompactConfig) []models.Message {
	systemEnd := 0
	for i, msg := range history {
		if msg.Role == "system" && i == systemEnd {
			systemEnd = i + 1
		} else {
			break
		}
	}

	keepRecent := cfg.MinKeepRecent
	if keepRecent > len(history)-systemEnd {
		keepRecent = len(history) - systemEnd
	}

	recentStart := len(history) - keepRecent
	recentStart = snapToToolBlockBoundary(history, recentStart, systemEnd)
	if recentStart <= systemEnd {
		return history
	}

	droppedCount := recentStart - systemEnd

	result := make([]models.Message, 0, systemEnd+1+keepRecent)
	result = append(result, history[:systemEnd]...)
	result = append(result, models.Message{
		Role:    "user",
		Content: fmt.Sprintf("[CONTEXT TRUNCATED: %d messages removed due to context window limit. Recent context preserved below.]", droppedCount),
		Meta: &models.MessageMeta{
			IsSummary: true,
			SummaryOf: droppedCount,
		},
	})
	result = append(result, history[recentStart:]...)

	return result
}

// shrinkToBudget hard-truncates the CONTENT of non-system messages, largest
// first, until the total fits the budget. This is the true last resort:
// emergencyTruncate drops whole middle messages, but with a short history
// (agent mode: system prompt + a few huge tool results) there is no middle
// to drop and it returns the input unchanged — the request then goes out
// oversized and a proxy/WAF rejects it again. System messages are never
// touched (they carry the agent charter, skills and tool instructions); if
// they alone exceed the budget, everything else is shrunk to the floor and
// the result is returned as-is — the caller's floor diagnostics handle
// that case.
//
// When a CCR layer is available, each message is archived verbatim before
// its first truncation and the content gains a <<ccr:KEY>> retrieval
// marker, so even this emergency path loses nothing permanently: the model
// can expand any shrunk message later with @recall. The marker rides at
// the tail of the content, which truncatePreservingStructure always keeps.
func shrinkToBudget(history []models.Message, budget int, ccr *compress.Layer) []models.Message {
	// Floor per message: enough to keep tool_use/tool_result pairing and the
	// gist of each exchange meaningful after truncation.
	const floorChars = 400

	total := totalChars(history)
	if total <= budget {
		return history
	}

	result := make([]models.Message, len(history))
	copy(result, history)

	for total > budget {
		// Pick the largest shrinkable (non-system, above-floor) message.
		idx, maxLen := -1, floorChars
		for i, msg := range result {
			if msg.Role == "system" {
				continue
			}
			if len(msg.Content) > maxLen {
				idx, maxLen = i, len(msg.Content)
			}
		}
		if idx < 0 {
			// Only system messages / floor-sized content left. With weighted
			// totals (messageWeight) the residual may still exceed the budget
			// when the bulk is unshrinkable payload — tool-call arguments or
			// images have no Content to cut — so bail out rather than spin.
			break
		}

		// Archive before the lossy cut. Archive refuses content that
		// already carries a marker (a previous round stored the original),
		// so repeated shrinking never duplicates store entries.
		if key, ok := ccr.Archive(result[idx].Content); ok {
			result[idx].Content += "\n[full content recoverable via @recall " +
				compress.FormatMarker(key) + "]"
		}

		// truncatePreservingStructure appends an omission banner on top of
		// the requested length — aim below the deficit so the result lands
		// within budget instead of hovering just above it.
		const bannerSlack = 96
		target := maxLen - (total - budget) - bannerSlack
		if target < floorChars {
			target = floorChars
		}
		result[idx].Content = truncatePreservingStructure(result[idx].Content, target)

		newTotal := totalChars(result)
		if newTotal >= total {
			break // no progress possible (truncation banner overhead)
		}
		total = newTotal
	}
	return result
}

// structuredSummaryPrompt is the prompt template for fact extraction.
// Written in English for best model performance across all providers.
const structuredSummaryPrompt = `You are a precise technical note-taker. Extract ONLY factual information from this conversation segment.

OUTPUT FORMAT (use exactly this structure, omit empty sections):

## Files Read
- <path> (<line count> lines) - <one-line description of content/purpose>

## Files Modified
- <path>:<lines> - <exact description of what was changed and why>

## Commands Executed
- <command> → <outcome (success/failure + key output)>

## Key Decisions
- <decision made and rationale>

## Errors & Resolutions
- <error> → <how it was resolved>

## Current Task State
- Done: <what's completed>
- Pending: <what remains>

RULES:
- Include EXACT file paths and line numbers — never paraphrase paths
- Include EXACT error messages (first line only)
- Do NOT paraphrase code — reference by file:line
- Do NOT add information that is not explicitly in the conversation
- If a file was read and then modified, show both entries
- Keep each bullet to ONE line
- If nothing fits a section, omit that section entirely
- Do NOT use code blocks or XML tags in the output`
