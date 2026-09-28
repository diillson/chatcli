/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * cmd — web_turns.go
 *
 * The turns of a web session, for the page's /rewind and /retry. A live
 * history is a flat list of messages: a chat turn adds its turn context,
 * the user's message and the reply; a coder turn adds run context, skills,
 * the task and any number of tool exchanges, some of them user-role
 * messages no flag tells from typed text. So the backend remembers each
 * turn the page asked for — the typed message that opened it, by
 * fingerprint, and how many messages it spanned — and finds turns other
 * surfaces made (the terminal writing the same bound session) by their
 * typed user messages. Fingerprints, not indexes: a coder run replaces or
 * prepends its system message, which moves every index after it.
 */
package cmd

import (
	"context"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/webui"
	"github.com/diillson/chatcli/models"
)

// webTurnMarksKept bounds the turns remembered per session; older turns
// are still found by their typed user message.
const webTurnMarksKept = 200

// webTurnImagesKept is how many of the newest turns keep their attached
// images for /retry, which only ever asks the last turn again.
const webTurnImagesKept = 4

// webTurnMark is one turn the page asked for.
type webTurnMark struct {
	anchor string // fingerprint of the typed message that opened the turn
	span   int    // messages from that message to the end of the turn
	rec    webui.TurnRecord
}

var (
	_ webui.TurnEditor       = (*rpcBackend)(nil)
	_ webui.PlanController   = (*rpcBackend)(nil)
	_ webui.SlashExpander    = (*rpcBackend)(nil)
	_ webui.SkillStager      = (*rpcBackend)(nil)
	_ webui.WebCommandRunner = (*rpcBackend)(nil)
)

func msgFingerprint(m models.Message) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(m.Role))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(m.Content))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(m.ToolCallID))
	return strconv.FormatUint(h.Sum64(), 16) + "." + strconv.Itoa(len(m.Images))
}

// typedUserMessage reports whether m is something a person sent, as far as
// the message itself can tell: a user message ChatCLI did not inject.
func typedUserMessage(m models.Message) bool {
	if m.Role != "user" || m.ToolCallID != "" || m.IsInjectedContext() {
		return false
	}
	if m.Meta != nil && (m.Meta.AgentFeedback || m.Meta.SkillNames != "" || m.Meta.IsSummary) {
		return false
	}
	return strings.TrimSpace(m.Content) != "" || len(m.Images) > 0
}

// turnPreamble reports whether m is context ChatCLI placed before a turn's
// typed message (turn and run context, the run's skills): it belongs to the
// turn it opens.
func turnPreamble(m models.Message) bool {
	return m.Role == "user" && (m.IsInjectedContext() || (m.Meta != nil && m.Meta.SkillNames != ""))
}

// liveHistory is a copy of the session's live history.
func (b *rpcBackend) liveHistory(session string) []models.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]models.Message(nil), b.sessions[session]...)
}

// recordTurn remembers the turn the page just ran: the first typed message
// in after that before did not have, and how far the turn reaches.
func (b *rpcBackend) recordTurn(session string, before, after []models.Message, rec webui.TurnRecord) {
	seen := make(map[string]int, len(before))
	for _, m := range before {
		seen[msgFingerprint(m)]++
	}
	anchor := -1
	for i, m := range after {
		fp := msgFingerprint(m)
		if seen[fp] > 0 {
			seen[fp]--
			continue
		}
		if typedUserMessage(m) {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return
	}
	mark := webTurnMark{anchor: msgFingerprint(after[anchor]), span: len(after) - anchor, rec: rec}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turns == nil {
		b.turns = map[string][]webTurnMark{}
	}
	marks := append(b.turns[session], mark)
	if len(marks) > webTurnMarksKept {
		marks = marks[len(marks)-webTurnMarksKept:]
	}
	for i := 0; i < len(marks)-webTurnImagesKept; i++ {
		marks[i].rec.Options.Images = nil
	}
	b.turns[session] = marks
}

// dropTurns forgets the remembered turns of a session whose history was
// replaced wholesale.
func (b *rpcBackend) dropTurns(session string) {
	b.mu.Lock()
	delete(b.turns, session)
	b.mu.Unlock()
}

// turnStarts returns where each turn of hist starts, oldest first, and for
// the starts that open a remembered turn, which mark it is.
func turnStarts(hist []models.Message, marks []webTurnMark) ([]int, map[int]int) {
	covered := make([]bool, len(hist))
	owners := map[int]int{}
	type found struct{ at, mark int }
	var located []found
	from := 0
	for mi, mk := range marks {
		at := -1
		for i := from; i < len(hist); i++ {
			if typedUserMessage(hist[i]) && msgFingerprint(hist[i]) == mk.anchor {
				at = i
				break
			}
		}
		if at < 0 {
			continue
		}
		for i := at; i < at+mk.span && i < len(hist); i++ {
			covered[i] = true
		}
		located = append(located, found{at, mi})
		from = at + 1
	}
	extend := func(i int) int {
		for i > 0 && !covered[i-1] && turnPreamble(hist[i-1]) {
			i--
		}
		return i
	}
	set := map[int]bool{}
	for _, f := range located {
		st := extend(f.at)
		owners[st] = f.mark
		set[st] = true
	}
	for i, m := range hist {
		if !covered[i] && typedUserMessage(m) {
			set[extend(i)] = true
		}
	}
	starts := make([]int, 0, len(set))
	for st := range set {
		starts = append(starts, st)
	}
	sort.Ints(starts)
	return starts, owners
}

// LastTurn implements webui.TurnEditor: how the last turn was asked. A turn
// the page ran comes back exactly (mode, route, attachments); one another
// surface made comes back as its typed text and images, for the page's
// current mode and route.
func (b *rpcBackend) LastTurn(session string) (webui.TurnRecord, bool) {
	b.refreshBound(session)
	b.mu.Lock()
	defer b.mu.Unlock()
	hist, marks := b.sessions[session], b.turns[session]
	starts, owners := turnStarts(hist, marks)
	if len(starts) == 0 {
		return webui.TurnRecord{}, false
	}
	last := starts[len(starts)-1]
	if mi, ok := owners[last]; ok {
		return marks[mi].rec, true
	}
	for i := last; i < len(hist); i++ {
		if typedUserMessage(hist[i]) {
			rec := webui.TurnRecord{Text: hist[i].Content}
			for _, im := range hist[i].Images {
				rec.Options.Images = append(rec.Options.Images, webui.ImageInput{Name: im.FileName, MediaType: im.MediaType, Data: im.Data})
			}
			return rec, true
		}
	}
	return webui.TurnRecord{}, false
}

// RewindTurns implements webui.TurnEditor: the last n turns leave the live
// history, and the result is written to the bound saved session and the
// autosave mirror, so the terminal sharing the session sees the same
// conversation. Only the conversation rewinds — like the terminal's
// /rewind, files a coder turn changed stay as they are.
func (b *rpcBackend) RewindTurns(session string, n int) (webui.RewindResult, error) {
	if n < 1 {
		n = 1
	}
	b.refreshBound(session)
	b.mu.Lock()
	hist, marks := b.sessions[session], b.turns[session]
	starts, owners := turnStarts(hist, marks)
	if len(starts) == 0 {
		b.mu.Unlock()
		return webui.RewindResult{}, nil
	}
	if n > len(starts) {
		n = len(starts)
	}
	cut := starts[len(starts)-n]
	kept := keptAfterRewind(hist[:cut])
	var keptMarks []webTurnMark
	for _, st := range starts[:len(starts)-n] {
		if mi, ok := owners[st]; ok {
			keptMarks = append(keptMarks, marks[mi])
		}
	}
	b.sessions[session] = kept
	if b.turns == nil {
		b.turns = map[string][]webTurnMark{}
	}
	b.turns[session] = keptMarks
	b.mu.Unlock()
	b.persistEdit(session, kept)

	restore := func() bool {
		b.mu.Lock()
		cur := b.sessions[session]
		if !sameTail(cur, kept) {
			b.mu.Unlock()
			return false
		}
		b.sessions[session] = hist
		b.turns[session] = marks
		b.mu.Unlock()
		b.persistEdit(session, hist)
		return true
	}
	return webui.RewindResult{Turns: n, Messages: len(hist) - len(kept), Restore: restore}, nil
}

// keptAfterRewind is what remains of a history cut at a turn start: its
// copy, with no half tool exchange at the edge and no lone system prompt a
// run left behind.
func keptAfterRewind(prefix []models.Message) []models.Message {
	kept := trimDanglingToolPairs(append([]models.Message(nil), prefix...))
	for _, m := range kept {
		if m.Role != "system" {
			return kept
		}
	}
	return []models.Message{}
}

func sameTail(a, b []models.Message) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || msgFingerprint(a[len(a)-1]) == msgFingerprint(b[len(b)-1])
}

// persistEdit writes an edited history where turns write theirs. Unlike a
// turn, an edit may leave the session empty, and that is written too.
func (b *rpcBackend) persistEdit(session string, hist []models.Message) {
	b.autosaveSession(session, hist)
	b.persistBound(session, hist)
}

// PlanCommand implements webui.PlanController.
func (b *rpcBackend) PlanCommand(ctx context.Context, line string) (string, string, string) {
	if b.cli == nil {
		return "", "", ""
	}
	return b.cli.PlanCommandRPC(ctx, line)
}

// StageSkill implements webui.SkillStager.
func (b *rpcBackend) StageSkill(name, args string) (string, string, bool) {
	if b.cli == nil {
		return "", "", false
	}
	return b.cli.StageSkillRPC(name, args)
}

// RunWebCommand implements webui.WebCommandRunner: RunCommand's per-session
// /session handling, then the page's rules for the rest.
func (b *rpcBackend) RunWebCommand(ctx context.Context, session, line string) (string, error) {
	if b.cli == nil {
		return "", errCLIUnavailable
	}
	if f := strings.Fields(line); len(f) > 0 && (f[0] == "/session" || f[0] == "/newsession") {
		out, err := b.RunCommand(ctx, session, line)
		// A loaded or new conversation is another one: its turns are
		// not the ones remembered.
		if f[0] == "/newsession" || (len(f) > 1 && (f[1] == "load" || f[1] == "attach" || f[1] == "new")) {
			b.dropTurns(session)
		}
		return out, err
	}
	return b.cli.RunWebCommandRPC(ctx, line)
}
