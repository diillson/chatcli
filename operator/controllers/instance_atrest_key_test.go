/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"encoding/base64"
	stderrors "errors"
	"testing"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func getAtrestSecret(t *testing.T, r *InstanceReconciler, inst *platformv1alpha1.Instance) corev1.Secret {
	t.Helper()
	var sec corev1.Secret
	if err := r.Get(context.Background(), types.NamespacedName{Name: inst.Name + "-atrest-key", Namespace: inst.Namespace}, &sec); err != nil {
		t.Fatalf("key Secret: %v", err)
	}
	return sec
}

// The key Secret is created once, without an owner reference, and never
// overwritten by later reconciles.
func TestReconcileAtrestKey_CreatesSecretOnceAndNeverOverwrites(t *testing.T) {
	inst := newInstance("enc", "default")
	r := &InstanceReconciler{Client: fakeClientWith(inst), Scheme: newScheme()}
	ctx := context.Background()

	if err := r.reconcileAtrestKey(ctx, inst); err != nil {
		t.Fatal(err)
	}
	sec := getAtrestSecret(t, r, inst)
	first := string(sec.Data["encryption-key"])
	raw, err := base64.StdEncoding.DecodeString(first)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key must be 32 random bytes in base64, got %d bytes (err %v)", len(raw), err)
	}
	if len(sec.OwnerReferences) != 0 {
		t.Errorf("the key Secret must outlive the Instance, got owner references %v", sec.OwnerReferences)
	}

	if err := r.reconcileAtrestKey(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if got := string(getAtrestSecret(t, r, inst).Data["encryption-key"]); got != first {
		t.Fatal("a second reconcile must not replace the key")
	}
}

// A pre-existing Secret, even one the operator did not create, is left as is.
func TestReconcileAtrestKey_LeavesExistingSecretAlone(t *testing.T) {
	inst := newInstance("pre", "default")
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pre-atrest-key", Namespace: "default"},
		Data:       map[string][]byte{"encryption-key": []byte("dummy-preexisting-key")},
	}
	r := &InstanceReconciler{Client: fakeClientWith(inst, existing), Scheme: newScheme()}
	if err := r.reconcileAtrestKey(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	sec := getAtrestSecret(t, r, inst)
	if string(sec.Data["encryption-key"]) != "dummy-preexisting-key" || len(sec.OwnerReferences) != 0 {
		t.Fatalf("existing Secret modified: %+v", sec)
	}
}

// The server reads its key with a Role restricted to that one Secret.
func TestReconcileAtrestKey_RoleRestrictedByResourceName(t *testing.T) {
	inst := newInstance("rbac", "default")
	r := &InstanceReconciler{Client: fakeClientWith(inst), Scheme: newScheme()}
	if err := r.reconcileAtrestKey(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	var role rbacv1.Role
	if err := r.Get(context.Background(), types.NamespacedName{Name: "rbac-atrest-key", Namespace: "default"}, &role); err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 {
		t.Fatalf("want one rule, got %+v", role.Rules)
	}
	rule := role.Rules[0]
	if len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != "rbac-atrest-key" {
		t.Errorf("rule must be restricted to the key Secret: %+v", rule)
	}
	if len(rule.Verbs) != 1 || rule.Verbs[0] != "get" || len(rule.Resources) != 1 || rule.Resources[0] != "secrets" {
		t.Errorf("rule must grant only get on secrets: %+v", rule)
	}
	var rb rbacv1.RoleBinding
	if err := r.Get(context.Background(), types.NamespacedName{Name: "rbac-atrest-key", Namespace: "default"}, &rb); err != nil {
		t.Fatal(err)
	}
	if rb.RoleRef.Name != "rbac-atrest-key" || len(rb.Subjects) != 1 || rb.Subjects[0].Name != "rbac" || rb.Subjects[0].Kind != "ServiceAccount" {
		t.Errorf("binding must give the Instance ServiceAccount the key Role: %+v", rb)
	}
}

// The server container is pointed at the Secret and gets its token.
func TestBuildPodSpec_InjectsAtrestKeySecret(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}
	inst := newInstance("env", "default")
	spec := r.buildPodSpec(inst)
	env := envByName(spec.Containers[0].Env)
	if env["CHATCLI_ENCRYPTION_KEY_SECRET"].Value != "env-atrest-key" {
		t.Errorf("CHATCLI_ENCRYPTION_KEY_SECRET = %q", env["CHATCLI_ENCRYPTION_KEY_SECRET"].Value)
	}
	if _, ok := env["CHATCLI_ENCRYPTION_KEY"]; ok {
		t.Error("the operator-managed mode must not set CHATCLI_ENCRYPTION_KEY itself")
	}
	if spec.AutomountServiceAccountToken == nil || !*spec.AutomountServiceAccountToken {
		t.Error("the service account token must be mounted to read the key Secret")
	}
}

// Every other key source, and the explicit opt-out, skips the managed key:
// no env, no Secret, no Role.
func TestReconcileAtrestKey_SkippedWhenKeyComesFromElsewhere(t *testing.T) {
	off := false
	cases := map[string]func(*platformv1alpha1.Instance){
		"encryptionKeyRef": func(i *platformv1alpha1.Instance) {
			i.Spec.Features = &platformv1alpha1.FeaturesSpec{EncryptionKeyRef: &platformv1alpha1.SecretKeyRefSpec{Name: "mine", Key: "key"}}
		},
		"encryptionAtRest=false": func(i *platformv1alpha1.Instance) {
			i.Spec.Features = &platformv1alpha1.FeaturesSpec{EncryptionAtRest: &off}
		},
		"extraEnv key": func(i *platformv1alpha1.Instance) {
			i.Spec.ExtraEnv = []corev1.EnvVar{{Name: "CHATCLI_ENCRYPTION_KEY", Value: "dummy"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			inst := newInstance("skip", "default")
			mutate(inst)
			r := &InstanceReconciler{Client: fakeClientWith(inst), Scheme: newScheme()}
			if err := r.reconcileAtrestKey(context.Background(), inst); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(context.Background(), types.NamespacedName{Name: "skip-atrest-key", Namespace: "default"}, &corev1.Secret{}); !errors.IsNotFound(err) {
				t.Errorf("no key Secret expected, got %v", err)
			}
			if err := r.Get(context.Background(), types.NamespacedName{Name: "skip-atrest-key", Namespace: "default"}, &rbacv1.Role{}); !errors.IsNotFound(err) {
				t.Errorf("no key Role expected, got %v", err)
			}
			env := envByName(r.buildPodSpec(inst).Containers[0].Env)
			if _, ok := env["CHATCLI_ENCRYPTION_KEY_SECRET"]; ok {
				t.Error("CHATCLI_ENCRYPTION_KEY_SECRET must not be set")
			}
		})
	}
}

// Turning the managed key off removes its Role and RoleBinding but keeps
// the Secret, which may be the only way to read data sealed with it.
func TestReconcileAtrestKey_OptOutRemovesRBACKeepsSecret(t *testing.T) {
	inst := newInstance("later", "default")
	r := &InstanceReconciler{Client: fakeClientWith(inst), Scheme: newScheme()}
	ctx := context.Background()
	if err := r.reconcileAtrestKey(ctx, inst); err != nil {
		t.Fatal(err)
	}
	off := false
	inst.Spec.Features = &platformv1alpha1.FeaturesSpec{EncryptionAtRest: &off}
	if err := r.reconcileAtrestKey(ctx, inst); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Name: "later-atrest-key", Namespace: "default"}
	if err := r.Get(ctx, key, &rbacv1.Role{}); !errors.IsNotFound(err) {
		t.Errorf("Role must be removed, got %v", err)
	}
	if err := r.Get(ctx, key, &rbacv1.RoleBinding{}); !errors.IsNotFound(err) {
		t.Errorf("RoleBinding must be removed, got %v", err)
	}
	getAtrestSecret(t, r, inst)
}

// A Role of the same name the Instance does not control is never deleted.
func TestReconcileAtrestKey_OptOutKeepsForeignRole(t *testing.T) {
	off := false
	inst := newInstance("own", "default")
	inst.Spec.Features = &platformv1alpha1.FeaturesSpec{EncryptionAtRest: &off}
	foreign := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "own-atrest-key", Namespace: "default"}}
	r := &InstanceReconciler{Client: fakeClientWith(inst, foreign), Scheme: newScheme()}
	if err := r.reconcileAtrestKey(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "own-atrest-key", Namespace: "default"}, &rbacv1.Role{}); err != nil {
		t.Errorf("foreign Role must survive: %v", err)
	}
}

// A key Secret the operator created and that later disappeared is a lost
// key: no new one is created (it could not open the data sealed with the
// old one) until the user restores the Secret or removes the annotation.
func TestReconcileAtrestKey_DeletedSecretIsNotRecreated(t *testing.T) {
	inst := newInstance("lost", "default")
	r := &InstanceReconciler{Client: fakeClientWith(inst), Scheme: newScheme()}
	ctx := context.Background()
	if err := r.reconcileAtrestKey(ctx, inst); err != nil {
		t.Fatal(err)
	}
	var fresh platformv1alpha1.Instance
	if err := r.Get(ctx, types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Annotations[atrestKeyCreatedAnnotation] == "" {
		t.Fatal("the Instance does not record that its key Secret was created")
	}

	sec := getAtrestSecret(t, r, inst)
	if err := r.Delete(ctx, &sec); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileAtrestKey(ctx, &fresh); !stderrors.Is(err, errAtrestKeyLost) {
		t.Fatalf("err = %v, want errAtrestKeyLost", err)
	}
	var gone corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: sec.Name, Namespace: sec.Namespace}, &gone); !errors.IsNotFound(err) {
		t.Fatal("a new key Secret was created after the original was deleted")
	}

	// Starting over is an explicit choice: removing the annotation.
	delete(fresh.Annotations, atrestKeyCreatedAnnotation)
	if err := r.Update(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileAtrestKey(ctx, &fresh); err != nil {
		t.Fatalf("after removing the annotation: %v", err)
	}
	getAtrestSecret(t, r, inst)
}

// A key Secret created by 1.221.0/1.221.1 carries an owner reference to the
// Instance; the reconcile removes it (keeping any other owner and the key),
// so deleting the Instance no longer deletes the key.
func TestReconcileAtrestKey_ReleasesSecretOwnedByTheInstance(t *testing.T) {
	inst := newInstance("legacy", "default")
	inst.UID = "legacy-uid"
	other := metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other-uid"}
	isController := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "legacy-atrest-key",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "Instance", Name: inst.Name, UID: inst.UID, Controller: &isController},
				other,
			},
		},
		Data: map[string][]byte{"encryption-key": []byte("sealed-data-key")},
	}
	r := &InstanceReconciler{Client: fakeClientWith(inst, secret), Scheme: newScheme()}

	for i := 0; i < 2; i++ {
		if err := r.reconcileAtrestKey(context.Background(), inst); err != nil {
			t.Fatal(err)
		}
	}
	sec := getAtrestSecret(t, r, inst)
	if string(sec.Data["encryption-key"]) != "sealed-data-key" {
		t.Fatal("releasing the Secret must not touch the key")
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != other.UID {
		t.Fatalf("owner references = %v, want only the unrelated owner", sec.OwnerReferences)
	}
}
