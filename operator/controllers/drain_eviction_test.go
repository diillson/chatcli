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
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func drainFixture() []client.Object {
	pod := func(name string, mutate func(*corev1.Pod)) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}, Spec: corev1.PodSpec{NodeName: "node-1"}}
		if mutate != nil {
			mutate(p)
		}
		return p
	}
	return []client.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		pod("app", nil),
		pod("guarded", nil),
		pod("ds", func(p *corev1.Pod) {
			p.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Name: "d", APIVersion: "apps/v1", UID: "u"}}
		}),
		pod("mirror", func(p *corev1.Pod) { p.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "x"} }),
		pod("elsewhere", func(p *corev1.Pod) { p.Spec.NodeName = "node-2" }),
	}
}

// evictionFuncs refuses evictions of "guarded" with the 429 a
// PodDisruptionBudget answers, refusals times; -1 refuses forever.
func evictionFuncs(evictions *[]string, refusals int) interceptor.Funcs {
	return interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*corev1.Pod); ok {
				return apierrors.NewBadRequest("drain must evict, not delete")
			}
			return c.Delete(ctx, obj, opts...)
		},
		SubResourceCreate: func(ctx context.Context, c client.Client, sub string, obj client.Object, subObj client.Object, opts ...client.SubResourceCreateOption) error {
			if _, ok := subObj.(*policyv1.Eviction); !ok || sub != "eviction" {
				return apierrors.NewBadRequest("unexpected subresource " + sub)
			}
			*evictions = append(*evictions, obj.GetName())
			if obj.GetName() == "guarded" && refusals != 0 {
				refusals--
				return apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 1)
			}
			return c.Delete(ctx, obj)
		},
	}
}

// Item 4: pods leave through the Eviction API (which honors
// PodDisruptionBudgets); DaemonSet and mirror pods stay.
func TestDrainNode_EvictsThroughEvictionAPI(t *testing.T) {
	var evictions []string
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(drainFixture()...).WithInterceptorFuncs(evictionFuncs(&evictions, 0)).Build()
	r := &RemediationReconciler{Client: c, Scheme: newScheme()}
	if err := r.executeDrainNode(context.Background(), platformv1alpha1.ResourceRef{}, map[string]string{"node": "node-1"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(evictions, ",") != "app,guarded" && strings.Join(evictions, ",") != "guarded,app" {
		t.Fatalf("evictions = %v, want app and guarded only", evictions)
	}
	var node corev1.Node
	if err := c.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &node); err != nil || !node.Spec.Unschedulable {
		t.Fatalf("node not cordoned (err=%v)", err)
	}
}

// Item 4: a 429 from a disruption budget is retried within the timeout.
func TestDrainNode_RetriesDisruptionBudget(t *testing.T) {
	old := drainRetryInterval
	drainRetryInterval = 10 * time.Millisecond
	defer func() { drainRetryInterval = old }()

	var evictions []string
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(drainFixture()...).WithInterceptorFuncs(evictionFuncs(&evictions, 2)).Build()
	r := &RemediationReconciler{Client: c, Scheme: newScheme()}
	if err := r.executeDrainNode(context.Background(), platformv1alpha1.ResourceRef{}, map[string]string{"node": "node-1", "timeout": "5s"}); err != nil {
		t.Fatalf("drain failed although the budget allowed the eviction on the third try: %v", err)
	}
	guarded := 0
	for _, e := range evictions {
		if e == "guarded" {
			guarded++
		}
	}
	if guarded != 3 {
		t.Fatalf("guarded pod evicted %d times, want 3 (two refusals, then accepted)", guarded)
	}
}

// Item 4: a pod the budget never lets go fails the action and is named.
func TestDrainNode_FailsWhenNotDrained(t *testing.T) {
	old := drainRetryInterval
	drainRetryInterval = 10 * time.Millisecond
	defer func() { drainRetryInterval = old }()

	var evictions []string
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(drainFixture()...).WithInterceptorFuncs(evictionFuncs(&evictions, -1)).Build()
	r := &RemediationReconciler{Client: c, Scheme: newScheme()}
	err := r.executeDrainNode(context.Background(), platformv1alpha1.ResourceRef{}, map[string]string{"node": "node-1", "timeout": "100ms"})
	if err == nil || !strings.Contains(err.Error(), "not drained") || !strings.Contains(err.Error(), testNS+"/guarded") {
		t.Fatalf("err = %v, want a not-drained error naming the guarded pod", err)
	}
	if _, err := drainTimeout("soon"); err == nil {
		t.Fatal("an invalid timeout must be rejected")
	}
	if d, _ := drainTimeout("1h"); d != drainMaxTimeout {
		t.Fatalf("timeout cap = %s", d)
	}
}
