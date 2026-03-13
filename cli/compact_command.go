package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// handleCompactCommand handles /compact [instruction].
// Without instruction: runs automatic compaction.
// With instruction: runs guided compaction preserving what the user specifies.
func (cli *ChatCLI) handleCompactCommand(userInput string) {
	instruction := strings.TrimSpace(strings.TrimPrefix(userInput, "/compact"))

	if cli.Client == nil {
		fmt.Println(colorize("  "+i18n.T("compact.error.no_provider"), ColorYellow))
		return
	}

	if len(cli.history) < 4 {
		fmt.Println(colorize("  "+i18n.T("compact.error.history_too_short"), ColorGray))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if instruction == "" {
		// Automatic compaction
		cfg := DefaultCompactConfig(cli.Provider, cli.Model)
		cfg.BudgetRatio = 0.50 // more aggressive for explicit /compact
		compacted, err := cli.historyCompactor.Compact(ctx, cli.history, cli.Client, cfg)
		if err != nil {
			fmt.Println(colorize(fmt.Sprintf("  %s", i18n.T("compact.error.failed", err)), ColorYellow))
			return
		}

		before := len(cli.history)
		cli.history = compacted
		after := len(cli.history)
		fmt.Printf("  %s %s\n",
			colorize("📦", ""), i18n.T("compact.success", before, after))
		return
	}

	// Guided compaction: use LLM to summarize with user's instruction
	cli.guidedCompact(ctx, instruction)
}

// guidedCompact uses the LLM to summarize conversation history while
// preserving what the user explicitly requests.
func (cli *ChatCLI) guidedCompact(ctx context.Context, instruction string) {
	// Find boundaries: keep system messages and a few recent messages verbatim
	systemEnd := 0
	for i, msg := range cli.history {
		if msg.Role == "system" && i == systemEnd {
			systemEnd = i + 1
		} else {
			break
		}
	}

	minKeepRecent := 4
	recentStart := len(cli.history) - minKeepRecent
	if recentStart <= systemEnd {
		fmt.Println(colorize("  "+i18n.T("compact.error.not_enough_with_instruction"), ColorGray))
		return
	}

	middleMessages := cli.history[systemEnd:recentStart]
	if len(middleMessages) < 2 {
		fmt.Println(colorize("  "+i18n.T("compact.error.not_enough_middle"), ColorGray))
		return
	}

	// Build conversation text for the summarizer
	var sb strings.Builder
	for _, msg := range middleMessages {
		content := msg.Content
		if len(content) > 2000 {
			content = content[:1500] + "\n... [truncated] ...\n" + content[len(content)-300:]
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n\n", msg.Role, content))
	}

	prompt := fmt.Sprintf(`You are a conversation compactor. Summarize the conversation below into a concise structured note.

USER INSTRUCTION: %s

Follow the user's instruction about what to preserve. Everything the user says to keep MUST appear in your output.
Drop everything else. Be concise but complete for the preserved topics.

OUTPUT FORMAT:
- Use markdown headers for major topics
- Use bullet points for details
- Include exact file paths, code references, and technical specifics when relevant
- Do NOT add information that is not in the conversation

CONVERSATION TO COMPACT:

%s`, instruction, sb.String())

	summaryHistory := []models.Message{
		{Role: "user", Content: prompt},
	}

	fmt.Printf("  %s %s %s\n",
		colorize("📦", ""),
		i18n.T("compact.compacting_with_instruction"),
		colorize(instruction, ColorCyan),
	)

	response, err := cli.Client.SendPrompt(ctx, prompt, summaryHistory, 0)
	// Auto-retry on OAuth token expiration (401)
	if cli.refreshClientOnAuthError(err) {
		response, err = cli.Client.SendPrompt(ctx, prompt, summaryHistory, 0)
	}
	if err != nil {
		cli.logger.Warn("Guided compaction failed", zap.Error(err))
		fmt.Println(colorize(fmt.Sprintf("  %s", i18n.T("compact.error.failed", err)), ColorYellow))
		return
	}

	// Reconstruct: system + summary + recent
	before := len(cli.history)
	result := make([]models.Message, 0, systemEnd+1+minKeepRecent)
	result = append(result, cli.history[:systemEnd]...)
	result = append(result, models.Message{
		Role:    "user",
		Content: fmt.Sprintf("[COMPACTED CONTEXT — %d messages summarized per user instruction: \"%s\"]\n\n%s", len(middleMessages), instruction, response),
		Meta: &models.MessageMeta{
			IsSummary: true,
			SummaryOf: len(middleMessages),
		},
	})
	result = append(result, cli.history[recentStart:]...)

	cli.history = result
	after := len(cli.history)

	fmt.Printf("  %s %s (%s: %s)\n",
		colorize("✓", ColorGreen),
		i18n.T("compact.success", before, after),
		i18n.T("compact.preserved"),
		colorize(instruction, ColorCyan),
	)
}
