/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// TestPostMortemName covers the GAP-03 PostMortem name builder. The K8s name
// limit is 63 chars; longer issue names get truncated to stay under it.
func TestPostMortemName(t *testing.T) {
	cases := []struct {
		issueName string
		want      string
	}{
		{"web-crash-1", "pm-web-crash-1"},
		{"a-very-long-issue-name-that-pushes-the-pm-prefix-over-63-chars", "pm-a-very-long-issue-name-that-pushes-the-pm-prefix-over-63-cha"},
		{"", "pm-"},
	}
	for _, tc := range cases {
		t.Run(tc.issueName, func(t *testing.T) {
			got := postMortemName(&platformv1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: tc.issueName}})
			if got != tc.want {
				t.Fatalf("postMortemName: want %q, got %q", tc.want, got)
			}
			if len(got) > 63 {
				t.Fatalf("postMortemName must respect the 63-char RFC 1123 limit, got len=%d", len(got))
			}
		})
	}
}

// TestPostMortemDuration covers the duration formatter. Issues without a
// DetectedAt timestamp return an empty string so callers can decide whether
// to omit the duration line entirely.
func TestPostMortemDuration(t *testing.T) {
	now := metav1.Now()
	twoMinAgo := metav1.NewTime(now.Add(-2 * time.Minute))

	t.Run("nil DetectedAt → empty string", func(t *testing.T) {
		if d := postMortemDuration(&platformv1alpha1.Issue{}, now); d != "" {
			t.Fatalf("missing DetectedAt must return empty string, got %q", d)
		}
	})

	t.Run("DetectedAt populated → human-readable duration", func(t *testing.T) {
		issue := &platformv1alpha1.Issue{Status: platformv1alpha1.IssueStatus{DetectedAt: &twoMinAgo}}
		d := postMortemDuration(issue, now)
		if d != "2m0s" {
			t.Fatalf("duration: want 2m0s, got %q", d)
		}
	})
}

// TestSplitAnnotation covers the multi-value annotation splitter that
// generatePostMortem uses to read lessons-learned and prevention-actions from
// plan annotations (joined with the "\n---\n" sentinel).
func TestSplitAnnotation(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		wantN int
	}{
		{name: "empty → nil", raw: "", wantN: 0},
		{name: "single entry → length 1", raw: "lesson one", wantN: 1},
		{name: "two entries", raw: "lesson one\n---\nlesson two", wantN: 2},
		{name: "three entries", raw: "a\n---\nb\n---\nc", wantN: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitAnnotation(tc.raw)
			if len(got) != tc.wantN {
				t.Fatalf("split %q: want %d entries, got %d (%v)", tc.raw, tc.wantN, len(got), got)
			}
		})
	}
}

// TestAgenticHistoryToTimelineEvent covers the conversion that surfaces the
// AI's reasoning chain into the PostMortem timeline. The detail string differs
// whether the step succeeded or failed and whether the AI provided reasoning.
func TestAgenticHistoryToTimelineEvent(t *testing.T) {
	now := metav1.Now()
	scaleAction := &platformv1alpha1.RemediationAction{Type: platformv1alpha1.ActionScaleDeployment}

	cases := []struct {
		name        string
		step        platformv1alpha1.AgenticStep
		wantType    string
		wantSubstrs []string
	}{
		{
			name: "successful step with AI reasoning → detail embeds reasoning",
			step: platformv1alpha1.AgenticStep{
				StepNumber:  1,
				AIMessage:   "scale to 0 stops the crashloop",
				Action:      scaleAction,
				Observation: "SUCCESS: ScaleDeployment executed",
				Timestamp:   now,
			},
			wantType:    "action_executed",
			wantSubstrs: []string{"AI reasoning: scale to 0", "ScaleDeployment", "SUCCESS"},
		},
		{
			name: "failed step → type flips to action_failed",
			step: platformv1alpha1.AgenticStep{
				StepNumber:  2,
				Action:      scaleAction,
				Observation: "FAILED: rbac forbidden",
			},
			wantType:    "action_failed",
			wantSubstrs: []string{"ScaleDeployment", "FAILED"},
		},
		{
			name: "observation-only step (no Action) → renders as (observation)",
			step: platformv1alpha1.AgenticStep{
				StepNumber:  3,
				Action:      nil,
				Observation: "Observation step — no action taken",
			},
			wantType:    "action_executed",
			wantSubstrs: []string{"(observation)"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := agenticHistoryToTimelineEvent(tc.step)
			if ev.Type != tc.wantType {
				t.Fatalf("type: want %q, got %q", tc.wantType, ev.Type)
			}
			for _, want := range tc.wantSubstrs {
				if !strings.Contains(ev.Detail, want) {
					t.Fatalf("detail %q must contain %q", ev.Detail, want)
				}
			}
		})
	}
}

// TestBuildPostMortemTimeline covers the full timeline assembly. The result
// always opens with a "detected" event (if DetectedAt is set) and closes with
// a "resolved" event carrying the plan's Result.
func TestBuildPostMortemTimeline(t *testing.T) {
	now := metav1.Now()
	detectedAt := metav1.NewTime(now.Add(-5 * time.Minute))
	startedAt := metav1.NewTime(now.Add(-2 * time.Minute))

	issue := &platformv1alpha1.Issue{
		Spec:   platformv1alpha1.IssueSpec{Description: "pod crashloop"},
		Status: platformv1alpha1.IssueStatus{DetectedAt: &detectedAt},
	}

	t.Run("agentic plan timeline has detected + N step events + resolved", func(t *testing.T) {
		plan := &platformv1alpha1.RemediationPlan{
			Spec: platformv1alpha1.RemediationPlanSpec{
				AgenticHistory: []platformv1alpha1.AgenticStep{
					{StepNumber: 1, Action: &platformv1alpha1.RemediationAction{Type: platformv1alpha1.ActionRestartDeployment}, Observation: "SUCCESS"},
					{StepNumber: 2, Action: nil, Observation: "Observation"},
				},
			},
			Status: platformv1alpha1.RemediationPlanStatus{Result: "deployment recovered"},
		}
		tl := buildPostMortemTimeline(issue, plan, now)
		if len(tl) != 4 {
			t.Fatalf("timeline length: want 4 (detected + 2 steps + resolved), got %d", len(tl))
		}
		if tl[0].Type != "detected" || tl[len(tl)-1].Type != "resolved" {
			t.Fatalf("timeline must open with detected and close with resolved, got %s ... %s", tl[0].Type, tl[len(tl)-1].Type)
		}
		if tl[len(tl)-1].Detail != "deployment recovered" {
			t.Fatalf("resolved event must carry plan.Status.Result")
		}
	})

	t.Run("runbook plan timeline has detected + one event per action + resolved", func(t *testing.T) {
		plan := &platformv1alpha1.RemediationPlan{
			Spec: platformv1alpha1.RemediationPlanSpec{
				Actions: []platformv1alpha1.RemediationAction{
					{Type: platformv1alpha1.ActionScaleDeployment, Params: map[string]string{"replicas": "3"}},
					{Type: platformv1alpha1.ActionRestartDeployment},
				},
			},
			Status: platformv1alpha1.RemediationPlanStatus{StartedAt: &startedAt, Result: "scaled and restarted"},
		}
		tl := buildPostMortemTimeline(issue, plan, now)
		if len(tl) != 4 {
			t.Fatalf("timeline length: want 4 (detected + 2 actions + resolved), got %d", len(tl))
		}
		for _, ev := range tl[1 : len(tl)-1] {
			if ev.Type != "action_executed" {
				t.Fatalf("middle events must be action_executed, got %s", ev.Type)
			}
			if !ev.Timestamp.Equal(&startedAt) {
				t.Fatalf("action events must inherit plan.Status.StartedAt")
			}
		}
	})

	t.Run("issue without DetectedAt → timeline opens with first action event", func(t *testing.T) {
		issueNoDetected := &platformv1alpha1.Issue{Spec: platformv1alpha1.IssueSpec{Description: "x"}}
		plan := &platformv1alpha1.RemediationPlan{
			Spec:   platformv1alpha1.RemediationPlanSpec{Actions: []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionRestartDeployment}}},
			Status: platformv1alpha1.RemediationPlanStatus{Result: "done"},
		}
		tl := buildPostMortemTimeline(issueNoDetected, plan, now)
		if tl[0].Type != "action_executed" {
			t.Fatalf("without DetectedAt the timeline must start at the first action, got %s", tl[0].Type)
		}
	})
}

// TestBuildPostMortemActions covers the action-record assembly for the two
// plan shapes (agentic history vs runbook actions). Agentic observation-only
// steps must be skipped — they leave no executable action record.
func TestBuildPostMortemActions(t *testing.T) {
	now := metav1.Now()

	t.Run("agentic plan: actions list skips observation-only steps", func(t *testing.T) {
		plan := &platformv1alpha1.RemediationPlan{
			Spec: platformv1alpha1.RemediationPlanSpec{
				AgenticHistory: []platformv1alpha1.AgenticStep{
					{Action: &platformv1alpha1.RemediationAction{Type: platformv1alpha1.ActionScaleDeployment}, AIMessage: "step one", Observation: "SUCCESS"},
					{Action: nil, AIMessage: "watching", Observation: "wait"},
					{Action: &platformv1alpha1.RemediationAction{Type: platformv1alpha1.ActionRestartDeployment}, Observation: "FAILED: rbac"},
				},
			},
		}
		records := buildPostMortemActions(plan, now)
		if len(records) != 2 {
			t.Fatalf("actions: want 2 (observation-only skipped), got %d", len(records))
		}
		if records[0].Result != "success" || records[1].Result != "failed" {
			t.Fatalf("results: want success/failed, got %s/%s", records[0].Result, records[1].Result)
		}
		if !strings.Contains(records[0].Detail, "[AI: step one]") {
			t.Fatalf("AI reasoning must be embedded in the detail string, got %q", records[0].Detail)
		}
	})

	t.Run("runbook plan: one record per action with checkpoint outcome", func(t *testing.T) {
		plan := &platformv1alpha1.RemediationPlan{
			Spec: platformv1alpha1.RemediationPlanSpec{
				Actions: []platformv1alpha1.RemediationAction{
					{Type: platformv1alpha1.ActionScaleDeployment},
					{Type: platformv1alpha1.ActionRestartDeployment},
				},
			},
			Status: platformv1alpha1.RemediationPlanStatus{
				ActionCheckpoints: []platformv1alpha1.ActionCheckpoint{
					{ActionIndex: 1, Success: false},
				},
			},
		}
		records := buildPostMortemActions(plan, now)
		if len(records) != 2 {
			t.Fatalf("want 2 records, got %d", len(records))
		}
		if records[0].Result != "success" {
			t.Fatalf("action 0 must default to success when no checkpoint says otherwise")
		}
		if records[1].Result != "failed" {
			t.Fatalf("action 1 must reflect the failed checkpoint")
		}
	})
}

// TestPlanActionTimestamp covers the small helper that picks the timestamp to
// attribute to a plan action. Falls back to "now" when StartedAt is nil.
func TestPlanActionTimestamp(t *testing.T) {
	now := metav1.Now()
	earlier := metav1.NewTime(now.Add(-time.Hour))

	t.Run("StartedAt populated → use it", func(t *testing.T) {
		plan := &platformv1alpha1.RemediationPlan{Status: platformv1alpha1.RemediationPlanStatus{StartedAt: &earlier}}
		if got := planActionTimestamp(plan, now); !got.Equal(&earlier) {
			t.Fatalf("want StartedAt, got %v", got)
		}
	})
	t.Run("StartedAt nil → fall back to now", func(t *testing.T) {
		if got := planActionTimestamp(&platformv1alpha1.RemediationPlan{}, now); !got.Equal(&now) {
			t.Fatalf("want now, got %v", got)
		}
	})
}

// TestApplyPostMortemContextLabels covers the GAP-04 chaos enrichment that
// stamps the PostMortem with chaos correlation labels. After GAP-07 the
// human-action fields moved from Spec to Status and are written by
// applyPostMortemStatusFields, not by this helper.
func TestApplyPostMortemContextLabels(t *testing.T) {
	t.Run("chaos-induced Issue: PostMortem gets source + experiment labels", func(t *testing.T) {
		issue := &platformv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
				LabelSource:                            SourceChaosExperiment,
				"platform.chatcli.io/chaos-experiment": "kill-pod-1",
			}},
		}
		pm := &platformv1alpha1.PostMortem{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}
		applyPostMortemContextLabels(pm, issue, &platformv1alpha1.RemediationPlan{})
		if pm.Labels[LabelSource] != SourceChaosExperiment {
			t.Fatalf("source label must propagate, got %v", pm.Labels)
		}
		if pm.Labels["platform.chatcli.io/chaos-experiment"] != "kill-pod-1" {
			t.Fatalf("experiment name must propagate, got %v", pm.Labels)
		}
	})

	t.Run("Resolved Issue without chaos label: nothing extra is set", func(t *testing.T) {
		issue := &platformv1alpha1.Issue{Status: platformv1alpha1.IssueStatus{State: platformv1alpha1.IssueStateResolved}}
		pm := &platformv1alpha1.PostMortem{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}
		applyPostMortemContextLabels(pm, issue, &platformv1alpha1.RemediationPlan{})
		if len(pm.Labels) != 0 {
			t.Fatalf("normal Resolved issue must not be enriched, got Labels=%v", pm.Labels)
		}
	})
}

// TestApplyPostMortemStatusFields covers the idempotent status writer that
// is also called on the conflict-retry path.
func TestApplyPostMortemStatusFields(t *testing.T) {
	now := metav1.Now()
	pm := &platformv1alpha1.PostMortem{}
	timeline := []platformv1alpha1.TimelineEvent{{Type: "detected"}}
	actions := []platformv1alpha1.ActionRecord{{Action: "RestartDeployment", Result: "success"}}
	narrative := postMortemNarrative{
		Summary:           "OOMKilled, increased memory_limit",
		RootCause:         "memory leak in v1.5.0",
		Impact:            "30s of 502s",
		LessonsLearned:    []string{"add OOM alert", "tune memory_limit"},
		PreventionActions: []string{"add resource quota"},
	}

	applyPostMortemStatusFields(pm, narrative, timeline, actions, "12s", &now)

	if pm.Status.State != platformv1alpha1.PostMortemStateOpen {
		t.Fatalf("state must start Open, got %q", pm.Status.State)
	}
	if pm.Status.Summary != narrative.Summary || pm.Status.RootCause != narrative.RootCause {
		t.Fatalf("narrative fields must be copied verbatim")
	}
	if len(pm.Status.LessonsLearned) != 2 || pm.Status.PreventionActions[0] != "add resource quota" {
		t.Fatalf("lesson/prevention lists must be propagated")
	}
	if pm.Status.Duration != "12s" || !pm.Status.GeneratedAt.Equal(&now) {
		t.Fatalf("Duration / GeneratedAt must be applied")
	}
}
