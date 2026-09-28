/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/transcription"
	"github.com/diillson/chatcli/llm/tts"
)

// fakeSTT records what the handler hands the engine.
type fakeSTT struct {
	name string
	err  error
	mu   sync.Mutex
	got  []string // mime|filename|lang|first bytes
}

func (f *fakeSTT) Name() string { return f.name }
func (f *fakeSTT) Transcribe(_ context.Context, audio []byte, mime, filename, lang string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head := audio
	if len(head) > 4 {
		head = head[:4]
	}
	f.got = append(f.got, mime+"|"+filename+"|"+lang+"|"+string(head))
	if f.err != nil {
		return "", f.err
	}
	return "  hello there  ", nil
}

// fakeEmbeddedSTT is an installable engine whose download waits for the
// test (or for the context to end). When started is set, the engine
// announces itself there as its download begins, so a test releases the
// engine that is really installing (the server also builds engines only to
// describe the cache).
type fakeEmbeddedSTT struct {
	fakeSTT
	release chan struct{}
	err     error
	started chan *fakeEmbeddedSTT
}

func (f *fakeEmbeddedSTT) EnsureReady(ctx context.Context) error {
	if f.started != nil {
		f.started <- f
	}
	select {
	case <-f.release:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type fakeTTS struct {
	name string
	mu   sync.Mutex
	got  []string
}

func (f *fakeTTS) Name() string { return f.name }
func (f *fakeTTS) Synthesize(_ context.Context, text, _, _ string) (tts.Audio, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, text)
	return tts.Audio{Data: []byte("RIFFwav"), Mime: "audio/wav", Ext: "wav"}, nil
}

type fakeEmbeddedTTS struct {
	fakeTTS
	release chan struct{}
}

func (f *fakeEmbeddedTTS) EnsureReady(ctx context.Context) error {
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func startVoice(t *testing.T, o Options) *Server {
	t.Helper()
	o.Backend = newFakeBackend()
	srv, err := Start(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

// postRaw sends a raw body with a content type.
func postRaw(t *testing.T, srv *Server, path, ctype string, body []byte) (int, map[string]string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+srv.Host()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tokenHeader, srv.Token())
	req.Header.Set("Content-Type", ctype)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]string{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func voiceStatusOf(t *testing.T, srv *Server) voiceStatus {
	t.Helper()
	code, body := call(t, srv, http.MethodGet, "/api/voice/status", nil, true)
	if code != http.StatusOK {
		t.Fatalf("status = %d %s", code, body)
	}
	var st voiceStatus
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// waitInstall waits for the latest install attempt to settle, then checks
// the state it settled in. The timer only bounds a hang; it never decides
// the outcome.
func waitInstall(t *testing.T, srv *Server, want string) voiceStatus {
	t.Helper()
	waitClosed(t, srv.voice.installEnded(), "install to settle")
	st := voiceStatusOf(t, srv)
	if st.Install.State != want {
		t.Fatalf("install state = %+v, want %s", st.Install, want)
	}
	return st
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	if ch == nil {
		t.Fatalf("waiting for %s: no install was started", what)
	}
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// nextStarted returns the next engine whose download began.
func nextStarted(t *testing.T, started <-chan *fakeEmbeddedSTT) *fakeEmbeddedSTT {
	t.Helper()
	select {
	case e := <-started:
		return e
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for an install attempt to start")
		return nil
	}
}

// The page sends WAV; the handler names the file after the bytes (not the
// label), maps a BCP 47 hint to the primary subtag, falls back to the
// configured language, and trims the transcript.
func TestSTT_ContentTypesAndLanguage(t *testing.T) {
	stt := &fakeSTT{name: "fake-stt"}
	srv := startVoice(t, Options{STT: stt, STTLanguage: "pt-BR"})
	wav := append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), make([]byte, 32)...)
	cases := []struct {
		path, ctype string
		body        []byte
		want        string
	}{
		{"/api/stt?filename=voice.wav", "audio/wav", wav, "audio/wav|voice.wav|pt|RIFF"},
		{"/api/stt?filename=voice.webm&lang=en-US", "audio/wav", []byte{0x1A, 0x45, 0xDF, 0xA3, 1, 2}, "audio/wav|voice.webm|en|\x1a\x45\xdf\xa3"},
		{"/api/stt?lang=auto", "audio/ogg", []byte("OggS...."), "audio/ogg|voice.ogg|pt|OggS"},
		{"/api/stt?filename=voice.webm", "audio/mp4", []byte("\x00\x00\x00\x18ftypmp42"), "audio/mp4|voice.mp4|pt|\x00\x00\x00\x18"},
		{"/api/stt?filename=clip.bin", "application/octet-stream", []byte("????"), "application/octet-stream|clip.bin|pt|????"},
	}
	for _, c := range cases {
		code, out := postRaw(t, srv, c.path, c.ctype, c.body)
		if code != http.StatusOK || out["text"] != "hello there" || out["engine"] != "fake-stt" {
			t.Fatalf("%s = %d %v", c.path, code, out)
		}
		if got := stt.got[len(stt.got)-1]; got != c.want {
			t.Errorf("%s: engine saw %q, want %q", c.path, got, c.want)
		}
	}
	if code, out := postRaw(t, srv, "/api/stt", "audio/wav", nil); code != http.StatusBadRequest || out["code"] != "stt" {
		t.Errorf("empty body = %d %v", code, out)
	}
}

// Failures reach the page with the reason: a format the engine cannot decode
// is a 415 naming the format and the fix; anything else a 502 naming the
// engine.
func TestSTT_ErrorsAreExplicit(t *testing.T) {
	stt := &fakeSTT{name: "embedded:whisper/base", err: fmt.Errorf("wrapped: %w", transcription.ErrNeedsFFmpeg)}
	srv := startVoice(t, Options{STT: stt})
	code, out := postRaw(t, srv, "/api/stt", "audio/webm", []byte{0x1A, 0x45, 0xDF, 0xA3})
	if code != http.StatusUnsupportedMediaType || out["code"] != "stt_format" || !strings.Contains(out["error"], "webm") {
		t.Fatalf("undecodable = %d %v", code, out)
	}
	stt.err = errors.New("returned 429: no credits")
	code, out = postRaw(t, srv, "/api/stt", "audio/wav", []byte("RIFFxxxxWAVE"))
	if code != http.StatusBadGateway || out["code"] != "stt" || !strings.Contains(out["error"], "429") || !strings.Contains(out["error"], "embedded:whisper/base") {
		t.Fatalf("engine failure = %d %v", code, out)
	}
}

// Speech is flattened before synthesis, exactly like the gateway replies,
// and a reply that is only code has nothing to say.
func TestTTS_StripsMarkdownAndRefusesEmpty(t *testing.T) {
	voice := &fakeTTS{name: "fake-tts"}
	srv := startVoice(t, Options{Voice: voice})
	code, _ := call(t, srv, http.MethodPost, "/api/tts", map[string]string{"text": "**Hello** there.\n```go\nfmt.Println(1)\n```\nSee `x`."}, true)
	if code != http.StatusOK || len(voice.got) != 1 || strings.ContainsAny(voice.got[0], "*`") || strings.Contains(voice.got[0], "Println") {
		t.Fatalf("tts = %d, engine saw %q", code, voice.got)
	}
	code, body := call(t, srv, http.MethodPost, "/api/tts", map[string]string{"text": "```\nonly code\n```"}, true)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "tts_empty") || len(voice.got) != 1 {
		t.Fatalf("code-only = %d %s", code, body)
	}
}

// With the embedded engine preferred but not installed, the fallback serves
// and the install is offered; without a fallback the endpoint says the
// install is required (409), not that voice is off.
func TestVoice_OfferAndInstallRequired(t *testing.T) {
	fallback := &fakeSTT{name: "openai:whisper-1"}
	sel := &transcription.Selection{
		Active: fallback, Source: transcription.SourceAuto, PreferEmbedded: true,
		Embedded: transcription.EmbeddedInfo{Name: "embedded:whisper/base", Supported: true, DownloadBytes: 1234},
		NewEmbedded: func() transcription.Provider {
			return &fakeEmbeddedSTT{fakeSTT: fakeSTT{name: "embedded:whisper/base"}, release: make(chan struct{})}
		},
	}
	srv := startVoice(t, Options{STTChoice: sel})
	st := voiceStatusOf(t, srv)
	if !st.STT.Ready || !st.STT.Offer || !st.STT.PreferEmbedded || st.STT.Engine != "openai:whisper-1" || st.STT.Embedded.DownloadBytes != 1234 || st.STT.Source != "auto" {
		t.Fatalf("stt status = %+v", st.STT)
	}
	if st.TTS.Ready || st.TTS.Offer || st.Install.State != jobIdle {
		t.Fatalf("tts/install status = %+v %+v", st.TTS, st.Install)
	}

	none := *sel
	none.Active, none.Source = transcription.NewNull(), transcription.SourceNone
	srv2 := startVoice(t, Options{STTChoice: &none})
	if !srv2.features().STT {
		t.Fatal("an installable engine keeps the mic on the page")
	}
	if code, out := postRaw(t, srv2, "/api/stt", "audio/wav", []byte("RIFF")); code != http.StatusConflict || out["code"] != "voice_install" {
		t.Fatalf("stt without engine = %d %v", code, out)
	}
	if code, body := call(t, srv2, http.MethodPost, "/api/tts", map[string]string{"text": "hi"}, true); code != http.StatusNotImplemented || !strings.Contains(string(body), "voice_off") {
		t.Fatalf("tts without engine or offer = %d %s", code, body)
	}
}

// The install runs in the background with a fresh engine per attempt: a
// cancelled attempt reports cancelled, the next one completes and becomes
// the active engine for the next transcription.
func TestVoice_InstallCancelRetryAndSwap(t *testing.T) {
	started := make(chan *fakeEmbeddedSTT, 4)
	sel := &transcription.Selection{
		Active: transcription.NewNull(), Source: transcription.SourceNone, PreferEmbedded: true,
		Embedded: transcription.EmbeddedInfo{Name: "embedded:whisper/base", Supported: true, DownloadBytes: 99},
		NewEmbedded: func() transcription.Provider {
			return &fakeEmbeddedSTT{fakeSTT: fakeSTT{name: "embedded:whisper/base"}, release: make(chan struct{}), started: started}
		},
	}
	srv := startVoice(t, Options{STTChoice: sel})

	if code, body := call(t, srv, http.MethodPost, "/api/voice/install", map[string][]string{"targets": {"stt"}}, true); code != http.StatusAccepted || !strings.Contains(string(body), `"running"`) {
		t.Fatalf("install = %d %s", code, body)
	}
	first := nextStarted(t, started)
	// A second request joins the running install instead of starting another.
	if code, _ := call(t, srv, http.MethodPost, "/api/voice/install", map[string][]string{"targets": {"stt"}}, true); code != http.StatusAccepted {
		t.Fatalf("joined install = %d", code)
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/voice/cancel", nil, true); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if st := waitInstall(t, srv, jobCancelled); st.STT.Ready || st.Install.Error == "" {
		t.Fatalf("cancelled status = %+v", st)
	}

	if code, _ := call(t, srv, http.MethodPost, "/api/voice/install", map[string][]string{}, true); code != http.StatusAccepted {
		t.Fatalf("retry = %d", code)
	}
	retry := nextStarted(t, started)
	if retry == first {
		t.Fatal("the retry must build a fresh engine")
	}
	close(retry.release)
	st := waitInstall(t, srv, jobDone)
	if !st.STT.Ready || st.STT.Offer || !st.STT.Embedded.Installed || st.STT.Source != transcription.SourceEmbedded || st.STT.Engine != "embedded:whisper/base" {
		t.Fatalf("installed status = %+v", st.STT)
	}
	if code, out := postRaw(t, srv, "/api/stt", "audio/wav", []byte("RIFFxxxxWAVE")); code != http.StatusOK || out["engine"] != "embedded:whisper/base" {
		t.Fatalf("stt after install = %d %v", code, out)
	}
	if code, body := call(t, srv, http.MethodPost, "/api/voice/install", map[string][]string{"targets": {"stt"}}, true); code != http.StatusBadRequest || !strings.Contains(string(body), "voice_nothing") {
		t.Fatalf("nothing left to install = %d %s", code, body)
	}
}

// A failed install keeps the fallback and reports the reason; the voice
// output direction installs the same way; shutting the server down cancels
// a download in flight.
func TestVoice_TTSInstallFailureAndShutdown(t *testing.T) {
	failing := &transcription.Selection{
		Active: transcription.NewNull(), Source: transcription.SourceNone, PreferEmbedded: true,
		Embedded: transcription.EmbeddedInfo{Supported: true},
		NewEmbedded: func() transcription.Provider {
			rel := make(chan struct{})
			close(rel)
			return &fakeEmbeddedSTT{fakeSTT: fakeSTT{name: "embedded:whisper/base"}, release: rel, err: errors.New("download status 404")}
		},
	}
	release := make(chan struct{})
	voiceSel := &tts.Selection{
		Active: tts.NewNull(), Source: tts.SourceNone, PreferEmbedded: true,
		Embedded: tts.EmbeddedInfo{Name: "embedded:kokoro/bm_george", Supported: true, DownloadBytes: 7},
		NewEmbedded: func() tts.Provider {
			return &fakeEmbeddedTTS{fakeTTS: fakeTTS{name: "embedded:kokoro/bm_george"}, release: release}
		},
	}
	srv := startVoice(t, Options{STTChoice: failing, VoiceChoice: voiceSel})
	if f := srv.features(); !f.STT || !f.Voice {
		t.Fatalf("features = %+v", f)
	}
	call(t, srv, http.MethodPost, "/api/voice/install", map[string][]string{"targets": {"stt"}}, true)
	if st := waitInstall(t, srv, jobFailed); !strings.Contains(st.Install.Error, "404") || st.STT.Ready || !st.STT.Offer {
		t.Fatalf("failed status = %+v", st)
	}

	call(t, srv, http.MethodPost, "/api/voice/install", map[string][]string{"targets": {"tts", "bogus"}}, true)
	close(release)
	if st := waitInstall(t, srv, jobDone); !st.TTS.Ready || st.TTS.Engine != "embedded:kokoro/bm_george" {
		t.Fatalf("tts installed status = %+v", st.TTS)
	}

	// Shutdown cancels the in-flight install through the server context.
	hang := &transcription.Selection{
		Active: transcription.NewNull(), PreferEmbedded: true, Embedded: transcription.EmbeddedInfo{Supported: true},
		NewEmbedded: func() transcription.Provider {
			return &fakeEmbeddedSTT{fakeSTT: fakeSTT{name: "e"}, release: make(chan struct{})}
		},
	}
	srv2 := startVoice(t, Options{STTChoice: hang})
	call(t, srv2, http.MethodPost, "/api/voice/install", map[string][]string{"targets": {"stt"}}, true)
	_ = srv2.Shutdown(context.Background())
	waitClosed(t, srv2.voice.installEnded(), "shutdown to cancel the install")
	if st := srv2.voice.status().Install.State; st != jobCancelled {
		t.Fatalf("shutdown left the install %+v", srv2.voice.status().Install)
	}
}

func TestVoice_UnknownSubpathsAndHelpers(t *testing.T) {
	srv := startVoice(t, Options{})
	for _, p := range []struct{ method, path string }{{http.MethodGet, "/api/voice/nope"}, {http.MethodPost, "/api/voice/nope"}} {
		if code, _ := call(t, srv, p.method, p.path, map[string]string{}, true); code != http.StatusNotFound {
			t.Errorf("%s %s = %d", p.method, p.path, code)
		}
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/voice/install", "not json", true); code != http.StatusBadRequest {
		t.Errorf("bad json install = %d", code)
	}
	for in, want := range map[string]string{"pt-BR": "pt", "EN_us": "en", "auto": "", "": "", " es ": "es"} {
		if got := sttLanguage(in); got != want {
			t.Errorf("sttLanguage(%q) = %q, want %q", in, got, want)
		}
	}
	if audioKind([]byte("RIFF")) != "" || audioKind(nil) != "" {
		t.Error("a truncated header is not a known container")
	}
}
