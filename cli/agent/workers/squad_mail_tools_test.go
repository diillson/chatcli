package workers

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/models"
)

func TestAppendInboxMessage(t *testing.T) {
	// Empty inbox text is a no-op.
	h := []models.Message{{Role: "system", Content: "s"}}
	if got := appendInboxMessage(h, ""); len(got) != 1 {
		t.Fatalf("empty inbox appended: %d", len(got))
	}
	// Non-user tail → new user message.
	h = appendInboxMessage(h, "[SQUAD MAIL] one")
	if len(h) != 2 || h[1].Role != "user" {
		t.Fatalf("expected appended user message: %+v", h)
	}
	// User tail → folded into it (strict-alternation providers).
	h = appendInboxMessage(h, "[SQUAD MAIL] two")
	if len(h) != 2 || !strings.Contains(h[1].Content, "one") || !strings.Contains(h[1].Content, "two") {
		t.Fatalf("expected merge into trailing user message: %+v", h)
	}
}

func TestExecuteMailSend(t *testing.T) {
	reg := runs.NewRegistry(10)
	ctx, run := reg.Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "reviewer"})
	defer run.End(nil)

	// Native args path: sender resolves from the run handle on ctx.
	v := validatedTC{rtc: resolvedToolCall{
		ID: "t1", Subcmd: "mail", Native: true,
		NativeArgs: map[string]interface{}{"to": "coder", "text": "fix tests", "card_id": "card-1"},
	}}
	res := executeMailSend(ctx, v)
	if res.failed {
		t.Fatalf("send failed: %s", res.output)
	}
	if !strings.Contains(res.output, "reviewer -> coder") {
		t.Fatalf("sender identity missing: %s", res.output)
	}
	got := mail.Default().Drain("coder")
	if len(got) != 1 || got[0].From != "reviewer" || got[0].CardID != "card-1" {
		t.Fatalf("message not enqueued correctly: %+v", got)
	}

	// Raw JSON envelope path (XML mode), no run on ctx → sender "worker".
	v2 := validatedTC{rtc: resolvedToolCall{
		ID: "t2", Subcmd: "mail",
		RawArgs: `{"cmd":"mail","args":{"to":"orchestrator","text":"done"}}`,
	}}
	res2 := executeMailSend(context.Background(), v2)
	if res2.failed {
		t.Fatalf("raw send failed: %s", res2.output)
	}
	got2 := mail.Default().Drain("orchestrator")
	if len(got2) != 1 || got2[0].From != "worker" {
		t.Fatalf("raw path wrong: %+v", got2)
	}

	// Validation: missing fields fail without enqueue.
	v3 := validatedTC{rtc: resolvedToolCall{ID: "t3", Subcmd: "mail", NativeArgs: map[string]interface{}{"to": "coder"}}}
	if res3 := executeMailSend(context.Background(), v3); !res3.failed {
		t.Fatal("missing text must fail")
	}
	if leftover := mail.Default().Drain("coder"); leftover != nil {
		t.Fatalf("failed send enqueued anyway: %+v", leftover)
	}
}

func TestActionLabels(t *testing.T) {
	read := resolvedToolCall{Subcmd: "read", Native: true, NativeArgs: map[string]interface{}{"file": "cli/foo.go"}}
	if got := toolActionLabel(read); got != "read cli/foo.go" {
		t.Fatalf("toolActionLabel: %q", got)
	}
	exec := resolvedToolCall{Subcmd: "exec", RawArgs: "exec --cmd ls"}
	if got := toolActionLabel(exec); got != "exec" {
		t.Fatalf("toolActionLabel no-file: %q", got)
	}
	if got := batchActionLabel([]resolvedToolCall{read}); got != "read cli/foo.go" {
		t.Fatalf("batch single: %q", got)
	}
	got := batchActionLabel([]resolvedToolCall{read, exec, {Subcmd: "read"}})
	if !strings.Contains(got, "3×") || !strings.Contains(got, "read") || !strings.Contains(got, "exec") {
		t.Fatalf("batch multi: %q", got)
	}
}

func TestSquadNativeToolWiring(t *testing.T) {
	// send_mail maps to the mail subcommand.
	if sub, ok := NativeToolNameToSubcmd("send_mail"); !ok || sub != "mail" {
		t.Fatalf("send_mail mapping: %q %v", sub, ok)
	}
	if MailToolDefinition().Function.Name != "send_mail" {
		t.Fatal("mail tool definition name drifted")
	}

	// Orchestrator plugin tools resolve to their plugins with the envelope.
	for name, plugin := range map[string]string{
		"agents_runs": "@agents",
		"board_cards": "@board",
		"squad_mail":  "@mail",
	} {
		if !IsNativePluginTool(name) {
			t.Fatalf("%s not registered as native plugin tool", name)
		}
		pluginName, args, ok := ResolveNativePluginTool(name, map[string]interface{}{"cmd": "list"})
		if !ok || pluginName != plugin || len(args) != 1 || !strings.Contains(args[0], `"cmd":"list"`) {
			t.Fatalf("%s resolution: %q %v %v", name, pluginName, args, ok)
		}
	}

	// The squad defs ship in PluginToolDefinitions.
	names := map[string]bool{}
	for _, def := range PluginToolDefinitions() {
		names[def.Function.Name] = true
	}
	for _, want := range []string{"agents_runs", "board_cards", "squad_mail"} {
		if !names[want] {
			t.Fatalf("PluginToolDefinitions missing %s", want)
		}
	}
}
