/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func drainValues(env []corev1.EnvVar) []string {
	var out []string
	for _, e := range env {
		if e.Name == envShutdownDelay {
			out = append(out, e.Value)
		}
	}
	return out
}

// The drain is on for rolling replacements, off with persistence
// (Recreate), and an extraEnv value replaces it rather than duplicating it.
func TestBuildPodSpec_ShutdownDrain(t *testing.T) {
	r := &InstanceReconciler{Scheme: newScheme()}

	rolling := newInstance("pool", "default")
	if got := drainValues(r.buildPodSpec(rolling).Containers[0].Env); len(got) != 1 || got[0] != rollingShutdownDelay {
		t.Fatalf("rolling: %s = %v, want [%s]", envShutdownDelay, got, rollingShutdownDelay)
	}

	persistent := newInstance("sticky", "default")
	persistent.Spec.Persistence = &platformv1alpha1.PersistenceSpec{Enabled: true}
	if got := drainValues(r.buildPodSpec(persistent).Containers[0].Env); len(got) != 0 {
		t.Fatalf("Recreate must not drain, got %v", got)
	}

	custom := newInstance("custom", "default")
	custom.Spec.ExtraEnv = []corev1.EnvVar{{Name: envShutdownDelay, Value: "15s"}}
	if got := drainValues(r.buildPodSpec(custom).Containers[0].Env); len(got) != 1 || got[0] != "15s" {
		t.Fatalf("extraEnv: %s = %v, want [15s]", envShutdownDelay, got)
	}
}
