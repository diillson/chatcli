/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package integration

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// The CRD accepts day-based SLA durations the controller understands and
// rejects free text, which used to be stored and then silently ignored.
func TestIncidentSLADurationValidation(t *testing.T) {
	ns := namespace(t, "it-sla-durations")
	ctx := context.Background()
	sla := func(name, response, resolution string) *platformv1alpha1.IncidentSLA {
		return &platformv1alpha1.IncidentSLA{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       platformv1alpha1.IncidentSLASpec{Severity: platformv1alpha1.IssueSeverityLow, ResponseTime: response, ResolutionTime: resolution},
		}
	}
	if err := k8sClient.Create(ctx, sla("days", "1d", "2d12h")); err != nil {
		t.Fatalf("day-based durations rejected: %v", err)
	}
	if err := k8sClient.Create(ctx, sla("minutes", "90m", "1h30m")); err != nil {
		t.Fatalf("Go durations rejected: %v", err)
	}
	if err := k8sClient.Create(ctx, sla("prose", "1 week", "4h")); err == nil {
		t.Fatal("a free-text duration was accepted")
	}
}
