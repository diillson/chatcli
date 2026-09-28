/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/webui"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// e2eModel is a recording stand-in for a vision model. Every request the
// engine makes is kept, so a test can assert what the model was actually
// sent. In coder mode it behaves like a model that wants to look at the
// attachment through @view: it asks for the file the turn announced, and
// answers once the image has been attached.
type e2eModel struct {
	mu    sync.Mutex
	calls [][]models.Message
}

var e2eAttachmentPath = regexp.MustCompile(`((?:[A-Za-z]:\\|/)[^\s"']*attachments[^\s"']*\.png)`)

func (m *e2eModel) GetModelName() string { return "gpt-4o" }

func (m *e2eModel) SendPrompt(_ context.Context, prompt string, hist []models.Message, _ int) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, append([]models.Message(nil), hist...))
	m.mu.Unlock()
	coder := false
	for _, msg := range hist {
		if msg.Role == "system" && strings.Contains(msg.Content, "/CODER MODE") {
			coder = true
		}
	}
	if !coder {
		return "I looked at it.", nil
	}
	if last := lastUserMessage(hist); last != nil && len(last.Images) > 0 {
		return "The image shows a red square.", nil
	}
	if p := e2eAttachmentPath.FindString(prompt + "\n" + joinContents(hist)); p != "" && !viewed(hist) {
		return "<plan>\n1. look at the attached image\n</plan>\n<tool_call name=\"@view\" args='{\"cmd\":\"view\",\"args\":{\"file\":\"" + filepath.ToSlash(p) + "\"}}' />", nil
	}
	return "There is no image I can open.", nil
}

func viewed(hist []models.Message) bool {
	for _, msg := range hist {
		if strings.Contains(msg.Content, "staged") {
			return true
		}
	}
	return false
}

func joinContents(hist []models.Message) string {
	var b strings.Builder
	for _, msg := range hist {
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func lastUserMessage(hist []models.Message) *models.Message {
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role == "user" && !hist[i].IsTurnContext() {
			return &hist[i]
		}
	}
	return nil
}

// callsWith returns the recorded requests whose last user message contains
// text: the requests of one turn, not the background ones (memory, titles).
func (m *e2eModel) callsWith(text string) [][]models.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out [][]models.Message
	for _, c := range m.calls {
		for _, msg := range c {
			if msg.Role == "user" && strings.Contains(msg.Content, text) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func imagesIn(hist []models.Message) int {
	n := 0
	for _, msg := range hist {
		n += len(msg.Images)
	}
	return n
}

// e2eManager serves the recording model for every route.
type e2eManager struct{ model *e2eModel }

func (m *e2eManager) GetClient(string, string) (client.LLMClient, error) { return m.model, nil }
func (m *e2eManager) GetAvailableProviders() []string                    { return []string{"OPENAI"} }
func (m *e2eManager) GetTokenManager() (token.Manager, bool)             { return nil, false }
func (m *e2eManager) SetStackSpotRealm(string)                           {}
func (m *e2eManager) SetStackSpotAgentID(string)                         {}
func (m *e2eManager) GetStackSpotRealm() string                          { return "" }
func (m *e2eManager) GetStackSpotAgentID() string                        { return "" }
func (m *e2eManager) RefreshProviders()                                  {}
func (m *e2eManager) CreateClientWithKey(p, mo, _ string) (client.LLMClient, error) {
	return m.GetClient(p, mo)
}
func (m *e2eManager) CreateClientWithConfig(p, mo, _ string, _ map[string]string) (client.LLMClient, error) {
	return m.GetClient(p, mo)
}
func (m *e2eManager) ListModelsForProvider(context.Context, string) ([]client.ModelInfo, error) {
	return nil, nil
}

func redSquarePNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

type e2eWeb struct {
	srv   *webui.Server
	model *e2eModel
	home  string
}

// startE2EWeb serves the real web UI over the real RPC backend and a real
// engine, with only the model replaced.
func startE2EWeb(t *testing.T) *e2eWeb {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CHATCLI_LANG", "en")
	t.Setenv("LLM_PROVIDER", "OPENAI")
	t.Setenv("OPENAI_MODEL", "gpt-4o")
	t.Setenv("CHATCLI_MEMORY", "off")
	t.Chdir(t.TempDir())
	model := &e2eModel{}
	engine, err := cli.NewChatCLI(context.Background(), &e2eManager{model: model}, zap.NewNop())
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	engine.SetUnattended(true)
	backend := &rpcBackend{mgr: &e2eManager{model: model}, cli: engine, provider: "OPENAI", model: "gpt-4o", sessions: map[string][]models.Message{}}
	srv, err := webui.Start(webui.Options{Backend: backend, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return &e2eWeb{srv: srv, model: model, home: home}
}

// turn posts one turn and returns the final event.
func (w *e2eWeb) turn(t *testing.T, mode, text string, images ...string) map[string]interface{} {
	t.Helper()
	var imgs []map[string]string
	for i, b64 := range images {
		imgs = append(imgs, map[string]string{"name": fmt.Sprintf("shot-%d.png", i+1), "media_type": "image/png", "data": b64})
	}
	body, _ := json.Marshal(map[string]interface{}{"session": "web", "mode": mode, "text": text, "images": imgs})
	req, _ := http.NewRequest(http.MethodPost, "http://"+w.srv.Host()+"/api/turn", bytes.NewReader(body))
	req.Header.Set("X-Web-Token", w.srv.Token())
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		t.Fatalf("turn %q: status %d %v", text, resp.StatusCode, e)
	}
	var last map[string]interface{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil {
			last = ev
		}
	}
	return last
}

func TestWebE2E_ChatImageSurvivesTheFollowUpTurn(t *testing.T) {
	w := startE2EWeb(t)
	img := redSquarePNG(t)

	w.turn(t, "chat", "what is in this image?", img)
	first := w.model.callsWith("what is in this image?")
	if len(first) == 0 || imagesIn(first[0]) != 1 {
		t.Fatalf("turn 1: the model did not receive the attached image: %d request(s)", len(first))
	}

	w.turn(t, "chat", "validate the image again")
	second := w.model.callsWith("validate the image again")
	if len(second) == 0 || imagesIn(second[0]) != 1 {
		t.Fatalf("turn 2: the image attached on turn 1 is gone from the conversation")
	}
}

// Attaching an image and asking for @view in chat mode used to run @view
// headless on the words of the request: the tool failed ("not a readable
// image") and the attachment was dropped, so the next turn had no image.
func TestWebE2E_ChatAtViewWithAttachmentReachesTheModel(t *testing.T) {
	w := startE2EWeb(t)
	img := redSquarePNG(t)

	ev := w.turn(t, "chat", "@view validate this image", img)
	sent := w.model.callsWith("validate this image")
	if len(sent) == 0 || imagesIn(sent[0]) != 1 {
		t.Fatalf("the attached image never reached the model; last event %v", ev)
	}
	w.turn(t, "chat", "and now?")
	if next := w.model.callsWith("and now?"); len(next) == 0 || imagesIn(next[0]) != 1 {
		t.Fatal("the follow-up turn lost the attachment")
	}
}

// An image with no caption is a turn: the model is asked to analyze it.
func TestWebE2E_ImageOnlyTurn(t *testing.T) {
	w := startE2EWeb(t)
	w.turn(t, "chat", "", redSquarePNG(t))
	w.model.mu.Lock()
	defer w.model.mu.Unlock()
	for _, c := range w.model.calls {
		if last := lastUserMessage(c); last != nil && len(last.Images) == 1 {
			return
		}
	}
	t.Fatal("an image-only turn never reached the model")
}

// The attachment is saved where the file tools can open it, and the turn
// tells the model where: in coder mode the model opens it with @view and
// gets the image back.
func TestWebE2E_CoderOpensTheAttachmentWithView(t *testing.T) {
	w := startE2EWeb(t)
	img := redSquarePNG(t)

	ev := w.turn(t, "coder", "check the attached screenshot with @view", img)
	calls := w.model.callsWith("check the attached screenshot")
	if len(calls) == 0 {
		t.Fatal("the coder never called the model")
	}
	path := e2eAttachmentPath.FindString(joinContents(calls[0]))
	if path == "" {
		t.Fatalf("the turn does not say where the attachment is saved; last event %v", ev)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("announced attachment missing: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(w.home, ".chatcli")) {
		t.Errorf("attachment saved outside the ChatCLI data dir: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("attachment readable by others: %v", info.Mode().Perm())
	}
	sawViewed := false
	for _, c := range calls {
		if last := lastUserMessage(c); last != nil && len(last.Images) > 0 && viewed(c) {
			sawViewed = true
		}
	}
	if !sawViewed {
		t.Fatalf("@view on the announced path never attached the image; reply %v", ev["reply"])
	}

	// The follow-up turn still carries the attachment and its path.
	w.turn(t, "coder", "validate the screenshot again")
	follow := w.model.callsWith("validate the screenshot again")
	if len(follow) == 0 || imagesIn(follow[0]) == 0 || !strings.Contains(joinContents(follow[0]), path) {
		t.Fatal("the follow-up coder turn lost the attachment")
	}
}
