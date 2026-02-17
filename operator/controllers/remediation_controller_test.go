package controllers

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
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

	// Verify plan completed
	var updatedPlan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "scale-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateCompleted {
		t.Errorf("expected state Completed, got %q", updatedPlan.Status.State)
	}
	if updatedPlan.Status.CompletedAt == nil {
		t.Error("expected completedAt to be set")
	}
	if len(updatedPlan.Status.Evidence) != 1 {
		t.Errorf("expected 1 evidence item, got %d", len(updatedPlan.Status.Evidence))
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

	// Verify plan completed
	var updatedPlan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "restart-plan", Namespace: "default"}, &updatedPlan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if updatedPlan.Status.State != platformv1alpha1.RemediationStateCompleted {
		t.Errorf("expected state Completed, got %q", updatedPlan.Status.State)
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

	r, c := setupFakeRemediationReconciler(issue, plan, deploy)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rollback-plan", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "api-server", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if _, ok := updated.Spec.Template.Annotations["platform.chatcli.io/rollback-at"]; !ok {
		t.Error("expected rollback-at annotation to be set")
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
