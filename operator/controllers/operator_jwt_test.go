/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func jwtPayload(t *testing.T, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWT: %q", tok)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestJWTMinter_MintsOperatorClaimsAndRenewsAtThreeQuarters(t *testing.T) {
	m, err := newJWTMinter([]byte("hs256-secret"), "chatcli-issuer", "chatcli-servers")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	first, err := m.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claims := jwtPayload(t, first)
	if claims["sub"] != operatorJWTSubject || claims["role"] != operatorJWTRole {
		t.Errorf("identity claims wrong: %v", claims)
	}
	if claims["iss"] != "chatcli-issuer" || claims["aud"] != "chatcli-servers" {
		t.Errorf("issuer/audience not stamped: %v", claims)
	}
	if claims["exp"] != float64(now.Add(time.Hour).Unix()) {
		t.Errorf("exp = %v, want one hour", claims["exp"])
	}

	now = now.Add(30 * time.Minute)
	again, _ := m.Token(context.Background())
	if again != first {
		t.Error("token must be cached until its renewal point")
	}
	now = now.Add(20 * time.Minute) // 50 min: past 75% of one hour
	renewed, _ := m.Token(context.Background())
	if renewed == first {
		t.Error("token must renew before it expires")
	}
	if jwtPayload(t, renewed)["exp"] != float64(now.Add(time.Hour).Unix()) {
		t.Error("renewed token carries a fresh expiry")
	}
}

func TestJWTMinter_RefusesRSAMaterialAndEmptySecret(t *testing.T) {
	if _, err := newJWTMinter(nil, "", ""); err == nil {
		t.Error("empty secret must be refused")
	}
	pem := "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA\n-----END PUBLIC KEY-----\n"
	_, err := newJWTMinter([]byte(pem), "", "")
	if err == nil || !strings.Contains(err.Error(), "operatorTokenRef") {
		t.Errorf("RSA material must be refused with the fix named, got %v", err)
	}
}

func TestServerClientWithAuth_PrefersTheTokenSource(t *testing.T) {
	sc := NewServerClient(zap.NewNop())
	sc.token = "static"
	ctx := sc.withAuth(context.Background())
	md, _ := metadata.FromOutgoingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer static" {
		t.Errorf("static token expected, got %v", got)
	}

	sc.source = staticTokenSource("minted")
	md, _ = metadata.FromOutgoingContext(sc.withAuth(context.Background()))
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer minted" {
		t.Errorf("source must win, got %v", got)
	}

	sc.source = failingSource{}
	if _, ok := metadata.FromOutgoingContext(sc.withAuth(context.Background())); ok {
		t.Error("a failing source sends no credential rather than a stale one")
	}
}

func instanceWithSecurity(sec *platformv1alpha1.ServerSecuritySpec, token *platformv1alpha1.SecretKeyRefSpec) *platformv1alpha1.Instance {
	return &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "chatcli", Namespace: "default", UID: types.UID("inst-sec")},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "OPENAI",
			Server:   platformv1alpha1.ServerSpec{Port: 50051, Token: token, Security: sec},
		},
	}
}

func TestBuildConnectionOpts_MintsHS256FromJWTSecretRef(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "chatcli-jwt", Namespace: "default"},
		Data:       map[string][]byte{"secret": []byte("shared-hs256")},
	}
	inst := instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{
		JWTSecretRef: &platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-jwt", Key: "secret"},
		JWTIssuer:    "chatcli-operator-issuer",
	}, nil)
	wb := setupFakeWatcherBridge(inst, secret)

	opts, err := wb.buildConnectionOpts(context.Background(), inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Token != "" || opts.TokenSource == nil {
		t.Fatalf("a JWT secret yields a token source, got token=%q source=%v", opts.Token, opts.TokenSource != nil)
	}
	tok, err := opts.TokenSource.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claims := jwtPayload(t, tok)
	if claims["sub"] != operatorJWTSubject || claims["iss"] != "chatcli-operator-issuer" {
		t.Errorf("minted claims wrong: %v", claims)
	}
}

func TestBuildConnectionOpts_RS256MaterialNeedsOperatorToken(t *testing.T) {
	pem := "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA\n-----END PUBLIC KEY-----\n"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "chatcli-jwt", Namespace: "default"},
		Data:       map[string][]byte{"secret": []byte(pem)},
	}
	inst := instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{
		JWTSecretRef: &platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-jwt", Key: "secret"},
	}, nil)
	wb := setupFakeWatcherBridge(inst, secret)
	if _, err := wb.buildConnectionOpts(context.Background(), inst); err == nil || !strings.Contains(err.Error(), "operatorTokenRef") {
		t.Errorf("RSA material must fail with the fix named, got %v", err)
	}

	pub := instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{
		JWTPublicKeyRef: &platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-jwt", Key: "secret"},
	}, nil)
	wb = setupFakeWatcherBridge(pub, secret)
	if _, err := wb.buildConnectionOpts(context.Background(), pub); err == nil || !strings.Contains(err.Error(), "operatorTokenRef") {
		t.Errorf("public-key-only must fail with the fix named, got %v", err)
	}
}

func TestBuildConnectionOpts_OperatorTokenRefAndPrecedence(t *testing.T) {
	secrets := []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "op-cred", Namespace: "default"}, Data: map[string][]byte{"token": []byte("issued-jwt")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "chatcli-jwt", Namespace: "default"}, Data: map[string][]byte{"secret": []byte("hs")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "chatcli-auth", Namespace: "default"}, Data: map[string][]byte{"token": []byte("shared")}},
	}
	sec := &platformv1alpha1.ServerSecuritySpec{
		OperatorTokenRef: &platformv1alpha1.SecretKeyRefSpec{Name: "op-cred"},
		JWTSecretRef:     &platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-jwt", Key: "secret"},
	}
	inst := instanceWithSecurity(sec, nil)
	wb := setupFakeWatcherBridge(inst, secrets[0], secrets[1], secrets[2])
	opts, err := wb.buildConnectionOpts(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	if opts.TokenSource == nil {
		t.Fatal("operatorTokenRef yields a source")
	}
	if tok, _ := opts.TokenSource.Token(context.Background()); tok != "issued-jwt" {
		t.Errorf("operatorTokenRef wins over jwtSecretRef, got %q", tok)
	}

	withToken := instanceWithSecurity(sec, &platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-auth", Key: "token"})
	wb = setupFakeWatcherBridge(withToken, secrets[0], secrets[1], secrets[2])
	opts, err = wb.buildConnectionOpts(context.Background(), withToken)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Token != "shared" || opts.TokenSource != nil {
		t.Errorf("spec.server.token keeps precedence, got token=%q source=%v", opts.Token, opts.TokenSource != nil)
	}

	missing := instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{
		OperatorTokenRef: &platformv1alpha1.SecretKeyRefSpec{Name: "absent"},
	}, nil)
	wb = setupFakeWatcherBridge(missing)
	if _, err := wb.buildConnectionOpts(context.Background(), missing); err == nil {
		t.Error("a missing operator credential secret is an error, not a silent unauthenticated connection")
	}
}

func TestOperatorCredentialCondition(t *testing.T) {
	cases := []struct {
		name string
		inst *platformv1alpha1.Instance
		ok   bool
	}{
		{"shared token", instanceWithSecurity(nil, &platformv1alpha1.SecretKeyRefSpec{Name: "a", Key: "token"}), true},
		{"operator token", instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{OperatorTokenRef: &platformv1alpha1.SecretKeyRefSpec{Name: "a"}}, nil), true},
		{"jwt secret", instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{JWTSecretRef: &platformv1alpha1.SecretKeyRefSpec{Name: "a", Key: "secret"}}, nil), true},
		{"nothing (loopback or blocked)", instanceWithSecurity(nil, nil), true},
		{"rs256 only", instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{JWTPublicKeyRef: &platformv1alpha1.SecretKeyRefSpec{Name: "a", Key: "pub"}}, nil), false},
	}
	for _, c := range cases {
		recordOperatorCredentialCondition(c.inst)
		var found *metav1.Condition
		for i := range c.inst.Status.Conditions {
			if c.inst.Status.Conditions[i].Type == OperatorCredentialConditionType {
				found = &c.inst.Status.Conditions[i]
			}
		}
		if found == nil {
			t.Fatalf("%s: condition missing", c.name)
		}
		if (found.Status == metav1.ConditionTrue) != c.ok {
			t.Errorf("%s: status=%s want ok=%v (%s)", c.name, found.Status, c.ok, found.Message)
		}
	}

	extra := instanceWithSecurity(nil, nil)
	extra.Spec.ExtraEnv = []corev1.EnvVar{{Name: "CHATCLI_JWT_PUBLIC_KEY", Value: "/k.pub"}}
	if !instanceHasCredential(extra) {
		t.Error("a public key through extraEnv secures the server")
	}
	if _, ok := operatorCredentialFor(extra); ok {
		t.Error("but the operator cannot present a credential for it")
	}
	ca := instanceWithSecurity(nil, nil)
	ca.Spec.ExtraEnv = []corev1.EnvVar{{Name: "CHATCLI_SERVER_TLS_CLIENT_CA", Value: "/ca.pem"}}
	if !instanceHasCredential(ca) {
		t.Error("a client CA secures the server")
	}
}

// failingSource is a TokenSource that never yields a credential.
type failingSource struct{}

func (failingSource) Token(context.Context) (string, error) { return "", context.DeadlineExceeded }
