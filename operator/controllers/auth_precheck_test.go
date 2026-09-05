package controllers

import (
	"context"
	"strings"
	"testing"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

func instanceWith(bind string, token *platformv1alpha1.SecretKeyRefSpec, jwt *platformv1alpha1.SecretKeyRefSpec, extra ...corev1.EnvVar) *platformv1alpha1.Instance {
	inst := &platformv1alpha1.Instance{}
	inst.Spec.ExtraEnv = extra
	inst.Spec.Server.Token = token
	if bind != "" || jwt != nil {
		inst.Spec.Server.Security = &platformv1alpha1.ServerSecuritySpec{BindAddress: bind, JWTSecretRef: jwt}
	}
	return inst
}

// No bindAddress in the spec means the pod inherits the in-cluster
// default, which is every interface — the case the crash loop came from.
func TestUnsetBindAddressCountsAsReachable(t *testing.T) {
	if got := instanceBindAddress(instanceWith("", nil, nil)); got != "0.0.0.0" {
		t.Errorf("instanceBindAddress = %q, want the in-cluster default", got)
	}
	if !instanceAuthUnconfigured(instanceWith("", nil, nil)) {
		t.Error("an instance with no bind and no credential must be flagged")
	}
}

func TestLoopbackBindNeedsNoCredential(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "::1", "localhost"} {
		if instanceAuthUnconfigured(instanceWith(addr, nil, nil)) {
			t.Errorf("%s binds only this pod and needs no credential", addr)
		}
	}
}

func TestEitherCredentialSatisfiesTheCheck(t *testing.T) {
	ref := &platformv1alpha1.SecretKeyRefSpec{Name: "s", Key: "k"}
	if instanceAuthUnconfigured(instanceWith("0.0.0.0", ref, nil)) {
		t.Error("a token reference must satisfy the check")
	}
	if instanceAuthUnconfigured(instanceWith("0.0.0.0", nil, ref)) {
		t.Error("a JWT secret reference must satisfy the check")
	}
}

// A credential supplied straight through extraEnv counts too — the
// operator must not demand its own field for something already provided.
func TestExtraEnvCredentialCounts(t *testing.T) {
	for _, name := range []string{"CHATCLI_SERVER_TOKEN", "CHATCLI_JWT_SECRET"} {
		inline := instanceWith("0.0.0.0", nil, nil, corev1.EnvVar{Name: name, Value: "x"})
		if instanceAuthUnconfigured(inline) {
			t.Errorf("%s given inline must satisfy the check", name)
		}
		fromSecret := instanceWith("0.0.0.0", nil, nil, corev1.EnvVar{
			Name:      name,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{}},
		})
		if instanceAuthUnconfigured(fromSecret) {
			t.Errorf("%s from a secret must satisfy the check", name)
		}
	}
	empty := instanceWith("0.0.0.0", nil, nil, corev1.EnvVar{Name: "CHATCLI_SERVER_TOKEN"})
	if !instanceAuthUnconfigured(empty) {
		t.Error("an empty value provisions nothing and must not satisfy the check")
	}
}

// The message is what a user sees on the Instance, so it has to name every
// way out.
func TestMessageNamesEveryRemedy(t *testing.T) {
	for _, want := range []string{"spec.server.token", "jwtSecretRef", "bindAddress"} {
		if !strings.Contains(authUnconfiguredMessage, want) {
			t.Errorf("the condition message should mention %s: %s", want, authUnconfiguredMessage)
		}
	}
}

func TestUnparseableBindIsReachable(t *testing.T) {
	if isLoopbackAddress("chatcli.internal") || isLoopbackAddress("") {
		t.Error("an unresolvable or empty address must not be assumed loopback")
	}
}

// The condition is what a user sees, so the reconcile has to raise it and
// stop rather than provisioning a Deployment that cannot start.
func TestReconcileRefusesToProvisionWithoutACredential(t *testing.T) {
	instance := newInstance("no-credential", "default")
	instance.Spec.Server.Token = nil // reachable bind, nothing to authenticate with
	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-credential", Namespace: "default"},
	}); err != nil {
		t.Fatalf("reconcile should stop cleanly, not error: %v", err)
	}

	var updated platformv1alpha1.Instance
	if err := c.Get(ctx, types.NamespacedName{Name: "no-credential", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, AuthConditionType)
	if cond == nil {
		t.Fatalf("want the %s condition on the instance, got %+v", AuthConditionType, updated.Status.Conditions)
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "CredentialMissing" {
		t.Errorf("condition = %+v, want False/CredentialMissing", cond)
	}

	var deploy appsv1.Deployment
	err := c.Get(ctx, types.NamespacedName{Name: "no-credential", Namespace: "default"}, &deploy)
	if err == nil {
		t.Error("a Deployment must not be provisioned for an instance that cannot start")
	}
}

// The happy path keeps working and records why.
func TestReconcileRecordsAConfiguredCredential(t *testing.T) {
	instance := newInstance("with-credential", "default")
	r, c := setupFakeReconciler(instance)
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "with-credential", Namespace: "default"},
	}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	var updated platformv1alpha1.Instance
	if err := c.Get(ctx, types.NamespacedName{Name: "with-credential", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if cond := meta.FindStatusCondition(updated.Status.Conditions, AuthConditionType); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("want a True %s condition, got %+v", AuthConditionType, cond)
	}
}
