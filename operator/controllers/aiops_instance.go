/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Labels the WatcherBridge stamps on every Anomaly with the Instance it
// streams alerts from; the Issue created from the Anomaly carries them too.
const (
	labelSourceInstance          = "platform.chatcli.io/instance"
	labelSourceInstanceNamespace = "platform.chatcli.io/instance-namespace"
)

// aiopsInstance returns the Instance whose AIOps settings apply to an
// object: the one its source labels name, else the Instance the
// WatcherBridge connects to (the first Ready one in list order), else the
// first Instance. It returns nil when there is none. Reading Items[0]
// picked an arbitrary Instance whenever several existed.
func aiopsInstance(ctx context.Context, c client.Reader, labels map[string]string) *platformv1alpha1.Instance {
	var instances platformv1alpha1.InstanceList
	if err := c.List(ctx, &instances); err != nil || len(instances.Items) == 0 {
		return nil
	}
	name, ns := labels[labelSourceInstance], labels[labelSourceInstanceNamespace]
	if name != "" {
		for i := range instances.Items {
			inst := &instances.Items[i]
			if inst.Name == name && (ns == "" || inst.Namespace == ns) {
				return inst
			}
		}
	}
	for i := range instances.Items {
		if instances.Items[i].Status.Ready {
			return &instances.Items[i]
		}
	}
	return &instances.Items[0]
}

// aiopsSpecFor is the AIOps block of aiopsInstance, nil when there is none.
func aiopsSpecFor(ctx context.Context, c client.Reader, labels map[string]string) *platformv1alpha1.AIOpsSpec {
	if inst := aiopsInstance(ctx, c, labels); inst != nil {
		return inst.Spec.AIOps
	}
	return nil
}
