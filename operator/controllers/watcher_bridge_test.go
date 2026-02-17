package controllers

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func setupFakeWatcherBridge(objs ...client.Object) *WatcherBridge {
	s := newScheme()
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(
		&platformv1alpha1.Anomaly{},
		&platformv1alpha1.Instance{},
	)
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()
	logger, _ := zap.NewDevelopment()
	sc := NewServerClient(logger)
	return NewWatcherBridge(c, s, sc, logger)
}

func TestMapAlertTypeToSignal(t *testing.T) {
	tests := []struct {
		alertType string
		expected  platformv1alpha1.AnomalySignalType
	}{
		{"HighRestartCount", platformv1alpha1.SignalPodRestart},
		{"OOMKilled", platformv1alpha1.SignalOOMKill},
		{"PodNotReady", platformv1alpha1.SignalPodNotReady},
		{"DeploymentFailing", platformv1alpha1.SignalDeployFail},
		{"CrashLoopBackOff", platformv1alpha1.SignalPodRestart},
		{"unknown", platformv1alpha1.AnomalySignalType("unknown")},
	}

	for _, tt := range tests {
		t.Run(tt.alertType, func(t *testing.T) {
			result := MapAlertTypeToSignal(tt.alertType)
			if result != tt.expected {
				t.Errorf("MapAlertTypeToSignal(%q) = %q, want %q", tt.alertType, result, tt.expected)
			}
		})
	}
}

func TestAlertHash_Deterministic(t *testing.T) {
	alert := &pb.WatcherAlert{
		Type:          "HighRestartCount",
		Object:        "pod-abc",
		Deployment:    "web",
		Namespace:     "default",
		TimestampUnix: 1700000000,
	}

	h1 := alertHash(alert)
	h2 := alertHash(alert)
	if h1 != h2 {
		t.Errorf("hash should be deterministic: %q != %q", h1, h2)
	}

	// Different timestamp in different minute bucket should produce different hash
	alert2 := &pb.WatcherAlert{
		Type:          "HighRestartCount",
		Object:        "pod-abc",
		Deployment:    "web",
		Namespace:     "default",
		TimestampUnix: 1700000000 + 120, // 2 minutes later
	}
	h3 := alertHash(alert2)
	if h1 == h3 {
		t.Error("different minute buckets should produce different hashes")
	}
}

func TestAlertHash_SameMinuteBucket(t *testing.T) {
	// Use timestamps within the same minute (both seconds 5 and 30 of minute starting at 1700000040)
	baseMinute := int64(1700000040) // divisible by 60 → exact minute boundary
	alert1 := &pb.WatcherAlert{
		Type:          "OOMKilled",
		Object:        "pod-xyz",
		Deployment:    "api",
		Namespace:     "production",
		TimestampUnix: baseMinute + 5,
	}
	alert2 := &pb.WatcherAlert{
		Type:          "OOMKilled",
		Object:        "pod-xyz",
		Deployment:    "api",
		Namespace:     "production",
		TimestampUnix: baseMinute + 30, // same minute
	}

	if alertHash(alert1) != alertHash(alert2) {
		t.Error("alerts in the same minute bucket with same fields should have same hash")
	}
}

func TestWatcherBridge_Dedup(t *testing.T) {
	wb := setupFakeWatcherBridge()

	hash := "abc123"
	if wb.isDuplicate(hash) {
		t.Error("should not be duplicate before marking")
	}

	wb.markSeen(hash)
	if !wb.isDuplicate(hash) {
		t.Error("should be duplicate after marking")
	}
}

func TestWatcherBridge_PruneDedup(t *testing.T) {
	wb := setupFakeWatcherBridge()

	// Add an old entry
	wb.mu.Lock()
	wb.seen["old-hash"] = time.Now().Add(-3 * time.Hour) // older than DedupTTL
	wb.seen["new-hash"] = time.Now()
	wb.mu.Unlock()

	wb.pruneDedup()

	if wb.GetSeenCount() != 1 {
		t.Errorf("expected 1 remaining entry after prune, got %d", wb.GetSeenCount())
	}
	if !wb.isDuplicate("new-hash") {
		t.Error("new-hash should still exist after prune")
	}
	if wb.isDuplicate("old-hash") {
		t.Error("old-hash should be pruned")
	}
}

func TestWatcherBridge_CreateAnomaly(t *testing.T) {
	wb := setupFakeWatcherBridge()
	ctx := context.Background()

	// Simulate a connected Instance for label tracking
	wb.connectedInstance = &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-prod",
			Namespace: "chatcli-system",
		},
	}

	alert := &pb.WatcherAlert{
		Type:          "HighRestartCount",
		Severity:      "CRITICAL",
		Message:       "Pod web-abc has 10 restarts",
		Object:        "web-abc",
		Namespace:     "default",
		Deployment:    "web",
		TimestampUnix: time.Now().Unix(),
	}

	if err := wb.createAnomaly(ctx, alert); err != nil {
		t.Fatalf("createAnomaly failed: %v", err)
	}

	// Verify the Anomaly was created
	var anomalies platformv1alpha1.AnomalyList
	if err := wb.client.List(ctx, &anomalies, client.InNamespace("default")); err != nil {
		t.Fatalf("failed to list anomalies: %v", err)
	}
	if len(anomalies.Items) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(anomalies.Items))
	}

	anom := anomalies.Items[0]
	if anom.Spec.Source != platformv1alpha1.AnomalySourceWatcher {
		t.Errorf("expected source watcher, got %q", anom.Spec.Source)
	}
	if anom.Spec.SignalType != platformv1alpha1.SignalPodRestart {
		t.Errorf("expected signal pod_restart, got %q", anom.Spec.SignalType)
	}
	if anom.Spec.Resource.Name != "web" {
		t.Errorf("expected resource name web, got %q", anom.Spec.Resource.Name)
	}
	if anom.Labels["platform.chatcli.io/source"] != "watcher" {
		t.Error("expected source label to be watcher")
	}
	if anom.Labels["platform.chatcli.io/deployment"] != "web" {
		t.Error("expected deployment label to be web")
	}
	// Verify Instance tracking labels (cross-namespace link)
	if anom.Labels["platform.chatcli.io/instance"] != "chatcli-prod" {
		t.Errorf("expected instance label 'chatcli-prod', got %q", anom.Labels["platform.chatcli.io/instance"])
	}
	if anom.Labels["platform.chatcli.io/instance-namespace"] != "chatcli-system" {
		t.Errorf("expected instance-namespace label 'chatcli-system', got %q", anom.Labels["platform.chatcli.io/instance-namespace"])
	}
}

func TestWatcherBridge_ResolveServerAddress(t *testing.T) {
	// No ready instances
	wb := setupFakeWatcherBridge()
	ctx := context.Background()

	_, err := wb.ResolveServerAddress(ctx)
	if err == nil {
		t.Error("expected error when no instances exist")
	}

	// Add a ready instance
	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-prod",
			Namespace: "chatcli-system",
			UID:       types.UID("inst-1"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
			},
		},
		Status: platformv1alpha1.InstanceStatus{
			Ready: true,
		},
	}
	wb2 := setupFakeWatcherBridge(inst)

	addr, err := wb2.ResolveServerAddress(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "chatcli-prod.chatcli-system.svc.cluster.local:50051"
	if addr != expected {
		t.Errorf("expected %q, got %q", expected, addr)
	}
}

func TestWatcherBridge_ResolveServerAddress_DefaultPort(t *testing.T) {
	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-2"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "OPENAI",
		},
		Status: platformv1alpha1.InstanceStatus{
			Ready: true,
		},
	}
	wb := setupFakeWatcherBridge(inst)
	ctx := context.Background()

	addr, err := wb.ResolveServerAddress(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "chatcli.default.svc.cluster.local:50051"
	if addr != expected {
		t.Errorf("expected %q, got %q", expected, addr)
	}
}

func TestSanitizeK8sName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple-name", "simple-name"},
		{"With_Underscores", "with-underscores"},
		{"UPPER-CASE", "upper-case"},
		{"has spaces", "has-spaces"},
		{"---leading-trailing---", "leading-trailing"},
		{"a-very-long-name-that-exceeds-the-kubernetes-sixty-three-character-limit-for-names", "a-very-long-name-that-exceeds-the-kubernetes-sixty-three-charac"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeK8sName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeK8sName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestWatcherBridge_NeedLeaderElection(t *testing.T) {
	wb := setupFakeWatcherBridge()
	if !wb.NeedLeaderElection() {
		t.Error("WatcherBridge should require leader election")
	}
}

func TestBuildConnectionOpts_NoTLSNoToken(t *testing.T) {
	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-plain"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "OPENAI",
			Server:   platformv1alpha1.ServerSpec{Port: 50051},
		},
	}

	wb := setupFakeWatcherBridge(inst)
	ctx := context.Background()

	opts, err := wb.buildConnectionOpts(ctx, inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.TLSEnabled {
		t.Error("TLSEnabled should be false when no TLS config")
	}
	if opts.Token != "" {
		t.Error("Token should be empty when no token config")
	}
	if len(opts.CACert) != 0 {
		t.Error("CACert should be empty when no TLS config")
	}
}

func TestBuildConnectionOpts_WithTLS(t *testing.T) {
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-tls",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"tls.crt": []byte("fake-cert"),
			"tls.key": []byte("fake-key"),
			"ca.crt":  []byte("fake-ca-cert"),
		},
	}

	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-tls"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
				TLS: &platformv1alpha1.TLSSpec{
					Enabled:    true,
					SecretName: "chatcli-tls",
				},
			},
		},
	}

	wb := setupFakeWatcherBridge(inst, tlsSecret)
	ctx := context.Background()

	opts, err := wb.buildConnectionOpts(ctx, inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.TLSEnabled {
		t.Error("TLSEnabled should be true")
	}
	if string(opts.CACert) != "fake-ca-cert" {
		t.Errorf("expected CACert 'fake-ca-cert', got %q", string(opts.CACert))
	}
	if opts.Token != "" {
		t.Error("Token should be empty when no token config")
	}
}

func TestBuildConnectionOpts_WithTLSNoCACert(t *testing.T) {
	// TLS secret exists but has no ca.crt key — should still enable TLS (system CAs)
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-tls",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"tls.crt": []byte("fake-cert"),
			"tls.key": []byte("fake-key"),
		},
	}

	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-tls-noca"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
				TLS: &platformv1alpha1.TLSSpec{
					Enabled:    true,
					SecretName: "chatcli-tls",
				},
			},
		},
	}

	wb := setupFakeWatcherBridge(inst, tlsSecret)
	ctx := context.Background()

	opts, err := wb.buildConnectionOpts(ctx, inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.TLSEnabled {
		t.Error("TLSEnabled should be true even without ca.crt")
	}
	if len(opts.CACert) != 0 {
		t.Error("CACert should be empty when secret lacks ca.crt key")
	}
}

func TestBuildConnectionOpts_WithTLSMissingSecret(t *testing.T) {
	// TLS enabled but secret doesn't exist — should still enable TLS (system CAs), warn only
	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-tls-nosecret"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
				TLS: &platformv1alpha1.TLSSpec{
					Enabled:    true,
					SecretName: "nonexistent-tls-secret",
				},
			},
		},
	}

	wb := setupFakeWatcherBridge(inst)
	ctx := context.Background()

	opts, err := wb.buildConnectionOpts(ctx, inst)
	if err != nil {
		t.Fatalf("unexpected error: %v (missing TLS secret should warn, not error)", err)
	}
	if !opts.TLSEnabled {
		t.Error("TLSEnabled should be true even when secret is missing")
	}
	if len(opts.CACert) != 0 {
		t.Error("CACert should be empty when secret is missing")
	}
}

func TestBuildConnectionOpts_WithToken(t *testing.T) {
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-auth",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"token": []byte("super-secret-token"),
		},
	}

	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-token"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
				Token: &platformv1alpha1.SecretKeyRefSpec{
					Name: "chatcli-auth",
					Key:  "token",
				},
			},
		},
	}

	wb := setupFakeWatcherBridge(inst, tokenSecret)
	ctx := context.Background()

	opts, err := wb.buildConnectionOpts(ctx, inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.TLSEnabled {
		t.Error("TLSEnabled should be false when no TLS config")
	}
	if opts.Token != "super-secret-token" {
		t.Errorf("expected token 'super-secret-token', got %q", opts.Token)
	}
}

func TestBuildConnectionOpts_WithTokenDefaultKey(t *testing.T) {
	// Token spec with empty Key should default to "token"
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-auth",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"token": []byte("default-key-token"),
		},
	}

	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-token-default"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
				Token: &platformv1alpha1.SecretKeyRefSpec{
					Name: "chatcli-auth",
					// Key intentionally empty — should default to "token"
				},
			},
		},
	}

	wb := setupFakeWatcherBridge(inst, tokenSecret)
	ctx := context.Background()

	opts, err := wb.buildConnectionOpts(ctx, inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Token != "default-key-token" {
		t.Errorf("expected token 'default-key-token', got %q", opts.Token)
	}
}

func TestBuildConnectionOpts_TokenSecretMissing(t *testing.T) {
	// Token configured but secret doesn't exist — should error (token is required for auth)
	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-token-nosecret"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
				Token: &platformv1alpha1.SecretKeyRefSpec{
					Name: "nonexistent-secret",
					Key:  "token",
				},
			},
		},
	}

	wb := setupFakeWatcherBridge(inst)
	ctx := context.Background()

	_, err := wb.buildConnectionOpts(ctx, inst)
	if err == nil {
		t.Error("expected error when token secret is missing")
	}
}

func TestBuildConnectionOpts_TokenKeyMissing(t *testing.T) {
	// Token secret exists but the specified key is not present
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-auth",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"other-key": []byte("some-value"),
		},
	}

	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli",
			Namespace: "default",
			UID:       types.UID("inst-token-badkey"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
				Token: &platformv1alpha1.SecretKeyRefSpec{
					Name: "chatcli-auth",
					Key:  "token",
				},
			},
		},
	}

	wb := setupFakeWatcherBridge(inst, tokenSecret)
	ctx := context.Background()

	_, err := wb.buildConnectionOpts(ctx, inst)
	if err == nil {
		t.Error("expected error when token key is missing from secret")
	}
}

func TestBuildConnectionOpts_TLSAndToken(t *testing.T) {
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-tls",
			Namespace: "production",
		},
		Data: map[string][]byte{
			"tls.crt": []byte("prod-cert"),
			"tls.key": []byte("prod-key"),
			"ca.crt":  []byte("prod-ca"),
		},
	}

	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-token",
			Namespace: "production",
		},
		Data: map[string][]byte{
			"api-token": []byte("prod-token-value"),
		},
	}

	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chatcli-prod",
			Namespace: "production",
			UID:       types.UID("inst-full"),
		},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI",
			Server: platformv1alpha1.ServerSpec{
				Port: 50051,
				TLS: &platformv1alpha1.TLSSpec{
					Enabled:    true,
					SecretName: "chatcli-tls",
				},
				Token: &platformv1alpha1.SecretKeyRefSpec{
					Name: "chatcli-token",
					Key:  "api-token",
				},
			},
		},
	}

	wb := setupFakeWatcherBridge(inst, tlsSecret, tokenSecret)
	ctx := context.Background()

	opts, err := wb.buildConnectionOpts(ctx, inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.TLSEnabled {
		t.Error("TLSEnabled should be true")
	}
	if string(opts.CACert) != "prod-ca" {
		t.Errorf("expected CACert 'prod-ca', got %q", string(opts.CACert))
	}
	if opts.Token != "prod-token-value" {
		t.Errorf("expected token 'prod-token-value', got %q", opts.Token)
	}
}
