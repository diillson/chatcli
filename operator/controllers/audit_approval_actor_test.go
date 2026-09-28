/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// The approval decision event names the approvers who decided; an
// auto-approval or an expiry stays with the approval controller.
func TestAudit_ApprovalDecisionNamesTheApprovers(t *testing.T) {
	ctx := context.Background()
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	rec := NewAuditRecorder(c, s)

	manual := &platformv1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "ar-manual", Namespace: "default"},
		Spec:       platformv1alpha1.ApprovalRequestSpec{IssueRef: platformv1alpha1.IssueRef{Name: "db-outage"}},
		Status: platformv1alpha1.ApprovalRequestStatus{Decisions: []platformv1alpha1.ApprovalDecision{
			{Approver: "alice", Decision: "approved", Timestamp: metav1.Now()},
			{Approver: "bob", Decision: "rejected", Timestamp: metav1.Now()},
			{Approver: "carol", Decision: "approved", Timestamp: metav1.Now()},
		}},
	}
	if err := rec.RecordApprovalDecision(ctx, manual, "approved"); err != nil {
		t.Fatal(err)
	}
	auto := manual.DeepCopy()
	auto.Name = "ar-auto"
	auto.Status.AutoApproved = true
	if err := rec.RecordApprovalDecision(ctx, auto, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordApprovalRequested(ctx, manual); err != nil {
		t.Fatal(err)
	}

	var events platformv1alpha1.AuditEventList
	if err := c.List(ctx, &events); err != nil {
		t.Fatal(err)
	}
	byResource := map[string]platformv1alpha1.AuditEvent{}
	for _, e := range events.Items {
		byResource[e.Spec.EventType+"/"+e.Spec.Resource.Name] = e
	}
	got := byResource["approval_approved/ar-manual"]
	if got.Spec.Actor.Type != "user" || got.Spec.Actor.Name != "alice,carol" || got.Spec.Details["approvers"] != "alice,carol" {
		t.Fatalf("manual decision actor = %+v details %v, want the approving users", got.Spec.Actor, got.Spec.Details)
	}
	if got.Spec.Actor.Controller != "remediation-controller" {
		t.Fatalf("writer = %q, want the remediation controller that records the outcome", got.Spec.Actor.Controller)
	}
	if a := byResource["approval_approved/ar-auto"].Spec.Actor; a.Type != "controller" || a.Name != "ApprovalReconciler" {
		t.Fatalf("auto-approval actor = %+v, want the approval controller", a)
	}
	if a := byResource["approval_requested/ar-manual"].Spec.Actor; a.Name != "RemediationReconciler" {
		t.Fatalf("request actor = %+v, want the remediation controller that created it", a)
	}
}
