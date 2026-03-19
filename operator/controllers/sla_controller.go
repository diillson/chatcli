package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

var (
	slaResponseTime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "chatcli", Subsystem: "operator", Name: "sla_response_time_seconds",
		Help: "Time from detection to first analysis.", Buckets: prometheus.ExponentialBuckets(10, 2, 12),
	}, []string{"severity"})
	slaResolutionTime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "chatcli", Subsystem: "operator", Name: "sla_resolution_time_seconds",
		Help: "Time from detection to resolution.", Buckets: prometheus.ExponentialBuckets(30, 2, 14),
	}, []string{"severity"})
	slaViolationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli", Subsystem: "operator", Name: "sla_violations_total",
		Help: "Total SLA violations.",
	}, []string{"severity", "type"})
	slaCompliancePct = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "chatcli", Subsystem: "operator", Name: "sla_compliance_percentage",
		Help: "SLA compliance percentage.",
	}, []string{"severity"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(slaResponseTime, slaResolutionTime, slaViolationsTotal, slaCompliancePct)
}

type SLAReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *SLAReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, req.NamespacedName, &issue); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	sla, err := r.findMatchingSLA(ctx, issue.Namespace, issue.Spec.Severity)
	if err != nil || sla == nil {
		return ctrl.Result{}, err
	}

	responseThreshold, err := time.ParseDuration(sla.Spec.ResponseTime)
	if err != nil {
		lg.Error(err, "Invalid responseTime", "sla", sla.Name)
		return ctrl.Result{}, nil
	}
	resolutionThreshold, err := time.ParseDuration(sla.Spec.ResolutionTime)
	if err != nil {
		lg.Error(err, "Invalid resolutionTime", "sla", sla.Name)
		return ctrl.Result{}, nil
	}

	now := time.Now()
	detectedAt := issue.CreationTimestamp.Time
	if issue.Status.DetectedAt != nil {
		detectedAt = issue.Status.DetectedAt.Time
	}

	violated := alreadyViolatedStr(issue)

	switch issue.Status.State {
	case platformv1alpha1.IssueStateAnalyzing, platformv1alpha1.IssueStateRemediating:
		if issue.Annotations == nil || issue.Annotations["platform.chatcli.io/sla-response-checked"] == "" {
			elapsed := r.calcElapsed(detectedAt, now, sla)
			slaResponseTime.WithLabelValues(string(issue.Spec.Severity)).Observe(elapsed.Seconds())
			if elapsed > responseThreshold && !strings.Contains(violated, "response") {
				lg.Info("SLA response violation", "issue", issue.Name, "elapsed", elapsed, "threshold", responseThreshold)
				r.recordViolation(ctx, sla, &issue, "response", elapsed, responseThreshold)
			}
			if issue.Annotations == nil {
				issue.Annotations = make(map[string]string)
			}
			issue.Annotations["platform.chatcli.io/sla-response-checked"] = "true"
			_ = r.Update(ctx, &issue)
		}

	case platformv1alpha1.IssueStateResolved:
		if issue.Annotations == nil || issue.Annotations["platform.chatcli.io/sla-resolution-checked"] == "" {
			resolvedAt := now
			if issue.Status.ResolvedAt != nil {
				resolvedAt = issue.Status.ResolvedAt.Time
			}
			elapsed := r.calcElapsed(detectedAt, resolvedAt, sla)
			slaResolutionTime.WithLabelValues(string(issue.Spec.Severity)).Observe(elapsed.Seconds())
			if elapsed > resolutionThreshold && !strings.Contains(violated, "resolution") {
				r.recordViolation(ctx, sla, &issue, "resolution", elapsed, resolutionThreshold)
			}
			sla.Status.TotalIssuesTracked++
			r.updateCompliance(sla)
			r.updateAverageTimes(ctx, sla)
			_ = r.Status().Update(ctx, sla)
			if issue.Annotations == nil {
				issue.Annotations = make(map[string]string)
			}
			issue.Annotations["platform.chatcli.io/sla-resolution-checked"] = "true"
			_ = r.Update(ctx, &issue)
		}
		return ctrl.Result{}, nil

	case platformv1alpha1.IssueStateEscalated, platformv1alpha1.IssueStateFailed:
		if issue.Annotations == nil || issue.Annotations["platform.chatcli.io/sla-resolution-checked"] == "" {
			elapsed := r.calcElapsed(detectedAt, now, sla)
			if elapsed > resolutionThreshold && !strings.Contains(violated, "resolution") {
				r.recordViolation(ctx, sla, &issue, "resolution", elapsed, resolutionThreshold)
			}
			sla.Status.TotalIssuesTracked++
			r.updateCompliance(sla)
			_ = r.Status().Update(ctx, sla)
			if issue.Annotations == nil {
				issue.Annotations = make(map[string]string)
			}
			issue.Annotations["platform.chatcli.io/sla-resolution-checked"] = "true"
			_ = r.Update(ctx, &issue)
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func alreadyViolatedStr(issue platformv1alpha1.Issue) string {
	if issue.Annotations == nil {
		return ""
	}
	return issue.Annotations["platform.chatcli.io/sla-violated"]
}

func (r *SLAReconciler) calcElapsed(start, end time.Time, sla *platformv1alpha1.IncidentSLA) time.Duration {
	if !sla.Spec.BusinessHoursOnly || sla.Spec.BusinessHours == nil {
		return end.Sub(start)
	}
	return calculateBusinessDuration(start, end, *sla.Spec.BusinessHours)
}

func calculateBusinessDuration(start, end time.Time, bh platformv1alpha1.BusinessHoursSpec) time.Duration {
	loc, err := time.LoadLocation(bh.Timezone)
	if err != nil {
		loc = time.UTC
	}
	start = start.In(loc)
	end = end.In(loc)
	if end.Before(start) {
		return 0
	}
	workDays := make(map[string]bool)
	for _, d := range bh.WorkDays {
		workDays[d] = true
	}
	var total time.Duration
	current := start
	for current.Before(end) {
		dayName := current.Weekday().String()
		if !workDays[dayName] {
			current = time.Date(current.Year(), current.Month(), current.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		dayStart := time.Date(current.Year(), current.Month(), current.Day(), int(bh.StartHour), 0, 0, 0, loc)
		dayEnd := time.Date(current.Year(), current.Month(), current.Day(), int(bh.EndHour), 0, 0, 0, loc)
		effStart := current
		if effStart.Before(dayStart) {
			effStart = dayStart
		}
		effEnd := end
		if effEnd.After(dayEnd) {
			effEnd = dayEnd
		}
		if effStart.Before(effEnd) {
			total += effEnd.Sub(effStart)
		}
		current = time.Date(current.Year(), current.Month(), current.Day()+1, 0, 0, 0, 0, loc)
	}
	return total
}

func (r *SLAReconciler) findMatchingSLA(ctx context.Context, namespace string, severity platformv1alpha1.IssueSeverity) (*platformv1alpha1.IncidentSLA, error) {
	var slas platformv1alpha1.IncidentSLAList
	if err := r.List(ctx, &slas, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	var allSLAs platformv1alpha1.IncidentSLAList
	if err := r.List(ctx, &allSLAs); err != nil {
		return nil, err
	}
	for _, s := range append(slas.Items, allSLAs.Items...) {
		if s.Spec.Severity == severity {
			return &s, nil
		}
	}
	return nil, nil
}

func (r *SLAReconciler) recordViolation(ctx context.Context, sla *platformv1alpha1.IncidentSLA, issue *platformv1alpha1.Issue, vType string, elapsed, threshold time.Duration) {
	now := metav1.Now()
	record := platformv1alpha1.SLAViolationRecord{
		IssueName: issue.Name, Type: vType,
		Elapsed: elapsed.Round(time.Second).String(), Threshold: threshold.String(), ViolatedAt: now,
	}
	sla.Status.RecentViolations = append(sla.Status.RecentViolations, record)
	if len(sla.Status.RecentViolations) > 50 {
		sla.Status.RecentViolations = sla.Status.RecentViolations[len(sla.Status.RecentViolations)-50:]
	}
	sla.Status.TotalViolations++
	sla.Status.ActiveViolations++
	sla.Status.LastViolationAt = &now
	slaViolationsTotal.WithLabelValues(string(sla.Spec.Severity), vType).Inc()
	r.updateCompliance(sla)
	meta.SetStatusCondition(&sla.Status.Conditions, metav1.Condition{
		Type: "SLAViolation", Status: metav1.ConditionTrue,
		Reason:             fmt.Sprintf("%sViolation", vType),
		Message:            fmt.Sprintf("Issue %s violated %s SLA: %s > %s", issue.Name, vType, elapsed.Round(time.Second), threshold),
		LastTransitionTime: now,
	})
	_ = r.Status().Update(ctx, sla)
	if issue.Annotations == nil {
		issue.Annotations = make(map[string]string)
	}
	existing := issue.Annotations["platform.chatcli.io/sla-violated"]
	if existing != "" {
		issue.Annotations["platform.chatcli.io/sla-violated"] = existing + "," + vType
	} else {
		issue.Annotations["platform.chatcli.io/sla-violated"] = vType
	}
	_ = r.Update(ctx, issue)
}

func (r *SLAReconciler) updateCompliance(sla *platformv1alpha1.IncidentSLA) {
	if sla.Status.TotalIssuesTracked == 0 {
		sla.Status.CompliancePercentage = 100
	} else {
		sla.Status.CompliancePercentage = float64(sla.Status.TotalIssuesTracked-sla.Status.TotalViolations) / float64(sla.Status.TotalIssuesTracked) * 100
		if sla.Status.CompliancePercentage < 0 {
			sla.Status.CompliancePercentage = 0
		}
	}
	slaCompliancePct.WithLabelValues(string(sla.Spec.Severity)).Set(sla.Status.CompliancePercentage)
}

func (r *SLAReconciler) updateAverageTimes(ctx context.Context, sla *platformv1alpha1.IncidentSLA) {
	var issues platformv1alpha1.IssueList
	if err := r.List(ctx, &issues); err != nil {
		return
	}
	var totalResponse, totalResolution time.Duration
	var rCount, resCount int
	for _, iss := range issues.Items {
		if iss.Spec.Severity != sla.Spec.Severity {
			continue
		}
		detectedAt := iss.CreationTimestamp.Time
		if iss.Status.DetectedAt != nil {
			detectedAt = iss.Status.DetectedAt.Time
		}
		if iss.Status.State != "" && iss.Status.State != platformv1alpha1.IssueStateDetected {
			for _, cond := range iss.Status.Conditions {
				if cond.Type == "Analyzing" && cond.Status == metav1.ConditionTrue {
					totalResponse += r.calcElapsed(detectedAt, cond.LastTransitionTime.Time, sla)
					rCount++
					break
				}
			}
		}
		if iss.Status.ResolvedAt != nil {
			totalResolution += r.calcElapsed(detectedAt, iss.Status.ResolvedAt.Time, sla)
			resCount++
		}
	}
	if rCount > 0 {
		sla.Status.AverageResponseTime = (totalResponse / time.Duration(rCount)).Round(time.Second).String()
	}
	if resCount > 0 {
		sla.Status.AverageResolutionTime = (totalResolution / time.Duration(resCount)).Round(time.Second).String()
	}
}

func (r *SLAReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&platformv1alpha1.Issue{}).Complete(r)
}
