/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package palette

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/theme"
)

// maxVisibleRows caps the list height so the overlay never scrolls the whole
// screen; the visible window follows the cursor.
const maxVisibleRows = 14

// Suggestion is one next-token option for a command line: the literal Text to
// append and an optional human-readable Desc.
type Suggestion struct {
	Text string
	Desc string
}

// SuggestFunc returns the next-token suggestions for a command line, exactly
// as if the user had typed line (with a trailing space) at the prompt. The
// overlay is driven entirely by this callback, so the palette and the inline
// completer always agree. The REPL supplies one that wraps cli.completer.
type SuggestFunc func(line string) []Suggestion

// item is a selectable row at the current level.
type item struct {
	text     string   // token appended to the composed line ("" for a bare run)
	label    string   // display label; falls back to text when empty
	desc     string   // optional description
	category Category // root-level grouping
	hasCat   bool     // true only at the root level
	bare     bool     // selecting runs the level's command as-is (prefix only)
}

// display returns the row's visible label.
func (it item) display() string {
	if it.label != "" {
		return it.label
	}
	return it.text
}

// level is one frame of the drill-down stack.
type level struct {
	prefix string // composed line up to this level ("" at root)
	title  string // header text for this level
	items  []item
	cursor int
	offset int
	query  string
}

// Model is the interactive command-palette overlay. It is a pure tea.Model:
// every state transition flows through Update, which makes it testable without
// a TTY by feeding tea.KeyMsg values directly.
type Model struct {
	suggest  SuggestFunc
	stack    []level
	width    int
	height   int
	th       theme.Theme
	prof     theme.Profile
	result   string // composed command on confirm; "" on cancel
	quitting bool
}

// NewRoot builds a palette that opens on the categorized root command list.
func NewRoot(suggest SuggestFunc) Model {
	return newModel(suggest, level{
		prefix: "",
		title:  i18n.T("palette.ui.title"),
		items:  rootItems(),
	})
}

// NewScoped builds a palette that opens directly on a command's options, e.g.
// NewScoped(suggest, "/model") lists the available models. When the command
// has no options the overlay still opens but shows the empty-state hint.
func NewScoped(suggest SuggestFunc, command string) Model {
	command = strings.TrimSpace(command)
	// No parent item to inherit a description from at the top level, so let
	// buildLevel fall back to the command's own summary.
	return newModel(suggest, buildLevel(command, "", suggest(command+" ")))
}

// buildLevel assembles a drilled level from raw suggestions. For any non-root
// level it prepends a "run as-is" entry so the user can execute the command at
// this point without choosing a subcommand — preserving the behavior of bare
// commands that did something on their own (e.g. "/config" overview, "/switch"
// provider picker) even though they also have subcommands.
//
// selfDesc is the description of the item the user selected to reach this
// level. Using it means a subcommand's own description follows it down — e.g.
// the "↵ /config security" entry reads "Inspect/manage security rules", not the
// parent /config summary. It falls back to the top-level command's summary
// (top-level open), then to the generic "run without arguments" label.
func buildLevel(prefix, selfDesc string, raw []Suggestion) level {
	items := cleanItems(raw)
	if prefix != "" {
		desc := selfDesc
		if desc == "" {
			cmd := prefix
			if i := strings.IndexAny(prefix, " \t"); i >= 0 {
				cmd = prefix[:i]
			}
			if summary, ok := RootSummary(cmd); ok && summary != "" {
				desc = summary
			} else {
				desc = i18n.T("palette.ui.run_bare")
			}
		}
		self := item{
			label: "↵ " + prefix,
			desc:  desc,
			bare:  true,
		}
		items = append([]item{self}, items...)
	}
	return level{prefix: prefix, title: prefix, items: items}
}

func newModel(suggest SuggestFunc, root level) Model {
	return Model{
		suggest: suggest,
		stack:   []level{root},
		th:      theme.Active(),
		prof:    theme.ActiveProfile(),
		width:   80,
		height:  24,
	}
}

// Result returns the composed command line, or "" if the user canceled.
func (m Model) Result() string { return m.result }

// rootItems builds the categorized root command rows from the registry.
func rootItems() []item {
	cmds := RootCommands()
	out := make([]item, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, item{
			text:     c.Name,
			desc:     c.Summary(),
			category: c.Category,
			hasCat:   true,
		})
	}
	return out
}

// cleanItems converts raw suggestions into selectable items, dropping command
// echoes (a leading slash) and free-form argument hints (<like-this>) that are
// placeholders rather than concrete choices.
func cleanItems(raw []Suggestion) []item {
	out := make([]item, 0, len(raw))
	for _, s := range raw {
		if isHint(s.Text) || strings.HasPrefix(s.Text, "/") {
			continue
		}
		out = append(out, item{text: s.Text, desc: s.Desc})
	}
	return out
}

// rawHasHint reports whether any suggestion is a free-form argument hint,
// signaling that the command expects a value the user types in.
func rawHasHint(raw []Suggestion) bool {
	for _, s := range raw {
		if isHint(s.Text) {
			return true
		}
	}
	return false
}

func isHint(text string) bool {
	return strings.HasPrefix(text, "<") && strings.HasSuffix(text, ">")
}

// HasConcreteOption reports whether raw contains at least one selectable option
// — not just command echoes or free-form <hints>. The REPL uses this to decide
// whether a bare command should open the per-command palette.
func HasConcreteOption(raw []Suggestion) bool {
	return len(cleanItems(raw)) > 0
}

// ── tea.Model ────────────────────────────────────────────────────────────────

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		if msg.Type == tea.KeyEsc && m.top().query != "" {
			return m.setQuery(""), nil
		}
		if msg.Type == tea.KeyEsc && m.depth() > 0 {
			return m.pop(), nil
		}
		m.result = ""
		m.quitting = true
		return m, tea.Quit
	case tea.KeyUp, tea.KeyCtrlP:
		return m.move(-1), nil
	case tea.KeyDown, tea.KeyCtrlN:
		return m.move(1), nil
	case tea.KeyLeft:
		if m.depth() > 0 {
			return m.pop(), nil
		}
		return m, nil
	case tea.KeyRight, tea.KeyEnter:
		return m.selectCurrent()
	case tea.KeyTab:
		// Tab grabs the current composed line as prefill text without
		// drilling, so a user can take "/session " and finish it manually.
		return m.prefillCurrent()
	case tea.KeyBackspace:
		if m.top().query != "" {
			r := []rune(m.top().query)
			return m.setQuery(string(r[:len(r)-1])), nil
		}
		if m.depth() > 0 {
			return m.pop(), nil
		}
		return m, nil
	case tea.KeySpace:
		return m.setQuery(m.top().query + " "), nil
	case tea.KeyRunes:
		return m.setQuery(m.top().query + string(msg.Runes)), nil
	}
	return m, nil
}

// top returns the current level (top of stack).
func (m Model) top() level { return m.stack[len(m.stack)-1] }

// depth is the number of drill-downs below the opening level.
func (m Model) depth() int { return len(m.stack) - 1 }

// replaceTop returns a copy of m with its top level replaced.
func (m Model) replaceTop(l level) Model {
	st := append([]level{}, m.stack...)
	st[len(st)-1] = l
	m.stack = st
	return m
}

// setQuery updates the active filter and resets the cursor/scroll.
func (m Model) setQuery(q string) Model {
	l := m.top()
	l.query = q
	l.cursor, l.offset = 0, 0
	return m.replaceTop(l)
}

// visible returns the current level's entries matching its query.
func (m Model) visible() []item {
	l := m.top()
	if l.query == "" {
		return l.items
	}
	q := strings.ToLower(l.query)
	out := make([]item, 0, len(l.items))
	for _, it := range l.items {
		if matches(it, q) {
			out = append(out, it)
		}
	}
	return out
}

func matches(it item, q string) bool {
	if subsequence(strings.ToLower(it.display()), q) {
		return true
	}
	return it.desc != "" && strings.Contains(strings.ToLower(it.desc), q)
}

func subsequence(s, q string) bool {
	if q == "" {
		return true
	}
	i := 0
	qr := []rune(q)
	for _, r := range s {
		if r == qr[i] {
			i++
			if i == len(qr) {
				return true
			}
		}
	}
	return false
}

func (m Model) move(delta int) Model {
	vis := m.visible()
	l := m.top()
	if len(vis) == 0 {
		l.cursor = 0
		return m.replaceTop(l)
	}
	l.cursor += delta
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor >= len(vis) {
		l.cursor = len(vis) - 1
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+maxVisibleRows {
		l.offset = l.cursor - maxVisibleRows + 1
	}
	return m.replaceTop(l)
}

func (m Model) pop() Model {
	if m.depth() == 0 {
		return m
	}
	m.stack = m.stack[:len(m.stack)-1]
	return m
}

// current returns the highlighted item and whether one exists.
func (m Model) current() (item, bool) {
	vis := m.visible()
	c := m.top().cursor
	if c < 0 || c >= len(vis) {
		return item{}, false
	}
	return vis[c], true
}

// composeLine joins a level prefix with a selected token.
func composeLine(prefix, token string) string {
	if prefix == "" {
		return token
	}
	if token == "" {
		return prefix
	}
	return prefix + " " + token
}

// selectCurrent drills into the highlighted item when it opens a genuinely
// deeper level, or finishes the command otherwise.
//
// The drill-vs-finish decision can't simply ask "are there more suggestions?",
// because many completers are stateless: after a value is picked (e.g. a model
// name) they re-offer the very same list for the next position. Drilling on
// that would loop forever. So a selection is TERMINAL when the next level is
// empty, repeats the token just chosen, or is the same option set we picked
// from — all signs the token was a value, not a subcommand opening a new level.
func (m Model) selectCurrent() (tea.Model, tea.Cmd) {
	it, ok := m.current()
	if !ok {
		return m, nil
	}
	// The "run as-is" entry executes the command at this level with no further
	// arguments (submit-ready, no trailing space).
	if it.bare {
		return m.finish(m.top().prefix, false)
	}
	line := composeLine(m.top().prefix, it.text)
	raw := m.suggest(line + " ")
	children := cleanItems(raw)

	terminal := len(children) == 0 ||
		containsToken(children, it.text) ||
		sameTokens(children, m.top().items)
	if !terminal {
		// Carry the selected subcommand's description into the new level so its
		// "run as-is" entry describes what that subcommand does, not the parent.
		m.stack = append(m.stack, buildLevel(line, it.desc, raw))
		return m, nil
	}
	// Keep a trailing space only when the command genuinely expects a
	// free-form argument the user must type (a <hint> with no concrete value).
	return m.finish(line, len(children) == 0 && rawHasHint(raw))
}

// containsToken reports whether any item carries the given token.
func containsToken(items []item, tok string) bool {
	for _, it := range items {
		if it.text == tok {
			return true
		}
	}
	return false
}

// sameTokens reports whether two item slices carry the same set of tokens —
// the signature of a stateless completer re-offering an unchanged option list.
func sameTokens(a, b []item) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(b))
	for _, it := range b {
		set[it.text] = true
	}
	for _, it := range a {
		if !set[it.text] {
			return false
		}
	}
	return true
}

// prefillCurrent returns the composed line for the highlighted item with a
// trailing space, letting the user complete it manually.
func (m Model) prefillCurrent() (tea.Model, tea.Cmd) {
	it, ok := m.current()
	if !ok {
		return m, nil
	}
	return m.finish(composeLine(m.top().prefix, it.text), true)
}

func (m Model) finish(line string, trailing bool) (tea.Model, tea.Cmd) {
	if trailing {
		line += " "
	}
	m.result = line
	m.quitting = true
	return m, tea.Quit
}

// Run displays the overlay and blocks until the user confirms or cancels,
// returning the composed command line ("" on cancel). It owns the terminal in
// the alt-screen; the caller must have released go-prompt's raw mode first.
func Run(ctx context.Context, m Model) (string, error) {
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	out, err := p.Run()
	if err != nil {
		return "", err
	}
	if fm, ok := out.(Model); ok {
		return fm.result, nil
	}
	return "", nil
}

// ── rendering ───────────────────────────────────────────────────────────────

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	l := m.top()
	var b strings.Builder

	b.WriteString(m.style(theme.RoleHeader).Bold(true).Render(l.title))
	b.WriteString("\n")

	crumb := ""
	if m.depth() > 0 && l.prefix != "" {
		crumb = m.style(theme.RoleStatus).Render(l.prefix) + " "
	}
	b.WriteString(crumb + i18n.T("palette.ui.filter_prompt") + " " +
		m.style(theme.RoleModelName).Render(l.query) + "▏")
	b.WriteString("\n\n")

	vis := m.visible()
	if len(vis) == 0 {
		b.WriteString(m.style(theme.RoleMuted).Render(i18n.T("palette.ui.no_match")))
		b.WriteString("\n")
	} else {
		m.renderRows(&b, vis)
	}

	b.WriteString("\n")
	b.WriteString(m.style(theme.RoleMuted).Render(i18n.T("palette.ui.hint")))
	return b.String()
}

func (m Model) renderRows(b *strings.Builder, vis []item) {
	l := m.top()
	end := l.offset + maxVisibleRows
	if end > len(vis) {
		end = len(vis)
	}
	showHeaders := m.depth() == 0 && l.query == ""
	lastCat := Category(255)
	labelW := labelWidth(vis[l.offset:end])

	for i := l.offset; i < end; i++ {
		it := vis[i]
		if showHeaders && it.hasCat && it.category != lastCat {
			lastCat = it.category
			b.WriteString(m.style(theme.RoleHeader).Render("  " + it.category.Label()))
			b.WriteString("\n")
		}
		b.WriteString(m.renderRow(it, i == l.cursor, labelW))
		b.WriteString("\n")
	}
	if end < len(vis) {
		b.WriteString(m.style(theme.RoleMuted).Render("  " + i18n.T("palette.ui.more", len(vis)-end)))
		b.WriteString("\n")
	}
}

func (m Model) renderRow(it item, selected bool, labelW int) string {
	display := pad(it.display(), labelW)
	// Truncate the description to the width left after the marker and label so
	// a long summary (e.g. /config's) never wraps into a giant multi-row line.
	descText := m.fitDesc(it.desc, labelW)
	// Distinct semantic roles per column so the label and its description never
	// collapse to the same color under any theme: the token reads in the
	// primary color, the description in muted; the selected row pops with an
	// accent marker, a bold label and a brighter (info) description.
	var marker, label, desc string
	if selected {
		marker = m.style(theme.RoleReasoning).Bold(true).Render("❯ ")
		label = m.style(theme.RoleModelName).Bold(true).Render(display)
		desc = m.style(theme.RoleExplanation).Render(descText)
	} else {
		marker = "  "
		label = m.style(theme.RoleModelName).Render(display)
		desc = m.style(theme.RoleMuted).Render(descText)
	}
	row := marker + label
	if descText != "" {
		row += "  " + desc
	}
	return row
}

// fitDesc trims a description to the space left on the row after the 2-col
// marker, the padded label and a 2-col gap, adding an ellipsis when cut.
// Returns "" when the terminal is too narrow to show anything useful.
func (m Model) fitDesc(desc string, labelW int) string {
	if desc == "" {
		return ""
	}
	avail := m.width - (2 + labelW + 2) - 1 // marker + label + gap + safety
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

func labelWidth(window []item) int {
	w := 0
	for _, it := range window {
		if l := lipgloss.Width(it.display()); l > w {
			w = l
		}
	}
	if w > 24 {
		w = 24
	}
	return w
}

func (m Model) style(r theme.Role) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(m.th.LipFor(r, m.prof))
}

func pad(s string, w int) string {
	d := lipgloss.Width(s)
	if d >= w {
		return s
	}
	return s + strings.Repeat(" ", w-d)
}
