package rest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"github.com/diillson/chatcli/operator/controllers"
	"github.com/diillson/chatcli/operator/web"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// unstructuredList wraps unstructured.UnstructuredList for the analytics helper.
type unstructuredList = unstructured.UnstructuredList

// groupVersionKind returns the GVK for known plural resource names.
func groupVersionKind(plural string) schema.GroupVersionKind {
	group := "platform.chatcli.io"
	version := "v1alpha1"

	kindMap := map[string]string{
		"servicelevelobjectives": "ServiceLevelObjectiveList",
		"approvalrequests":       "ApprovalRequestList",
		"auditevents":            "AuditEventList",
		"clusterregistrations":   "ClusterRegistrationList",
		"notificationpolicies":   "NotificationPolicyList",
		"escalationpolicies":     "EscalationPolicyList",
		"incidentslas":           "IncidentSLAList",
		"approvalpolicies":       "ApprovalPolicyList",
	}
	kind, ok := kindMap[plural]
	if !ok {
		kind = plural + "List"
	}
	return schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
}

// singleGVK returns the singular GVK for a plural resource name.
func singleGVK(plural string) schema.GroupVersionKind {
	group := "platform.chatcli.io"
	version := "v1alpha1"

	kindMap := map[string]string{
		"servicelevelobjectives": "ServiceLevelObjective",
		"approvalrequests":       "ApprovalRequest",
		"auditevents":            "AuditEvent",
		"clusterregistrations":   "ClusterRegistration",
		"notificationpolicies":   "NotificationPolicy",
		"escalationpolicies":     "EscalationPolicy",
		"incidentslas":           "IncidentSLA",
		"approvalpolicies":       "ApprovalPolicy",
	}
	kind, ok := kindMap[plural]
	if !ok {
		kind = plural
	}
	return schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
}

// WatcherDedupInvalidator allows the API server to invalidate watcher dedup entries.
type WatcherDedupInvalidator interface {
	InvalidateDedupForResource(deployment, namespace string)
}

// APIServer provides a REST API gateway for AIOps resources.
type APIServer struct {
	client        client.Client
	listenAddr    string
	apiKeyHeader  string
	apiKeysMu     sync.RWMutex
	apiKeys       map[string]string // key -> role
	limiter       *rateLimiter
	corsOrigin    string
	watcherBridge WatcherDedupInvalidator // optional, for dedup invalidation on manual resolve
}

// NewAPIServer creates a new APIServer.
func NewAPIServer(c client.Client, addr string) *APIServer {
	return &APIServer{
		client:       c,
		listenAddr:   addr,
		apiKeyHeader: "X-API-Key",
		apiKeys:      make(map[string]string),
		limiter:      newRateLimiter(30), // Security (M3): 30 requests/minute (reduced from 100)
		corsOrigin:   "",                 // Security (H6): deny-all CORS by default
	}
}

// SetAPIKeys configures API keys. Keys map API key strings to role names.
// Thread-safe: can be called at any time to hot-reload keys.
func (s *APIServer) SetAPIKeys(keys map[string]string) {
	s.apiKeysMu.Lock()
	defer s.apiKeysMu.Unlock()
	s.apiKeys = keys
}

// SetWatcherBridge sets the watcher bridge for dedup invalidation on manual resolve.
func (s *APIServer) SetWatcherBridge(wb WatcherDedupInvalidator) {
	s.watcherBridge = wb
}

// SetCORSOrigin configures the allowed CORS origin.
func (s *APIServer) SetCORSOrigin(origin string) {
	s.corsOrigin = origin
}

// Start implements manager.Runnable and starts the HTTP server.
func (s *APIServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Health endpoints (no auth required).
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)

	// All API routes go through the router.
	mux.Handle("/api/", chain(
		http.HandlerFunc(s.routeAPI),
		loggingMiddleware,
		s.corsMiddleware,
		s.rateLimitMiddleware,
		s.authMiddleware,
	))

	// Serve Web UI from embedded static files.
	webFS, err := fs.Sub(web.StaticFiles, "static")
	if err == nil {
		fileServer := http.FileServer(http.FS(webFS))
		mux.Handle("/", chain(
			fileServer,
			s.corsMiddleware,
		))
	} else {
		// Fallback: CORS preflight for non-api paths.
		mux.Handle("/", chain(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeError(w, http.StatusNotFound, "not found")
			}),
			s.corsMiddleware,
		))
	}

	server := &http.Server{
		Addr:         s.listenAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("[REST] shutdown error: %v", err)
		}
	}()

	// Security (M4): TLS support for REST API.
	// If CHATCLI_AIOPS_TLS_CERT and CHATCLI_AIOPS_TLS_KEY are set, use HTTPS.
	certFile := os.Getenv("CHATCLI_AIOPS_TLS_CERT")
	keyFile := os.Getenv("CHATCLI_AIOPS_TLS_KEY")

	if certFile != "" && keyFile != "" {
		server.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS13,
		}
		log.Printf("[REST] API server starting with TLS on %s", s.listenAddr)
		if err := server.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("REST API server (TLS) failed: %w", err)
		}
	} else {
		log.Printf("[REST] API server starting on %s (no TLS — configure CHATCLI_AIOPS_TLS_CERT/KEY for production)", s.listenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("REST API server failed: %w", err)
		}
	}
	return nil
}

// routeAPI is the main API router that dispatches based on path segments.
func (s *APIServer) routeAPI(w http.ResponseWriter, r *http.Request) {
	segs := pathSegments(r.URL.Path)

	// All routes start with ["api", "v1", ...].
	if len(segs) < 3 || segs[0] != "api" || segs[1] != "v1" {
		writeError(w, http.StatusNotFound, "unknown API path")
		return
	}

	resource := segs[2]
	rest := segs[3:]

	switch resource {
	case "incidents":
		s.routeIncidents(w, r, rest)
	case "slos":
		s.routeSLOs(w, r, rest)
	case "runbooks":
		s.routeRunbooks(w, r, rest)
	case "approvals":
		s.routeApprovals(w, r, rest)
	case "postmortems":
		s.routePostMortems(w, r, rest)
	case "analytics":
		s.routeAnalytics(w, r, rest)
	case "clusters":
		s.routeClusters(w, r, rest)
	case "audit":
		s.routeAudit(w, r, rest)
	case "aiinsights":
		s.routeAIInsights(w, r, rest)
	case "remediations":
		s.routeRemediations(w, r, rest)
	case "federation":
		s.routeFederation(w, r, rest)
	default:
		writeError(w, http.StatusNotFound, "unknown resource: "+resource)
	}
}

// ========== Incidents ==========

func (s *APIServer) routeIncidents(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		// GET /api/v1/incidents
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleListIncidents(w, r)

	case len(rest) == 1 && r.Method == http.MethodGet:
		// GET /api/v1/incidents/:name
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleGetIncident(w, r, rest[0])

	case len(rest) == 2 && rest[1] == "acknowledge" && r.Method == http.MethodPost:
		// POST /api/v1/incidents/:name/acknowledge
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleAcknowledgeIncident(w, r, rest[0])

	case len(rest) == 2 && rest[1] == "snooze" && r.Method == http.MethodPost:
		// POST /api/v1/incidents/:name/snooze
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleSnoozeIncident(w, r, rest[0])

	case len(rest) == 2 && rest[1] == "resolve" && r.Method == http.MethodPost:
		// POST /api/v1/incidents/:name/resolve
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleResolveIncident(w, r, rest[0])

	case len(rest) == 2 && rest[1] == "timeline" && r.Method == http.MethodGet:
		// GET /api/v1/incidents/:name/timeline
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleGetIncidentTimeline(w, r, rest[0])

	case len(rest) == 2 && rest[1] == "remediation" && r.Method == http.MethodGet:
		// GET /api/v1/incidents/:name/remediation
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleGetIncidentRemediation(w, r, rest[0])

	default:
		writeError(w, http.StatusNotFound, "unknown incidents endpoint")
	}
}

func (s *APIServer) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)

	var issues v1alpha1.IssueList
	opts := []client.ListOption{}
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(ctx, &issues, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issues: "+err.Error())
		return
	}

	// Apply filters.
	severity := r.URL.Query().Get("severity")
	state := r.URL.Query().Get("state")
	tr := parseTimeRange(r)

	filtered := make([]v1alpha1.Issue, 0, len(issues.Items))
	for _, iss := range issues.Items {
		if severity != "" && string(iss.Spec.Severity) != severity {
			continue
		}
		if state != "" && string(iss.Status.State) != state {
			continue
		}
		if !inTimeRange(iss.CreationTimestamp.Time, tr) {
			continue
		}
		filtered = append(filtered, iss)
	}

	total := len(filtered)
	start, end := paginateSlice(total, pp)
	page := filtered[start:end]

	items := make([]IncidentItem, 0, len(page))
	for _, iss := range page {
		items = append(items, issueToIncidentItem(iss))
	}

	writeListResponse(w, "IncidentList", items, total, pp)
}

func (s *APIServer) handleGetIncident(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	issue, err := s.getIssue(ctx, name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found: "+err.Error())
		return
	}

	item := issueToIncidentItem(*issue)
	writeSingleResponse(w, "Incident", item, item, resourceMetaFromIssue(*issue))
}

func (s *APIServer) handleAcknowledgeIncident(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	issue, err := s.getIssue(ctx, name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found: "+err.Error())
		return
	}

	// Add acknowledgment annotation.
	if issue.Annotations == nil {
		issue.Annotations = make(map[string]string)
	}
	issue.Annotations["aiops.chatcli.io/acknowledged"] = "true"
	issue.Annotations["aiops.chatcli.io/acknowledged-at"] = time.Now().Format(time.RFC3339)
	issue.Annotations["aiops.chatcli.io/acknowledged-by"] = roleFromContext(ctx)

	if err := s.client.Update(ctx, issue); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to acknowledge: "+err.Error())
		return
	}

	item := issueToIncidentItem(*issue)
	writeSingleResponse(w, "Incident", item, item, resourceMetaFromIssue(*issue))
}

func (s *APIServer) handleSnoozeIncident(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	var req SnoozeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	duration, err := time.ParseDuration(req.Duration)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid duration: "+err.Error())
		return
	}

	issue, err := s.getIssue(ctx, name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found: "+err.Error())
		return
	}

	if issue.Annotations == nil {
		issue.Annotations = make(map[string]string)
	}
	issue.Annotations["aiops.chatcli.io/snoozed-until"] = time.Now().Add(duration).Format(time.RFC3339)
	issue.Annotations["aiops.chatcli.io/snoozed-by"] = roleFromContext(ctx)

	if err := s.client.Update(ctx, issue); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to snooze: "+err.Error())
		return
	}

	item := issueToIncidentItem(*issue)
	writeSingleResponse(w, "Incident", item, item, resourceMetaFromIssue(*issue))
}

func (s *APIServer) handleResolveIncident(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	var reqBody struct {
		Resolution string `json:"resolution"`
	}
	_ = readJSON(r, &reqBody)

	issue, err := s.getIssue(ctx, name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found: "+err.Error())
		return
	}

	// Only allow resolving Escalated or non-terminal issues
	if issue.Status.State == v1alpha1.IssueStateResolved {
		writeError(w, http.StatusConflict, "issue is already resolved")
		return
	}

	// Update status to Resolved
	resolution := reqBody.Resolution
	if resolution == "" {
		resolution = "Manually resolved via dashboard"
	}
	issue.Status.State = v1alpha1.IssueStateResolved
	issue.Status.Resolution = resolution
	now := metav1.Now()
	issue.Status.ResolvedAt = &now

	if err := s.client.Status().Update(ctx, issue); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve: "+err.Error())
		return
	}

	// Add annotation for audit trail
	if issue.Annotations == nil {
		issue.Annotations = make(map[string]string)
	}
	issue.Annotations["aiops.chatcli.io/resolved-by"] = roleFromContext(ctx)
	issue.Annotations["aiops.chatcli.io/resolved-at"] = now.Format(time.RFC3339)
	issue.Annotations["aiops.chatcli.io/manual-resolution"] = "true"
	if err := s.client.Update(ctx, issue); err != nil {
		log.Printf("[REST] warning: failed to update annotations on resolved issue %s: %v", issue.Name, err)
	}

	// Invalidate dedup for the resource so new anomalies can be detected
	if s.watcherBridge != nil {
		res := issue.Spec.Resource
		s.watcherBridge.InvalidateDedupForResource(res.Name, res.Namespace)
	}

	item := issueToIncidentItem(*issue)
	writeSingleResponse(w, "Incident", item, item, resourceMetaFromIssue(*issue))
}

func (s *APIServer) handleGetIncidentTimeline(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	// Find the PostMortem for this issue.
	var postmortems v1alpha1.PostMortemList
	opts := []client.ListOption{}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(ctx, &postmortems, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list postmortems: "+err.Error())
		return
	}

	for _, pm := range postmortems.Items {
		if pm.Spec.IssueRef.Name == name {
			timeline := make([]TimelineItem, 0, len(pm.Status.Timeline))
			for _, te := range pm.Status.Timeline {
				timeline = append(timeline, TimelineItem{
					Timestamp: te.Timestamp.Format(time.RFC3339),
					Type:      te.Type,
					Detail:    te.Detail,
				})
			}

			writeJSON(w, http.StatusOK, APIResponse{
				APIVersion: "v1",
				Kind:       "IncidentTimeline",
				Metadata:   &ListMeta{TotalCount: len(timeline), Page: 1, PageSize: len(timeline)},
				Items:      timeline,
			})
			return
		}
	}

	writeError(w, http.StatusNotFound, "no timeline found for issue: "+name)
}

func (s *APIServer) handleGetIncidentRemediation(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	pp := parsePagination(r)

	var remediations v1alpha1.RemediationPlanList
	opts := []client.ListOption{}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(ctx, &remediations, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list remediations: "+err.Error())
		return
	}

	var matched []RemediationItem
	for _, rp := range remediations.Items {
		if rp.Spec.IssueRef.Name == name {
			matched = append(matched, remediationToItem(rp))
		}
	}

	total := len(matched)
	start, end := paginateSlice(total, pp)

	writeListResponse(w, "RemediationPlanList", matched[start:end], total, pp)
}

// ========== SLOs ==========

func (s *APIServer) routeSLOs(w http.ResponseWriter, r *http.Request, rest []string) {
	if !hasMinRole(roleFromContext(r.Context()), "viewer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		s.handleListSLOs(w, r)
	case len(rest) == 1 && r.Method == http.MethodGet:
		s.handleGetSLO(w, r, rest[0])
	case len(rest) == 2 && rest[1] == "budget" && r.Method == http.MethodGet:
		s.handleGetSLOBudget(w, r, rest[0])
	default:
		writeError(w, http.StatusNotFound, "unknown SLO endpoint")
	}
}

func (s *APIServer) handleListSLOs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)
	ns := r.URL.Query().Get("namespace")

	items, err := s.listUnstructured(ctx, "servicelevelobjectives", ns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list SLOs: "+err.Error())
		return
	}

	slos := make([]SLOItem, 0, len(items))
	for _, item := range items {
		slos = append(slos, unstructuredToSLO(item))
	}

	total := len(slos)
	start, end := paginateSlice(total, pp)

	writeListResponse(w, "ServiceLevelObjectiveList", slos[start:end], total, pp)
}

func (s *APIServer) handleGetSLO(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	obj, err := s.getUnstructured(ctx, "servicelevelobjectives", name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "SLO not found: "+err.Error())
		return
	}

	slo := unstructuredToSLO(obj.Object)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion:   "v1",
		Kind:         "ServiceLevelObjective",
		Spec:         slo,
		Status:       slo,
		ResourceMeta: unstructuredResourceMeta(obj),
	})
}

func (s *APIServer) handleGetSLOBudget(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	obj, err := s.getUnstructured(ctx, "servicelevelobjectives", name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "SLO not found: "+err.Error())
		return
	}

	slo := unstructuredToSLO(obj.Object)
	budget := SLOBudgetItem{
		Name:                 slo.Name,
		Target:               slo.Target,
		CurrentValue:         slo.CurrentValue,
		ErrorBudgetTotal:     slo.ErrorBudgetTotal,
		ErrorBudgetUsed:      slo.ErrorBudgetUsed,
		ErrorBudgetRemaining: slo.ErrorBudgetRemaining,
		Window:               slo.Window,
		State:                slo.State,
	}
	// Compute burn rate: used / total (normalized).
	if budget.ErrorBudgetTotal > 0 {
		budget.BurnRate = budget.ErrorBudgetUsed / budget.ErrorBudgetTotal
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "SLOBudget",
		Spec:       budget,
	})
}

// ========== Runbooks ==========

func (s *APIServer) routeRunbooks(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleListRunbooks(w, r)

	case len(rest) == 0 && r.Method == http.MethodPost:
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleCreateRunbook(w, r)

	case len(rest) == 1 && r.Method == http.MethodGet:
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleGetRunbook(w, r, rest[0])

	case len(rest) == 1 && r.Method == http.MethodPut:
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleUpdateRunbook(w, r, rest[0])

	case len(rest) == 1 && r.Method == http.MethodDelete:
		if !hasMinRole(roleFromContext(r.Context()), "admin") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleDeleteRunbook(w, r, rest[0])

	default:
		writeError(w, http.StatusNotFound, "unknown runbooks endpoint")
	}
}

func (s *APIServer) handleListRunbooks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)

	var runbooks v1alpha1.RunbookList
	opts := []client.ListOption{}
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(ctx, &runbooks, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runbooks: "+err.Error())
		return
	}

	items := make([]RunbookItem, 0, len(runbooks.Items))
	for _, rb := range runbooks.Items {
		items = append(items, runbookToItem(rb))
	}

	total := len(items)
	start, end := paginateSlice(total, pp)

	writeListResponse(w, "RunbookList", items[start:end], total, pp)
}

func (s *APIServer) handleGetRunbook(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var rb v1alpha1.Runbook
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &rb); err != nil {
		writeError(w, http.StatusNotFound, "runbook not found: "+err.Error())
		return
	}

	item := runbookToItem(rb)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "Runbook",
		Spec:       item,
		ResourceMeta: &ResourceMeta{
			Name:              rb.Name,
			Namespace:         rb.Namespace,
			UID:               string(rb.UID),
			CreationTimestamp: rb.CreationTimestamp.Format(time.RFC3339),
			Labels:            rb.Labels,
			Annotations:       rb.Annotations,
		},
	})
}

func (s *APIServer) handleCreateRunbook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req RunbookCreateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "default"
	}

	rb := &v1alpha1.Runbook{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
			Labels:    req.Labels,
		},
		Spec: v1alpha1.RunbookSpec{
			Description: req.Description,
			Trigger: v1alpha1.RunbookTrigger{
				SignalType:   v1alpha1.AnomalySignalType(req.Trigger.SignalType),
				Severity:     v1alpha1.IssueSeverity(req.Trigger.Severity),
				ResourceKind: req.Trigger.ResourceKind,
			},
			MaxAttempts: req.MaxAttempts,
		},
	}

	for _, step := range req.Steps {
		rb.Spec.Steps = append(rb.Spec.Steps, v1alpha1.RunbookStep{
			Name:        step.Name,
			Action:      step.Action,
			Description: step.Description,
			Params:      step.Params,
		})
	}

	if rb.Spec.MaxAttempts == 0 {
		rb.Spec.MaxAttempts = 3
	}

	if err := s.client.Create(ctx, rb); err != nil {
		writeError(w, http.StatusConflict, "failed to create runbook: "+err.Error())
		return
	}

	item := runbookToItem(*rb)
	writeJSON(w, http.StatusCreated, APIResponse{
		APIVersion: "v1",
		Kind:       "Runbook",
		Spec:       item,
		ResourceMeta: &ResourceMeta{
			Name:              rb.Name,
			Namespace:         rb.Namespace,
			UID:               string(rb.UID),
			CreationTimestamp: rb.CreationTimestamp.Format(time.RFC3339),
			Labels:            rb.Labels,
		},
	})
}

func (s *APIServer) handleUpdateRunbook(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req RunbookCreateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	var rb v1alpha1.Runbook
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &rb); err != nil {
		writeError(w, http.StatusNotFound, "runbook not found: "+err.Error())
		return
	}

	rb.Spec.Description = req.Description
	rb.Spec.Trigger = v1alpha1.RunbookTrigger{
		SignalType:   v1alpha1.AnomalySignalType(req.Trigger.SignalType),
		Severity:     v1alpha1.IssueSeverity(req.Trigger.Severity),
		ResourceKind: req.Trigger.ResourceKind,
	}
	rb.Spec.Steps = nil
	for _, step := range req.Steps {
		rb.Spec.Steps = append(rb.Spec.Steps, v1alpha1.RunbookStep{
			Name:        step.Name,
			Action:      step.Action,
			Description: step.Description,
			Params:      step.Params,
		})
	}
	if req.MaxAttempts > 0 {
		rb.Spec.MaxAttempts = req.MaxAttempts
	}
	if req.Labels != nil {
		rb.Labels = req.Labels
	}

	if err := s.client.Update(ctx, &rb); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update runbook: "+err.Error())
		return
	}

	item := runbookToItem(rb)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "Runbook",
		Spec:       item,
		ResourceMeta: &ResourceMeta{
			Name:              rb.Name,
			Namespace:         rb.Namespace,
			UID:               string(rb.UID),
			CreationTimestamp: rb.CreationTimestamp.Format(time.RFC3339),
			Labels:            rb.Labels,
		},
	})
}

func (s *APIServer) handleDeleteRunbook(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var rb v1alpha1.Runbook
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &rb); err != nil {
		writeError(w, http.StatusNotFound, "runbook not found: "+err.Error())
		return
	}

	if err := s.client.Delete(ctx, &rb); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete runbook: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"apiVersion": "v1",
		"kind":       "Status",
		"status":     "deleted",
		"name":       name,
	})
}

// ========== Approvals ==========

func (s *APIServer) routeApprovals(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleListApprovals(w, r)

	case len(rest) == 1 && r.Method == http.MethodGet:
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleGetApproval(w, r, rest[0])

	case len(rest) == 2 && rest[1] == "approve" && r.Method == http.MethodPost:
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleApprovalDecision(w, r, rest[0], "Approved")

	case len(rest) == 2 && rest[1] == "reject" && r.Method == http.MethodPost:
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleApprovalDecision(w, r, rest[0], "Rejected")

	default:
		writeError(w, http.StatusNotFound, "unknown approvals endpoint")
	}
}

func (s *APIServer) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)
	ns := r.URL.Query().Get("namespace")

	items, err := s.listUnstructured(ctx, "approvalrequests", ns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list approvals: "+err.Error())
		return
	}

	// Filter to pending if requested.
	stateFilter := r.URL.Query().Get("state")
	approvals := make([]ApprovalItem, 0, len(items))
	for _, item := range items {
		ai := unstructuredToApproval(item)
		if stateFilter != "" && ai.State != stateFilter {
			continue
		}
		approvals = append(approvals, ai)
	}

	total := len(approvals)
	start, end := paginateSlice(total, pp)

	writeListResponse(w, "ApprovalRequestList", approvals[start:end], total, pp)
}

func (s *APIServer) handleGetApproval(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	obj, err := s.getUnstructured(ctx, "approvalrequests", name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "approval not found: "+err.Error())
		return
	}

	ai := unstructuredToApproval(obj.Object)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion:   "v1",
		Kind:         "ApprovalRequest",
		Spec:         ai,
		Status:       ai,
		ResourceMeta: unstructuredResourceMeta(obj),
	})
}

func (s *APIServer) handleApprovalDecision(w http.ResponseWriter, r *http.Request, name, decision string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	var req ApprovalDecisionRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.Approver == "" {
		writeError(w, http.StatusBadRequest, "approver is required")
		return
	}

	obj, err := s.getUnstructured(ctx, "approvalrequests", name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "approval not found: "+err.Error())
		return
	}

	// Update the status fields.
	status, _ := obj.Object["status"].(map[string]interface{})
	if status == nil {
		status = make(map[string]interface{})
	}
	status["state"] = decision
	status["decidedAt"] = time.Now().Format(time.RFC3339)
	status["decisionReason"] = req.Reason

	if decision == "Approved" {
		status["approvedBy"] = req.Approver
	} else {
		status["rejectedBy"] = req.Approver
	}
	obj.Object["status"] = status

	if err := s.client.Status().Update(ctx, obj); err != nil {
		// If status subresource update fails, try regular update.
		if err2 := s.client.Update(ctx, obj); err2 != nil {
			writeError(w, http.StatusInternalServerError, "failed to update approval: "+err2.Error())
			return
		}
	}

	ai := unstructuredToApproval(obj.Object)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion:   "v1",
		Kind:         "ApprovalRequest",
		Spec:         ai,
		ResourceMeta: unstructuredResourceMeta(obj),
	})
}

// ========== PostMortems ==========

func (s *APIServer) routePostMortems(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleListPostMortems(w, r)

	case len(rest) == 1 && r.Method == http.MethodGet:
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleGetPostMortem(w, r, rest[0])

	case len(rest) == 2 && rest[1] == "review" && r.Method == http.MethodPost:
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handlePostMortemStateChange(w, r, rest[0], v1alpha1.PostMortemStateInReview)

	case len(rest) == 2 && rest[1] == "close" && r.Method == http.MethodPost:
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handlePostMortemStateChange(w, r, rest[0], v1alpha1.PostMortemStateClosed)

	case len(rest) == 2 && rest[1] == "feedback" && r.Method == http.MethodPost:
		if !hasMinRole(roleFromContext(r.Context()), "operator") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handlePostMortemFeedback(w, r, rest[0])

	default:
		writeError(w, http.StatusNotFound, "unknown postmortems endpoint")
	}
}

func (s *APIServer) handleListPostMortems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)

	var postmortems v1alpha1.PostMortemList
	opts := []client.ListOption{}
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(ctx, &postmortems, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list postmortems: "+err.Error())
		return
	}

	// Apply filters.
	severity := r.URL.Query().Get("severity")
	state := r.URL.Query().Get("state")
	tr := parseTimeRange(r)

	filtered := make([]v1alpha1.PostMortem, 0, len(postmortems.Items))
	for _, pm := range postmortems.Items {
		if severity != "" && string(pm.Spec.Severity) != severity {
			continue
		}
		if state != "" && string(pm.Status.State) != state {
			continue
		}
		if !inTimeRange(pm.CreationTimestamp.Time, tr) {
			continue
		}
		filtered = append(filtered, pm)
	}

	total := len(filtered)
	start, end := paginateSlice(total, pp)
	page := filtered[start:end]

	items := make([]PostMortemItem, 0, len(page))
	for _, pm := range page {
		items = append(items, postmortemToItem(pm))
	}

	writeListResponse(w, "PostMortemList", items, total, pp)
}

func (s *APIServer) handleGetPostMortem(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var pm v1alpha1.PostMortem
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &pm); err != nil {
		writeError(w, http.StatusNotFound, "postmortem not found: "+err.Error())
		return
	}

	item := postmortemToItem(pm)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "PostMortem",
		Spec:       item,
		Status:     item,
		ResourceMeta: &ResourceMeta{
			Name:              pm.Name,
			Namespace:         pm.Namespace,
			UID:               string(pm.UID),
			CreationTimestamp: pm.CreationTimestamp.Format(time.RFC3339),
			Labels:            pm.Labels,
			Annotations:       pm.Annotations,
		},
	})
}

func (s *APIServer) handlePostMortemStateChange(w http.ResponseWriter, r *http.Request, name string, targetState v1alpha1.PostMortemState) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var reqBody ReviewRequest
	// Body is optional for state changes.
	_ = readJSON(r, &reqBody)

	var pm v1alpha1.PostMortem
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &pm); err != nil {
		writeError(w, http.StatusNotFound, "postmortem not found: "+err.Error())
		return
	}

	// Add reviewer annotation if provided (metadata update first).
	if reqBody.Reviewer != "" {
		if pm.Annotations == nil {
			pm.Annotations = make(map[string]string)
		}
		pm.Annotations["aiops.chatcli.io/reviewed-by"] = reqBody.Reviewer
		if reqBody.Notes != "" {
			pm.Annotations["aiops.chatcli.io/review-notes"] = reqBody.Notes
		}
		if err := s.client.Update(ctx, &pm); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update postmortem metadata: "+err.Error())
			return
		}
		// Re-fetch to get the latest resourceVersion after metadata update.
		if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &pm); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to re-fetch postmortem: "+err.Error())
			return
		}
	}

	// Now update status with correct resourceVersion.
	pm.Status.State = targetState
	now := metav1.Now()
	if targetState == v1alpha1.PostMortemStateInReview || targetState == v1alpha1.PostMortemStateClosed {
		pm.Status.ReviewedAt = &now
	}

	if err := s.client.Status().Update(ctx, &pm); err != nil {
		if apierrors.IsConflict(err) {
			writeError(w, http.StatusConflict, "postmortem was modified concurrently, please retry")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update postmortem status: "+err.Error())
		return
	}

	item := postmortemToItem(pm)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "PostMortem",
		Spec:       item,
		Status:     item,
		ResourceMeta: &ResourceMeta{
			Name:              pm.Name,
			Namespace:         pm.Namespace,
			UID:               string(pm.UID),
			CreationTimestamp: pm.CreationTimestamp.Format(time.RFC3339),
			Labels:            pm.Labels,
			Annotations:       pm.Annotations,
		},
	})
}

func (s *APIServer) handlePostMortemFeedback(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var reqBody FeedbackRequest
	if err := readJSON(r, &reqBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if reqBody.ProvidedBy == "" {
		writeError(w, http.StatusBadRequest, "providedBy is required")
		return
	}
	if len(reqBody.ProvidedBy) > 253 {
		writeError(w, http.StatusBadRequest, "providedBy must be at most 253 characters")
		return
	}
	if reqBody.RemediationAccuracy < 1 || reqBody.RemediationAccuracy > 5 {
		writeError(w, http.StatusBadRequest, "remediationAccuracy must be between 1 and 5")
		return
	}
	if len(reqBody.OverrideRootCause) > 4096 {
		writeError(w, http.StatusBadRequest, "overrideRootCause must be at most 4096 characters")
		return
	}
	if len(reqBody.Comments) > 4096 {
		writeError(w, http.StatusBadRequest, "comments must be at most 4096 characters")
		return
	}

	var pm v1alpha1.PostMortem
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &pm); err != nil {
		writeError(w, http.StatusNotFound, "postmortem not found: "+err.Error())
		return
	}

	now := metav1.Now()
	pm.Status.Feedback = &v1alpha1.DevFeedback{
		OverrideRootCause:   reqBody.OverrideRootCause,
		RemediationAccuracy: reqBody.RemediationAccuracy,
		Comments:            reqBody.Comments,
		ProvidedBy:          reqBody.ProvidedBy,
		ProvidedAt:          &now,
	}

	if err := s.client.Status().Update(ctx, &pm); err != nil {
		if apierrors.IsConflict(err) {
			writeError(w, http.StatusConflict, "postmortem was modified concurrently, please retry")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update postmortem feedback: "+err.Error())
		return
	}

	item := postmortemToItem(pm)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "PostMortem",
		Spec:       item,
		Status:     item,
		ResourceMeta: &ResourceMeta{
			Name:              pm.Name,
			Namespace:         pm.Namespace,
			UID:               string(pm.UID),
			CreationTimestamp: pm.CreationTimestamp.Format(time.RFC3339),
			Labels:            pm.Labels,
			Annotations:       pm.Annotations,
		},
	})
}

// ========== Analytics ==========

func (s *APIServer) routeAnalytics(w http.ResponseWriter, r *http.Request, rest []string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "analytics endpoints only support GET")
		return
	}
	if !hasMinRole(roleFromContext(r.Context()), "viewer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	if len(rest) == 0 {
		writeError(w, http.StatusNotFound, "specify an analytics endpoint")
		return
	}

	tr := parseTimeRange(r)

	switch rest[0] {
	case "summary":
		s.handleAnalyticsSummary(w, r)
	case "mttd":
		s.handleAnalyticsMTTD(w, r, tr)
	case "mttr":
		s.handleAnalyticsMTTR(w, r, tr)
	case "trends":
		s.handleAnalyticsTrends(w, r, tr)
	case "top-resources":
		s.handleAnalyticsTopResources(w, r, tr)
	case "remediation-stats":
		s.handleAnalyticsRemediationStats(w, r, tr)
	case "compliance":
		s.handleAnalyticsCompliance(w, r, tr)
	case "capacity":
		s.handleAnalyticsCapacity(w, r, tr)
	default:
		writeError(w, http.StatusNotFound, "unknown analytics endpoint: "+rest[0])
	}
}

func (s *APIServer) handleAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.computeSummary(r.Context(), parseTimeRange(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute summary: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "AnalyticsSummary",
		Spec:       summary,
	})
}

func (s *APIServer) handleAnalyticsMTTD(w http.ResponseWriter, r *http.Request, tr timeRangeParams) {
	metrics, err := s.computeMTTD(r.Context(), tr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute MTTD: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "MTTDMetrics",
		Metadata:   &ListMeta{TotalCount: len(metrics), Page: 1, PageSize: len(metrics)},
		Items:      metrics,
	})
}

func (s *APIServer) handleAnalyticsMTTR(w http.ResponseWriter, r *http.Request, tr timeRangeParams) {
	metrics, err := s.computeMTTR(r.Context(), tr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute MTTR: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "MTTRMetrics",
		Metadata:   &ListMeta{TotalCount: len(metrics), Page: 1, PageSize: len(metrics)},
		Items:      metrics,
	})
}

func (s *APIServer) handleAnalyticsTrends(w http.ResponseWriter, r *http.Request, tr timeRangeParams) {
	groupBy := r.URL.Query().Get("groupBy")
	trends, err := s.computeTrends(r.Context(), tr, groupBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute trends: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "IssueTrends",
		Metadata:   &ListMeta{TotalCount: len(trends), Page: 1, PageSize: len(trends)},
		Items:      trends,
	})
}

func (s *APIServer) handleAnalyticsTopResources(w http.ResponseWriter, r *http.Request, tr timeRangeParams) {
	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	resources, err := s.computeTopResources(r.Context(), tr, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute top resources: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "TopResources",
		Metadata:   &ListMeta{TotalCount: len(resources), Page: 1, PageSize: len(resources)},
		Items:      resources,
	})
}

func (s *APIServer) handleAnalyticsRemediationStats(w http.ResponseWriter, r *http.Request, tr timeRangeParams) {
	stats, err := s.computeRemediationStats(r.Context(), tr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute remediation stats: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "RemediationStats",
		Metadata:   &ListMeta{TotalCount: len(stats), Page: 1, PageSize: len(stats)},
		Items:      stats,
	})
}

func (s *APIServer) handleAnalyticsCompliance(w http.ResponseWriter, r *http.Request, tr timeRangeParams) {
	reporter := controllers.NewComplianceReporter(s.client)
	ns := r.URL.Query().Get("namespace")
	window := 7 * 24 * time.Hour // default 7 days
	if tr.From != nil && tr.To != nil {
		window = tr.To.Sub(*tr.From)
	}
	report, err := reporter.GenerateReport(r.Context(), ns, window)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate compliance report: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "ComplianceReport",
		Spec:       report,
	})
}

func (s *APIServer) handleAnalyticsCapacity(w http.ResponseWriter, r *http.Request, tr timeRangeParams) {
	ctx := r.Context()
	planner := controllers.NewCapacityPlanner(s.client)
	ns := r.URL.Query().Get("namespace")
	window := 7 * 24 * time.Hour
	if tr.From != nil && tr.To != nil {
		window = tr.To.Sub(*tr.From)
	}

	// Collect unique resources from active/recent issues
	var issues v1alpha1.IssueList
	opts := []client.ListOption{}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(ctx, &issues, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issues for capacity analysis: "+err.Error())
		return
	}

	seen := make(map[string]bool)
	var forecasts []interface{}
	for _, iss := range issues.Items {
		key := fmt.Sprintf("%s/%s/%s", iss.Spec.Resource.Kind, iss.Spec.Resource.Namespace, iss.Spec.Resource.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		forecast, err := planner.AnalyzeResourceTrends(ctx, iss.Spec.Resource, window)
		if err != nil {
			continue
		}
		forecasts = append(forecasts, forecast)
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "CapacityForecastList",
		Metadata:   &ListMeta{TotalCount: len(forecasts), Page: 1, PageSize: len(forecasts)},
		Items:      forecasts,
	})
}

// ========== Clusters ==========

func (s *APIServer) routeClusters(w http.ResponseWriter, r *http.Request, rest []string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "clusters endpoints only support GET")
		return
	}
	if !hasMinRole(roleFromContext(r.Context()), "viewer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	switch {
	case len(rest) == 0:
		s.handleListClusters(w, r)
	case len(rest) == 1 && rest[0] == "global-status":
		s.handleGlobalClusterStatus(w, r)
	case len(rest) == 1:
		s.handleGetCluster(w, r, rest[0])
	default:
		writeError(w, http.StatusNotFound, "unknown clusters endpoint")
	}
}

func (s *APIServer) handleListClusters(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)
	ns := r.URL.Query().Get("namespace")

	items, err := s.listUnstructured(ctx, "clusterregistrations", ns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list clusters: "+err.Error())
		return
	}

	clusters := make([]ClusterItem, 0, len(items))
	for _, item := range items {
		clusters = append(clusters, unstructuredToCluster(item))
	}

	total := len(clusters)
	start, end := paginateSlice(total, pp)

	writeListResponse(w, "ClusterRegistrationList", clusters[start:end], total, pp)
}

func (s *APIServer) handleGetCluster(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	obj, err := s.getUnstructured(ctx, "clusterregistrations", name, ns)
	if err != nil {
		writeError(w, http.StatusNotFound, "cluster not found: "+err.Error())
		return
	}

	cluster := unstructuredToCluster(obj.Object)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion:   "v1",
		Kind:         "ClusterRegistration",
		Spec:         cluster,
		Status:       cluster,
		ResourceMeta: unstructuredResourceMeta(obj),
	})
}

func (s *APIServer) handleGlobalClusterStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	items, err := s.listUnstructured(ctx, "clusterregistrations", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list clusters: "+err.Error())
		return
	}

	status := GlobalClusterStatus{
		TotalClusters: len(items),
	}

	for _, item := range items {
		cluster := unstructuredToCluster(item)
		status.Clusters = append(status.Clusters, cluster)

		if cluster.Connected {
			status.HealthyClusters++
		} else {
			status.OfflineClusters++
		}
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "GlobalClusterStatus",
		Spec:       status,
	})
}

// ========== Audit ==========

func (s *APIServer) routeAudit(w http.ResponseWriter, r *http.Request, rest []string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "audit endpoints only support GET")
		return
	}
	if !hasMinRole(roleFromContext(r.Context()), "viewer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	switch {
	case len(rest) == 0:
		s.handleListAuditEvents(w, r)
	case len(rest) == 1 && rest[0] == "export":
		s.handleExportAuditEvents(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown audit endpoint")
	}
}

func (s *APIServer) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)
	ns := r.URL.Query().Get("namespace")

	items, err := s.listUnstructured(ctx, "auditevents", ns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list audit events: "+err.Error())
		return
	}

	// Apply filters.
	typeFilter := r.URL.Query().Get("type")
	severityFilter := r.URL.Query().Get("severity")
	resourceFilter := r.URL.Query().Get("resource")
	tr := parseTimeRange(r)

	events := make([]AuditEventItem, 0, len(items))
	for _, item := range items {
		ae := unstructuredToAuditEvent(item)
		if typeFilter != "" && ae.EventType != typeFilter {
			continue
		}
		if severityFilter != "" && ae.Severity != severityFilter {
			continue
		}
		if resourceFilter != "" && ae.ResourceName != resourceFilter {
			continue
		}
		if ae.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, ae.Timestamp); err == nil {
				if !inTimeRange(t, tr) {
					continue
				}
			}
		} else if ae.CreationTimestamp != "" {
			if t, err := time.Parse(time.RFC3339, ae.CreationTimestamp); err == nil {
				if !inTimeRange(t, tr) {
					continue
				}
			}
		}
		events = append(events, ae)
	}

	total := len(events)
	start, end := paginateSlice(total, pp)

	writeListResponse(w, "AuditEventList", events[start:end], total, pp)
}

func (s *APIServer) handleExportAuditEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")

	items, err := s.listUnstructured(ctx, "auditevents", ns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list audit events: "+err.Error())
		return
	}

	// Apply same filters.
	typeFilter := r.URL.Query().Get("type")
	severityFilter := r.URL.Query().Get("severity")
	resourceFilter := r.URL.Query().Get("resource")
	tr := parseTimeRange(r)

	events := make([]AuditEventItem, 0, len(items))
	for _, item := range items {
		ae := unstructuredToAuditEvent(item)
		if typeFilter != "" && ae.EventType != typeFilter {
			continue
		}
		if severityFilter != "" && ae.Severity != severityFilter {
			continue
		}
		if resourceFilter != "" && ae.ResourceName != resourceFilter {
			continue
		}
		if ae.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, ae.Timestamp); err == nil {
				if !inTimeRange(t, tr) {
					continue
				}
			}
		} else if ae.CreationTimestamp != "" {
			if t, err := time.Parse(time.RFC3339, ae.CreationTimestamp); err == nil {
				if !inTimeRange(t, tr) {
					continue
				}
			}
		}
		events = append(events, ae)
	}

	// Export as JSON for SIEM integration (download).
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=audit-events-%s.json",
		time.Now().Format("20060102-150405")))
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "AuditEventExport",
		"exportedAt": time.Now().Format(time.RFC3339),
		"totalCount": len(events),
		"items":      events,
	})
}

// ========== Health ==========

func (s *APIServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().Format(time.RFC3339),
	})
}

func (s *APIServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// Check if we can reach the K8s API by listing namespaces.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues, client.Limit(1)); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, HealthResponse{
			Status:    "not ready: " + err.Error(),
			Timestamp: time.Now().Format(time.RFC3339),
		})
		return
	}

	writeJSON(w, http.StatusOK, HealthResponse{
		Status:    "ready",
		Timestamp: time.Now().Format(time.RFC3339),
	})
}

// ========== K8s client helpers ==========

// getIssue retrieves an Issue by name. If namespace is empty, searches all namespaces.
func (s *APIServer) getIssue(ctx context.Context, name, namespace string) (*v1alpha1.Issue, error) {
	if namespace != "" {
		var issue v1alpha1.Issue
		if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &issue); err != nil {
			return nil, err
		}
		return &issue, nil
	}

	// Search all namespaces.
	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		return nil, err
	}
	for i := range issues.Items {
		if issues.Items[i].Name == name {
			return &issues.Items[i], nil
		}
	}
	return nil, fmt.Errorf("issue %q not found in any namespace", name)
}

// getUnstructured retrieves a single unstructured resource.
func (s *APIServer) getUnstructured(ctx context.Context, plural, name, namespace string) (*unstructured.Unstructured, error) {
	if namespace == "" {
		// Search all namespaces.
		items, err := s.listUnstructured(ctx, plural, "")
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			meta, _ := item["metadata"].(map[string]interface{})
			if meta != nil {
				n, _ := meta["name"].(string)
				if n == name {
					obj := &unstructured.Unstructured{Object: item}
					obj.SetGroupVersionKind(singleGVK(plural))
					return obj, nil
				}
			}
		}
		return nil, fmt.Errorf("%s %q not found in any namespace", plural, name)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(singleGVK(plural))
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// ========== Conversion helpers ==========

func issueToIncidentItem(iss v1alpha1.Issue) IncidentItem {
	item := IncidentItem{
		Name:       iss.Name,
		Namespace:  iss.Namespace,
		Severity:   string(iss.Spec.Severity),
		Source:     string(iss.Spec.Source),
		SignalType: iss.Spec.SignalType,
		Resource: ResourceRefItem{
			Kind:      iss.Spec.Resource.Kind,
			Name:      iss.Spec.Resource.Name,
			Namespace: iss.Spec.Resource.Namespace,
		},
		Description:            iss.Spec.Description,
		RiskScore:              iss.Spec.RiskScore,
		State:                  string(iss.Status.State),
		Resolution:             iss.Status.Resolution,
		RemediationAttempts:    iss.Status.RemediationAttempts,
		MaxRemediationAttempts: iss.Status.MaxRemediationAttempts,
		CreationTimestamp:      iss.CreationTimestamp.Format(time.RFC3339),
		Labels:                 iss.Labels,
		Annotations:            iss.Annotations,
	}
	if iss.Status.DetectedAt != nil {
		t := iss.Status.DetectedAt.Format(time.RFC3339)
		item.DetectedAt = &t
	}
	if iss.Status.ResolvedAt != nil {
		t := iss.Status.ResolvedAt.Format(time.RFC3339)
		item.ResolvedAt = &t
	}
	return item
}

func resourceMetaFromIssue(iss v1alpha1.Issue) *ResourceMeta {
	return &ResourceMeta{
		Name:              iss.Name,
		Namespace:         iss.Namespace,
		UID:               string(iss.UID),
		CreationTimestamp: iss.CreationTimestamp.Format(time.RFC3339),
		Labels:            iss.Labels,
		Annotations:       iss.Annotations,
	}
}

func remediationToItem(rp v1alpha1.RemediationPlan) RemediationItem {
	item := RemediationItem{
		Name:              rp.Name,
		Namespace:         rp.Namespace,
		IssueRef:          rp.Spec.IssueRef.Name,
		Attempt:           rp.Spec.Attempt,
		Strategy:          rp.Spec.Strategy,
		State:             string(rp.Status.State),
		Result:            rp.Status.Result,
		AgenticMode:       rp.Spec.AgenticMode,
		AgenticStepCount:  rp.Status.AgenticStepCount,
		CreationTimestamp: rp.CreationTimestamp.Format(time.RFC3339),
	}
	if rp.Status.StartedAt != nil {
		t := rp.Status.StartedAt.Format(time.RFC3339)
		item.StartedAt = &t
	}
	if rp.Status.CompletedAt != nil {
		t := rp.Status.CompletedAt.Format(time.RFC3339)
		item.CompletedAt = &t
	}
	for _, a := range rp.Spec.Actions {
		item.Actions = append(item.Actions, ActionItem{
			Type:   string(a.Type),
			Params: a.Params,
		})
	}
	return item
}

func postmortemToItem(pm v1alpha1.PostMortem) PostMortemItem {
	item := PostMortemItem{
		Name:      pm.Name,
		Namespace: pm.Namespace,
		IssueRef:  pm.Spec.IssueRef.Name,
		Resource: ResourceRefItem{
			Kind:      pm.Spec.Resource.Kind,
			Name:      pm.Spec.Resource.Name,
			Namespace: pm.Spec.Resource.Namespace,
		},
		Severity:          string(pm.Spec.Severity),
		State:             string(pm.Status.State),
		Summary:           pm.Status.Summary,
		RootCause:         pm.Status.RootCause,
		Impact:            pm.Status.Impact,
		Duration:          pm.Status.Duration,
		LessonsLearned:    pm.Status.LessonsLearned,
		PreventionActions: pm.Status.PreventionActions,
		CreationTimestamp: pm.CreationTimestamp.Format(time.RFC3339),
	}
	if pm.Status.Feedback != nil {
		fb := &DevFeedbackItem{
			OverrideRootCause:   pm.Status.Feedback.OverrideRootCause,
			RemediationAccuracy: pm.Status.Feedback.RemediationAccuracy,
			Comments:            pm.Status.Feedback.Comments,
			ProvidedBy:          pm.Status.Feedback.ProvidedBy,
		}
		if pm.Status.Feedback.ProvidedAt != nil {
			t := pm.Status.Feedback.ProvidedAt.Format(time.RFC3339)
			fb.ProvidedAt = &t
		}
		item.Feedback = fb
	}
	if pm.Status.GeneratedAt != nil {
		t := pm.Status.GeneratedAt.Format(time.RFC3339)
		item.GeneratedAt = &t
	}
	if pm.Status.ReviewedAt != nil {
		t := pm.Status.ReviewedAt.Format(time.RFC3339)
		item.ReviewedAt = &t
	}
	for _, te := range pm.Status.Timeline {
		item.Timeline = append(item.Timeline, TimelineItem{
			Timestamp: te.Timestamp.Format(time.RFC3339),
			Type:      te.Type,
			Detail:    te.Detail,
		})
	}
	for _, ar := range pm.Status.ActionsExecuted {
		item.ActionsExecuted = append(item.ActionsExecuted, ActionRecordItem{
			Action:    ar.Action,
			Params:    ar.Params,
			Result:    ar.Result,
			Detail:    ar.Detail,
			Timestamp: ar.Timestamp.Format(time.RFC3339),
		})
	}
	return item
}

func runbookToItem(rb v1alpha1.Runbook) RunbookItem {
	item := RunbookItem{
		Name:        rb.Name,
		Namespace:   rb.Namespace,
		Description: rb.Spec.Description,
		Trigger: RunbookTriggerItem{
			SignalType:   string(rb.Spec.Trigger.SignalType),
			Severity:     string(rb.Spec.Trigger.Severity),
			ResourceKind: rb.Spec.Trigger.ResourceKind,
		},
		MaxAttempts:       rb.Spec.MaxAttempts,
		CreationTimestamp: rb.CreationTimestamp.Format(time.RFC3339),
	}
	for _, s := range rb.Spec.Steps {
		item.Steps = append(item.Steps, RunbookStepItem{
			Name:        s.Name,
			Action:      s.Action,
			Description: s.Description,
			Params:      s.Params,
		})
	}
	return item
}

// Unstructured conversion helpers for CRDs that may not have Go types yet.

func unstructuredToSLO(obj map[string]interface{}) SLOItem {
	meta := extractMeta(obj)
	spec, _ := obj["spec"].(map[string]interface{})
	status, _ := obj["status"].(map[string]interface{})

	slo := SLOItem{
		Name:              meta.name,
		Namespace:         meta.namespace,
		CreationTimestamp: meta.creationTimestamp,
	}

	if spec != nil {
		slo.Service, _ = spec["serviceName"].(string)

		// indicator.type → SLI
		if indicator, ok := spec["indicator"].(map[string]interface{}); ok {
			slo.SLI, _ = indicator["type"].(string)
		}

		// target.percentage and target.window (nested)
		if target, ok := spec["target"].(map[string]interface{}); ok {
			slo.Target = toFloat64(target["percentage"])
			slo.Window, _ = target["window"].(string)
		}
	}

	if status != nil {
		// currentValue is a fraction (0.0-1.0) — convert to percentage for display
		slo.CurrentValue = toFloat64(status["currentValue"]) * 100

		slo.ErrorBudgetTotal = toFloat64(status["errorBudgetTotal"])
		slo.ErrorBudgetUsed = toFloat64(status["errorBudgetUsed"])

		// errorBudgetRemaining is a fraction (0.0-1.0) — convert to percentage
		slo.ErrorBudgetRemaining = toFloat64(status["errorBudgetRemaining"]) * 100

		slo.State, _ = status["state"].(string)
	}

	return slo
}

func unstructuredToApproval(obj map[string]interface{}) ApprovalItem {
	meta := extractMeta(obj)
	spec, _ := obj["spec"].(map[string]interface{})
	status, _ := obj["status"].(map[string]interface{})

	ai := ApprovalItem{
		Name:              meta.name,
		Namespace:         meta.namespace,
		CreationTimestamp: meta.creationTimestamp,
		Labels:            meta.labels,
	}

	if spec != nil {
		ai.RequestedBy, _ = spec["requester"].(string)
		ai.Reason, _ = spec["policyRef"].(string)

		// Extract resource from issueRef
		if issueRef, ok := spec["issueRef"].(map[string]interface{}); ok {
			ai.Resource, _ = issueRef["name"].(string)
		}

		// Extract action from first requestedAction
		if actions, ok := spec["requestedActions"].([]interface{}); ok && len(actions) > 0 {
			if action, ok := actions[0].(map[string]interface{}); ok {
				ai.Action, _ = action["type"].(string)
			}
		}
	}

	if status != nil {
		ai.State, _ = status["state"].(string)
		ai.ApprovedBy, _ = status["approvedBy"].(string)
		ai.RejectedBy, _ = status["rejectedBy"].(string)
		ai.DecisionReason, _ = status["decisionReason"].(string)
		if da, ok := status["decidedAt"].(string); ok {
			ai.DecidedAt = &da
		}
	}
	if ai.State == "" {
		ai.State = "Pending"
	}

	return ai
}

func unstructuredToCluster(obj map[string]interface{}) ClusterItem {
	meta := extractMeta(obj)
	spec, _ := obj["spec"].(map[string]interface{})
	status, _ := obj["status"].(map[string]interface{})

	cluster := ClusterItem{
		Name:              meta.name,
		Namespace:         meta.namespace,
		CreationTimestamp: meta.creationTimestamp,
		Labels:            meta.labels,
	}

	if spec != nil {
		cluster.DisplayName, _ = spec["displayName"].(string)
		cluster.Region, _ = spec["region"].(string)
		cluster.Environment, _ = spec["environment"].(string)
		cluster.Tier, _ = spec["tier"].(string)
	}

	if status != nil {
		cluster.Connected, _ = status["connected"].(bool)
		cluster.Version, _ = status["kubernetesVersion"].(string)
		cluster.NodeCount = toInt64(status["nodeCount"])
		cluster.NamespaceCount = toInt64(status["namespaceCount"])
		cluster.ActiveIssues = toInt64(status["activeIssues"])
		cluster.ActiveRemediations = toInt64(status["activeRemediations"])
		if lh, ok := status["lastHealthCheck"].(string); ok {
			cluster.LastHealthCheck = &lh
		}
	}

	return cluster
}

func unstructuredToAuditEvent(obj map[string]interface{}) AuditEventItem {
	meta := extractMeta(obj)
	spec, _ := obj["spec"].(map[string]interface{})

	ae := AuditEventItem{
		Name:              meta.name,
		Namespace:         meta.namespace,
		CreationTimestamp: meta.creationTimestamp,
	}

	if spec != nil {
		ae.EventType, _ = spec["eventType"].(string)
		ae.Severity, _ = spec["severity"].(string)
		ae.CorrelationID, _ = spec["correlationId"].(string)
		ae.Timestamp, _ = spec["timestamp"].(string)

		// actor is a nested struct {type, name, controller}
		if actor, ok := spec["actor"].(map[string]interface{}); ok {
			ae.ActorType, _ = actor["type"].(string)
			ae.ActorName, _ = actor["name"].(string)
		}

		// resource is a nested struct {kind, name, namespace}
		if resource, ok := spec["resource"].(map[string]interface{}); ok {
			ae.ResourceKind, _ = resource["kind"].(string)
			ae.ResourceName, _ = resource["name"].(string)
			ae.ResourceNamespace, _ = resource["namespace"].(string)
		}

		// details is a map — flatten first entry as detail for display
		if details, ok := spec["details"].(map[string]interface{}); ok {
			var parts []string
			for k, v := range details {
				parts = append(parts, k+"="+fmt.Sprintf("%v", v))
			}
			if len(parts) > 0 {
				ae.Detail = strings.Join(parts, "; ")
			}
		}
	}

	if ae.Timestamp == "" {
		ae.Timestamp = meta.creationTimestamp
	}

	return ae
}

func unstructuredResourceMeta(obj *unstructured.Unstructured) *ResourceMeta {
	return &ResourceMeta{
		Name:              obj.GetName(),
		Namespace:         obj.GetNamespace(),
		UID:               string(obj.GetUID()),
		CreationTimestamp: obj.GetCreationTimestamp().Format(time.RFC3339),
		Labels:            obj.GetLabels(),
		Annotations:       obj.GetAnnotations(),
	}
}

type metaFields struct {
	name              string
	namespace         string
	creationTimestamp string
	labels            map[string]string
}

func extractMeta(obj map[string]interface{}) metaFields {
	meta, _ := obj["metadata"].(map[string]interface{})
	m := metaFields{}
	if meta != nil {
		m.name, _ = meta["name"].(string)
		m.namespace, _ = meta["namespace"].(string)
		m.creationTimestamp, _ = meta["creationTimestamp"].(string)
		if lbls, ok := meta["labels"].(map[string]interface{}); ok {
			m.labels = make(map[string]string, len(lbls))
			for k, v := range lbls {
				m.labels[k], _ = v.(string)
			}
		}
	}
	return m
}

func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int64:
		return float64(val)
	case int32:
		return float64(val)
	case int:
		return float64(val)
	case json.Number:
		f, _ := val.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	}
	return 0
}

func toInt64(v interface{}) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case int32:
		return int64(val)
	case int:
		return int64(val)
	case float64:
		return int64(val)
	case float32:
		return int64(val)
	case json.Number:
		i, _ := val.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(val, 10, 64)
		return i
	}
	return 0
}

// ========== HTTP helpers ==========

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		log.Printf("[REST] failed to encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, ErrorResponse{
		APIVersion: "v1",
		Kind:       "Error",
		Error:      http.StatusText(code),
		Code:       code,
		Message:    message,
	})
}

func writeListResponse(w http.ResponseWriter, kind string, items interface{}, total int, pp paginationParams) {
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       kind,
		Metadata: &ListMeta{
			TotalCount: total,
			Page:       pp.Page,
			PageSize:   pp.PageSize,
		},
		Items: items,
	})
}

func writeSingleResponse(w http.ResponseWriter, kind string, spec, status interface{}, meta *ResourceMeta) {
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion:   "v1",
		Kind:         kind,
		Spec:         spec,
		Status:       status,
		ResourceMeta: meta,
	})
}

func readJSON(r *http.Request, v interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		return err
	}
	defer r.Body.Close()

	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

func parsePagination(r *http.Request) paginationParams {
	pp := paginationParams{
		Page:     1,
		PageSize: 20,
	}
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			pp.Page = n
		}
	}
	if ps := r.URL.Query().Get("pageSize"); ps != "" {
		if n, err := strconv.Atoi(ps); err == nil && n > 0 {
			if n > 100 {
				n = 100
			}
			pp.PageSize = n
		}
	}
	return pp
}

func parseTimeRange(r *http.Request) timeRangeParams {
	tr := timeRangeParams{}
	if from := r.URL.Query().Get("from"); from != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			tr.From = &t
		}
	}
	if to := r.URL.Query().Get("to"); to != "" {
		if t, err := time.Parse(time.RFC3339, to); err == nil {
			tr.To = &t
		}
	}
	return tr
}

// paginateSlice returns start and end indices for a slice of the given total length.
func paginateSlice(total int, pp paginationParams) (start, end int) {
	start = (pp.Page - 1) * pp.PageSize
	if start > total {
		start = total
	}
	end = start + pp.PageSize
	if end > total {
		end = total
	}
	return start, end
}

// ========== Federation ==========

func (s *APIServer) routeFederation(w http.ResponseWriter, r *http.Request, rest []string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "federation endpoints only support GET")
		return
	}
	if !hasMinRole(roleFromContext(r.Context()), "viewer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	switch {
	case len(rest) == 0 || rest[0] == "status":
		s.handleFederationStatus(w, r)
	case rest[0] == "clusters":
		s.handleFederationClusters(w, r)
	case rest[0] == "correlations":
		s.handleFederationCorrelations(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown federation endpoint: "+rest[0])
	}
}

func (s *APIServer) handleFederationStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	clusterItems, err := s.listUnstructured(ctx, "clusterregistrations", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list clusters: "+err.Error())
		return
	}

	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issues: "+err.Error())
		return
	}

	connected, disconnected := 0, 0
	for _, c := range clusterItems {
		status, _ := c["status"].(map[string]interface{})
		if status != nil {
			if conn, _ := status["connected"].(bool); conn {
				connected++
			} else {
				disconnected++
			}
		} else {
			disconnected++
		}
	}

	activeIssues := 0
	for _, iss := range issues.Items {
		if iss.Status.State != v1alpha1.IssueStateResolved &&
			iss.Status.State != v1alpha1.IssueStateFailed {
			activeIssues++
		}
	}

	status := map[string]interface{}{
		"totalClusters":        len(clusterItems),
		"connectedClusters":    connected,
		"disconnectedClusters": disconnected,
		"totalActiveIssues":    activeIssues,
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "FederationStatus",
		Spec:       status,
	})
}

func (s *APIServer) handleFederationClusters(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tier := r.URL.Query().Get("tier")

	items, err := s.listUnstructured(ctx, "clusterregistrations", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list clusters: "+err.Error())
		return
	}

	var clusters []ClusterItem
	for _, item := range items {
		ci := unstructuredToCluster(item)
		if tier != "" && ci.Tier != tier {
			continue
		}
		clusters = append(clusters, ci)
	}

	writeListResponse(w, "ClusterRegistrationList", clusters, len(clusters), paginationParams{Page: 1, PageSize: len(clusters)})
}

func (s *APIServer) handleFederationCorrelations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issues: "+err.Error())
		return
	}

	var correlations []map[string]interface{}
	seen := make(map[string]bool)
	for _, iss := range issues.Items {
		if iss.Annotations == nil {
			continue
		}
		corrID := iss.Annotations["platform.chatcli.io/correlation-id"]
		if corrID == "" || seen[corrID] {
			continue
		}
		seen[corrID] = true

		correlations = append(correlations, map[string]interface{}{
			"correlationId":      corrID,
			"issue":              iss.Name,
			"namespace":          iss.Namespace,
			"severity":           string(iss.Spec.Severity),
			"signalType":         iss.Spec.SignalType,
			"correlatedClusters": iss.Annotations["platform.chatcli.io/correlated-clusters"],
			"elevated":           iss.Annotations["platform.chatcli.io/elevated-severity"] == "true",
			"cascade":            iss.Annotations["platform.chatcli.io/cascade-detected"] == "true",
		})
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "FederationCorrelations",
		Metadata:   &ListMeta{TotalCount: len(correlations), Page: 1, PageSize: len(correlations)},
		Items:      correlations,
	})
}

// ========== AI Insights ==========

func (s *APIServer) routeAIInsights(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		// GET /api/v1/aiinsights
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleListAIInsights(w, r)

	case len(rest) == 1 && r.Method == http.MethodGet:
		// GET /api/v1/aiinsights/:name
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleGetAIInsight(w, r, rest[0])

	default:
		writeError(w, http.StatusNotFound, "unknown aiinsights route")
	}
}

func (s *APIServer) handleListAIInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)
	ns := r.URL.Query().Get("namespace")
	issueFilter := r.URL.Query().Get("issue")

	var insights v1alpha1.AIInsightList
	opts := []client.ListOption{}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(ctx, &insights, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list AI insights: "+err.Error())
		return
	}

	var items []AIInsightItem
	for _, ai := range insights.Items {
		if issueFilter != "" && ai.Spec.IssueRef.Name != issueFilter {
			continue
		}
		items = append(items, aiInsightToItem(ai))
	}

	total := len(items)
	start, end := paginateSlice(total, pp)
	writeListResponse(w, "AIInsightList", items[start:end], total, pp)
}

func (s *APIServer) handleGetAIInsight(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var insight v1alpha1.AIInsight
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &insight); err != nil {
		writeError(w, http.StatusNotFound, "AI insight not found: "+err.Error())
		return
	}

	item := aiInsightToItem(insight)
	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "AIInsight",
		ResourceMeta: &ResourceMeta{
			Name:              insight.Name,
			Namespace:         insight.Namespace,
			UID:               string(insight.UID),
			CreationTimestamp: insight.CreationTimestamp.Format(time.RFC3339),
			Labels:            insight.Labels,
			Annotations:       insight.Annotations,
		},
		Spec:   item,
		Status: insight.Status,
	})
}

func aiInsightToItem(ai v1alpha1.AIInsight) AIInsightItem {
	item := AIInsightItem{
		Name:                  ai.Name,
		Namespace:             ai.Namespace,
		IssueRef:              ai.Spec.IssueRef.Name,
		Provider:              ai.Spec.Provider,
		Model:                 ai.Spec.Model,
		Analysis:              ai.Status.Analysis,
		Confidence:            ai.Status.Confidence,
		Recommendations:       ai.Status.Recommendations,
		LogAnalysis:           ai.Status.LogAnalysis,
		MetricsContext:        ai.Status.MetricsContext,
		SourceCodeContext:     ai.Status.SourceCodeContext,
		GitOpsContext:         ai.Status.GitOpsContext,
		CascadeAnalysis:       ai.Status.CascadeAnalysis,
		BlastRadiusPrediction: ai.Status.BlastRadiusPrediction,
		CreationTimestamp:     ai.CreationTimestamp.Format(time.RFC3339),
	}
	if ai.Status.GeneratedAt != nil {
		t := ai.Status.GeneratedAt.Format(time.RFC3339)
		item.GeneratedAt = &t
	}
	for _, sa := range ai.Status.SuggestedActions {
		item.SuggestedActions = append(item.SuggestedActions, SuggestedActionItem{
			Name:        sa.Name,
			Action:      sa.Action,
			Description: sa.Description,
			Params:      sa.Params,
		})
	}
	return item
}

// ========== Remediations ==========

func (s *APIServer) routeRemediations(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		// GET /api/v1/remediations
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleListRemediations(w, r)

	case len(rest) == 1 && r.Method == http.MethodGet:
		// GET /api/v1/remediations/:name
		if !hasMinRole(roleFromContext(r.Context()), "viewer") {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		s.handleGetRemediation(w, r, rest[0])

	default:
		writeError(w, http.StatusNotFound, "unknown remediations route")
	}
}

func (s *APIServer) handleListRemediations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pp := parsePagination(r)
	ns := r.URL.Query().Get("namespace")
	stateFilter := r.URL.Query().Get("state")
	issueFilter := r.URL.Query().Get("issue")

	var remediations v1alpha1.RemediationPlanList
	opts := []client.ListOption{}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(ctx, &remediations, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list remediations: "+err.Error())
		return
	}

	var items []RemediationItem
	for _, rp := range remediations.Items {
		if stateFilter != "" && !strings.EqualFold(string(rp.Status.State), stateFilter) {
			continue
		}
		if issueFilter != "" && rp.Spec.IssueRef.Name != issueFilter {
			continue
		}
		items = append(items, remediationToItem(rp))
	}

	total := len(items)
	start, end := paginateSlice(total, pp)
	writeListResponse(w, "RemediationPlanList", items[start:end], total, pp)
}

func (s *APIServer) handleGetRemediation(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var rp v1alpha1.RemediationPlan
	if err := s.client.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &rp); err != nil {
		writeError(w, http.StatusNotFound, "remediation plan not found: "+err.Error())
		return
	}

	detail := RemediationDetailItem{
		RemediationItem:   remediationToItem(rp),
		SafetyConstraints: rp.Spec.SafetyConstraints,
		RollbackPerformed: rp.Status.RollbackPerformed,
		RollbackResult:    rp.Status.RollbackResult,
		Evidence:          []EvidenceDetailItem{},
		AgenticHistory:    []AgenticStepItem{},
	}

	for _, ev := range rp.Status.Evidence {
		detail.Evidence = append(detail.Evidence, EvidenceDetailItem{
			Type:      ev.Type,
			Data:      ev.Data,
			Timestamp: ev.Timestamp.Format(time.RFC3339),
		})
	}

	for _, step := range rp.Spec.AgenticHistory {
		as := AgenticStepItem{
			StepNumber:  step.StepNumber,
			AIMessage:   step.AIMessage,
			Observation: step.Observation,
			Timestamp:   step.Timestamp.Format(time.RFC3339),
		}
		if step.Action != nil {
			as.Action = &ActionItem{
				Type:   string(step.Action.Type),
				Params: step.Action.Params,
			}
		}
		detail.AgenticHistory = append(detail.AgenticHistory, as)
	}

	writeJSON(w, http.StatusOK, APIResponse{
		APIVersion: "v1",
		Kind:       "RemediationPlan",
		ResourceMeta: &ResourceMeta{
			Name:              rp.Name,
			Namespace:         rp.Namespace,
			UID:               string(rp.UID),
			CreationTimestamp: rp.CreationTimestamp.Format(time.RFC3339),
			Labels:            rp.Labels,
			Annotations:       rp.Annotations,
		},
		Spec:   detail,
		Status: rp.Status,
	})
}
