package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// policyStubBridge is a CLIBridge that classifies each command it
// sees according to a lookup map. Unknown commands default to Ask,
// matching real fail-closed behavior.
type policyStubBridge struct {
	classify map[string]ShellPolicy
	seen     []string
}

func newStubBridge(classify map[string]ShellPolicy) *policyStubBridge {
	return &policyStubBridge{classify: classify}
}

func (b *policyStubBridge) ClassifyShellCommand(cmd string) ShellPolicy {
	b.seen = append(b.seen, cmd)
	if p, ok := b.classify[cmd]; ok {
		return p
	}
	return ShellPolicyAsk
}

func (b *policyStubBridge) ExecuteSlashCommand(context.Context, string, bool) (string, bool, error) {
	return "", false, nil
}
func (b *policyStubBridge) RunAgentTask(context.Context, string, string, bool) (string, error) {
	return "", nil
}
func (b *policyStubBridge) DispatchWorker(context.Context, string, string) (string, error) {
	return "", nil
}
func (b *policyStubBridge) SendLLMPrompt(context.Context, string, string, int) (string, int, float64, error) {
	return "", 0, 0, nil
}
func (b *policyStubBridge) FireHook(hooks.HookEvent) *hooks.HookResult { return nil }
func (b *policyStubBridge) RunShell(context.Context, string, map[string]string, bool, bool) (string, string, int, error) {
	return "", "", 0, nil
}
func (b *policyStubBridge) KubeconfigPath() string       { return "" }
func (b *policyStubBridge) DockerSocketPath() string     { return "" }
func (b *policyStubBridge) WorkspaceDir() string         { return "" }
func (b *policyStubBridge) LLMClient() client.LLMClient  { return nil }
func (b *policyStubBridge) AppendHistory(models.Message) {}
func (b *policyStubBridge) PublishEvent(Event)           {}

func TestEnumerateShellCommands_ActionAndWait(t *testing.T) {
	j := &Job{
		Action: Action{Type: ActionShell, Payload: map[string]any{"command": "kubectl get pods"}},
		Wait: &WaitSpec{Condition: Condition{
			Type: "all_of",
			Children: []Condition{
				{Type: "shell_exit", Spec: map[string]any{"cmd": "test -f /tmp/x"}},
				{Type: "regex_match", Spec: map[string]any{"cmd": "docker ps", "pattern": "Up"}},
				{Type: "http_status", Spec: map[string]any{"url": "http://x"}}, // ignored
			},
		}},
	}
	cmds := enumerateShellCommands(j)
	want := []string{"kubectl get pods", "test -f /tmp/x", "docker ps"}
	if len(cmds) != len(want) {
		t.Fatalf("cmds=%v, want=%v", cmds, want)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("cmds[%d]=%q want %q", i, cmds[i], want[i])
		}
	}
}

func TestEnumerateShellCommands_FallbackIncluded(t *testing.T) {
	j := &Job{
		Action: Action{Type: ActionNoop},
		Wait: &WaitSpec{
			Condition: Condition{Type: "http_status", Spec: map[string]any{"url": "http://x"}},
			Fallback:  &Action{Type: ActionShell, Payload: map[string]any{"command": "echo fallback"}},
		},
	}
	cmds := enumerateShellCommands(j)
	if len(cmds) != 1 || cmds[0] != "echo fallback" {
		t.Errorf("cmds=%v", cmds)
	}
}

func TestPreflightShellPolicy_AllowAdmitted(t *testing.T) {
	s := newTestScheduler(t, newStubBridge(map[string]ShellPolicy{
		"kubectl get pods": ShellPolicyAllow,
	}))
	defer s.DrainAndShutdown(time.Second)
	_, err := s.Enqueue(context.Background(), testShellJob("ok", "kubectl get pods", false))
	if err != nil {
		t.Fatalf("allow should admit; got %v", err)
	}
}

func TestPreflightShellPolicy_DenyRejectsAlways(t *testing.T) {
	s := newTestScheduler(t, newStubBridge(map[string]ShellPolicy{
		"rm -rf /": ShellPolicyDeny,
	}))
	defer s.DrainAndShutdown(time.Second)
	// Even with DangerousConfirmed (i.e. --i-know), deny wins.
	_, err := s.Enqueue(context.Background(), testShellJob("deny", "rm -rf /", true))
	if !errors.Is(err, ErrShellPolicyDeny) {
		t.Fatalf("expected ErrShellPolicyDeny, got %v", err)
	}
}

func TestPreflightShellPolicy_AskRejectedWithoutConfirm(t *testing.T) {
	s := newTestScheduler(t, newStubBridge(map[string]ShellPolicy{
		"curl http://unknown": ShellPolicyAsk,
	}))
	defer s.DrainAndShutdown(time.Second)
	_, err := s.Enqueue(context.Background(), testShellJob("ask-no", "curl http://unknown", false))
	if !errors.Is(err, ErrShellPolicyAsk) {
		t.Fatalf("expected ErrShellPolicyAsk, got %v", err)
	}
}

func TestPreflightShellPolicy_AskAcceptedWithIKnow(t *testing.T) {
	s := newTestScheduler(t, newStubBridge(map[string]ShellPolicy{
		"curl http://unknown": ShellPolicyAsk,
	}))
	defer s.DrainAndShutdown(time.Second)
	_, err := s.Enqueue(context.Background(), testShellJob("ask-yes", "curl http://unknown", true))
	if err != nil {
		t.Fatalf("ask with --i-know should admit, got %v", err)
	}
}

func TestPreflightShellPolicy_NoShellCommandsNoCheck(t *testing.T) {
	// Jobs without any shell command (e.g. webhook action, http wait)
	// must not be blocked by the preflight.
	stub := newStubBridge(map[string]ShellPolicy{})
	s := newTestScheduler(t, stub)
	defer s.DrainAndShutdown(time.Second)

	j := NewJob("http-only", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: time.Minute},
		Action{Type: ActionNoop, Payload: map[string]any{}})
	j.Wait = &WaitSpec{Condition: Condition{Type: "http_status", Spec: map[string]any{"url": "http://x"}}}
	if _, err := s.Enqueue(context.Background(), j); err != nil {
		t.Fatalf("no-shell job should admit, got %v", err)
	}
	if len(stub.seen) != 0 {
		t.Errorf("stub saw %d calls, expected 0: %v", len(stub.seen), stub.seen)
	}
}

func TestPreflightShellPolicy_FirstFailureWins(t *testing.T) {
	// Deny in the wait condition must reject even when the action is
	// allowed.
	s := newTestScheduler(t, newStubBridge(map[string]ShellPolicy{
		"echo ok":     ShellPolicyAllow,
		"rm -rf /var": ShellPolicyDeny,
	}))
	defer s.DrainAndShutdown(time.Second)

	j := NewJob("mixed", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: time.Minute},
		Action{Type: ActionShell, Payload: map[string]any{"command": "echo ok"}})
	j.Wait = &WaitSpec{Condition: Condition{Type: "shell_exit", Spec: map[string]any{"cmd": "rm -rf /var"}}}
	_, err := s.Enqueue(context.Background(), j)
	if !errors.Is(err, ErrShellPolicyDeny) {
		t.Fatalf("expected ErrShellPolicyDeny from condition, got %v", err)
	}
	if !strings.Contains(err.Error(), "rm -rf /var") {
		t.Errorf("error should mention the denied command: %v", err)
	}
}

// ─── helpers ──────────────────────────────────────────────────

func newTestScheduler(t *testing.T, bridge CLIBridge) *Scheduler {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false
	s, err := New(cfg, bridge, SchedulerDeps{}, nil)
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return s
}

func testShellJob(name, cmd string, confirmed bool) *Job {
	j := NewJob(name, Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: time.Minute},
		Action{Type: ActionShell, Payload: map[string]any{"command": cmd}})
	j.DangerousConfirmed = confirmed
	return j
}
