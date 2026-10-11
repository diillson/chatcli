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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// Synthetic secrets: none of these is a real credential.
const (
	leakedPassword = "password=Synthet1cPw-0000"
	leakedDBURI    = "postgres://app:synthetic-db-pass@db.internal:5432/orders"
)

func leakyEvent() *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "web-event-1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Name: "web", Namespace: "default"},
		Type:           "Warning",
		Reason:         "BackOff",
		Message:        "connect failed " + leakedPassword + " via " + leakedDBURI,
		Count:          3,
		LastTimestamp:  metav1.Now(),
	}
}

// The shared context builder scrubs what it collects, so every caller gets
// redacted text, not only the AIInsight path.
func TestBuildContext_ScrubsSensitiveData(t *testing.T) {
	_, c := setupAgenticReconciler(&mockAgenticStepper{}, newDeployment("web", "default", 1), leakyEvent())
	out, err := NewKubernetesContextBuilder(c).BuildContext(context.Background(),
		platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Synthet1cPw-0000", "synthetic-db-pass"} {
		if strings.Contains(out, secret) {
			t.Fatalf("context leaks %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "[REDACTED:") {
		t.Fatalf("context shows no redaction:\n%s", out)
	}
}

// The agentic step sends the scrubbed context and a scrubbed description.
func TestAgentic_ContextIsScrubbed(t *testing.T) {
	issue := newIssue("agent-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	issue.Spec.Description = "orders failing: " + leakedDBURI
	mock := &mockAgenticStepper{response: &pb.AgenticStepResponse{Reasoning: "watching"}}
	r, c := setupAgenticReconciler(mock, issue, newAgenticPlan("agent-plan", "default", "agent-issue"),
		newDeployment("web", "default", 1), leakyEvent())
	r.ContextBuilder = NewKubernetesContextBuilder(c)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "agent-plan", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	if mock.request == nil {
		t.Fatal("AgenticStep was not called")
	}
	for _, field := range []string{mock.request.KubernetesContext, mock.request.Description} {
		if strings.Contains(field, "Synthet1cPw-0000") || strings.Contains(field, "synthetic-db-pass") {
			t.Fatalf("agentic request leaks a secret: %q", field)
		}
	}
	if !strings.Contains(mock.request.KubernetesContext, "BackOff") {
		t.Fatalf("the event itself must still reach the model: %q", mock.request.KubernetesContext)
	}
}
