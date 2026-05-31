/*
 * ChatCLI - AskUser interactive overlay.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * askModel is a dedicated Bubble Tea overlay for the @ask / ask_user tool: a
 * 1-4 question wizard with per-question single OR multi-select plus a free-text
 * "Other" row. It is a sibling of the command palette Model (which is a
 * drill-down command composer and the wrong shape for this), reusing the same
 * theme/lipgloss helpers and the alt-screen Run pattern.
 *
 * Like Model, askModel is a pure tea.Model: every transition flows through
 * Update, so it is fully testable without a TTY by feeding tea.KeyMsg values.
 */
package palette

import (
	"context"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/cli/agent/ask"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/theme"
)

// askModel holds the wizard state. Per-question selection state is kept in
// parallel slices indexed by question so that going back and forth between
// questions preserves earlier choices; the final []ask.Answer is computed once
// at confirm time from this state.
type askModel struct {
	questions []ask.Question

	qIndex int   // current question
	cursor int   // highlighted row: 0..len(options) (last row is "Other")
	single []int // single-select: chosen option index per question (-1 = none)

	checked   []map[int]bool // multi-select: toggled option indices per question
	otherText []string       // free-text per question
	editing   bool           // typing into the "Other" field

	answers  []ask.Answer
	canceled bool
	quitting bool

	width, height int
	th            theme.Theme
	prof          theme.Profile
}

// NewAsk builds the overlay for the given questions (clamped to MaxQuestions).
func NewAsk(questions []ask.Question) askModel {
	if len(questions) > ask.MaxQuestions {
		questions = questions[:ask.MaxQuestions]
	}
	n := len(questions)
	m := askModel{
		questions: questions,
		single:    make([]int, n),
		checked:   make([]map[int]bool, n),
		otherText: make([]string, n),
		th:        theme.Active(),
		prof:      theme.ActiveProfile(),
		width:     80,
		height:    24,
	}
	for i := range m.single {
		m.single[i] = -1
		m.checked[i] = map[int]bool{}
	}
	return m
}

// Answers returns the collected answers (valid only after a confirmed run).
func (m askModel) Answers() []ask.Answer { return m.answers }

// Canceled reports whether the user dismissed the overlay without answering.
func (m askModel) Canceled() bool { return m.canceled }

// ── tea.Model ────────────────────────────────────────────────────────────────

func (m askModel) Init() tea.Cmd { return nil }

func (m askModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// otherRow is the index of the synthetic "Other" row for the current question.
func (m askModel) otherRow() int { return len(m.cur().Options) }

// rowCount is the total selectable rows for the current question.
func (m askModel) rowCount() int { return len(m.cur().Options) + 1 }

func (m askModel) cur() ask.Question { return m.questions[m.qIndex] }

func (m askModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While editing the free-text field, keystrokes feed the buffer.
	if m.editing {
		return m.handleEditingKey(msg)
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		m.canceled = true
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		if m.qIndex > 0 {
			m.qIndex--
			m.cursor = 0
			return m, nil
		}
		m.canceled = true
		m.quitting = true
		return m, tea.Quit
	case tea.KeyUp, tea.KeyCtrlP:
		m.cursor = (m.cursor - 1 + m.rowCount()) % m.rowCount()
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		m.cursor = (m.cursor + 1) % m.rowCount()
		return m, nil
	case tea.KeyLeft:
		if m.qIndex > 0 {
			m.qIndex--
			m.cursor = 0
		}
		return m, nil
	case tea.KeySpace:
		return m.handleSpace()
	case tea.KeyEnter, tea.KeyRight:
		return m.handleEnter()
	}
	return m, nil
}

// handleSpace toggles in multi-select, marks the radio in single-select (visual
// feedback without advancing), and enters editing on the "Other" row.
func (m askModel) handleSpace() (tea.Model, tea.Cmd) {
	if m.cursor == m.otherRow() {
		m.editing = true
		return m, nil
	}
	if m.cur().MultiSelect {
		set := m.checked[m.qIndex]
		set[m.cursor] = !set[m.cursor]
		return m, nil
	}
	// Single-select: set the radio so the user sees the pick before confirming
	// with Enter. Clears any prior free-text since a concrete option wins.
	m.single[m.qIndex] = m.cursor
	m.otherText[m.qIndex] = ""
	return m, nil
}

// handleEnter is the workhorse: it either picks (single) / confirms (multi) the
// current question and advances, or enters editing on the "Other" row.
func (m askModel) handleEnter() (tea.Model, tea.Cmd) {
	onOther := m.cursor == m.otherRow()

	if m.cur().MultiSelect {
		if onOther {
			// On "Other": start editing instead of confirming, so the user
			// can type the custom text before confirming the whole question.
			m.editing = true
			return m, nil
		}
		// Confirm the question with the current toggles and advance.
		return m.advance()
	}

	// Single-select.
	if onOther {
		m.editing = true
		return m, nil
	}
	m.single[m.qIndex] = m.cursor
	m.otherText[m.qIndex] = "" // a concrete option supersedes prior free text
	return m.advance()
}

func (m askModel) handleEditingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		// Cancel the edit; stay on the "Other" row, keep prior text.
		m.editing = false
		return m, nil
	case tea.KeyEnter:
		// Commit the typed text. For single-select this also confirms the
		// question (Other is the selection) and advances; for multi-select it
		// just commits the text and exits editing so other toggles can stand.
		m.editing = false
		if m.cur().MultiSelect {
			return m, nil
		}
		m.single[m.qIndex] = -1 // free-text supersedes any concrete option
		return m.advance()
	case tea.KeyBackspace:
		r := []rune(m.otherText[m.qIndex])
		if len(r) > 0 {
			m.otherText[m.qIndex] = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeySpace:
		m.otherText[m.qIndex] += " "
		return m, nil
	case tea.KeyRunes:
		m.otherText[m.qIndex] += string(msg.Runes)
		return m, nil
	}
	return m, nil
}

// advance moves to the next question, or finishes when past the last one.
func (m askModel) advance() (tea.Model, tea.Cmd) {
	if m.qIndex+1 < len(m.questions) {
		m.qIndex++
		m.cursor = 0
		return m, nil
	}
	m.answers = m.buildAnswers()
	m.quitting = true
	return m, tea.Quit
}

// buildAnswers computes the final answers from per-question state.
func (m askModel) buildAnswers() []ask.Answer {
	out := make([]ask.Answer, 0, len(m.questions))
	for i, q := range m.questions {
		a := ask.Answer{Header: q.Header, Selected: []string{}, Other: strings.TrimSpace(m.otherText[i])}
		if q.MultiSelect {
			idxs := make([]int, 0, len(m.checked[i]))
			for idx, on := range m.checked[i] {
				if on && idx < len(q.Options) {
					idxs = append(idxs, idx)
				}
			}
			sort.Ints(idxs)
			for _, idx := range idxs {
				a.Selected = append(a.Selected, q.Options[idx].Label)
			}
		} else if m.single[i] >= 0 && m.single[i] < len(q.Options) {
			a.Selected = append(a.Selected, q.Options[m.single[i]].Label)
		}
		out = append(out, a)
	}
	return out
}

// RunAsk displays the overlay and blocks until the user confirms or cancels.
// It owns the terminal in the alt-screen; the caller must release any competing
// stdin reader / raw mode first (see AgentMode.withInteractiveStdin).
func RunAsk(ctx context.Context, m askModel) ([]ask.Answer, bool, error) {
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	out, err := p.Run()
	if err != nil {
		return nil, false, err
	}
	if fm, ok := out.(askModel); ok {
		if fm.canceled {
			return nil, true, nil
		}
		return fm.answers, false, nil
	}
	return nil, true, nil
}

// ── rendering ───────────────────────────────────────────────────────────────

func (m askModel) View() string {
	if m.quitting {
		return ""
	}
	q := m.cur()
	var b strings.Builder

	// Header: progress + question header.
	progress := i18n.T("ask.ui.progress", m.qIndex+1, len(m.questions))
	b.WriteString(m.style(theme.RoleStatus).Render(progress) + " ")
	b.WriteString(m.style(theme.RoleHeader).Bold(true).Render(q.Header))
	b.WriteString("\n")

	// Question text.
	b.WriteString(m.style(theme.RoleExplanation).Render(q.Question))
	b.WriteString("\n\n")

	labelW := m.labelWidth(q.Options)
	for i, opt := range q.Options {
		b.WriteString(m.renderOption(q, i, opt, labelW))
		b.WriteString("\n")
	}
	b.WriteString(m.renderOther())
	b.WriteString("\n\n")

	b.WriteString(m.style(theme.RoleMuted).Render(m.hint(q)))
	return b.String()
}

func (m askModel) renderOption(q ask.Question, i int, opt ask.Option, labelW int) string {
	selected := m.cursor == i
	var box string
	if q.MultiSelect {
		if m.checked[m.qIndex][i] {
			box = "[x] "
		} else {
			box = "[ ] "
		}
	} else {
		if m.single[m.qIndex] == i {
			box = "(•) "
		} else {
			box = "( ) "
		}
	}

	display := pad(opt.Label, labelW)
	descText := m.fitDesc(opt.Desc, labelW)

	var marker, label, desc string
	if selected {
		marker = m.style(theme.RoleReasoning).Bold(true).Render("❯ ")
		label = m.style(theme.RoleModelName).Bold(true).Render(box + display)
		desc = m.style(theme.RoleExplanation).Render(descText)
	} else {
		marker = "  "
		label = m.style(theme.RoleModelName).Render(box + display)
		desc = m.style(theme.RoleMuted).Render(descText)
	}
	row := marker + label
	if descText != "" {
		row += "  " + desc
	}
	return row
}

func (m askModel) renderOther() string {
	selected := m.cursor == m.otherRow()
	text := m.otherText[m.qIndex]

	var body string
	if m.editing {
		body = "[+] " + text + "▏"
	} else if text != "" {
		body = "[+] " + text
	} else {
		body = "[+] " + i18n.T("ask.ui.other")
	}

	var marker, label string
	if selected {
		marker = m.style(theme.RoleReasoning).Bold(true).Render("❯ ")
		label = m.style(theme.RoleModelName).Bold(true).Render(body)
	} else {
		marker = "  "
		if text == "" && !m.editing {
			label = m.style(theme.RoleMuted).Render(body)
		} else {
			label = m.style(theme.RoleModelName).Render(body)
		}
	}
	return marker + label
}

func (m askModel) hint(q ask.Question) string {
	if m.editing {
		return i18n.T("ask.ui.editing")
	}
	if q.MultiSelect {
		return i18n.T("ask.ui.hint_multi")
	}
	return i18n.T("ask.ui.hint_single")
}

func (m askModel) style(r theme.Role) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(m.th.LipFor(r, m.prof))
}

func (m askModel) labelWidth(opts []ask.Option) int {
	w := 0
	for _, o := range opts {
		if l := lipgloss.Width(o.Label); l > w {
			w = l
		}
	}
	if w > 24 {
		w = 24
	}
	return w
}

// fitDesc trims a description to the row width left after the marker, the
// checkbox, the padded label and a gap, adding an ellipsis when cut.
func (m askModel) fitDesc(desc string, labelW int) string {
	if desc == "" {
		return ""
	}
	avail := m.width - (2 + 4 + labelW + 2) - 1 // marker + box + label + gap + safety
	const minUseful = 8
	if avail < minUseful {
		return ""
	}
	if lipgloss.Width(desc) <= avail {
		return desc
	}
	r := []rune(desc)
	if avail-1 < len(r) {
		r = r[:avail-1]
	}
	return string(r) + "…"
}
