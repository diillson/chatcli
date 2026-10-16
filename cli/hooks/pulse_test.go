package hooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// A command hook that blocks the action is one background node per event,
// hung from the span it fired under, carrying its exit code and the block —
// never its command.
func TestHookPulseCommandBlock(t *testing.T) {
	rec := pulsetest.Record(t)
	m := NewManager(testLogger())
	m.hooks = []HookConfig{{Name: "blocker", Event: EventPreToolUse, Type: HookTypeCommand, Command: "echo secret-cmd >&2; exit 2", Timeout: 5000}}

	ctx := pulse.WithParent(context.Background(), "tool-77")
	if res := m.Fire(ctx, HookEvent{Type: EventPreToolUse, Timestamp: time.Now(), ToolName: "@coder"}); res == nil || !res.Blocked {
		t.Fatalf("expected a block, got %+v", res)
	}
	ev := rec.WaitFor(t, "hook end", pulsetest.Is(pulse.KindBackground, "hook:PreToolUse", pulse.PhaseEnd))
	if ev.Parent != "tool-77" || ev.Status != pulse.StatusOK {
		t.Fatalf("hook node: %+v", ev)
	}
	want := map[string]string{"type": "command", "exit_code": "2", "blocked_action": "true", "timeout": "false", "tool": "@coder"}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Fatalf("attr %s = %q, want %q (%+v)", k, ev.Attrs[k], v, ev.Attrs)
		}
	}
	for _, v := range ev.Attrs {
		if strings.Contains(v, "secret") {
			t.Fatalf("hook content leaked: %+v", ev.Attrs)
		}
	}
}

// A command hook cut off by its deadline reads as cancelled with timeout=true.
func TestHookPulseCommandTimeout(t *testing.T) {
	rec := pulsetest.Record(t)
	m := NewManager(testLogger())
	m.hooks = []HookConfig{{Name: "slow", Event: EventPostToolUse, Type: HookTypeCommand, Command: "sleep 5", Timeout: 50}}

	m.Fire(context.Background(), HookEvent{Type: EventPostToolUse, Timestamp: time.Now()})
	ev := rec.WaitFor(t, "hook end", pulsetest.Is(pulse.KindBackground, "hook:PostToolUse", pulse.PhaseEnd))
	if ev.Status != pulse.StatusCancelled || ev.Attrs["timeout"] != "true" {
		t.Fatalf("timed-out hook: %+v", ev)
	}
}

// An HTTP hook reports the status class of its answer, and its connection
// goes through the metered client so it shows as a conn node.
func TestHookPulseHTTP(t *testing.T) {
	rec := pulsetest.Record(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	m := NewManager(testLogger())
	m.hooks = []HookConfig{{Name: "gate", Event: EventPreToolUse, Type: HookTypeHTTP, URL: srv.URL + "/secret-path", Timeout: 5000}}

	if res := m.Fire(context.Background(), HookEvent{Type: EventPreToolUse, Timestamp: time.Now(), ToolName: "x"}); res == nil || !res.Blocked {
		t.Fatalf("403 must block, got %+v", res)
	}
	ev := rec.WaitFor(t, "hook end", pulsetest.Is(pulse.KindBackground, "hook:PreToolUse", pulse.PhaseEnd))
	if ev.Attrs["type"] != "http" || ev.Attrs["http_status"] != "4xx" || ev.Attrs["blocked_action"] != "true" || ev.Attrs["exit_code"] != "" {
		t.Fatalf("http hook node: %+v", ev)
	}
	rec.WaitFor(t, "metered hook connection", pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))
}
