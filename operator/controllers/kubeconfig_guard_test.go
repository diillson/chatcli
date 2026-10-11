/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// kubeconfigWith renders a kubeconfig whose only cluster and user carry the
// given extra lines. The token and server are synthetic.
func kubeconfigWith(clusterExtra, userExtra string) []byte {
	return []byte(`apiVersion: v1
kind: Config
current-context: remote
clusters:
- name: remote
  cluster:
    server: https://remote.example.invalid:6443
` + clusterExtra + `
contexts:
- name: remote
  context:
    cluster: remote
    user: remote
users:
- name: remote
  user:
    token: synthetic-token
` + userExtra + `
`)
}

func TestKubeconfigGuard_AcceptsInlineCredentials(t *testing.T) {
	cfg, err := restConfigFromUntrustedKubeconfig(kubeconfigWith("", ""))
	if err != nil {
		t.Fatalf("inline token kubeconfig refused: %v", err)
	}
	if cfg.BearerToken != "synthetic-token" || cfg.Host != "https://remote.example.invalid:6443" {
		t.Fatalf("config = host %q token set %v", cfg.Host, cfg.BearerToken != "")
	}
}

// Every form that would run a command, read a local file, skip TLS
// verification or send the credential in clear is refused.
func TestKubeconfigGuard_RefusesForbiddenForms(t *testing.T) {
	cases := map[string]struct{ cluster, user, want string }{
		"exec": {user: `    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: /bin/sh
      args: ["-c", "true"]`, want: "exec"},
		"auth-provider": {user: `    auth-provider:
      name: oidc
      config: {}`, want: "auth-provider"},
		"tokenFile":                  {user: `    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token`, want: "tokenFile"},
		"client-certificate path":    {user: `    client-certificate: /etc/ssl/client.crt`, want: "client-certificate"},
		"client-key path":            {user: `    client-key: /etc/ssl/client.key`, want: "client-key"},
		"certificate-authority path": {cluster: `    certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt`, want: "certificate-authority"},
		"insecure-skip-tls-verify":   {cluster: `    insecure-skip-tls-verify: true`, want: "insecure-skip-tls-verify"},
		"proxy-url":                  {cluster: `    proxy-url: http://proxy.example.invalid:3128`, want: "proxy-url"},
		"basic auth": {user: `    username: admin
    password: synthetic`, want: "basic authentication"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := restConfigFromUntrustedKubeconfig(kubeconfigWith(tc.cluster, tc.user))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

func TestKubeconfigGuard_RefusesPlainHTTPAndUnusedForbiddenEntries(t *testing.T) {
	plain := []byte(strings.Replace(string(kubeconfigWith("", "")), "https://", "http://", 1))
	if _, err := restConfigFromUntrustedKubeconfig(plain); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http server: err = %v", err)
	}
	// A forbidden user outside the current context is refused too: the
	// whole file must be clean.
	extra := string(kubeconfigWith("", "")) + `- name: other
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: /bin/sh
`
	if _, err := restConfigFromUntrustedKubeconfig([]byte(extra)); err == nil || !strings.Contains(err.Error(), `user "other"`) {
		t.Fatalf("unused exec user: err = %v", err)
	}
}

func kubeconfigSecret(ns string, annotations map[string]string, data []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "east-kubeconfig", Namespace: ns, Annotations: annotations},
		Data:       map[string][]byte{"kubeconfig": data},
	}
}

// The kubeconfig Secret is read from the operator's namespace only, must be
// bound to the registration, and the client cache never mixes two
// registrations that share a name across namespaces.
func TestFederation_KubeconfigSecretTrustBoundary(t *testing.T) {
	cr := &platformv1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "east", Namespace: "tenant-a"},
		Spec:       platformv1alpha1.ClusterRegistrationSpec{KubeconfigSecretRef: platformv1alpha1.SecretRefSpec{Name: "east-kubeconfig"}},
	}
	bound := map[string]string{AnnotationClusterRegistrationBinding: "tenant-a/east"}
	ctx := context.Background()

	// A Secret in the registration's own namespace is not read.
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(kubeconfigSecret("tenant-a", bound, kubeconfigWith("", ""))).Build()
	r := &FederationReconciler{Client: c, Scheme: newScheme(), OperatorNamespace: "chatcli-system"}
	if _, err := r.getOrCreateRemoteClient(ctx, cr); err == nil {
		t.Fatal("a kubeconfig Secret outside the operator namespace was used")
	}

	// Without an operator namespace nothing is read.
	r = &FederationReconciler{Client: c, Scheme: newScheme()}
	if _, err := r.getOrCreateRemoteClient(ctx, cr); err == nil {
		t.Fatal("connected without knowing the operator namespace")
	}

	// In the operator namespace, but not bound to this registration.
	c = fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		kubeconfigSecret("chatcli-system", map[string]string{AnnotationClusterRegistrationBinding: "ops/east"}, kubeconfigWith("", ""))).Build()
	r = &FederationReconciler{Client: c, Scheme: newScheme(), OperatorNamespace: "chatcli-system"}
	if _, err := r.getOrCreateRemoteClient(ctx, cr); err == nil || !strings.Contains(err.Error(), AnnotationClusterRegistrationBinding) {
		t.Fatalf("unbound Secret: err = %v", err)
	}

	// Bound, but carrying a forbidden form.
	c = fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		kubeconfigSecret("chatcli-system", bound, kubeconfigWith("", "    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token"))).Build()
	r = &FederationReconciler{Client: c, Scheme: newScheme(), OperatorNamespace: "chatcli-system"}
	if _, err := r.getOrCreateRemoteClient(ctx, cr); err == nil || !strings.Contains(err.Error(), "tokenFile") {
		t.Fatalf("tokenFile kubeconfig: err = %v", err)
	}

	// Bound and clean: the client is built and cached under namespace/name.
	c = fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(kubeconfigSecret("chatcli-system", bound, kubeconfigWith("", ""))).Build()
	r = &FederationReconciler{Client: c, Scheme: newScheme(), OperatorNamespace: "chatcli-system"}
	if _, err := r.getOrCreateRemoteClient(ctx, cr); err != nil {
		t.Fatalf("bound inline kubeconfig refused: %v", err)
	}
	if _, ok := r.remoteClients.Load("tenant-a/east"); !ok {
		t.Fatal("client not cached under namespace/name")
	}
	twin := cr.DeepCopy()
	twin.Namespace = "tenant-b"
	if _, err := r.getOrCreateRemoteClient(ctx, twin); err == nil {
		t.Fatal("a same-named registration in another namespace reused the cached client")
	}
}
