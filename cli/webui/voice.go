/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/transcription"
	"github.com/diillson/chatcli/llm/tts"
)

// Voice directions, as the page names them.
const (
	dirSTT = "stt"
	dirTTS = "tts"
)

// Install job states.
const (
	jobIdle      = "idle"
	jobRunning   = "running"
	jobDone      = "done"
	jobFailed    = "failed"
	jobCancelled = "cancelled"
)

// voiceRoutes extend the API table with the voice engine endpoints:
// GET /api/voice/status, POST /api/voice/install and POST /api/voice/cancel.
var voiceRoutes = []apiRoute{
	{http.MethodGet, "voice", 1, func(s *Server, w http.ResponseWriter, r *http.Request, rest []string) {
		if rest[0] != "status" {
			writeErr(w, http.StatusNotFound, "not_found", i18n.T("web.unknown_endpoint", r.URL.Path))
			return
		}
		writeJSON(w, http.StatusOK, s.voice.status())
	}},
	{http.MethodPost, "voice", 1, func(s *Server, w http.ResponseWriter, r *http.Request, rest []string) {
		switch rest[0] {
		case "install":
			s.handleVoiceInstall(w, r)
		case "cancel":
			s.voice.cancelInstall()
			writeJSON(w, http.StatusOK, s.voice.status())
		default:
			writeErr(w, http.StatusNotFound, "not_found", i18n.T("web.unknown_endpoint", r.URL.Path))
		}
	}},
}

func init() { routes = append(routes, voiceRoutes...) }

// voiceState is the page's engine choice per direction and the one-time
// install of the embedded engines. The page prefers the embedded engines
// (see transcription.SelectPreferEmbedded) and asks before downloading
// them; the rest of the process keeps its own defaults.
type voiceState struct {
	log *zap.Logger

	mu     sync.Mutex
	stt    transcription.Provider
	tts    tts.Provider
	sttSel *transcription.Selection
	ttsSel *tts.Selection
	job    installJob
	cancel context.CancelFunc
	closed bool
	ended  chan struct{} // closed when the latest install attempt settles
}

// installJob is the progress of the running (or last) install.
type installJob struct {
	State  string `json:"state"`
	Target string `json:"target,omitempty"`
	Phase  string `json:"phase,omitempty"`
	File   string `json:"file,omitempty"`
	Done   int64  `json:"done"`
	Total  int64  `json:"total"`
	Error  string `json:"error,omitempty"`
}

// voiceDirection is one direction as GET /api/voice/status reports it.
type voiceDirection struct {
	Engine         string        `json:"engine"`
	Ready          bool          `json:"ready"`
	Source         string        `json:"source"`
	PreferEmbedded bool          `json:"prefer_embedded"`
	Offer          bool          `json:"offer"`
	Embedded       embeddedFacts `json:"embedded"`
}

type embeddedFacts struct {
	Name          string `json:"name"`
	Supported     bool   `json:"supported"`
	Installed     bool   `json:"installed"`
	DownloadBytes int64  `json:"download_bytes"`
}

type voiceStatus struct {
	STT     voiceDirection `json:"stt"`
	TTS     voiceDirection `json:"tts"`
	Install installJob     `json:"install"`
}

func newVoiceState(o Options) *voiceState {
	v := &voiceState{log: o.Logger, stt: o.STT, tts: o.Voice, job: installJob{State: jobIdle}}
	if o.STTChoice != nil {
		sel := *o.STTChoice
		v.sttSel, v.stt = &sel, sel.Active
	}
	if o.VoiceChoice != nil {
		sel := *o.VoiceChoice
		v.ttsSel, v.tts = &sel, sel.Active
	}
	return v
}

// sttProvider is the transcription engine serving now, nil when none.
func (v *voiceState) sttProvider() transcription.Provider {
	v.mu.Lock()
	defer v.mu.Unlock()
	if transcription.IsNull(v.stt) {
		return nil
	}
	return v.stt
}

// ttsProvider is the synthesis engine serving now, nil when none.
func (v *voiceState) ttsProvider() tts.Provider {
	v.mu.Lock()
	defer v.mu.Unlock()
	if tts.IsNull(v.tts) {
		return nil
	}
	return v.tts
}

// offers reports whether the page may offer each direction's embedded
// install: supported, not installed, and either preferred or the only way
// to get that direction working.
func (v *voiceState) offers() (stt, voice bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.sttOfferLocked(), v.ttsOfferLocked()
}

func (v *voiceState) sttOfferLocked() bool {
	s := v.sttSel
	return s != nil && s.NewEmbedded != nil && s.Embedded.Supported && !s.Embedded.Installed &&
		(s.PreferEmbedded || transcription.IsNull(v.stt))
}

func (v *voiceState) ttsOfferLocked() bool {
	s := v.ttsSel
	return s != nil && s.NewEmbedded != nil && s.Embedded.Supported && !s.Embedded.Installed &&
		(s.PreferEmbedded || tts.IsNull(v.tts))
}

func (v *voiceState) status() voiceStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := voiceStatus{Install: v.job}
	out.STT = voiceDirection{Source: transcription.SourceEnv, Offer: v.sttOfferLocked()}
	if !transcription.IsNull(v.stt) {
		out.STT.Engine, out.STT.Ready = v.stt.Name(), true
	}
	if s := v.sttSel; s != nil {
		out.STT.Source, out.STT.PreferEmbedded = s.Source, s.PreferEmbedded
		out.STT.Embedded = embeddedFacts{s.Embedded.Name, s.Embedded.Supported, s.Embedded.Installed, s.Embedded.DownloadBytes}
	}
	out.TTS = voiceDirection{Source: tts.SourceEnv, Offer: v.ttsOfferLocked()}
	if !tts.IsNull(v.tts) {
		out.TTS.Engine, out.TTS.Ready = v.tts.Name(), true
	}
	if s := v.ttsSel; s != nil {
		out.TTS.Source, out.TTS.PreferEmbedded = s.Source, s.PreferEmbedded
		out.TTS.Embedded = embeddedFacts{s.Embedded.Name, s.Embedded.Supported, s.Embedded.Installed, s.Embedded.DownloadBytes}
	}
	return out
}

// startInstall provisions the offered embedded engines among targets in the
// background: detached from the request that asked (the page polls the
// status), ended by a cancel or the server shutdown. A second request while
// one runs joins it. It reports false when there is nothing to install.
func (v *voiceState) startInstall(parent context.Context, targets []string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.job.State == jobRunning {
		return true
	}
	if v.closed {
		return false
	}
	var dirs []string
	for _, t := range dedupe(targets) {
		if (t == dirSTT && v.sttOfferLocked()) || (t == dirTTS && v.ttsOfferLocked()) {
			dirs = append(dirs, t)
		}
	}
	if len(dirs) == 0 {
		return false
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	v.cancel = cancel
	v.ended = make(chan struct{})
	v.job = installJob{State: jobRunning, Target: dirs[0], Total: -1}
	go v.runInstall(ctx, cancel, dirs, v.ended)
	return true
}

// installEnded is closed once the latest install attempt has settled its
// final state; nil before the first attempt.
func (v *voiceState) installEnded() <-chan struct{} {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.ended
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (v *voiceState) runInstall(ctx context.Context, cancel context.CancelFunc, dirs []string, ended chan struct{}) {
	defer cancel()
	var err error
	for _, d := range dirs {
		if err = v.installOne(ctx, d); err != nil {
			break
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	defer close(ended)
	v.cancel = nil
	v.refreshEmbeddedLocked()
	switch {
	case err == nil:
		v.job = installJob{State: jobDone}
	case ctx.Err() != nil:
		v.job = installJob{State: jobCancelled, Error: i18n.T("web.voice_install_cancelled")}
	default:
		v.job.State, v.job.Error = jobFailed, err.Error()
		v.log.Warn("web: embedded voice install failed", zap.Error(err))
	}
}

// installOne provisions one direction with a fresh embedded provider (a
// cancelled attempt must not latch its error into the next one) and, once
// ready, makes it the active engine.
func (v *voiceState) installOne(ctx context.Context, dir string) error {
	v.mu.Lock()
	v.job = installJob{State: jobRunning, Target: dir, Total: -1}
	sttSel, ttsSel := v.sttSel, v.ttsSel
	v.mu.Unlock()
	progress := func(phase, file string, done, total int64) {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.job.Phase, v.job.File, v.job.Done, v.job.Total = phase, file, done, total
	}
	switch dir {
	case dirSTT:
		p := sttSel.NewEmbedded()
		prov, ok := p.(transcription.Provisioner)
		if !ok {
			return errors.New(i18n.T("web.voice_not_installable", p.Name()))
		}
		pctx := transcription.WithProvisionProgress(ctx, func(pp transcription.ProvisionProgress) { progress(pp.Phase, pp.File, pp.Done, pp.Total) })
		if err := prov.EnsureReady(pctx); err != nil {
			return err
		}
		v.mu.Lock()
		v.stt = p
		v.sttSel.Embedded.Installed, v.sttSel.Embedded.DownloadBytes = true, 0
		if v.sttSel.Source != transcription.SourceEnv {
			v.sttSel.Source = transcription.SourceEmbedded
		}
		v.mu.Unlock()
	case dirTTS:
		p := ttsSel.NewEmbedded()
		prov, ok := p.(tts.Provisioner)
		if !ok {
			return errors.New(i18n.T("web.voice_not_installable", p.Name()))
		}
		pctx := tts.WithProvisionProgress(ctx, func(pp tts.ProvisionProgress) { progress(pp.Phase, pp.File, pp.Done, pp.Total) })
		if err := prov.EnsureReady(pctx); err != nil {
			return err
		}
		v.mu.Lock()
		v.tts = p
		v.ttsSel.Embedded.Installed, v.ttsSel.Embedded.DownloadBytes = true, 0
		if v.ttsSel.Source != tts.SourceEnv {
			v.ttsSel.Source = tts.SourceEmbedded
		}
		v.mu.Unlock()
	}
	return nil
}

// refreshEmbeddedLocked re-reads the embedded facts from the cache after
// an install attempt: a cancelled or failed one may still have landed a
// piece, which the next offer must not count again.
func (v *voiceState) refreshEmbeddedLocked() {
	if s := v.sttSel; s != nil && s.NewEmbedded != nil && !s.Embedded.Installed {
		if info, ok := transcription.DescribeEmbedded(s.NewEmbedded()); ok {
			s.Embedded = info
		}
	}
	if s := v.ttsSel; s != nil && s.NewEmbedded != nil && !s.Embedded.Installed {
		if info, ok := tts.DescribeEmbedded(s.NewEmbedded()); ok {
			s.Embedded = info
		}
	}
}

// shutdown cancels a running install and refuses new ones.
func (v *voiceState) shutdown() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	if v.cancel != nil {
		v.cancel()
	}
}

func (v *voiceState) cancelInstall() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cancel != nil {
		v.cancel()
	}
}

func (s *Server) handleVoiceInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Targets []string `json:"targets"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Targets) == 0 {
		req.Targets = []string{dirSTT, dirTTS}
	}
	if !s.voice.startInstall(r.Context(), req.Targets) {
		writeErr(w, http.StatusBadRequest, "voice_nothing", i18n.T("web.voice_nothing_to_install"))
		return
	}
	writeJSON(w, http.StatusAccepted, s.voice.status())
}

// audioKind sniffs the container from the bytes (browsers label the same
// recording differently): wav, ogg, webm, mp4 or "" when unknown.
func audioKind(b []byte) string {
	switch {
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WAVE":
		return "wav"
	case bytes.HasPrefix(b, []byte("OggS")):
		return "ogg"
	case bytes.HasPrefix(b, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return "webm"
	case len(b) >= 8 && string(b[4:8]) == "ftyp":
		return "mp4"
	}
	return ""
}

// sttLanguage maps a language hint to what the engines take: the primary
// subtag of a BCP 47 tag ("pt-BR" is "pt"), empty for automatic detection.
func sttLanguage(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	if tag == "auto" {
		return ""
	}
	return tag
}
