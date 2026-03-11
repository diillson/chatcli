package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// CLIBridge is the minimal interface that the TUI adapter needs from ChatCLI.
// This avoids importing the full cli package (which would create an import cycle).
type CLIBridge interface {
	// LLM
	GetLLMClient() client.LLMClient
	GetHistory() []models.Message
	SetHistory([]models.Message)
	GetMaxTokens() int
	GetContextWindow() int
	CancelCurrentOperation()

	// Metadata
	GetModelName() string
	GetProviderName() string
	GetSessionName() string
	GetWorkingDir() string

	// Commands
	HandleSlashCommand(input string) (shouldExit bool)
	GetCompletions(prefix string) []Completion

	// System prompt building
	BuildTempHistory(userInput, additionalContext string) []models.Message
	ProcessSpecialCommands(input string) (cleanInput, additionalContext string)

	// Lifecycle
	SaveCheckpoint()

	// Agent mode integration
	SetAgentEmitter(emitter interface{})
	GetAgentTasks() []TaskInfo

	// MCP servers
	GetMCPServers() []MCPServerInfo

	// RunAgentLoop runs the ReAct loop for the given query.
	// Returns nil if no agent mode is available (fallback to streaming chat).
	RunAgentLoop(ctx context.Context, query string) error
}

// Adapter implements Backend using a CLIBridge.
type Adapter struct {
	bridge CLIBridge
	mu     sync.Mutex
	cancel context.CancelFunc

	// Cumulative token usage tracking
	totalInputTokens  int
	totalOutputTokens int
	totalCost         float64
}

// NewAdapter creates a new TUI adapter.
func NewAdapter(bridge CLIBridge) *Adapter {
	return &Adapter{bridge: bridge}
}

// SendMessage implements Backend.SendMessage.
func (a *Adapter) SendMessage(ctx context.Context, input string) (<-chan Event, error) {
	ch := make(chan Event, 32)

	go func() {
		defer close(ch)

		// /agent, /coder, /run as hints: route through ReAct loop
		if query, ok := extractHint(input, "/agent "); ok {
			a.runAgentLoop(ctx, query, ch)
			return
		}
		if query, ok := extractHint(input, "/run "); ok {
			a.runAgentLoop(ctx, query, ch)
			return
		}
		if query, ok := extractHint(input, "/coder "); ok {
			a.runAgentLoop(ctx, query, ch)
			return
		}

		// Handle /clear locally — clear TUI viewport messages
		if input == "/clear" || input == "/reset" || input == "/redraw" {
			ch <- Event{Type: EventClear}
			ch <- Event{Type: EventDone}
			return
		}

		// Handle /rewind locally — remove last exchange from viewport
		if input == "/rewind" {
			ch <- Event{Type: EventRewind}
			ch <- Event{Type: EventDone}
			return
		}

		// Handle slash commands
		if strings.HasPrefix(input, "/") {
			a.handleCommand(input, ch)
			return
		}

		// Regular chat (including @file, @command, @url — processed by sendToLLM)
		a.sendToLLM(ctx, input, ch)
	}()

	return ch, nil
}

func (a *Adapter) runAgentLoop(ctx context.Context, query string, ch chan<- Event) {
	// Set TUI emitter so agent output renders in the viewport
	a.bridge.SetAgentEmitter(NewTUIEmitter(ch))

	// Create cancellable context
	agentCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.cancel = nil
		a.mu.Unlock()
		cancel()
	}()

	// Add user message to history
	history := a.bridge.GetHistory()
	history = append(history, models.Message{Role: "user", Content: query})
	a.bridge.SetHistory(history)

	// Run the ReAct loop — all output goes through the TUIEmitter → ch
	if err := a.bridge.RunAgentLoop(agentCtx, query); err != nil {
		if err.Error() != "context canceled" {
			ch <- Event{Type: EventError, Error: fmt.Errorf("agent error: %w", err)}
		}
	}
	ch <- Event{Type: EventDone}
}

func (a *Adapter) handleCommand(input string, ch chan<- Event) {
	// Capture stdout during command execution
	var shouldExit bool
	output := captureStdout(func() {
		shouldExit = a.bridge.HandleSlashCommand(input)
	})

	if output != "" {
		ch <- Event{Type: EventTextDelta, Text: output}
	}
	if shouldExit {
		ch <- Event{Type: EventExit}
	}
	ch <- Event{Type: EventDone}
}

func (a *Adapter) sendToLLM(ctx context.Context, input string, ch chan<- Event) {
	a.bridge.SaveCheckpoint()

	// Set TUI emitter on agent mode so tool output renders in the viewport
	a.bridge.SetAgentEmitter(NewTUIEmitter(ch))

	// Process @file, !command, etc. in the input
	cleanInput, additionalContext := a.bridge.ProcessSpecialCommands(input)

	// Build temp history with system prompt + context + history + user message
	tempHistory := a.bridge.BuildTempHistory(cleanInput, additionalContext)
	maxTokens := a.bridge.GetMaxTokens()

	// Create cancellable context
	streamCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.cancel = nil
		a.mu.Unlock()
		cancel()
	}()

	fullInput := cleanInput
	if additionalContext != "" {
		fullInput += additionalContext
	}

	// Try streaming first
	llmClient := a.bridge.GetLLMClient()
	sc := client.AsStreamingClient(llmClient)

	streamCh, err := sc.SendPromptStream(streamCtx, fullInput, tempHistory, maxTokens)
	if err != nil {
		ch <- Event{Type: EventError, Error: fmt.Errorf("LLM error: %w", err)}
		return
	}

	// Forward streaming chunks as events
	var responseBuf strings.Builder
	for chunk := range streamCh {
		if chunk.Error != nil {
			ch <- Event{Type: EventError, Error: chunk.Error}
			return
		}
		if chunk.Text != "" {
			ch <- Event{Type: EventTextDelta, Text: chunk.Text}
			responseBuf.WriteString(chunk.Text)
		}
		if chunk.Done {
			var usage *client.UsageInfo
			if chunk.Usage != nil {
				usage = chunk.Usage
				a.accumulateUsage(usage)
			}
			// Update history with the user message and response
			history := a.bridge.GetHistory()
			history = append(history, models.Message{Role: "user", Content: fullInput})
			history = append(history, models.Message{Role: "assistant", Content: responseBuf.String()})
			a.bridge.SetHistory(history)

			ch <- Event{Type: EventDone, Usage: usage}
			return
		}
	}

	// Channel closed without Done event
	if responseBuf.Len() > 0 {
		history := a.bridge.GetHistory()
		history = append(history, models.Message{Role: "user", Content: fullInput})
		history = append(history, models.Message{Role: "assistant", Content: responseBuf.String()})
		a.bridge.SetHistory(history)
	}
	ch <- Event{Type: EventDone}
}

// CancelOperation implements Backend.CancelOperation.
func (a *Adapter) CancelOperation() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	a.bridge.CancelCurrentOperation()
}

// GetHistory implements Backend.GetHistory.
func (a *Adapter) GetHistory() []models.Message {
	return a.bridge.GetHistory()
}

// GetModelName implements Backend.GetModelName.
func (a *Adapter) GetModelName() string {
	return a.bridge.GetModelName()
}

// GetProvider implements Backend.GetProvider.
func (a *Adapter) GetProvider() string {
	return a.bridge.GetProviderName()
}

// GetSessionName implements Backend.GetSessionName.
func (a *Adapter) GetSessionName() string {
	return a.bridge.GetSessionName()
}

// GetWorkingDir implements Backend.GetWorkingDir.
func (a *Adapter) GetWorkingDir() string {
	return a.bridge.GetWorkingDir()
}

// GetCompletions implements Backend.GetCompletions.
func (a *Adapter) GetCompletions(prefix string) []Completion {
	return a.bridge.GetCompletions(prefix)
}

// GetTokenUsage implements Backend.GetTokenUsage.
func (a *Adapter) GetTokenUsage() TokenUsage {
	a.mu.Lock()
	total := a.totalInputTokens + a.totalOutputTokens
	cost := a.totalCost
	a.mu.Unlock()
	return TokenUsage{
		Used:  total,
		Limit: a.bridge.GetContextWindow(),
		Cost:  cost,
	}
}

// accumulateUsage adds token usage from a streaming response.
func (a *Adapter) accumulateUsage(usage *client.UsageInfo) {
	if usage == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.totalInputTokens += usage.InputTokens
	a.totalOutputTokens += usage.OutputTokens
	// Rough cost estimate (Sonnet pricing as default)
	a.totalCost += float64(usage.InputTokens) * 3.0 / 1_000_000   // $3/M input
	a.totalCost += float64(usage.OutputTokens) * 15.0 / 1_000_000  // $15/M output
	if usage.CacheRead > 0 {
		a.totalCost += float64(usage.CacheRead) * 0.3 / 1_000_000  // $0.30/M cache read
	}
}

// GetModifiedFiles implements Backend.GetModifiedFiles.
// Runs git diff --numstat to get file changes in the working directory.
func (a *Adapter) GetModifiedFiles() []FileChange {
	cwd := a.bridge.GetWorkingDir()
	return getGitModifiedFiles(cwd)
}

// GetMCPServers implements Backend.GetMCPServers.
func (a *Adapter) GetMCPServers() []MCPServer {
	infos := a.bridge.GetMCPServers()
	if len(infos) == 0 {
		return nil
	}
	servers := make([]MCPServer, len(infos))
	for i, info := range infos {
		servers[i] = MCPServer{
			Name:      info.Name,
			Connected: info.Connected,
			ToolCount: info.ToolCount,
		}
	}
	return servers
}

// GetTasks implements Backend.GetTasks.
func (a *Adapter) GetTasks() []Task {
	infos := a.bridge.GetAgentTasks()
	if len(infos) == 0 {
		return nil
	}
	tasks := make([]Task, len(infos))
	for i, info := range infos {
		// Map agent status to sidebar-friendly status
		status := info.Status
		switch status {
		case "in_progress":
			status = "running"
		case "completed":
			status = "done"
		case "failed":
			status = "error"
		}
		tasks[i] = Task{Description: info.Description, Status: status}
	}
	return tasks
}

// extractHint checks if input starts with a hint prefix (e.g., "/agent ")
// and returns the query portion. Returns ("", false) if no match.
func extractHint(input, prefix string) (string, bool) {
	if strings.HasPrefix(input, prefix) {
		query := strings.TrimSpace(strings.TrimPrefix(input, prefix))
		if query != "" {
			return query, true
		}
	}
	return "", false
}

// captureStdout captures stdout during fn execution and returns it as a string.
func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}
	os.Stdout = w

	outCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		outCh <- buf.String()
	}()

	fn()

	_ = w.Close()
	os.Stdout = old
	return <-outCh
}
