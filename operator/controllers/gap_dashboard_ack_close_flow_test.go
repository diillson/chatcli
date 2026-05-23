/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// TestPostMortemReconciler_AckClearedStatusAllowsClose covers the dashboard
// UX fix where the operator clicks "Ack Human Action" and then "Close" right
// after. The end-to-end contract:
//
//  1. /api/v1/postmortems/{name}/ack-human-action sets the ack annotation
//     AND clears Status.RequiresHumanAction in the same handler so the
//     dashboard re-render shows the Close button immediately
//  2. /api/v1/postmortems/{name}/close transitions Status.State to Closed
//  3. The PostMortemReconciler runs and MUST NOT revert that Closed state
//     because (a) RequiresHumanAction is now false AND (b) the ack
//     annotation is set
//
// Without the status clear in step (1), the dashboard left operators with
// no Close button (the symptom reported as "no button to close a postmortem
// with needs-human tag" and "the Ack button does not do anything").
func TestPostMortemReconciler_AckClearedStatusAllowsClose(t *testing.T) {
	s := newScheme()
	// Initial state simulates a PostMortem after the /ack-human-action endpoint
	// has done its job: annotation set, Status.RequiresHumanAction cleared,
	// State=Closed (operator clicked Close right after Ack).
	pm := &platformv1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pm-acked-then-closed",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationHumanActionAcknowledged: "true",
			},
		},
		Spec: platformv1alpha1.PostMortemSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "iss"},
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"},
			Severity: platformv1alpha1.IssueSeverityHigh,
		},
		Status: platformv1alpha1.PostMortemStatus{
			State:               platformv1alpha1.PostMortemStateClosed,
			RequiresHumanAction: false, // <— cleared by the ack endpoint
			RequiredAction:      "restore the deployment's replicas after fixing the root cause",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.PostMortem{}).
		WithObjects(pm).
		Build()
	r := &PostMortemReconciler{Client: c, Scheme: s}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: pm.Name, Namespace: pm.Namespace}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: unexpected error %v", err)
	}

	var got platformv1alpha1.PostMortem
	if err := c.Get(context.Background(), types.NamespacedName{Name: pm.Name, Namespace: pm.Namespace}, &got); err != nil {
		t.Fatalf("post-reconcile Get: %v", err)
	}
	if got.Status.State != platformv1alpha1.PostMortemStateClosed {
		t.Fatalf("Closed state must stick after Ack-then-Close — the reconciler should not revert when RequiresHumanAction is false. Got state=%q",
			got.Status.State)
	}
	if got.Status.RequiredAction == "" {
		t.Fatalf("RequiredAction must be preserved as historical context even after the action was performed")
	}
}

// TestPostMortemReconciler_StillRevertsWhenAckMissingAndStatusTrue keeps the
// inverse contract: a Closed PostMortem with RequiresHumanAction still true
// and no ack annotation MUST be reverted to Open. This is the original
// GAP-03 guardrail; the dashboard UX fix above must not weaken it.
func TestPostMortemReconciler_StillRevertsWhenAckMissingAndStatusTrue(t *testing.T) {
	s := newScheme()
	pm := &platformv1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{Name: "pm-stale-close", Namespace: "default"},
		Spec: platformv1alpha1.PostMortemSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "iss"},
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"},
			Severity: platformv1alpha1.IssueSeverityHigh,
		},
		Status: platformv1alpha1.PostMortemStatus{
			State:               platformv1alpha1.PostMortemStateClosed,
			RequiresHumanAction: true, // <— still pending, no ack
			RequiredAction:      "restore replicas",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.PostMortem{}).
		WithObjects(pm).
		Build()
	r := &PostMortemReconciler{Client: c, Scheme: s}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: pm.Name, Namespace: pm.Namespace}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: unexpected error %v", err)
	}

	var got platformv1alpha1.PostMortem
	if err := c.Get(context.Background(), types.NamespacedName{Name: pm.Name, Namespace: pm.Namespace}, &got); err != nil {
		t.Fatalf("post-reconcile Get: %v", err)
	}
	if got.Status.State != platformv1alpha1.PostMortemStateOpen {
		t.Fatalf("Closed state must be reverted to Open when RequiresHumanAction is still true and ack is missing — GAP-03 guardrail. Got state=%q",
			got.Status.State)
	}
}
