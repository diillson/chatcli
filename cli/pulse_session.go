/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strconv"

	"github.com/diillson/chatcli/pkg/pulse"
)

// Session persistence on the live dashboard: every save and load of the
// session store — REPL save, exit autosave, MCP mirrors, gateway
// write-through — is one span under the session, with the message count,
// the byte count and whether the file is sealed at rest. Never the session
// name, its path or its contents.

// sessionIOStat is what one save or load moved through the disk.
type sessionIOStat struct {
	bytes     int
	encrypted bool
}

// pulseSessionMessages counts the messages a session carries across its
// scoped histories, "" for none.
func pulseSessionMessages(sd *SessionData) string {
	if sd == nil {
		return ""
	}
	return strconv.Itoa(len(sd.ChatHistory) + len(sd.AgentHistory) + len(sd.CoderHistory) + len(sd.SharedMemory))
}

// pulseBeginSessionIO opens the span of a session save or load. sd, when
// known up front (a save), sets the message count.
func pulseBeginSessionIO(name string, sd *SessionData) *pulse.Span {
	if !pulse.Enabled() {
		return nil
	}
	return pulse.Begin(pulse.KindBackground, name, "").With("messages", pulseSessionMessages(sd))
}

// pulseEndSessionIO closes a session span with what went through the disk.
func pulseEndSessionIO(span *pulse.Span, st sessionIOStat, err error) {
	if span == nil {
		return
	}
	if st.bytes > 0 {
		span = span.With("bytes", strconv.Itoa(st.bytes)).With("encrypted", strconv.FormatBool(st.encrypted))
	}
	span.EndErr(err)
}
