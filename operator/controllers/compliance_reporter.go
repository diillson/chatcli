package controllers

import (
	"context"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

type ComplianceReporter struct {
	client client.Client
}

// ComplianceReport is served as-is by GET /api/v1/analytics/compliance.
// The JSON keys are the Go field names (PascalCase) and every time.Duration
// is an integer number of nanoseconds: the explicit tags pin that wire
// format so a rename can never change it silently.
type ComplianceReport struct {
	Period             ReportPeriod                 `json:"Period"`
	IncidentMetrics    IncidentMetrics              `json:"IncidentMetrics"`
	RemediationMetrics RemediationComplianceMetrics `json:"RemediationMetrics"`
	SLAMetrics         SLAComplianceMetrics         `json:"SLAMetrics"`
	ApprovalMetrics    ApprovalComplianceMetrics    `json:"ApprovalMetrics"`
	AuditSummary       AuditSummaryMetrics          `json:"AuditSummary"`
	// IncidentSLAs lists each IncidentSLA in scope with the counters its
	// controller keeps.
	IncidentSLAs []IncidentSLASummary `json:"IncidentSLAs,omitempty"`
}

type ReportPeriod struct{ Start, End time.Time }

type IncidentMetrics struct {
	TotalIncidents          int64            `json:"TotalIncidents"`
	BySeverity              map[string]int64 `json:"BySeverity"`
	ByState                 map[string]int64 `json:"ByState"`
	MTTD                    time.Duration    `json:"MTTD"`
	MTTR                    time.Duration    `json:"MTTR"`
	MeanRemediationAttempts float64          `json:"MeanRemediationAttempts"`
}

type RemediationComplianceMetrics struct {
	TotalRemediations   int64                  `json:"TotalRemediations"`
	SuccessRate         float64                `json:"SuccessRate"`
	ByActionType        map[string]ActionStats `json:"ByActionType"`
	AutoRemediatedCount int64                  `json:"AutoRemediatedCount"`
	AgenticCount        int64                  `json:"AgenticCount"`
}

type ActionStats struct{ Count, Success, Failed int64 }

// SLAComplianceMetrics counts the violations the IncidentSLA controller
// recorded on the Issues of the period. Without any IncidentSLA in scope it
// falls back to counting Escalated Issues as resolution violations.
type SLAComplianceMetrics struct {
	CompliancePercentage    float64       `json:"CompliancePercentage"`
	ResponseSLAViolations   int64         `json:"ResponseSLAViolations"`
	ResolutionSLAViolations int64         `json:"ResolutionSLAViolations"`
	AverageResponseTime     time.Duration `json:"AverageResponseTime"`
	AverageResolutionTime   time.Duration `json:"AverageResolutionTime"`
}

// IncidentSLASummary is one IncidentSLA as its controller reports it.
type IncidentSLASummary struct {
	Name                 string  `json:"Name"`
	Namespace            string  `json:"Namespace"`
	Severity             string  `json:"Severity"`
	ResponseTime         string  `json:"ResponseTime"`
	ResolutionTime       string  `json:"ResolutionTime"`
	CompliancePercentage float64 `json:"CompliancePercentage"`
	ActiveViolations     int32   `json:"ActiveViolations"`
	TotalViolations      int64   `json:"TotalViolations"`
	TotalIssuesTracked   int64   `json:"TotalIssuesTracked"`
}

type ApprovalComplianceMetrics struct {
	TotalRequests       int64         `json:"TotalRequests"`
	AutoApproved        int64         `json:"AutoApproved"`
	ManualApproved      int64         `json:"ManualApproved"`
	Rejected            int64         `json:"Rejected"`
	Expired             int64         `json:"Expired"`
	AverageDecisionTime time.Duration `json:"AverageDecisionTime"`
}

type AuditSummaryMetrics struct {
	TotalEvents int64            `json:"TotalEvents"`
	BySeverity  map[string]int64 `json:"BySeverity"`
	ByEventType map[string]int64 `json:"ByEventType"`
}

func NewComplianceReporter(c client.Client) *ComplianceReporter {
	return &ComplianceReporter{client: c}
}

// GenerateReport covers the window that ends now.
func (cr *ComplianceReporter) GenerateReport(ctx context.Context, namespace string, window time.Duration) (*ComplianceReport, error) {
	now := time.Now()
	return cr.GenerateReportForPeriod(ctx, namespace, now.Add(-window), now)
}

// GenerateReportForPeriod covers objects created in [start, end].
func (cr *ComplianceReporter) GenerateReportForPeriod(ctx context.Context, namespace string, start, end time.Time) (*ComplianceReport, error) {
	report := &ComplianceReport{Period: ReportPeriod{Start: start, End: end}}

	var issues platformv1alpha1.IssueList
	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := cr.client.List(ctx, &issues, opts...); err != nil {
		return nil, err
	}
	issues.Items = createdWithin(issues.Items, start, end)

	detectCount, resolveCount := fillIncidentMetrics(report, &issues, start)
	if err := cr.fillRemediationMetrics(ctx, report, opts, start, end); err != nil {
		return nil, err
	}
	cr.fillApprovalMetrics(ctx, report, opts, start, end)
	slaCount := cr.fillIncidentSLAs(ctx, report, opts)
	fillSLAMetrics(report, &issues, start, detectCount, resolveCount, slaCount > 0)
	cr.fillAuditSummary(ctx, report, opts, start, end)

	return report, nil
}

// createdWithin keeps the Issues created in [start, end].
func createdWithin(items []platformv1alpha1.Issue, start, end time.Time) []platformv1alpha1.Issue {
	out := items[:0]
	for _, iss := range items {
		if inPeriod(iss.CreationTimestamp.Time, start, end) {
			out = append(out, iss)
		}
	}
	return out
}

// inPeriod reports whether t falls in [start, end].
func inPeriod(t, start, end time.Time) bool {
	return !t.Before(start) && !t.After(end)
}

// fillIncidentSLAs lists the IncidentSLAs in scope and returns how many
// there are. A list failure leaves the section empty.
func (cr *ComplianceReporter) fillIncidentSLAs(ctx context.Context, report *ComplianceReport, opts []client.ListOption) int {
	var slas platformv1alpha1.IncidentSLAList
	if err := cr.client.List(ctx, &slas, opts...); err != nil {
		return 0
	}
	for _, sla := range slas.Items {
		report.IncidentSLAs = append(report.IncidentSLAs, IncidentSLASummary{
			Name:                 sla.Name,
			Namespace:            sla.Namespace,
			Severity:             string(sla.Spec.Severity),
			ResponseTime:         sla.Spec.ResponseTime,
			ResolutionTime:       sla.Spec.ResolutionTime,
			CompliancePercentage: sla.Status.CompliancePercentage,
			ActiveViolations:     sla.Status.ActiveViolations,
			TotalViolations:      sla.Status.TotalViolations,
			TotalIssuesTracked:   sla.Status.TotalIssuesTracked,
		})
	}
	return len(slas.Items)
}

// fillIncidentMetrics aggregates incident counts, MTTD and MTTR over the
// window, returning the detect/resolve sample sizes the SLA section reuses.
func fillIncidentMetrics(report *ComplianceReport, issues *platformv1alpha1.IssueList, start time.Time) (detectCount, resolveCount int) {
	report.IncidentMetrics.BySeverity = make(map[string]int64)
	report.IncidentMetrics.ByState = make(map[string]int64)
	var totalDetectDur, totalResolveDur time.Duration
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
			totalDetectDur += iss.Status.DetectedAt.Sub(iss.CreationTimestamp.Time)
			detectCount++
		}
		if iss.Status.DetectedAt != nil && iss.Status.ResolvedAt != nil {
			totalResolveDur += iss.Status.ResolvedAt.Sub(iss.Status.DetectedAt.Time)
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
	return detectCount, resolveCount
}

// fillRemediationMetrics aggregates remediation-plan outcomes and per-action
// success/failure stats over the window.
func (cr *ComplianceReporter) fillRemediationMetrics(ctx context.Context, report *ComplianceReport, opts []client.ListOption, start, end time.Time) error {
	var plans platformv1alpha1.RemediationPlanList
	if err := cr.client.List(ctx, &plans, opts...); err != nil {
		return err
	}

	report.RemediationMetrics.ByActionType = make(map[string]ActionStats)
	var completed, failed int64
	for _, plan := range plans.Items {
		if !inPeriod(plan.CreationTimestamp.Time, start, end) {
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
			switch plan.Status.State {
			case platformv1alpha1.RemediationStateCompleted:
				as.Success++
			case platformv1alpha1.RemediationStateFailed:
				as.Failed++
			}
			report.RemediationMetrics.ByActionType[string(a.Type)] = as
		}
	}
	if completed+failed > 0 {
		report.RemediationMetrics.SuccessRate = float64(completed) / float64(completed+failed) * 100
	}
	return nil
}

// fillApprovalMetrics aggregates approval outcomes and mean decision time.
// List failures leave the section empty — approvals are optional data.
func (cr *ComplianceReporter) fillApprovalMetrics(ctx context.Context, report *ComplianceReport, opts []client.ListOption, start, end time.Time) {
	var approvals platformv1alpha1.ApprovalRequestList
	if err := cr.client.List(ctx, &approvals, opts...); err != nil {
		return
	}
	var totalDecisionDur time.Duration
	var decisionCount int
	for _, ar := range approvals.Items {
		if !inPeriod(ar.CreationTimestamp.Time, start, end) {
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
				totalDecisionDur += ar.Status.ApprovedAt.Sub(ar.CreationTimestamp.Time)
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

// fillSLAMetrics derives SLA compliance from the incident set:
// CompliancePercentage = ((totalIncidents - violatingIncidents) / totalIncidents) * 100.
// With IncidentSLAs in scope, a violation is what their controller recorded
// on the Issue (the sla-violated annotation, response and/or resolution);
// without any, an Escalated Issue counts as a resolution violation.
func fillSLAMetrics(report *ComplianceReport, issues *platformv1alpha1.IssueList, start time.Time, detectCount, resolveCount int, haveIncidentSLAs bool) {
	totalIncidents := report.IncidentMetrics.TotalIncidents
	if totalIncidents == 0 {
		report.SLAMetrics.CompliancePercentage = 100 // No incidents = 100% compliance
		return
	}
	var violations int64
	for _, iss := range issues.Items {
		if iss.CreationTimestamp.Time.Before(start) {
			continue
		}
		if countSLAViolations(&report.SLAMetrics, &iss, haveIncidentSLAs) {
			violations++
		}
		// Check if response SLA was violated (DetectedAt too late)
		if iss.Status.DetectedAt != nil {
			detectTime := iss.Status.DetectedAt.Sub(iss.CreationTimestamp.Time)
			report.SLAMetrics.AverageResponseTime += detectTime
		}
		if iss.Status.ResolvedAt != nil {
			resolveTime := iss.Status.ResolvedAt.Sub(iss.CreationTimestamp.Time)
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
}

// countSLAViolations adds the Issue's violations to the counters and reports
// whether the Issue violated any SLA.
func countSLAViolations(m *SLAComplianceMetrics, iss *platformv1alpha1.Issue, haveIncidentSLAs bool) bool {
	if !haveIncidentSLAs {
		if iss.Status.State == platformv1alpha1.IssueStateEscalated {
			m.ResolutionSLAViolations++
			return true
		}
		return false
	}
	violated := false
	for _, vType := range strings.Split(iss.Annotations["platform.chatcli.io/sla-violated"], ",") {
		switch strings.TrimSpace(vType) {
		case "response":
			m.ResponseSLAViolations++
			violated = true
		case "resolution":
			m.ResolutionSLAViolations++
			violated = true
		}
	}
	return violated
}

// fillAuditSummary aggregates audit events by severity and type. List
// failures leave the section empty — audit data is optional.
func (cr *ComplianceReporter) fillAuditSummary(ctx context.Context, report *ComplianceReport, opts []client.ListOption, start, end time.Time) {
	var auditEvents platformv1alpha1.AuditEventList
	if err := cr.client.List(ctx, &auditEvents, opts...); err != nil {
		return
	}
	report.AuditSummary.BySeverity = make(map[string]int64)
	report.AuditSummary.ByEventType = make(map[string]int64)
	for _, ae := range auditEvents.Items {
		if !inPeriod(ae.CreationTimestamp.Time, start, end) {
			continue
		}
		report.AuditSummary.TotalEvents++
		report.AuditSummary.BySeverity[ae.Spec.Severity]++
		report.AuditSummary.ByEventType[ae.Spec.EventType]++
	}
}
