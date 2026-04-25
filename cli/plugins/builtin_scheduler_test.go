/*
 * Tests for the @scheduler tool's input-shape acceptance. Pinned to the
 * three forms the agent dispatcher might hand us:
 *   1. JSON envelope with args wrapper
 *   2. Flat JSON (no args wrapper)
 *   3. argv form produced by buildArgvFromJSONMap
 * Plus alias resolution for the natural-language field names the LLM
 * reached for in production traces (delay, command).
 */
package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeAdapter struct {
	lastCmd   string
	lastInner string
}

func (f *fakeAdapter) Owner() SchedulerOwner { return SchedulerOwner{Kind: "agent", ID: "test"} }
func (f *fakeAdapter) ScheduleJob(_ context.Context, _ SchedulerOwner, in string) (string, error) {
	f.lastCmd, f.lastInner = "schedule", in
	return "{\"ok\":true}", nil
}
func (f *fakeAdapter) WaitUntil(_ context.Context, _ SchedulerOwner, in string) (string, error) {
	f.lastCmd, f.lastInner = "wait", in
	return "{\"ok\":true}", nil
}
func (f *fakeAdapter) QueryJob(_ context.Context, _ SchedulerOwner, in string) (string, error) {
	f.lastCmd, f.lastInner = "query", in
	return "{\"ok\":true}", nil
}
func (f *fakeAdapter) ListJobs(_ context.Context, _ SchedulerOwner, in string) (string, error) {
	f.lastCmd, f.lastInner = "list", in
	return "{\"ok\":true}", nil
}
func (f *fakeAdapter) CancelJob(_ context.Context, _ SchedulerOwner, in string) (string, error) {
	f.lastCmd, f.lastInner = "cancel", in
	return "{\"ok\":true}", nil
}

func withFakeAdapter(t *testing.T) *fakeAdapter {
	t.Helper()
	prev := currentSchedulerAdapter()
	f := &fakeAdapter{}
	SetSchedulerAdapter(f)
	t.Cleanup(func() {
		// atomic.Value rejects nil; restore only if there was a prior value.
		if prev != nil {
			SetSchedulerAdapter(prev)
		}
	})
	return f
}

func parseInner(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("inner not valid JSON: %v (%q)", err, raw)
	}
	return m
}

// TestExecute_JSONEnvelopeWithArgs covers the documented shape using
// the /run form (the canonical example in the public docs).
func TestExecute_JSONEnvelopeWithArgs(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	out, err := p.Execute(context.Background(), []string{
		`{"cmd":"schedule","args":{"name":"x","when":"+5m","do":"/run echo hi"}}`,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out == "" {
		t.Fatal("empty output")
	}
	if f.lastCmd != "schedule" {
		t.Fatalf("cmd routed to %q, want schedule", f.lastCmd)
	}
	m := parseInner(t, f.lastInner)
	if m["when"] != "+5m" || m["do"] != "/run echo hi" || m["name"] != "x" {
		t.Fatalf("inner missing fields: %v", m)
	}
}

// TestExecute_FlatJSON: no args wrapper, scheduler must accept it.
func TestExecute_FlatJSON(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{
		`{"cmd":"schedule","name":"y","when":"+1m","do":"/run ls"}`,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := parseInner(t, f.lastInner)
	if m["when"] != "+1m" || m["do"] != "/run ls" || m["name"] != "y" {
		t.Fatalf("inner: %v", m)
	}
	if _, leaked := m["cmd"]; leaked {
		t.Fatalf("flat envelope leaked cmd into inner: %v", m)
	}
}

// TestExecute_ArgvFromAgentNormalizer: this is the *exact* shape the
// agent dispatcher produces for {"cmd":"schedule","args":{...}}. Before
// the fix it failed with "parse envelope: invalid character 's'".
func TestExecute_ArgvFromAgentNormalizer(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{
		"schedule", "--name", "docker-up", "--when", "+5m", "--do", "/run open -a Docker",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if f.lastCmd != "schedule" {
		t.Fatalf("cmd %q", f.lastCmd)
	}
	m := parseInner(t, f.lastInner)
	if m["name"] != "docker-up" || m["when"] != "+5m" || m["do"] != "/run open -a Docker" {
		t.Fatalf("inner: %v", m)
	}
}

// TestExecute_AliasDelayCommand: the LLM in the trace used delay/command
// instead of when/do. Aliases must rewrite them.
func TestExecute_AliasDelayCommand(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{
		`{"cmd":"schedule","args":{"name":"docker","delay":"5m","command":"open -a Docker"}}`,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := parseInner(t, f.lastInner)
	if m["when"] != "5m" {
		t.Fatalf("delay->when failed: %v", m)
	}
	if m["do"] != "open -a Docker" {
		t.Fatalf("command->do failed: %v", m)
	}
	if _, ok := m["delay"]; ok {
		t.Fatalf("alias did not consume delay: %v", m)
	}
	if _, ok := m["command"]; ok {
		t.Fatalf("alias did not consume command: %v", m)
	}
}

// TestExecute_AliasArgvForm: argv normalizer renames "command" → "cmd"
// inside args before reaching us. Make sure cmd→do alias still kicks in.
func TestExecute_AliasArgvForm(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{
		"schedule", "--name", "x", "--delay", "5m", "--cmd", "open -a Docker",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := parseInner(t, f.lastInner)
	if m["when"] != "5m" || m["do"] != "open -a Docker" {
		t.Fatalf("argv aliases failed: %v", m)
	}
}

// TestExecute_AliasExplicitWins: explicit canonical key must beat alias.
func TestExecute_AliasExplicitWins(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{
		`{"cmd":"schedule","args":{"when":"+10m","delay":"5m","do":"/run x","command":"/run y"}}`,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := parseInner(t, f.lastInner)
	if m["when"] != "+10m" {
		t.Fatalf("explicit when overridden by delay: %v", m)
	}
	if m["do"] != "/run x" {
		t.Fatalf("explicit do overridden by command: %v", m)
	}
}

// TestExecute_ArgvNumericAndArrayValues: --max_polls 5 → 5 (int),
// --depends_on '["a","b"]' → []string. Type fidelity matters because
// ToolInput.MaxPolls is int and DependsOn is []string.
func TestExecute_ArgvNumericAndArrayValues(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{
		"schedule", "--when", "+1m", "--do", "/run x",
		"--max_polls", "5", "--depends_on", `["a","b"]`,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := parseInner(t, f.lastInner)
	if m["max_polls"] != float64(5) {
		t.Fatalf("max_polls not numeric: %v (%T)", m["max_polls"], m["max_polls"])
	}
	dep, ok := m["depends_on"].([]any)
	if !ok || len(dep) != 2 || dep[0] != "a" || dep[1] != "b" {
		t.Fatalf("depends_on not parsed as array: %v", m["depends_on"])
	}
}

// TestExecute_ListAndCancelArgv: smoke for the simpler subcommands.
func TestExecute_ListAndCancelArgv(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()

	_, err := p.Execute(context.Background(), []string{"list"})
	if err != nil {
		t.Fatalf("list err: %v", err)
	}
	if f.lastCmd != "list" {
		t.Fatalf("cmd %q", f.lastCmd)
	}

	_, err = p.Execute(context.Background(), []string{"cancel", "--id", "job-1", "--reason", "redo"})
	if err != nil {
		t.Fatalf("cancel err: %v", err)
	}
	if f.lastCmd != "cancel" {
		t.Fatalf("cmd %q", f.lastCmd)
	}
	m := parseInner(t, f.lastInner)
	if m["id"] != "job-1" || m["reason"] != "redo" {
		t.Fatalf("cancel inner: %v", m)
	}
}

// TestExecute_AliasIKnowVariants reproduces the production trace where
// the LLM tried "i-know":true (hyphen), then --i-know argv form, both
// silently ignored — the underlying ToolInput.IKnow is json:"i_know"
// (underscore). Aliases must rewrite all natural variants to i_know
// so the dangerous-confirmed flag actually reaches the scheduler.
func TestExecute_AliasIKnowVariants(t *testing.T) {
	cases := []struct {
		desc string
		args []string
	}{
		{
			desc: "JSON envelope with hyphen i-know",
			args: []string{`{"cmd":"schedule","args":{"when":"+5m","do":"shell: ls","i-know":true}}`},
		},
		{
			desc: "JSON envelope with iKnow camelCase",
			args: []string{`{"cmd":"schedule","args":{"when":"+5m","do":"shell: ls","iKnow":true}}`},
		},
		{
			desc: "JSON envelope with iknow lowercase",
			args: []string{`{"cmd":"schedule","args":{"when":"+5m","do":"shell: ls","iknow":true}}`},
		},
		{
			desc: "argv form --i-know (sanitizer drops leading dashes)",
			args: []string{"schedule", "--when", "+5m", "--do", "shell: ls", "--i-know"},
		},
		{
			desc: "argv form --i_know canonical",
			args: []string{"schedule", "--when", "+5m", "--do", "shell: ls", "--i_know"},
		},
	}
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := p.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			m := parseInner(t, f.lastInner)
			if m["i_know"] != true {
				t.Fatalf("i_know not set on inner: %v", m)
			}
			// All aliases must have been consumed.
			for _, alias := range []string{"i-know", "iKnow", "iknow", "know"} {
				if _, leaked := m[alias]; leaked {
					t.Errorf("alias %q leaked into inner: %v", alias, m)
				}
			}
		})
	}
}

// TestExecute_AliasJobID: query/cancel often see job_id from the LLM.
func TestExecute_AliasJobID(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{
		`{"cmd":"query","args":{"job_id":"abc"}}`,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	m := parseInner(t, f.lastInner)
	if m["id"] != "abc" {
		t.Fatalf("job_id->id failed: %v", m)
	}
}

// TestExecute_UnknownCmd_ErrorMessageHelpful: error must point the LLM
// at the canonical envelope so the next try succeeds.
func TestExecute_UnknownCmd_ErrorMessageHelpful(t *testing.T) {
	withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"reschedule","args":{}}`})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "schedule|wait|query|list|cancel") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

// TestExecute_BadJSON_ErrorMessageHelpful: bad JSON should suggest the
// envelope shape, not just dump the raw json.Unmarshal error.
func TestExecute_BadJSON_ErrorMessageHelpful(t *testing.T) {
	withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"schedule","args":{`})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `"cmd":"schedule"`) {
		t.Fatalf("error did not include canonical example: %v", err)
	}
}

// TestExecute_NoAdapter: fast-fail when the session has no scheduler.
// atomic.Value cannot be cleared once set, so this only runs if the
// process hasn't initialized an adapter yet (the common test case).
func TestExecute_NoAdapter(t *testing.T) {
	if currentSchedulerAdapter() != nil {
		t.Skip("adapter already initialized in this process; uninitialized path untestable")
	}
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if err == nil {
		t.Fatal("expected error when adapter missing")
	}
}

// TestExecute_SlashFormsPassThroughUnchanged: /run, /agent, /coder are
// valid action DSL forms and must reach the underlying adapter
// verbatim. The bridge intercepts them at fire-time and routes to
// RunAgentTask / RunCoderTask — no rewriting at the plugin layer.
func TestExecute_SlashFormsPassThroughUnchanged(t *testing.T) {
	cases := []string{
		`/run open -a Docker`,
		`/agent refactor cli/agent_mode.go`,
		`/coder fix the docker validate job`,
		`/jobs list`,
		`@coder exec ls -la`,
		`shell: docker info`,
		`agent: deploy and verify`,
		`POST https://hooks.example/x | body`,
		`noop`,
	}
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	for _, do := range cases {
		envelope := map[string]any{
			"cmd": "schedule",
			"args": map[string]any{
				"when": "+1m",
				"do":   do,
			},
		}
		raw, _ := json.Marshal(envelope)
		_, err := p.Execute(context.Background(), []string{string(raw)})
		if err != nil {
			t.Fatalf("err for do=%q: %v", do, err)
		}
		m := parseInner(t, f.lastInner)
		if m["do"] != do {
			t.Fatalf("do mutated: input=%q → inner=%v", do, m["do"])
		}
	}
}

// TestSchema_IsValidStructured: getToolContextString in cli/agent_mode.go
// unmarshals the schema into a structured shape — keep it parseable.
func TestSchema_IsValidStructured(t *testing.T) {
	p := NewBuiltinSchedulerPlugin()
	var schema struct {
		ArgsFormat  string `json:"argsFormat"`
		Subcommands []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Examples    []string `json:"examples"`
			Flags       []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Type        string `json:"type"`
				Default     string `json:"default"`
				Required    bool   `json:"required"`
			} `json:"flags"`
		} `json:"subcommands"`
	}
	if err := json.Unmarshal([]byte(p.Schema()), &schema); err != nil {
		t.Fatalf("schema not valid for agent prompt builder: %v", err)
	}
	if schema.ArgsFormat == "" {
		t.Fatal("argsFormat empty")
	}
	want := map[string]bool{"schedule": false, "wait": false, "query": false, "list": false, "cancel": false}
	for _, s := range schema.Subcommands {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
		if len(s.Examples) == 0 {
			t.Errorf("subcommand %q has no examples", s.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing subcommand %q in schema", name)
		}
	}
}
