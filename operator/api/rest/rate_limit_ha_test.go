/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"github.com/diillson/chatcli/operator/controllers"
)

func limitedHandler(api *APIServer) http.Handler {
	return chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), api.rateLimitMiddleware, api.authMiddleware)
}

func requestFrom(remote, apiKey string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	req.RemoteAddr = remote
	if apiKey != "" {
		req.Header.Set(authHeaderName, apiKey)
	}
	return req
}

// Every new TCP connection has a new source port; keying by the full peer
// address gave each one a fresh bucket, i.e. no limit.
func TestRateLimit_UnauthenticatedIsKeyedByHostNotConnection(t *testing.T) {
	api := NewAPIServer(fake.NewClientBuilder().WithScheme(newRestScheme()).Build(), ":0")
	api.SetAPIKeys(map[string]string{"valid-key": "viewer"})
	h := limitedHandler(api)

	var last *httptest.ResponseRecorder
	for i := 0; i <= unauthenticatedRequestsPerMinute; i++ {
		last = httptest.NewRecorder()
		// A different ephemeral port on every request, and a wrong key.
		h.ServeHTTP(last, requestFrom(fmt.Sprintf("203.0.113.7:%d", 40000+i), "guess"))
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d from one host = %d, want 429", unauthenticatedRequestsPerMinute+1, last.Code)
	}
	if !strings.Contains(last.Body.String(), fmt.Sprintf("%d requests per minute", unauthenticatedRequestsPerMinute)) {
		t.Errorf("429 body names the real limit: %s", last.Body.String())
	}
	if got := last.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2 (one token every two seconds)", got)
	}

	// Another host has its own budget.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, requestFrom("198.51.100.9:50000", "guess"))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("another host = %d, want 401 (limited separately)", w.Code)
	}
}

// Forwarding headers are client input without a trusted proxy.
func TestRateLimit_IgnoresForwardedFor(t *testing.T) {
	api := NewAPIServer(fake.NewClientBuilder().WithScheme(newRestScheme()).Build(), ":0")
	api.SetAPIKeys(map[string]string{"valid-key": "viewer"})
	h := limitedHandler(api)
	var last *httptest.ResponseRecorder
	for i := 0; i <= unauthenticatedRequestsPerMinute; i++ {
		req := requestFrom("203.0.113.8:1234", "")
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.0.0.%d", i))
		last = httptest.NewRecorder()
		h.ServeHTTP(last, req)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("rotating X-Forwarded-For escaped the limit: %d", last.Code)
	}
}

// A valid key is limited per key, with room for the dashboard's refresh
// traffic from several viewers behind one address (port-forward, ingress).
func TestRateLimit_ValidKeyHasItsOwnBudget(t *testing.T) {
	api := NewAPIServer(fake.NewClientBuilder().WithScheme(newRestScheme()).Build(), ":0")
	api.SetAPIKeys(map[string]string{"valid-key": "viewer", "other-key": "viewer"})
	h := limitedHandler(api)

	// Exhaust the host's unauthenticated budget first.
	for i := 0; i <= unauthenticatedRequestsPerMinute; i++ {
		h.ServeHTTP(httptest.NewRecorder(), requestFrom("127.0.0.1:1000", ""))
	}
	// Three minutes of a dashboard at its fastest refresh, from the same host.
	for i := 0; i < 3*66; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, requestFrom(fmt.Sprintf("127.0.0.1:%d", 2000+i), "valid-key"))
		if w.Code != http.StatusOK {
			t.Fatalf("authenticated request %d = %d", i, w.Code)
		}
	}
	// The ceiling still exists.
	var last *httptest.ResponseRecorder
	for i := 0; i < authenticatedRequestsPerMinute; i++ {
		last = httptest.NewRecorder()
		h.ServeHTTP(last, requestFrom("127.0.0.1:3000", "other-key"))
	}
	if last.Code != http.StatusOK {
		t.Fatalf("within the per-key budget = %d", last.Code)
	}
	last = httptest.NewRecorder()
	h.ServeHTTP(last, requestFrom("127.0.0.1:3000", "other-key"))
	if last.Code != http.StatusTooManyRequests || !strings.Contains(last.Body.String(), "600 requests per minute") {
		t.Errorf("past the per-key budget = %d %s", last.Code, last.Body.String())
	}
}

func TestRateLimit_DevModeUsesTheKeyBudget(t *testing.T) {
	t.Setenv("CHATCLI_OPERATOR_DEV_MODE", "true")
	api := NewAPIServer(fake.NewClientBuilder().WithScheme(newRestScheme()).Build(), ":0")
	limiter, key := api.rateLimitBucketFor(requestFrom("127.0.0.1:1", ""))
	if limiter != api.keyLimiter || key != "dev:127.0.0.1" {
		t.Errorf("dev mode bucket = %q on the %v-rpm limiter", key, limiter.maxRPM)
	}
}

func TestClientHost(t *testing.T) {
	for remote, want := range map[string]string{
		"203.0.113.7:4242":   "203.0.113.7",
		"[2001:db8::1]:4242": "2001:db8::1",
		"unix-socket":        "unix-socket",
	} {
		if got := clientHost(&http.Request{RemoteAddr: remote}); got != want {
			t.Errorf("clientHost(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestRateLimiter_PrunesIdleBuckets(t *testing.T) {
	rl := newRateLimiter(30)
	rl.getBucket("idle")
	rl.getBucket("busy")
	v, _ := rl.buckets.Load("idle")
	idle := v.(*tokenBucket)
	idle.mu.Lock()
	idle.lastRefill = time.Now().Add(-bucketIdleTTL - time.Minute)
	idle.mu.Unlock()

	// Not due yet: nothing pruned.
	rl.pruneIdle(time.Now())
	if _, ok := rl.buckets.Load("idle"); !ok {
		t.Fatal("pruned before the prune interval")
	}
	rl.pruneIdle(time.Now().Add(bucketPruneInterval))
	if _, ok := rl.buckets.Load("idle"); ok {
		t.Error("idle bucket kept")
	}
	if _, ok := rl.buckets.Load("busy"); !ok {
		t.Error("active bucket dropped")
	}
	if got := newRateLimiter(0).retryAfterSeconds(); got != 60 {
		t.Errorf("degenerate limit Retry-After = %d", got)
	}
}

// --- HA: the REST API runs on every replica ---

func TestAPIServer_RunsWithoutLeaderElection(t *testing.T) {
	api := NewAPIServer(fake.NewClientBuilder().WithScheme(newRestScheme()).Build(), ":0")
	if api.NeedLeaderElection() {
		t.Fatal("the REST API must serve on every replica the Service routes to")
	}
}

type fakeBridge struct {
	mu     sync.Mutex
	active bool
	calls  []string
}

func (f *fakeBridge) InvalidateDedupForResource(deployment, namespace string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, namespace+"/"+deployment)
}

func (f *fakeBridge) IsActive() bool { return f.active }

func resolvableIssue() *v1alpha1.Issue {
	return &v1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "inc-1", Namespace: "default"},
		Spec:       v1alpha1.IssueSpec{Resource: v1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}},
		Status:     v1alpha1.IssueStatus{State: v1alpha1.IssueStateEscalated},
	}
}

func resolveVia(t *testing.T, bridge WatcherDedupInvalidator) (*fakeBridge, v1alpha1.Issue) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newRestScheme()).
		WithStatusSubresource(&v1alpha1.Issue{}).WithObjects(resolvableIssue()).Build()
	api := NewAPIServer(c, ":0")
	if bridge != nil {
		api.SetWatcherBridge(bridge)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/inc-1/resolve?namespace=default", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	api.handleResolveIncident(w, req, "inc-1")
	if w.Code != http.StatusOK {
		t.Fatalf("resolve = %d %s", w.Code, w.Body.String())
	}
	var got v1alpha1.Issue
	if err := c.Get(context.Background(), client.ObjectKey{Name: "inc-1", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	fb, _ := bridge.(*fakeBridge)
	return fb, got
}

func TestResolveIncident_InvalidatesLocallyOnTheLeader(t *testing.T) {
	fb, got := resolveVia(t, &fakeBridge{active: true})
	if len(fb.calls) != 1 || fb.calls[0] != "default/web" {
		t.Fatalf("invalidations = %v", fb.calls)
	}
	if got.Annotations[controllers.ManualResolutionDedupAnnotation] != "true" {
		t.Error("the Issue records that the dedup was already invalidated")
	}
}

func TestResolveIncident_LeavesItToTheLeaderElsewhere(t *testing.T) {
	fb, got := resolveVia(t, &fakeBridge{active: false})
	if len(fb.calls) != 0 {
		t.Fatalf("an idle bridge has no dedup to invalidate: %v", fb.calls)
	}
	if _, ok := got.Annotations[controllers.ManualResolutionDedupAnnotation]; ok {
		t.Error("the marker must stay unset so the leader does the invalidation")
	}
	if got.Annotations["aiops.chatcli.io/manual-resolution"] != "true" {
		t.Error("manual resolution is still recorded")
	}
	if _, got := resolveVia(t, nil); got.Status.State != v1alpha1.IssueStateResolved {
		t.Error("resolve works without a bridge")
	}
}
