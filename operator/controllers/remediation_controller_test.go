package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

func setupFakeRemediationReconciler(objs ...client.Object) (*RemediationReconciler, client.Client) {
	s := newScheme()
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(
		&platformv1alpha1.RemediationPlan{},
		&platformv1alpha1.Issue{},
	)
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()
	return &RemediationReconciler{Client: c, Scheme: s}, c
}

func newRemediationPlan(name, ns string) *platformv1alpha1.RemediationPlan {
	return &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       "plan-uid",
		},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "test-issue"},
			Attempt:  1,
			Strategy: "Scale up deployment",
			Actions: []platformv1alpha1.RemediationAction{
				{
					Type:   platformv1alpha1.ActionScaleDeployment,
					Params: map[string]string{"replicas": "5"},
				},
			},
			SafetyConstraints: []string{"Do not scale below 2 replicas"},
		},
	}
}

func newDeployment(name, ns string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       "deploy-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: name, Image: "test:latest"},
					},
				},
			},
		},
	}
}

func newConfigMap(name, ns string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       "cm-uid",
		},
		Data: data,
	}
}

func TestRemediationReconcile_NotFound(t *testing.T) {
	r, _ := setupFakeRemediationReconciler()
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue")
	}
}

func TestRemediationReconcile_PendingToExecuting(t *testing.T) {
	plan := newRemediationPlan("test-plan", "default")
	r, c := setupFakeRemediationReconciler(plan)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if !result.Requeue {
		t.Error("expected requeue after transitioning to Executing")
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "test-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated plan: %v", err)
	}
	if updated.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Errorf("expected state Executing, got %q", updated.Status.State)
	}
	if updated.Status.StartedAt == nil {
		t.Error("expected startedAt to be set")
	}
}

func TestRemediationReconcile_SafetyConstraintViolation(t *testing.T) {
	plan := newRemediationPlan("unsafe-plan", "default")
	plan.Spec.Actions = []platformv1alpha1.RemediationAction{
		{
			Type:   platformv1alpha1.ActionScaleDeployment,
			Params: map[string]string{"replicas": "0"},
		},
	}

	r, c := setupFakeRemediationReconciler(plan)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "unsafe-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "unsafe-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated plan: %v", err)
	}
	if updated.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Errorf("expected state Failed, got %q", updated.Status.State)
	}
	if updated.Status.Result == "" {
		t.Error("expected result message about safety constraint")
	}
}

func TestRemediationReconcile_CustomActionBlocked(t *testing.T) {
	plan := newRemediationPlan("custom-plan", "default")
	plan.Spec.Actions = []platformv1alpha1.RemediationAction{
		{
			Type:   platformv1alpha1.ActionCustom,
			Params: map[string]string{"script": "cleanup.sh"},
		},
	}

	r, c := setupFakeRemediationReconciler(plan)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "custom-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "custom-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated plan: %v", err)
	}
	if updated.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Errorf("expected state Failed, got %q", updated.Status.State)
	}
}

func TestRemediationReconcile_ScaleDeployment(t *testing.T) {
	issue := newIssue("test-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "payments-api", Namespace: "default"}

	plan := newRemediationPlan("scale-plan", "default")
	plan.Status.State = platformv1alpha1.RemediationStateExecuting
	now := metav1.Now()
	plan.Status.StartedAt = &now

	deploy := newDeployment("payments-api", "default", 2)

	r, c := setupFakeRemediationReconciler(issue, plan, deploy)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "scale-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify deployment was scaled
	var updated appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "payments-api", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if *updated.Spec.Replicas != 5 {
		t.Errorf("expected 5 replicas, got %d", *updated.Spec.Replicas)
	}

	// After executing, plan should be in Verifying state
	var updatedPlan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "scale-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateVerifying {
		t.Errorf("expected state Verifying, got %q", updatedPlan.Status.State)
	}
	if updatedPlan.Status.ActionsCompletedAt == nil {
		t.Error("expected actionsCompletedAt to be set")
	}
	if len(updatedPlan.Status.Evidence) < 1 {
		t.Errorf("expected at least 1 evidence item, got %d", len(updatedPlan.Status.Evidence))
	}
	// First evidence should be preflight snapshot
	if len(updatedPlan.Status.Evidence) > 0 && updatedPlan.Status.Evidence[0].Type != "preflight_snapshot" {
		t.Errorf("expected first evidence to be preflight_snapshot, got %q", updatedPlan.Status.Evidence[0].Type)
	}

	// Simulate healthy deployment status for verification
	if err := c.Get(ctx, types.NamespacedName{Name: "payments-api", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	updated.Status.ReadyReplicas = 5
	updated.Status.UpdatedReplicas = 5
	updated.Status.Replicas = 5
	updated.Status.UnavailableReplicas = 0
	if err := c.Status().Update(ctx, &updated); err != nil {
		t.Fatalf("failed to update deployment status: %v", err)
	}

	// Reconcile again: Verifying → Completed
	_, err = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "scale-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("verification reconcile failed: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: "scale-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateCompleted {
		t.Errorf("expected state Completed after verification, got %q", updatedPlan.Status.State)
	}
	if updatedPlan.Status.CompletedAt == nil {
		t.Error("expected completedAt to be set")
	}
}

func TestRemediationReconcile_RestartDeployment(t *testing.T) {
	issue := newIssue("restart-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web-app", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restart-plan",
			Namespace: "default",
			UID:       "plan-uid-restart",
		},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "restart-issue"},
			Attempt:  1,
			Strategy: "Restart deployment",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionRestartDeployment},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State: platformv1alpha1.RemediationStateExecuting,
		},
	}
	now := metav1.Now()
	plan.Status.StartedAt = &now

	deploy := newDeployment("web-app", "default", 3)

	r, c := setupFakeRemediationReconciler(issue, plan, deploy)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "restart-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify restart annotation was set
	var updated appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "web-app", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if _, ok := updated.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; !ok {
		t.Error("expected restartedAt annotation to be set")
	}

	// After executing, plan should be in Verifying state
	var updatedPlan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "restart-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateVerifying {
		t.Errorf("expected state Verifying, got %q", updatedPlan.Status.State)
	}

	// Simulate healthy deployment status for verification
	if err := c.Get(ctx, types.NamespacedName{Name: "web-app", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	updated.Status.ReadyReplicas = 3
	updated.Status.UpdatedReplicas = 3
	updated.Status.Replicas = 3
	updated.Status.UnavailableReplicas = 0
	if err := c.Status().Update(ctx, &updated); err != nil {
		t.Fatalf("failed to update deployment status: %v", err)
	}

	// Reconcile again: Verifying → Completed
	_, err = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "restart-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("verification reconcile failed: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: "restart-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateCompleted {
		t.Errorf("expected state Completed after verification, got %q", updatedPlan.Status.State)
	}
}

func TestRemediationReconcile_RollbackDeployment(t *testing.T) {
	issue := newIssue("rollback-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "api-server", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rollback-plan",
			Namespace: "default",
			UID:       "plan-uid-rollback",
		},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "rollback-issue"},
			Attempt:  1,
			Strategy: "Rollback deployment",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionRollbackDeployment},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State: platformv1alpha1.RemediationStateExecuting,
		},
	}
	now := metav1.Now()
	plan.Status.StartedAt = &now

	deploy := newDeployment("api-server", "default", 2)
	deploy.Spec.Template.Spec.Containers[0].Image = "api:v2" // current bad image

	// Create two ReplicaSets: revision 1 (old good) and revision 2 (current bad)
	rs1 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-server-rs1", Namespace: "default", UID: "rs1-uid",
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "1"},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "api-server", UID: deploy.UID, APIVersion: "apps/v1"},
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api-server"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api-server"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api-server", Image: "api:v1"}}},
			},
		},
		Status: appsv1.ReplicaSetStatus{ReadyReplicas: 2},
	}
	rs2 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-server-rs2", Namespace: "default", UID: "rs2-uid",
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "2"},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "api-server", UID: deploy.UID, APIVersion: "apps/v1"},
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api-server"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api-server"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api-server", Image: "api:v2"}}},
			},
		},
		Status: appsv1.ReplicaSetStatus{ReadyReplicas: 0},
	}

	r, c := setupFakeRemediationReconciler(issue, plan, deploy, rs1, rs2)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rollback-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify deployment now has the old image from revision 1
	var updated appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "api-server", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updated.Spec.Template.Spec.Containers[0].Image != "api:v1" {
		t.Errorf("expected image api:v1 after rollback, got %q", updated.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestRemediationReconcile_RollbackDeployment_Healthy(t *testing.T) {
	issue := newIssue("rollback-healthy-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "svc", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-healthy-plan", Namespace: "default", UID: "plan-uid-rh"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "rollback-healthy-issue"},
			Attempt:  1, Strategy: "Rollback healthy",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionRollbackDeployment, Params: map[string]string{"toRevision": "healthy"}},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{State: platformv1alpha1.RemediationStateExecuting, StartedAt: func() *metav1.Time { n := metav1.Now(); return &n }()},
	}

	deploy := newDeployment("svc", "default", 2)
	deploy.Spec.Template.Spec.Containers[0].Image = "svc:v3"

	makeRS := func(name string, rev string, image string, ready int32) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default", UID: types.UID(name),
				Annotations:     map[string]string{"deployment.kubernetes.io/revision": rev},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "svc", UID: deploy.UID, APIVersion: "apps/v1"}},
			},
			Spec: appsv1.ReplicaSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "svc"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "svc"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "svc", Image: image}}},
				},
			},
			Status: appsv1.ReplicaSetStatus{ReadyReplicas: ready},
		}
	}

	// Rev 3 (current, broken), Rev 2 (also broken), Rev 1 (healthy)
	rs1 := makeRS("svc-rs1", "1", "svc:v1", 2)
	rs2 := makeRS("svc-rs2", "2", "svc:v2", 0)
	rs3 := makeRS("svc-rs3", "3", "svc:v3", 0)

	r, c := setupFakeRemediationReconciler(issue, plan, deploy, rs1, rs2, rs3)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "rollback-healthy-plan", Namespace: "default"}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "svc", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	// Should skip rev 2 (broken) and pick rev 1 (healthy)
	if updated.Spec.Template.Spec.Containers[0].Image != "svc:v1" {
		t.Errorf("expected image svc:v1 (healthy revision), got %q", updated.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestRemediationReconcile_RollbackDeployment_SpecificRevision(t *testing.T) {
	issue := newIssue("rollback-rev-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "app", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-rev-plan", Namespace: "default", UID: "plan-uid-rr"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "rollback-rev-issue"},
			Attempt:  1, Strategy: "Rollback to rev 1",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionRollbackDeployment, Params: map[string]string{"toRevision": "1"}},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{State: platformv1alpha1.RemediationStateExecuting, StartedAt: func() *metav1.Time { n := metav1.Now(); return &n }()},
	}

	deploy := newDeployment("app", "default", 1)
	deploy.Spec.Template.Spec.Containers[0].Image = "app:v3"

	makeRS := func(name string, rev string, image string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default", UID: types.UID(name),
				Annotations:     map[string]string{"deployment.kubernetes.io/revision": rev},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "app", UID: deploy.UID, APIVersion: "apps/v1"}},
			},
			Spec: appsv1.ReplicaSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "app"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "app"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
				},
			},
		}
	}

	r, c := setupFakeRemediationReconciler(issue, plan, deploy, makeRS("app-rs1", "1", "app:v1"), makeRS("app-rs2", "2", "app:v2"), makeRS("app-rs3", "3", "app:v3"))
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "rollback-rev-plan", Namespace: "default"}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "app", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get: %v", err)
	}
	if updated.Spec.Template.Spec.Containers[0].Image != "app:v1" {
		t.Errorf("expected app:v1, got %q", updated.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestRemediationReconcile_RollbackDeployment_TooFewRevisions(t *testing.T) {
	issue := newIssue("rollback-few-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "solo", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "rollback-few-plan", Namespace: "default", UID: "plan-uid-rf"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "rollback-few-issue"},
			Attempt:  1, Strategy: "Rollback",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionRollbackDeployment},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{State: platformv1alpha1.RemediationStateExecuting, StartedAt: func() *metav1.Time { n := metav1.Now(); return &n }()},
	}

	deploy := newDeployment("solo", "default", 1)
	// Only 1 RS — cannot rollback
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "solo-rs1", Namespace: "default", UID: "solo-rs1",
			Annotations:     map[string]string{"deployment.kubernetes.io/revision": "1"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "solo", UID: deploy.UID, APIVersion: "apps/v1"}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "solo"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "solo"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "solo", Image: "solo:v1"}}},
			},
		},
	}

	r, c := setupFakeRemediationReconciler(issue, plan, deploy, rs)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "rollback-few-plan", Namespace: "default"}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updatedPlan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "rollback-few-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Errorf("expected Failed state, got %q", updatedPlan.Status.State)
	}
	if !strings.Contains(updatedPlan.Status.Result, "fewer than 2 revisions") {
		t.Errorf("expected 'fewer than 2 revisions' in result, got %q", updatedPlan.Status.Result)
	}
}

func TestRemediationReconcile_AdjustResources(t *testing.T) {
	issue := newIssue("oom-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "worker", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "adjust-plan", Namespace: "default", UID: "plan-uid-adj"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "oom-issue"},
			Attempt:  1, Strategy: "Adjust resources",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionAdjustResources, Params: map[string]string{"memory_limit": "1Gi", "memory_request": "512Mi"}},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{State: platformv1alpha1.RemediationStateExecuting, StartedAt: func() *metav1.Time { n := metav1.Now(); return &n }()},
	}

	deploy := newDeployment("worker", "default", 2)

	r, c := setupFakeRemediationReconciler(issue, plan, deploy)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adjust-plan", Namespace: "default"}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "worker", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	memLimit := updated.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != "1Gi" {
		t.Errorf("expected memory limit 1Gi, got %s", memLimit.String())
	}
	memReq := updated.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "512Mi" {
		t.Errorf("expected memory request 512Mi, got %s", memReq.String())
	}
}

func TestRemediationReconcile_AdjustResources_LimitBelowRequest(t *testing.T) {
	issue := newIssue("adj-safety-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "app", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "adj-safety-plan", Namespace: "default", UID: "plan-uid-as"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "adj-safety-issue"},
			Attempt:  1, Strategy: "Bad adjust",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionAdjustResources, Params: map[string]string{"memory_limit": "256Mi", "memory_request": "512Mi"}},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{State: platformv1alpha1.RemediationStateExecuting, StartedAt: func() *metav1.Time { n := metav1.Now(); return &n }()},
	}

	deploy := newDeployment("app", "default", 1)

	r, c := setupFakeRemediationReconciler(issue, plan, deploy)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "adj-safety-plan", Namespace: "default"}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updatedPlan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "adj-safety-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Errorf("expected Failed state, got %q", updatedPlan.Status.State)
	}
	if !strings.Contains(updatedPlan.Status.Result, "cannot be less than request") {
		t.Errorf("expected limit-below-request error, got %q", updatedPlan.Status.Result)
	}
}

func TestRemediationReconcile_DeletePod_MostUnhealthy(t *testing.T) {
	issue := newIssue("crash-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "api", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-pod-plan", Namespace: "default", UID: "plan-uid-dp"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "crash-issue"},
			Attempt:  1, Strategy: "Delete unhealthy pod",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionDeletePod},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{State: platformv1alpha1.RemediationStateExecuting, StartedAt: func() *metav1.Time { n := metav1.Now(); return &n }()},
	}

	deploy := newDeployment("api", "default", 3)

	// Create 3 pods: pod-1 (healthy), pod-2 (high restarts), pod-3 (CrashLoopBackOff)
	makePod := func(name string, rsName string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{Kind: "ReplicaSet", Name: rsName, APIVersion: "apps/v1"},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-rs1", Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "api", UID: deploy.UID, APIVersion: "apps/v1"},
			},
		},
	}

	pod1 := makePod("pod-1", "api-rs1")
	pod1.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 0}}

	pod2 := makePod("pod-2", "api-rs1")
	pod2.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 5}}

	pod3 := makePod("pod-3", "api-rs1")
	pod3.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", RestartCount: 8,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}

	r, c := setupFakeRemediationReconciler(issue, plan, deploy, rs, pod1, pod2, pod3)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "delete-pod-plan", Namespace: "default"}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// pod-3 (CrashLoopBackOff) should have been deleted
	var deletedPod corev1.Pod
	err = c.Get(ctx, types.NamespacedName{Name: "pod-3", Namespace: "default"}, &deletedPod)
	if err == nil {
		t.Error("expected pod-3 to be deleted, but it still exists")
	}
	// pod-1 and pod-2 should still exist
	if err := c.Get(ctx, types.NamespacedName{Name: "pod-1", Namespace: "default"}, &deletedPod); err != nil {
		t.Errorf("pod-1 should still exist: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: "pod-2", Namespace: "default"}, &deletedPod); err != nil {
		t.Errorf("pod-2 should still exist: %v", err)
	}
}

func TestRemediationReconcile_DeletePod_SinglePod_Refused(t *testing.T) {
	issue := newIssue("single-pod-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "solo", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-single-plan", Namespace: "default", UID: "plan-uid-ds"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "single-pod-issue"},
			Attempt:  1, Strategy: "Delete pod",
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionDeletePod},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{State: platformv1alpha1.RemediationStateExecuting, StartedAt: func() *metav1.Time { n := metav1.Now(); return &n }()},
	}

	deploy := newDeployment("solo", "default", 1)
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "solo-rs1", Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "solo", UID: deploy.UID, APIVersion: "apps/v1"},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "solo-pod", Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "solo-rs1", APIVersion: "apps/v1"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	r, c := setupFakeRemediationReconciler(issue, plan, deploy, rs, pod)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "delete-single-plan", Namespace: "default"}})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updatedPlan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "delete-single-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Errorf("expected Failed, got %q", updatedPlan.Status.State)
	}
	if !strings.Contains(updatedPlan.Status.Result, "refusing to delete") {
		t.Errorf("expected 'refusing to delete' in result, got %q", updatedPlan.Status.Result)
	}
}

func TestRemediationReconcile_PatchConfig(t *testing.T) {
	issue := newIssue("config-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "app", Namespace: "default"}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "config-plan",
			Namespace: "default",
			UID:       "plan-uid-config",
		},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "config-issue"},
			Attempt:  1,
			Strategy: "Patch ConfigMap",
			Actions: []platformv1alpha1.RemediationAction{
				{
					Type: platformv1alpha1.ActionPatchConfig,
					Params: map[string]string{
						"configmap":  "app-config",
						"log_level":  "debug",
						"rate_limit": "1000",
					},
				},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State: platformv1alpha1.RemediationStateExecuting,
		},
	}
	now := metav1.Now()
	plan.Status.StartedAt = &now

	cm := newConfigMap("app-config", "default", map[string]string{"log_level": "info"})

	r, c := setupFakeRemediationReconciler(issue, plan, cm)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "config-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Name: "app-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get configmap: %v", err)
	}
	if updated.Data["log_level"] != "debug" {
		t.Errorf("expected log_level=debug, got %q", updated.Data["log_level"])
	}
	if updated.Data["rate_limit"] != "1000" {
		t.Errorf("expected rate_limit=1000, got %q", updated.Data["rate_limit"])
	}
}

func TestRemediationReconcile_ExecutingMissingIssue(t *testing.T) {
	plan := newRemediationPlan("orphan-plan", "default")
	plan.Status.State = platformv1alpha1.RemediationStateExecuting
	now := metav1.Now()
	plan.Status.StartedAt = &now

	// No issue created — should fail with "Parent issue not found"
	r, c := setupFakeRemediationReconciler(plan)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "orphan-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "orphan-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updated.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Errorf("expected state Failed, got %q", updated.Status.State)
	}
	if updated.Status.Result != "Parent issue not found" {
		t.Errorf("expected 'Parent issue not found', got %q", updated.Status.Result)
	}
}

func TestRemediationReconcile_TerminalStateNoop(t *testing.T) {
	plan := newRemediationPlan("completed-plan", "default")
	plan.Status.State = platformv1alpha1.RemediationStateCompleted

	r, _ := setupFakeRemediationReconciler(plan)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "completed-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for terminal state")
	}
}

// --- Agentic remediation tests ---

// mockAgenticStepper implements AgenticStepCaller for tests.
type mockAgenticStepper struct {
	response *pb.AgenticStepResponse
	err      error
	called   bool
	request  *pb.AgenticStepRequest
}

func (m *mockAgenticStepper) AgenticStep(_ context.Context, req *pb.AgenticStepRequest) (*pb.AgenticStepResponse, error) {
	m.called = true
	m.request = req
	return m.response, m.err
}

func (m *mockAgenticStepper) IsConnected() bool { return true }

func setupAgenticReconciler(mock *mockAgenticStepper, objs ...client.Object) (*RemediationReconciler, client.Client) {
	s := newScheme()
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(
		&platformv1alpha1.RemediationPlan{},
		&platformv1alpha1.Issue{},
	)
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()
	return &RemediationReconciler{Client: c, Scheme: s, ServerClient: mock}, c
}

func newAgenticPlan(name, ns, issueName string) *platformv1alpha1.RemediationPlan {
	now := metav1.Now()
	return &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: "agentic-uid"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef:        platformv1alpha1.IssueRef{Name: issueName},
			Attempt:         1,
			Strategy:        "Agentic AI remediation",
			AgenticMode:     true,
			AgenticMaxSteps: 10,
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State:            platformv1alpha1.RemediationStateExecuting,
			StartedAt:        &now,
			AgenticStartedAt: &now,
		},
	}
}

func TestAgentic_FirstStep(t *testing.T) {
	issue := newIssue("agent-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}

	plan := newAgenticPlan("agent-plan", "default", "agent-issue")
	deploy := newDeployment("web", "default", 2)

	mock := &mockAgenticStepper{
		response: &pb.AgenticStepResponse{
			Reasoning: "Scaling up to handle load",
			Resolved:  false,
			NextAction: &pb.SuggestedAction{
				Name:   "Scale up",
				Action: "ScaleDeployment",
				Params: map[string]string{"replicas": "5"},
			},
		},
	}

	r, c := setupAgenticReconciler(mock, issue, plan, deploy)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "agent-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != 5*time.Second {
		t.Errorf("expected requeue after 5s, got %v", result.RequeueAfter)
	}
	if !mock.called {
		t.Fatal("expected AgenticStep to be called")
	}

	// Verify history was recorded
	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "agent-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if len(updated.Spec.AgenticHistory) != 1 {
		t.Fatalf("expected 1 step in history, got %d", len(updated.Spec.AgenticHistory))
	}
	step := updated.Spec.AgenticHistory[0]
	if step.StepNumber != 1 {
		t.Errorf("expected step number 1, got %d", step.StepNumber)
	}
	if step.AIMessage != "Scaling up to handle load" {
		t.Errorf("unexpected AI message: %q", step.AIMessage)
	}
	if step.Action == nil || step.Action.Type != platformv1alpha1.ActionScaleDeployment {
		t.Errorf("expected ScaleDeployment action, got %v", step.Action)
	}
	if !strings.Contains(step.Observation, "SUCCESS") {
		t.Errorf("expected SUCCESS observation, got %q", step.Observation)
	}

	// Verify deployment was scaled
	var updatedDeploy appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "web", Namespace: "default"}, &updatedDeploy); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 5 {
		t.Errorf("expected 5 replicas, got %d", *updatedDeploy.Spec.Replicas)
	}

	// Still in Executing state (not resolved yet)
	if updated.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Errorf("expected state Executing, got %q", updated.Status.State)
	}
	if updated.Status.AgenticStepCount != 1 {
		t.Errorf("expected AgenticStepCount 1, got %d", updated.Status.AgenticStepCount)
	}
}

func TestAgentic_Resolved(t *testing.T) {
	issue := newIssue("resolved-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}

	plan := newAgenticPlan("resolved-plan", "default", "resolved-issue")
	// Add previous step in history
	plan.Spec.AgenticHistory = []platformv1alpha1.AgenticStep{
		{
			StepNumber:  1,
			AIMessage:   "Scaling up",
			Observation: "SUCCESS: ScaleDeployment executed successfully",
			Timestamp:   metav1.Now(),
			Action: &platformv1alpha1.RemediationAction{
				Type:   platformv1alpha1.ActionScaleDeployment,
				Params: map[string]string{"replicas": "5"},
			},
		},
	}

	mock := &mockAgenticStepper{
		response: &pb.AgenticStepResponse{
			Reasoning:         "Issue resolved after scaling",
			Resolved:          true,
			PostmortemSummary: "High load caused OOM, scaled to 5 replicas",
			RootCause:         "Insufficient replicas under peak traffic",
			Impact:            "Service degradation for 5 minutes",
			LessonsLearned:    []string{"Set HPA for this deployment"},
			PreventionActions: []string{"Configure HPA with min 3 replicas"},
		},
	}

	r, c := setupAgenticReconciler(mock, issue, plan)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "resolved-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Errorf("expected requeue after 10s for verification, got %v", result.RequeueAfter)
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "resolved-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}

	// State should transition to Verifying
	if updated.Status.State != platformv1alpha1.RemediationStateVerifying {
		t.Errorf("expected state Verifying, got %q", updated.Status.State)
	}

	// History should have 2 steps (1 previous + 1 resolved)
	if len(updated.Spec.AgenticHistory) != 2 {
		t.Fatalf("expected 2 steps in history, got %d", len(updated.Spec.AgenticHistory))
	}

	// Annotations should contain postmortem data
	if updated.Annotations["platform.chatcli.io/postmortem-summary"] != "High load caused OOM, scaled to 5 replicas" {
		t.Errorf("unexpected postmortem summary: %q", updated.Annotations["platform.chatcli.io/postmortem-summary"])
	}
	if updated.Annotations["platform.chatcli.io/root-cause"] != "Insufficient replicas under peak traffic" {
		t.Errorf("unexpected root cause: %q", updated.Annotations["platform.chatcli.io/root-cause"])
	}
}

func TestAgentic_MaxSteps(t *testing.T) {
	issue := newIssue("maxstep-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}

	plan := newAgenticPlan("maxstep-plan", "default", "maxstep-issue")
	plan.Spec.AgenticMaxSteps = 2
	// Already 2 steps — next would be step 3, exceeding max
	plan.Spec.AgenticHistory = []platformv1alpha1.AgenticStep{
		{StepNumber: 1, AIMessage: "step 1", Observation: "ok", Timestamp: metav1.Now()},
		{StepNumber: 2, AIMessage: "step 2", Observation: "ok", Timestamp: metav1.Now()},
	}

	mock := &mockAgenticStepper{
		response: &pb.AgenticStepResponse{Reasoning: "should not be called"},
	}

	r, c := setupAgenticReconciler(mock, issue, plan)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "maxstep-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if mock.called {
		t.Error("AgenticStep should NOT have been called — max steps exceeded")
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "maxstep-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updated.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Errorf("expected Failed, got %q", updated.Status.State)
	}
	if !strings.Contains(updated.Status.Result, "max steps") {
		t.Errorf("expected 'max steps' in result, got %q", updated.Status.Result)
	}
}

func TestAgentic_Timeout(t *testing.T) {
	issue := newIssue("timeout-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}

	plan := newAgenticPlan("timeout-plan", "default", "timeout-issue")
	// Set AgenticStartedAt to 15 minutes ago
	pastTime := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	plan.Status.AgenticStartedAt = &pastTime

	mock := &mockAgenticStepper{
		response: &pb.AgenticStepResponse{Reasoning: "should not be called"},
	}

	r, c := setupAgenticReconciler(mock, issue, plan)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "timeout-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if mock.called {
		t.Error("AgenticStep should NOT have been called — timeout exceeded")
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "timeout-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updated.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Errorf("expected Failed, got %q", updated.Status.State)
	}
	if !strings.Contains(updated.Status.Result, "timed out") {
		t.Errorf("expected 'timed out' in result, got %q", updated.Status.Result)
	}
}

func TestAgentic_ActionFailed(t *testing.T) {
	issue := newIssue("fail-action-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "nonexistent", Namespace: "default"}

	plan := newAgenticPlan("fail-action-plan", "default", "fail-action-issue")
	// No deployment exists — action will fail

	mock := &mockAgenticStepper{
		response: &pb.AgenticStepResponse{
			Reasoning: "Scaling to fix",
			Resolved:  false,
			NextAction: &pb.SuggestedAction{
				Name:   "Scale",
				Action: "ScaleDeployment",
				Params: map[string]string{"replicas": "3"},
			},
		},
	}

	r, c := setupAgenticReconciler(mock, issue, plan)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "fail-action-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Should continue the loop, not fail permanently
	if result.RequeueAfter != 5*time.Second {
		t.Errorf("expected requeue after 5s, got %v", result.RequeueAfter)
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "fail-action-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}

	// Should still be Executing (loop continues)
	if updated.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Errorf("expected state Executing (loop continues), got %q", updated.Status.State)
	}

	// History should have the failed step with FAILED observation
	if len(updated.Spec.AgenticHistory) != 1 {
		t.Fatalf("expected 1 step in history, got %d", len(updated.Spec.AgenticHistory))
	}
	if !strings.Contains(updated.Spec.AgenticHistory[0].Observation, "FAILED") {
		t.Errorf("expected FAILED in observation, got %q", updated.Spec.AgenticHistory[0].Observation)
	}
}

func TestAgentic_ObservationStep(t *testing.T) {
	issue := newIssue("observe-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}

	plan := newAgenticPlan("observe-plan", "default", "observe-issue")

	// AI returns no action and not resolved — observation-only step
	mock := &mockAgenticStepper{
		response: &pb.AgenticStepResponse{
			Reasoning:  "Observing current state before acting",
			Resolved:   false,
			NextAction: nil,
		},
	}

	r, c := setupAgenticReconciler(mock, issue, plan)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "observe-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Errorf("expected requeue after 10s for observation, got %v", result.RequeueAfter)
	}

	var updated platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "observe-plan", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}

	if updated.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Errorf("expected Executing, got %q", updated.Status.State)
	}
	if len(updated.Spec.AgenticHistory) != 1 {
		t.Fatalf("expected 1 step in history, got %d", len(updated.Spec.AgenticHistory))
	}
	step := updated.Spec.AgenticHistory[0]
	if step.Action != nil {
		t.Error("expected no action for observation step")
	}
	if step.AIMessage != "Observing current state before acting" {
		t.Errorf("unexpected AI message: %q", step.AIMessage)
	}
	if !strings.Contains(step.Observation, "Observation step") {
		t.Errorf("expected 'Observation step' in observation, got %q", step.Observation)
	}
}
