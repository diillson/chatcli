package controllers

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

type ComplianceReporter struct {
	client client.Client
}

type ComplianceReport struct {
	Period             ReportPeriod
	IncidentMetrics    IncidentMetrics
	RemediationMetrics RemediationComplianceMetrics
	SLAMetrics         SLAComplianceMetrics
	ApprovalMetrics    ApprovalComplianceMetrics
	AuditSummary       AuditSummaryMetrics
}

type ReportPeriod struct{ Start, End time.Time }

type IncidentMetrics struct {
	TotalIncidents          int64
	BySeverity              map[string]int64
	ByState                 map[string]int64
	MTTD                    time.Duration
	MTTR                    time.Duration
	MeanRemediationAttempts float64
}

type RemediationComplianceMetrics struct {
	TotalRemediations   int64
	SuccessRate         float64
	ByActionType        map[string]ActionStats
	AutoRemediatedCount int64
	AgenticCount        int64
}

type ActionStats struct{ Count, Success, Failed int64 }

type SLAComplianceMetrics struct {
	CompliancePercentage    float64
	ResponseSLAViolations   int64
	ResolutionSLAViolations int64
	AverageResponseTime     time.Duration
	AverageResolutionTime   time.Duration
}

type ApprovalComplianceMetrics struct {
	TotalRequests       int64
	AutoApproved        int64
	ManualApproved      int64
	Rejected            int64
	Expired             int64
	AverageDecisionTime time.Duration
}

type AuditSummaryMetrics struct {
	TotalEvents int64
	BySeverity  map[string]int64
	ByEventType map[string]int64
}

func NewComplianceReporter(c client.Client) *ComplianceReporter {
	return &ComplianceReporter{client: c}
}

func (cr *ComplianceReporter) GenerateReport(ctx context.Context, namespace string, window time.Duration) (*ComplianceReport, error) {
	now := time.Now()
	start := now.Add(-window)
	report := &ComplianceReport{Period: ReportPeriod{Start: start, End: now}}

	// Issues
	var issues platformv1alpha1.IssueList
	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := cr.client.List(ctx, &issues, opts...); err != nil {
		return nil, err
	}

	report.IncidentMetrics.BySeverity = make(map[string]int64)
	report.IncidentMetrics.ByState = make(map[string]int64)
	var totalDetectDur, totalResolveDur time.Duration
	var detectCount, resolveCount int
	var totalAttempts int64

	for _, iss := range issues.Items {
		if iss.CreationTimestamp.Time.Before(start) {
			continue
		}
		report.IncidentMetrics.TotalIncidents++
		report.IncidentMetrics.BySeverity[string(iss.Spec.Severity)]++
		report.IncidentMetrics.ByState[string(iss.Status.State)]++
		totalAttempts += int64(iss.Status.RemediationAttempts)

		if iss.Status.DetectedAt != nil {
			totalDetectDur += iss.Status.DetectedAt.Time.Sub(iss.CreationTimestamp.Time)
			detectCount++
		}
		if iss.Status.DetectedAt != nil && iss.Status.ResolvedAt != nil {
			totalResolveDur += iss.Status.ResolvedAt.Time.Sub(iss.Status.DetectedAt.Time)
			resolveCount++
		}
	}
	if detectCount > 0 {
		report.IncidentMetrics.MTTD = totalDetectDur / time.Duration(detectCount)
	}
	if resolveCount > 0 {
		report.IncidentMetrics.MTTR = totalResolveDur / time.Duration(resolveCount)
	}
	if report.IncidentMetrics.TotalIncidents > 0 {
		report.IncidentMetrics.MeanRemediationAttempts = float64(totalAttempts) / float64(report.IncidentMetrics.TotalIncidents)
	}

	// Remediations
	var plans platformv1alpha1.RemediationPlanList
	if err := cr.client.List(ctx, &plans, opts...); err != nil {
		return nil, err
	}

	report.RemediationMetrics.ByActionType = make(map[string]ActionStats)
	var completed, failed int64
	for _, plan := range plans.Items {
		if plan.CreationTimestamp.Time.Before(start) {
			continue
		}
		report.RemediationMetrics.TotalRemediations++
		if plan.Spec.AgenticMode {
			report.RemediationMetrics.AgenticCount++
		}
		switch plan.Status.State {
		case platformv1alpha1.RemediationStateCompleted:
			completed++
			report.RemediationMetrics.AutoRemediatedCount++
		case platformv1alpha1.RemediationStateFailed, platformv1alpha1.RemediationStateRolledBack:
			failed++
		}
		for _, a := range plan.Spec.Actions {
			as := report.RemediationMetrics.ByActionType[string(a.Type)]
			as.Count++
			if plan.Status.State == platformv1alpha1.RemediationStateCompleted {
				as.Success++
			} else if plan.Status.State == platformv1alpha1.RemediationStateFailed {
				as.Failed++
			}
			report.RemediationMetrics.ByActionType[string(a.Type)] = as
		}
	}
	if completed+failed > 0 {
		report.RemediationMetrics.SuccessRate = float64(completed) / float64(completed+failed) * 100
	}

	// Approvals
	var approvals platformv1alpha1.ApprovalRequestList
	if err := cr.client.List(ctx, &approvals, opts...); err == nil {
		var totalDecisionDur time.Duration
		var decisionCount int
		for _, ar := range approvals.Items {
			if ar.CreationTimestamp.Time.Before(start) {
				continue
			}
			report.ApprovalMetrics.TotalRequests++
			switch ar.Status.State {
			case platformv1alpha1.ApprovalStateApproved:
				if ar.Status.AutoApproved {
					report.ApprovalMetrics.AutoApproved++
				} else {
					report.ApprovalMetrics.ManualApproved++
				}
				if ar.Status.ApprovedAt != nil {
					totalDecisionDur += ar.Status.ApprovedAt.Time.Sub(ar.CreationTimestamp.Time)
					decisionCount++
				}
			case platformv1alpha1.ApprovalStateRejected:
				report.ApprovalMetrics.Rejected++
			case platformv1alpha1.ApprovalStateExpired:
				report.ApprovalMetrics.Expired++
			}
		}
		if decisionCount > 0 {
			report.ApprovalMetrics.AverageDecisionTime = totalDecisionDur / time.Duration(decisionCount)
		}
	}

	// SLA Compliance — calculate from issues and SLA definitions
	// CompliancePercentage = ((totalIncidents - slaViolations) / totalIncidents) * 100
	totalIncidents := report.IncidentMetrics.TotalIncidents
	if totalIncidents > 0 {
		// Count SLA violations: issues that were escalated (indicates SLA breach)
		// or have SLA-related annotations
		var violations int64
		for _, iss := range issues.Items {
			if iss.CreationTimestamp.Time.Before(start) {
				continue
			}
			// Escalated = SLA resolution time exceeded
			if iss.Status.State == platformv1alpha1.IssueStateEscalated {
				report.SLAMetrics.ResolutionSLAViolations++
				violations++
			}
			// Check if response SLA was violated (DetectedAt too late)
			if iss.Status.DetectedAt != nil {
				detectTime := iss.Status.DetectedAt.Time.Sub(iss.CreationTimestamp.Time)
				report.SLAMetrics.AverageResponseTime += detectTime
			}
			if iss.Status.ResolvedAt != nil {
				resolveTime := iss.Status.ResolvedAt.Time.Sub(iss.CreationTimestamp.Time)
				report.SLAMetrics.AverageResolutionTime += resolveTime
			}
		}
		if detectCount > 0 {
			report.SLAMetrics.AverageResponseTime = report.SLAMetrics.AverageResponseTime / time.Duration(detectCount)
		}
		if resolveCount > 0 {
			report.SLAMetrics.AverageResolutionTime = report.SLAMetrics.AverageResolutionTime / time.Duration(resolveCount)
		}
		report.SLAMetrics.CompliancePercentage = float64(totalIncidents-violations) / float64(totalIncidents) * 100
	} else {
		report.SLAMetrics.CompliancePercentage = 100 // No incidents = 100% compliance
	}

	// Audit events
	var auditEvents platformv1alpha1.AuditEventList
	if err := cr.client.List(ctx, &auditEvents, opts...); err == nil {
		report.AuditSummary.BySeverity = make(map[string]int64)
		report.AuditSummary.ByEventType = make(map[string]int64)
		for _, ae := range auditEvents.Items {
			if ae.CreationTimestamp.Time.Before(start) {
				continue
			}
			report.AuditSummary.TotalEvents++
			report.AuditSummary.BySeverity[ae.Spec.Severity]++
			report.AuditSummary.ByEventType[ae.Spec.EventType]++
		}
	}

	return report, nil
}
