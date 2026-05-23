/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// TestIssueToIncidentItem_RequiresHumanAction covers the GAP-07 mapper that
// surfaces the Issue's human-action signal in the REST IncidentItem.
//
// Three signal sources, in priority order:
//  1. iss.Status.RequiresHumanAction / RequiredAction (typed fields, GAP-07)
//  2. iss.Status.State == Contained (fallback for 1.122.x Issues that have
//     no typed fields yet)
//  3. RequiresHumanAction condition Message (final fallback for legacy
//     operators that only wrote the condition)
//
// The fallback chain MUST set the boolean from any source AND pull the
// action text from whichever source has it. Failing this test would mean
// the dashboard reverts to "needs human" badge without the actual action
// text (the original 1.122.x UX problem).
func TestIssueToIncidentItem_RequiresHumanAction(t *testing.T) {
	cases := []struct {
		name           string
		issue          v1alpha1.Issue
		wantRequires   bool
		wantAction     string
		wantSourceNote string // documentation for the test reader
	}{
		{
			name: "GAP-07 typed status fields populated → mapped verbatim",
			issue: v1alpha1.Issue{
				Status: v1alpha1.IssueStatus{
					State:               v1alpha1.IssueStateContained,
					RequiresHumanAction: true,
					RequiredAction:      "restore the deployment's replicas to the desired count",
				},
			},
			wantRequires:   true,
			wantAction:     "restore the deployment's replicas to the desired count",
			wantSourceNote: "typed status field is the source of truth",
		},
		{
			name: "1.122.x compat — state=Contained but no typed fields, condition has message",
			issue: v1alpha1.Issue{
				Status: v1alpha1.IssueStatus{
					State: v1alpha1.IssueStateContained,
					Conditions: []metav1.Condition{
						{Type: "RequiresHumanAction", Status: metav1.ConditionTrue, Message: "legacy condition text"},
					},
				},
			},
			wantRequires:   true,
			wantAction:     "", // state=Contained branch hits first; condition text only used when state != Contained
			wantSourceNote: "state=Contained triggers RequiresHumanAction even without typed fields",
		},
		{
			name: "Pure-condition fallback — no state, only condition",
			issue: v1alpha1.Issue{
				Status: v1alpha1.IssueStatus{
					State: v1alpha1.IssueStateResolved,
					Conditions: []metav1.Condition{
						{Type: "RequiresHumanAction", Status: metav1.ConditionTrue, Message: "rollback the bad config"},
					},
				},
			},
			wantRequires:   true,
			wantAction:     "rollback the bad config",
			wantSourceNote: "condition fallback populates RequiredAction from the condition message",
		},
		{
			name: "Resolved Issue with no human-action signal → both empty",
			issue: v1alpha1.Issue{
				Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateResolved},
			},
			wantRequires: false,
			wantAction:   "",
		},
		{
			name: "Typed fields beat fallback — operator-written field takes precedence over condition",
			issue: v1alpha1.Issue{
				Status: v1alpha1.IssueStatus{
					State:               v1alpha1.IssueStateContained,
					RequiresHumanAction: true,
					RequiredAction:      "scale to 3 after fixing image tag",
					Conditions: []metav1.Condition{
						{Type: "RequiresHumanAction", Status: metav1.ConditionTrue, Message: "legacy condition message (stale)"},
					},
				},
			},
			wantRequires: true,
			wantAction:   "scale to 3 after fixing image tag",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := issueToIncidentItem(tc.issue)
			if item.RequiresHumanAction != tc.wantRequires {
				t.Fatalf("RequiresHumanAction: want %v, got %v (%s)", tc.wantRequires, item.RequiresHumanAction, tc.wantSourceNote)
			}
			if item.RequiredAction != tc.wantAction {
				t.Fatalf("RequiredAction: want %q, got %q (%s)", tc.wantAction, item.RequiredAction, tc.wantSourceNote)
			}
		})
	}
}
