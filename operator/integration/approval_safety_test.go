/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package integration

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	restapi "github.com/diillson/chatcli/operator/api/rest"
	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// scalePolicy gates ScaleDeployment plans of the namespace with one rule.
func scalePolicy(ns string, mode platformv1alpha1.ApprovalMode, required int32) *platformv1alpha1.ApprovalPolicy {
	return &platformv1alpha1.ApprovalPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "scale-gate", Namespace: ns},
		Spec: platformv1alpha1.ApprovalPolicySpec{
			Enabled: true,
			Rules: []platformv1alpha1.ApprovalRule{{
				Name:              "scale",
				Match:             platformv1alpha1.ApprovalMatch{ActionTypes: []platformv1alpha1.RemediationActionType{platformv1alpha1.ActionScaleDeployment}, Namespaces: []string{ns}},
				Mode:              mode,
				RequiredApprovers: required,
			}},
		},
	}
}

// startPipeline creates the objects that lead to a scale plan for target.
func startPipeline(t *testing.T, ns, target string) *platformv1alpha1.Issue {
	t.Helper()
	mustCreate(t, scaleRunbook("error-rate", ns))
	mustCreate(t, deployment(target, ns, 2))
	mustCreate(t, errorRateAnomaly("error-spike", ns, target))
	var issue *platformv1alpha1.Issue
	eventually(t, wait, "the Issue", func() bool {
		issue = firstIssueFor(t, ns, target)
		return issue != nil
	})
	return issue
}

// waitingRequest waits for the plan to park and returns its request.
func waitingRequest(t *testing.T, ns string, issue *platformv1alpha1.Issue) platformv1alpha1.ApprovalRequest {
	t.Helper()
	ctx := context.Background()
	var plan platformv1alpha1.RemediationPlan
	eventually(t, wait, "the plan to wait for approval", func() bool {
		return k8sClient.Get(ctx, key(ns, issue.Name+"-plan-1"), &plan) == nil && plan.Status.State == platformv1alpha1.RemediationStateWaitingApproval
	})
	var ar platformv1alpha1.ApprovalRequest
	eventually(t, wait, "the pending ApprovalRequest", func() bool {
		return k8sClient.Get(ctx, key(ns, "approval-"+plan.Name), &ar) == nil && ar.Status.State == platformv1alpha1.ApprovalStatePending
	})
	return ar
}

func replicas(t *testing.T, ns, name string) int32 {
	t.Helper()
	var d appsv1.Deployment
	if err := k8sClient.Get(context.Background(), key(ns, name), &d); err != nil {
		t.Fatal(err)
	}
	return *d.Spec.Replicas
}

// While the ApprovalPolicies of the namespace cannot be listed, the plan
// stays Pending (it used to run "without approval"), an event says why,
// and once they can be read again the policy parks it as usual.
func TestApprovalGateFailsClosedWhenPoliciesUnreadable(t *testing.T) {
	ns := namespace(t, "it-gate-closed")
	ctx := context.Background()
	policyListFaults.Store(ns, true)
	defer policyListFaults.Delete(ns)
	mustCreate(t, scalePolicy(ns, platformv1alpha1.ApprovalModeManual, 1))
	issue := startPipeline(t, ns, "orders")

	var plan platformv1alpha1.RemediationPlan
	eventually(t, wait, "the plan", func() bool {
		return k8sClient.Get(ctx, key(ns, issue.Name+"-plan-1"), &plan) == nil
	})
	var evs eventsv1.EventList
	eventually(t, wait, "the ApprovalGateUnavailable event", func() bool {
		if err := k8sClient.List(ctx, &evs, client.InNamespace(ns)); err != nil {
			return false
		}
		for _, e := range evs.Items {
			if e.Reason == "ApprovalGateUnavailable" && e.Regarding.Name == plan.Name {
				return true
			}
		}
		return false
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := k8sClient.Get(ctx, key(ns, plan.Name), &plan); err != nil {
			t.Fatal(err)
		}
		if plan.Status.State != "" && plan.Status.State != platformv1alpha1.RemediationStatePending {
			t.Fatalf("plan left Pending while the policies were unreadable: %s (%s)", plan.Status.State, plan.Status.Result)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := replicas(t, ns, "orders"); got != 2 {
		t.Fatalf("the Deployment changed without the gate: replicas=%d", got)
	}

	policyListFaults.Delete(ns)
	waitingRequest(t, ns, issue)
}

var (
	restOnce sync.Once
	restURL  string
)

// restServer starts the REST API against the envtest API server with two
// operator keys, one per approver.
func restServer(t *testing.T) string {
	t.Helper()
	restOnce.Do(func() {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		s := restapi.NewAPIServer(k8sClient, addr)
		s.SetAPIKeyEntries(map[string]restapi.APIKey{
			"key-alice": {Role: "operator", Name: "alice"},
			"key-bob":   {Role: "operator", Name: "bob"},
		})
		go func() { _ = s.Start(context.Background()) }()
		restURL = "http://" + addr
		eventually(t, 10*time.Second, "the REST API to listen", func() bool {
			resp, err := http.Get(restURL + "/healthz")
			if err != nil {
				return false
			}
			_ = resp.Body.Close()
			return true
		})
	})
	return restURL
}

func restDecision(t *testing.T, ns, name, verb, apiKey, approver string) int {
	t.Helper()
	body := strings.NewReader(`{"approver":"` + approver + `","reason":"integration"}`)
	req, err := http.NewRequest(http.MethodPost, restServer(t)+"/api/v1/approvals/"+name+"/"+verb+"?namespace="+ns, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// A quorum-2 request approved through the REST API needs two distinct
// approvers: the first approval (and a second one with the same key) leave
// it Pending and the Deployment untouched; the second key approves it.
func TestRESTApprovalQuorumNeedsTwoApprovers(t *testing.T) {
	ns := namespace(t, "it-rest-quorum")
	ctx := context.Background()
	mustCreate(t, scalePolicy(ns, platformv1alpha1.ApprovalModeQuorum, 2))
	ar := waitingRequest(t, ns, startPipeline(t, ns, "payments"))

	if code := restDecision(t, ns, ar.Name, "approve", "key-alice", "Alice"); code != http.StatusOK {
		t.Fatalf("first approval: %d", code)
	}
	if code := restDecision(t, ns, ar.Name, "approve", "key-alice", "Someone Else"); code != http.StatusConflict {
		t.Fatalf("second approval with the same key: %d, want 409", code)
	}
	time.Sleep(3 * time.Second) // give the reconciler every chance to (wrongly) approve
	if err := k8sClient.Get(ctx, key(ns, ar.Name), &ar); err != nil {
		t.Fatal(err)
	}
	if ar.Status.State != platformv1alpha1.ApprovalStatePending || len(ar.Status.Decisions) != 1 {
		t.Fatalf("after one approver: state=%s decisions=%+v", ar.Status.State, ar.Status.Decisions)
	}
	if got := replicas(t, ns, "payments"); got != 2 {
		t.Fatalf("scaled after one of two approvals: replicas=%d", got)
	}

	if code := restDecision(t, ns, ar.Name, "approve", "key-bob", "Bob"); code != http.StatusOK {
		t.Fatalf("second approver: %d", code)
	}
	eventually(t, wait, "the request to be approved", func() bool {
		return k8sClient.Get(ctx, key(ns, ar.Name), &ar) == nil && ar.Status.State == platformv1alpha1.ApprovalStateApproved
	})
	if ar.Status.Decisions[0].Approver != "Alice (api-key: alice)" || ar.Status.Decisions[1].Approver != "Bob (api-key: bob)" {
		t.Fatalf("recorded approvers = %+v", ar.Status.Decisions)
	}
	eventually(t, wait, "the approved plan to scale the Deployment", func() bool {
		return replicas(t, ns, "payments") == 4
	})
}

// A rejected request cannot be approved afterwards: 409, and it stays
// Rejected.
func TestRESTApproveRejectedRequestConflicts(t *testing.T) {
	ns := namespace(t, "it-rest-rejected")
	ctx := context.Background()
	mustCreate(t, scalePolicy(ns, platformv1alpha1.ApprovalModeManual, 1))
	ar := waitingRequest(t, ns, startPipeline(t, ns, "search"))

	if code := restDecision(t, ns, ar.Name, "reject", "key-alice", "Alice"); code != http.StatusOK {
		t.Fatalf("reject: %d", code)
	}
	eventually(t, wait, "the request to be rejected", func() bool {
		return k8sClient.Get(ctx, key(ns, ar.Name), &ar) == nil && ar.Status.State == platformv1alpha1.ApprovalStateRejected
	})
	if code := restDecision(t, ns, ar.Name, "approve", "key-bob", "Bob"); code != http.StatusConflict {
		t.Fatalf("approve after rejection: %d, want 409", code)
	}
	if err := k8sClient.Get(ctx, key(ns, ar.Name), &ar); err != nil || ar.Status.State != platformv1alpha1.ApprovalStateRejected {
		t.Fatalf("state = %s (err=%v)", ar.Status.State, err)
	}
	if got := replicas(t, ns, "search"); got != 2 {
		t.Fatalf("rejected plan scaled the Deployment: replicas=%d", got)
	}
}
