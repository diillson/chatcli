/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// connectedBridge is a streaming bridge whose connected Instance and
// credential fingerprint come from the fake cluster, as discoverAndConnect
// leaves them.
func connectedBridge(t *testing.T) *WatcherBridge {
	t.Helper()
	inst := newInstance("chatcli-prod", "default")
	inst.Spec.Server.Security = &platformv1alpha1.ServerSecuritySpec{
		OperatorTokenRef: &platformv1alpha1.SecretKeyRefSpec{Name: "op-token", Key: "token"},
	}
	wb := setupFakeWatcherBridge(inst,
		secretFixture("chatcli-server-token", map[string]string{"token": "server-1"}),
		secretFixture("op-token", map[string]string{"token": "op-1"}),
	)
	wb.serverClient = bufconnServerClient(t, &fakeAlertServer{live: make(chan *pb.WatcherAlert, 1)})
	wb.pollInterval = 20 * time.Millisecond
	wb.heartbeatTimeout = time.Second
	wb.connectedInstance = inst.DeepCopy()
	fp, err := operatorCredentialFingerprint(context.Background(), wb.client, inst)
	if err != nil {
		t.Fatal(err)
	}
	wb.connectedFingerprint = fp
	return wb
}

func TestCredentialsStale_FollowsTheOperatorsCredentialSecrets(t *testing.T) {
	wb := connectedBridge(t)
	ctx := context.Background()
	if wb.credentialsStale(ctx) {
		t.Fatal("nothing changed yet")
	}

	// The server's API keys are not the operator's credential.
	if err := wb.client.Create(ctx, secretFixture("api-keys", map[string]string{"OPENAI_API_KEY": "k"})); err != nil {
		t.Fatal(err)
	}
	if wb.credentialsStale(ctx) {
		t.Fatal("an unrelated Secret must not drop the connection")
	}

	// Rotating the operator's token makes the connection stale.
	var s = secretFixture("op-token", nil)
	if err := wb.client.Get(ctx, types.NamespacedName{Name: "op-token", Namespace: "default"}, s); err != nil {
		t.Fatal(err)
	}
	s.Data["token"] = []byte("op-2")
	if err := wb.client.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if !wb.credentialsStale(ctx) {
		t.Fatal("a rotated operator token must be noticed")
	}

	// Rebuilt fingerprint: fresh again.
	var inst platformv1alpha1.Instance
	if err := wb.client.Get(ctx, types.NamespacedName{Name: "chatcli-prod", Namespace: "default"}, &inst); err != nil {
		t.Fatal(err)
	}
	fp, err := operatorCredentialFingerprint(ctx, wb.client, &inst)
	if err != nil {
		t.Fatal(err)
	}
	wb.connectedFingerprint = fp
	if wb.credentialsStale(ctx) {
		t.Fatal("fingerprint rebuilt from current material is fresh")
	}

	// A spec change of the credential (issuer stamped on minted tokens) counts too.
	inst.Spec.Server.Security.JWTIssuer = "https://issuer.example"
	if err := wb.client.Update(ctx, &inst); err != nil {
		t.Fatal(err)
	}
	if !wb.credentialsStale(ctx) {
		t.Fatal("a changed issuer must be noticed")
	}

	// A deleted Instance is stale.
	if err := wb.client.Delete(ctx, &inst); err != nil {
		t.Fatal(err)
	}
	if !wb.credentialsStale(ctx) {
		t.Fatal("a deleted Instance must drop the connection")
	}

	// Without a fingerprint (tests, or the fingerprint failed) nothing is checked.
	wb.connectedFingerprint = ""
	if wb.credentialsStale(ctx) {
		t.Error("no fingerprint means no rotation check")
	}
}

func TestConsumeStream_EndsWhenTheCredentialRotates(t *testing.T) {
	wb := connectedBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := wb.consumeStream(ctx)
		done <- err
	}()

	// Let a few heartbeats through, then rotate the server token the
	// operator presents (spec.server.token wins the precedence).
	time.Sleep(50 * time.Millisecond)
	s := secretFixture("chatcli-server-token", nil)
	if err := wb.client.Get(ctx, types.NamespacedName{Name: "chatcli-server-token", Namespace: "default"}, s); err != nil {
		t.Fatal(err)
	}
	s.Data["token"] = []byte("server-2")
	if err := wb.client.Update(ctx, s); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, errCredentialsChanged) {
			t.Fatalf("stream ended with %v, want errCredentialsChanged", err)
		}
	case <-ctx.Done():
		t.Fatal("stream kept running on a rotated credential")
	}

	// The cycle drops the connection so the next round redials.
	wb.dropConnection()
	if wb.serverClient.IsConnected() || wb.connectedFingerprint != "" {
		t.Error("dropConnection leaves nothing behind")
	}
}

func TestCycle_ReconnectsRightAwayOnRotation(t *testing.T) {
	wb := connectedBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		time.Sleep(50 * time.Millisecond)
		s := secretFixture("chatcli-server-token", nil)
		if err := wb.client.Get(ctx, types.NamespacedName{Name: "chatcli-server-token", Namespace: "default"}, s); err != nil {
			return
		}
		s.Data["token"] = []byte("server-2")
		_ = wb.client.Update(ctx, s)
	}()

	backoff := 8 * time.Second
	start := time.Now()
	wb.cycle(ctx, &backoff)
	if ctx.Err() != nil {
		t.Fatal("cycle did not return on rotation")
	}
	if wb.serverClient.IsConnected() {
		t.Error("the stale connection must be dropped")
	}
	if backoff != time.Second || wb.streamFailures != 0 {
		t.Errorf("rotation is not a failure: backoff=%v failures=%d", backoff, wb.streamFailures)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("reconnect must not wait out a backoff")
	}

	// Poll transport: the check runs before each round.
	wb2 := connectedBridge(t)
	wb2.transport = AlertTransportPoll
	s := secretFixture("op-token", nil)
	if err := wb2.client.Get(ctx, types.NamespacedName{Name: "op-token", Namespace: "default"}, s); err != nil {
		t.Fatal(err)
	}
	// op-token is not the credential in use (spec.server.token wins) but is
	// operator material all the same: any change rebuilds the connection.
	s.Data["token"] = []byte("op-2")
	if err := wb2.client.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	wb2.cycle(ctx, &backoff)
	if wb2.serverClient.IsConnected() {
		t.Error("the poll round drops the stale connection before polling")
	}
}

func TestWatcherBridge_IsActiveOnlyWhileRunning(t *testing.T) {
	wb := setupFakeWatcherBridge()
	wb.pollInterval = 10 * time.Millisecond
	if wb.IsActive() {
		t.Fatal("not started")
	}
	stop := runBridge(t, wb)
	eventually(t, wb.IsActive, "bridge active")
	stop()
	if wb.IsActive() {
		t.Error("stopped bridge still active")
	}
}

// --- manual resolution on a replica that is not the leader ---

type recordingInvalidator struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingInvalidator) InvalidateDedupForResource(deployment, namespace string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, namespace+"/"+deployment)
}

func TestIssueReconciler_InvalidatesDedupForAManualResolutionOnce(t *testing.T) {
	now := metav1.Now()
	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{
			Name: "manual", Namespace: "default",
			Annotations: map[string]string{manualResolutionAnnotation: "true"},
			Finalizers:  []string{issueFinalizerName},
		},
		Spec: platformv1alpha1.IssueSpec{Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}},
	}
	c := fakeClientWith(issue)
	issue.Status.State = platformv1alpha1.IssueStateResolved
	issue.Status.ResolvedAt = &now
	inv := &recordingInvalidator{}
	r := &IssueReconciler{Client: c, Scheme: newScheme(), DedupInvalidator: inv}
	ctx := context.Background()

	if err := r.invalidateManualResolution(ctx, issue); err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 1 || inv.calls[0] != "default/web" {
		t.Fatalf("invalidations = %v", inv.calls)
	}
	var stored platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "manual", Namespace: "default"}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Annotations[ManualResolutionDedupAnnotation] != "true" {
		t.Fatal("the invalidation is recorded on the Issue")
	}

	// Already recorded (by the leader, or by the replica that served the
	// resolve): no second invalidation.
	stored.Status = issue.Status
	if err := r.invalidateManualResolution(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 1 {
		t.Errorf("second pass invalidated again: %v", inv.calls)
	}

	// Not a manual resolution: untouched.
	auto := issue.DeepCopy()
	auto.Annotations = nil
	if err := r.invalidateManualResolution(ctx, auto); err != nil {
		t.Fatal(err)
	}
	// Resolved long ago: no dedup entry can be left, and no write.
	old := issue.DeepCopy()
	delete(old.Annotations, ManualResolutionDedupAnnotation)
	long := metav1.NewTime(time.Now().Add(-48 * time.Hour))
	old.Status.ResolvedAt = &long
	if err := r.invalidateManualResolution(ctx, old); err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 1 {
		t.Errorf("unexpected invalidations: %v", inv.calls)
	}
}
