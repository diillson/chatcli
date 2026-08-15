package workers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/persona"
)

// --- mapPersonaTools tolerance tests ---

func TestMapToolsToCommands_CaseInsensitive(t *testing.T) {
	cmds := MapToolsToCommands([]string{"read", "BASH", "wRiTe"})
	set := toSet(cmds)
	for _, want := range []string{"read", "exec", "test", "write", "git-status"} {
		if !set[want] {
			t.Errorf("expected %q in commands, got %v", want, cmds)
		}
	}
}

func TestMapToolsToCommands_ArgumentSpecifier(t *testing.T) {
	cmds := MapToolsToCommands([]string{"Bash(go build:*)", "Read"})
	set := toSet(cmds)
	if !set["exec"] || !set["read"] {
		t.Errorf("Bash(go build:*) should grant exec; got %v", cmds)
	}
}

func TestMapToolsToCommands_Aliases(t *testing.T) {
	cmds := MapToolsToCommands([]string{"Shell", "Patch", "MultiEdit", "Search"})
	set := toSet(cmds)
	for _, want := range []string{"exec", "patch", "search"} {
		if !set[want] {
			t.Errorf("expected %q via alias, got %v", want, cmds)
		}
	}
}

func TestMapToolsToCommands_ContextTools(t *testing.T) {
	cmds := MapToolsToCommands([]string{"Memory", "Session", "Knowledge"})
	set := toSet(cmds)
	for _, want := range []string{
		ContextToolMemoryRecall,
		ContextToolSessionSearch, ContextToolSessionGet,
		ContextToolKnowledgeSearch, ContextToolKnowledgeGet,
	} {
		if !set[want] {
			t.Errorf("expected %q, got %v", want, cmds)
		}
	}
}

func TestMapPersonaTools_UnknownCollected(t *testing.T) {
	m := mapPersonaTools([]string{"Read", "WebFetch", "Banana"})
	if len(m.unknown) != 2 || m.unknown[0] != "WebFetch" || m.unknown[1] != "Banana" {
		t.Errorf("unknown = %v, want [WebFetch Banana]", m.unknown)
	}
	if m.writable {
		t.Error("read-only set must not be writable")
	}
}

func TestIsReadOnlyToolSet_AliasesAndSpecifiers(t *testing.T) {
	if isReadOnlyToolSet([]string{"bash"}) {
		t.Error("lowercase bash must count as writable")
	}
	if isReadOnlyToolSet([]string{"Bash(go test:*)"}) {
		t.Error("Bash with specifier must count as writable")
	}
	if isReadOnlyToolSet([]string{"MultiEdit"}) {
		t.Error("MultiEdit must count as writable")
	}
	if !isReadOnlyToolSet([]string{"Memory", "Session", "Knowledge", "Read"}) {
		t.Error("context tools are read-only")
	}
}

func TestNewCustomAgent_DegradedFallbackTracked(t *testing.T) {
	pa := &persona.Agent{Name: "helper", Tools: []string{"banana", "webfetch"}}
	a := NewCustomAgent(pa, nil)
	if !a.fellBackReadOnly {
		t.Error("all-unknown tools list must mark fellBackReadOnly")
	}
	if len(a.unknownTools) != 2 {
		t.Errorf("unknownTools = %v", a.unknownTools)
	}
	if !a.IsReadOnly() {
		t.Error("fallback agent must be read-only")
	}
	set := toSet(a.AllowedCommands())
	if !set["read"] || !set["search"] || !set["tree"] {
		t.Errorf("fallback commands = %v", a.AllowedCommands())
	}
}

func TestNewCustomAgent_LowercaseBashGrantsExec(t *testing.T) {
	pa := &persona.Agent{Name: "builder", Tools: []string{"read", "bash"}}
	a := NewCustomAgent(pa, nil)
	if a.IsReadOnly() {
		t.Error("bash grant must clear read-only")
	}
	if !toSet(a.AllowedCommands())["exec"] {
		t.Errorf("expected exec, got %v", a.AllowedCommands())
	}
	if a.fellBackReadOnly || len(a.unknownTools) != 0 {
		t.Errorf("unexpected degradation markers: fellBack=%v unknown=%v", a.fellBackReadOnly, a.unknownTools)
	}
}

// --- policyCallSurface tests ---

func TestPolicyCallSurface_NativeCanonicalized(t *testing.T) {
	rtc := resolvedToolCall{
		Name:       "run_command",
		Subcmd:     "exec",
		Native:     true,
		NativeArgs: map[string]interface{}{"cmd": "go build ./..."},
		RawArgs:    `{"cmd":"go build ./..."}`,
	}
	name, args := policyCallSurface(rtc)
	if name != "@coder" {
		t.Errorf("name = %q, want @coder", name)
	}
	if !strings.Contains(args, `"cmd":"exec"`) || !strings.Contains(args, "go build ./...") {
		t.Errorf("args must carry the {cmd:exec,args:{...}} envelope, got %s", args)
	}
}

func TestPolicyCallSurface_XMLPassthrough(t *testing.T) {
	rtc := resolvedToolCall{
		Name:    "@coder",
		Subcmd:  "exec",
		RawArgs: `{"cmd":"exec","args":{"cmd":"ls"}}`,
	}
	name, args := policyCallSurface(rtc)
	if name != "@coder" || args != rtc.RawArgs {
		t.Errorf("XML mode must pass through unchanged, got (%q, %s)", name, args)
	}
}

func TestIsPolicyExemptSubcmd(t *testing.T) {
	for _, sub := range []string{"mail", recallSubcmd, ContextToolMemoryRecall, ContextToolSessionGet} {
		if !isPolicyExemptSubcmd(sub) {
			t.Errorf("%q must be policy-exempt", sub)
		}
	}
	for _, sub := range []string{"exec", "write", "read", "delegate"} {
		if isPolicyExemptSubcmd(sub) {
			t.Errorf("%q must NOT be policy-exempt", sub)
		}
	}
}

// --- recall tool tests ---

func TestExecuteRecall_ExpandsMarkers(t *testing.T) {
	RegisterCCRRecaller(func(key string) (string, bool) {
		if key == "aaaabbbbccccdddd" {
			return "ORIGINAL CONTENT", true
		}
		return "", false
	})
	defer RegisterCCRRecaller(nil)

	v := validatedTC{rtc: resolvedToolCall{
		Subcmd:     recallSubcmd,
		Native:     true,
		NativeArgs: map[string]interface{}{"keys": "please expand <<ccr:aaaabbbbccccdddd>>"},
	}}
	r := executeRecall(v)
	if r.failed {
		t.Fatalf("recall failed: %v", r.record.Error)
	}
	if !strings.Contains(r.output, "ORIGINAL CONTENT") {
		t.Errorf("output = %q", r.output)
	}
}

func TestExecuteRecall_UnknownKeyFails(t *testing.T) {
	RegisterCCRRecaller(func(string) (string, bool) { return "", false })
	defer RegisterCCRRecaller(nil)

	v := validatedTC{rtc: resolvedToolCall{
		Subcmd:     recallSubcmd,
		Native:     true,
		NativeArgs: map[string]interface{}{"keys": "<<ccr:aaaabbbbccccdddd>>"},
	}}
	r := executeRecall(v)
	if !r.failed {
		t.Error("all-miss recall must be reported as failed")
	}
}

func TestExecuteRecall_NoRecallerRegistered(t *testing.T) {
	RegisterCCRRecaller(nil)
	v := validatedTC{rtc: resolvedToolCall{Subcmd: recallSubcmd, Native: true,
		NativeArgs: map[string]interface{}{"keys": "<<ccr:aaaabbbbccccdddd>>"}}}
	if r := executeRecall(v); !r.failed {
		t.Error("recall without a recaller must fail cleanly")
	}
}

// --- context tool defs/runner tests ---

func TestContextToolDefinitions_RequiresRunner(t *testing.T) {
	RegisterContextToolRunner(nil)
	if defs := ContextToolDefinitions([]string{ContextToolMemoryRecall}); len(defs) != 0 {
		t.Errorf("no runner registered → no defs, got %d", len(defs))
	}

	RegisterContextToolRunner(func(context.Context, string, map[string]interface{}) (string, error) {
		return "", nil
	})
	defer RegisterContextToolRunner(nil)

	defs := ContextToolDefinitions([]string{"read", ContextToolMemoryRecall, ContextToolKnowledgeSearch})
	if len(defs) != 2 {
		t.Fatalf("defs = %d, want 2", len(defs))
	}
}

func TestExecuteContextTool_RoutesToRunner(t *testing.T) {
	var gotTool string
	RegisterContextToolRunner(func(_ context.Context, tool string, args map[string]interface{}) (string, error) {
		gotTool = tool
		if args["query"] != "auth" {
			return "", errors.New("missing query")
		}
		return "HIT", nil
	})
	defer RegisterContextToolRunner(nil)

	v := validatedTC{rtc: resolvedToolCall{
		Subcmd:     ContextToolSessionSearch,
		Native:     true,
		NativeArgs: map[string]interface{}{"query": "auth"},
	}}
	r := executeContextTool(context.Background(), v)
	if r.failed || !strings.Contains(r.output, "HIT") {
		t.Fatalf("unexpected result: failed=%v output=%q err=%v", r.failed, r.output, r.record.Error)
	}
	if gotTool != ContextToolSessionSearch {
		t.Errorf("tool = %q", gotTool)
	}
}

func TestResolvedCallArgs_XMLEnvelope(t *testing.T) {
	args := resolvedCallArgs(resolvedToolCall{
		RawArgs: `{"cmd":"session-search","args":{"query":"x"}}`,
	})
	if args["query"] != "x" {
		t.Errorf("args = %v", args)
	}
}

// --- prompt preservation tests ---

func TestNativeToolSystemPrompt_PreservesSpecialist(t *testing.T) {
	cfg := WorkerReActConfig{SystemPrompt: "You are a specialized CODE REVIEWER agent.\nUse <tool_call> syntax."}
	got := nativeToolSystemPrompt(cfg)
	if !strings.Contains(got, "CODE REVIEWER") {
		t.Error("specialist identity must be preserved in native mode")
	}
	if !strings.Contains(got, "NATIVE TOOL CALLING MODE") {
		t.Error("native override section missing")
	}
	if !strings.Contains(got, "TRUNCATED & COMPRESSED OUTPUT") {
		t.Error("context guidance missing")
	}
}

func TestNativeToolSystemPrompt_GenericFallback(t *testing.T) {
	got := nativeToolSystemPrompt(WorkerReActConfig{})
	if !strings.Contains(got, "specialized coding agent") {
		t.Error("generic charter missing for empty specialist prompt")
	}
	if !strings.Contains(got, "TRUNCATED & COMPRESSED OUTPUT") {
		t.Error("context guidance missing")
	}
}

func toSet(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, s := range list {
		set[s] = true
	}
	return set
}
