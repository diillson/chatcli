package controllers

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func setupFakePostMortemReconciler(objs ...client.Object) (*PostMortemReconciler, client.Client) {
	s := newScheme()
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(
		&platformv1alpha1.PostMortem{},
	)
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()
	return &PostMortemReconciler{Client: c, Scheme: s}, c
}

func TestPostMortem_InitializesState(t *testing.T) {
	pm := &platformv1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pm-test-issue",
			Namespace: "default",
		},
		Spec: platformv1alpha1.PostMortemSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "test-issue"},
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"},
			Severity: platformv1alpha1.IssueSeverityCritical,
		},
	}

	r, c := setupFakePostMortemReconciler(pm)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pm-test-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated platformv1alpha1.PostMortem
	if err := c.Get(ctx, types.NamespacedName{Name: "pm-test-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated PostMortem: %v", err)
	}
	if updated.Status.State != platformv1alpha1.PostMortemStateOpen {
		t.Errorf("expected state Open, got %q", updated.Status.State)
	}
}

func TestPostMortem_TerminalClosed(t *testing.T) {
	pm := &platformv1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pm-closed",
			Namespace: "default",
		},
		Spec: platformv1alpha1.PostMortemSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "closed-issue"},
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"},
			Severity: platformv1alpha1.IssueSeverityMedium,
		},
		Status: platformv1alpha1.PostMortemStatus{
			State: platformv1alpha1.PostMortemStateClosed,
		},
	}

	r, c := setupFakePostMortemReconciler(pm)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pm-closed", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.Requeue || result.RequeueAfter > 0 {
		t.Error("expected no requeue for terminal state")
	}

	var updated platformv1alpha1.PostMortem
	if err := c.Get(ctx, types.NamespacedName{Name: "pm-closed", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get PostMortem: %v", err)
	}
	if updated.Status.State != platformv1alpha1.PostMortemStateClosed {
		t.Errorf("expected state Closed, got %q", updated.Status.State)
	}
}
