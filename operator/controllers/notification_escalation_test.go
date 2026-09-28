/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// hookRecorder is a webhook endpoint that remembers every notification.
type hookRecorder struct {
	mu     sync.Mutex
	titles []string
	states []string
}

func (h *hookRecorder) serve(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		Title string `json:"title"`
		State string `json:"state"`
	}
	_ = json.NewDecoder(r.Body).Decode(&msg)
	h.mu.Lock()
	h.titles = append(h.titles, msg.Title)
	h.states = append(h.states, msg.State)
	h.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (h *hookRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.titles)
}

// escalationFixture wires a NotificationReconciler to a fake API server
// holding an Escalated Issue, a three-level EscalationPolicy whose levels
// notify the "hook" channel, and a NotificationPolicy that defines "hook".
type escalationFixture struct {
	t      *testing.T
	ctx    context.Context
	c      client.Client
	r      *NotificationReconciler
	hook   *hookRecorder
	policy string
}

func newEscalationFixture(t *testing.T, repeatMinutes int32, rules []platformv1alpha1.NotificationRule, mutate func(*platformv1alpha1.Issue)) *escalationFixture {
	t.Helper()
	hook := &hookRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(hook.serve))
	t.Cleanup(srv.Close)

	issue := newIssue("esc-issue", "default")
	issue.Status.State = platformv1alpha1.IssueStateEscalated
	if mutate != nil {
		mutate(issue)
	}
	level := func(name string) platformv1alpha1.EscalationLevel {
		return platformv1alpha1.EscalationLevel{
			Name: name, TimeoutMinutes: 5, NotifyChannels: []string{"hook"}, RepeatIntervalMinutes: repeatMinutes,
			Targets: []platformv1alpha1.EscalationTarget{{Type: platformv1alpha1.EscalationTargetTeam, Name: "sre"}},
		}
	}
	escalation := &platformv1alpha1.EscalationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "oncall-chain", Namespace: "default"},
		Spec: platformv1alpha1.EscalationPolicySpec{
			Enabled: true,
			Levels:  []platformv1alpha1.EscalationLevel{level("L1"), level("L2"), level("L3")},
		},
	}
	notify := &platformv1alpha1.NotificationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "channels", Namespace: "default"},
		Spec: platformv1alpha1.NotificationPolicySpec{
			Enabled:  true,
			Channels: []platformv1alpha1.NotificationChannel{{Name: "hook", Type: platformv1alpha1.ChannelWebhook, Config: map[string]string{"url": srv.URL}}},
			Rules:    rules,
		},
	}
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.Issue{}, &platformv1alpha1.EscalationPolicy{}, &platformv1alpha1.NotificationPolicy{}).
		WithObjects(issue, escalation, notify).Build()
	return &escalationFixture{t: t, ctx: context.Background(), c: c, r: &NotificationReconciler{Client: c, Scheme: s}, hook: hook, policy: "oncall-chain"}
}

func (f *escalationFixture) reconcile() ctrl.Result {
	f.t.Helper()
	res, err := f.r.Reconcile(f.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "esc-issue", Namespace: "default"}})
	if err != nil {
		f.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (f *escalationFixture) issue() *platformv1alpha1.Issue {
	f.t.Helper()
	var iss platformv1alpha1.Issue
	if err := f.c.Get(f.ctx, types.NamespacedName{Name: "esc-issue", Namespace: "default"}, &iss); err != nil {
		f.t.Fatal(err)
	}
	return &iss
}

// setAnnotations writes annotations on the stored Issue, as a person or
// the passage of time would.
func (f *escalationFixture) setAnnotations(kv map[string]string) {
	f.t.Helper()
	iss := f.issue()
	for k, v := range kv {
		setIssueAnnotation(iss, k, v)
	}
	if err := f.c.Update(f.ctx, iss); err != nil {
		f.t.Fatal(err)
	}
}

// ago formats a time in the past as the controller writes annotations.
func ago(d time.Duration) string { return time.Now().UTC().Add(-d).Format(time.RFC3339) }

func (f *escalationFixture) escalationPolicy() *platformv1alpha1.EscalationPolicy {
	f.t.Helper()
	var p platformv1alpha1.EscalationPolicy
	if err := f.c.Get(f.ctx, types.NamespacedName{Name: f.policy, Namespace: "default"}, &p); err != nil {
		f.t.Fatal(err)
	}
	return &p
}

// The level reached is written back to the Issue, so each timeout moves one
// level up and the chain reaches L3; a reconcile in between re-sends nothing
// and the final level is never passed.
func TestEscalation_PersistsLevelAndReachesL3(t *testing.T) {
	f := newEscalationFixture(t, 0, nil, nil)
	l2 := metricValue(t, "chatcli_operator_escalation_level_reached", map[string]string{"policy": f.policy, "level": "L2"})
	l3 := metricValue(t, "chatcli_operator_escalation_level_reached", map[string]string{"policy": f.policy, "level": "L3"})

	res := f.reconcile()
	if got := f.issue().Annotations[annotationEscalationLevel]; got != "0" {
		t.Fatalf("level after initiation = %q, want 0 persisted", got)
	}
	if f.hook.count() != 1 {
		t.Fatalf("L1 notifications = %d, want 1", f.hook.count())
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > 5*time.Minute {
		t.Fatalf("requeue after initiation = %v, want the level timeout", res.RequeueAfter)
	}

	for i, want := range []string{"1", "2"} {
		f.setAnnotations(map[string]string{annotationEscalationTime: ago(6 * time.Minute)})
		f.reconcile()
		if got := f.issue().Annotations[annotationEscalationLevel]; got != want {
			t.Fatalf("level after timeout %d = %q, want %s", i+1, got, want)
		}
		sent := f.hook.count()
		// A reconcile before the next timeout must not send the level again.
		f.reconcile()
		if f.hook.count() != sent {
			t.Fatalf("a reconcile inside the level timeout re-sent the notification (%d -> %d)", sent, f.hook.count())
		}
	}
	if f.hook.count() != 3 {
		t.Fatalf("notifications = %d, want exactly L1, L2 and L3", f.hook.count())
	}
	if got := metricValue(t, "chatcli_operator_escalation_level_reached", map[string]string{"policy": f.policy, "level": "L2"}) - l2; got != 1 {
		t.Fatalf("escalation_level_reached{L2} grew by %v, want 1", got)
	}
	if got := metricValue(t, "chatcli_operator_escalation_level_reached", map[string]string{"policy": f.policy, "level": "L3"}) - l3; got != 1 {
		t.Fatalf("escalation_level_reached{L3} grew by %v, want 1", got)
	}

	// Past the last level's timeout nothing changes.
	f.setAnnotations(map[string]string{annotationEscalationTime: ago(6 * time.Minute)})
	f.reconcile()
	if got := f.issue().Annotations[annotationEscalationLevel]; got != "2" || f.hook.count() != 3 {
		t.Fatalf("after the last timeout: level %q, notifications %d; want 2 and 3", got, f.hook.count())
	}
	p := f.escalationPolicy()
	if len(p.Status.ActiveEscalations) != 1 || p.Status.ActiveEscalations[0].CurrentLevel != 2 || p.Status.TotalEscalations != 1 {
		t.Fatalf("policy status = %+v, want one active escalation at level 2", p.Status)
	}
}

// An acknowledged Issue stops escalating: the timeout passes, no level is
// reached, nothing is sent, and the acknowledgment lands on the policy.
func TestEscalation_AcknowledgeStopsEscalation(t *testing.T) {
	f := newEscalationFixture(t, 0, nil, nil)
	f.reconcile()
	f.setAnnotations(map[string]string{
		AnnotationIncidentAcknowledged:   "true",
		AnnotationIncidentAcknowledgedBy: "operator",
		AnnotationIncidentAcknowledgedAt: ago(time.Minute),
		annotationEscalationTime:         ago(30 * time.Minute),
	})
	res := f.reconcile()
	if got := f.issue().Annotations[annotationEscalationLevel]; got != "0" {
		t.Fatalf("acknowledged Issue escalated to %q", got)
	}
	if f.hook.count() != 1 || res.RequeueAfter != 0 {
		t.Fatalf("notifications = %d, requeue = %v; want only the initial page and no timer", f.hook.count(), res.RequeueAfter)
	}
	entry := f.escalationPolicy().Status.ActiveEscalations[0]
	if entry.AcknowledgedAt == nil || entry.AcknowledgedBy != "operator" {
		t.Fatalf("acknowledgment not recorded on the policy: %+v", entry)
	}
}

// An Issue acknowledged before it escalates is not paged at all.
func TestEscalation_AcknowledgedBeforeEscalationIsNotPaged(t *testing.T) {
	f := newEscalationFixture(t, 0, nil, func(iss *platformv1alpha1.Issue) {
		iss.Annotations = map[string]string{AnnotationIncidentAcknowledged: "true"}
	})
	f.reconcile()
	if _, ok := f.issue().Annotations[annotationEscalationLevel]; ok || f.hook.count() != 0 {
		t.Fatalf("acknowledged Issue was escalated (notifications %d)", f.hook.count())
	}
}

// A snooze holds the page: nothing is sent while it lasts, the timer waits
// for its end, and the held notification goes out when it is over, with the
// level's clock starting again from there.
func TestEscalation_SnoozeHoldsAndResumes(t *testing.T) {
	until := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
	f := newEscalationFixture(t, 0, nil, func(iss *platformv1alpha1.Issue) {
		iss.Annotations = map[string]string{AnnotationIncidentSnoozedUntil: until}
	})
	res := f.reconcile()
	iss := f.issue()
	if iss.Annotations[annotationEscalationLevel] != "0" || iss.Annotations[annotationEscalationPendingNotify] != "true" {
		t.Fatalf("snoozed escalation should start at level 0 with the page held: %v", iss.Annotations)
	}
	if f.hook.count() != 0 {
		t.Fatalf("snoozed Issue paged %d times", f.hook.count())
	}
	if res.RequeueAfter <= 9*time.Minute || res.RequeueAfter > 10*time.Minute {
		t.Fatalf("requeue while snoozed = %v, want the snooze end", res.RequeueAfter)
	}

	// Snooze over 30s ago, escalation started long before: the held page
	// goes out and the level does not jump, its clock restarted at the end.
	f.setAnnotations(map[string]string{
		AnnotationIncidentSnoozedUntil: ago(30 * time.Second),
		annotationEscalationTime:       ago(time.Hour),
	})
	f.reconcile()
	iss = f.issue()
	if f.hook.count() != 1 {
		t.Fatalf("held page sent %d times after the snooze, want 1", f.hook.count())
	}
	if iss.Annotations[annotationEscalationLevel] != "0" || iss.Annotations[annotationEscalationPendingNotify] != "" {
		t.Fatalf("after the snooze: %v, want level 0 and nothing pending", iss.Annotations)
	}
}

// repeatIntervalMinutes re-sends the current level until something changes.
func TestEscalation_RepeatInterval(t *testing.T) {
	f := newEscalationFixture(t, 2, nil, nil)
	res := f.reconcile()
	if res.RequeueAfter <= 0 || res.RequeueAfter > 2*time.Minute {
		t.Fatalf("requeue = %v, want the repeat interval", res.RequeueAfter)
	}
	f.setAnnotations(map[string]string{annotationEscalationNotifiedAt: ago(3 * time.Minute), annotationEscalationTime: ago(3 * time.Minute)})
	f.reconcile()
	if f.hook.count() != 2 || f.issue().Annotations[annotationEscalationLevel] != "0" {
		t.Fatalf("notifications %d level %q, want a repeat at level 0", f.hook.count(), f.issue().Annotations[annotationEscalationLevel])
	}
	f.reconcile()
	if f.hook.count() != 2 {
		t.Fatalf("repeat sent again before its interval: %d", f.hook.count())
	}
}

// Resolving clears the escalation from the Issue and from the policy.
func TestEscalation_ResolveClearsState(t *testing.T) {
	f := newEscalationFixture(t, 0, nil, nil)
	f.reconcile()
	iss := f.issue()
	iss.Status.State = platformv1alpha1.IssueStateResolved
	if err := f.c.Status().Update(f.ctx, iss); err != nil {
		t.Fatal(err)
	}
	f.reconcile()
	for _, key := range escalationAnnotationKeys {
		if _, ok := f.issue().Annotations[key]; ok {
			t.Fatalf("annotation %s left on a resolved Issue", key)
		}
	}
	if n := len(f.escalationPolicy().Status.ActiveEscalations); n != 0 {
		t.Fatalf("active escalations after resolve = %d, want 0", n)
	}
}

// A snoozed Issue sends no state-change notification, except Resolved.
func TestNotification_SnoozeSuppressesAllButResolved(t *testing.T) {
	rules := []platformv1alpha1.NotificationRule{{Name: "all", Channels: []string{"hook"}}}
	until := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	f := newEscalationFixture(t, 0, rules, func(iss *platformv1alpha1.Issue) {
		iss.Status.State = platformv1alpha1.IssueStateAnalyzing
		iss.Annotations = map[string]string{AnnotationIncidentSnoozedUntil: until}
	})
	f.reconcile()
	if f.hook.count() != 0 {
		t.Fatalf("snoozed Issue notified %d times", f.hook.count())
	}
	if got := f.issue().Annotations[annotationLastNotifiedState]; got != string(platformv1alpha1.IssueStateAnalyzing) {
		t.Fatalf("last-notified-state = %q; the suppressed change must not be re-sent later", got)
	}
	iss := f.issue()
	iss.Status.State = platformv1alpha1.IssueStateResolved
	if err := f.c.Status().Update(f.ctx, iss); err != nil {
		t.Fatal(err)
	}
	f.reconcile()
	if f.hook.count() != 1 || f.hook.states[0] != string(platformv1alpha1.IssueStateResolved) {
		t.Fatalf("resolve during a snooze: %v, want exactly the Resolved notification", f.hook.states)
	}
}

// Every delivery is timed per channel type.
func TestNotification_DeliveryDurationIsObserved(t *testing.T) {
	series := map[string]string{"channel_type": "webhook"}
	before := metricValue(t, "chatcli_operator_notification_duration_seconds", series)
	rules := []platformv1alpha1.NotificationRule{{Name: "all", Channels: []string{"hook"}}}
	f := newEscalationFixture(t, 0, rules, func(iss *platformv1alpha1.Issue) {
		iss.Status.State = platformv1alpha1.IssueStateAnalyzing
	})
	f.reconcile()
	if f.hook.count() != 1 {
		t.Fatalf("notifications = %d, want 1", f.hook.count())
	}
	if got := metricValue(t, "chatcli_operator_notification_duration_seconds", series) - before; got != 1 {
		t.Fatalf("notification_duration_seconds{webhook} observed %v deliveries, want 1", got)
	}
}

// Resolves on the paging channels skip the throttle; everything else keeps it.
func TestNotification_BypassesThrottle(t *testing.T) {
	cases := []struct {
		ch    platformv1alpha1.NotificationChannelType
		state platformv1alpha1.IssueState
		want  bool
	}{
		{platformv1alpha1.ChannelPagerDuty, platformv1alpha1.IssueStateResolved, true},
		{platformv1alpha1.ChannelOpsGenie, platformv1alpha1.IssueStateResolved, true},
		{platformv1alpha1.ChannelSlack, platformv1alpha1.IssueStateResolved, false},
		{platformv1alpha1.ChannelPagerDuty, platformv1alpha1.IssueStateEscalated, false},
	}
	for _, tc := range cases {
		if got := bypassesThrottle(tc.ch, tc.state); got != tc.want {
			t.Errorf("bypassesThrottle(%s, %s) = %v, want %v", tc.ch, tc.state, got, tc.want)
		}
	}
}

func TestIncidentSnoozedUntil(t *testing.T) {
	now := time.Now()
	iss := newIssue("x", "default")
	if _, ok := IncidentSnoozedUntil(iss, now); ok {
		t.Fatal("no annotation must mean not snoozed")
	}
	iss.Annotations = map[string]string{AnnotationIncidentSnoozedUntil: "not-a-time"}
	if _, ok := IncidentSnoozedUntil(iss, now); ok {
		t.Fatal("an unparsable value must mean not snoozed")
	}
	iss.Annotations[AnnotationIncidentSnoozedUntil] = now.Add(-time.Minute).UTC().Format(time.RFC3339)
	if _, ok := IncidentSnoozedUntil(iss, now); ok {
		t.Fatal("a past snooze must mean not snoozed")
	}
	iss.Annotations[AnnotationIncidentSnoozedUntil] = now.Add(time.Minute).UTC().Format(time.RFC3339)
	if _, ok := IncidentSnoozedUntil(iss, now); !ok {
		t.Fatal("a future snooze must hold")
	}
	iss.Annotations[AnnotationIncidentAcknowledged] = "True"
	if !IncidentAcknowledged(iss) {
		t.Fatal("acknowledged=True must count")
	}
}
