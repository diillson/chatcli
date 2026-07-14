package action

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// fakeBridge satisfies scheduler.CLIBridge for the polling action.
// Only the fields ParkPoll touches are populated.
type fakeBridge struct {
	httpStatus int
	httpBody   string
	httpErr    error
	httpCalls  atomic.Int32

	cmdStdout string
	cmdStderr string
	cmdExit   int
	cmdErr    error
	cmdCalls  atomic.Int32
	// cmdMatchAfter, when > 0, forces exit 0 from that probe count on.
	// Lets integration tests flip a failing probe to success without a
	// data race on cmdExit mid-run.
	cmdMatchAfter atomic.Int32

	notifyCalls atomic.Int32
	notifyTok   string
	notifyOut   string
	notifyDet   string
}

func (f *fakeBridge) ExecuteSlashCommand(_ context.Context, _ string, _ bool) (string, bool, error) {
	return "", false, nil
}
func (f *fakeBridge) RunAgentTask(_ context.Context, _, _ string, _ bool) (string, error) {
	return "", nil
}
func (f *fakeBridge) DispatchWorker(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (f *fakeBridge) SendLLMPrompt(_ context.Context, _, _ string, _ int) (string, int, float64, error) {
	return "", 0, 0, nil
}
func (f *fakeBridge) FireHook(_ hooks.HookEvent) *hooks.HookResult { return nil }
func (f *fakeBridge) RunShell(_ context.Context, _ string, _ map[string]string, _, _ bool) (string, string, int, error) {
	calls := f.cmdCalls.Add(1)
	exit := f.cmdExit
	if after := f.cmdMatchAfter.Load(); after > 0 && calls >= after {
		exit = 0
	}
	return f.cmdStdout, f.cmdStderr, exit, f.cmdErr
}
func (f *fakeBridge) ClassifyShellCommand(_ string) scheduler.ShellPolicy {
	return scheduler.ShellPolicyAllow
}
func (f *fakeBridge) KubeconfigPath() string         { return "" }
func (f *fakeBridge) DockerSocketPath() string       { return "" }
func (f *fakeBridge) WorkspaceDir() string           { return "" }
func (f *fakeBridge) LLMClient() client.LLMClient    { return nil }
func (f *fakeBridge) AppendHistory(_ models.Message) {}
func (f *fakeBridge) PublishEvent(_ scheduler.Event) {}
func (f *fakeBridge) NotifyParkComplete(_ context.Context, tok, outcome, detail string) error {
	// Fields first, counter last: the atomic Add is the release edge a
	// concurrent test observes before reading the fields.
	f.notifyTok, f.notifyOut, f.notifyDet = tok, outcome, detail
	f.notifyCalls.Add(1)
	return nil
}
func (f *fakeBridge) RunHTTPProbe(_ context.Context, _, _ string, _ map[string]string, _ time.Duration) (int, string, error) {
	f.httpCalls.Add(1)
	return f.httpStatus, f.httpBody, f.httpErr
}

// trackingEnqueue captures the jobs that ParkPoll sends back through
// env.Enqueue. Used to assert the next-poll vs resume fanout.
type trackingEnqueue struct {
	jobs []*scheduler.Job
}

func (t *trackingEnqueue) fn(_ context.Context, j *scheduler.Job) (*scheduler.Job, error) {
	t.jobs = append(t.jobs, j)
	return j, nil
}

func newEnv(t *testing.T, b *fakeBridge, enq *trackingEnqueue) *scheduler.ExecEnv {
	t.Helper()
	return &scheduler.ExecEnv{
		Bridge:  b,
		Job:     scheduler.JobSummary{Owner: scheduler.Owner{Kind: "park", ID: "agent", Tag: "tok"}},
		Enqueue: enq.fn,
	}
}

func TestParkPoll_HTTP_Match200(t *testing.T) {
	b := &fakeBridge{httpStatus: 200, httpBody: "ok"}
	enq := &trackingEnqueue{}
	env := newEnv(t, b, enq)

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_url",
			"url":           "https://x",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(time.Minute).Unix(),
			"success_when":  "status=200",
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("execute err: %v", res.Err)
	}
	if len(enq.jobs) != 1 {
		t.Fatalf("expected 1 follow-up job, got %d", len(enq.jobs))
	}
	if enq.jobs[0].Action.Type != scheduler.ActionAgentResume {
		t.Fatalf("expected AgentResume fanout, got %s", enq.jobs[0].Action.Type)
	}
	if enq.jobs[0].Action.Payload["outcome"] != "matched" {
		t.Fatalf("outcome should be matched, got %v", enq.jobs[0].Action.Payload["outcome"])
	}
}

func TestParkPoll_HTTP_DeadlineTimeout(t *testing.T) {
	b := &fakeBridge{httpStatus: 503, httpBody: "still working"}
	enq := &trackingEnqueue{}
	env := newEnv(t, b, enq)

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_url",
			"url":           "https://x",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(-time.Second).Unix(), // already past
			"success_when":  "status=200",
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("execute err: %v", res.Err)
	}
	if len(enq.jobs) != 1 {
		t.Fatalf("expected timeout fanout job, got %d", len(enq.jobs))
	}
	if enq.jobs[0].Action.Payload["outcome"] != "timeout" {
		t.Fatalf("expected timeout outcome, got %v", enq.jobs[0].Action.Payload["outcome"])
	}
	// Probe is NOT called past deadline.
	if b.httpCalls.Load() != 0 {
		t.Fatalf("probe should not run past deadline")
	}
}

func TestParkPoll_HTTP_RescheduleSelf(t *testing.T) {
	b := &fakeBridge{httpStatus: 503}
	enq := &trackingEnqueue{}
	env := newEnv(t, b, enq)

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_url",
			"url":           "https://x",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(5 * time.Minute).Unix(),
			"success_when":  "status=200",
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("execute err: %v", res.Err)
	}
	if len(enq.jobs) != 1 || enq.jobs[0].Action.Type != scheduler.ActionParkPoll {
		t.Fatalf("expected self-reschedule, got %+v", enq.jobs)
	}
	// The sibling must NOT reuse the live job's name — the scheduler's
	// duplicate-name admission would reject it while the current cycle
	// is still Running.
	if enq.jobs[0].Name != "park-poll:tok-abcdefgh.i1" {
		t.Fatalf("sibling must carry a per-iteration unique name, got %q", enq.jobs[0].Name)
	}
	if res.StopRecurrence {
		t.Fatalf("waiting must not stop recurrence")
	}
}

func TestParkPoll_RecurringJob_WaitsInPlace(t *testing.T) {
	b := &fakeBridge{httpStatus: 503}
	enq := &trackingEnqueue{}
	env := newEnv(t, b, enq)
	env.Job.Type = string(scheduler.ScheduleInterval)

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_url",
			"url":           "https://x",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(5 * time.Minute).Unix(),
			"success_when":  "status=200",
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("execute err: %v", res.Err)
	}
	// Recurring jobs re-arm in the dispatcher: no sibling enqueue, no
	// StopRecurrence — a plain success carries the cycle forward.
	if len(enq.jobs) != 0 {
		t.Fatalf("recurring job must not enqueue a sibling, got %+v", enq.jobs)
	}
	if res.StopRecurrence {
		t.Fatalf("waiting must not stop recurrence")
	}
}

func TestParkPoll_Match_StopsRecurrence(t *testing.T) {
	b := &fakeBridge{httpStatus: 200}
	enq := &trackingEnqueue{}
	env := newEnv(t, b, enq)
	env.Job.Type = string(scheduler.ScheduleInterval)

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_url",
			"url":           "https://x",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(5 * time.Minute).Unix(),
			"success_when":  "status=200",
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("execute err: %v", res.Err)
	}
	if !res.StopRecurrence {
		t.Fatalf("matched probe must finalize the recurring poll job")
	}
	if len(enq.jobs) != 1 || enq.jobs[0].Action.Type != scheduler.ActionAgentResume {
		t.Fatalf("expected AgentResume fanout, got %+v", enq.jobs)
	}
}

func TestParkPoll_ResumeEnqueueRejected_NotifiesBridgeDirectly(t *testing.T) {
	b := &fakeBridge{httpStatus: 200}
	env := newEnv(t, b, &trackingEnqueue{})
	env.Job.Type = string(scheduler.ScheduleInterval)
	env.Enqueue = func(_ context.Context, _ *scheduler.Job) (*scheduler.Job, error) {
		return nil, errors.New("admission rejected")
	}

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_url",
			"url":           "https://x",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(5 * time.Minute).Unix(),
			"success_when":  "status=200",
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("direct-notify fallback must succeed, got err: %v", res.Err)
	}
	if !res.StopRecurrence {
		t.Fatalf("fallback wake-up must still finalize the poll job")
	}
	if b.notifyCalls.Load() != 1 || b.notifyOut != "matched" {
		t.Fatalf("bridge must be notified directly: calls=%d outcome=%q", b.notifyCalls.Load(), b.notifyOut)
	}
}

func TestParkPoll_BodyContains(t *testing.T) {
	b := &fakeBridge{httpStatus: 200, httpBody: "build status: completed"}
	enq := &trackingEnqueue{}
	env := newEnv(t, b, enq)

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_url",
			"url":           "https://x",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(5 * time.Minute).Unix(),
			"success_when":  "body contains:completed",
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("execute err: %v", res.Err)
	}
	if len(enq.jobs) != 1 || enq.jobs[0].Action.Payload["outcome"] != "matched" {
		t.Fatalf("body contains should match: %+v", enq.jobs)
	}
}

func TestParkPoll_Cmd_ExitMatcher(t *testing.T) {
	b := &fakeBridge{cmdStdout: "ok\n", cmdExit: 0}
	enq := &trackingEnqueue{}
	env := newEnv(t, b, enq)

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_cmd",
			"command":       "true",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(5 * time.Minute).Unix(),
			"success_when":  "exit=0",
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("execute err: %v", res.Err)
	}
	if len(enq.jobs) != 1 || enq.jobs[0].Action.Payload["outcome"] != "matched" {
		t.Fatalf("expected exit=0 to match: %+v", enq.jobs)
	}
}

func TestMatchSuccessWhen(t *testing.T) {
	cases := []struct {
		expr    string
		summary probeSummary
		dflt    bool
		want    bool
	}{
		{"", probeSummary{HTTPStatus: 200}, true, true},
		{"", probeSummary{HTTPStatus: 500}, false, false},
		{"status=200", probeSummary{HTTPStatus: 200}, false, true},
		{"status=200..299", probeSummary{HTTPStatus: 204}, false, true},
		{"status=200..299", probeSummary{HTTPStatus: 500}, false, false},
		{"exit=0", probeSummary{ExitCode: 0}, false, true},
		{"exit=1", probeSummary{ExitCode: 0}, false, false},
		{"body contains:done", probeSummary{Body: "all done now"}, false, true},
		{"body matches:^OK$", probeSummary{Body: "OK"}, false, true},
		{"body matches:^OK$", probeSummary{Body: "okay"}, false, false},
	}
	for _, c := range cases {
		got := matchSuccessWhen(c.expr, c.summary, c.dflt)
		if got != c.want {
			t.Errorf("expr=%q summary=%+v dflt=%v: want=%v got=%v", c.expr, c.summary, c.dflt, c.want, got)
		}
	}
}

func TestParkPoll_HTTPProbeError_Reschedules(t *testing.T) {
	b := &fakeBridge{httpErr: errors.New("connection refused")}
	enq := &trackingEnqueue{}
	env := newEnv(t, b, enq)

	action := scheduler.Action{
		Type: scheduler.ActionParkPoll,
		Payload: map[string]any{
			"resume_token":  "tok-abcdefgh",
			"mode":          "for_url",
			"url":           "https://x",
			"interval":      "30s",
			"deadline_unix": time.Now().Add(5 * time.Minute).Unix(),
		},
	}
	res := ParkPoll{}.Execute(context.Background(), action, env)
	if res.Err != nil {
		t.Fatalf("execute err: %v", res.Err)
	}
	if len(enq.jobs) != 1 || enq.jobs[0].Action.Type != scheduler.ActionParkPoll {
		t.Fatalf("expected self-reschedule on probe error: %+v", enq.jobs)
	}
	if !strings.Contains(res.Output, "probe error") {
		t.Fatalf("output should mention probe error: %q", res.Output)
	}
}
