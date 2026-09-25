/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rest

import (
	"net/http"
)

// policyKinds maps the REST segment to the CRD plural for the four policy
// kinds the controllers read but that had no surface beyond kubectl.
var policyKinds = map[string]struct{ plural, kind, listKind string }{
	"approval":     {"approvalpolicies", "ApprovalPolicy", "ApprovalPolicyList"},
	"notification": {"notificationpolicies", "NotificationPolicy", "NotificationPolicyList"},
	"escalation":   {"escalationpolicies", "EscalationPolicy", "EscalationPolicyList"},
	"sla":          {"incidentslas", "IncidentSLA", "IncidentSLAList"},
}

// PolicyItem is a policy of any of the four kinds, served as its raw spec
// and status: the kinds differ too much for one flat projection, and the
// dashboard renders each one on its own.
type PolicyItem struct {
	Name              string                 `json:"name"`
	Namespace         string                 `json:"namespace"`
	Kind              string                 `json:"kind"`
	CreationTimestamp string                 `json:"creationTimestamp,omitempty"`
	Spec              map[string]interface{} `json:"spec,omitempty"`
	Status            map[string]interface{} `json:"status,omitempty"`
}

func policyItemFrom(kind string, obj map[string]interface{}) PolicyItem {
	item := PolicyItem{Kind: kind}
	if meta, ok := obj["metadata"].(map[string]interface{}); ok {
		item.Name, _ = meta["name"].(string)
		item.Namespace, _ = meta["namespace"].(string)
		item.CreationTimestamp, _ = meta["creationTimestamp"].(string)
	}
	item.Spec, _ = obj["spec"].(map[string]interface{})
	item.Status, _ = obj["status"].(map[string]interface{})
	return item
}

// routePolicies serves GET /policies/{approval|notification|escalation|sla}
// and GET /policies/{kind}/{name}. Read-only: policies are written through
// kubectl or GitOps, where they are reviewed.
func (s *APIServer) routePolicies(w http.ResponseWriter, r *http.Request, rest []string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "policies endpoints only support GET")
		return
	}
	if !hasMinRole(roleFromContext(r.Context()), "viewer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	if len(rest) == 0 {
		writeError(w, http.StatusNotFound, "policy kind required: approval, notification, escalation or sla")
		return
	}
	pk, ok := policyKinds[rest[0]]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown policy kind: "+rest[0])
		return
	}
	switch len(rest) {
	case 1:
		s.handleListPolicies(w, r, pk.plural, pk.kind, pk.listKind)
	case 2:
		s.handleGetPolicy(w, r, pk.plural, pk.kind, rest[1])
	default:
		writeError(w, http.StatusNotFound, "unknown policies endpoint")
	}
}

func (s *APIServer) handleListPolicies(w http.ResponseWriter, r *http.Request, plural, kind, listKind string) {
	ctx := r.Context()
	pp := parsePagination(r)
	ns := r.URL.Query().Get("namespace")

	objs, err := s.listUnstructured(ctx, plural, ns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list "+plural+": "+err.Error())
		return
	}
	items := make([]PolicyItem, 0, len(objs))
	for _, obj := range objs {
		items = append(items, policyItemFrom(kind, obj))
	}
	total := len(items)
	start, end := paginateSlice(total, pp)
	writeListResponse(w, listKind, items[start:end], total, pp)
}

func (s *APIServer) handleGetPolicy(w http.ResponseWriter, r *http.Request, plural, kind, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	obj, err := s.getUnstructured(ctx, plural, name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, kind+" not found: "+err.Error())
		return
	}
	item := policyItemFrom(kind, obj.Object)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion:   "v1",
		Kind:         kind,
		Spec:         item.Spec,
		Status:       item.Status,
		ResourceMeta: unstructuredResourceMeta(obj),
	})
}
