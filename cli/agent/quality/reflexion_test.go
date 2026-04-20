/*
 * ChatCLI - Reflexion tests (Phase 4).
 */
package quality

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/models"
)

// recordingPersister buffers lessons so tests can assert on them. Uses
// a mutex because the hook persists from a background goroutine.
type recordingPersister struct {
	mu      sync.Mutex
	lessons []Lesson
	err     error
}

func (r *recordingPersister) persist(_ context.Context, l Lesson) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lessons = append(r.lessons, l)
	return r.err
}

func (r *recordingPersister) snapshot() []Lesson {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Lesson, len(r.lessons))
	copy(out, r.lessons)
	return out
}

func cannedLessonLLM(response string, err error) LessonLLM {
	return func(_ context.Context, _ []models.Message) (string, error) {
		return response, err
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met in %s", timeout)
}

func TestReflexionHook_DisabledIsNoop(t *testing.T) {
	cfg := Defaults()
	cfg.Reflexion.Enabled = false
	rp := &recordingPersister{}
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "x", Config: cfg}
	res := &workers.AgentResult{Error: errors.New("boom")}

	hook := NewReflexionHook(cannedLessonLLM("ignored", nil), rp.persist, nil)
	_ = hook.PostRun(context.Background(), hc, res)
	time.Sleep(20 * time.Millisecond)
	if got := rp.snapshot(); len(got) != 0 {
		t.Errorf("disabled reflexion must not persist; got %d", len(got))
	}
}

func TestReflexionHook_TriggersOnError(t *testing.T) {
	cfg := Defaults()
	cfg.Reflexion.Enabled = true
	cfg.Reflexion.OnError = true
	rp := &recordingPersister{}
	llm := cannedLessonLLM(`<situation>edit large file</situation>
<mistake>tried to rewrite whole file</mistake>
<correction>use targeted Edit</correction>
<tags>go, edit-file</tags>`, nil)

	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "rewrite main.go", Config: cfg}
	res := &workers.AgentResult{Error: errors.New("file too large")}

	_ = NewReflexionHook(llm, rp.persist, nil).PostRun(context.Background(), hc, res)
	waitFor(t, 200*time.Millisecond, func() bool { return len(rp.snapshot()) >= 1 })
	got := rp.snapshot()[0]
	if got.Trigger != "error" {
		t.Errorf("trigger=%q want error", got.Trigger)
	}
	if got.Situation != "edit large file" {
		t.Errorf("situation=%q want 'edit large file'", got.Situation)
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags=%v want 2 entries", got.Tags)
	}
}

func TestReflexionHook_TriggersOnHallucinationMetadata(t *testing.T) {
	cfg := Defaults()
	cfg.Reflexion.Enabled = true
	cfg.Reflexion.OnHallucination = true
	rp := &recordingPersister{}
	llm := cannedLessonLLM(`<situation>quoting library APIs</situation>
<mistake>cited a function that does not exist</mistake>
<correction>verify symbol exists before quoting</correction>
<tags>citations</tags>`, nil)

	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "explain stdlib", Config: cfg}
	res := &workers.AgentResult{Output: "fine"}
	res.SetMetadata("verified_with_discrepancy", "true")
	res.SetMetadata("verifier_discrepancies", "func does not exist")

	_ = NewReflexionHook(llm, rp.persist, nil).PostRun(context.Background(), hc, res)
	waitFor(t, 200*time.Millisecond, func() bool { return len(rp.snapshot()) >= 1 })
	if got := rp.snapshot()[0].Trigger; got != "hallucination" {
		t.Errorf("trigger=%q want hallucination", got)
	}
}

func TestReflexionHook_SkipResponseDoesNotPersist(t *testing.T) {
	cfg := Defaults()
	cfg.Reflexion.Enabled = true
	cfg.Reflexion.OnError = true
	rp := &recordingPersister{}
	llm := cannedLessonLLM(`<skip>nothing actionable</skip>`, nil)

	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "x", Config: cfg}
	res := &workers.AgentResult{Error: errors.New("transient")}

	_ = NewReflexionHook(llm, rp.persist, nil).PostRun(context.Background(), hc, res)
	time.Sleep(50 * time.Millisecond)
	if got := rp.snapshot(); len(got) != 0 {
		t.Errorf("skip response must not persist; got %d", len(got))
	}
}

func TestReflexionHook_ManualOverrideForcesTrigger(t *testing.T) {
	cfg := Defaults()
	cfg.Reflexion.Enabled = true
	cfg.Reflexion.OnError = false
	cfg.Reflexion.OnHallucination = false
	rp := &recordingPersister{}
	llm := cannedLessonLLM(`<situation>x</situation>
<mistake>y</mistake>
<correction>z</correction>
<tags>a</tags>`, nil)
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "x", Config: cfg}
	res := &workers.AgentResult{Output: "ok"}
	res.SetMetadata(MetaForceReflexion, "true")

	_ = NewReflexionHook(llm, rp.persist, nil).PostRun(context.Background(), hc, res)
	waitFor(t, 200*time.Millisecond, func() bool { return len(rp.snapshot()) >= 1 })
	if got := rp.snapshot()[0].Trigger; got != "manual" {
		t.Errorf("trigger=%q want manual", got)
	}
}

func TestParseLesson_RejectsMissingBlocks(t *testing.T) {
	if _, err := parseLesson(`<situation>x</situation>`, "error"); err == nil {
		t.Fatal("missing correction must error")
	}
}

func TestParseLesson_HandlesAllTagsLowercase(t *testing.T) {
	raw := `<situation>S</situation>
<mistake>M</mistake>
<correction>C</correction>
<tags>Go, Edit-File, Large</tags>`
	l, err := parseLesson(raw, "manual")
	if err != nil || l == nil {
		t.Fatalf("parse failed: err=%v lesson=%v", err, l)
	}
	if got := strings.Join(l.Tags, ","); got != "go,edit-file,large" {
		t.Errorf("tags should be lowercased; got %q", got)
	}
}

func TestLesson_FactContent_RoundTrip(t *testing.T) {
	l := Lesson{Situation: "A", Mistake: "B", Correction: "C", Trigger: "manual"}
	body := l.FactContent()
	for _, want := range []string{"LESSON: A", "MISTAKE: B", "CORRECTION: C", "TRIGGER: manual"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %s", want, body)
		}
	}
}
