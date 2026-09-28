/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/i18n"
)

// The page's session commands: /clear, /retry, /rewind and /plan act on
// the page's live session and its bound saved session, so the server
// serves them itself through optional backend capabilities; a backend
// without one answers like any other command the page cannot run.

// TurnRecord is how a turn was asked: what /retry asks again.
type TurnRecord struct {
	// Mode is chat, coder or agent; empty runs in the page's current mode.
	Mode    string
	Text    string
	Options TurnOptions
}

// RewindResult reports what a rewind removed. Restore puts the removed
// turns back when nothing was added to the session since, and reports
// whether it did.
type RewindResult struct {
	Turns    int
	Messages int
	Restore  func() bool
}

// TurnEditor is the capability behind /rewind and /retry: drop the last
// turns of a live session (persisted to its bound saved session) and say
// how the last one was asked.
type TurnEditor interface {
	LastTurn(session string) (TurnRecord, bool)
	RewindTurns(session string, n int) (RewindResult, error)
}

// PlanController is the capability behind /plan: it applies the command as
// the terminal does (arming Plan-First) and reports which mode should run
// which task; mode is empty when there is nothing to run.
type PlanController interface {
	PlanCommand(ctx context.Context, line string) (mode, task, notice string)
}

// SlashExpander expands slash-command templates ("/review 12") into the
// prompt they stand for, the capability ACP uses for the same purpose.
type SlashExpander interface {
	ExpandSlashCommand(ctx context.Context, session, text string) (string, bool)
}

// SkillStager stages a user-invocable skill for the next turn ("/<skill>
// args") and returns the prompt that turn sends. ok is false when name is
// not a skill; refused carries the notice for a skill that cannot be
// invoked by hand.
type SkillStager interface {
	StageSkill(name, args string) (prompt, refused string, ok bool)
}

// WebCommandRunner runs a command under the page's rules; a backend without
// it runs RunCommand.
type WebCommandRunner interface {
	RunWebCommand(ctx context.Context, session, line string) (string, error)
}

// sessionEvent tells the page its session changed under it: the bound
// saved session and the transcript to show now.
type sessionEvent struct {
	Bound   string              `json:"bound"`
	History []map[string]string `json:"history"`
	Notice  string              `json:"notice,omitempty"`
}

// webRefusals are the terminal commands the page cannot run, each with the
// reason and what to do instead.
var webRefusals = map[string]string{
	"/exit": "web.command.refuse.exit", "/quit": "web.command.refuse.exit",
	"/menu":     "web.command.refuse.menu",
	"/update":   "web.command.refuse.update",
	"/auth":     "web.command.refuse.auth",
	"/gateway":  "web.command.refuse.daemon",
	"/watch":    "web.command.refuse.daemon",
	"/connect":  "web.command.refuse.daemon",
	"/worktree": "web.command.refuse.worktree",
	"/reload":   "web.command.refuse.reload",
	"/wait":     "web.command.refuse.wait",
	"/retryall": "web.command.refuse.chunks", "/nextchunk": "web.command.refuse.chunks", "/skipchunk": "web.command.refuse.chunks",
	"/reset": "web.command.refuse.redraw", "/redraw": "web.command.refuse.redraw",
}

// routeWebCommand serves the page's own commands. matched reports whether
// the line was one of them; handled, as for routeSlash, whether the line
// is done (false with mode and text rewritten runs a task).
func (s *Server) routeWebCommand(ctx context.Context, rn *run, session, line string, mode, text *string) (handled, matched bool) {
	token, rest := splitLeadingToken(line)
	token = strings.ToLower(token)
	sub, _ := splitLeadingToken(rest)
	switch {
	case token == "/clear" || token == "/newsession" || (token == "/session" && strings.EqualFold(sub, "new")):
		// A new conversation in the page is the "+ New" action: the
		// previous one stays saved and listed; /newsession would leave the
		// page unbound, so it takes the same path.
		return s.cmdClear(ctx, rn, session), true
	case token == "/session" && s.commandAdvertised(token):
		return s.cmdSession(ctx, rn, session, line), true
	case token == "/rewind":
		if te, ok := s.opts.Backend.(TurnEditor); ok {
			return s.cmdRewind(ctx, rn, te, session, rest), true
		}
	case token == "/retry":
		if te, ok := s.opts.Backend.(TurnEditor); ok {
			return s.cmdRetry(ctx, rn, te, session, *mode), true
		}
	case token == "/plan":
		if pc, ok := s.opts.Backend.(PlanController); ok {
			return s.cmdPlan(ctx, rn, pc, session, line, mode, text), true
		}
	}
	if key, ok := webRefusals[token]; ok {
		rn.sink.push(event{Type: "done", Reply: i18n.T(key, token)})
		return true, true
	}
	return false, false
}

// expandTemplate turns a slash-command template into the prompt it stands
// for, to run as a turn in the page's mode. Run as a headless command
// instead, the terminal handler fires the turn in the background and the
// command itself prints nothing.
func (s *Server) expandTemplate(ctx context.Context, session, line string, text *string) bool {
	exp, ok := s.opts.Backend.(SlashExpander)
	if !ok {
		return false
	}
	expanded, isTemplate := exp.ExpandSlashCommand(ctx, session, line)
	if !isTemplate {
		return false
	}
	*text = expanded
	return true
}

// routeSkill turns "/<skill> args" into a turn with that skill staged, the
// way the terminal invokes a skill by hand.
func (s *Server) routeSkill(rn *run, token, rest string, text *string) (handled, matched bool) {
	st, ok := s.opts.Backend.(SkillStager)
	if !ok {
		return false, false
	}
	prompt, refused, isSkill := st.StageSkill(strings.TrimPrefix(token, "/"), rest)
	switch {
	case !isSkill:
		return false, false
	case refused != "":
		rn.sink.push(event{Type: "done", Reply: refused})
		return true, true
	}
	*text = prompt
	return false, true
}

// runCommand runs a headless command under the page's rules.
func (s *Server) runCommand(ctx context.Context, session, line string) (string, error) {
	if wr, ok := s.opts.Backend.(WebCommandRunner); ok {
		return wr.RunWebCommand(ctx, session, line)
	}
	return s.opts.Backend.RunCommand(ctx, session, line)
}

// startFresh starts a new conversation for the live session: the current
// one is cleared from it (its saved session keeps it) and a fresh
// web-owned session is bound, so the new conversation is written through
// and listed like every other one. Binding is best effort: a backend that
// does not bind keeps the cleared live session.
func (s *Server) startFresh(ctx context.Context, session string) (string, error) {
	prev := s.boundOf(session)
	out, err := s.opts.Backend.ManageSession(ctx, "clear", session, "")
	if err != nil {
		return out, err
	}
	// Names carry the second: a second new conversation within it must not
	// reattach to, and reload, the one just left.
	name := FreshSessionName()
	if name == prev {
		name += "-" + newRunID()[:4]
	}
	if _, err := s.opts.Backend.ManageSession(ctx, "attach", session, name); err != nil {
		s.log.Warn("web: binding the new session", zap.Error(err))
	}
	return out, nil
}

func (s *Server) pushSession(ctx context.Context, rn *run, session, notice string, extra ...map[string]string) {
	hist := s.historyItems(ctx, session)
	hist = append(hist, extra...)
	rn.sink.push(event{Type: "session", Session: &sessionEvent{Bound: s.boundOf(session), History: hist, Notice: notice}})
}

func (s *Server) cmdClear(ctx context.Context, rn *run, session string) bool {
	prev := s.boundOf(session)
	if _, err := s.startFresh(ctx, session); err != nil {
		rn.sink.push(event{Type: "error", Error: i18n.T("web.command_failed", err.Error())})
		return true
	}
	notice := i18n.T("web.command.clear.done")
	if prev != "" && s.sessionListed(prev) {
		notice = i18n.T("web.command.clear.kept", prev)
	}
	s.pushSession(ctx, rn, session, notice)
	rn.sink.push(event{Type: "done"})
	return true
}

func (s *Server) sessionListed(name string) bool {
	for _, sum := range s.opts.Backend.SessionCatalog() {
		if sum.Name == name {
			return true
		}
	}
	return false
}

// cmdSession runs /session and then shows the page what it did to the
// session: load, attach, detach and fork change what the page is bound to
// and shows.
func (s *Server) cmdSession(ctx context.Context, rn *run, session, line string) bool {
	out, err := s.runCommand(ctx, session, line)
	if err != nil {
		rn.sink.push(event{Type: "error", Error: i18n.T("web.command_failed", err.Error())})
		return true
	}
	s.pushSession(ctx, rn, session, "")
	rn.sink.push(event{Type: "done", Reply: out})
	return true
}

func (s *Server) cmdRewind(ctx context.Context, rn *run, te TurnEditor, session, rest string) bool {
	n := 1
	if arg, _ := splitLeadingToken(rest); arg != "" {
		v, err := strconv.Atoi(arg)
		if err != nil || v < 1 {
			rn.sink.push(event{Type: "done", Reply: i18n.T("web.command.rewind.usage")})
			return true
		}
		n = v
	}
	res, err := te.RewindTurns(session, n)
	if err != nil {
		rn.sink.push(event{Type: "error", Error: i18n.T("web.command_failed", err.Error())})
		return true
	}
	if res.Turns == 0 {
		rn.sink.push(event{Type: "done", Reply: i18n.T("web.command.rewind.nothing")})
		return true
	}
	s.pushSession(ctx, rn, session, i18n.T("web.command.rewind.done", res.Turns, res.Messages))
	rn.sink.push(event{Type: "done"})
	return true
}

// cmdRetry asks the last turn again, the same way it was asked, in place
// of the reply it got. A retry that fails before adding anything puts the
// previous turn back.
func (s *Server) cmdRetry(ctx context.Context, rn *run, te TurnEditor, session, pageMode string) bool {
	rec, ok := te.LastTurn(session)
	if !ok {
		rn.sink.push(event{Type: "done", Reply: i18n.T("web.command.retry.nothing")})
		return true
	}
	res, err := te.RewindTurns(session, 1)
	if err != nil {
		rn.sink.push(event{Type: "error", Error: i18n.T("web.command_failed", err.Error())})
		return true
	}
	mode := rec.Mode
	if mode == "" {
		mode = normalizeMode(pageMode)
	}
	s.pushSession(ctx, rn, session, i18n.T("web.command.retry.running", i18n.T("web.ui.mode."+normalizeMode(mode))), map[string]string{"role": "user", "content": rec.Text})
	reply, err := s.dispatchTurn(ctx, rn, session, mode, rec.Text, rec.Options)
	if err != nil && res.Restore != nil && res.Restore() {
		s.pushSession(ctx, rn, session, i18n.T("web.command.retry.restored"))
	}
	s.finishTurn(ctx, rn, mode, reply, err)
	return true
}

// cmdPlan applies /plan. With a task it runs that task plan-first in the
// mode /plan names; otherwise it arms Plan-First as the terminal does and
// shows the session's current plan in the plan panel.
func (s *Server) cmdPlan(ctx context.Context, rn *run, pc PlanController, session, line string, mode, text *string) bool {
	m, task, notice := pc.PlanCommand(ctx, line)
	if m != "" && strings.TrimSpace(task) != "" {
		*mode, *text = m, task
		return false
	}
	s.mu.Lock()
	entries := s.plans[session]
	s.mu.Unlock()
	parts := []string{}
	if n := strings.TrimSpace(notice); n != "" {
		parts = append(parts, n)
	}
	if len(entries) > 0 {
		rn.sink.push(event{Type: "plan", Plan: entries})
		parts = append(parts, i18n.T("web.command.plan.shown", len(entries)))
	} else {
		parts = append(parts, i18n.T("web.command.plan.none"))
	}
	parts = append(parts, i18n.T("web.command.plan.how"))
	rn.sink.push(event{Type: "done", Reply: strings.Join(parts, "\n\n")})
	return true
}

// rememberPlan keeps the last plan a run of the session showed, for /plan.
func (s *Server) rememberPlan(session string, entries []planEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plans == nil {
		s.plans = map[string][]planEntry{}
	}
	s.plans[session] = entries
}

// dispatchTurn runs one turn in its mode.
func (s *Server) dispatchTurn(ctx context.Context, rn *run, session, mode, text string, o TurnOptions) (string, error) {
	b := s.opts.Backend
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "coder":
		return b.RunCoder(ctx, session, text, o, rn.sink)
	case "agent":
		return b.RunAgent(ctx, session, text, o, rn.sink)
	default:
		return b.Chat(ctx, session, text, o, rn.sink)
	}
}

// finishTurn sends the final event of a turn.
func (s *Server) finishTurn(ctx context.Context, rn *run, mode, reply string, err error) {
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || ctx.Err() != nil):
		rn.sink.push(event{Type: "done", Reply: reply, Cancelled: true})
	case err != nil:
		s.log.Warn("web: turn failed", zap.String("mode", mode), zap.Error(err))
		rn.sink.push(event{Type: "error", Error: err.Error(), Reply: reply})
	default:
		rn.sink.push(event{Type: "done", Reply: reply})
	}
}
