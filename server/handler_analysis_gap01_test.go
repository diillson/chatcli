/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"strings"
	"testing"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// TestFirstActionDivergesFromAnalysis covers the GAP-01 server-side guard that
// warns when the LLM's analysis prose describes a containment outcome but the
// first proposed action is a diagnostic that cannot remediate the situation.
// Chaos test Cycle 1 (2026-05-23) showed this exact divergence: analysis said
// "Scaling to 0 is the only correct containment action" and actions[0] was
// ExecDiagnostic.
func TestFirstActionDivergesFromAnalysis(t *testing.T) {
	cases := []struct {
		name string
		in   analysisResult
		want bool
	}{
		{
			name: "no actions → no divergence",
			in:   analysisResult{Analysis: "containment via scale to 0"},
			want: false,
		},
		{
			name: "first action is the remediation → no divergence",
			in: analysisResult{
				Analysis: "scale to 0 is the only correct containment action",
				Actions:  []actionEntry{{Action: "ScaleDeployment", Params: map[string]string{"replicas": "0", "containment": "true"}}},
			},
			want: false,
		},
		{
			name: "first action is ExecDiagnostic but prose has no containment keyword → allowed",
			in: analysisResult{
				Analysis: "OOMKilled pods, increase memory_limit",
				Actions:  []actionEntry{{Action: "ExecDiagnostic", Params: map[string]string{"command": "df -h"}}},
			},
			want: false,
		},
		{
			name: "prose says 'scale to 0' but first action is ExecDiagnostic → diverges",
			in: analysisResult{
				Analysis: "The only correct containment action is to scale to 0 replicas.",
				Actions:  []actionEntry{{Action: "ExecDiagnostic", Params: map[string]string{"command": "df -h"}}},
			},
			want: true,
		},
		{
			name: "prose says 'unrecoverable' but first action is ExecDiagnostic → diverges",
			in: analysisResult{
				Analysis: "The app has an unrecoverable startup bug; ExecDiagnostic cannot help.",
				Actions:  []actionEntry{{Action: "ExecDiagnostic", Params: map[string]string{"command": "ps aux"}}},
			},
			want: true,
		},
		{
			name: "case-insensitive keyword matching",
			in: analysisResult{
				Analysis: "STOP THE BLEEDING — scale the deployment down immediately.",
				Actions:  []actionEntry{{Action: "ExecDiagnostic"}},
			},
			want: true,
		},
		{
			name: "first action is RestartDeployment + containment prose → not flagged (only ExecDiagnostic triggers)",
			in: analysisResult{
				Analysis: "scale to 0 is the only correct containment action",
				Actions:  []actionEntry{{Action: "RestartDeployment"}},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstActionDivergesFromAnalysis(tc.in)
			if got != tc.want {
				t.Fatalf("firstActionDivergesFromAnalysis: want %v, got %v", tc.want, got)
			}
		})
	}
}

// TestSameActionIntent verifies the GAP-01 equivalence predicate used to decide
// whether two SuggestedActions represent the same remediation strategy. Action
// type and the containment flag matter; everything else (replicas count,
// container name, target node) is shifting cluster state and intentionally
// ignored — flipping containment turns a stop-the-bleeding action into a
// destructive one (or vice versa), so it MUST count as a divergence.
func TestSameActionIntent(t *testing.T) {
	cases := []struct {
		name string
		a, b *pb.SuggestedAction
		want bool
	}{
		{
			name: "identical action and containment → same",
			a:    &pb.SuggestedAction{Action: "ScaleDeployment", Params: map[string]string{"replicas": "0", "containment": "true"}},
			b:    &pb.SuggestedAction{Action: "ScaleDeployment", Params: map[string]string{"replicas": "0", "containment": "true"}},
			want: true,
		},
		{
			name: "same type, different non-containment params → same intent",
			a:    &pb.SuggestedAction{Action: "AdjustResources", Params: map[string]string{"memory_limit": "1Gi"}},
			b:    &pb.SuggestedAction{Action: "AdjustResources", Params: map[string]string{"memory_limit": "2Gi"}},
			want: true,
		},
		{
			name: "different action types → diverges",
			a:    &pb.SuggestedAction{Action: "ScaleDeployment"},
			b:    &pb.SuggestedAction{Action: "ExecDiagnostic"},
			want: false,
		},
		{
			name: "same type but containment flipped → diverges (safety-critical)",
			a:    &pb.SuggestedAction{Action: "ScaleDeployment", Params: map[string]string{"replicas": "0", "containment": "true"}},
			b:    &pb.SuggestedAction{Action: "ScaleDeployment", Params: map[string]string{"replicas": "0"}},
			want: false,
		},
		{
			name: "missing containment on both sides → same",
			a:    &pb.SuggestedAction{Action: "RestartDeployment"},
			b:    &pb.SuggestedAction{Action: "RestartDeployment"},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameActionIntent(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("sameActionIntent: want %v, got %v", tc.want, got)
			}
		})
	}
}

// TestAnnotateInsightDivergence is the end-to-end guard for the GAP-01 fix on
// the AgenticStep RPC. The server compares the LLM's next_action with the
// AIInsight's primary suggested action and flips DivergesFromInsight=true so
// the operator can reject the action when no justification is provided.
func TestAnnotateInsightDivergence(t *testing.T) {
	primary := &pb.SuggestedAction{Action: "ScaleDeployment", Params: map[string]string{"replicas": "0", "containment": "true"}}

	t.Run("nil response is a no-op", func(t *testing.T) {
		annotateInsightDivergence(nil, &pb.AgenticStepRequest{})
	})

	t.Run("resolved response is not annotated", func(t *testing.T) {
		resp := &pb.AgenticStepResponse{Resolved: true}
		annotateInsightDivergence(resp, &pb.AgenticStepRequest{
			InsightSuggestedActions: []*pb.SuggestedAction{primary},
		})
		if resp.DivergesFromInsight {
			t.Fatalf("resolved=true should not be flagged as divergent")
		}
	})

	t.Run("no insight actions → no annotation", func(t *testing.T) {
		resp := &pb.AgenticStepResponse{
			NextAction: &pb.SuggestedAction{Action: "RestartDeployment"},
		}
		annotateInsightDivergence(resp, &pb.AgenticStepRequest{})
		if resp.DivergesFromInsight {
			t.Fatalf("no insight actions → DivergesFromInsight must remain false")
		}
	})

	t.Run("matching next_action → not divergent", func(t *testing.T) {
		resp := &pb.AgenticStepResponse{
			NextAction: &pb.SuggestedAction{Action: "ScaleDeployment", Params: map[string]string{"replicas": "0", "containment": "true"}},
		}
		annotateInsightDivergence(resp, &pb.AgenticStepRequest{
			InsightSuggestedActions: []*pb.SuggestedAction{primary},
		})
		if resp.DivergesFromInsight {
			t.Fatalf("matching action should not flag divergence")
		}
	})

	t.Run("different action type → diverges, reason preserved", func(t *testing.T) {
		resp := &pb.AgenticStepResponse{
			NextAction:       &pb.SuggestedAction{Action: "ExecDiagnostic", Params: map[string]string{"command": "df -h"}},
			DivergenceReason: "new evidence shows pods are running",
		}
		annotateInsightDivergence(resp, &pb.AgenticStepRequest{
			InsightSuggestedActions: []*pb.SuggestedAction{primary},
		})
		if !resp.DivergesFromInsight {
			t.Fatalf("ExecDiagnostic vs ScaleDeployment must be flagged as divergent")
		}
		if resp.DivergenceReason != "new evidence shows pods are running" {
			t.Fatalf("LLM-provided DivergenceReason must be preserved verbatim, got %q", resp.DivergenceReason)
		}
	})

	t.Run("containment flag flipped → diverges even with same type", func(t *testing.T) {
		resp := &pb.AgenticStepResponse{
			NextAction: &pb.SuggestedAction{Action: "ScaleDeployment", Params: map[string]string{"replicas": "0"}},
		}
		annotateInsightDivergence(resp, &pb.AgenticStepRequest{
			InsightSuggestedActions: []*pb.SuggestedAction{primary},
		})
		if !resp.DivergesFromInsight {
			t.Fatalf("flipping containment must be flagged as divergent (destructive)")
		}
		if resp.DivergenceReason != "" {
			t.Fatalf("when LLM does not provide reason, it must stay empty (operator rejects)")
		}
	})
}

// TestTruncate covers the small helper used by the divergence warning log.
// truncate slices by byte count then appends a 3-byte UTF-8 ellipsis.
func TestTruncate(t *testing.T) {
	if got := truncate("short", 200); got != "short" {
		t.Fatalf("short string must pass through unchanged, got %q", got)
	}
	const ellipsis = "…"
	long := strings.Repeat("x", 250)
	got := truncate(long, 200)
	if !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("long string must end with the ellipsis marker, got %q", got[max(0, len(got)-10):])
	}
	if got != long[:200]+ellipsis {
		t.Fatalf("long string must be truncated to %d bytes plus the ellipsis marker", 200)
	}
}
