package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/diillson/chatcli/cli/tui/components"
)

// State represents the TUI state machine.
type State int

const (
	StateIdle      State = iota // waiting for user input
	StateStreaming              // receiving tokens from LLM
	StateToolExec               // executing a tool (ReAct turn)
	StateApproval               // approval overlay visible
)

// RenderedMessage is a message ready for display in the viewport.
type RenderedMessage struct {
	Role    string // "user", "assistant", "tool", "error", "system"
	Content string // rendered text
}

// Model is the root Bubble Tea model.
type Model struct {
	backend Backend
	state   State
	width   int
	height  int
	keys    KeyMap
	ready   bool

	// Sub-components
	input    textarea.Model
	viewport viewport.Model
	header   components.HeaderModel
	footer   components.FooterModel
	sidebar  components.SidebarModel
	spinner  components.SpinnerModel
	toolView components.ToolViewModel

	// State
	sidebarVisible bool
	messages       []RenderedMessage
	streamBuf      strings.Builder  // accumulates streaming text
	eventCh        <-chan Event      // active event channel from backend
	err            error

	// Approval
	pendingApproval *ApprovalRequest // active approval request

	// Autocomplete
	completions     []Completion // current completion candidates
	completionIdx   int          // selected completion index (-1 = none)
	completionShown bool         // is the completion overlay visible

	// Input history
	inputHistory []string // previous inputs
	historyIdx   int      // -1 = current input, 0..n = history entries
	historyDraft string   // draft text before navigating history

	// Double-Esc rewind
	lastEscTime time.Time // timestamp of last Escape press
}

func newModel(backend Backend) Model {
	// Input textarea
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = "❯ "
	ta.CharLimit = 0 // no limit
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(colorSuccess)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.Prompt = lipgloss.NewStyle().Foreground(colorMuted)
	ta.Focus()

	return Model{
		backend:        backend,
		state:          StateIdle,
		keys:           DefaultKeyMap(),
		input:          ta,
		header:         components.NewHeaderModel(),
		footer:         components.NewFooterModel(),
		sidebar:        components.NewSidebarModel(),
		spinner:        components.NewSpinnerModel(),
		toolView:       components.NewToolViewModel(),
		sidebarVisible: true,
		messages:       []RenderedMessage{},
		completionIdx:  -1,
		historyIdx:     -1,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Init(),
	)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.recalcLayout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case startStreamMsg:
		if msg.err != nil {
			m.messages = append(m.messages, RenderedMessage{Role: "error", Content: msg.err.Error()})
			m.state = StateIdle
			m.updateViewportContent()
			return m, nil
		}
		m.eventCh = msg.ch
		return m, listenNext(m.eventCh)

	case BackendEventMsg:
		// Ignore events arriving after cancel (from pending listenNext goroutines)
		if m.state == StateIdle && m.eventCh == nil {
			return m, nil
		}
		return m.handleBackendEvent(msg.Event)

	case StreamDoneMsg:
		// Ignore if already idle (cancelled)
		if m.state == StateIdle {
			return m, nil
		}
		return m.handleStreamDone()

	case components.SpinnerTickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	// Forward to textarea when idle
	if m.state == StateIdle {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Cancel):
		if m.state == StateStreaming || m.state == StateToolExec {
			m.backend.CancelOperation()
			// Flush any partial streaming content
			if m.streamBuf.Len() > 0 {
				rendered := RenderMarkdown(m.streamBuf.String())
				found := false
				for i := len(m.messages) - 1; i >= 0; i-- {
					if m.messages[i].Role == "assistant-streaming" {
						m.messages[i] = RenderedMessage{Role: "assistant", Content: rendered}
						found = true
						break
					}
				}
				if !found {
					m.messages = append(m.messages, RenderedMessage{Role: "assistant", Content: rendered})
				}
				m.streamBuf.Reset()
			}
			m.messages = append(m.messages, RenderedMessage{
				Role:    "system",
				Content: "Operation cancelled.",
			})
			// Drain remaining events from the channel to prevent goroutine leak
			if m.eventCh != nil {
				go func(ch <-chan Event) {
					for range ch {
					}
				}(m.eventCh)
				m.eventCh = nil
			}
			m.state = StateIdle
			m.updateViewportContent()
			return m, nil
		}
		// Clear input if idle
		m.input.Reset()
		return m, nil

	case key.Matches(msg, m.keys.ToggleSidebar):
		m.sidebarVisible = !m.sidebarVisible
		m.recalcLayout()
		return m, nil

	case key.Matches(msg, m.keys.ScrollUp):
		m.viewport.LineUp(5)
		return m, nil

	case key.Matches(msg, m.keys.ScrollDown):
		m.viewport.LineDown(5)
		return m, nil

	case key.Matches(msg, m.keys.Submit):
		if m.state != StateIdle {
			return m, nil
		}
		if m.completionShown && m.completionIdx >= 0 {
			return m.acceptCompletion(), nil
		}
		return m.handleSubmit()

	case key.Matches(msg, m.keys.NewLine):
		if m.state == StateIdle {
			m.input.InsertString("\n")
		}
		return m, nil

	case key.Matches(msg, m.keys.TabComplete):
		if m.state == StateIdle {
			if m.completionShown {
				// Cycle through completions
				m.completionIdx = (m.completionIdx + 1) % len(m.completions)
			} else {
				m.updateCompletions()
			}
			return m, nil
		}

	case key.Matches(msg, m.keys.HistoryUp):
		if m.state == StateIdle && !m.completionShown && !strings.Contains(m.input.Value(), "\n") {
			return m.navigateHistory(-1), nil
		}

	case key.Matches(msg, m.keys.HistoryDown):
		if m.state == StateIdle && !m.completionShown && !strings.Contains(m.input.Value(), "\n") {
			return m.navigateHistory(1), nil
		}

	case key.Matches(msg, m.keys.Rewind):
		now := time.Now()
		if m.state == StateIdle && !m.lastEscTime.IsZero() && now.Sub(m.lastEscTime) < 500*time.Millisecond {
			// Double-Esc: rewind last exchange
			m.lastEscTime = time.Time{}
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].Role == "user" {
					m.messages = m.messages[:i]
					break
				}
			}
			m.updateViewportContent()
			m.messages = append(m.messages, RenderedMessage{
				Role: "system", Content: "Last exchange removed.",
			})
			m.updateViewportContent()
			return m, nil
		}
		m.lastEscTime = now
		return m, nil

	// Approval keys
	case m.state == StateApproval && key.Matches(msg, m.keys.ApproveYes):
		return m.handleApproval(ApprovalYes)
	case m.state == StateApproval && key.Matches(msg, m.keys.ApproveNo):
		return m.handleApproval(ApprovalNo)
	case m.state == StateApproval && key.Matches(msg, m.keys.ApproveAlways):
		return m.handleApproval(ApprovalAlways)
	case m.state == StateApproval && key.Matches(msg, m.keys.ApproveSkip):
		return m.handleApproval(ApprovalSkip)
	}

	// Default: forward to textarea
	if m.state == StateIdle {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		// Dismiss completion overlay on any other keypress
		if m.completionShown {
			m.dismissCompletions()
		}
		return m, cmd
	}

	return m, nil
}

func (m Model) handleSubmit() (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.input.Value())
	if input == "" {
		return m, nil
	}

	// Record in input history
	m.inputHistory = append(m.inputHistory, input)
	m.historyIdx = -1
	m.historyDraft = ""
	m.dismissCompletions()

	// Add user message to viewport
	m.messages = append(m.messages, RenderedMessage{Role: "user", Content: input})
	m.input.Reset()
	m.state = StateStreaming
	m.streamBuf.Reset()
	m.updateViewportContent()

	// Start listening to backend
	cmd := m.startStream(input)
	return m, tea.Batch(cmd, m.spinner.Tick())
}

// startStreamCmd is a tea.Cmd that opens the backend stream and returns the first event.
// The channel is stored via a closure so listenNext can chain subsequent reads.
type startStreamMsg struct {
	ch  <-chan Event
	err error
}

func (m *Model) startStream(input string) tea.Cmd {
	backend := m.backend
	return func() tea.Msg {
		ch, err := backend.SendMessage(context.Background(), input)
		return startStreamMsg{ch: ch, err: err}
	}
}

func (m Model) handleBackendEvent(evt Event) (tea.Model, tea.Cmd) {
	next := listenNext(m.eventCh) // chain to read next event

	switch evt.Type {
	case EventTextDelta:
		m.streamBuf.WriteString(evt.Text)
		m.updateStreamingMessage()
		return m, next

	case EventToolStart:
		if evt.Tool != nil {
			rendered := m.toolView.RenderToolStart(evt.Tool.Name, evt.Tool.Args)
			m.messages = append(m.messages, RenderedMessage{
				Role:    "tool",
				Content: rendered,
			})
			m.state = StateToolExec
			m.updateViewportContent()
		}
		return m, next

	case EventToolResult:
		if evt.Tool != nil {
			rendered := m.toolView.RenderToolResult(evt.Tool.Name, evt.Tool.ExitCode, evt.Tool.Output, evt.Tool.Duration)
			if len(m.messages) > 0 && m.messages[len(m.messages)-1].Role == "tool" {
				m.messages[len(m.messages)-1].Content = rendered
			} else {
				m.messages = append(m.messages, RenderedMessage{Role: "tool", Content: rendered})
			}
			m.state = StateStreaming
			m.updateViewportContent()
		}
		return m, next

	case EventNeedApproval:
		m.state = StateApproval
		m.pendingApproval = evt.Approval
		// Show approval info in messages
		if evt.Approval != nil {
			desc := evt.Approval.Command
			if evt.Approval.Description != "" {
				desc += " — " + evt.Approval.Description
			}
			m.messages = append(m.messages, RenderedMessage{
				Role:    "system",
				Content: fmt.Sprintf("⚠️ Dangerous command: %s [%s risk]", desc, evt.Approval.Risk),
			})
			m.updateViewportContent()
		}
		// Don't chain next — wait for user approval response
		return m, nil

	case EventExit:
		return m, tea.Quit

	case EventClear:
		m.messages = nil
		m.updateViewportContent()
		return m, next

	case EventRewind:
		// Remove last user + assistant messages from viewport
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == "user" {
				m.messages = m.messages[:i]
				break
			}
		}
		m.updateViewportContent()
		m.messages = append(m.messages, RenderedMessage{
			Role: "system", Content: "Last exchange removed.",
		})
		m.updateViewportContent()
		return m, next

	case EventDone:
		m.eventCh = nil
		return m.handleStreamDone()

	case EventError:
		m.messages = append(m.messages, RenderedMessage{
			Role:    "error",
			Content: evt.Error.Error(),
		})
		m.state = StateIdle
		m.eventCh = nil
		m.updateViewportContent()
		return m, nil

	case EventTurnStart:
		if evt.Turn != nil {
			m.messages = append(m.messages, RenderedMessage{
				Role:    "system",
				Content: fmt.Sprintf("Turn %d/%d", evt.Turn.Turn, evt.Turn.MaxTurns),
			})
			m.updateViewportContent()
		}
		return m, next

	case EventPlanUpdate:
		// Tasks updated in sidebar automatically via backend.GetTasks()
		return m, next

	case EventThinking:
		return m, next
	}

	return m, next
}

func (m Model) handleStreamDone() (tea.Model, tea.Cmd) {
	// Flush streaming buffer as assistant message with markdown rendering
	if m.streamBuf.Len() > 0 {
		rendered := RenderMarkdown(m.streamBuf.String())
		found := false
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == "assistant-streaming" {
				m.messages[i] = RenderedMessage{Role: "assistant", Content: rendered}
				found = true
				break
			}
		}
		if !found {
			m.messages = append(m.messages, RenderedMessage{Role: "assistant", Content: rendered})
		}
		m.streamBuf.Reset()
	}
	m.state = StateIdle
	m.updateViewportContent()
	return m, nil
}

func (m Model) handleApproval(resp ApprovalResponse) (tea.Model, tea.Cmd) {
	if m.pendingApproval != nil && m.pendingApproval.ResponseCh != nil {
		// Send the response back to the backend
		m.pendingApproval.ResponseCh <- resp
	}
	m.pendingApproval = nil
	m.state = StateStreaming

	// Show what the user chose
	labels := map[ApprovalResponse]string{
		ApprovalYes:    "Approved",
		ApprovalNo:     "Denied",
		ApprovalAlways: "Always approve",
		ApprovalSkip:   "Skipped",
	}
	m.messages = append(m.messages, RenderedMessage{
		Role:    "system",
		Content: "→ " + labels[resp],
	})
	m.updateViewportContent()

	// Resume listening to the event channel
	if m.eventCh != nil {
		return m, listenNext(m.eventCh)
	}
	return m, nil
}

// --- Autocomplete ---

func (m *Model) updateCompletions() {
	text := m.input.Value()
	if !strings.HasPrefix(text, "/") {
		m.dismissCompletions()
		return
	}
	candidates := m.backend.GetCompletions(text)
	if len(candidates) == 0 {
		m.dismissCompletions()
		return
	}
	m.completions = candidates
	m.completionIdx = 0
	m.completionShown = true
}

func (m *Model) dismissCompletions() {
	m.completions = nil
	m.completionIdx = -1
	m.completionShown = false
}

func (m Model) acceptCompletion() Model {
	if m.completionIdx >= 0 && m.completionIdx < len(m.completions) {
		chosen := m.completions[m.completionIdx]
		m.input.Reset()
		m.input.InsertString(chosen.Text + " ")
	}
	m.dismissCompletions()
	return m
}

func (m Model) renderCompletionOverlay(width int) string {
	if !m.completionShown || len(m.completions) == 0 {
		return ""
	}
	var sb strings.Builder
	maxItems := 8
	if len(m.completions) < maxItems {
		maxItems = len(m.completions)
	}
	for i := 0; i < maxItems; i++ {
		c := m.completions[i]
		prefix := "  "
		if i == m.completionIdx {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%-16s %s", prefix, c.Text, c.Description)
		if len(line) > width-2 {
			line = line[:width-2]
		}
		sb.WriteString(line + "\n")
	}
	return Styles.Muted.Render(sb.String())
}

// --- Input History ---

func (m Model) navigateHistory(direction int) Model {
	if len(m.inputHistory) == 0 {
		return m
	}

	// Save current input as draft when starting to navigate
	if m.historyIdx == -1 {
		m.historyDraft = m.input.Value()
	}

	newIdx := m.historyIdx + direction
	if newIdx < -1 {
		newIdx = -1
	}
	if newIdx >= len(m.inputHistory) {
		newIdx = len(m.inputHistory) - 1
	}
	m.historyIdx = newIdx

	m.input.Reset()
	if m.historyIdx == -1 {
		// Restore draft
		m.input.InsertString(m.historyDraft)
	} else {
		// Show history entry (newest first: index 0 = most recent)
		histIdx := len(m.inputHistory) - 1 - m.historyIdx
		if histIdx >= 0 && histIdx < len(m.inputHistory) {
			m.input.InsertString(m.inputHistory[histIdx])
		}
	}
	return m
}

// --- Layout ---

func (m *Model) recalcLayout() {
	if !m.ready {
		return
	}
	headerH := 1
	footerH := 1
	inputH := 3 + 1 // textarea height + border
	viewportH := m.height - headerH - footerH - inputH
	if viewportH < 1 {
		viewportH = 1
	}

	viewportW := m.width
	if m.sidebarVisible && m.width >= SidebarBreakpoint {
		viewportW = m.width - SidebarWidth
	}

	m.viewport = viewport.New(viewportW, viewportH)
	m.viewport.SetContent(m.renderMessages(viewportW))
	m.input.SetWidth(viewportW - 2)

	m.header.SetWidth(m.width)
	m.footer.SetWidth(m.width)
	m.sidebar.SetSize(SidebarWidth, m.height-headerH-footerH)
	m.toolView.SetWidth(viewportW)
}

func (m *Model) updateViewportContent() {
	if !m.ready {
		return
	}
	w := m.width
	if m.sidebarVisible && m.width >= SidebarBreakpoint {
		w = m.width - SidebarWidth
	}
	m.viewport.SetContent(m.renderMessages(w))
	m.viewport.GotoBottom()
}

func (m *Model) updateStreamingMessage() {
	// Add or update an "assistant-streaming" message
	found := false
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == "assistant-streaming" {
			m.messages[i].Content = m.streamBuf.String()
			found = true
			break
		}
	}
	if !found {
		m.messages = append(m.messages, RenderedMessage{
			Role:    "assistant-streaming",
			Content: m.streamBuf.String(),
		})
	}
	m.updateViewportContent()
}

func (m Model) renderMessages(width int) string {
	if len(m.messages) == 0 {
		welcome := "\n" +
			Styles.Bold.Render("  Welcome to ChatCLI") + "\n\n" +
			Styles.Muted.Render("  Type a message to chat, or use /commands:") + "\n" +
			Styles.Muted.Render("  /agent <task>  — agent mode with tools") + "\n" +
			Styles.Muted.Render("  /coder <task>  — code editing focus") + "\n" +
			Styles.Muted.Render("  /help          — show all commands") + "\n" +
			Styles.Muted.Render("  Tab            — autocomplete /commands") + "\n"
		return welcome
	}

	contentWidth := width - 4 // padding
	if contentWidth < 20 {
		contentWidth = 20
	}

	modelName := m.backend.GetModelName()
	if modelName == "" {
		modelName = "assistant"
	}

	var sb strings.Builder
	for _, msg := range m.messages {
		sb.WriteString("\n")
		switch msg.Role {
		case "user":
			label := Styles.UserLabel.Render(" > ")
			body := Styles.UserMsg.Width(contentWidth).Render(msg.Content)
			sb.WriteString(label + " " + body)
		case "assistant", "assistant-streaming":
			label := Styles.AssistantLabel.Render(" " + modelName + " ")
			if msg.Role == "assistant-streaming" {
				label = Styles.Spinner.Render(m.spinner.View()) + " " + Styles.AssistantLabel.Render(modelName)
				body := Styles.AssistantMsg.Width(contentWidth).Render(msg.Content)
				sb.WriteString(label + "\n" + body)
			} else {
				body := Styles.AssistantMsg.Width(contentWidth).Render(msg.Content)
				sb.WriteString(label + "\n" + body)
			}
		case "tool":
			label := Styles.ToolLabel.Render(" tool ")
			body := Styles.ToolMsg.Width(contentWidth).Render(msg.Content)
			sb.WriteString(label + "\n" + body)
		case "error":
			body := Styles.ErrorMsg.Width(contentWidth).Render("Error: " + msg.Content)
			sb.WriteString(body)
		case "system":
			sb.WriteString(Styles.SystemMsg.Render(msg.Content))
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// --- View ---

// View implements tea.Model.
func (m Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	// Header
	tu := m.backend.GetTokenUsage()
	header := m.header.View(
		m.backend.GetProvider(),
		m.backend.GetModelName(),
		m.backend.GetSessionName(),
		components.TokenUsage{Used: tu.Used, Limit: tu.Limit, Cost: tu.Cost},
	)

	// Viewport + optional sidebar
	var mainArea string
	if m.sidebarVisible && m.width >= SidebarBreakpoint {
		tasks := m.backend.GetTasks()
		ct := make([]components.Task, len(tasks))
		for i, t := range tasks {
			ct[i] = components.Task{Description: t.Description, Status: t.Status}
		}
		files := m.backend.GetModifiedFiles()
		cf := make([]components.FileChange, len(files))
		for i, f := range files {
			cf[i] = components.FileChange{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions}
		}
		mcpServers := m.backend.GetMCPServers()
		cm := make([]components.MCPServer, len(mcpServers))
		for i, s := range mcpServers {
			cm[i] = components.MCPServer{Name: s.Name, Connected: s.Connected, ToolCount: s.ToolCount}
		}
		sidebar := m.sidebar.View(
			components.TokenUsage{Used: tu.Used, Limit: tu.Limit, Cost: tu.Cost},
			ct,
			cf,
			cm,
			m.backend.GetSessionName(),
		)
		mainArea = lipgloss.JoinHorizontal(lipgloss.Top, m.viewport.View(), sidebar)
	} else {
		mainArea = m.viewport.View()
	}

	// Input area
	var inputView string
	switch m.state {
	case StateApproval:
		inputView = Styles.Approval.Render(
			"Dangerous command detected. Approve? (Y)es (N)o (A)lways (S)kip",
		)
	case StateStreaming, StateToolExec:
		modelLabel := m.backend.GetModelName()
		if modelLabel == "" {
			modelLabel = "thinking"
		}
		inputView = Styles.InputArea.Render(
			Styles.Spinner.Render(m.spinner.View()) + " " +
				Styles.AssistantLabel.Render(modelLabel) +
				Styles.Muted.Render("  Ctrl+C to cancel"),
		)
	default:
		inputContent := m.input.View()
		if overlay := m.renderCompletionOverlay(m.width); overlay != "" {
			inputContent = overlay + inputContent
		}
		inputView = Styles.InputArea.Render(inputContent)
	}

	// Footer
	footer := m.footer.View(
		m.backend.GetWorkingDir(),
		m.sidebarVisible,
		m.state == StateStreaming || m.state == StateToolExec,
	)

	return lipgloss.JoinVertical(lipgloss.Left, header, mainArea, inputView, footer)
}
