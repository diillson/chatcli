package rest

import (
	"context"
	"sort"
	"time"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// computeSummary calculates an overview of all AIOps metrics, optionally
// filtered by time range. Each contributing dataset (issues, remediations,
// postmortems, runbooks, SLOs, approvals) is folded by a dedicated helper so
// the orchestration here stays a flat sequence of named steps. Early-exit on
// list errors propagates the failure to the caller — the partial summary is
// never returned, which prevents dashboards from rendering misleading totals.
func (s *APIServer) computeSummary(ctx context.Context, tr timeRangeParams) (*AnalyticsSummary, error) {
	summary := &AnalyticsSummary{SeverityBreakdown: make(map[string]int)}

	issues, err := s.listIssues(ctx)
	if err != nil {
		return nil, err
	}
	foldIssueSummary(summary, issues, tr)

	plans, err := s.listRemediationPlans(ctx)
	if err != nil {
		return nil, err
	}
	foldRemediationSummary(summary, plans, issues, tr)

	if err := s.foldPostMortemSummary(ctx, summary, tr); err != nil {
		return nil, err
	}
	if err := s.foldRunbookSummary(ctx, summary); err != nil {
		return nil, err
	}
	s.foldSLOSummary(ctx, summary)
	s.foldApprovalSummary(ctx, summary)

	return summary, nil
}

// listIssues returns every Issue in the cluster. Splitting the list call from
// the folding step keeps computeSummary linear and lets the issue list be
// reused for orphan-detection in foldRemediationSummary.
func (s *APIServer) listIssues(ctx context.Context) ([]v1alpha1.Issue, error) {
	var list v1alpha1.IssueList
	if err := s.client.List(ctx, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// listRemediationPlans returns every RemediationPlan in the cluster.
func (s *APIServer) listRemediationPlans(ctx context.Context) ([]v1alpha1.RemediationPlan, error) {
	var list v1alpha1.RemediationPlanList
	if err := s.client.List(ctx, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// foldIssueSummary accumulates the issue-driven counters (totals, severity
// breakdown, risk score average, and the GAP-03/04 Contained / chaos-induced
// buckets) into the running summary.
func foldIssueSummary(summary *AnalyticsSummary, issues []v1alpha1.Issue, tr timeRangeParams) {
	var totalRisk int64
	for i := range issues {
		iss := issues[i]
		if !inTimeRange(iss.CreationTimestamp.Time, tr) {
			continue
		}
		summary.TotalIssues++
		summary.SeverityBreakdown[string(iss.Spec.Severity)]++
		totalRisk += int64(iss.Spec.RiskScore)
		accumulateIssueCounters(summary, &iss)
	}
	if summary.TotalIssues > 0 {
		summary.AvgRiskScore = float64(totalRisk) / float64(summary.TotalIssues)
	}
}

// foldRemediationSummary accumulates remediation-plan counters: totals, success/
// failure split, plus the "did remediation actually resolve the parent Issue"
// dimension (RemediatedIssues / ResolvedByRemediation) used by the dashboard's
// Success Rate card.
func foldRemediationSummary(summary *AnalyticsSummary, plans []v1alpha1.RemediationPlan, allIssues []v1alpha1.Issue, tr timeRangeParams) {
	plansInRange := filterPlansByRange(plans, tr)
	summary.TotalRemediations = len(plansInRange)
	for _, rp := range plansInRange {
		switch rp.Status.State {
		case v1alpha1.RemediationStateCompleted:
			summary.SuccessfulRemediations++
		case v1alpha1.RemediationStateFailed, v1alpha1.RemediationStateRolledBack:
			summary.FailedRemediations++
		}
	}

	// Issue-outcome view: use ALL issues (not time-filtered) so plans whose
	// parent Issue falls outside the time range are not falsely penalized.
	stateByIssue := make(map[string]v1alpha1.IssueState, len(allIssues))
	for _, iss := range allIssues {
		stateByIssue[iss.Name] = iss.Status.State
	}
	resolvedByIssue := remediationOutcomesByIssue(plansInRange, stateByIssue)
	for _, resolved := range resolvedByIssue {
		summary.RemediatedIssues++
		if resolved {
			summary.ResolvedByRemediation++
		}
	}
}

// filterPlansByRange returns plans created within the requested window.
func filterPlansByRange(plans []v1alpha1.RemediationPlan, tr timeRangeParams) []v1alpha1.RemediationPlan {
	out := make([]v1alpha1.RemediationPlan, 0, len(plans))
	for _, rp := range plans {
		if inTimeRange(rp.CreationTimestamp.Time, tr) {
			out = append(out, rp)
		}
	}
	return out
}

// remediationOutcomesByIssue collapses plans into per-Issue outcomes: the map
// value is true when the parent Issue reached Resolved, false otherwise.
// Orphaned plans (Issue deleted) are skipped — counting them would distort the
// success rate.
func remediationOutcomesByIssue(plans []v1alpha1.RemediationPlan, stateByIssue map[string]v1alpha1.IssueState) map[string]bool {
	out := make(map[string]bool)
	for _, rp := range plans {
		issName := rp.Spec.IssueRef.Name
		state, ok := stateByIssue[issName]
		if !ok {
			continue
		}
		if _, seen := out[issName]; !seen {
			out[issName] = false
		}
		if state == v1alpha1.IssueStateResolved {
			out[issName] = true
		}
	}
	return out
}

// foldPostMortemSummary fills TotalPostMortems plus the GAP-03 counter that
// tracks PostMortems still pending a human follow-up.
func (s *APIServer) foldPostMortemSummary(ctx context.Context, summary *AnalyticsSummary, tr timeRangeParams) error {
	var list v1alpha1.PostMortemList
	if err := s.client.List(ctx, &list); err != nil {
		return err
	}
	for _, pm := range list.Items {
		if !inTimeRange(pm.CreationTimestamp.Time, tr) {
			continue
		}
		summary.TotalPostMortems++
		if pm.Spec.RequiresHumanAction && pm.Status.State != v1alpha1.PostMortemStateClosed {
			summary.PostMortemsRequiringHumanAction++
		}
	}
	return nil
}

// foldRunbookSummary fills TotalRunbooks.
func (s *APIServer) foldRunbookSummary(ctx context.Context, summary *AnalyticsSummary) error {
	var list v1alpha1.RunbookList
	if err := s.client.List(ctx, &list); err != nil {
		return err
	}
	summary.TotalRunbooks = len(list.Items)
	return nil
}

// foldSLOSummary counts total SLOs and how many are at risk or breached.
// Uses the unstructured client because the SLO CRD may not be registered with
// the operator's scheme yet (the analytics endpoint must keep working before
// the SLO controller starts).
func (s *APIServer) foldSLOSummary(ctx context.Context, summary *AnalyticsSummary) {
	items, err := s.listUnstructured(ctx, "servicelevelobjectives", "")
	if err != nil {
		return
	}
	summary.TotalSLOs = len(items)
	for _, item := range items {
		if isUnstructuredStateOneOf(item, "AtRisk", "Breached") {
			summary.SLOsAtRisk++
		}
	}
}

// foldApprovalSummary counts ApprovalRequest CRs that are still pending. An
// empty status block is treated as pending so the dashboard surfaces freshly-
// created approvals immediately.
func (s *APIServer) foldApprovalSummary(ctx context.Context, summary *AnalyticsSummary) {
	items, err := s.listUnstructured(ctx, "approvalrequests", "")
	if err != nil {
		return
	}
	for _, item := range items {
		statusMap, _ := item["status"].(map[string]interface{})
		if statusMap == nil {
			summary.PendingApprovals++
			continue
		}
		state, _ := statusMap["state"].(string)
		if state == "Pending" || state == "" {
			summary.PendingApprovals++
		}
	}
}

// isUnstructuredStateOneOf checks whether item.status.state matches any of the
// supplied values. Safe with missing or non-string state fields.
func isUnstructuredStateOneOf(item map[string]interface{}, states ...string) bool {
	statusMap, _ := item["status"].(map[string]interface{})
	if statusMap == nil {
		return false
	}
	state, _ := statusMap["state"].(string)
	for _, s := range states {
		if s == state {
			return true
		}
	}
	return false
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

// computeRemediationStats calculates remediation success rates based on the
// outcome of the parent Issue (remediation activity), not individual plan states.
// If the Issue was ultimately Resolved, all plans for that issue count as
// successful remediation — even if some intermediate attempts failed.
// Plans are grouped by primary strategy for the breakdown view.
func (s *APIServer) computeRemediationStats(ctx context.Context, tr timeRangeParams) ([]RemediationStat, error) {
	// Fetch issues to determine remediation outcome.
	var issues v1alpha1.IssueList
	if err := s.client.List(ctx, &issues); err != nil {
		return nil, err
	}
	issueStateMap := make(map[string]v1alpha1.IssueState)
	for _, iss := range issues.Items {
		issueStateMap[iss.Name] = iss.Status.State
	}

	var remediations v1alpha1.RemediationPlanList
	if err := s.client.List(ctx, &remediations); err != nil {
		return nil, err
	}

	// Group plans by Issue, then determine success by Issue outcome.
	// For the strategy breakdown, each Issue counts once per strategy used.
	type issueGroup struct {
		strategies  map[string]bool
		resolved    bool
		totalDurSec float64
		durCount    int
	}

	byIssue := make(map[string]*issueGroup)

	for _, rp := range remediations.Items {
		if !inTimeRange(rp.CreationTimestamp.Time, tr) {
			continue
		}

		issName := rp.Spec.IssueRef.Name
		if _, found := issueStateMap[issName]; !found {
			continue // skip orphaned plans (Issue deleted)
		}
		ig, ok := byIssue[issName]
		if !ok {
			ig = &issueGroup{strategies: make(map[string]bool)}
			byIssue[issName] = ig
		}

		// Mark resolved if parent Issue is Resolved.
		if issueStateMap[issName] == v1alpha1.IssueStateResolved {
			ig.resolved = true
		}

		// Collect strategy label.
		strategy := planStrategy(rp)
		ig.strategies[strategy] = true

		// Accumulate duration from terminal plans.
		if rp.Status.StartedAt != nil && rp.Status.CompletedAt != nil {
			ig.totalDurSec += rp.Status.CompletedAt.Time.Sub(rp.Status.StartedAt.Time).Seconds()
			ig.durCount++
		}
	}

	// Build stats per strategy: each Issue counts once per strategy it used.
	type actionStats struct {
		Total       int
		Successful  int
		Failed      int
		TotalDurSec float64
		DurCount    int
	}
	stats := make(map[string]*actionStats)

	for _, ig := range byIssue {
		for strategy := range ig.strategies {
			as, ok := stats[strategy]
			if !ok {
				as = &actionStats{}
				stats[strategy] = as
			}
			as.Total++
			if ig.resolved {
				as.Successful++
			} else {
				as.Failed++
			}
			if ig.durCount > 0 {
				as.TotalDurSec += ig.totalDurSec
				as.DurCount += ig.durCount
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
			successRate = float64(e.Stats.Successful) / float64(e.Stats.Total)
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

// planStrategy returns a label for the primary strategy used by a remediation plan.
func planStrategy(rp v1alpha1.RemediationPlan) string {
	if rp.Spec.AgenticMode {
		if rp.Spec.Strategy != "" {
			return rp.Spec.Strategy
		}
		for _, step := range rp.Spec.AgenticHistory {
			if step.Action != nil {
				return string(step.Action.Type)
			}
		}
		return "Agentic"
	}
	if len(rp.Spec.Actions) > 0 {
		return string(rp.Spec.Actions[0].Type)
	}
	return "Unknown"
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

// accumulateIssueCounters folds a single Issue into the rolling summary
// counters. Extracted from computeSummary to keep the latter's cyclomatic
// complexity below the Floor 8 threshold while preserving the GAP-03/04
// counter semantics:
//   - Contained is counted both in its own bucket and as Open (customer impact
//     persists until a human restores the workload),
//   - chaos-induced Issues are tracked separately so production dashboards can
//     subtract them from "real" incident counts.
func accumulateIssueCounters(summary *AnalyticsSummary, iss *v1alpha1.Issue) {
	switch iss.Status.State {
	case v1alpha1.IssueStateResolved:
		summary.ResolvedIssues++
	case v1alpha1.IssueStateContained:
		summary.ContainedIssues++
		summary.OpenIssues++
	default:
		// Detected, Analyzing, Remediating, Escalated, Failed all count as open.
		summary.OpenIssues++
	}
	if iss.Spec.Severity == v1alpha1.IssueSeverityCritical {
		summary.CriticalIssues++
	}
	if iss.Labels["platform.chatcli.io/source"] == "chaos-experiment" {
		summary.ChaosInducedIssues++
	}
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
