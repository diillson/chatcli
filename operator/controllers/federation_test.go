/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func registration(name string, connected, degraded bool) *platformv1alpha1.ClusterRegistration {
	cr := &platformv1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fed"},
		Spec:       platformv1alpha1.ClusterRegistrationSpec{KubeconfigSecretRef: platformv1alpha1.SecretRefSpec{Name: name + "-kubeconfig"}},
		Status:     platformv1alpha1.ClusterRegistrationStatus{Connected: connected},
	}
	if degraded {
		cr.Status.Conditions = []metav1.Condition{{Type: ClusterConditionDegraded, Status: metav1.ConditionTrue, Reason: "NodesNotReady", LastTransitionTime: metav1.Now()}}
	}
	return cr
}

func clusterGauge(t *testing.T, status string) float64 {
	return metricValue(t, "chatcli_operator_federation_clusters_total", map[string]string{"status": status})
}

// The gauge is set per state from the registrations that exist: a cluster
// that goes offline moves series instead of being counted twice.
func TestFederation_ClusterGaugeIsSetPerState(t *testing.T) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&platformv1alpha1.ClusterRegistration{}).
		WithObjects(registration("east", true, false), registration("west", true, true), registration("south", false, false)).Build()
	r := &FederationReconciler{Client: c, Scheme: s}
	r.refreshClusterGauges(context.Background(), nil)
	for status, want := range map[string]float64{ClusterStatusConnected: 1, ClusterStatusDegraded: 1, ClusterStatusDisconnected: 1} {
		if got := clusterGauge(t, status); got != want {
			t.Fatalf("federation_clusters_total{%s} = %v, want %v", status, got, want)
		}
	}

	// east fails its health check (its kubeconfig Secret is missing).
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "east", Namespace: "fed"}}); err != nil {
			t.Fatal(err)
		}
	}
	if clusterGauge(t, ClusterStatusConnected) != 0 || clusterGauge(t, ClusterStatusDisconnected) != 2 {
		t.Fatalf("after east went offline: connected %v disconnected %v, want 0 and 2",
			clusterGauge(t, ClusterStatusConnected), clusterGauge(t, ClusterStatusDisconnected))
	}
	var east platformv1alpha1.ClusterRegistration
	if err := c.Get(context.Background(), types.NamespacedName{Name: "east", Namespace: "fed"}, &east); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(east.Status.Conditions, ClusterConditionConnected)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "KubeconfigUnusable" {
		t.Fatalf("Connected condition = %+v", cond)
	}
}

func TestFederation_DegradedWhenNodesNotReady(t *testing.T) {
	ready := corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	notReady := corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}
	if n := notReadyNodes([]corev1.Node{ready, notReady, {}}); n != 2 {
		t.Fatalf("notReadyNodes = %d, want 2", n)
	}
	cr := registration("x", true, false)
	cr.Status.NodeCount = 3
	setClusterConditions(cr, "HealthCheckPassed", "ok", 2)
	if ClusterHealth(cr) != ClusterStatusDegraded {
		t.Fatalf("health = %s, want degraded", ClusterHealth(cr))
	}
	setClusterConditions(cr, "HealthCheckPassed", "ok", 0)
	if ClusterHealth(cr) != ClusterStatusConnected {
		t.Fatalf("health = %s, want connected", ClusterHealth(cr))
	}
	cr.Status.Connected = false
	setClusterConditions(cr, "NodeListFailed", "boom", 0)
	if ClusterHealth(cr) != ClusterStatusDisconnected {
		t.Fatalf("health = %s, want disconnected", ClusterHealth(cr))
	}
}

// A new Issue joins the correlation its peers already carry, and the
// elevation is marked.
func TestFederation_CorrelationReusesIDAndMarksElevation(t *testing.T) {
	s := newScheme()
	mkIssue := func(name string, annotations map[string]string) *platformv1alpha1.Issue {
		iss := newIssue(name, "default")
		iss.Spec.SignalType = "oom_kill"
		iss.Spec.Severity = platformv1alpha1.IssueSeverityMedium
		iss.Status.State = platformv1alpha1.IssueStateAnalyzing
		iss.Annotations = annotations
		return iss
	}
	local := fake.NewClientBuilder().WithScheme(s).WithObjects(
		registration("a", true, false), registration("b", true, false),
		mkIssue("local-oom", nil)).Build()
	remoteA := fake.NewClientBuilder().WithScheme(s).WithObjects(mkIssue("a-oom", map[string]string{AnnotationCrossClusterCorrelation: "xcluster-keepme"})).Build()
	remoteB := fake.NewClientBuilder().WithScheme(s).WithObjects(mkIssue("b-oom", nil)).Build()
	r := &FederationReconciler{Client: local, Scheme: s}
	r.remoteClients.Store("a", remoteA)
	r.remoteClients.Store("b", remoteB)

	trigger := mkIssue("local-oom", nil)
	if err := r.CorrelateAcrossClusters(context.Background(), trigger); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		c    client.Client
		name string
	}{{local, "local-oom"}, {remoteA, "a-oom"}, {remoteB, "b-oom"}} {
		var got platformv1alpha1.Issue
		if err := tc.c.Get(context.Background(), types.NamespacedName{Name: tc.name, Namespace: "default"}, &got); err != nil {
			t.Fatal(err)
		}
		if got.Annotations[AnnotationCrossClusterCorrelation] != "xcluster-keepme" {
			t.Fatalf("%s correlation = %q, want the existing ID", tc.name, got.Annotations[AnnotationCrossClusterCorrelation])
		}
		if got.Annotations[AnnotationAffectedClusters] != "3" || got.Annotations[AnnotationSeverityElevated] != "true" {
			t.Fatalf("%s annotations = %v", tc.name, got.Annotations)
		}
	}
}
