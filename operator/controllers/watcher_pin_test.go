/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"errors"
	"testing"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// A Ready Instance someone created in another namespace is never
// connected: without a pin the bridge stays disconnected, and with a pin
// it dials only the pinned Instance, even while that one is not ready.
func TestWatcherBridge_NeverConnectsToUnpinnedInstance(t *testing.T) {
	rogue := pinnedWatcherInstance("rogue", "tenant-a", "tenant-a")
	pinned := pinnedWatcherInstance("aiops", "chatcli-system", "default")
	pinned.Status.Ready = false
	wb := setupFakeWatcherBridge(rogue, pinned)
	ctx := context.Background()

	if err := wb.discoverAndConnect(ctx); !errors.Is(err, errAIOpsInstanceNotPinned) {
		t.Fatalf("without a pin discovery = %v, want errAIOpsInstanceNotPinned", err)
	}
	if wb.serverClient.IsConnected() || wb.connectedInstance != nil {
		t.Fatal("connected without a pin")
	}

	wb.SetAIOpsInstance(types.NamespacedName{Namespace: "chatcli-system", Name: "aiops"})
	if err := wb.discoverAndConnect(ctx); err == nil {
		t.Fatal("the pinned Instance is not ready: discovery must fail, not fall back to another Instance")
	}
	if wb.serverClient.IsConnected() || wb.connectedInstance != nil {
		t.Fatalf("connected to %v instead of waiting for the pinned Instance", wb.connectedInstance)
	}
	if addr, err := wb.ResolveServerAddress(ctx); err == nil {
		t.Fatalf("resolved %q while the pinned Instance is not ready", addr)
	}

	// Once the pinned Instance is ready, it and only it is dialed.
	ready := newInstance("aiops", "chatcli-system")
	ready.Status.Ready = true
	token := secretFixture("chatcli-server-token", map[string]string{"token": "synthetic"})
	token.Namespace = "chatcli-system"
	wb = setupFakeWatcherBridge(rogue, ready, token)
	wb.SetAIOpsInstance(types.NamespacedName{Namespace: "chatcli-system", Name: "aiops"})
	t.Cleanup(func() { _ = wb.serverClient.Close() })
	if err := wb.discoverAndConnect(ctx); err != nil {
		t.Fatalf("connecting to the pinned Instance: %v", err)
	}
	if wb.connectedInstance == nil || wb.connectedInstance.Namespace != "chatcli-system" || wb.connectedInstance.Name != "aiops" {
		t.Fatalf("connected to %v, want chatcli-system/aiops", wb.connectedInstance)
	}
}

// Alerts become Anomalies only in the namespaces the pinned Instance's
// watcher declares.
func TestWatcherBridge_DropsAlertsOutsideWatchTargets(t *testing.T) {
	inst := pinnedWatcherInstance("aiops", "chatcli-system", "default")
	wb := setupFakeWatcherBridge(inst)
	ctx := context.Background()
	alert := func(ns string) *pb.WatcherAlert {
		a := alertFixture("Deployment/web", "web")
		a.Namespace = ns
		return a
	}
	count := func(ns string) int {
		var list platformv1alpha1.AnomalyList
		if err := wb.client.List(ctx, &list, client.InNamespace(ns)); err != nil {
			t.Fatal(err)
		}
		return len(list.Items)
	}

	wb.handleAlert(ctx, alert("default"))
	if count("default") != 0 {
		t.Fatal("without a pin no alert may become an Anomaly")
	}

	wb.SetAIOpsInstance(types.NamespacedName{Namespace: "chatcli-system", Name: "aiops"})
	wb.handleAlert(ctx, alert("kube-system"))
	if n := count("kube-system"); n != 0 {
		t.Fatalf("alert outside the watch targets created %d Anomaly(ies)", n)
	}
	wb.handleAlert(ctx, alert("default"))
	if n := count("default"); n != 1 {
		t.Fatalf("alert inside the watch targets created %d Anomaly(ies), want 1", n)
	}
}

func TestWatchedNamespaces(t *testing.T) {
	legacy := &platformv1alpha1.Instance{}
	legacy.Spec.Watcher = &platformv1alpha1.WatcherSpec{Enabled: true, Deployment: "web"}
	if got := watchedNamespaces(legacy); !got["default"] || len(got) != 1 {
		t.Fatalf("legacy watcher without namespace = %v, want default", got)
	}
	disabled := pinnedWatcherInstance("x", "y", "prod")
	disabled.Spec.Watcher.Enabled = false
	if got := watchedNamespaces(disabled); len(got) != 0 {
		t.Fatalf("disabled watcher = %v, want none", got)
	}
	if got := watchedNamespaces(&platformv1alpha1.Instance{}); len(got) != 0 {
		t.Fatalf("no watcher = %v, want none", got)
	}
}
