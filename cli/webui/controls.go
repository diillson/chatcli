/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"net/http"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// Completion is one completion the page offers for the word being typed.
type Completion struct {
	Text        string `json:"text"`
	Description string `json:"description,omitempty"`
}

// Controls is the optional seam behind the page's switches and argument
// completion. A backend that implements it runs them through the same
// code the terminal commands use; without it the page shows the lists
// read-only and completes command names only.
type Controls interface {
	// SetMCPServer starts (on) or stops (off) one configured MCP server.
	SetMCPServer(name string, on bool) error
	// SkillState lists the pinned skills and the manual-only ones, which
	// cannot be pinned.
	SkillState() (pinned, manualOnly []string)
	// SetSkillPinned pins (on) or unpins (off) a skill.
	SetSkillPinned(name string, on bool) error
	// Complete completes the last word of a slash command line.
	Complete(line string) []Completion
}

// maxCompletionLine bounds the line the page sends for completion: a
// command line, not a message body.
const maxCompletionLine = 4096

func (s *Server) controls() (Controls, bool) {
	c, ok := s.opts.Backend.(Controls)
	return c, ok
}

// skillsPayload is the skills list with the switch state when the backend
// has controls.
func (s *Server) skillsPayload() map[string]interface{} {
	out := map[string]interface{}{"skills": s.opts.Backend.Skills()}
	if c, ok := s.controls(); ok {
		pinned, manual := c.SkillState()
		out["pinned"] = nonNil(pinned)
		out["manual_only"] = nonNil(manual)
		out["controls"] = true
	}
	return out
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Server) handleMCPSwitch(w http.ResponseWriter, r *http.Request, name string) {
	c, ok := s.controls()
	if !ok {
		writeErr(w, http.StatusNotImplemented, "unsupported", i18n.T("web.controls.unsupported"))
		return
	}
	var req struct {
		On *bool `json:"on"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.On == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", i18n.T("web.controls.on_required"))
		return
	}
	if err := c.SetMCPServer(name, *req.On); err != nil {
		writeErr(w, http.StatusConflict, "mcp", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"mcp": s.opts.Backend.Status().MCP})
}

func (s *Server) handleSkillSwitch(w http.ResponseWriter, r *http.Request, name string) {
	c, ok := s.controls()
	if !ok {
		writeErr(w, http.StatusNotImplemented, "unsupported", i18n.T("web.controls.unsupported"))
		return
	}
	var req struct {
		Pinned *bool `json:"pinned"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Pinned == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", i18n.T("web.controls.pinned_required"))
		return
	}
	if err := c.SetSkillPinned(name, *req.Pinned); err != nil {
		writeErr(w, http.StatusConflict, "skill", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.skillsPayload())
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	line := r.URL.Query().Get("line")
	items := []Completion{}
	if c, ok := s.controls(); ok && len(line) <= maxCompletionLine && strings.HasPrefix(line, "/") {
		if got := c.Complete(line); got != nil {
			items = got
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}
