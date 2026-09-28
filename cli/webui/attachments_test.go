/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/rpcserve"
)

// viewBackend is the scripted backend with @view in its tool catalog, as
// the real engine has it.
type viewBackend struct {
	*fakeBackend
	calls []string
}

func (v *viewBackend) Tools() []rpcserve.ToolInfo {
	return []rpcserve.ToolInfo{{Name: "@read"}, {Name: "@view"}}
}

func (v *viewBackend) CallTool(_ context.Context, name, args string) (string, error) {
	v.mu.Lock()
	v.calls = append(v.calls, name+" "+args)
	v.mu.Unlock()
	return "tool-out", nil
}

func startViewTest(t *testing.T) (*Server, *viewBackend) {
	t.Helper()
	vb := &viewBackend{fakeBackend: newFakeBackend()}
	srv, err := Start(Options{Backend: vb, PermissionTimeout: 2 * time.Second, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv, vb
}

var tinyPNG = base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n0000"))

// postTurn sends a turn with the given images (base64) and returns the
// status and, for a stream, its events.
func postTurn(t *testing.T, srv *Server, mode, text string, images ...string) (int, []event, []byte) {
	t.Helper()
	imgs := []map[string]string{}
	for _, b64 := range images {
		imgs = append(imgs, map[string]string{"name": "shot.png", "media_type": "image/png", "data": b64})
	}
	b, _ := json.Marshal(map[string]interface{}{"mode": mode, "text": text, "images": imgs})
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Host()+"/api/turn", bytes.NewReader(b))
	req.Header.Set(tokenHeader, srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, nil, body
	}
	return resp.StatusCode, readSSE(t, resp), nil
}

// A line sent with attached images is about them: it reaches the model
// with the images, even when it starts with a tool. "@view check this"
// used to run @view on the words, fail, and drop the image unseen.
func TestRouteInline_AttachedImagesAlwaysReachTheModel(t *testing.T) {
	srv, vb := startViewTest(t)
	for _, line := range []string{"@view validate this image", "@read what does this screenshot say?"} {
		code, evs, body := postTurn(t, srv, "chat", line, tinyPNG)
		if code != http.StatusOK || types(evs) != "run,chunk,chunk,done" {
			t.Fatalf("%q: %d %s %s", line, code, types(evs), body)
		}
	}
	if len(vb.calls) != 0 {
		t.Fatalf("a tool ran instead of the turn: %v", vb.calls)
	}
	if len(vb.seen) != 2 || len(vb.seen[0].Images) != 1 || len(vb.seen[1].Images) != 1 {
		t.Fatalf("the attachments did not reach the model: %+v", vb.seen)
	}
}

// In chat mode @view never runs on its own (it has no conversation to
// attach to): "@view <image file>" becomes the turn's own image
// attachment, like the terminal's @file, and anything else is user text.
func TestRouteInline_ViewInChatIsTheTurnsAttachment(t *testing.T) {
	srv, vb := startViewTest(t)
	cases := map[string]string{
		"@view shots/login.PNG what is wrong here?": "hello @file shots/login.PNG what is wrong here?",
		"@view validate the picture":                "hello @view validate the picture",
		"@view":                                     "hello @view",
	}
	for line, want := range cases {
		code, evs, body := postTurn(t, srv, "chat", line)
		if code != http.StatusOK || types(evs) != "run,chunk,chunk,done" || evs[len(evs)-1].Reply != want {
			t.Fatalf("%q: %d %s %+v %s", line, code, types(evs), evs, body)
		}
	}
	if len(vb.calls) != 0 {
		t.Fatalf("@view ran headless: %v", vb.calls)
	}
	// Other tools still run directly, as before.
	if _, evs, _ := postTurn(t, srv, "chat", "@read go.mod"); types(evs) != "run,tool_start,tool_end,done" {
		t.Fatalf("@read: %s", types(evs))
	}
}

// An image with no caption is a turn: the model is asked to look at it.
// Without an image an empty turn is still refused.
func TestServer_ImageOnlyTurn(t *testing.T) {
	srv, vb := startViewTest(t)
	code, evs, body := postTurn(t, srv, "chat", "  ", tinyPNG)
	if code != http.StatusOK || types(evs) != "run,chunk,chunk,done" {
		t.Fatalf("image-only turn = %d %s %s", code, types(evs), body)
	}
	if reply := evs[len(evs)-1].Reply; strings.TrimSpace(strings.TrimPrefix(reply, "hello")) == "" {
		t.Fatalf("the image-only turn carries no instruction: %q", reply)
	}
	if len(vb.seen) != 1 || len(vb.seen[0].Images) != 1 {
		t.Fatalf("the image did not reach the model: %+v", vb.seen)
	}
	if code, _, _ := postTurn(t, srv, "chat", ""); code != http.StatusBadRequest {
		t.Fatalf("empty turn without an image = %d", code)
	}
}

// A screenshot routinely passes the ordinary 8 MB request limit once
// base64-encoded; a turn takes the media limit, and a body past it is
// answered with 413 instead of a JSON decoder error.
func TestServer_TurnTakesScreenshotSizedImages(t *testing.T) {
	srv, vb := startViewTest(t)
	big := make([]byte, 7<<20)
	copy(big, "\x89PNG\r\n\x1a\n")
	code, _, body := postTurn(t, srv, "chat", "look", base64.StdEncoding.EncodeToString(big))
	if code != http.StatusOK {
		t.Fatalf("a 7 MB screenshot was refused: %d %s", code, body)
	}
	if len(vb.seen) != 1 || len(vb.seen[0].Images[0].Data) != len(big) {
		t.Fatal("the screenshot did not reach the turn whole")
	}
	huge := base64.StdEncoding.EncodeToString(make([]byte, maxMediaBody))
	code, _, body = postTurn(t, srv, "chat", "look", huge)
	var e apiError
	_ = json.Unmarshal(body, &e)
	if code != http.StatusRequestEntityTooLarge || e.Code != "too_large" {
		t.Fatalf("oversized turn = %d %s", code, body)
	}
}

// The page sends a turn that carries only images.
func TestPage_SendsImageOnlyTurns(t *testing.T) {
	if !strings.Contains(string(Page()), "if (!text && !state.images.length) return;") {
		t.Fatal("the composer drops a turn that carries only images")
	}
}
