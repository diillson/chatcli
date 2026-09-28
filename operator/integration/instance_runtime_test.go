/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package integration

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// On a real API server: the pod template carries probes the API server
// accepts, a rotated token Secret reaches the Instance through the Secret
// watch and rolls the template, and a ready Instance settles instead of
// rewriting its status on every reconcile.
func TestInstanceProbesRotationAndSteadyStatus(t *testing.T) {
	ns := namespace(t, "it-instance-runtime")
	ctx := context.Background()
	token := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "server-token", Namespace: ns},
		StringData: map[string]string{"token": "integration-token-1"},
	}
	mustCreate(t, token)
	mustCreate(t, &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "chatcli", Namespace: ns},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI", Model: "claude-sonnet-5",
			Server: platformv1alpha1.ServerSpec{Token: &platformv1alpha1.SecretKeyRefSpec{Name: "server-token", Key: "token"}},
		},
	})

	var deploy appsv1.Deployment
	eventually(t, wait, "the Deployment", func() bool {
		return k8sClient.Get(ctx, key(ns, "chatcli"), &deploy) == nil
	})
	c := deploy.Spec.Template.Spec.Containers[0]
	if c.ReadinessProbe == nil || c.LivenessProbe == nil || c.StartupProbe == nil {
		t.Fatalf("probes missing: %+v", c)
	}
	if c.ReadinessProbe.HTTPGet == nil || c.ReadinessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("readiness probe = %+v", c.ReadinessProbe)
	}
	first := deploy.Spec.Template.Annotations["chatcli.io/credentials-hash"]
	if first == "" {
		t.Fatal("credentials hash missing")
	}

	// Rotate the token: only the Secret watch can bring this to the Instance.
	if err := k8sClient.Get(ctx, key(ns, "server-token"), token); err != nil {
		t.Fatal(err)
	}
	token.Data["token"] = []byte("integration-token-2")
	if err := k8sClient.Update(ctx, token); err != nil {
		t.Fatal(err)
	}
	eventually(t, wait, "the rotation to roll the pod template", func() bool {
		if err := k8sClient.Get(ctx, key(ns, "chatcli"), &deploy); err != nil {
			return false
		}
		return deploy.Spec.Template.Annotations["chatcli.io/credentials-hash"] != first
	})

	// Ready: the probe runs (and fails: there is no server behind the
	// Service in envtest), then the status must settle.
	markDeploymentHealthy(t, ns, "chatcli", 1)
	var inst platformv1alpha1.Instance
	eventually(t, wait, "the probe outcome", func() bool {
		if err := k8sClient.Get(ctx, key(ns, "chatcli"), &inst); err != nil {
			return false
		}
		return meta.FindStatusCondition(inst.Status.Conditions, "ServerReachable") != nil && inst.Status.Ready
	})
	settled := ""
	eventually(t, wait, "the status to settle", func() bool {
		if err := k8sClient.Get(ctx, key(ns, "chatcli"), &inst); err != nil {
			return false
		}
		rv := inst.ResourceVersion
		time.Sleep(12 * time.Second)
		if err := k8sClient.Get(ctx, key(ns, "chatcli"), &inst); err != nil {
			return false
		}
		settled = inst.ResourceVersion
		return rv == settled
	})
	if settled == "" {
		t.Fatal("no settled resource version")
	}
}

// tls.enabled without tls.secretName is refused with a condition instead
// of a crash-looping pod.
func TestInstanceTLSWithoutSecretIsNotProvisioned(t *testing.T) {
	ns := namespace(t, "it-instance-tls")
	ctx := context.Background()
	mustCreate(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "server-token", Namespace: ns},
		StringData: map[string]string{"token": "integration-token"},
	})
	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: ns},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Token: &platformv1alpha1.SecretKeyRefSpec{Name: "server-token", Key: "token"},
				TLS:   &platformv1alpha1.TLSSpec{Enabled: true},
			},
		},
	}
	mustCreate(t, inst)
	eventually(t, wait, "the TLSConfigured condition", func() bool {
		if err := k8sClient.Get(ctx, key(ns, "tls"), inst); err != nil {
			return false
		}
		c := meta.FindStatusCondition(inst.Status.Conditions, "TLSConfigured")
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "SecretNameMissing"
	})
	if err := k8sClient.Get(ctx, key(ns, "tls"), &appsv1.Deployment{}); err == nil {
		t.Error("no Deployment for TLS without a certificate Secret")
	}
}
