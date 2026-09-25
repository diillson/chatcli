/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// selfSignedPEM returns a throwaway certificate and key in PEM.
func selfSignedPEM(t *testing.T) (cert, key []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "chatcli-operator"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func fakeClientWith(objs ...client.Object) client.Client {
	cb := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&platformv1alpha1.Instance{})
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	return cb.Build()
}

func TestInstanceAddress(t *testing.T) {
	inst := newInstance("api", "prod")
	if got := instanceAddress(inst); got != "dns:///api.prod.svc.cluster.local:50051" {
		t.Errorf("default port: %s", got)
	}
	inst.Spec.Server.Port = 7000
	if got := instanceAddress(inst); got != "dns:///api.prod.svc.cluster.local:7000" {
		t.Errorf("explicit port: %s", got)
	}
}

func TestInstanceConnectionOpts_ClientCertificateIsLoadedAndCanBeTheCredential(t *testing.T) {
	crt, key := selfSignedPEM(t)
	certSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "op-cert", Namespace: "default"},
		Data:       map[string][]byte{corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key},
	}
	inst := instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{OperatorClientCertSecretName: "op-cert"}, nil)
	inst.Spec.Server.TLS = &platformv1alpha1.TLSSpec{Enabled: true, ClientCASecretName: "clients-ca"}

	opts, err := instanceConnectionOpts(context.Background(), fakeClientWith(certSecret), inst, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if !opts.TLSEnabled || opts.ClientCertificate == nil {
		t.Fatalf("certificate not loaded: %+v", opts)
	}
	if opts.Token != "" || opts.TokenSource != nil {
		t.Error("with only a certificate there is no bearer credential")
	}

	// The certificate rides along with a bearer credential too.
	withToken := instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{OperatorClientCertSecretName: "op-cert"},
		&platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-auth", Key: "token"})
	tok := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "chatcli-auth", Namespace: "default"}, Data: map[string][]byte{"token": []byte("shared")}}
	opts, err = instanceConnectionOpts(context.Background(), fakeClientWith(certSecret, tok), withToken, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if opts.Token != "shared" || opts.ClientCertificate == nil {
		t.Errorf("both expected, got token=%q cert=%v", opts.Token, opts.ClientCertificate != nil)
	}

	// A public-key-only server is fine when the certificate is there.
	rs := instanceWithSecurity(&platformv1alpha1.ServerSecuritySpec{
		OperatorClientCertSecretName: "op-cert",
		JWTPublicKeyRef:              &platformv1alpha1.SecretKeyRefSpec{Name: "k", Key: "pub"},
	}, nil)
	if _, err := instanceConnectionOpts(context.Background(), fakeClientWith(certSecret), rs, zap.NewNop()); err != nil {
		t.Errorf("certificate satisfies an RS256 server: %v", err)
	}

	// Missing or incomplete certificate secret is an error.
	if _, err := instanceConnectionOpts(context.Background(), fakeClientWith(), inst, zap.NewNop()); err == nil {
		t.Error("missing certificate secret must fail")
	}
	half := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "op-cert", Namespace: "default"}, Data: map[string][]byte{corev1.TLSCertKey: crt}}
	if _, err := instanceConnectionOpts(context.Background(), fakeClientWith(half), inst, zap.NewNop()); err == nil || !strings.Contains(err.Error(), "tls.key") {
		t.Errorf("incomplete certificate secret must name the missing key, got %v", err)
	}
	junk := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "op-cert", Namespace: "default"}, Data: map[string][]byte{corev1.TLSCertKey: []byte("junk"), corev1.TLSPrivateKeyKey: key}}
	if _, err := instanceConnectionOpts(context.Background(), fakeClientWith(junk), inst, zap.NewNop()); err == nil {
		t.Error("an unparsable pair must fail at options time")
	}
}

func TestServerClientConnect_UsesTheInstanceClientCertificate(t *testing.T) {
	crt, key := selfSignedPEM(t)
	pair, err := tls.X509KeyPair(crt, key)
	if err != nil {
		t.Fatal(err)
	}
	sc := NewServerClient(zap.NewNop())
	if err := sc.Connect("localhost:19997", ConnectionOpts{TLSEnabled: true, ClientCertificate: &pair}); err != nil {
		t.Fatalf("a parsed pair must be accepted at dial time: %v", err)
	}
	_ = sc.Close()
}

// fakeInfoServer answers Health and GetServerInfo like the real server.
type fakeInfoServer struct {
	pb.UnimplementedChatCLIServiceServer
	notServing bool
	infoErr    error
}

func (s *fakeInfoServer) Health(context.Context, *pb.HealthRequest) (*pb.HealthResponse, error) {
	st := pb.HealthResponse_SERVING
	if s.notServing {
		st = pb.HealthResponse_NOT_SERVING
	}
	return &pb.HealthResponse{Status: st, Version: "1.210.9"}, nil
}

func (s *fakeInfoServer) GetServerInfo(context.Context, *pb.GetServerInfoRequest) (*pb.GetServerInfoResponse, error) {
	if s.infoErr != nil {
		return nil, s.infoErr
	}
	return &pb.GetServerInfoResponse{Version: "1.211.0", Provider: "CLAUDEAI", Model: "claude-sonnet-5"}, nil
}

// bufconnServerClient dials an in-memory server over plaintext, bypassing
// Connect's mandatory TLS so the probe logic can be exercised.
func bufconnServerClient(t *testing.T, srv pb.ChatCLIServiceServer) *ServerClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	pb.RegisterChatCLIServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return &ServerClient{conn: conn, client: pb.NewChatCLIServiceClient(conn), logger: zap.NewNop()}
}

func TestGRPCProber_ReportsVersionHealthAndCredentialFailures(t *testing.T) {
	inst := newInstance("api", "default")
	inst.Spec.Server.Token = nil // the fixture's token Secret does not exist here
	mk := func(srv pb.ChatCLIServiceServer) *grpcProber {
		return &grpcProber{client: fakeClientWith(), logger: zap.NewNop(), dial: func(string, ConnectionOpts) (*ServerClient, error) {
			return bufconnServerClient(t, srv), nil
		}}
	}
	res, err := mk(&fakeInfoServer{}).ProbeInstance(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healthy || res.Version != "1.211.0" || res.Provider != "CLAUDEAI" || res.Model != "claude-sonnet-5" {
		t.Errorf("unexpected result: %+v", res)
	}

	res, err = mk(&fakeInfoServer{notServing: true}).ProbeInstance(context.Background(), inst)
	if err != nil || res.Healthy {
		t.Errorf("NOT_SERVING must be reported as unhealthy without an error, got %+v %v", res, err)
	}

	_, err = mk(&fakeInfoServer{infoErr: errors.New("rpc error: code = Unauthenticated")}).ProbeInstance(context.Background(), inst)
	if err == nil || !strings.Contains(err.Error(), "credential") {
		t.Errorf("a refused GetServerInfo must be reported as a credential failure, got %v", err)
	}

	dialFail := &grpcProber{client: fakeClientWith(), logger: zap.NewNop(), dial: func(string, ConnectionOpts) (*ServerClient, error) {
		return nil, errors.New("dial tcp: connection refused")
	}}
	if _, err := dialFail.ProbeInstance(context.Background(), inst); err == nil {
		t.Error("dial failure must surface")
	}

	// A production prober against a closed port fails fast instead of hanging.
	p := NewGRPCProber(fakeClientWith(), zap.NewNop()).(*grpcProber)
	closed := newInstance("nowhere", "default")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := p.ProbeInstance(ctx, closed); err == nil {
		t.Error("probing an unreachable Instance must fail")
	}
}

type fakeProber struct {
	res ProbeResult
	err error
}

func (f fakeProber) ProbeInstance(context.Context, *platformv1alpha1.Instance) (ProbeResult, error) {
	return f.res, f.err
}

func TestUpdateStatus_RecordsServerReachableAndVersion(t *testing.T) {
	inst := newInstance("api", "default")
	one := int32(1)
	inst.Spec.Replicas = &one
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
	c := fakeClientWith(inst, deploy)
	r := &InstanceReconciler{Client: c, Scheme: newScheme(), Prober: fakeProber{res: ProbeResult{Healthy: true, Version: "1.211.0", Provider: "OPENAI", Model: "gpt-6-astra"}}}
	if err := r.updateStatus(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	var got platformv1alpha1.Instance
	if err := c.Get(context.Background(), types.NamespacedName{Name: "api", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ServerVersion != "1.211.0" || got.Status.ServerProbeTime == nil {
		t.Errorf("version/probe time not recorded: %+v", got.Status)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, ServerReachableConditionType)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "Serving" {
		t.Errorf("reachable condition: %+v", cond)
	}

	// Probe failure keeps the last known version and flips the condition.
	r.Prober = fakeProber{err: errors.New("server info (credential check): Unauthenticated")}
	if err := r.updateStatus(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	cond = meta.FindStatusCondition(got.Status.Conditions, ServerReachableConditionType)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "ProbeFailed" || !strings.Contains(cond.Message, "credential") {
		t.Errorf("failed probe condition: %+v", cond)
	}
	if got.Status.ServerVersion != "1.211.0" {
		t.Error("last known version is kept across a failed probe")
	}

	// Not ready: no probe, condition Unknown.
	if err := c.Delete(context.Background(), deploy); err != nil {
		t.Fatal(err)
	}
	down := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 0},
	}
	if err := c.Create(context.Background(), down); err != nil {
		t.Fatal(err)
	}
	r.Prober = fakeProber{res: ProbeResult{Healthy: true, Version: "never"}}
	if err := r.updateStatus(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	cond = meta.FindStatusCondition(got.Status.Conditions, ServerReachableConditionType)
	if cond == nil || cond.Status != metav1.ConditionUnknown {
		t.Errorf("not-ready condition: %+v", cond)
	}
	if got.Status.ServerVersion == "never" {
		t.Error("no probe while not ready")
	}

	// No prober configured: nothing recorded.
	plain := newInstance("plain", "default")
	c2 := fakeClientWith(plain)
	r2 := &InstanceReconciler{Client: c2, Scheme: newScheme()}
	if err := r2.updateStatus(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(plain.Status.Conditions, ServerReachableConditionType) != nil {
		t.Error("without a prober the condition is absent")
	}
}

func TestBuildPodSpec_RendersClientCAAndMTLSRole(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}
	inst := newInstance("sec", "default")
	inst.Spec.Server.Token = nil // mutual TLS is the only credential here
	inst.Spec.Server.TLS = &platformv1alpha1.TLSSpec{Enabled: true, SecretName: "srv-tls", ClientCASecretName: "clients-ca"}
	inst.Spec.Server.Security = &platformv1alpha1.ServerSecuritySpec{MTLSRole: "admin"}

	spec := r.buildPodSpec(inst)
	args := strings.Join(spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--tls-client-ca /etc/chatcli/client-ca/ca.crt") {
		t.Errorf("client CA arg missing: %s", args)
	}
	env := envByName(spec.Containers[0].Env)
	if env["CHATCLI_MTLS_ROLE"].Value != "admin" {
		t.Errorf("mTLS role env: %+v", env["CHATCLI_MTLS_ROLE"])
	}
	mounted := false
	for _, v := range spec.Volumes {
		if v.Name == "client-ca" && v.Secret != nil && v.Secret.SecretName == "clients-ca" {
			mounted = true
		}
	}
	if !mounted {
		t.Error("client CA secret not mounted")
	}
	if !instanceHasCredential(inst) {
		t.Error("a client CA is a credential for the reachable-bind check")
	}
	if src, ok := operatorCredentialFor(inst); ok {
		t.Errorf("client CA alone gives the operator nothing to present, got %q", src)
	}
	inst.Spec.Server.Security.OperatorClientCertSecretName = "op-cert"
	if src, ok := operatorCredentialFor(inst); !ok || !strings.Contains(src, "client certificate") {
		t.Errorf("operator certificate is a credential, got %q %v", src, ok)
	}

	// Client CA without TLS enabled renders nothing (the server has no handshake to verify).
	off := newInstance("off", "default")
	off.Spec.Server.TLS = &platformv1alpha1.TLSSpec{Enabled: false, ClientCASecretName: "clients-ca"}
	if strings.Contains(strings.Join(r.buildPodSpec(off).Containers[0].Args, " "), "--tls-client-ca") {
		t.Error("client CA arg must not render with TLS disabled")
	}
}
