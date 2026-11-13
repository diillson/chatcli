/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/textfold"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
)

// The flat timeline is how a theme that draws without frames shows the
// steps of a turn. A tool call is ONE line: a state glyph, the tool and the
// argument that identifies the call, and what it cost on the right in the
// muted tone. Output hangs from a thin rail under that line instead of
// sitting in a card, and consecutive calls stack with no blank rows, so a
// turn reads top to bottom as one list.
//
// A call that is still running is not printed at all. While it runs its
// line is the caller's live line (repainted in place and erased when the
// call ends), and the permanent line is written once, with the final state.
// That is what makes "running" turn into "done" in place on a terminal and
// keeps the scrollback identical when there is no terminal to repaint: the
// same lines are written either way, only the transient one is missing.
// The one exception is a call that streams output: its line has to be on
// screen before the first row of output, so it is committed in the running
// state and the call is closed by a short line at the end of the rail.

// flatTool is the call the timeline is currently drawing.
type flatTool struct {
	verb, arg string
	started   time.Time
	open      bool // a call was announced and has not been closed
	header    bool // its line is already on screen, in the running state
	streamed  int  // rows of streamed output shown under it
}

// flatVerbPad is the column the argument starts at: the common verbs
// (read, exec, patch, search) fit, so their arguments line up.
const flatVerbPad = 6

// flatResultBudget is how much of a result the full style shows; it is the
// budget the framed result card has always had.
const flatResultBudget = 2000

// FlatLayout reports whether the active theme draws without frames.
func (r *UIRenderer) FlatLayout() bool {
	return theme.Active().Layout == theme.LayoutFlat
}

// FlatToolQueue announces the next call of a batch. Nothing is printed: the
// call may still be refused before it runs, and FlatToolRefused then draws
// it with the outcome the loop reports.
func (r *UIRenderer) FlatToolQueue(tool, subcmd, rawArgs string) {
	verb, arg := flatToolLabel(tool, subcmd, rawArgs)
	r.flatMu.Lock()
	r.flat = flatTool{verb: verb, arg: arg, open: true, started: time.Now()}
	r.flatMu.Unlock()
}

// FlatToolBegin marks the announced call as running: its clock starts here,
// after whatever time the permission gate took.
func (r *UIRenderer) FlatToolBegin() {
	r.flatMu.Lock()
	r.flat.started = time.Now()
	r.flatMu.Unlock()
}

// FlatToolLiveFrame is the running call's line, for a live repaint. Once
// output hangs from the call its line is already on screen, so the live
// row is the open end of the rail with the clock.
func (r *UIRenderer) FlatToolLiveFrame() string {
	r.flatMu.Lock()
	t := r.flat
	r.flatMu.Unlock()
	if t.header {
		return "  " + kit.Sym(kit.GlyphGutterEnd) + " " + kit.Sym(kit.GlyphRunning) + " " +
			kit.Colorize(flatDuration(time.Since(t.started)), theme.RoleMuted)
	}
	return flatToolLine(kit.GlyphRunning, t.verb, t.arg, []string{flatDuration(time.Since(t.started))}, kit.ContentWidth())
}

// flatToolOutput prints one row of streamed output under the running call,
// committing the call's line first.
func (r *UIRenderer) flatToolOutput(line string) {
	r.flatMu.Lock()
	defer r.flatMu.Unlock()
	if !r.flat.header {
		fmt.Println(flatToolLine(kit.GlyphRunning, r.flat.verb, r.flat.arg, nil, kit.ContentWidth()))
		r.flat.header = true
	}
	for _, sub := range strings.Split(line, "\n") {
		role := theme.RoleMuted
		if rest, ok := strings.CutPrefix(sub, "ERR: "); ok {
			sub, role = rest, theme.RoleWarning
		}
		flatGutter(sub, role)
		r.flat.streamed++
	}
}

// FlatToolDone closes the running call with its final line and, depending
// on the style, the result under the rail. exit is the exit status a failed
// command reported, empty when the failure carried none; the caller reads
// it from the error itself, never from the text of the output.
func (r *UIRenderer) FlatToolDone(output string, failed bool, elapsed time.Duration, exit string) {
	r.flatMu.Lock()
	t := r.flat
	r.flat = flatTool{}
	r.flatMu.Unlock()
	if !t.open {
		return // already closed by an error line
	}
	glyph := kit.GlyphSuccess
	if failed {
		glyph = kit.GlyphError
	}
	meta := flatToolMeta(output, elapsed, exit)
	if !t.header {
		fmt.Println(flatToolLine(glyph, t.verb, t.arg, meta, kit.ContentWidth()))
	}
	if body, role := r.flatResultBody(output, failed, t.streamed > 0); body != "" {
		flatGutter(body, role)
	}
	if t.header {
		fmt.Println("  " + kit.Sym(kit.GlyphGutterEnd) + " " + kit.Sym(glyph) + " " +
			kit.Colorize(strings.Join(meta, " · "), theme.RoleMuted))
	}
}

// flatResultBody picks what of a result goes under the rail. A failure
// always says why. A success shows nothing in the compact style, its first
// line in the minimal one and the whole result (within the budget) in the
// full one — unless the output already streamed by, which would print it
// twice.
func (r *UIRenderer) flatResultBody(output string, failed, streamed bool) (string, theme.Role) {
	output = strings.TrimSpace(output)
	if output == "" || (streamed && !failed) || (r.IsCompact() && !failed) {
		return "", theme.RoleMuted
	}
	role := theme.RoleMuted
	if failed {
		role = theme.RoleToolError
	}
	if r.IsFull() {
		if len(output) > flatResultBudget {
			output = textfold.Cut(output, flatResultBudget) + i18n.T("agent.ui.result_truncated")
		}
		return output, role
	}
	if failed {
		// The reason is the last thing a failing command prints.
		output = output[strings.LastIndex(output, "\n")+1:]
	} else if i := strings.Index(output, "\n"); i >= 0 {
		output = output[:i]
	}
	return output, role
}

// FlatOutcome is why a call of the flat timeline produced no result. The
// loop knows which it was and says so; the renderer does not guess it from
// how far the call got, because the same point in the loop is reached by a
// user saying no, a rule, a hook and a plain bad argument.
type FlatOutcome int

const (
	// FlatFailed: the call was rejected or broke before it could run.
	FlatFailed FlatOutcome = iota
	// FlatDenied: the user was asked and said no.
	FlatDenied
	// FlatBlocked: a policy rule or a hook stopped it; nobody was asked.
	FlatBlocked
	// FlatSkipped: not attempted, because an earlier call of the batch failed.
	FlatSkipped
	// FlatCanceled: the user interrupted the prompt or the run.
	FlatCanceled
	// FlatParked: the agent suspended itself on this call.
	FlatParked
)

func (o FlatOutcome) word() string {
	switch o {
	case FlatDenied:
		return i18n.T("agent.flat.denied")
	case FlatBlocked:
		return i18n.T("agent.flat.blocked")
	case FlatSkipped:
		return i18n.T("agent.flat.skipped")
	case FlatCanceled:
		return i18n.T("agent.flat.canceled")
	case FlatParked:
		return i18n.T("agent.flat.parked")
	default:
		return i18n.T("agent.flat.failed")
	}
}

// glyph is the failure mark for a call that broke and the not-run mark for
// every outcome in which the call was never attempted or was given up.
func (o FlatOutcome) glyph() kit.Glyph {
	if o == FlatFailed {
		return kit.GlyphError
	}
	return kit.GlyphDenied
}

// FlatToolRefused closes the announced call without a result: its line
// carries the outcome's glyph and word, and msg, when there is one, says
// why under the rail. With no call announced a message stands alone.
func (r *UIRenderer) FlatToolRefused(outcome FlatOutcome, msg string) {
	r.flatMu.Lock()
	t := r.flat
	r.flat = flatTool{}
	r.flatMu.Unlock()
	msg = strings.TrimSpace(msg)
	switch {
	case !t.open:
		if msg != "" {
			fmt.Println("  " + kit.Sym(kit.GlyphError) + " " + kit.Colorize(kit.Plain(msg), theme.RoleToolError))
		}
		return
	case !t.header:
		fmt.Println(flatToolLine(outcome.glyph(), t.verb, t.arg, []string{outcome.word()}, kit.ContentWidth()))
	}
	if msg != "" {
		flatGutter(msg, theme.RoleToolError)
	}
	if t.header {
		// Output already hangs from the call's line: close the rail.
		fmt.Println("  " + kit.Sym(kit.GlyphGutterEnd) + " " + kit.Sym(outcome.glyph()) + " " + kit.Colorize(outcome.word(), theme.RoleMuted))
	}
}

// FlatAgentDone draws one dispatched agent the way a tool call is drawn:
// state glyph, the agent, its task, and on the right how long it took and
// how many tool calls it made. A failure says why under the rail; a result
// follows the style, as a tool result does.
func (r *UIRenderer) FlatAgentDone(agentName, task, output string, failed bool, elapsed time.Duration, toolCalls, parallel int) {
	glyph := kit.GlyphSuccess
	if failed {
		glyph = kit.GlyphError
	}
	meta := []string{flatDuration(elapsed), flatCallCount(toolCalls)}
	if parallel > 1 {
		meta = append(meta, i18n.T("agent.flat.parallel", parallel))
	}
	fmt.Println(flatToolLine(glyph, agentName, strings.Join(strings.Fields(task), " "), meta, kit.ContentWidth()))
	if body, role := r.flatResultBody(output, failed, false); body != "" {
		flatGutter(body, role)
	}
}

// FlatAgentSummary closes a dispatch with one line: how many agents
// finished, and what the dispatch cost in tool calls and time.
func (r *UIRenderer) FlatAgentSummary(done, total, toolCalls int, elapsed time.Duration) {
	glyph := kit.GlyphSuccess
	if done < total {
		glyph = kit.GlyphError
	}
	fmt.Println(flatToolLine(glyph, i18n.T("agent.flat.agents"), fmt.Sprintf("%d/%d", done, total),
		[]string{flatDuration(elapsed), flatCallCount(toolCalls)}, kit.ContentWidth()))
}

func flatCallCount(n int) string {
	if n == 1 {
		return i18n.T("agent.flat.tool_call")
	}
	return i18n.T("agent.flat.tool_calls", n)
}

// flatBlockTitle is the title line of a prose block (plan, note, answer)
// in the flat timeline. The framed title carries the block's state in its
// hue; a monochrome theme has one accent, so the state moves to the glyph
// and the title is plain strong text.
func flatBlockTitle(title, color string) string {
	glyph := kit.GlyphAssistant
	switch color {
	case ColorRed:
		glyph = kit.GlyphError
	case ColorGreen:
		glyph = kit.GlyphSuccess
	}
	return "  " + kit.Sym(glyph) + " " + theme.ANSI(theme.RoleTextStrong) + "\033[1m" +
		strings.TrimSpace(kit.Plain(title)) + theme.Reset()
}

// flatToolActive reports whether a flat call is being drawn, which is when
// the framed stream section and its output rows give way to the rail.
func (r *UIRenderer) flatToolActive() bool {
	if !r.FlatLayout() {
		return false
	}
	r.flatMu.Lock()
	defer r.flatMu.Unlock()
	return r.flat.open
}

// flatToolLine lays one call out on width columns: the argument is cut so
// the metadata always fits and ends at the right edge.
func flatToolLine(g kit.Glyph, verb, arg string, meta []string, width int) string {
	right := strings.Join(meta, " · ")
	left := "  " + kit.Sym(g) + " " + theme.ANSI(theme.RoleTextStrong) + kit.PadRight(verb, flatVerbPad) + theme.Reset()
	room := width - kit.VisibleLen(left) - 1 - kit.VisibleLen(right)
	if right != "" {
		room -= 2
	}
	if room < 8 {
		room = 8
	}
	if arg != "" {
		left += " " + kit.Colorize(kit.Truncate(arg, room), theme.RoleText)
	}
	if right == "" {
		return left
	}
	gap := width - kit.VisibleLen(left) - kit.VisibleLen(right)
	if gap < 2 {
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + kit.Colorize(right, theme.RoleMuted)
}

// flatGutter prints text under the rail, wrapped to the content width with
// its indentation kept (tool output is YAML, JSON and tables).
func flatGutter(text string, role theme.Role) {
	rail := "  " + kit.Sym(kit.GlyphGutter) + " "
	width := kit.ContentWidth() - kit.VisibleLen(rail)
	if width < 20 {
		width = 20
	}
	for _, ln := range kit.TrimBlankBorderRows(kit.WrapPreserve(kit.Plain(text), width)) {
		fmt.Println(rail + kit.Colorize(ln, role))
	}
}

// flatToolMeta is the right-hand side of a finished call: how long it took,
// the exit status a failing command reported, and how much it returned.
func flatToolMeta(output string, elapsed time.Duration, exit string) []string {
	meta := []string{flatDuration(elapsed)}
	if exit != "" {
		meta = append(meta, i18n.T("agent.flat.exit", exit))
	}
	if n := countLines(output); n == 1 {
		meta = append(meta, i18n.T("agent.flat.line"))
	} else if n > 1 {
		meta = append(meta, i18n.T("agent.flat.lines", n))
	}
	return meta
}

func countLines(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// flatDuration keeps durations short and of similar width: milliseconds
// under a second, one decimal under a minute, whole seconds after.
func flatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

// flatSalientKeys are the argument names that identify a call, most
// telling first, for tools the coder verbs do not cover.
var flatSalientKeys = []string{"url", "query", "q", "path", "file", "cmd", "command", "term", "pattern", "name", "id", "task", "prompt", "text"}

// flatToolLabel splits a call into the verb shown in the strong tone and
// the one argument worth a place on the line.
func flatToolLabel(tool, subcmd, rawArgs string) (verb, arg string) {
	name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tool)), "@")
	sub := strings.TrimSpace(subcmd)
	if strings.ContainsAny(sub, "{<\"=") || strings.HasPrefix(sub, "-") || sub == "unknown" {
		sub = "" // not a subcommand: the first word of a JSON or flag blob
	}
	switch {
	case name == "" || name == "coder":
		verb = sub
	case sub == "" || sub == name:
		verb = name
	default:
		verb = name + " " + sub
	}
	if verb == "" {
		verb = name
	}
	arg = extractPrimaryArg(subcmd, rawArgs)
	if arg == "" {
		arg = flatSalientArg(rawArgs)
	}
	if arg == sub {
		arg = "" // {"cmd":"status"}: the subcommand is already the verb
	}
	return verb, strings.Join(strings.Fields(arg), " ")
}

// flatSalientArg finds the identifying argument in a JSON call, looking
// inside the {"cmd":…,"args":{…}} envelope when there is one.
func flatSalientArg(rawArgs string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(rawArgs), &fields) != nil {
		return ""
	}
	if inner, ok := fields["args"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(inner, &nested) == nil {
			fields = nested
		}
	}
	for _, key := range flatSalientKeys {
		var s string
		if raw, ok := fields[key]; ok && json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	return ""
}
