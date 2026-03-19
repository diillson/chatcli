package rest

import (
	"context"
	"sort"
	"time"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// computeSummary calculates an overview of all AIOps metrics.
func (s *APIServer) computeSummary(ctx context.Context) (*AnalyticsSummary, error) {
	summary := &AnalyticsSummary{
		SeverityBreakdown: make(map[string]int),
	}

	// Fetch all issues.
	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		return nil, err
	}

	summary.TotalIssues = len(issues.Items)
	var totalRisk int64
	for _, iss := range issues.Items {
		summary.SeverityBreakdown[string(iss.Spec.Severity)]++
		totalRisk += int64(iss.Spec.RiskScore)

		switch iss.Status.State {
		case v1alpha1.IssueStateResolved:
			summary.ResolvedIssues++
		case v1alpha1.IssueStateFailed:
			// count as open since it failed remediation
			summary.OpenIssues++
		default:
			summary.OpenIssues++
		}

		if iss.Spec.Severity == v1alpha1.IssueSeverityCritical {
			summary.CriticalIssues++
		}
	}
	if summary.TotalIssues > 0 {
		summary.AvgRiskScore = float64(totalRisk) / float64(summary.TotalIssues)
	}

	// Fetch all remediations.
	var remediations v1alpha1.RemediationPlanList
	if err := s.client.List(ctx, &remediations); err != nil {
		return nil, err
	}
	summary.TotalRemediations = len(remediations.Items)
	for _, rp := range remediations.Items {
		switch rp.Status.State {
		case v1alpha1.RemediationStateCompleted:
			summary.SuccessfulRemediations++
		case v1alpha1.RemediationStateFailed, v1alpha1.RemediationStateRolledBack:
			summary.FailedRemediations++
		}
	}

	// Fetch postmortems.
	var postmortems v1alpha1.PostMortemList
	if err := s.client.List(ctx, &postmortems); err != nil {
		return nil, err
	}
	summary.TotalPostMortems = len(postmortems.Items)

	// Fetch runbooks.
	var runbooks v1alpha1.RunbookList
	if err := s.client.List(ctx, &runbooks); err != nil {
		return nil, err
	}
	summary.TotalRunbooks = len(runbooks.Items)

	// SLOs — use unstructured since the CRD may not be registered yet.
	sloItems, err := s.listUnstructured(ctx, "servicelevelobjectives", "")
	if err == nil {
		summary.TotalSLOs = len(sloItems)
		for _, item := range sloItems {
			statusMap, _ := item["status"].(map[string]interface{})
			if statusMap != nil {
				state, _ := statusMap["state"].(string)
				if state == "AtRisk" || state == "Breached" {
					summary.SLOsAtRisk++
				}
			}
		}
	}

	// Approvals — use unstructured.
	approvalItems, err := s.listUnstructured(ctx, "approvalrequests", "")
	if err == nil {
		for _, item := range approvalItems {
			statusMap, _ := item["status"].(map[string]interface{})
			if statusMap != nil {
				state, _ := statusMap["state"].(string)
				if state == "Pending" || state == "" {
					summary.PendingApprovals++
				}
			} else {
				// No status yet means pending.
				summary.PendingApprovals++
			}
		}
	}

	return summary, nil
}

// computeMTTD calculates Mean Time to Detect over time, grouped by day.
func (s *APIServer) computeMTTD(ctx context.Context, tr timeRangeParams) ([]MTTMetric, error) {
	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		return nil, err
	}

	// Group by day: MTTD = DetectedAt - CreationTimestamp
	// In practice, DetectedAt is when the issue state was set to Detected.
	// If DetectedAt is nil, use CreationTimestamp as the detection time (instant detection).
	dayBuckets := make(map[string][]float64)

	for _, iss := range issues.Items {
		created := iss.CreationTimestamp.Time
		if !inTimeRange(created, tr) {
			continue
		}

		var detectedTime time.Time
		if iss.Status.DetectedAt != nil {
			detectedTime = iss.Status.DetectedAt.Time
		} else {
			detectedTime = created
		}

		// MTTD = time from resource creation to issue detection.
		// Since we only have the issue creation as the proxy, MTTD is effectively
		// the gap between creation and DetectedAt.
		mttd := detectedTime.Sub(created).Seconds()
		if mttd < 0 {
			mttd = 0
		}

		day := created.Format("2006-01-02")
		dayBuckets[day] = append(dayBuckets[day], mttd)
	}

	return aggregateMTTBuckets(dayBuckets), nil
}

// computeMTTR calculates Mean Time to Resolve over time, grouped by day.
func (s *APIServer) computeMTTR(ctx context.Context, tr timeRangeParams) ([]MTTMetric, error) {
	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		return nil, err
	}

	dayBuckets := make(map[string][]float64)

	for _, iss := range issues.Items {
		if iss.Status.State != v1alpha1.IssueStateResolved {
			continue
		}
		if iss.Status.ResolvedAt == nil {
			continue
		}

		created := iss.CreationTimestamp.Time
		if !inTimeRange(created, tr) {
			continue
		}

		mttr := iss.Status.ResolvedAt.Time.Sub(created).Seconds()
		if mttr < 0 {
			mttr = 0
		}

		day := created.Format("2006-01-02")
		dayBuckets[day] = append(dayBuckets[day], mttr)
	}

	return aggregateMTTBuckets(dayBuckets), nil
}

// computeTrends computes issue trends by day or week.
func (s *APIServer) computeTrends(ctx context.Context, tr timeRangeParams, groupBy string) ([]TrendPoint, error) {
	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		return nil, err
	}

	if groupBy == "" {
		groupBy = "day"
	}

	type bucket struct {
		total      int
		bySeverity map[string]int
	}
	buckets := make(map[string]*bucket)

	for _, iss := range issues.Items {
		created := iss.CreationTimestamp.Time
		if !inTimeRange(created, tr) {
			continue
		}

		var key string
		switch groupBy {
		case "week":
			year, week := created.ISOWeek()
			key = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).
				AddDate(0, 0, (week-1)*7).Format("2006-01-02")
		default: // "day"
			key = created.Format("2006-01-02")
		}

		b, ok := buckets[key]
		if !ok {
			b = &bucket{bySeverity: make(map[string]int)}
			buckets[key] = b
		}
		b.total++
		b.bySeverity[string(iss.Spec.Severity)]++
	}

	// Sort by date.
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]TrendPoint, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		result = append(result, TrendPoint{
			Date:       k,
			Total:      b.total,
			BySeverity: b.bySeverity,
		})
	}

	return result, nil
}

// computeTopResources returns the most incident-prone resources.
func (s *APIServer) computeTopResources(ctx context.Context, tr timeRangeParams, limit int) ([]TopResource, error) {
	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 10
	}

	type resourceKey struct {
		Kind      string
		Name      string
		Namespace string
	}

	type resourceStats struct {
		Count        int
		LastIncident time.Time
	}

	stats := make(map[resourceKey]*resourceStats)

	for _, iss := range issues.Items {
		created := iss.CreationTimestamp.Time
		if !inTimeRange(created, tr) {
			continue
		}

		key := resourceKey{
			Kind:      iss.Spec.Resource.Kind,
			Name:      iss.Spec.Resource.Name,
			Namespace: iss.Spec.Resource.Namespace,
		}

		s, ok := stats[key]
		if !ok {
			s = &resourceStats{}
			stats[key] = s
		}
		s.Count++
		if created.After(s.LastIncident) {
			s.LastIncident = created
		}
	}

	// Sort by count descending.
	type sortEntry struct {
		Key   resourceKey
		Stats *resourceStats
	}
	entries := make([]sortEntry, 0, len(stats))
	for k, v := range stats {
		entries = append(entries, sortEntry{Key: k, Stats: v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Stats.Count > entries[j].Stats.Count
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}

	result := make([]TopResource, 0, len(entries))
	for _, e := range entries {
		result = append(result, TopResource{
			Kind:          e.Key.Kind,
			Name:          e.Key.Name,
			Namespace:     e.Key.Namespace,
			IncidentCount: e.Stats.Count,
			LastIncident:  e.Stats.LastIncident.Format(time.RFC3339),
		})
	}

	return result, nil
}

// computeRemediationStats calculates remediation success rates by action type.
func (s *APIServer) computeRemediationStats(ctx context.Context, tr timeRangeParams) ([]RemediationStat, error) {
	var remediations v1alpha1.RemediationPlanList
	if err := s.client.List(ctx, &remediations); err != nil {
		return nil, err
	}

	type actionStats struct {
		Total       int
		Successful  int
		Failed      int
		TotalDurSec float64
		DurCount    int
	}

	stats := make(map[string]*actionStats)

	for _, rp := range remediations.Items {
		created := rp.CreationTimestamp.Time
		if !inTimeRange(created, tr) {
			continue
		}

		// Determine action types from the plan.
		actionTypes := make(map[string]bool)
		for _, a := range rp.Spec.Actions {
			actionTypes[string(a.Type)] = true
		}
		// If agentic mode, extract from history.
		if rp.Spec.AgenticMode {
			for _, step := range rp.Spec.AgenticHistory {
				if step.Action != nil {
					actionTypes[string(step.Action.Type)] = true
				}
			}
		}
		// If no specific action types found, categorize as "Unknown".
		if len(actionTypes) == 0 {
			actionTypes["Unknown"] = true
		}

		isCompleted := rp.Status.State == v1alpha1.RemediationStateCompleted
		isFailed := rp.Status.State == v1alpha1.RemediationStateFailed ||
			rp.Status.State == v1alpha1.RemediationStateRolledBack

		// Calculate duration if completed or failed.
		var durSec float64
		hasDuration := false
		if rp.Status.StartedAt != nil && rp.Status.CompletedAt != nil {
			durSec = rp.Status.CompletedAt.Time.Sub(rp.Status.StartedAt.Time).Seconds()
			hasDuration = true
		}

		for actionType := range actionTypes {
			as, ok := stats[actionType]
			if !ok {
				as = &actionStats{}
				stats[actionType] = as
			}
			as.Total++
			if isCompleted {
				as.Successful++
			}
			if isFailed {
				as.Failed++
			}
			if hasDuration {
				as.TotalDurSec += durSec
				as.DurCount++
			}
		}
	}

	// Sort by total descending.
	type sortEntry struct {
		Action string
		Stats  *actionStats
	}
	entries := make([]sortEntry, 0, len(stats))
	for k, v := range stats {
		entries = append(entries, sortEntry{Action: k, Stats: v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Stats.Total > entries[j].Stats.Total
	})

	result := make([]RemediationStat, 0, len(entries))
	for _, e := range entries {
		var successRate float64
		if e.Stats.Total > 0 {
			successRate = float64(e.Stats.Successful) / float64(e.Stats.Total) * 100.0
		}
		var avgDur float64
		if e.Stats.DurCount > 0 {
			avgDur = e.Stats.TotalDurSec / float64(e.Stats.DurCount)
		}
		result = append(result, RemediationStat{
			Action:      e.Action,
			Total:       e.Stats.Total,
			Successful:  e.Stats.Successful,
			Failed:      e.Stats.Failed,
			SuccessRate: successRate,
			AvgDuration: avgDur,
		})
	}

	return result, nil
}

// --- Helpers ---

// aggregateMTTBuckets converts day->[]seconds into sorted MTTMetric slices.
func aggregateMTTBuckets(buckets map[string][]float64) []MTTMetric {
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]MTTMetric, 0, len(keys))
	for _, k := range keys {
		vals := buckets[k]
		var sum float64
		for _, v := range vals {
			sum += v
		}
		avg := sum / float64(len(vals))
		result = append(result, MTTMetric{
			Date:  k,
			Value: avg,
			Count: len(vals),
		})
	}
	return result
}

// inTimeRange checks if a time falls within the optional time range.
func inTimeRange(t time.Time, tr timeRangeParams) bool {
	if tr.From != nil && t.Before(*tr.From) {
		return false
	}
	if tr.To != nil && t.After(*tr.To) {
		return false
	}
	return true
}

// listUnstructured queries for unstructured resources by plural name.
// This is used for CRDs that may not have Go types registered yet.
func (s *APIServer) listUnstructured(ctx context.Context, plural, namespace string) ([]map[string]interface{}, error) {
	// Use the controller-runtime client's unstructured list capability.
	list := &unstructuredList{}
	list.SetGroupVersionKind(groupVersionKind(plural))

	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}

	if err := s.client.List(ctx, list, opts...); err != nil {
		return nil, err
	}

	result := make([]map[string]interface{}, 0, len(list.Items))
	for _, item := range list.Items {
		result = append(result, item.Object)
	}
	return result, nil
}
