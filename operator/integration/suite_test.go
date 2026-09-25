/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package integration runs the operator's controllers against a real
// Kubernetes API server (envtest: kube-apiserver + etcd) with the CRDs from
// config/crd/bases. The fake client used by the unit tests does not enforce
// status subresources, CRD schemas, defaults or owner references; this
// suite does. It needs the envtest binaries: run `make test-integration`
// from operator/, or set KUBEBUILDER_ASSETS. Without them every test skips
// so a plain `go test ./...` stays green.
package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"github.com/diillson/chatcli/operator/controllers"
	"github.com/diillson/chatcli/operator/internal/setup"
)

var k8sClient client.Client

// fakeServer stands in for the ChatCLI server: it answers the RPCs the
// controllers call with deterministic content so the pipeline can be
// driven end to end without an LLM.
type fakeServer struct {
	pb.UnimplementedChatCLIServiceServer
}

func (f *fakeServer) Health(context.Context, *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Status: pb.HealthResponse_SERVING, Version: "integration"}, nil
}

func (f *fakeServer) GetServerInfo(context.Context, *pb.GetServerInfoRequest) (*pb.GetServerInfoResponse, error) {
	return &pb.GetServerInfoResponse{Version: "integration", Provider: "FAKE", Model: "fake-model"}, nil
}

func (f *fakeServer) GetAlerts(context.Context, *pb.GetAlertsRequest) (*pb.GetAlertsResponse, error) {
	return &pb.GetAlertsResponse{}, nil
}

func (f *fakeServer) StreamAlerts(_ *pb.StreamAlertsRequest, stream pb.ChatCLIService_StreamAlertsServer) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-tick.C:
			if err := stream.Send(&pb.StreamAlertsResponse{Heartbeat: true}); err != nil {
				return err
			}
		}
	}
}

// analysisUnavailableFor names Issues the fake server refuses to analyze, so
// they stay in Analyzing: a scenario that needs an Issue parked in a stable
// state uses it to keep the Issue reconciler from racing its own writes.
const analysisUnavailableFor = "db-outage"

func (f *fakeServer) AnalyzeIssue(_ context.Context, req *pb.AnalyzeIssueRequest) (*pb.AnalyzeIssueResponse, error) {
	if req.GetIssueName() == analysisUnavailableFor {
		return nil, status.Error(codes.Unavailable, "analysis withheld by the integration suite")
	}
	return &pb.AnalyzeIssueResponse{
		Analysis:        "Error rate spiked after the last rollout of " + req.GetIssueName(),
		Confidence:      0.92,
		Recommendations: []string{"Scale up", "Roll back the last revision"},
		Model:           "fake-model",
		Provider:        "FAKE",
		SuggestedActions: []*pb.SuggestedAction{{
			Name: "Scale up", Action: "ScaleDeployment", Description: "absorb the load", Params: map[string]string{"replicas": "4"},
		}},
	}, nil
}

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Println("integration: KUBEBUILDER_ASSETS not set; run `make test-integration` (skipping)")
		os.Exit(0)
	}
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctrl.SetLogger(logzap.New(logzap.UseDevMode(false)))

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Println("integration: envtest failed to start:", err)
		return 1
	}
	defer func() { _ = env.Stop() }()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fmt.Println("integration:", err)
		return 1
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		fmt.Println("integration:", err)
		return 1
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		fmt.Println("integration: manager:", err)
		return 1
	}
	k8sClient = mgr.GetClient()

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Println("integration: clientset:", err)
		return 1
	}

	// The controllers talk to an in-memory ChatCLI server.
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	pb.RegisterChatCLIServiceServer(gs, &fakeServer{})
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Println("integration: grpc:", err)
		return 1
	}
	serverClient := controllers.NewServerClientWithConn(conn, zap.NewNop())

	// The decision engine is on so its gate is exercised on a live API
	// server; the other scenarios pass it (medium severity, confidence
	// 0.92 is auto-notify) and the SLA scenario never reaches a plan.
	if _, err := setup.Controllers(mgr, setup.Options{
		Clientset:      clientset,
		ServerClient:   serverClient,
		Logger:         zap.NewNop(),
		AlertTransport: controllers.AlertTransportStream,
		DecisionEngine: true,
	}); err != nil {
		fmt.Println("integration: controllers:", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := mgr.Start(ctx); err != nil {
			fmt.Println("integration: manager stopped:", err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		fmt.Println("integration: cache never synced")
		return 1
	}
	return m.Run()
}

// namespace creates an isolated namespace for one test.
func namespace(t *testing.T, name string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("namespace %s: %v", name, err)
	}
	return name
}

func mustCreate(t *testing.T, obj client.Object) {
	t.Helper()
	if err := k8sClient.Create(context.Background(), obj); err != nil {
		t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
	}
}

func key(ns, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: ns, Name: name}
}

// eventually polls cond until it holds or the deadline passes. Controllers
// requeue on their own timers, so a generous deadline is deliberate.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func ownedBy(obj client.Object, owner client.Object) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.GetUID() {
			return true
		}
	}
	return false
}
