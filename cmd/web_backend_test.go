/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/webui"
	"github.com/diillson/chatcli/models"
)

func TestWebBackend_DefaultsRouteAndAttachments(t *testing.T) {
	b := &rpcBackend{provider: "OPENAI", model: "gpt-6", sessions: map[string][]models.Message{}}
	if p, m := b.routeOf(webui.TurnOptions{}); p != "OPENAI" || m != "gpt-6" {
		t.Fatalf("empty route = %s/%s", p, m)
	}
	// Another provider without a model must not inherit the default model,
	// which belongs to the default provider.
	if p, m := b.routeOf(webui.TurnOptions{Provider: "CLAUDEAI"}); p != "CLAUDEAI" || m != "" {
		t.Fatalf("cross-provider route = %s/%s", p, m)
	}
	b.SetDefaults("", "gpt-6-luna")
	if p, m := b.Defaults(); p != "OPENAI" || m != "gpt-6-luna" {
		t.Fatalf("SetDefaults with empty provider = %s/%s", p, m)
	}
	if attachmentsOf(webui.TurnOptions{}) != nil {
		t.Fatal("no images must give nil attachments")
	}
	att := attachmentsOf(webui.TurnOptions{Images: []webui.ImageInput{{Name: "a.png", MediaType: "image/png", Data: []byte{1}}}})
	if att == nil || len(att.Images) != 1 || att.Images[0].FileName != "a.png" {
		t.Fatalf("attachments = %+v", att)
	}
}

func TestWebBackend_DegradedWithoutEngine(t *testing.T) {
	b := &rpcBackend{mgr: &fakeManager{noProviders: true}, sessions: map[string][]models.Message{}} // no provider, no cli
	if _, err := b.Chat(context.Background(), "web", "hi", webui.TurnOptions{}, nil); err == nil {
		t.Fatal("chat without an LLM must fail")
	}
	if _, err := b.RunCoder(context.Background(), "web", "task", webui.TurnOptions{}, nil); err == nil {
		t.Fatal("coder without the engine must fail")
	}
	if st := b.Status(); st.Version == "" || st.MCP != nil || st.Cost != nil {
		t.Fatalf("degraded status = %+v", st)
	}
	if b.SessionCatalog() != nil || b.Commands() != nil {
		t.Fatal("catalogs must be empty without the engine")
	}
	if got := webLang(); got != "en" && got != "pt-BR" {
		t.Fatalf("webLang = %q", got)
	}
}

func TestRunWeb_RejectsUnknownFlags(t *testing.T) {
	if err := RunWeb([]string{"-bogus"}, nil, nil); err == nil {
		t.Fatal("an unknown flag must fail before anything boots")
	}
}

// A restored transcript carries what people said, never the context
// ChatCLI injected for a turn or a run.
func TestRestoreSession_SkipsInjectedContext(t *testing.T) {
	b := &rpcBackend{sessions: map[string][]models.Message{"web": {
		models.TurnContextMessage("[TURN CONTEXT] recalled facts"),
		{Role: "user", Content: "hello"},
		models.RunContextMessage("[RUN CONTEXT] workspace"),
		{Role: "assistant", Content: "hi"},
	}}}
	items, err := b.RestoreSession(context.Background(), "web")
	if err != nil || len(items) != 2 || items[0].Content != "hello" || items[1].Content != "hi" {
		t.Fatalf("items = %+v err=%v", items, err)
	}
}
