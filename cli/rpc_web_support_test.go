/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/models"
)

// The web getters degrade to empty values without the subsystems and read
// the saved sessions when the store is there.
func TestWebGetters_DegradeAndReadSessions(t *testing.T) {
	bare := &ChatCLI{logger: zap.NewNop()}
	if _, ok := bare.CostSnapshotRPC(); ok {
		t.Fatal("no cost tracker must report ok=false")
	}
	if bare.BudgetBlockedRPC() || bare.MCPStatusRPC() != nil || bare.SessionCatalogRPC() != nil || bare.MaxTokensRPC() != 0 {
		t.Fatal("bare CLI must report nothing")
	}
	if spent, limit := bare.DailyBudgetRPC(); spent != 0 || limit != 0 {
		t.Fatalf("daily budget without tracker = %v/%v", spent, limit)
	}
	if _, _, err := bare.SessionMessagesRPC("x", 0, 10); err == nil {
		t.Fatal("no store must error")
	}
	if ActiveThemeName() == "" {
		t.Fatal("the active theme has a name")
	}
	_ = ActiveThemeCSSVars() // nil for dark themes, a map for light ones

	c := &ChatCLI{sessionManager: &SessionManager{sessionsDir: t.TempDir(), logger: zap.NewNop()}, logger: zap.NewNop(), UserMaxTokens: 4096}
	if err := c.sessionManager.SaveSession("web-a", []models.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.sessionManager.SaveSession("web-b", []models.Message{{Role: "user", Content: "later"}}); err != nil {
		t.Fatal(err)
	}
	cat := c.SessionCatalogRPC()
	if len(cat) != 2 || cat[0].Modified.Before(cat[1].Modified) {
		t.Fatalf("catalog = %+v, want two sessions newest first", cat)
	}
	msgs, total, err := c.SessionMessagesRPC("web-a", 0, 10)
	if err != nil || total != 2 || len(msgs) != 2 || msgs[1].Content != "hello" {
		t.Fatalf("messages = %v total=%d err=%v", msgs, total, err)
	}
	if _, _, err := c.SessionMessagesRPC("../escape", 0, 10); err == nil {
		t.Fatal("an invalid session name must be refused")
	}
	if c.MaxTokensRPC() != 4096 {
		t.Fatal("max tokens override not reported")
	}
}

// Attached images are offered to the loop as pending inbound images for
// the run and cleared afterwards.
func TestRunLoopRPC_AttachmentsBecomePendingImages(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), manager: &loopFakeManager{}}
	img := models.ImageContent{MediaType: "image/png", Data: []byte{1}, FileName: "a.png"}
	var seen int
	_, err := c.runLoopRPC(context.Background(), RPCRunOpts{Attachments: &TurnAttachments{Images: []models.ImageContent{img}}}, func(context.Context) error {
		seen = len(c.pendingInboundImages)
		return nil
	})
	if err != nil || seen != 1 || len(c.pendingInboundImages) != 0 {
		t.Fatalf("seen=%d after=%d err=%v", seen, len(c.pendingInboundImages), err)
	}
}
