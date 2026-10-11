/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Labels the WatcherBridge stamps on every Anomaly with the Instance it
// streams alerts from; the Issue created from the Anomaly carries them too.
const (
	labelSourceInstance          = "platform.chatcli.io/instance"
	labelSourceInstanceNamespace = "platform.chatcli.io/instance-namespace"
)

// AIOpsInstanceEnv names the environment variable that pins the Instance
// whose server drives the AIOps pipeline, as "namespace/name".
const AIOpsInstanceEnv = "CHATCLI_OPERATOR_AIOPS_INSTANCE"

// ParseAIOpsInstancePin reads the CHATCLI_OPERATOR_AIOPS_INSTANCE value.
// An empty value means no pin: the AIOps bridge then stays disconnected.
// Anything else must be "namespace/name" with two valid Kubernetes names.
//
// The pin exists because whoever creates an Instance chooses its image:
// connecting to any Ready Instance would let that image drive alerts,
// analyses and remediations with the operator's cluster-wide permissions.
func ParseAIOpsInstancePin(v string) (types.NamespacedName, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return types.NamespacedName{}, nil
	}
	ns, name, ok := strings.Cut(v, "/")
	if !ok || strings.Contains(name, "/") {
		return types.NamespacedName{}, fmt.Errorf("%s=%q: want namespace/name", AIOpsInstanceEnv, v)
	}
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		return types.NamespacedName{}, fmt.Errorf("%s=%q: invalid namespace: %s", AIOpsInstanceEnv, v, strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return types.NamespacedName{}, fmt.Errorf("%s=%q: invalid name: %s", AIOpsInstanceEnv, v, strings.Join(errs, "; "))
	}
	return types.NamespacedName{Namespace: ns, Name: name}, nil
}

// aiopsInstance returns the pinned Instance, the only one whose AIOps
// settings and provider apply. It returns nil when no Instance is pinned or
// the pinned one cannot be read. Source labels on an object are not
// consulted: anyone who can create an Anomaly can set them.
func aiopsInstance(ctx context.Context, c client.Reader, pin types.NamespacedName) *platformv1alpha1.Instance {
	if pin.Name == "" || pin.Namespace == "" {
		return nil
	}
	var inst platformv1alpha1.Instance
	if err := c.Get(ctx, pin, &inst); err != nil {
		return nil
	}
	return &inst
}

// aiopsSpecFor is the AIOps block of the pinned Instance, nil when there is
// none.
func aiopsSpecFor(ctx context.Context, c client.Reader, pin types.NamespacedName) *platformv1alpha1.AIOpsSpec {
	if inst := aiopsInstance(ctx, c, pin); inst != nil {
		return inst.Spec.AIOps
	}
	return nil
}

// resolveInstanceProvider returns the provider and model of the pinned
// Instance. Empty strings let the server use its own defaults.
func resolveInstanceProvider(ctx context.Context, c client.Reader, pin types.NamespacedName) (provider, model string) {
	if inst := aiopsInstance(ctx, c, pin); inst != nil {
		return inst.Spec.Provider, inst.Spec.Model
	}
	return "", ""
}

// watchedNamespaces returns the namespaces an Instance's watcher declares:
// the namespace of every target, or the legacy single-target namespace.
// An omitted namespace is "default", as the server reads it. A watcher that
// is disabled or absent declares none.
func watchedNamespaces(inst *platformv1alpha1.Instance) map[string]bool {
	out := map[string]bool{}
	w := inst.Spec.Watcher
	if w == nil || !w.Enabled {
		return out
	}
	add := func(ns string) {
		if ns == "" {
			ns = "default"
		}
		out[ns] = true
	}
	if len(w.Targets) > 0 {
		for _, t := range w.Targets {
			add(t.Namespace)
		}
		return out
	}
	if w.Deployment != "" {
		add(w.Namespace)
	}
	return out
}
