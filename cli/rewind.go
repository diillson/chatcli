package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

// conversationCheckpoint stores a snapshot of the conversation at a given point.
type conversationCheckpoint struct {
	Timestamp time.Time
	Label     string           // auto-generated summary of last user message
	History   []models.Message // deep copy of history at checkpoint time
	MsgCount  int              // number of messages at checkpoint
}

const maxCheckpoints = 20

// saveCheckpoint creates a checkpoint of the current conversation state.
// Called automatically before each user message is sent to the LLM.
func (cli *ChatCLI) saveCheckpoint() {
	// Build a short label from the last user message
	label := ""
	for i := len(cli.history) - 1; i >= 0; i-- {
		if cli.history[i].Role == "user" {
			label = cli.history[i].Content
			break
		}
	}
	if label == "" {
		label = "(start)"
	}
	// Truncate label
	label = strings.ReplaceAll(label, "\n", " ")
	if len(label) > 60 {
		label = label[:57] + "..."
	}

	// Deep copy history
	snapshot := make([]models.Message, len(cli.history))
	copy(snapshot, cli.history)

	cp := conversationCheckpoint{
		Timestamp: time.Now(),
		Label:     label,
		History:   snapshot,
		MsgCount:  len(cli.history),
	}

	cli.checkpoints = append(cli.checkpoints, cp)

	// Keep only last N checkpoints
	if len(cli.checkpoints) > maxCheckpoints {
		cli.checkpoints = cli.checkpoints[len(cli.checkpoints)-maxCheckpoints:]
	}
}

// showRewindMenu displays the rewind menu and handles user selection.
// Returns true if a rewind was performed.
func (cli *ChatCLI) showRewindMenu() bool {
	if len(cli.checkpoints) == 0 {
		fmt.Println(colorize("  No checkpoints available yet.", ColorGray))
		return false
	}

	fmt.Println()
	fmt.Println(colorize("  REWIND — Select a checkpoint to restore", ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	for i := len(cli.checkpoints) - 1; i >= 0; i-- {
		cp := cli.checkpoints[i]
		timeStr := cp.Timestamp.Format("15:04:05")
		msgInfo := fmt.Sprintf("%d msgs", cp.MsgCount)
		idx := len(cli.checkpoints) - i

		fmt.Printf("  %s  %s  %s  %s\n",
			colorize(fmt.Sprintf("[%d]", idx), ColorCyan),
			colorize(timeStr, ColorGray),
			colorize(msgInfo, ColorYellow),
			cp.Label,
		)
	}

	fmt.Println()
	fmt.Printf("  %s ", colorize("Select [1-"+fmt.Sprintf("%d", len(cli.checkpoints))+"] or (q)uit:", ColorGray))

	// Read selection via stdin
	var input string
	_, _ = fmt.Scanln(&input)
	input = strings.TrimSpace(input)

	if input == "q" || input == "" {
		fmt.Println(colorize("  Rewind cancelled.", ColorGray))
		return false
	}

	var idx int
	if _, err := fmt.Sscanf(input, "%d", &idx); err != nil || idx < 1 || idx > len(cli.checkpoints) {
		fmt.Println(colorize("  Invalid selection.", ColorYellow))
		return false
	}

	// Map display index to checkpoint index (displayed in reverse)
	cpIdx := len(cli.checkpoints) - idx
	cp := cli.checkpoints[cpIdx]

	// Restore history from checkpoint snapshot
	cli.history = make([]models.Message, len(cp.History))
	copy(cli.history, cp.History)

	// Trim checkpoints to the restored point
	cli.checkpoints = cli.checkpoints[:cpIdx+1]

	fmt.Printf("  %s Rewound to checkpoint [%d] (%s, %d messages)\n",
		colorize("↩", ColorGreen),
		idx,
		cp.Timestamp.Format("15:04:05"),
		cp.MsgCount,
	)

	return true
}
