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
	Role      string // "user", "assistant", "tool", "error", "system"
	Content   string // rendered text
	PreStyled bool   // true if Content already has ANSI styling (skip lipgloss Width)
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
	streamBuf      strings.Builder // accumulates streaming text
	eventCh        <-chan Event    // active event channel from backend
	err            error

	// Approval
	pendingApproval *ApprovalRequest // active approval request

	// Autocomplete
	completions     []Completion // current completion candidates
	completionIdx   int          // selected completion index (-1 = none)
	completionShown bool         // is the completion overlay visible

	// Scroll tracking
	userScrolled bool // true if user manually scrolled away from bottom

	// Input history
	inputHistory []string // previous inputs
	historyIdx   int      // -1 = current input, 0..n = history entries
	historyDraft string   // draft text before navigating history

	// Double-Esc rewind
	lastEscTime time.Time // timestamp of last Escape press

	// Message queue for non-blocking input
	pendingInputs []string // queued messages to send when current stream finishes
}

func newModel(backend Backend) Model {
	// Input textarea — single visible line; use Shift+Enter for multiline
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = "❯ "
	ta.CharLimit = 0 // no limit
	ta.SetHeight(1)
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
		viewport:       viewport.New(80, 20), // placeholder — recalcLayout sets real size
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
		m.backend.SetContentWidth(m.contentWidth())
		if !m.userScrolled {
			m.viewport.GotoBottom()
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		// Delegate scroll to viewport's built-in mouse handling
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		if m.viewport.AtBottom() {
			m.userScrolled = false
		} else if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			m.userScrolled = true
		}
		return m, cmd

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

	case BatchEventMsg:
		// Process multiple events (batched text + following event)
		if m.state == StateIdle && m.eventCh == nil {
			return m, nil
		}
		var result tea.Model = m
		var lastCmd tea.Cmd
		for _, evt := range msg.Events {
			result, lastCmd = result.(Model).handleBackendEvent(evt)
		}
		m = result.(Model)
		return m, lastCmd

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

	// Always forward to textarea so user can type while streaming
	if m.state != StateApproval {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// isEscapeFragment detects unparsed escape sequence fragments that leak through
// when the terminal splits ESC from the rest, or when ANSI codes aren't consumed.
func isEscapeFragment(msg tea.KeyMsg) bool {
	s := msg.String()
	if len(s) == 0 {
		return false
	}
	// SGR mouse: [<Cb;Cx;Cy[Mm]
	if len(s) > 4 && s[0] == '[' && s[1] == '<' && (s[len(s)-1] == 'M' || s[len(s)-1] == 'm') {
		return true
	}
	// CSI sequences: [... ending in a letter (SGR colors, cursor, etc.)
	if s[0] == '[' && len(s) > 1 && s[len(s)-1] >= 'A' && s[len(s)-1] <= 'z' {
		return true
	}
	// Bare ESC or ESC[ prefix
	if s == "\x1b" || s == "\x1b[" {
		return true
	}
	return false
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Filter raw escape sequence fragments that weren't parsed by Bubble Tea
	if isEscapeFragment(msg) {
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Cancel):
		if m.state == StateStreaming || m.state == StateToolExec {
			m.backend.CancelOperation()
			// Flush any partial streaming content
			if m.streamBuf.Len() > 0 {
				rendered := RenderMarkdown(m.streamBuf.String(), m.contentWidth())
				found := false
				for i := len(m.messages) - 1; i >= 0; i-- {
					if m.messages[i].Role == "assistant-streaming" {
						m.messages[i] = RenderedMessage{Role: "assistant", Content: rendered, PreStyled: true}
						found = true
						break
					}
				}
				if !found {
					m.messages = append(m.messages, RenderedMessage{Role: "assistant", Content: rendered, PreStyled: true})
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
		m.userScrolled = true
		return m, nil

	case key.Matches(msg, m.keys.ScrollDown):
		m.viewport.LineDown(5)
		if m.viewport.AtBottom() {
			m.userScrolled = false
		}
		return m, nil

	case key.Matches(msg, m.keys.Submit):
		if m.state == StateApproval {
			return m, nil
		}
		// Queue message if streaming, otherwise send immediately
		input := strings.TrimSpace(m.input.Value())
		if input == "" {
			return m, nil
		}
		if m.state != StateIdle {
			// Queue for later — show queued indicator
			m.pendingInputs = append(m.pendingInputs, input)
			m.messages = append(m.messages, RenderedMessage{
				Role:    "user",
				Content: input,
			})
			m.input.Reset()
			m.dismissCompletions()
			m.updateViewportContent()
			return m, nil
		}
		return m.handleSubmit()

	case key.Matches(msg, m.keys.NewLine):
		if m.state != StateApproval {
			m.input.InsertString("\n")
		}
		return m, nil

	case key.Matches(msg, m.keys.TabComplete):
		if m.state != StateApproval {
			if !m.completionShown {
				m.updateCompletions()
			} else {
				m.completionIdx = (m.completionIdx + 1) % len(m.completions)
			}
			if m.completionShown && m.completionIdx >= 0 && m.completionIdx < len(m.completions) {
				chosen := m.completions[m.completionIdx]
				m.input.Reset()
				m.input.InsertString(chosen.Text + " ")
				m.updateCompletions()
			}
			return m, nil
		}

	case key.Matches(msg, m.keys.HistoryUp):
		if m.state != StateApproval {
			if m.completionShown {
				if m.completionIdx > 0 {
					m.completionIdx--
				} else {
					m.completionIdx = len(m.completions) - 1
				}
				return m, nil
			}
			if !strings.Contains(m.input.Value(), "\n") {
				return m.navigateHistory(-1), nil
			}
			return m, nil
		}

	case key.Matches(msg, m.keys.HistoryDown):
		if m.state != StateApproval {
			if m.completionShown {
				m.completionIdx = (m.completionIdx + 1) % len(m.completions)
				return m, nil
			}
			if !strings.Contains(m.input.Value(), "\n") {
				return m.navigateHistory(1), nil
			}
			return m, nil
		}

	case key.Matches(msg, m.keys.Rewind):
		// Esc dismisses completions or clears input
		if m.completionShown {
			m.dismissCompletions()
			return m, nil
		}
		// Clear current input text
		if m.input.Value() != "" {
			m.input.Reset()
			return m, nil
		}
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

	// Default: forward to textarea (non-blocking — works during streaming too)
	if m.state != StateApproval {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		// Auto-update completions if input starts with "/" or "@"
		text := m.input.Value()
		if strings.HasPrefix(text, "/") || strings.HasPrefix(text, "@") {
			m.updateCompletions()
		} else if m.completionShown {
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
	m.userScrolled = false
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
		m.removeStatusMessages()
		m.streamBuf.WriteString(evt.Text)
		m.updateStreamingMessage()
		return m, next

	case EventPreStyledText:
		// Pre-rendered content (markdown, status, turn info) — flush stream and append
		m.removeStatusMessages()
		m.flushStreamBuf()
		// Accumulate consecutive pre-styled text into the same message
		if n := len(m.messages); n > 0 && m.messages[n-1].Role == "assistant" && m.messages[n-1].PreStyled {
			m.messages[n-1].Content += "\n" + evt.Text
		} else {
			m.messages = append(m.messages, RenderedMessage{
				Role:      "assistant",
				Content:   evt.Text,
				PreStyled: true,
			})
		}
		m.updateViewportContent()
		return m, next

	case EventToolStart:
		if evt.Tool != nil {
			m.removeStatusMessages()
			m.flushStreamBuf()
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
		// Flush any pending assistant text before showing approval
		m.flushStreamBuf()
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

	case EventCommandOutput:
		// Plain text output from slash commands — no markdown rendering
		m.messages = append(m.messages, RenderedMessage{
			Role:    "command",
			Content: evt.Text,
		})
		m.updateViewportContent()
		return m, next

	case EventDone:
		m.eventCh = nil
		return m.handleStreamDone()

	case EventError:
		m.flushStreamBuf()
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
			// Flush assistant text from previous turn before starting new one
			m.flushStreamBuf()
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

	case EventStatusUpdate:
		// Replace last status message in-place (no accumulation)
		found := false
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == "status" {
				m.messages[i].Content = evt.Text
				found = true
				break
			}
		}
		if !found {
			m.messages = append(m.messages, RenderedMessage{
				Role:      "status",
				Content:   evt.Text,
				PreStyled: true,
			})
		}
		m.updateViewportContent()
		return m, next
	}

	return m, next
}

func (m Model) handleStreamDone() (tea.Model, tea.Cmd) {
	// Flush streaming buffer as assistant message with markdown rendering
	if m.streamBuf.Len() > 0 {
		rendered := RenderMarkdown(m.streamBuf.String(), m.contentWidth())
		found := false
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == "assistant-streaming" {
				m.messages[i] = RenderedMessage{Role: "assistant", Content: rendered, PreStyled: true}
				found = true
				break
			}
		}
		if !found {
			m.messages = append(m.messages, RenderedMessage{Role: "assistant", Content: rendered, PreStyled: true})
		}
		m.streamBuf.Reset()
	}
	m.state = StateIdle
	m.userScrolled = false
	m.updateViewportContent()

	// Process queued messages
	if len(m.pendingInputs) > 0 {
		next := m.pendingInputs[0]
		m.pendingInputs = m.pendingInputs[1:]
		// User message already in viewport (added when queued) — just start streaming
		m.state = StateStreaming
		m.streamBuf.Reset()
		cmd := m.startStream(next)
		return m, tea.Batch(cmd, m.spinner.Tick())
	}

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

// contentWidth returns the usable text width inside a message box,
// accounting for sidebar, border (2) and padding (2).
func (m Model) contentWidth() int {
	w := m.width
	if m.sidebarVisible && m.width >= SidebarBreakpoint {
		w = m.width - SidebarWidth
	}
	cw := w - 4 // border(2) + padding(2)
	if cw < 20 {
		cw = 20
	}
	return cw
}

// --- Autocomplete ---

func (m *Model) updateCompletions() {
	text := m.input.Value()
	if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@") {
		m.dismissCompletions()
		return
	}
	candidates := m.backend.GetCompletions(text)
	if len(candidates) == 0 {
		m.dismissCompletions()
		return
	}
	wasShown := m.completionShown
	prevCount := len(m.completions)
	m.completions = candidates
	// Preserve selection index if still valid, otherwise reset to 0
	if m.completionIdx < 0 || m.completionIdx >= len(candidates) {
		m.completionIdx = 0
	}
	m.completionShown = true
	// Recalc layout when overlay appears or changes size (viewport needs to shrink)
	if !wasShown || len(candidates) != prevCount {
		m.recalcLayout()
	}
}

func (m *Model) dismissCompletions() {
	wasShown := m.completionShown
	m.completions = nil
	m.completionIdx = -1
	m.completionShown = false
	// Recalc layout to reclaim overlay space for viewport
	if wasShown {
		m.recalcLayout()
	}
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
		if i < maxItems-1 {
			sb.WriteString(line + "\n")
		} else {
			sb.WriteString(line)
		}
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
	infoBarH := 1   // info bar above input
	inputH := 1 + 1 // textarea (1 line) + top border

	// Reserve space for completion overlay when visible
	completionH := 0
	if m.completionShown && len(m.completions) > 0 {
		completionH = len(m.completions)
		if completionH > 8 {
			completionH = 8
		}
	}

	viewportH := m.height - headerH - footerH - infoBarH - inputH - completionH
	if viewportH < 1 {
		viewportH = 1
	}

	viewportW := m.width
	if m.sidebarVisible && m.width >= SidebarBreakpoint {
		viewportW = m.width - SidebarWidth
	}

	// Update existing viewport dimensions (don't recreate — preserves scroll state)
	m.viewport.Width = viewportW
	m.viewport.Height = viewportH
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
	if !m.userScrolled {
		m.viewport.GotoBottom()
	}
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

// removeStatusMessages removes transient "status" messages (thinking/timer).
func (m *Model) removeStatusMessages() {
	filtered := m.messages[:0]
	for _, msg := range m.messages {
		if msg.Role != "status" {
			filtered = append(filtered, msg)
		}
	}
	m.messages = filtered
}

// flushStreamBuf finalizes any pending streaming text as an assistant message.
// Must be called before inserting tool/system/turn messages to keep them separate.
func (m *Model) flushStreamBuf() {
	if m.streamBuf.Len() == 0 {
		return
	}
	rendered := RenderMarkdown(m.streamBuf.String(), m.contentWidth())
	found := false
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == "assistant-streaming" {
			m.messages[i] = RenderedMessage{Role: "assistant", Content: rendered, PreStyled: true}
			found = true
			break
		}
	}
	if !found {
		m.messages = append(m.messages, RenderedMessage{Role: "assistant", Content: rendered, PreStyled: true})
	}
	m.streamBuf.Reset()
}

// renderInfoBar renders a compact status line above the input box.
func (m Model) renderInfoBar() string {
	sep := lipgloss.NewStyle().Foreground(colorBorder).Render(" │ ")
	modelStyle := lipgloss.NewStyle().Foreground(colorPrimary)
	dimStyle := lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle := lipgloss.NewStyle().Foreground(colorAccent)

	provider := m.backend.GetProvider()
	model := m.backend.GetModelName()
	if model == "" {
		model = "unknown"
	}

	// Left: model@provider + session + contexts
	left := " " + modelStyle.Render(model) + dimStyle.Render("@") + dimStyle.Render(provider)

	session := m.backend.GetSessionName()
	if session != "" {
		left += sep + lipgloss.NewStyle().Foreground(colorSuccess).Render(session)
	}

	ctxs := m.backend.GetAttachedContexts()
	if len(ctxs) > 0 {
		names := make([]string, 0, len(ctxs))
		for _, c := range ctxs {
			names = append(names, c.Name)
		}
		ctxText := strings.Join(names, ",")
		if len(ctxText) > 25 {
			ctxText = fmt.Sprintf("%d ctx", len(ctxs))
		}
		left += sep + accentStyle.Render(ctxText)
	}

	// Right: tokens + cost + state
	var parts []string

	tu := m.backend.GetTokenUsage()
	if tu.Used > 0 {
		pct := ""
		if tu.Limit > 0 {
			pct = fmt.Sprintf(" %d%%", tu.Used*100/tu.Limit)
		}
		parts = append(parts, accentStyle.Render(formatCompact(tu.Used)+pct))
	}
	if tu.Cost > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorWarning).Render(fmt.Sprintf("$%.3f", tu.Cost)))
	}

	// Queued messages indicator
	if len(m.pendingInputs) > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorWarning).Render(fmt.Sprintf("%d queued", len(m.pendingInputs))))
	}

	switch m.state {
	case StateStreaming:
		parts = append(parts, lipgloss.NewStyle().Foreground(colorSuccess).Render("● streaming"))
	case StateToolExec:
		parts = append(parts, lipgloss.NewStyle().Foreground(colorWarning).Render("● tool"))
	case StateApproval:
		parts = append(parts, lipgloss.NewStyle().Foreground(colorError).Render("● approval"))
	}

	right := ""
	for i, p := range parts {
		if i > 0 {
			right += sep
		}
		right += p
	}
	right += " "

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	barStyle := lipgloss.NewStyle().
		Foreground(colorMuted).
		Background(colorSurface).
		Width(m.width)

	return barStyle.Render(left + spaces(gap) + right)
}

func formatCompact(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

func (m Model) renderMessages(width int) string {
	if len(m.messages) == 0 {
		neon := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
		dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#262626"))
		accent := lipgloss.NewStyle().Foreground(colorAccent)
		muted := lipgloss.NewStyle().Foreground(colorSubtle)
		cmdStyle := lipgloss.NewStyle().Foreground(colorAccent)
		descStyle := lipgloss.NewStyle().Foreground(colorMuted)

		// Compact logo for narrow terminals, full for wide
		var logo string
		if width >= 60 {
			logo = neon.Render("  ██████╗██╗  ██╗ █████╗ ████████╗ ██████╗██╗     ██╗") + "\n" +
				neon.Render("  ██╔════╝██║  ██║██╔══██╗╚══██╔══╝██╔════╝██║     ██║") + "\n" +
				neon.Render("  ██║     ███████║███████║   ██║   ██║     ██║     ██║") + "\n" +
				neon.Render("  ██║     ██╔══██║██╔══██║   ██║   ██║     ██║     ██║") + "\n" +
				neon.Render("  ╚██████╗██║  ██║██║  ██║   ██║   ╚██████╗███████╗██║") + "\n" +
				neon.Render("  ╚═════╝╚═╝  ╚═╝╚═╝  ╚═╝   ╚═╝    ╚═════╝╚══════╝╚═╝")
		} else {
			logo = neon.Render("  ▲ ChatCLI")
		}

		model := m.backend.GetModelName()
		provider := m.backend.GetProvider()
		modelInfo := ""
		if model != "" {
			modelInfo = accent.Render("  "+model) + muted.Render(" @ ") + accent.Render(provider)
		}

		sep := dim.Render("  " + strings.Repeat("─", min(width-4, 54)))

		welcome := "\n" + logo + "\n" + modelInfo + "\n" + sep + "\n\n" +
			cmdStyle.Render("  /agent") + descStyle.Render(" <task>") + muted.Render("  agent mode with tools") + "\n" +
			cmdStyle.Render("  /coder") + descStyle.Render(" <task>") + muted.Render("  code editing focus") + "\n" +
			cmdStyle.Render("  /session") + muted.Render("         manage sessions") + "\n" +
			cmdStyle.Render("  /context") + muted.Render("         manage contexts") + "\n" +
			cmdStyle.Render("  /rewind") + muted.Render("          checkpoint list") + "\n" +
			cmdStyle.Render("  /help") + muted.Render("            all commands") + "\n" +
			cmdStyle.Render("  Tab") + muted.Render("              autocomplete") + "\n"
		return welcome
	}

	// In lipgloss, .Width(n) = padding + content (border is added outside).
	// Our bordered styles have border(2) + padding(2) = 4 chars total overhead.
	// So .Width(width - 2) renders a box exactly `width` chars wide.
	boxWidth := width - 2
	if boxWidth < 20 {
		boxWidth = 20
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
			body := Styles.UserMsg.Width(boxWidth).Render(msg.Content)
			sb.WriteString(label + "\n" + body)

		case "assistant", "assistant-streaming":
			if msg.Role == "assistant-streaming" {
				label := Styles.Spinner.Render(m.spinner.View()) + " " + Styles.AssistantLabel.Render(modelName)
				var body string
				if msg.PreStyled {
					body = Styles.AssistantMsg.Render(msg.Content)
				} else {
					body = Styles.AssistantMsg.Width(boxWidth).Render(msg.Content)
				}
				sb.WriteString(label + "\n" + body)
			} else {
				label := Styles.AssistantLabel.Render(" " + modelName + " ")
				var body string
				if msg.PreStyled {
					body = Styles.AssistantMsg.Render(msg.Content)
				} else {
					body = Styles.AssistantMsg.Width(boxWidth).Render(msg.Content)
				}
				sb.WriteString(label + "\n" + body)
			}

		case "tool":
			label := Styles.ToolLabel.Render(" tool ")
			var body string
			if msg.PreStyled {
				body = Styles.ToolMsg.Render(msg.Content)
			} else {
				body = Styles.ToolMsg.Width(boxWidth).Render(msg.Content)
			}
			sb.WriteString(label + "\n" + body)

		case "command":
			// Plain text from slash commands — no markdown, render as-is in box
			body := Styles.ToolMsg.Width(boxWidth).Render(msg.Content)
			sb.WriteString(body)

		case "error":
			body := Styles.ErrorMsg.Width(boxWidth).Render("Error: " + msg.Content)
			sb.WriteString(body)

		case "status":
			// Transient status (thinking/timer) — rendered with spinner
			statusStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
			sb.WriteString(Styles.Spinner.Render(m.spinner.View()) + " " + statusStyle.Render(msg.Content))

		case "system":
			sb.WriteString(Styles.SystemMsg.Width(width).Render(msg.Content))
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
		attachedCtxs := m.backend.GetAttachedContexts()
		cc := make([]components.ContextInfo, len(attachedCtxs))
		for i, c := range attachedCtxs {
			cc[i] = components.ContextInfo{Name: c.Name, FileCount: c.FileCount, SizeBytes: c.SizeBytes}
		}
		sidebar := m.sidebar.View(
			components.TokenUsage{Used: tu.Used, Limit: tu.Limit, Cost: tu.Cost},
			ct,
			cf,
			cm,
			m.backend.GetSessionName(),
			cc,
		)
		mainArea = lipgloss.JoinHorizontal(lipgloss.Top, m.viewport.View(), sidebar)
	} else {
		mainArea = m.viewport.View()
	}

	// Completion overlay (rendered as separate section above input)
	completionView := ""
	if m.state != StateApproval {
		completionView = m.renderCompletionOverlay(m.width)
	}

	// Input area — always show textarea (non-blocking input)
	var inputView string
	switch m.state {
	case StateApproval:
		inputView = Styles.Approval.Render(
			"Dangerous command detected. Approve? (Y)es (N)o (A)lways (S)kip",
		)
	default:
		inputView = Styles.InputArea.Render(m.input.View())
	}

	// Footer
	footer := m.footer.View(
		m.backend.GetWorkingDir(),
		m.sidebarVisible,
		m.state == StateStreaming || m.state == StateToolExec,
	)

	// Info bar above input — model, provider, message count
	infoBar := m.renderInfoBar()

	// Compose final layout — only include completion overlay if non-empty
	sections := []string{header, mainArea}
	if completionView != "" {
		sections = append(sections, completionView)
	}
	sections = append(sections, infoBar, inputView, footer)
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}
