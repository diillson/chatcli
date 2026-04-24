package scheduler

import "testing"

func TestParseConditionDSL_Shapes(t *testing.T) {
	cases := []struct {
		in      string
		wantT   string
		wantErr bool
	}{
		{"http://localhost:8080/health==200", "http_status", false},
		{"https://foo/bar~=/ok/", "http_status", false},
		{"tcp://db:5432", "tcp_reachable", false},
		{"k8s:pod/prod/api:Ready", "k8s_resource_ready", false},
		{"k8s:deployment/api", "k8s_resource_ready", false},
		{"docker:postgres:healthy", "docker_running", false},
		{"file:/tmp/x", "file_exists", false},
		{"file:/tmp/x>=100", "file_exists", false},
		{"shell: echo done", "shell_exit", false},
		{"llm: is this healthy?", "llm_check", false},
		{"and(http://x==200, tcp://y:1)", "all_of", false},
		{"or(file:/a, file:/b)", "any_of", false},
		{"not file:/gone", "file_exists", false}, // negate propagates
		{"blah blah blah", "", true},
	}
	for _, c := range cases {
		got, err := ParseConditionDSL(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q wantErr=%v err=%v", c.in, c.wantErr, err)
			continue
		}
		if err == nil && got.Type != c.wantT {
			t.Errorf("%q type=%q want %q", c.in, got.Type, c.wantT)
		}
	}
}

func TestParseActionDSL_Shapes(t *testing.T) {
	cases := []struct {
		in   string
		want ActionType
		bad  bool
	}{
		{"/run tests", ActionSlashCmd, false},
		{"shell: make build", ActionShell, false},
		{"agent: deploy the cluster", ActionAgentTask, false},
		{"worker planner: plan deploy", ActionWorkerDispatch, false},
		{"llm: summarize the weekly report", ActionLLMPrompt, false},
		{"POST https://hooks.slack.com/x | hello", ActionWebhook, false},
		{"hook:PostToolUse", ActionHook, false},
		{"noop", ActionNoop, false},
		{"lol", "", true},
	}
	for _, c := range cases {
		got, err := ParseActionDSL(c.in)
		if (err != nil) != c.bad {
			t.Errorf("%q bad=%v err=%v", c.in, c.bad, err)
			continue
		}
		if err == nil && got.Type != c.want {
			t.Errorf("%q got %s want %s", c.in, got.Type, c.want)
		}
	}
}

func TestSplitTopLevelArgs(t *testing.T) {
	parts := splitTopLevelArgs("a, b, c(d, e), f")
	want := []string{"a", "b", "c(d, e)", "f"}
	if len(parts) != len(want) {
		t.Fatalf("len %d want %d (%v)", len(parts), len(want), parts)
	}
	for i, p := range parts {
		if p != want[i] {
			t.Errorf("%d: %q want %q", i, p, want[i])
		}
	}
}
