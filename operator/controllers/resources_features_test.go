/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildPodSpec_RendersFeaturesAsServerVariables(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}
	inst := newInstance("feat", "default")
	on, off := true, false
	inst.Spec.Features = &platformv1alpha1.FeaturesSpec{
		Memory:             &platformv1alpha1.MemoryFeatureSpec{Enabled: true, Mode: "pull"},
		Knowledge:          &off,
		Budget:             &platformv1alpha1.BudgetSpec{SessionUSD: "5", DailyUSD: "50", HardStop: true},
		Hub:                &on,
		CABundleSecretName: "corp-ca",
		AllowHTTPProviders: true,
		EncryptionKeyRef:   &platformv1alpha1.SecretKeyRefSpec{Name: "at-rest", Key: "key"},
		LogRotation:        &platformv1alpha1.LogRotationSpec{MaxSizeMB: 200, MaxBackups: 3, MaxAgeDays: 7, Compress: true},
	}
	spec := r.buildPodSpec(inst)
	env := envByName(spec.Containers[0].Env)
	want := map[string]string{
		"CHATCLI_MEMORY_ENABLED":       "true",
		"CHATCLI_MEMORY_MODE":          "pull",
		"CHATCLI_CHAT_KNOWLEDGE":       "false",
		"CHATCLI_SESSION_BUDGET_USD":   "5",
		"CHATCLI_DAILY_BUDGET_USD":     "50",
		"CHATCLI_BUDGET_HARD_STOP":     "true",
		"CHATCLI_HUB_ENABLED":          "true",
		"CHATCLI_CA_BUNDLE":            "/etc/chatcli/ca/ca.crt",
		"CHATCLI_ALLOW_HTTP_PROVIDERS": "true",
		"CHATCLI_LOG_MAX_SIZE_MB":      "200",
		"CHATCLI_LOG_MAX_BACKUPS":      "3",
		"CHATCLI_LOG_MAX_AGE_DAYS":     "7",
		"CHATCLI_LOG_COMPRESS":         "true",
	}
	for k, v := range want {
		if env[k].Value != v {
			t.Errorf("%s = %q, want %q", k, env[k].Value, v)
		}
	}
	key := env["CHATCLI_ENCRYPTION_KEY"]
	if key.ValueFrom == nil || key.ValueFrom.SecretKeyRef == nil || key.ValueFrom.SecretKeyRef.Name != "at-rest" || key.ValueFrom.SecretKeyRef.Key != "key" {
		t.Errorf("encryption key must come from the Secret: %+v", key)
	}
	mounted := false
	for _, v := range spec.Volumes {
		if v.Name == "ca-bundle" && v.Secret != nil && v.Secret.SecretName == "corp-ca" {
			mounted = true
		}
	}
	if !mounted {
		t.Error("CA bundle secret not mounted")
	}

	// Nothing set: nothing rendered, server defaults apply.
	plain := newInstance("plain", "default")
	env = envByName(r.buildPodSpec(plain).Containers[0].Env)
	for k := range want {
		if _, ok := env[k]; ok {
			t.Errorf("%s rendered without configuration", k)
		}
	}
	if _, ok := env["CHATCLI_ENCRYPTION_KEY"]; ok {
		t.Error("encryption key rendered without configuration")
	}
	if featureEnv(nil) != nil {
		t.Error("nil features render nothing")
	}
	// Memory off is rendered explicitly (it overrides the server default of on).
	offMem := newInstance("mem", "default")
	offMem.Spec.Features = &platformv1alpha1.FeaturesSpec{Memory: &platformv1alpha1.MemoryFeatureSpec{Enabled: false}}
	env = envByName(r.buildPodSpec(offMem).Containers[0].Env)
	if env["CHATCLI_MEMORY_ENABLED"].Value != "false" {
		t.Errorf("memory off must render false, got %+v", env["CHATCLI_MEMORY_ENABLED"])
	}
	if _, ok := env["CHATCLI_MEMORY_MODE"]; ok {
		t.Error("empty mode is not rendered")
	}
}

func TestBuildPodSpec_AppliesSchedulingAndPodAnnotations(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}
	inst := newInstance("sched", "default")
	inst.Spec.Scheduling = &platformv1alpha1.SchedulingSpec{
		NodeSelector:      map[string]string{"pool": "ai"},
		Tolerations:       []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
		Affinity:          &corev1.Affinity{},
		ImagePullSecrets:  []corev1.LocalObjectReference{{Name: "ghcr"}},
		PriorityClassName: "high",
		PodAnnotations:    map[string]string{"sidecar.istio.io/inject": "false", "chatcli.io/tls-hash": "user-must-not-win"},
	}
	inst.Spec.Server.TLS = &platformv1alpha1.TLSSpec{Enabled: true, SecretName: "srv-tls"}
	spec := r.buildPodSpec(inst)
	if spec.NodeSelector["pool"] != "ai" || len(spec.Tolerations) != 1 || spec.Affinity == nil || len(spec.ImagePullSecrets) != 1 || spec.PriorityClassName != "high" {
		t.Errorf("scheduling not applied: %+v", spec)
	}
	plain := r.buildPodSpec(newInstance("plain", "default"))
	if plain.NodeSelector != nil || plain.Tolerations != nil || plain.PriorityClassName != "" {
		t.Error("no scheduling spec: pod spec untouched")
	}

	// Pod annotations: user keys land, operator hashes win on a clash.
	tlsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "srv-tls", Namespace: "default"}, Data: map[string][]byte{"tls.crt": []byte("c"), "tls.key": []byte("k")}}
	c := fakeClientWith(inst, tlsSecret)
	rr := &InstanceReconciler{Client: c, Scheme: newScheme()}
	if err := rr.reconcileDeployment(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	var deploy appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: "sched", Namespace: "default"}, &deploy); err != nil {
		t.Fatal(err)
	}
	ann := deploy.Spec.Template.Annotations
	if ann["sidecar.istio.io/inject"] != "false" {
		t.Errorf("user annotation missing: %+v", ann)
	}
	if ann["chatcli.io/tls-hash"] == "user-must-not-win" {
		t.Error("operator rollout hash must win over a user annotation with the same key")
	}
}

func TestReconcileServiceAccount_MergesAnnotationsAndKeepsForeignOnes(t *testing.T) {
	inst := newInstance("irsa", "default")
	inst.Spec.ServiceAccount = &platformv1alpha1.ServiceAccountSpec{Annotations: map[string]string{"eks.amazonaws.com/role-arn": "arn:aws:iam::123456789012:role/chatcli"}}
	existing := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "irsa", Namespace: "default", Annotations: map[string]string{"mesh.example/inject": "yes"}},
	}
	c := fakeClientWith(inst, existing)
	r := &InstanceReconciler{Client: c, Scheme: newScheme()}
	if err := r.reconcileServiceAccount(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	var got corev1.ServiceAccount
	if err := c.Get(context.Background(), types.NamespacedName{Name: "irsa", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations["eks.amazonaws.com/role-arn"] != "arn:aws:iam::123456789012:role/chatcli" {
		t.Errorf("declared annotation missing: %+v", got.Annotations)
	}
	if got.Annotations["mesh.example/inject"] != "yes" {
		t.Errorf("foreign annotation must survive: %+v", got.Annotations)
	}
	if got.Labels["app.kubernetes.io/name"] == "" {
		t.Error("labels still managed")
	}
}
