package claudeai

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

// output_config already carries the effort level, so the budget merges
// into it rather than replacing it — two independent settings sharing one
// field.
func TestTaskBudgetMergesWithEffort(t *testing.T) {
	ctx := client.WithEffortHint(context.Background(), client.EffortXHigh)
	ctx = client.WithTaskBudget(ctx, client.AnthropicTaskBudget(64000))

	body := map[string]interface{}{"max_tokens": 4096}
	applyThinkingForEffort(body, "claude-opus-5", ctx)
	applyTaskBudget(body, "claude-opus-5", ctx)

	cfg, ok := body["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("output_config missing or wrong shape: %+v", body["output_config"])
	}
	if cfg["effort"] != "xhigh" {
		t.Errorf("the effort level must survive the merge: %+v", cfg)
	}
	budget, ok := cfg["task_budget"].(*client.TaskBudget)
	if !ok {
		t.Fatalf("task_budget missing: %+v", cfg)
	}
	if budget.Type != "tokens" || budget.Total != 64000 || budget.Remaining != 64000 {
		t.Errorf("task_budget = %+v", budget)
	}
}

// A model that does not read the field must not be sent it.
func TestTaskBudgetOnlyForModelsThatReadIt(t *testing.T) {
	ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudget(64000))
	body := map[string]interface{}{"max_tokens": 4096}
	if applyTaskBudget(body, "claude-sonnet-4-5", ctx) {
		t.Error("a model without the capability must not carry a task budget")
	}
	if _, present := body["output_config"]; present {
		t.Errorf("nothing should have been written: %+v", body)
	}
}

func TestNoTaskBudgetWithoutOne(t *testing.T) {
	body := map[string]interface{}{"max_tokens": 4096}
	if applyTaskBudget(body, "claude-opus-5", context.Background()) {
		t.Error("a turn with no budget must send none")
	}
}

func TestAnthropicTaskBudgetRejectsNothingToSay(t *testing.T) {
	if b := client.AnthropicTaskBudget(0); b != nil {
		t.Errorf("an empty budget must be nil, got %+v", b)
	}
	if b := client.AnthropicTaskBudget(-1); b != nil {
		t.Errorf("a negative budget must be nil, got %+v", b)
	}
}

// TestTaskBudgetTravelsWithoutAnEffort is the regression this ordering
// exists for. The budget used to be attached from inside the effort
// routing, which returns early when no effort was chosen — and the
// routing that chooses one ships off by default. So the ceiling never
// traveled under the default configuration, and every turn of work spent
// on getting its numbers right applied to a field nobody sent.
func TestTaskBudgetTravelsWithoutAnEffort(t *testing.T) {
	ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudget(64000))
	if client.EffortFromContext(ctx) != client.EffortUnset {
		t.Fatal("precondition: no effort hint on this turn")
	}

	body := map[string]interface{}{"max_tokens": 4096}
	if attached := applyThinkingForEffort(body, "claude-opus-5", ctx); attached {
		t.Fatal("precondition: no effort means no thinking block")
	}
	if !applyTaskBudget(body, "claude-opus-5", ctx) {
		t.Fatal("a spending ceiling must travel whether or not an effort was chosen")
	}

	cfg, ok := body["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("output_config missing: %+v", body["output_config"])
	}
	if _, ok := cfg["effort"]; ok {
		t.Errorf("no effort was chosen, so none may be sent: %+v", cfg)
	}
	budget, ok := cfg["task_budget"].(*client.TaskBudget)
	if !ok || budget.Total != 64000 {
		t.Fatalf("task_budget = %+v", cfg["task_budget"])
	}
}
