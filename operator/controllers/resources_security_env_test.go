/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"testing"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func envByName(env []corev1.EnvVar) map[string]corev1.EnvVar {
	m := map[string]corev1.EnvVar{}
	for _, e := range env {
		m[e.Name] = e
	}
	return m
}

func TestBuildPodSpec_RendersRS256IssuerAndAudience(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}
	instance := newInstance("sec", "default")
	instance.Spec.Server.Security = &platformv1alpha1.ServerSecuritySpec{
		JWTPublicKeyRef: &platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-jwt", Key: "public.pem"},
		JWTIssuer:       "chatcli-issuer",
		JWTAudience:     "chatcli-servers",
		JWTSecretRef:    &platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-jwt", Key: "secret"},
	}

	spec := r.buildPodSpec(instance)
	if len(spec.Containers) == 0 {
		t.Fatal("no container rendered")
	}
	env := envByName(spec.Containers[0].Env)

	pub, ok := env["CHATCLI_JWT_PUBLIC_KEY"]
	if !ok || pub.ValueFrom == nil || pub.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("CHATCLI_JWT_PUBLIC_KEY must come from the referenced Secret, got %+v", pub)
	}
	if pub.ValueFrom.SecretKeyRef.Name != "chatcli-jwt" || pub.ValueFrom.SecretKeyRef.Key != "public.pem" {
		t.Errorf("wrong secret ref: %+v", pub.ValueFrom.SecretKeyRef)
	}
	if env["CHATCLI_JWT_ISSUER"].Value != "chatcli-issuer" {
		t.Errorf("issuer not rendered: %+v", env["CHATCLI_JWT_ISSUER"])
	}
	if env["CHATCLI_JWT_AUDIENCE"].Value != "chatcli-servers" {
		t.Errorf("audience not rendered: %+v", env["CHATCLI_JWT_AUDIENCE"])
	}
	if sec, ok := env["CHATCLI_JWT_SECRET"]; !ok || sec.ValueFrom == nil {
		t.Error("jwtSecretRef still renders CHATCLI_JWT_SECRET")
	}

	// Nothing configured: none of the JWT variables appear.
	plain := newInstance("plain", "default")
	env = envByName(r.buildPodSpec(plain).Containers[0].Env)
	for _, name := range []string{"CHATCLI_JWT_PUBLIC_KEY", "CHATCLI_JWT_ISSUER", "CHATCLI_JWT_AUDIENCE", "CHATCLI_JWT_SECRET"} {
		if _, ok := env[name]; ok {
			t.Errorf("%s rendered without configuration", name)
		}
	}
}
