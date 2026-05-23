/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// =============================================================================
// GAP-02 — watcher_bridge UID lookup (lookupResourceUID + computeAlertHash)
// =============================================================================

// TestWatcherBridge_LookupResourceUID covers the live K8s lookup that feeds
// the UID-aware dedup hash. The kind in the alert determines the first try;
// when that misses, the bridge falls back to the other workload kinds before
// giving up and returning the empty-string sentinel.
func TestWatcherBridge_LookupResourceUID(t *testing.T) {
	const (
		deploymentUID  = "uid-deploy"
		statefulSetUID = "uid-sts"
		jobUID         = "uid-job"
	)

	cases := []struct {
		name     string
		objects  []interface{ runtimeObject() }
		alert    *pb.WatcherAlert
		wantUID  string
		describe string
	}{
		{
			name: "Deployment match on first try",
			objects: []interface{ runtimeObject() }{
				deployment("web", "default", deploymentUID),
			},
			alert:   &pb.WatcherAlert{Type: "CrashLoopBackOff", Deployment: "web", Namespace: "default"},
			wantUID: deploymentUID,
		},
		{
			name: "StatefulSet matched via fallback (alert tags as Deployment by inference)",
			objects: []interface{ runtimeObject() }{
				statefulSet("postgres", "default", statefulSetUID),
			},
			alert:   &pb.WatcherAlert{Type: "OOMKilled", Deployment: "postgres", Namespace: "default"},
			wantUID: statefulSetUID,
		},
		{
			name: "Job matched via JobFailed alert type (inferred kind=Job)",
			objects: []interface{ runtimeObject() }{
				job("nightly-export", "default", jobUID),
			},
			alert:   &pb.WatcherAlert{Type: "JobFailed", Deployment: "nightly-export", Namespace: "default"},
			wantUID: jobUID,
		},
		{
			name:    "Missing resource returns the empty-string sentinel",
			alert:   &pb.WatcherAlert{Type: "CrashLoopBackOff", Deployment: "gone", Namespace: "default"},
			wantUID: "",
		},
		{
			name:    "Empty deployment name short-circuits",
			alert:   &pb.WatcherAlert{Type: "CrashLoopBackOff", Namespace: "default"},
			wantUID: "",
		},
		{
			name: "Namespace defaults to 'default' when empty",
			objects: []interface{ runtimeObject() }{
				deployment("web", "default", deploymentUID),
			},
			alert:   &pb.WatcherAlert{Type: "CrashLoopBackOff", Deployment: "web"},
			wantUID: deploymentUID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wb := setupBridgeWithObjects(tc.objects...)
			got := wb.lookupResourceUID(context.Background(), tc.alert)
			if got != tc.wantUID {
				t.Fatalf("lookupResourceUID: want %q, got %q", tc.wantUID, got)
			}
		})
	}
}

// TestWatcherBridge_ComputeAlertHash covers the end-to-end hash flow: the
// alert goes through lookupResourceUID, the result feeds alertHash, and the
// final hash should be stable for the same (alert + UID) tuple. When the
// resource is missing the sentinel keeps the hash distinct from any real UID.
func TestWatcherBridge_ComputeAlertHash(t *testing.T) {
	alert := &pb.WatcherAlert{Type: "CrashLoopBackOff", Deployment: "web", Namespace: "default"}

	t.Run("hash differs across UIDs (canonical GAP-02 case)", func(t *testing.T) {
		wb1 := setupBridgeWithObjects(deployment("web", "default", "uid-A"))
		wb2 := setupBridgeWithObjects(deployment("web", "default", "uid-B"))
		if wb1.computeAlertHash(context.Background(), alert) == wb2.computeAlertHash(context.Background(), alert) {
			t.Fatalf("recreated resource (different UID) must yield a different hash")
		}
	})

	t.Run("hash stable across calls when UID stable", func(t *testing.T) {
		wb := setupBridgeWithObjects(deployment("web", "default", "uid-stable"))
		ctx := context.Background()
		// Take two independent samples to exercise the lookup path twice;
		// SA4000 (identical expressions) would fire on `f(x) != f(x)` so we
		// bind the values first — also makes the failure log more useful.
		first := wb.computeAlertHash(ctx, alert)
		second := wb.computeAlertHash(ctx, alert)
		if first != second {
			t.Fatalf("hash must be deterministic for a stable (alert, UID) tuple, got %q vs %q", first, second)
		}
	})

	t.Run("hash for missing resource is distinct from any UID hash", func(t *testing.T) {
		wbMissing := setupBridgeWithObjects()
		wbPresent := setupBridgeWithObjects(deployment("web", "default", "uid-real"))
		ctx := context.Background()
		if wbMissing.computeAlertHash(ctx, alert) == wbPresent.computeAlertHash(ctx, alert) {
			t.Fatalf("missing-UID sentinel hash must not collide with real-UID hash")
		}
	})
}

// TestWorkloadGVKForKind covers the lookup table that powers the per-kind
// fetch helper. Any kind absent from the table must return false rather than
// the zero GVK, otherwise the fetch helper would issue a misdirected API call.
func TestWorkloadGVKForKind(t *testing.T) {
	cases := []struct {
		kind     string
		wantOK   bool
		wantKind string
	}{
		{kind: "Deployment", wantOK: true, wantKind: "Deployment"},
		{kind: "StatefulSet", wantOK: true, wantKind: "StatefulSet"},
		{kind: "DaemonSet", wantOK: true, wantKind: "DaemonSet"},
		{kind: "Job", wantOK: true, wantKind: "Job"},
		{kind: "CronJob", wantOK: true, wantKind: "CronJob"},
		{kind: "Node", wantOK: true, wantKind: "Node"},
		{kind: "Unknown", wantOK: false},
		{kind: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			gvk, ok := workloadGVKForKind(tc.kind)
			if ok != tc.wantOK {
				t.Fatalf("workloadGVKForKind(%q) ok: want %v, got %v", tc.kind, tc.wantOK, ok)
			}
			if ok && gvk.Kind != tc.wantKind {
				t.Fatalf("workloadGVKForKind(%q) Kind: want %q, got %q", tc.kind, tc.wantKind, gvk.Kind)
			}
		})
	}
}

// TestInvalidateDedupForResource verifies that the GAP-02 refactored
// invalidation path matches by deployment+namespace rather than recomputing
// the hash (which now includes UID and would be impossible to reverse).
func TestInvalidateDedupForResource(t *testing.T) {
	wb := setupBridgeWithObjects()

	wb.markSeen("hash-1", "web", "default")
	wb.markSeen("hash-2", "web", "default")
	wb.markSeen("hash-3", "api", "default")
	wb.markSeen("hash-4", "web", "other-ns")

	if wb.GetSeenCount() != 4 {
		t.Fatalf("setup: want 4 entries, got %d", wb.GetSeenCount())
	}

	wb.InvalidateDedupForResource("web", "default")

	if wb.GetSeenCount() != 2 {
		t.Fatalf("after invalidation: want 2 entries (api/default + web/other-ns), got %d", wb.GetSeenCount())
	}
	if wb.isDuplicate("hash-1") || wb.isDuplicate("hash-2") {
		t.Fatalf("hash-1 / hash-2 (web/default) must have been invalidated")
	}
	if !wb.isDuplicate("hash-3") || !wb.isDuplicate("hash-4") {
		t.Fatalf("entries for unrelated resources must be preserved")
	}
}

// =============================================================================
// GAP-03 — audit_recorder.RecordIssueContained
// =============================================================================

func TestAuditRecorder_RecordIssueContained(t *testing.T) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	ar := NewAuditRecorder(c, s)

	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "iss-1", Namespace: "default", UID: "issue-uid"},
		Spec:       platformv1alpha1.IssueSpec{Severity: platformv1alpha1.IssueSeverityCritical},
		Status:     platformv1alpha1.IssueStatus{RemediationAttempts: 2},
	}

	if err := ar.RecordIssueContained(context.Background(), issue, "rollback image to v1.2.3 and scale to 3"); err != nil {
		t.Fatalf("RecordIssueContained: unexpected error %v", err)
	}

	var events platformv1alpha1.AuditEventList
	if err := c.List(context.Background(), &events); err != nil {
		t.Fatalf("List AuditEvents: %v", err)
	}
	if len(events.Items) != 1 {
		t.Fatalf("want exactly one audit event, got %d", len(events.Items))
	}
	ev := events.Items[0]
	if ev.Spec.EventType != "issue_contained" {
		t.Fatalf("event type: want issue_contained, got %q", ev.Spec.EventType)
	}
	// Severity MUST be warning, NOT info — the platform has not actually
	// resolved the incident; recording it as info would mask the unresolved bug.
	if ev.Spec.Severity != "warning" {
		t.Fatalf("severity: want warning, got %q (recording Contained as info would mask unresolved bug)", ev.Spec.Severity)
	}
	if !strings.Contains(ev.Spec.Details["required_action"], "rollback image to v1.2.3") {
		t.Fatalf("required_action detail must include the human-action description, got %q", ev.Spec.Details["required_action"])
	}
}

// =============================================================================
// helpers
// =============================================================================

// setupBridgeWithObjects builds a WatcherBridge backed by a fake K8s client
// pre-populated with the given workload objects. Each helper below wraps the
// concrete K8s types in a small adapter so the test cases can declare them
// inline without leaking corev1/appsv1 imports across the table.
func setupBridgeWithObjects(objs ...interface{ runtimeObject() }) *WatcherBridge {
	s := newScheme()
	builder := fake.NewClientBuilder().WithScheme(s)
	for _, o := range objs {
		switch v := o.(type) {
		case *deploymentWrapper:
			builder = builder.WithObjects(v.obj)
		case *statefulSetWrapper:
			builder = builder.WithObjects(v.obj)
		case *jobWrapper:
			builder = builder.WithObjects(v.obj)
		}
	}
	return NewWatcherBridge(builder.Build(), s, nil, zap.NewNop())
}

// deploymentWrapper / statefulSetWrapper / jobWrapper are tiny tagged wrappers
// so we can use one variadic objs parameter on setupBridgeWithObjects above
// and still type-switch to register them correctly with the fake client.
type deploymentWrapper struct{ obj *appsv1.Deployment }

func (*deploymentWrapper) runtimeObject() {}

type statefulSetWrapper struct{ obj *appsv1.StatefulSet }

func (*statefulSetWrapper) runtimeObject() {}

type jobWrapper struct{ obj *batchv1.Job }

func (*jobWrapper) runtimeObject() {}

func deployment(name, namespace, uid string) *deploymentWrapper {
	return &deploymentWrapper{obj: &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid)},
	}}
}

func statefulSet(name, namespace, uid string) *statefulSetWrapper {
	return &statefulSetWrapper{obj: &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid)},
	}}
}

func job(name, namespace, uid string) *jobWrapper {
	return &jobWrapper{obj: &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid)},
	}}
}
