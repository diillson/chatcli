package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/diillson/chatcli/llm/pricing"
	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const costLedgerCM = "chatcli-cost-ledger"
const costConfigCM = "chatcli-cost-config"

type CostTracker struct {
	client client.Client
}

type IncidentCost struct {
	IssueName         string           `json:"issueName"`
	LLMCosts          LLMCostBreakdown `json:"llmCosts"`
	DowntimeCost      DowntimeCostInfo `json:"downtimeCost"`
	EngineerTimeSaved float64          `json:"engineerTimeSavedMinutes"`
	TotalCostUSD      float64          `json:"totalCostUSD"`
}

type LLMCostBreakdown struct {
	TotalInputTokens  int64   `json:"totalInputTokens"`
	TotalOutputTokens int64   `json:"totalOutputTokens"`
	AnalysisCalls     int32   `json:"analysisCalls"`
	AgenticSteps      int32   `json:"agenticSteps"`
	EstimatedCostUSD  float64 `json:"estimatedCostUSD"`
	Provider          string  `json:"provider"`
	Model             string  `json:"model"`
}

type DowntimeCostInfo struct {
	DurationSeconds      float64 `json:"durationSeconds"`
	EstimatedRevenueLoss float64 `json:"estimatedRevenueLoss"`
}

type CostSummary struct {
	PeriodStart       time.Time `json:"periodStart"`
	PeriodEnd         time.Time `json:"periodEnd"`
	TotalLLMCost      float64   `json:"totalLLMCost"`
	TotalDowntimeCost float64   `json:"totalDowntimeCost"`
	IncidentCount     int       `json:"incidentCount"`
	CostPerIncident   float64   `json:"costPerIncident"`
}

type ROIReport struct {
	PeriodStart             time.Time `json:"periodStart"`
	PeriodEnd               time.Time `json:"periodEnd"`
	AutoRemediatedIncidents int       `json:"autoRemediatedIncidents"`
	ManualIncidents         int       `json:"manualIncidents"`
	EngineerHoursSaved      float64   `json:"engineerHoursSaved"`
	LLMCostTotal            float64   `json:"llmCostTotal"`
	DowntimePreventedMins   float64   `json:"downtimePreventedMinutes"`
	EstimatedSavingsUSD     float64   `json:"estimatedSavingsUSD"`
	ROIPercentage           float64   `json:"roiPercentage"`
}

type tokenPricing struct {
	InputPerMillion  float64
	OutputPerMillion float64
}

func NewCostTracker(c client.Client) *CostTracker {
	return &CostTracker{client: c}
}

// getTokenPricing resolves the per-million token rates of provider+model.
// Precedence: an explicit entry in the chatcli-cost-config ConfigMap (the
// cluster operator's word), then the shared pricing engine in llm/pricing
// (the same per-model tables, overrides and subscription rules the CLI's
// /cost uses, so a ledger here and a session there agree on the price of
// the same call), then the legacy per-provider defaults below for a model
// the engine does not know.
func (ct *CostTracker) getTokenPricing(ctx context.Context, namespace, provider, model string) tokenPricing {
	// Try to load from config
	cm := &corev1.ConfigMap{}
	if err := ct.client.Get(ctx, types.NamespacedName{Name: costConfigCM, Namespace: namespace}, cm); err == nil {
		if cm.Data != nil {
			var configured map[string]tokenPricing
			if json.Unmarshal([]byte(cm.Data["pricing"]), &configured) == nil {
				if p, ok := configured[provider]; ok {
					return p
				}
			}
		}
	}

	if rates := pricing.RatesFor(provider, model); rates.Known {
		return tokenPricing{InputPerMillion: rates.InputPerMTok, OutputPerMillion: rates.OutputPerMTok}
	}

	// Defaults for a model the engine does not price.
	switch provider {
	case "CLAUDEAI", "claudeai":
		return tokenPricing{InputPerMillion: 3.0, OutputPerMillion: 15.0}
	case "OPENAI", "openai":
		return tokenPricing{InputPerMillion: 10.0, OutputPerMillion: 30.0}
	case "GOOGLEAI", "googleai":
		return tokenPricing{InputPerMillion: 1.25, OutputPerMillion: 5.0}
	case "XAI", "xai":
		return tokenPricing{InputPerMillion: 3.0, OutputPerMillion: 15.0}
	case "ZAI", "zai":
		return tokenPricing{InputPerMillion: 1.0, OutputPerMillion: 4.0}
	case "MINIMAX", "minimax":
		return tokenPricing{InputPerMillion: 0.3, OutputPerMillion: 1.2}
	case "MOONSHOT", "moonshot":
		// kimi-k2.6 public list as of 2026-05: $0.95/M input (cache miss),
		// $4.00/M output. Cache-hit is $0.16/M but operator cost tracking
		// uses a single tier — pick the miss price to stay conservative.
		return tokenPricing{InputPerMillion: 0.95, OutputPerMillion: 4.0}
	case "COPILOT", "copilot":
		return tokenPricing{InputPerMillion: 10.0, OutputPerMillion: 30.0}
	case "DEVIN", "devin":
		// Devin CLI wrapper: the binary reports no token usage and cost is
		// carried by the Cognition subscription — zero, like local providers.
		return tokenPricing{InputPerMillion: 0, OutputPerMillion: 0}
	case "OPENROUTER", "openrouter":
		// OpenRouter pricing varies by routed model; use conservative average.
		// Override via ConfigMap chatcli-cost-config for accurate per-model pricing.
		return tokenPricing{InputPerMillion: 2.0, OutputPerMillion: 8.0}
	default:
		return tokenPricing{InputPerMillion: 1.0, OutputPerMillion: 3.0}
	}
}

func (ct *CostTracker) RecordLLMCost(ctx context.Context, issueRef platformv1alpha1.IssueRef, namespace, provider, model string, inputTokens, outputTokens int64) error {
	cost, err := ct.loadCost(ctx, issueRef.Name, namespace)
	if err != nil {
		cost = &IncidentCost{IssueName: issueRef.Name}
	}

	cost.LLMCosts.TotalInputTokens += inputTokens
	cost.LLMCosts.TotalOutputTokens += outputTokens
	cost.LLMCosts.AnalysisCalls++
	cost.LLMCosts.Provider = provider
	cost.LLMCosts.Model = model

	rates := ct.getTokenPricing(ctx, namespace, provider, model)
	cost.LLMCosts.EstimatedCostUSD = float64(cost.LLMCosts.TotalInputTokens)/1_000_000*rates.InputPerMillion +
		float64(cost.LLMCosts.TotalOutputTokens)/1_000_000*rates.OutputPerMillion
	cost.TotalCostUSD = cost.LLMCosts.EstimatedCostUSD + cost.DowntimeCost.EstimatedRevenueLoss

	return ct.saveCost(ctx, namespace, cost)
}

func (ct *CostTracker) RecordAgenticStep(ctx context.Context, issueRef platformv1alpha1.IssueRef, namespace, provider, model string, inputTokens, outputTokens int64) error {
	cost, err := ct.loadCost(ctx, issueRef.Name, namespace)
	if err != nil {
		cost = &IncidentCost{IssueName: issueRef.Name}
	}

	cost.LLMCosts.TotalInputTokens += inputTokens
	cost.LLMCosts.TotalOutputTokens += outputTokens
	cost.LLMCosts.AgenticSteps++
	cost.LLMCosts.Provider = provider
	cost.LLMCosts.Model = model

	rates := ct.getTokenPricing(ctx, namespace, provider, model)
	cost.LLMCosts.EstimatedCostUSD = float64(cost.LLMCosts.TotalInputTokens)/1_000_000*rates.InputPerMillion +
		float64(cost.LLMCosts.TotalOutputTokens)/1_000_000*rates.OutputPerMillion
	cost.TotalCostUSD = cost.LLMCosts.EstimatedCostUSD + cost.DowntimeCost.EstimatedRevenueLoss

	return ct.saveCost(ctx, namespace, cost)
}

func (ct *CostTracker) RecordDowntimeCost(ctx context.Context, issueRef platformv1alpha1.IssueRef, namespace string, duration time.Duration, revenuePerMinute float64) error {
	cost, err := ct.loadCost(ctx, issueRef.Name, namespace)
	if err != nil {
		cost = &IncidentCost{IssueName: issueRef.Name}
	}

	cost.DowntimeCost.DurationSeconds = duration.Seconds()
	cost.DowntimeCost.EstimatedRevenueLoss = duration.Minutes() * revenuePerMinute
	cost.TotalCostUSD = cost.LLMCosts.EstimatedCostUSD + cost.DowntimeCost.EstimatedRevenueLoss

	return ct.saveCost(ctx, namespace, cost)
}

func (ct *CostTracker) GetIncidentCost(ctx context.Context, issueName, namespace string) (*IncidentCost, error) {
	return ct.loadCost(ctx, issueName, namespace)
}

func (ct *CostTracker) GetCostSummary(ctx context.Context, namespace string, window time.Duration) (*CostSummary, error) {
	now := time.Now()
	summary := &CostSummary{PeriodStart: now.Add(-window), PeriodEnd: now}

	costs, err := ct.loadAllCosts(ctx, namespace)
	if err != nil {
		return summary, nil
	}

	for _, cost := range costs {
		summary.TotalLLMCost += cost.LLMCosts.EstimatedCostUSD
		summary.TotalDowntimeCost += cost.DowntimeCost.EstimatedRevenueLoss
		summary.IncidentCount++
	}

	if summary.IncidentCount > 0 {
		summary.CostPerIncident = (summary.TotalLLMCost + summary.TotalDowntimeCost) / float64(summary.IncidentCount)
	}
	return summary, nil
}

func (ct *CostTracker) CalculateROI(ctx context.Context, namespace string, window time.Duration) (*ROIReport, error) {
	now := time.Now()
	start := now.Add(-window)
	report := &ROIReport{PeriodStart: start, PeriodEnd: now}

	// Count auto-remediated vs manual
	var plans platformv1alpha1.RemediationPlanList
	opts := []client.ListOption{client.InNamespace(namespace)}
	if err := ct.client.List(ctx, &plans, opts...); err != nil {
		return report, err
	}

	for _, plan := range plans.Items {
		if plan.CreationTimestamp.Time.Before(start) {
			continue
		}
		if plan.Status.State == platformv1alpha1.RemediationStateCompleted {
			report.AutoRemediatedIncidents++
			// Estimate downtime prevented (avg 15min per auto-remediated incident)
			report.DowntimePreventedMins += 15
		}
	}

	// Count manual incidents (escalated/failed)
	var issues platformv1alpha1.IssueList
	if err := ct.client.List(ctx, &issues, opts...); err != nil {
		return report, err
	}
	for _, iss := range issues.Items {
		if iss.CreationTimestamp.Time.Before(start) {
			continue
		}
		if iss.Status.State == platformv1alpha1.IssueStateEscalated || iss.Status.State == platformv1alpha1.IssueStateFailed {
			report.ManualIncidents++
		}
	}

	// Load costs
	costs, _ := ct.loadAllCosts(ctx, namespace)
	for _, cost := range costs {
		report.LLMCostTotal += cost.LLMCosts.EstimatedCostUSD
	}

	// Engineer time saved: 2h average per auto-remediated incident
	report.EngineerHoursSaved = float64(report.AutoRemediatedIncidents) * 2.0

	// Savings: engineer hours * $75/hour + downtime prevented * $10/min (configurable)
	hourlyRate := 75.0
	revenuePerMin := 10.0
	report.EstimatedSavingsUSD = report.EngineerHoursSaved*hourlyRate + report.DowntimePreventedMins*revenuePerMin

	if report.LLMCostTotal > 0 {
		report.ROIPercentage = (report.EstimatedSavingsUSD - report.LLMCostTotal) / report.LLMCostTotal * 100
	}

	return report, nil
}

func (ct *CostTracker) loadCost(ctx context.Context, issueName, namespace string) (*IncidentCost, error) {
	cm := &corev1.ConfigMap{}
	if err := ct.client.Get(ctx, types.NamespacedName{Name: costLedgerCM, Namespace: namespace}, cm); err != nil {
		return nil, err
	}
	data, ok := cm.Data[issueName]
	if !ok {
		return nil, fmt.Errorf("no cost data for %s", issueName)
	}
	var cost IncidentCost
	if err := json.Unmarshal([]byte(data), &cost); err != nil {
		return nil, err
	}
	return &cost, nil
}

func (ct *CostTracker) loadAllCosts(ctx context.Context, namespace string) ([]IncidentCost, error) {
	cm := &corev1.ConfigMap{}
	if err := ct.client.Get(ctx, types.NamespacedName{Name: costLedgerCM, Namespace: namespace}, cm); err != nil {
		return nil, err
	}
	var costs []IncidentCost
	for _, v := range cm.Data {
		var cost IncidentCost
		if json.Unmarshal([]byte(v), &cost) == nil {
			costs = append(costs, cost)
		}
	}
	return costs, nil
}

func (ct *CostTracker) saveCost(ctx context.Context, namespace string, cost *IncidentCost) error {
	cm := &corev1.ConfigMap{}
	err := ct.client.Get(ctx, types.NamespacedName{Name: costLedgerCM, Namespace: namespace}, cm)
	if err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: costLedgerCM, Namespace: namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "chatcli-operator"}},
			Data: make(map[string]string),
		}
		data, _ := json.Marshal(cost)
		cm.Data[cost.IssueName] = string(data)
		return ct.client.Create(ctx, cm)
	}

	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	data, _ := json.Marshal(cost)
	cm.Data[cost.IssueName] = string(data)
	return ct.client.Update(ctx, cm)
}
