/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// While the agent works, one goroutine reads stdin and keeps the line the
// user is typing (the type-ahead draft). When the loop stops to ask the
// user something — a permission prompt, a confirmation — the answer comes
// through that same reader, and the draft was still in its buffer: typing
// "y" produced "<draft>y", which no prompt accepts, and the action was
// denied. The draft is therefore set aside ("parked") for as long as a
// question is open and put back once it is answered, so the user neither
// loses what they were writing nor has it read as their answer.

// stdinDraft is the reader goroutine's view of a parked draft. Questions
// can nest (a confirmation raised from inside another prompt's handling),
// so parking is counted and the draft returns when the last one closes.
type stdinDraft struct {
	parked string
	depth  int
}

// park sets the line being typed aside and returns it. Only the outermost
// park moves text; nested ones find the buffer already clean.
func (d *stdinDraft) park(line *strings.Builder) string {
	d.depth++
	if d.depth == 1 {
		d.parked = line.String()
		line.Reset()
	}
	return d.parked
}

// restore puts the parked draft back in front of whatever was typed since
// the answer (normally nothing) once the outermost question closes.
func (d *stdinDraft) restore(line *strings.Builder) {
	if d.depth == 0 {
		return
	}
	d.depth--
	if d.depth > 0 || d.parked == "" {
		return
	}
	rest := line.String()
	line.Reset()
	line.WriteString(d.parked + rest)
	d.parked = ""
}

// pending is the full draft a reader that is shutting down must hand over:
// what is parked plus what is being typed.
func (d *stdinDraft) pending(line *strings.Builder) string {
	return d.parked + line.String()
}

// stdinDraftReq asks the reader goroutine to park or restore the draft.
type stdinDraftReq struct {
	park  bool
	reply chan string
}

// stdinDraftWait bounds how long a prompt waits for the reader to act on a
// request. The reader polls every 50ms; when it cannot answer in time (it
// is blocked handing a line over) the prompt goes ahead without parking,
// which is the behavior before this existed.
const stdinDraftWait = 300 * time.Millisecond

// requestStdinDraft sends one request to the reader and waits for it. ok
// is false when there is no reader or it did not answer in time.
func requestStdinDraft(ctl chan stdinDraftReq, done <-chan struct{}, park bool) (draft string, ok bool) {
	if ctl == nil {
		return "", false
	}
	req := stdinDraftReq{park: park, reply: make(chan string, 1)}
	timeout := time.NewTimer(stdinDraftWait)
	defer timeout.Stop()
	select {
	case ctl <- req:
	case <-done:
		return "", false
	case <-timeout.C:
		return "", false
	}
	select {
	case draft = <-req.reply:
		return draft, true
	case <-done:
		return "", false
	case <-timeout.C:
		return "", false
	}
}

// parkTypeaheadDraft sets the user's half-typed line aside while a question
// is open and returns the function that puts it back. Safe to call with no
// reader running (it does nothing then).
func (a *AgentMode) parkTypeaheadDraft() (restore func()) {
	a.stdinMu.Lock()
	ctl, done := a.stdinCtl, a.stdinDone
	a.stdinMu.Unlock()
	draft, ok := requestStdinDraft(ctl, done, true)
	if !ok {
		return func() {}
	}
	a.announceParkedDraft(draft)
	return func() {
		// A reader replaced meanwhile (a suspend for an overlay) already
		// carried the parked draft over to its successor on the way out.
		a.stdinMu.Lock()
		same := a.stdinCtl == ctl
		a.stdinMu.Unlock()
		if same {
			_, _ = requestStdinDraft(ctl, done, false)
		}
	}
}

// draftNoticeRepeat is how long the same draft stays announced: a question
// read line by line parks the draft once per line and must say so once.
const draftNoticeRepeat = 2 * time.Minute

// announceParkedDraft tells the user their draft was set aside, so text
// vanishing from the input line is not read as lost.
func (a *AgentMode) announceParkedDraft(draft string) {
	shown := sanitizeTypeaheadPreview(draft)
	if strings.TrimSpace(shown) == "" {
		return
	}
	a.typeaheadMu.Lock()
	repeat := draft == a.draftNoticed && time.Since(a.draftNoticedAt) < draftNoticeRepeat
	a.draftNoticed, a.draftNoticedAt = draft, time.Now()
	a.typeaheadMu.Unlock()
	if !repeat {
		parked := i18n.T("agent.typeahead.draft_parked", shown)
		fmt.Println(flatNoticeOr(noticeInfo, parked, colorize("  ✎ "+parked, ColorGray)))
	}
}
