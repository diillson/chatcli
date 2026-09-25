/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/imagegen"
	"github.com/diillson/chatcli/models"
)

// apiError is the JSON body of every failed API call.
type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

func readJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", i18n.T("web.bad_request", err))
		return false
	}
	return true
}

// apiRoute is one entry of the route table: method, resource and how many
// trailing path segments it takes (-1 = any).
type apiRoute struct {
	method   string
	resource string
	segments int
	handle   func(s *Server, w http.ResponseWriter, r *http.Request, rest []string)
}

// routes is the API surface. GET reads, POST acts; nothing on this surface
// deletes a file the engine did not already delete through its own
// commands.
var routes = []apiRoute{
	{http.MethodGet, "boot", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleBoot(w, r) }},
	{http.MethodGet, "status", 0, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) {
		writeJSON(w, http.StatusOK, s.opts.Backend.Status())
	}},
	{http.MethodGet, "providers", 0, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) { s.handleProviders(w) }},
	{http.MethodPost, "defaults", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleDefaults(w, r) }},
	{http.MethodPost, "turn", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleTurn(w, r) }},
	{http.MethodPost, "runs", 2, func(s *Server, w http.ResponseWriter, r *http.Request, rest []string) {
		s.handleRunAction(w, r, rest[0], rest[1])
	}},
	{http.MethodGet, "sessions", 0, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": s.opts.Backend.SessionCatalog()})
	}},
	{http.MethodGet, "sessions", 2, func(s *Server, w http.ResponseWriter, r *http.Request, rest []string) {
		if rest[1] != "messages" {
			writeErr(w, http.StatusNotFound, "not_found", i18n.T("web.unknown_endpoint", r.URL.Path))
			return
		}
		s.handleSessionMessages(w, r, rest[0])
	}},
	{http.MethodPost, "session", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleSessionAction(w, r) }},
	{http.MethodGet, "history", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleHistory(w, r) }},
	{http.MethodGet, "tools", 0, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"tools": s.opts.Backend.Tools()})
	}},
	{http.MethodPost, "tools", 1, func(s *Server, w http.ResponseWriter, r *http.Request, rest []string) { s.handleTool(w, r, rest[0]) }},
	{http.MethodGet, "skills", 0, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"skills": s.opts.Backend.Skills()})
	}},
	{http.MethodGet, "skills", 1, func(s *Server, w http.ResponseWriter, _ *http.Request, rest []string) { s.handleSkill(w, rest[0]) }},
	{http.MethodGet, "commands", 0, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"commands": s.opts.Backend.Commands()})
	}},
	{http.MethodPost, "command", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleCommand(w, r) }},
	{http.MethodGet, "resources", 0, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"resources": s.opts.Backend.Resources()})
	}},
	{http.MethodGet, "resource", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleResource(w, r) }},
	{http.MethodPost, "tts", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleTTS(w, r) }},
	{http.MethodPost, "stt", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleSTT(w, r) }},
	{http.MethodPost, "image", 0, func(s *Server, w http.ResponseWriter, r *http.Request, _ []string) { s.handleImage(w, r) }},
}

// routeAPI dispatches /api/<resource>[/...] through the route table.
func (s *Server) routeAPI(w http.ResponseWriter, r *http.Request) {
	segs := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	rest := segs[1:]
	for _, rt := range routes {
		if rt.method == r.Method && rt.resource == segs[0] && rt.segments == len(rest) {
			rt.handle(s, w, r, rest)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "not_found", i18n.T("web.unknown_endpoint", r.URL.Path))
}

func (s *Server) handleBoot(w http.ResponseWriter, r *http.Request) {
	b := s.opts.Backend
	providers := json.RawMessage("null")
	if raw, err := b.ProvidersJSON(); err == nil {
		providers = json.RawMessage(raw)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"boot":      s.bootData(),
		"status":    b.Status(),
		"providers": providers,
		"tools":     b.Tools(),
		"skills":    b.Skills(),
		"commands":  b.Commands(),
		"resources": b.Resources(),
		"sessions":  b.SessionCatalog(),
		"history":   s.historyItems(r.Context(), r.URL.Query().Get("session")),
	})
}

func (s *Server) handleProviders(w http.ResponseWriter) {
	raw, err := s.opts.Backend.ProvidersJSON()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "providers", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(w, raw)
}

func (s *Server) handleDefaults(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.opts.Backend.SetDefaults(strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model))
	p, m := s.opts.Backend.Defaults()
	writeJSON(w, http.StatusOK, map[string]string{"provider": p, "model": m})
}

// turnRequest is what the page sends to start a turn.
type turnRequest struct {
	Session  string `json:"session"`
	Mode     string `json:"mode"` // chat | coder | agent
	Text     string `json:"text"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Images   []struct {
		Name      string `json:"name"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"` // base64
	} `json:"images"`
}

func (s *Server) sessionOf(v string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return "web"
}

func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// handleTurn runs one turn and streams it as server-sent events. One turn
// at a time: the engine serializes runs, so a second request is refused
// with 409 instead of queueing silently behind the first.
func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	var req turnRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "empty", i18n.T("web.empty_turn"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	opts, err := turnOptionsFrom(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_image", err.Error())
		return
	}

	// The run outlives the request only long enough to be cancelled and
	// drained: it inherits the request's values, not its cancellation, so
	// a page that disconnects is handled below rather than by a stray cancel.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	rn := &run{id: newRunID(), cancel: cancel, done: make(chan struct{})}
	rn.sink = newRunSink(runCtx, rn.id, s.opts.PermissionTimeout)
	s.mu.Lock()
	if s.closed || s.active != nil {
		s.mu.Unlock()
		cancel()
		writeErr(w, http.StatusConflict, "busy", i18n.T("web.busy"))
		return
	}
	s.active = rn
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.active == rn {
			s.active = nil
		}
		s.mu.Unlock()
		cancel()
	}()

	session := s.sessionOf(req.Session)
	go s.execute(runCtx, rn, session, req.Mode, req.Text, opts)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s.writeEvent(w, flusher, event{Type: "run", Run: rn.id})

	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-rn.sink.events:
			if !ok {
				return
			}
			s.writeEvent(w, flusher, ev)
		case <-tick.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			// The page went away: the run is cancelled, not left running
			// blind with nobody to answer its permission dialogs.
			cancel()
			<-rn.done
			return
		}
	}
}

func (s *Server) writeEvent(w http.ResponseWriter, f http.Flusher, ev event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	f.Flush()
}

// execute drives the backend for one run and closes the sink with a final
// done or error event.
func (s *Server) execute(ctx context.Context, rn *run, session, mode, text string, o TurnOptions) {
	defer close(rn.done)
	defer rn.sink.close()
	var (
		reply string
		err   error
	)
	b := s.opts.Backend
	if s.routeInline(ctx, rn, session, &mode, &text) {
		return
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "coder":
		reply, err = b.RunCoder(ctx, session, text, o, rn.sink)
	case "agent":
		reply, err = b.RunAgent(ctx, session, text, o, rn.sink)
	default:
		reply, err = b.Chat(ctx, session, text, o, rn.sink)
	}
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

func turnOptionsFrom(req turnRequest) (TurnOptions, error) {
	o := TurnOptions{Provider: strings.TrimSpace(req.Provider), Model: strings.TrimSpace(req.Model)}
	for _, img := range req.Images {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(img.Data))
		if err != nil {
			return o, fmt.Errorf("%s", i18n.T("web.bad_image", img.Name))
		}
		mime := img.MediaType
		if _, ok := models.NormalizeImageMediaType(mime); !ok {
			if sniffed, ok2 := models.DetectImageMediaType(data); ok2 {
				mime = sniffed
			} else {
				return o, fmt.Errorf("%s", i18n.T("web.bad_image", img.Name))
			}
		}
		o.Images = append(o.Images, ImageInput{Name: img.Name, MediaType: mime, Data: data})
	}
	return o, nil
}

// handleRunAction answers a permission dialog or cancels the run.
func (s *Server) handleRunAction(w http.ResponseWriter, r *http.Request, runID, action string) {
	s.mu.Lock()
	rn := s.active
	s.mu.Unlock()
	if rn == nil || rn.id != runID {
		writeErr(w, http.StatusNotFound, "no_run", i18n.T("web.no_such_run"))
		return
	}
	switch action {
	case "cancel":
		rn.cancel()
		writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
	case "permission":
		var req struct {
			ID       string `json:"id"`
			Decision string `json:"decision"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		d := agentevents.PermissionDecision(req.Decision)
		switch d {
		case agentevents.PermissionAllowOnce, agentevents.PermissionAllowAlways, agentevents.PermissionDenyOnce, agentevents.PermissionDenyAlways:
		default:
			writeErr(w, http.StatusBadRequest, "bad_decision", i18n.T("web.bad_decision", req.Decision))
			return
		}
		if !rn.sink.resolve(req.ID, d) {
			writeErr(w, http.StatusNotFound, "no_dialog", i18n.T("web.no_such_dialog"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"decision": req.Decision})
	default:
		writeErr(w, http.StatusNotFound, "not_found", i18n.T("web.unknown_endpoint", r.URL.Path))
	}
}

func (s *Server) historyItems(ctx context.Context, session string) []map[string]string {
	items, err := s.opts.Backend.RestoreSession(ctx, s.sessionOf(session))
	if err != nil {
		return []map[string]string{}
	}
	out := make([]map[string]string, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]string{"role": it.Role, "content": it.Content})
	}
	return out
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"history": s.historyItems(r.Context(), r.URL.Query().Get("session"))})
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request, name string) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	msgs, total, err := s.opts.Backend.SessionMessages(name, offset, limit)
	if err != nil {
		writeErr(w, http.StatusNotFound, "session", err.Error())
		return
	}
	type msgOut struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		Images  int    `json:"images,omitempty"`
		Tools   int    `json:"tool_calls,omitempty"`
	}
	out := make([]msgOut, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, msgOut{Role: m.Role, Content: m.Content, Images: len(m.Images), Tools: len(m.ToolCalls)})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": out, "total": total, "offset": offset})
}

func (s *Server) handleSessionAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string `json:"action"`
		Session string `json:"session"`
		Name    string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.opts.Backend.ManageSession(r.Context(), strings.TrimSpace(req.Action), s.sessionOf(req.Session), strings.TrimSpace(req.Name))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "session", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"result": out, "sessions": s.opts.Backend.SessionCatalog(), "history": s.historyItems(r.Context(), req.Session)})
}

func (s *Server) handleTool(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Args string `json:"args"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.opts.Backend.CallTool(r.Context(), name, req.Args)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "tool", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (s *Server) handleSkill(w http.ResponseWriter, name string) {
	content, err := s.opts.Backend.SkillContent(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "skill", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "content": content})
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		Line    string `json:"line"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	out, err := s.opts.Backend.RunCommand(r.Context(), s.sessionOf(req.Session), strings.TrimSpace(req.Line))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "command", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"output": out, "status": s.opts.Backend.Status()})
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	uri := r.URL.Query().Get("uri")
	rc, err := s.opts.Backend.ReadResource(r.Context(), uri)
	if err != nil {
		writeErr(w, http.StatusNotFound, "resource", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rc)
}

func (s *Server) handleTTS(w http.ResponseWriter, r *http.Request) {
	if !s.features().Voice {
		writeErr(w, http.StatusNotImplemented, "voice_off", i18n.T("web.voice_off"))
		return
	}
	var req struct {
		Text   string `json:"text"`
		Voice  string `json:"voice"`
		Format string `json:"format"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	audio, err := s.opts.Voice.Synthesize(r.Context(), req.Text, req.Voice, req.Format)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "tts", err.Error())
		return
	}
	w.Header().Set("Content-Type", audio.Mime)
	_, _ = w.Write(audio.Data)
}

func (s *Server) handleSTT(w http.ResponseWriter, r *http.Request) {
	if !s.features().STT {
		writeErr(w, http.StatusNotImplemented, "stt_off", i18n.T("web.stt_off"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMediaBody)
	audio, err := io.ReadAll(r.Body)
	if err != nil || len(audio) == 0 {
		writeErr(w, http.StatusBadRequest, "stt", i18n.T("web.empty_audio"))
		return
	}
	text, err := s.opts.STT.Transcribe(r.Context(), audio, r.Header.Get("Content-Type"), r.URL.Query().Get("filename"), r.URL.Query().Get("lang"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "stt", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	if !s.features().Images {
		writeErr(w, http.StatusNotImplemented, "images_off", i18n.T("web.images_off"))
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
		Size   string `json:"size"`
		N      int    `json:"n"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	imgs, err := s.opts.Images.Generate(r.Context(), req.Prompt, imagegen.Options{Size: req.Size, N: req.N})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "image", err.Error())
		return
	}
	type out struct {
		Mime string `json:"mime"`
		Data string `json:"data"`
	}
	res := make([]out, 0, len(imgs))
	for _, im := range imgs {
		res = append(res, out{Mime: im.Mime, Data: base64.StdEncoding.EncodeToString(im.Data)})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"images": res})
}
