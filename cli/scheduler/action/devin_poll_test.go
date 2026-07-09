/*
 * DevinPoll tests: admission validation and the poll → complete/reschedule
 * decision tree, with the Devin API faked behind devinPollNewAPI.
 */
package action

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/llm/devin"
	"go.uber.org/zap"
)

func TestDevinPollValidateSpec(t *testing.T) {
	valid := map[string]any{"session_id": "devin-1", "interval": "30s", "deadline_unix": int64(1)}
	if err := (DevinPoll{}).ValidateSpec(valid); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	for name, payload := range map[string]map[string]any{
		"missing session":  {"interval": "30s", "deadline_unix": int64(1)},
		"missing interval": {"session_id": "x", "deadline_unix": int64(1)},
		"missing deadline": {"session_id": "x", "interval": "30s"},
	} {
		if err := (DevinPoll{}).ValidateSpec(payload); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// withDevinServer fakes the Devin v1 API behind devinPollNewAPI.
func withDevinServer(t *testing.T, sessionJSON string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/devin-1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sessionJSON))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	orig := devinPollNewAPI
	devinPollNewAPI = func() (devin.API, error) {
		return devin.NewAPI(devin.APIConfig{APIKey: "apk_test", BaseURL: srv.URL, Logger: zap.NewNop()})
	}
	t.Cleanup(func() { devinPollNewAPI = orig })
}

func devinPollAction(deadline time.Time) scheduler.Action {
	return scheduler.Action{
		Type: scheduler.ActionDevinPoll,
		Payload: map[string]any{
			"session_id":    "devin-1",
			"interval":      "30s",
			"deadline_unix": deadline.Unix(),
		},
	}
}

func devinPollEnv(enq *trackingEnqueue) *scheduler.ExecEnv {
	return &scheduler.ExecEnv{
		Job:     scheduler.JobSummary{Owner: scheduler.Owner{Kind: "user", ID: "u1"}},
		Enqueue: enq.fn,
	}
}

func TestDevinPollTurnBoundaryCompletes(t *testing.T) {
	withDevinServer(t, `{
		"session_id":"devin-1","status_enum":"blocked",
		"created_at":"2026-07-08T10:00:00Z","updated_at":"2026-07-08T10:05:00Z",
		"messages":[
			{"type":"user_message","message":"do it","timestamp":"2026-07-08T10:00:00Z"},
			{"type":"devin_message","message":"need repo access","timestamp":"2026-07-08T10:04:00Z"}
		],
		"pull_request":{"url":"https://github.com/x/pull/3"}
	}`)
	enq := &trackingEnqueue{}
	res := DevinPoll{}.Execute(context.Background(), devinPollAction(time.Now().Add(time.Hour)), devinPollEnv(enq))
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if len(enq.jobs) != 0 {
		t.Fatal("turn boundary must not reschedule")
	}
	if !strings.Contains(res.Output, "blocked") || !strings.Contains(res.Output, "need repo access") || !strings.Contains(res.Output, "pull/3") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestDevinPollWorkingReschedules(t *testing.T) {
	withDevinServer(t, `{"session_id":"devin-1","status_enum":"working","created_at":"2026-07-08T10:00:00Z","updated_at":"2026-07-08T10:00:00Z"}`)
	enq := &trackingEnqueue{}
	res := DevinPoll{}.Execute(context.Background(), devinPollAction(time.Now().Add(time.Hour)), devinPollEnv(enq))
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if len(enq.jobs) != 1 {
		t.Fatalf("expected one rescheduled job, got %d", len(enq.jobs))
	}
	next := enq.jobs[0]
	if next.Action.Type != scheduler.ActionDevinPoll {
		t.Fatalf("rescheduled action = %s", next.Action.Type)
	}
	var payloadJSON []byte
	payloadJSON, _ = json.Marshal(next.Action.Payload)
	if strings.Contains(string(payloadJSON), "apk_test") {
		t.Fatal("payload must never carry the credential")
	}
	if !res.Transient {
		t.Fatal("reschedule result should be transient")
	}
}

func TestDevinPollDeadlineElapsed(t *testing.T) {
	withDevinServer(t, `{"session_id":"devin-1","status_enum":"working","created_at":"2026-07-08T10:00:00Z","updated_at":"2026-07-08T10:00:00Z"}`)
	enq := &trackingEnqueue{}
	res := DevinPoll{}.Execute(context.Background(), devinPollAction(time.Now().Add(-time.Minute)), devinPollEnv(enq))
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if len(enq.jobs) != 0 {
		t.Fatal("elapsed watch must not reschedule")
	}
	if !strings.Contains(res.Output, "deadline elapsed") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestDevinPollErroredSessionFails(t *testing.T) {
	withDevinServer(t, `{"session_id":"devin-1","status_enum":"expired","created_at":"2026-07-08T10:00:00Z","updated_at":"2026-07-08T10:00:00Z"}`)
	enq := &trackingEnqueue{}
	res := DevinPoll{}.Execute(context.Background(), devinPollAction(time.Now().Add(time.Hour)), devinPollEnv(enq))
	if res.Err == nil {
		t.Fatal("expired session must fail the watch")
	}
}

func TestDevinPollMissingCredentials(t *testing.T) {
	orig := devinPollNewAPI
	devinPollNewAPI = func() (devin.API, error) {
		return devin.NewAPI(devin.APIConfig{Logger: zap.NewNop()})
	}
	t.Cleanup(func() { devinPollNewAPI = orig })
	enq := &trackingEnqueue{}
	res := DevinPoll{}.Execute(context.Background(), devinPollAction(time.Now().Add(time.Hour)), devinPollEnv(enq))
	if res.Err == nil {
		t.Fatal("missing credentials must fail permanently")
	}
}
