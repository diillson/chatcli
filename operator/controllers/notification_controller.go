package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"github.com/diillson/chatcli/operator/channels"
)

const (
	annotationLastNotifiedState = "platform.chatcli.io/last-notified-state"
	annotationEscalationLevel   = "platform.chatcli.io/escalation-level"
	annotationEscalationTime    = "platform.chatcli.io/escalation-time"
	annotationEscalationPolicy  = "platform.chatcli.io/escalation-policy"

	maxRecentDeliveries = 20
)

// Prometheus metrics for the notification controller.
var (
	notificationsSentTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "notifications_sent_total",
		Help:      "Total notifications sent by channel type, severity, and result.",
	}, []string{"channel_type", "severity", "result"})

	notificationsFailedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "notifications_failed_total",
		Help:      "Total notification failures by channel type and reason.",
	}, []string{"channel_type", "reason"})

	escalationLevelReached = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "escalation_level_reached",
		Help:      "Escalation levels reached by policy and level name.",
	}, []string{"policy", "level"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		notificationsSentTotal,
		notificationsFailedTotal,
		escalationLevelReached,
	)
}

// NotificationReconciler reconciles Issue objects and sends notifications
// based on matching NotificationPolicy rules.
type NotificationReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	throttle   sync.Map // key: "issue/channel" -> last sent time (time.Time)
	hourCounts sync.Map // key: "issue/hour" -> count (*int64)
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues/status,verbs=get
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=notificationpolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=notificationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=escalationpolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=escalationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *NotificationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("issue", req.NamespacedName)

	// Fetch the Issue.
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, req.NamespacedName, &issue); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	currentState := string(issue.Status.State)
	if currentState == "" {
		return ctrl.Result{}, nil
	}

	// Check if state changed since last notification.
	annotations := issue.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	lastNotifiedState := annotations[annotationLastNotifiedState]

	stateChanged := lastNotifiedState != currentState

	// Handle escalation timer check even if state has not changed.
	requeueAfter, escalationErr := r.handleEscalationTimer(ctx, &issue)
	if escalationErr != nil {
		logger.Error(escalationErr, "failed to handle escalation timer")
	}

	if !stateChanged {
		if requeueAfter > 0 {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		return ctrl.Result{}, nil
	}

	logger.Info("issue state changed", "from", lastNotifiedState, "to", currentState)

	// Find matching NotificationPolicies.
	var policies platformv1alpha1.NotificationPolicyList
	if err := r.List(ctx, &policies); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing notification policies: %w", err)
	}

	for i := range policies.Items {
		policy := &policies.Items[i]
		if !policy.Spec.Enabled {
			continue
		}
		if err := r.processPolicy(ctx, policy, &issue); err != nil {
			logger.Error(err, "failed to process notification policy", "policy", policy.Name)
		}
	}

	// Handle escalation initiation when issue reaches Escalated state.
	if issue.Status.State == platformv1alpha1.IssueStateEscalated {
		if err := r.initiateEscalation(ctx, &issue); err != nil {
			logger.Error(err, "failed to initiate escalation")
		}
	}

	// Update the last-notified-state annotation.
	annotations[annotationLastNotifiedState] = currentState
	issue.SetAnnotations(annotations)
	if err := r.Update(ctx, &issue); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating issue annotation: %w", err)
	}

	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// processPolicy evaluates a single NotificationPolicy against the issue and sends matching notifications.
func (r *NotificationReconciler) processPolicy(ctx context.Context, policy *platformv1alpha1.NotificationPolicy, issue *platformv1alpha1.Issue) error {
	logger := log.FromContext(ctx).WithValues("policy", policy.Name)

	// Build a channel lookup map from the policy spec.
	channelMap := make(map[string]platformv1alpha1.NotificationChannel, len(policy.Spec.Channels))
	for _, ch := range policy.Spec.Channels {
		channelMap[ch.Name] = ch
	}

	// Parse throttle config.
	dedupWindow := parseDuration(policy.Spec.Throttle.DeduplicationWindow, 5*time.Minute)
	maxPerHour := int64(policy.Spec.Throttle.MaxPerHour)
	if maxPerHour <= 0 {
		maxPerHour = 10
	}

	for _, rule := range policy.Spec.Rules {
		if !r.ruleMatchesIssue(rule, issue) {
			continue
		}

		logger.Info("rule matched", "rule", rule.Name)

		for _, channelName := range rule.Channels {
			ch, ok := channelMap[channelName]
			if !ok {
				logger.Info("channel not found in policy", "channel", channelName)
				continue
			}

			// Check throttle.
			throttleKey := fmt.Sprintf("%s/%s/%s", issue.Namespace, issue.Name, channelName)
			if r.isThrottled(throttleKey, dedupWindow, maxPerHour) {
				logger.Info("notification throttled", "channel", channelName, "issue", issue.Name)
				continue
			}

			// Resolve secret references into the config map.
			config, err := r.resolveChannelConfig(ctx, policy.Namespace, ch)
			if err != nil {
				logger.Error(err, "failed to resolve channel config", "channel", channelName)
				notificationsFailedTotal.WithLabelValues(string(ch.Type), "config_resolve").Inc()
				r.recordDelivery(ctx, policy, channelName, false, err.Error())
				continue
			}

			// Create sender.
			sender, err := channels.NewSender(string(ch.Type), config)
			if err != nil {
				logger.Error(err, "failed to create channel sender", "channel", channelName)
				notificationsFailedTotal.WithLabelValues(string(ch.Type), "sender_create").Inc()
				r.recordDelivery(ctx, policy, channelName, false, err.Error())
				continue
			}

			// Build and send the notification message.
			msg := r.buildMessage(issue, policy)
			if err := sender.Send(ctx, msg); err != nil {
				logger.Error(err, "failed to send notification", "channel", channelName)
				notificationsSentTotal.WithLabelValues(string(ch.Type), string(issue.Spec.Severity), "failure").Inc()
				notificationsFailedTotal.WithLabelValues(string(ch.Type), "send").Inc()
				r.recordDelivery(ctx, policy, channelName, false, err.Error())
				continue
			}

			logger.Info("notification sent", "channel", channelName, "type", ch.Type)
			notificationsSentTotal.WithLabelValues(string(ch.Type), string(issue.Spec.Severity), "success").Inc()
			r.recordDelivery(ctx, policy, channelName, true, "")
			r.updateThrottle(throttleKey)
		}
	}

	return nil
}

// ruleMatchesIssue checks whether a NotificationRule matches the given Issue.
func (r *NotificationReconciler) ruleMatchesIssue(rule platformv1alpha1.NotificationRule, issue *platformv1alpha1.Issue) bool {
	// Check severity filter.
	if len(rule.Severities) > 0 {
		matched := false
		for _, s := range rule.Severities {
			if s == issue.Spec.Severity {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check signal type filter.
	if len(rule.SignalTypes) > 0 {
		matched := false
		for _, st := range rule.SignalTypes {
			if st == issue.Spec.SignalType {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check namespace filter.
	if len(rule.Namespaces) > 0 {
		matched := false
		for _, ns := range rule.Namespaces {
			if ns == issue.Spec.Resource.Namespace {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check resource kind filter.
	if len(rule.ResourceKinds) > 0 {
		matched := false
		for _, rk := range rule.ResourceKinds {
			if rk == issue.Spec.Resource.Kind {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check state filter.
	if len(rule.States) > 0 {
		matched := false
		for _, st := range rule.States {
			if st == issue.Status.State {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// isThrottled returns true if sending should be suppressed by deduplication or hourly limits.
func (r *NotificationReconciler) isThrottled(key string, dedupWindow time.Duration, maxPerHour int64) bool {
	// Deduplication window check.
	if val, ok := r.throttle.Load(key); ok {
		lastSent := val.(time.Time)
		if time.Since(lastSent) < dedupWindow {
			return true
		}
	}

	// Hourly count check.
	hourKey := fmt.Sprintf("%s/%d", key, time.Now().Unix()/3600)
	if val, ok := r.hourCounts.Load(hourKey); ok {
		count := val.(*int64)
		if *count >= maxPerHour {
			return true
		}
	}

	return false
}

// updateThrottle records that a notification was just sent for the given key.
func (r *NotificationReconciler) updateThrottle(key string) {
	now := time.Now()
	r.throttle.Store(key, now)

	hourKey := fmt.Sprintf("%s/%d", key, now.Unix()/3600)
	val, loaded := r.hourCounts.LoadOrStore(hourKey, new(int64))
	count := val.(*int64)
	*count++

	// Clean up stale hour keys on first store.
	if !loaded {
		prevHourKey := fmt.Sprintf("%s/%d", key, now.Unix()/3600-1)
		r.hourCounts.Delete(prevHourKey)
	}
}

// resolveChannelConfig merges the channel's Config with values from the referenced Secret.
func (r *NotificationReconciler) resolveChannelConfig(ctx context.Context, namespace string, ch platformv1alpha1.NotificationChannel) (map[string]string, error) {
	config := make(map[string]string, len(ch.Config))
	for k, v := range ch.Config {
		config[k] = v
	}

	if ch.SecretRef == nil {
		return config, nil
	}

	var secret corev1.Secret
	secretKey := types.NamespacedName{
		Name:      ch.SecretRef.Name,
		Namespace: namespace,
	}
	if err := r.Get(ctx, secretKey, &secret); err != nil {
		return nil, fmt.Errorf("fetching secret %s: %w", secretKey, err)
	}

	for k, v := range secret.Data {
		config[k] = string(v)
	}

	return config, nil
}

// buildMessage constructs a NotificationMessage from an Issue.
func (r *NotificationReconciler) buildMessage(issue *platformv1alpha1.Issue, policy *platformv1alpha1.NotificationPolicy) *channels.NotificationMessage {
	severity := string(issue.Spec.Severity)
	state := string(issue.Status.State)

	title := fmt.Sprintf("%s [%s] %s/%s — %s",
		channels.SeverityEmoji(severity),
		strings.ToUpper(severity),
		issue.Spec.Resource.Namespace,
		issue.Spec.Resource.Name,
		state,
	)

	body := issue.Spec.Description
	if body == "" {
		body = fmt.Sprintf("Issue %s on %s/%s transitioned to %s.",
			issue.Name,
			issue.Spec.Resource.Kind,
			issue.Spec.Resource.Name,
			state,
		)
	}

	// Check for custom template.
	templateKey := r.templateKeyForState(issue.Status.State)
	if policy.Spec.Templates != nil {
		if tmpl, ok := policy.Spec.Templates[templateKey]; ok && tmpl != "" {
			body = r.renderSimpleTemplate(tmpl, issue)
		}
	}

	fields := map[string]string{
		"Source":     string(issue.Spec.Source),
		"SignalType": issue.Spec.SignalType,
		"RiskScore":  fmt.Sprintf("%d", issue.Spec.RiskScore),
	}
	if issue.Spec.CorrelationId != "" {
		fields["CorrelationID"] = issue.Spec.CorrelationId
	}
	if issue.Status.RemediationAttempts > 0 {
		fields["RemediationAttempts"] = fmt.Sprintf("%d/%d",
			issue.Status.RemediationAttempts,
			issue.Status.MaxRemediationAttempts,
		)
	}
	if issue.Status.Resolution != "" {
		fields["Resolution"] = issue.Status.Resolution
	}

	return &channels.NotificationMessage{
		Title:     title,
		Body:      body,
		Severity:  severity,
		IssueName: issue.Name,
		Namespace: issue.Spec.Resource.Namespace,
		Resource:  fmt.Sprintf("%s/%s", issue.Spec.Resource.Kind, issue.Spec.Resource.Name),
		State:     state,
		Timestamp: time.Now(),
		Fields:    fields,
		Color:     channels.SeverityColor(severity),
	}
}

// templateKeyForState maps an IssueState to a template key.
func (r *NotificationReconciler) templateKeyForState(state platformv1alpha1.IssueState) string {
	switch state {
	case platformv1alpha1.IssueStateDetected:
		return "issue_created"
	case platformv1alpha1.IssueStateResolved:
		return "issue_resolved"
	case platformv1alpha1.IssueStateEscalated:
		return "issue_escalated"
	case platformv1alpha1.IssueStateRemediating:
		return "remediation_started"
	case platformv1alpha1.IssueStateFailed:
		return "remediation_failed"
	default:
		return "issue_created"
	}
}

// renderSimpleTemplate performs basic variable substitution in templates.
// Supported placeholders: {{.Name}}, {{.Namespace}}, {{.Severity}}, {{.State}},
// {{.Resource}}, {{.Description}}, {{.Source}}, {{.SignalType}}, {{.RiskScore}}.
func (r *NotificationReconciler) renderSimpleTemplate(tmpl string, issue *platformv1alpha1.Issue) string {
	replacer := strings.NewReplacer(
		"{{.Name}}", issue.Name,
		"{{.Namespace}}", issue.Spec.Resource.Namespace,
		"{{.Severity}}", string(issue.Spec.Severity),
		"{{.State}}", string(issue.Status.State),
		"{{.Resource}}", fmt.Sprintf("%s/%s", issue.Spec.Resource.Kind, issue.Spec.Resource.Name),
		"{{.Description}}", issue.Spec.Description,
		"{{.Source}}", string(issue.Spec.Source),
		"{{.SignalType}}", issue.Spec.SignalType,
		"{{.RiskScore}}", fmt.Sprintf("%d", issue.Spec.RiskScore),
	)
	return replacer.Replace(tmpl)
}

// recordDelivery appends a delivery record to the NotificationPolicy status, keeping only the last 20.
func (r *NotificationReconciler) recordDelivery(ctx context.Context, policy *platformv1alpha1.NotificationPolicy, channelName string, success bool, errMsg string) {
	logger := log.FromContext(ctx)

	// Re-fetch to avoid conflicts.
	var fresh platformv1alpha1.NotificationPolicy
	if err := r.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, &fresh); err != nil {
		logger.Error(err, "failed to re-fetch notification policy for status update")
		return
	}

	now := metav1.Now()
	record := platformv1alpha1.NotificationDeliveryRecord{
		Channel: channelName,
		SentAt:  now,
		Success: success,
		Error:   errMsg,
	}

	fresh.Status.RecentDeliveries = append(fresh.Status.RecentDeliveries, record)
	if len(fresh.Status.RecentDeliveries) > maxRecentDeliveries {
		fresh.Status.RecentDeliveries = fresh.Status.RecentDeliveries[len(fresh.Status.RecentDeliveries)-maxRecentDeliveries:]
	}

	if success {
		fresh.Status.TotalSent++
		fresh.Status.LastNotifiedAt = &now
	} else {
		fresh.Status.FailedCount++
	}

	// Update ready condition.
	condType := "Ready"
	if success {
		meta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
			Type:               condType,
			Status:             metav1.ConditionTrue,
			Reason:             "DeliverySucceeded",
			Message:            fmt.Sprintf("Last delivery to %s succeeded", channelName),
			LastTransitionTime: now,
		})
	} else {
		meta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
			Type:               condType,
			Status:             metav1.ConditionFalse,
			Reason:             "DeliveryFailed",
			Message:            fmt.Sprintf("Delivery to %s failed: %s", channelName, errMsg),
			LastTransitionTime: now,
		})
	}

	if err := r.Status().Update(ctx, &fresh); err != nil {
		logger.Error(err, "failed to update notification policy status")
	}
}

// initiateEscalation finds a matching EscalationPolicy and sets escalation annotations on the Issue.
func (r *NotificationReconciler) initiateEscalation(ctx context.Context, issue *platformv1alpha1.Issue) error {
	logger := log.FromContext(ctx).WithValues("issue", issue.Name)

	annotations := issue.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Skip if escalation is already initiated.
	if _, hasLevel := annotations[annotationEscalationLevel]; hasLevel {
		return nil
	}

	// Find matching escalation policy.
	var policies platformv1alpha1.EscalationPolicyList
	if err := r.List(ctx, &policies); err != nil {
		return fmt.Errorf("listing escalation policies: %w", err)
	}

	var matched *platformv1alpha1.EscalationPolicy
	var defaultPolicy *platformv1alpha1.EscalationPolicy

	for i := range policies.Items {
		p := &policies.Items[i]
		if !p.Spec.Enabled {
			continue
		}
		if p.Spec.DefaultPolicy {
			defaultPolicy = p
		}
		if r.escalationPolicyMatchesSeverity(p, issue.Spec.Severity) {
			matched = p
			break
		}
	}

	if matched == nil {
		matched = defaultPolicy
	}
	if matched == nil {
		logger.Info("no escalation policy found for issue")
		return nil
	}

	if len(matched.Spec.Levels) == 0 {
		logger.Info("escalation policy has no levels", "policy", matched.Name)
		return nil
	}

	now := time.Now().UTC()
	annotations[annotationEscalationLevel] = "0"
	annotations[annotationEscalationTime] = now.Format(time.RFC3339)
	annotations[annotationEscalationPolicy] = matched.Name
	issue.SetAnnotations(annotations)

	logger.Info("escalation initiated",
		"policy", matched.Name,
		"level", matched.Spec.Levels[0].Name,
	)

	escalationLevelReached.WithLabelValues(matched.Name, matched.Spec.Levels[0].Name).Inc()

	// Send notification for escalation level 0.
	r.sendEscalationNotification(ctx, issue, matched, 0)

	// Record active escalation on the policy status.
	return r.recordActiveEscalation(ctx, matched, issue, 0)
}

// escalationPolicyMatchesSeverity checks if the policy applies to the given severity.
func (r *NotificationReconciler) escalationPolicyMatchesSeverity(policy *platformv1alpha1.EscalationPolicy, severity platformv1alpha1.IssueSeverity) bool {
	if len(policy.Spec.Severities) == 0 {
		return true
	}
	for _, s := range policy.Spec.Severities {
		if s == severity {
			return true
		}
	}
	return false
}

// handleEscalationTimer checks if the current escalation level has timed out and advances to the next level.
// Returns the duration to requeue after (for the next level timeout), or 0 if no requeue is needed.
func (r *NotificationReconciler) handleEscalationTimer(ctx context.Context, issue *platformv1alpha1.Issue) (time.Duration, error) {
	logger := log.FromContext(ctx).WithValues("issue", issue.Name)

	annotations := issue.GetAnnotations()
	if annotations == nil {
		return 0, nil
	}

	levelStr, hasLevel := annotations[annotationEscalationLevel]
	if !hasLevel {
		return 0, nil
	}
	timeStr, hasTime := annotations[annotationEscalationTime]
	if !hasTime {
		return 0, nil
	}
	policyName, hasPolicy := annotations[annotationEscalationPolicy]
	if !hasPolicy {
		return 0, nil
	}

	// If the issue is resolved, clean up escalation.
	if issue.Status.State == platformv1alpha1.IssueStateResolved {
		delete(annotations, annotationEscalationLevel)
		delete(annotations, annotationEscalationTime)
		delete(annotations, annotationEscalationPolicy)
		issue.SetAnnotations(annotations)
		return 0, nil
	}

	currentLevel, err := strconv.Atoi(levelStr)
	if err != nil {
		return 0, fmt.Errorf("parsing escalation level: %w", err)
	}

	escalationTime, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return 0, fmt.Errorf("parsing escalation time: %w", err)
	}

	// Fetch the escalation policy.
	var policy platformv1alpha1.EscalationPolicy
	if err := r.Get(ctx, types.NamespacedName{Name: policyName, Namespace: issue.Namespace}, &policy); err != nil {
		if errors.IsNotFound(err) {
			// Try cluster-scoped or all namespaces.
			var policies platformv1alpha1.EscalationPolicyList
			if listErr := r.List(ctx, &policies); listErr != nil {
				return 0, fmt.Errorf("listing escalation policies: %w", listErr)
			}
			found := false
			for i := range policies.Items {
				if policies.Items[i].Name == policyName {
					policy = policies.Items[i]
					found = true
					break
				}
			}
			if !found {
				return 0, fmt.Errorf("escalation policy %s not found", policyName)
			}
		} else {
			return 0, fmt.Errorf("fetching escalation policy: %w", err)
		}
	}

	if currentLevel >= len(policy.Spec.Levels) {
		// Already at max level, no further escalation.
		return 0, nil
	}

	level := policy.Spec.Levels[currentLevel]
	timeout := time.Duration(level.TimeoutMinutes) * time.Minute
	elapsed := time.Since(escalationTime)

	if elapsed < timeout {
		// Not yet timed out; requeue for when it does.
		return timeout - elapsed, nil
	}

	// Timeout exceeded — advance to the next level.
	nextLevel := currentLevel + 1
	if nextLevel >= len(policy.Spec.Levels) {
		logger.Info("escalation reached maximum level", "policy", policyName, "level", currentLevel)
		return 0, nil
	}

	now := time.Now().UTC()
	annotations[annotationEscalationLevel] = strconv.Itoa(nextLevel)
	annotations[annotationEscalationTime] = now.Format(time.RFC3339)
	issue.SetAnnotations(annotations)

	nextLevelSpec := policy.Spec.Levels[nextLevel]
	logger.Info("escalation advanced",
		"policy", policyName,
		"from_level", level.Name,
		"to_level", nextLevelSpec.Name,
	)

	escalationLevelReached.WithLabelValues(policyName, nextLevelSpec.Name).Inc()

	// Send notification for the new escalation level.
	r.sendEscalationNotification(ctx, issue, &policy, nextLevel)

	// Update active escalation on the policy status.
	if err := r.recordActiveEscalation(ctx, &policy, issue, int32(nextLevel)); err != nil {
		logger.Error(err, "failed to record active escalation")
	}

	// Return the timeout for the next level.
	nextTimeout := time.Duration(nextLevelSpec.TimeoutMinutes) * time.Minute
	return nextTimeout, nil
}

// sendEscalationNotification sends notifications for a specific escalation level.
func (r *NotificationReconciler) sendEscalationNotification(ctx context.Context, issue *platformv1alpha1.Issue, policy *platformv1alpha1.EscalationPolicy, levelIdx int) {
	logger := log.FromContext(ctx)

	if levelIdx >= len(policy.Spec.Levels) {
		return
	}

	level := policy.Spec.Levels[levelIdx]
	severity := string(issue.Spec.Severity)

	// Build escalation-specific message.
	targetNames := make([]string, 0, len(level.Targets))
	for _, t := range level.Targets {
		targetNames = append(targetNames, fmt.Sprintf("%s:%s", t.Type, t.Name))
	}

	msg := &channels.NotificationMessage{
		Title: fmt.Sprintf("%s ESCALATION [%s] %s — Level %d: %s",
			channels.SeverityEmoji(severity),
			strings.ToUpper(severity),
			issue.Name,
			levelIdx+1,
			level.Name,
		),
		Body: fmt.Sprintf("Issue %s has been escalated to level %d (%s). Targets: %s. Timeout: %d minutes.",
			issue.Name,
			levelIdx+1,
			level.Name,
			strings.Join(targetNames, ", "),
			level.TimeoutMinutes,
		),
		Severity:  severity,
		IssueName: issue.Name,
		Namespace: issue.Spec.Resource.Namespace,
		Resource:  fmt.Sprintf("%s/%s", issue.Spec.Resource.Kind, issue.Spec.Resource.Name),
		State:     string(issue.Status.State),
		Timestamp: time.Now(),
		Fields: map[string]string{
			"EscalationPolicy": policy.Name,
			"EscalationLevel":  level.Name,
			"Targets":          strings.Join(targetNames, ", "),
		},
		Color: channels.SeverityColor(severity),
	}

	// If the escalation level specifies notification channels, use those.
	if len(level.NotifyChannels) > 0 {
		// Find notification policies to look up channel configs.
		var notifPolicies platformv1alpha1.NotificationPolicyList
		if err := r.List(ctx, &notifPolicies); err != nil {
			logger.Error(err, "failed to list notification policies for escalation")
			return
		}

		for _, np := range notifPolicies.Items {
			if !np.Spec.Enabled {
				continue
			}
			for _, ch := range np.Spec.Channels {
				for _, targetChannel := range level.NotifyChannels {
					if ch.Name != targetChannel {
						continue
					}
					config, err := r.resolveChannelConfig(ctx, np.Namespace, ch)
					if err != nil {
						logger.Error(err, "failed to resolve escalation channel config", "channel", ch.Name)
						notificationsFailedTotal.WithLabelValues(string(ch.Type), "config_resolve").Inc()
						continue
					}
					sender, err := channels.NewSender(string(ch.Type), config)
					if err != nil {
						logger.Error(err, "failed to create escalation sender", "channel", ch.Name)
						notificationsFailedTotal.WithLabelValues(string(ch.Type), "sender_create").Inc()
						continue
					}
					if err := sender.Send(ctx, msg); err != nil {
						logger.Error(err, "failed to send escalation notification", "channel", ch.Name)
						notificationsSentTotal.WithLabelValues(string(ch.Type), severity, "failure").Inc()
						notificationsFailedTotal.WithLabelValues(string(ch.Type), "send").Inc()
					} else {
						logger.Info("escalation notification sent", "channel", ch.Name, "level", level.Name)
						notificationsSentTotal.WithLabelValues(string(ch.Type), severity, "success").Inc()
					}
				}
			}
		}
	}
}

// recordActiveEscalation updates the EscalationPolicy status with the active escalation info.
func (r *NotificationReconciler) recordActiveEscalation(ctx context.Context, policy *platformv1alpha1.EscalationPolicy, issue *platformv1alpha1.Issue, level int32) error {
	// Re-fetch to avoid conflicts.
	var fresh platformv1alpha1.EscalationPolicy
	if err := r.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, &fresh); err != nil {
		return fmt.Errorf("re-fetching escalation policy: %w", err)
	}

	now := metav1.Now()

	// Update or add the active escalation entry.
	found := false
	for i := range fresh.Status.ActiveEscalations {
		if fresh.Status.ActiveEscalations[i].IssueName == issue.Name {
			fresh.Status.ActiveEscalations[i].CurrentLevel = level
			fresh.Status.ActiveEscalations[i].EscalatedAt = now
			found = true
			break
		}
	}
	if !found {
		fresh.Status.ActiveEscalations = append(fresh.Status.ActiveEscalations, platformv1alpha1.ActiveEscalation{
			IssueName:    issue.Name,
			CurrentLevel: level,
			EscalatedAt:  now,
		})
		fresh.Status.TotalEscalations++
	}

	return r.Status().Update(ctx, &fresh)
}

// parseDuration parses a duration string with a fallback default.
func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// SetupWithManager sets up the controller with the Manager.
func (r *NotificationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("notification").
		For(&platformv1alpha1.Issue{}).
		Complete(r)
}
