/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/atrest"
)

var notedPath = regexp.MustCompile(`: (/\S+|[A-Za-z]:\\\S+)`)

// notedPaths lists the saved-copy paths an attachment note names.
func notedPaths(note string) []string {
	var out []string
	for _, m := range notedPath.FindAllStringSubmatch(note, -1) {
		out = append(out, m[1])
	}
	return out
}

// Each attachment is saved under the state root, readable by the owner
// only, under a name built here (never the one the user gave), and the
// note names the saved copy.
func TestSaveTurnAttachments_WritesPrivateCopiesAndNotesThem(t *testing.T) {
	root := t.TempDir()
	c := &ChatCLI{logger: zap.NewNop(), stateRoot: root}
	note := c.saveTurnAttachments([]models.ImageContent{
		{MediaType: "image/png", Data: tinyPNG, FileName: "../../etc/login screen.sh"},
		{MediaType: "", Data: tinyPNG},                              // sniffed
		{MediaType: "image/png", URL: "https://example.test/x.png"}, // nothing to save
	})
	paths := notedPaths(note)
	require.Len(t, paths, 2, note)
	assert.Contains(t, note, "@view")
	assert.Contains(t, note, "- login screen.sh: ")
	for _, p := range paths {
		assert.Equal(t, filepath.Join(root, attachmentsDirName), filepath.Dir(p), "saved outside the store")
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(tinyPNG, data))
		assert.True(t, strings.HasSuffix(p, ".png"), "the extension follows the media type: %s", p)
		if runtime.GOOS != "windows" {
			info, err := os.Stat(p)
			require.NoError(t, err)
			assert.Zero(t, info.Mode().Perm()&0o077, "readable by others: %v", info.Mode().Perm())
		}
	}
	assert.Contains(t, filepath.Base(paths[0]), "-login_screen.png")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, attachmentsDirName))
		require.NoError(t, err)
		assert.Zero(t, info.Mode().Perm()&0o077)
	}

	// Nothing savable, nothing to say.
	assert.Empty(t, c.saveTurnAttachments(nil))
	assert.Empty(t, c.saveTurnAttachments([]models.ImageContent{{MediaType: "image/png", Data: make([]byte, maxImageAttachmentBytes+1)}}))
	assert.Empty(t, c.saveTurnAttachments([]models.ImageContent{{MediaType: "text/plain", Data: []byte("not an image")}}))
}

func TestAttachmentStem(t *testing.T) {
	cases := map[string]string{
		"shot.png":                     "shot",
		"C:\\Users\\someone\\a b.jpeg": "a_b",
		"../../x.sh":                   "x",
		"":                             "image",
		"....":                         "image",
		"ação.png":                     "ao",
		strings.Repeat("z", 200):       strings.Repeat("z", attachmentNameMax),
	}
	for in, want := range cases {
		assert.Equal(t, want, attachmentStem(in), in)
	}
}

// With encryption at rest on, the saved copy is sealed like every other
// store, and @view (the image loader) still opens it.
func TestSaveTurnAttachments_SealedAtRestAndStillViewable(t *testing.T) {
	t.Setenv(atrest.EnvKey, "a-test-only-encryption-secret")
	t.Setenv("CHATCLI_VISION_INPUT", "native")
	c := minimalCLI(t)
	c.stateRoot = t.TempDir()
	paths := notedPaths(c.saveTurnAttachments([]models.ImageContent{{MediaType: "image/png", Data: tinyPNG, FileName: "a.png"}}))
	require.Len(t, paths, 1)
	raw, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.True(t, atrest.IsEncrypted(raw), "the copy is plaintext on disk")

	img, ok := c.loadImageAttachment(paths[0])
	require.True(t, ok)
	assert.True(t, bytes.Equal(tinyPNG, img.Data))

	c.agentMode = NewAgentMode(c, c.logger)
	out, err := (&viewToolAdapter{cli: c}).ViewImage(context.Background(), paths[0])
	require.NoError(t, err)
	assert.Contains(t, out, "staged")
}

// A loop run gets the note with the images and gives both back: they
// belong to one run.
func TestRunLoopRPC_AttachmentsAreSavedAndAnnounced(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), manager: &loopFakeManager{}, stateRoot: t.TempDir()}
	img := models.ImageContent{MediaType: "image/png", Data: tinyPNG, FileName: "a.png"}
	var images []models.ImageContent
	var note string
	_, err := c.runLoopRPC(context.Background(), RPCRunOpts{Attachments: &TurnAttachments{Images: []models.ImageContent{img}}}, func(context.Context) error {
		images, note = c.takePendingInbound(nil)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, images, 1)
	paths := notedPaths(note)
	require.Len(t, paths, 1, note)
	assert.FileExists(t, paths[0])
	assert.Empty(t, c.pendingInboundNote)
	assert.Empty(t, c.pendingInboundImages)
}

// A chat turn keeps the image and names its saved copy in the user
// message, so the next turn — or the terminal continuing the session —
// still has both.
func TestRunChatTurnRPC_AttachmentKeptAndAnnouncedAcrossTurns(t *testing.T) {
	t.Setenv("CHATCLI_VISION_INPUT", "native")
	fake := &rpcChatFakeClient{reply: "a red square"}
	c := newRPCChatCLI(t, fake)
	img := models.ImageContent{MediaType: "image/png", Data: tinyPNG, FileName: "shot.png"}
	turn, err := c.RunChatTurnRPC(context.Background(), "web", "what is this?", nil, RPCChatOpts{Attachments: &TurnAttachments{Images: []models.ImageContent{img}}})
	require.NoError(t, err)

	turn2, err := c.RunChatTurnRPC(context.Background(), "web", "validate it again", turn.History, RPCChatOpts{})
	require.NoError(t, err)
	var user *models.Message
	for i := range fake.lastHist {
		if fake.lastHist[i].Role == "user" && strings.HasPrefix(fake.lastHist[i].Content, "what is this?") {
			user = &fake.lastHist[i]
		}
	}
	require.NotNil(t, user, "turn 1 missing from turn 2's request")
	assert.Len(t, user.Images, 1, "turn 2 lost the image")
	paths := notedPaths(user.Content)
	require.Len(t, paths, 1, user.Content)
	assert.FileExists(t, paths[0])
	assert.NotEmpty(t, turn2.History)
}

// The vision gate judges the model the turn is routed to. It judged the
// session's model: a turn routed to a vision model had its image described
// away (or dropped) because the session's model had none, and a turn
// routed to a text model got the raw image its provider cannot read.
func TestRunChatTurnRPC_VisionGateJudgesTheRoutedModel(t *testing.T) {
	t.Setenv("CHATCLI_VISION_INPUT", "")
	img := models.ImageContent{MediaType: "image/png", Data: tinyPNG, FileName: "shot.png"}
	sentImages := func(sessionModel, routedModel string) int {
		fake := &rpcChatFakeClient{reply: "ok"}
		c := newRPCChatCLI(t, fake)
		c.manager = &rpcChatFakeManager{c: fake}
		c.Provider, c.Model = "OPENAI", sessionModel
		_, err := c.RunChatTurnRPC(context.Background(), "web", "look", nil, RPCChatOpts{Provider: "OPENAI", Model: routedModel, Attachments: &TurnAttachments{Images: []models.ImageContent{img}}})
		require.NoError(t, err)
		for _, m := range fake.lastHist {
			if m.Role == "user" && strings.HasPrefix(m.Content, "look") {
				return len(m.Images)
			}
		}
		t.Fatal("the turn never reached the model")
		return 0
	}
	assert.Equal(t, 1, sentImages("o3-mini", "gpt-4o"), "routed to a vision model: the image goes native")
	assert.Equal(t, 0, sentImages("gpt-4o", "o3-mini"), "routed to a text model: no raw image")
}

// The attachments store follows the session TTL, on demand and at boot.
func TestStorage_AttachmentsFollowTheSessionTTL(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	dir := filepath.Join(root, attachmentsDirName)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	old, fresh := filepath.Join(dir, "old.png"), filepath.Join(dir, "fresh.png")
	require.NoError(t, os.WriteFile(old, tinyPNG, 0o600))
	require.NoError(t, os.WriteFile(fresh, tinyPNG, 0o600))
	past := now.Add(-100 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(old, past, past))

	res, err := RunStorage(context.Background(), StorageOptions{Root: root, TTL: 90 * 24 * time.Hour, Now: now, Only: StoreAttachments})
	require.NoError(t, err)
	require.Len(t, res.Stores, 1)
	assert.Equal(t, PolicyTTL, res.Stores[0].Policy)
	assert.Equal(t, 2, res.Stores[0].Files)
	assert.Equal(t, 1, res.Stores[0].Prunable)

	t.Setenv("CHATCLI_SESSION_TTL", "90")
	rep := (&ChatCLI{stateRoot: root}).runRetentionPass()
	assert.Equal(t, 1, rep.Attachments)
	assert.NoFileExists(t, old)
	assert.FileExists(t, fresh)
}

// The session title is what the user wrote, never the attachment note.
func TestDeriveSessionTitle_SkipsTheAttachmentNote(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), stateRoot: t.TempDir()}
	note := c.saveTurnAttachments([]models.ImageContent{{MediaType: "image/png", Data: tinyPNG, FileName: "a.png"}})
	require.NotEmpty(t, note)
	got := deriveSessionTitle(&SessionData{ChatHistory: []models.Message{{Role: "user", Content: "what is in this image?" + note}}})
	assert.Equal(t, "what is in this image?", got)
}
