/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rest

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"github.com/diillson/chatcli/operator/controllers"
)

// maxApproverNameRunes bounds the free-text approver name of a decision.
const maxApproverNameRunes = 128

// sanitizeApproverName trims the typed approver name, drops control
// characters and bounds its length.
func sanitizeApproverName(name string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsControl(r) {
			continue
		}
		if n == maxApproverNameRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// handleApprovalDecision records one approver's decision on a pending
// ApprovalRequest, the same way an approve/reject annotation does: a
// status.decisions entry with the approver and a timestamp. The
// ApprovalReconciler then evaluates the decisions against the rule
// (required approvers, change window) and moves the request to Approved or
// Rejected; this handler never sets the state itself. The approver
// recorded is the typed name plus the identity of the API key that
// authenticated the call. A request that is not Pending, or on which the
// same key already decided, answers 409.
func (s *APIServer) handleApprovalDecision(w http.ResponseWriter, r *http.Request, name, decision string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	var req ApprovalDecisionRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	who := sanitizeApproverName(req.Approver)
	if who == "" {
		writeError(w, http.StatusBadRequest, "approver is required")
		return
	}
	verdict := controllers.ApprovalDecisionApproved
	if decision == string(v1alpha1.ApprovalStateRejected) {
		verdict = controllers.ApprovalDecisionRejected
	}

	if ns == "" {
		obj, err := s.getUnstructured(ctx, "approvalrequests", name, ns)
		if err != nil {
			writeError(w, http.StatusNotFound, "approval not found: "+err.Error())
			return
		}
		ns = obj.GetNamespace()
	}

	approver := controllers.FormatAPIKeyApprover(identityFromContext(ctx), who)
	var ar v1alpha1.ApprovalRequest
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &ar); err != nil {
			return err
		}
		if err := controllers.AppendApprovalDecision(&ar, approver, verdict, req.Reason, metav1.Now()); err != nil {
			return err
		}
		if ar.Status.State == "" {
			ar.Status.State = v1alpha1.ApprovalStatePending
		}
		return s.client.Status().Update(ctx, &ar)
	})
	switch {
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, "approval not found: "+err.Error())
		return
	case errors.Is(err, controllers.ErrApprovalNotPending), errors.Is(err, controllers.ErrApproverAlreadyDecided):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, controllers.ErrInvalidApprovalDecision):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "failed to record decision: "+err.Error())
		return
	}

	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&ar)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode approval: "+err.Error())
		return
	}
	obj := &unstructured.Unstructured{Object: content}
	ai := unstructuredToApproval(obj.Object)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion:   "v1",
		Kind:         "ApprovalRequest",
		Spec:         ai,
		Status:       ai,
		ResourceMeta: unstructuredResourceMeta(obj),
	})
}

// fillApprovalDecisions sets the decision fields of an ApprovalItem from
// the CRD status: status.decisions[] (who approved or rejected, and why)
// and approvedAt / rejectedAt / expiredAt (when it was decided).
func fillApprovalDecisions(ai *ApprovalItem, spec, status map[string]interface{}) {
	if spec != nil {
		if n := toFloat64(spec["requiredApprovers"]); n >= 1 && n <= math.MaxInt32 {
			ai.RequiredApprovers = int32(n)
		}
	}
	if status == nil {
		return
	}
	var approvedBy, rejectedBy []string
	rejectionReason, approvalReason := "", ""
	decisions, _ := status["decisions"].([]interface{})
	for _, raw := range decisions {
		d, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		item := ApprovalDecisionItem{}
		item.Approver, _ = d["approver"].(string)
		item.Decision, _ = d["decision"].(string)
		item.Reason, _ = d["reason"].(string)
		item.Timestamp, _ = d["timestamp"].(string)
		ai.Decisions = append(ai.Decisions, item)
		switch item.Decision {
		case controllers.ApprovalDecisionApproved:
			approvedBy = append(approvedBy, item.Approver)
			if item.Reason != "" {
				approvalReason = item.Reason
			}
		case controllers.ApprovalDecisionRejected:
			rejectedBy = append(rejectedBy, item.Approver)
			if rejectionReason == "" {
				rejectionReason = item.Reason
			}
		}
	}
	ai.ApprovedBy = strings.Join(approvedBy, ", ")
	ai.RejectedBy = strings.Join(rejectedBy, ", ")
	ai.DecisionReason = approvalReason
	if len(rejectedBy) > 0 {
		ai.DecisionReason = rejectionReason
	}
	for _, field := range []string{"approvedAt", "rejectedAt", "expiredAt"} {
		if at, ok := status[field].(string); ok && at != "" {
			ai.DecidedAt = &at
			break
		}
	}
}
