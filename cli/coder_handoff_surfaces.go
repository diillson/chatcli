/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * coder_handoff_surfaces.go
 *
 * The chat → coder handoff outside the terminal. A headless chat turn runs
 * for a surface that may have its own way to ask the user: the web page
 * shows the proposal as a card, ACP opens the client's permission dialog,
 * MCP elicits. Such a surface sets RPCChatOpts.Handoff, the model gets the
 * same instruction the REPL turn gets, and the proposed task comes back in
 * RPCChatTurn.HandoffTask for the surface to confirm. Nothing here starts
 * coder mode: the surface does, after the user said yes.
 *
 * The proposal is a line of markup at the end of the reply. On a streamed
 * turn it would reach the client before the reply is cleaned, so the stream
 * goes through handoffStreamFilter, which ends the visible text where the
 * proposal starts.
 */
package cli

import "strings"

// handoffOpener is the start of the proposal element, lower case.
const handoffOpener = "<" + coderHandoffTag

// handoffStreamFilter forwards streamed reply text up to the handoff
// proposal. Text that may still turn out to be the opening of the proposal
// is held until the next chunk settles it.
type handoffStreamFilter struct {
	next ChunkSink
	held string
	cut  bool
}

// handoffFilteredSink wraps a stream sink with the filter. flush releases
// what the filter still holds once the attempt is over. A nil sink stays
// nil, so the caller keeps its non-streaming path.
func handoffFilteredSink(next ChunkSink) (ChunkSink, func()) {
	if next == nil {
		return nil, func() {}
	}
	f := &handoffStreamFilter{next: next}
	return f, f.flush
}

// Chunk implements ChunkSink.
func (f *handoffStreamFilter) Chunk(text string) {
	if f.cut || text == "" {
		return
	}
	buf := f.held + text
	f.held = ""
	if at := handoffOpenerIndex(buf); at >= 0 {
		f.cut = true
		f.emit(buf[:at])
		return
	}
	// A trailing "<", "<cod", "< coder_h"… may be the proposal arriving in
	// pieces; anything else is ordinary text.
	if at := strings.LastIndexByte(buf, '<'); at >= 0 && handoffOpenerPrefix(buf[at:]) {
		f.held = buf[at:]
		buf = buf[:at]
	}
	f.emit(buf)
}

func (f *handoffStreamFilter) emit(text string) {
	if text != "" {
		f.next.Chunk(text)
	}
}

// flush sends text that was held as a possible opening and never became
// one (a reply that ends in "<").
func (f *handoffStreamFilter) flush() {
	if !f.cut {
		f.emit(f.held)
	}
	f.held = ""
}

// squeezeOpener lower-cases a fragment that starts with "<" and drops the
// whitespace the parser tolerates after it.
func squeezeOpener(fragment string) string {
	return "<" + strings.ToLower(strings.TrimLeft(fragment[1:], " \t\r\n\f\v"))
}

// handoffOpenerIndex is where the proposal element starts in text, -1 when
// it does not. It accepts what coderHandoffRe accepts: any letter case and
// whitespace after the "<".
func handoffOpenerIndex(text string) int {
	for from := 0; ; {
		at := strings.IndexByte(text[from:], '<')
		if at < 0 {
			return -1
		}
		at += from
		if strings.HasPrefix(squeezeOpener(text[at:]), handoffOpener) {
			return at
		}
		from = at + 1
	}
}

// handoffOpenerPrefix reports whether fragment (starting at "<") is an
// incomplete opening of the proposal element.
func handoffOpenerPrefix(fragment string) bool {
	return strings.HasPrefix(handoffOpener, squeezeOpener(fragment))
}
