package controllers

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

var (
	sloCurrentValue = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "slo_current_value",
		Help:      "Current SLI value for the SLO.",
	}, []string{"service", "slo_name"})

	sloErrorBudgetRemaining = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "slo_error_budget_remaining",
		Help:      "Remaining error budget fraction (0.0-1.0).",
	}, []string{"service", "slo_name"})

	sloBurnRate = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "slo_burn_rate",
		Help:      "Current burn rate for a given window.",
	}, []string{"service", "slo_name", "window"})

	sloViolationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "slo_violations_total",
		Help:      "Total SLO violations by severity.",
	}, []string{"service", "slo_name", "severity"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		sloCurrentValue,
		sloErrorBudgetRemaining,
		sloBurnRate,
		sloViolationsTotal,
	)
}

// SLOReconciler reconciles ServiceLevelObjective objects.
type SLOReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=servicelevelobjectives,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=servicelevelobjectives/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=servicelevelobjectives/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=anomalies,verbs=get;list;watch

func (r *SLOReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var slo platformv1alpha1.ServiceLevelObjective
	if err := r.Get(ctx, req.NamespacedName, &slo); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !slo.Spec.Enabled {
		logger.Info("SLO is disabled, skipping", "name", slo.Name)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// a. Calculate current SLI value
	currentValue, err := r.calculateSLI(ctx, &slo)
	if err != nil {
		logger.Error(err, "Failed to calculate SLI", "slo", slo.Name)
		meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "SLICalculationFailed",
			Message:            fmt.Sprintf("Failed to calculate SLI: %v", err),
			LastTransitionTime: metav1.Now(),
		})
		if updateErr := r.Status().Update(ctx, &slo); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// b. Calculate error budget
	target := slo.Spec.Target.Percentage
	errorBudgetTotal := 1.0 - (target / 100.0)
	var consumed, remaining, consumedPercentage float64
	if errorBudgetTotal > 0 {
		consumed = (1.0 - currentValue) / errorBudgetTotal
		remaining = math.Max(0, 1.0-consumed)
		consumedPercentage = consumed * 100.0
	} else {
		// 100% target means zero error budget
		if currentValue < 1.0 {
			consumed = 1.0
			remaining = 0.0
			consumedPercentage = 100.0
		} else {
			consumed = 0.0
			remaining = 1.0
			consumedPercentage = 0.0
		}
	}

	// c. Calculate multi-window burn rates
	burnRate1h := r.calculateBurnRate(ctx, &slo, parseSLODuration("1h"))
	burnRate6h := r.calculateBurnRate(ctx, &slo, parseSLODuration("6h"))
	burnRate24h := r.calculateBurnRate(ctx, &slo, parseSLODuration("24h"))
	burnRate72h := r.calculateBurnRate(ctx, &slo, parseSLODuration("72h"))

	// Update status
	now := metav1.Now()
	slo.Status.CurrentValue = currentValue
	slo.Status.TargetMet = currentValue >= (target / 100.0)
	slo.Status.ErrorBudgetTotal = errorBudgetTotal
	slo.Status.ErrorBudgetRemaining = remaining
	slo.Status.ErrorBudgetConsumedPercentage = consumedPercentage
	slo.Status.BurnRate1h = burnRate1h
	slo.Status.BurnRate6h = burnRate6h
	slo.Status.BurnRate24h = burnRate24h
	slo.Status.BurnRate72h = burnRate72h
	slo.Status.LastCalculatedAt = &now

	// Update Prometheus metrics
	sloCurrentValue.WithLabelValues(slo.Spec.ServiceName, slo.Name).Set(currentValue)
	sloErrorBudgetRemaining.WithLabelValues(slo.Spec.ServiceName, slo.Name).Set(remaining)
	sloBurnRate.WithLabelValues(slo.Spec.ServiceName, slo.Name, "1h").Set(burnRate1h)
	sloBurnRate.WithLabelValues(slo.Spec.ServiceName, slo.Name, "6h").Set(burnRate6h)
	sloBurnRate.WithLabelValues(slo.Spec.ServiceName, slo.Name, "24h").Set(burnRate24h)
	sloBurnRate.WithLabelValues(slo.Spec.ServiceName, slo.Name, "72h").Set(burnRate72h)

	// d. Check burn rate alerts
	r.checkBurnRateAlerts(ctx, &slo, errorBudgetTotal)

	// e. Check budget exhaustion
	r.checkBudgetExhaustion(ctx, &slo)

	// f. Set ready condition
	condMessage := fmt.Sprintf("SLI=%.4f, target=%.2f%%, budget_remaining=%.1f%%",
		currentValue, target, remaining*100.0)
	condStatus := metav1.ConditionTrue
	condReason := "SLOMet"
	if !slo.Status.TargetMet {
		condStatus = metav1.ConditionFalse
		condReason = "SLONotMet"
	}
	meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		Reason:             condReason,
		Message:            condMessage,
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, &slo); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("SLO reconciled",
		"name", slo.Name,
		"currentValue", currentValue,
		"targetMet", slo.Status.TargetMet,
		"budgetRemaining", remaining,
		"burnRate1h", burnRate1h,
	)

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// calculateSLI computes the current SLI value based on the indicator type.
func (r *SLOReconciler) calculateSLI(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective) (float64, error) {
	window := parseSLODuration(slo.Spec.Target.Window)

	switch slo.Spec.Indicator.Type {
	case platformv1alpha1.SLOIndicatorAvailability:
		return r.calculateAvailabilitySLI(ctx, slo, window)
	case platformv1alpha1.SLOIndicatorErrorRate:
		return r.calculateErrorRateSLI(ctx, slo, window)
	case platformv1alpha1.SLOIndicatorLatency:
		return r.calculateLatencySLI(ctx, slo, window)
	case platformv1alpha1.SLOIndicatorThroughput:
		return r.calculateThroughputSLI(ctx, slo, window)
	default:
		return 0, fmt.Errorf("unsupported indicator type: %s", slo.Spec.Indicator.Type)
	}
}

// calculateAvailabilitySLI computes: 1 - (total_incident_minutes / total_window_minutes)
func (r *SLOReconciler) calculateAvailabilitySLI(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective, window time.Duration) (float64, error) {
	totalWindowMinutes := window.Minutes()
	if totalWindowMinutes <= 0 {
		return 1.0, nil
	}

	incidentMinutes := countIncidentMinutes(ctx, r.Client, slo.Spec.ServiceName, slo.Namespace, window)
	sli := 1.0 - (incidentMinutes / totalWindowMinutes)
	if sli < 0 {
		sli = 0
	}
	return sli, nil
}

// calculateErrorRateSLI computes: 1 - (error_anomalies / total_anomalies)
func (r *SLOReconciler) calculateErrorRateSLI(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective, window time.Duration) (float64, error) {
	resourceName := ""
	if slo.Spec.Indicator.Resource != nil {
		resourceName = slo.Spec.Indicator.Resource.Name
	}

	errorSignals := []string{"error_rate"}
	errorCount, totalCount := countAnomalies(ctx, r.Client, resourceName, slo.Namespace, window, errorSignals)

	if totalCount == 0 {
		// No anomalies observed — perfect score
		return 1.0, nil
	}

	sli := 1.0 - (float64(errorCount) / float64(totalCount))
	if sli < 0 {
		sli = 0
	}
	return sli, nil
}

// calculateLatencySLI estimates latency SLI from issue durations when no Prometheus is available.
func (r *SLOReconciler) calculateLatencySLI(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective, window time.Duration) (float64, error) {
	if slo.Spec.Indicator.MetricSource == platformv1alpha1.SLOSourcePrometheus && slo.Spec.Indicator.PrometheusQuery != "" {
		// Prometheus integration placeholder: in production, query Prometheus here.
		// For now, fall through to issue-based estimation.
		_ = slo.Spec.Indicator.PrometheusQuery
	}

	// Estimate from issue durations: count issues with latency signal vs total
	resourceName := ""
	if slo.Spec.Indicator.Resource != nil {
		resourceName = slo.Spec.Indicator.Resource.Name
	}

	latencySignals := []string{"latency"}
	latencyCount, totalCount := countAnomalies(ctx, r.Client, resourceName, slo.Namespace, window, latencySignals)

	if totalCount == 0 {
		return 1.0, nil
	}

	// Proportion of requests within latency threshold
	sli := 1.0 - (float64(latencyCount) / float64(totalCount))
	if sli < 0 {
		sli = 0
	}
	return sli, nil
}

// calculateThroughputSLI estimates throughput from successful observations per time unit.
func (r *SLOReconciler) calculateThroughputSLI(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective, window time.Duration) (float64, error) {
	resourceName := ""
	if slo.Spec.Indicator.Resource != nil {
		resourceName = slo.Spec.Indicator.Resource.Name
	}

	// Count all anomalies (errors reduce throughput) vs total observations
	allErrorSignals := []string{"error_rate", "pod_restart", "oom_kill", "pod_not_ready", "deploy_failing"}
	errorCount, totalCount := countAnomalies(ctx, r.Client, resourceName, slo.Namespace, window, allErrorSignals)

	if totalCount == 0 {
		return 1.0, nil
	}

	sli := 1.0 - (float64(errorCount) / float64(totalCount))
	if sli < 0 {
		sli = 0
	}
	return sli, nil
}

// calculateBurnRate computes the burn rate for a given time window.
// burnRate = error_rate_in_window / errorBudgetTotal
func (r *SLOReconciler) calculateBurnRate(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective, window time.Duration) float64 {
	errorBudgetTotal := 1.0 - (slo.Spec.Target.Percentage / 100.0)
	if errorBudgetTotal <= 0 {
		return 0
	}

	windowMinutes := window.Minutes()
	if windowMinutes <= 0 {
		return 0
	}

	var errorRate float64

	switch slo.Spec.Indicator.Type {
	case platformv1alpha1.SLOIndicatorAvailability:
		incidentMinutes := countIncidentMinutes(ctx, r.Client, slo.Spec.ServiceName, slo.Namespace, window)
		errorRate = incidentMinutes / windowMinutes

	case platformv1alpha1.SLOIndicatorErrorRate:
		resourceName := ""
		if slo.Spec.Indicator.Resource != nil {
			resourceName = slo.Spec.Indicator.Resource.Name
		}
		errorCount, totalCount := countAnomalies(ctx, r.Client, resourceName, slo.Namespace, window, []string{"error_rate"})
		if totalCount > 0 {
			errorRate = float64(errorCount) / float64(totalCount)
		}

	case platformv1alpha1.SLOIndicatorLatency:
		resourceName := ""
		if slo.Spec.Indicator.Resource != nil {
			resourceName = slo.Spec.Indicator.Resource.Name
		}
		latencyCount, totalCount := countAnomalies(ctx, r.Client, resourceName, slo.Namespace, window, []string{"latency"})
		if totalCount > 0 {
			errorRate = float64(latencyCount) / float64(totalCount)
		}

	case platformv1alpha1.SLOIndicatorThroughput:
		resourceName := ""
		if slo.Spec.Indicator.Resource != nil {
			resourceName = slo.Spec.Indicator.Resource.Name
		}
		allErrors := []string{"error_rate", "pod_restart", "oom_kill", "pod_not_ready", "deploy_failing"}
		errorCount, totalCount := countAnomalies(ctx, r.Client, resourceName, slo.Namespace, window, allErrors)
		if totalCount > 0 {
			errorRate = float64(errorCount) / float64(totalCount)
		}
	}

	return errorRate / errorBudgetTotal
}

// checkBurnRateAlerts evaluates multi-window burn rate alert policies.
func (r *SLOReconciler) checkBurnRateAlerts(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective, errorBudgetTotal float64) {
	logger := log.FromContext(ctx)

	var newActiveAlerts []platformv1alpha1.SLOAlert

	for _, brw := range slo.Spec.AlertPolicy.BurnRateWindows {
		shortWindow := parseSLODuration(brw.ShortWindow)
		longWindow := parseSLODuration(brw.LongWindow)

		shortBurnRate := r.calculateBurnRate(ctx, slo, shortWindow)
		longBurnRate := r.calculateBurnRate(ctx, slo, longWindow)

		windowLabel := fmt.Sprintf("%s/%s", brw.ShortWindow, brw.LongWindow)

		if shortBurnRate >= brw.BurnRateThreshold && longBurnRate >= brw.BurnRateThreshold {
			// Both windows exceed threshold — fire alert
			logger.Info("Burn rate alert fired",
				"slo", slo.Name,
				"window", windowLabel,
				"shortBurnRate", shortBurnRate,
				"longBurnRate", longBurnRate,
				"threshold", brw.BurnRateThreshold,
				"severity", brw.Severity,
			)

			// Check if this alert is already active
			alreadyActive := false
			for _, existing := range slo.Status.ActiveAlerts {
				if existing.Window == windowLabel {
					alreadyActive = true
					newActiveAlerts = append(newActiveAlerts, existing)
					break
				}
			}

			if !alreadyActive {
				// New alert — create Issue CR
				alert := platformv1alpha1.SLOAlert{
					Window:   windowLabel,
					BurnRate: shortBurnRate,
					Severity: brw.Severity,
					FiredAt:  metav1.Now(),
				}
				newActiveAlerts = append(newActiveAlerts, alert)

				sloViolationsTotal.WithLabelValues(slo.Spec.ServiceName, slo.Name, string(brw.Severity)).Inc()

				if err := r.createSLOViolationIssue(ctx, slo, windowLabel, shortBurnRate, brw.Severity); err != nil {
					logger.Error(err, "Failed to create SLO violation issue", "slo", slo.Name)
				}
			}
		}
		// If burn rate drops below threshold, the alert is not added to newActiveAlerts (cleared)
	}

	slo.Status.ActiveAlerts = newActiveAlerts
}

// checkBudgetExhaustion creates a critical issue when error budget is fully consumed.
func (r *SLOReconciler) checkBudgetExhaustion(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective) {
	logger := log.FromContext(ctx)

	if !slo.Spec.AlertPolicy.PageOnBudgetExhausted {
		return
	}

	if slo.Status.ErrorBudgetRemaining > 0 {
		return
	}

	// Check if we already have an active budget exhaustion alert
	for _, alert := range slo.Status.ActiveAlerts {
		if alert.Window == "budget-exhausted" {
			return // already tracked
		}
	}

	logger.Info("Error budget exhausted, creating critical issue", "slo", slo.Name)

	sloViolationsTotal.WithLabelValues(slo.Spec.ServiceName, slo.Name, string(platformv1alpha1.IssueSeverityCritical)).Inc()

	if err := r.createSLOViolationIssue(ctx, slo, "budget-exhausted", slo.Status.BurnRate1h, platformv1alpha1.IssueSeverityCritical); err != nil {
		logger.Error(err, "Failed to create budget exhaustion issue", "slo", slo.Name)
		return
	}

	slo.Status.ActiveAlerts = append(slo.Status.ActiveAlerts, platformv1alpha1.SLOAlert{
		Window:   "budget-exhausted",
		BurnRate: slo.Status.BurnRate1h,
		Severity: platformv1alpha1.IssueSeverityCritical,
		FiredAt:  metav1.Now(),
	})
}

// createSLOViolationIssue creates an Issue CR for an SLO violation.
func (r *SLOReconciler) createSLOViolationIssue(ctx context.Context, slo *platformv1alpha1.ServiceLevelObjective, window string, burnRate float64, severity platformv1alpha1.IssueSeverity) error {
	issueName := sanitizeRunbookName(fmt.Sprintf("slo-%s-%s-%d", slo.Name, strings.ReplaceAll(window, "/", "-"), time.Now().Unix()))

	resourceRef := platformv1alpha1.ResourceRef{
		Kind:      "ServiceLevelObjective",
		Name:      slo.Name,
		Namespace: slo.Namespace,
	}
	if slo.Spec.Indicator.Resource != nil {
		resourceRef = *slo.Spec.Indicator.Resource
	}

	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{
			Name:      issueName,
			Namespace: slo.Namespace,
			Annotations: map[string]string{
				"platform.chatcli.io/signal-type": "slo_violation",
				"platform.chatcli.io/slo-name":    slo.Name,
				"platform.chatcli.io/slo-window":  window,
				"platform.chatcli.io/burn-rate":   fmt.Sprintf("%.4f", burnRate),
			},
			Labels: map[string]string{
				"platform.chatcli.io/signal":  "slo_violation",
				"platform.chatcli.io/service": slo.Spec.ServiceName,
			},
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, issue, func() error {
		if err := controllerutil.SetControllerReference(slo, issue, r.Scheme); err != nil {
			return err
		}
		issue.Spec = platformv1alpha1.IssueSpec{
			Severity:   severity,
			Source:     platformv1alpha1.IssueSourceWatcher,
			SignalType: "slo_violation",
			Resource:   resourceRef,
			Description: fmt.Sprintf(
				"SLO '%s' burn rate alert fired for window %s. Burn rate: %.2f, Service: %s, Current SLI: %.4f, Error budget remaining: %.1f%%",
				slo.Name, window, burnRate, slo.Spec.ServiceName, slo.Status.CurrentValue, slo.Status.ErrorBudgetRemaining*100,
			),
			RiskScore: calculateSLORiskScore(slo),
		}
		return nil
	})

	return err
}

// calculateSLORiskScore computes a risk score (0-100) based on SLO status.
func calculateSLORiskScore(slo *platformv1alpha1.ServiceLevelObjective) int32 {
	// Risk increases as error budget is consumed
	consumed := slo.Status.ErrorBudgetConsumedPercentage
	if consumed >= 100 {
		return 100
	}
	if consumed >= 90 {
		return 90
	}
	if consumed >= 75 {
		return 75
	}
	if consumed >= 50 {
		return 50
	}
	return int32(consumed)
}

// countIncidentMinutes counts total incident minutes for a service in the given window.
// It looks at Issues for the service and calculates Detected->Resolved durations.
func countIncidentMinutes(ctx context.Context, c client.Client, serviceName, namespace string, window time.Duration) float64 {
	windowStart := time.Now().Add(-window)

	var issues platformv1alpha1.IssueList
	if err := c.List(ctx, &issues, client.InNamespace(namespace)); err != nil {
		return 0
	}

	var totalMinutes float64
	for _, issue := range issues.Items {
		// Match by service name in labels or resource name
		isServiceMatch := false
		if issue.Labels != nil && issue.Labels["platform.chatcli.io/service"] == serviceName {
			isServiceMatch = true
		}
		if issue.Spec.Resource.Name == serviceName {
			isServiceMatch = true
		}
		if !isServiceMatch {
			continue
		}

		if issue.Status.DetectedAt == nil {
			continue
		}

		detectedAt := issue.Status.DetectedAt.Time
		if detectedAt.Before(windowStart) && issue.Status.ResolvedAt != nil && issue.Status.ResolvedAt.Time.Before(windowStart) {
			// Entirely outside window
			continue
		}

		// Determine start of incident within window
		incidentStart := detectedAt
		if incidentStart.Before(windowStart) {
			incidentStart = windowStart
		}

		// Determine end of incident
		var incidentEnd time.Time
		if issue.Status.ResolvedAt != nil {
			incidentEnd = issue.Status.ResolvedAt.Time
		} else {
			// Still active
			incidentEnd = time.Now()
		}

		duration := incidentEnd.Sub(incidentStart)
		if duration > 0 {
			totalMinutes += duration.Minutes()
		}
	}

	return totalMinutes
}

// countAnomalies counts error-type anomalies vs total anomalies in the given window.
func countAnomalies(ctx context.Context, c client.Client, resource, namespace string, window time.Duration, signalTypes []string) (errorCount, total int) {
	windowStart := time.Now().Add(-window)

	var anomalies platformv1alpha1.AnomalyList
	if err := c.List(ctx, &anomalies, client.InNamespace(namespace)); err != nil {
		return 0, 0
	}

	signalSet := make(map[string]bool, len(signalTypes))
	for _, s := range signalTypes {
		signalSet[s] = true
	}

	for _, a := range anomalies.Items {
		if a.CreationTimestamp.Time.Before(windowStart) {
			continue
		}

		// Filter by resource if specified
		if resource != "" && a.Spec.Resource.Name != resource {
			continue
		}

		total++
		if signalSet[string(a.Spec.SignalType)] {
			errorCount++
		}
	}

	return errorCount, total
}

// parseSLODuration parses duration strings including day-based formats like "7d", "30d", "90d"
// as well as standard Go durations like "1h", "6h", "24h".
func parseSLODuration(s string) time.Duration {
	if s == "" {
		return 0
	}

	// Handle day-based durations
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(numStr)
		if err != nil {
			return 0
		}
		return time.Duration(days) * 24 * time.Hour
	}

	// Standard Go duration parsing for h, m, s
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// SetupWithManager sets up the controller with the Manager.
func (r *SLOReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ServiceLevelObjective{}).
		Owns(&platformv1alpha1.Issue{}).
		Complete(r)
}
