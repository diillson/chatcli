package controllers

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

type CapacityPlanner struct {
	client client.Client
}

// CapacityForecast is served as-is by GET /api/v1/analytics/capacity.
//
// The operator keeps no usage history: CurrentUsage is what the first
// container requests (not measured usage), and no trend or exhaustion date
// can be projected, so Trend.Direction is "insufficient_history" and
// HistoryAvailable is false. Urgency ("plan" or "none") comes from what is
// known: requests close to limits and repeated incidents on the resource.
type CapacityForecast struct {
	Resource            platformv1alpha1.ResourceRef
	CurrentUsage        ResourceUsage
	Limits              ResourceUsage
	UsagePercentage     ResourcePercentage
	Trend               ResourceTrend
	Forecast            ForecastResult
	IncidentCorrelation IncidentResourceCorrelation
	// UsageSource says where CurrentUsage comes from: "requests".
	UsageSource string
	// HistoryAvailable is false: no usage samples are collected.
	HistoryAvailable bool
	// Urgency is "plan" when requests reach 80% of limits or the resource
	// is a bottleneck, and "none" otherwise.
	Urgency string
}

type ResourceUsage struct {
	CPUMillicores int64
	MemoryBytes   int64
}

type ResourcePercentage struct {
	CPU    float64
	Memory float64
}

type ResourceTrend struct {
	CPUTrendPerDay    float64
	MemoryTrendPerDay float64
	Direction         string
}

type ForecastResult struct {
	CPUExhaustionDate         *time.Time
	MemoryExhaustionDate      *time.Time
	DaysUntilCPUExhaustion    int
	DaysUntilMemoryExhaustion int
	Recommendation            string
}

type IncidentResourceCorrelation struct {
	IncidentsInWindow        int
	ResourceRelatedIncidents int
	ResourceIsBottleneck     bool
}

func NewCapacityPlanner(c client.Client) *CapacityPlanner {
	return &CapacityPlanner{client: c}
}

func (cp *CapacityPlanner) AnalyzeResourceTrends(ctx context.Context, resource platformv1alpha1.ResourceRef, window time.Duration) (*CapacityForecast, error) {
	forecast := &CapacityForecast{Resource: resource}

	// Get deployment to extract limits
	var deploy appsv1.Deployment
	if err := cp.client.Get(ctx, client.ObjectKey{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return nil, err
	}

	fillUsageFromDeployment(forecast, &deploy)
	forecast.UsageSource = "requests"

	// No usage history is collected, so there is no trend to fit: the
	// former regression ran over points that all held the current value and
	// always reported a flat, "stable" trend. Say so instead.
	forecast.HistoryAvailable = false
	forecast.Trend.Direction = TrendInsufficientHistory

	cutoff := time.Now().Add(-window)
	cp.fillIncidentCorrelation(ctx, forecast, resource, cutoff)

	forecast.Forecast.Recommendation = cp.generateRecommendation(forecast)
	forecast.Urgency = capacityUrgency(forecast)

	return forecast, nil
}

// TrendInsufficientHistory is the trend direction reported while no usage
// history exists to fit one.
const TrendInsufficientHistory = "insufficient_history"

// capacityUrgency is "plan" when requests reach 80% of limits or the
// resource keeps having incidents, "none" otherwise.
func capacityUrgency(f *CapacityForecast) string {
	if f.UsagePercentage.CPU >= 80 || f.UsagePercentage.Memory >= 80 || f.IncidentCorrelation.ResourceIsBottleneck {
		return "plan"
	}
	return "none"
}

// fillUsageFromDeployment extracts limits and request-based current usage from
// the deployment's first container and derives the usage percentages.
func fillUsageFromDeployment(forecast *CapacityForecast, deploy *appsv1.Deployment) {
	if len(deploy.Spec.Template.Spec.Containers) > 0 {
		c := deploy.Spec.Template.Spec.Containers[0]
		if c.Resources.Limits != nil {
			if cpu, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
				forecast.Limits.CPUMillicores = cpu.MilliValue()
			}
			if mem, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
				forecast.Limits.MemoryBytes = mem.Value()
			}
		}
		// Use requests as current usage proxy
		if c.Resources.Requests != nil {
			if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				forecast.CurrentUsage.CPUMillicores = cpu.MilliValue()
			}
			if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				forecast.CurrentUsage.MemoryBytes = mem.Value()
			}
		}
	}

	if forecast.Limits.CPUMillicores > 0 {
		forecast.UsagePercentage.CPU = float64(forecast.CurrentUsage.CPUMillicores) / float64(forecast.Limits.CPUMillicores) * 100
	}
	if forecast.Limits.MemoryBytes > 0 {
		forecast.UsagePercentage.Memory = float64(forecast.CurrentUsage.MemoryBytes) / float64(forecast.Limits.MemoryBytes) * 100
	}
}

// fillExhaustionForecast projects when CPU/memory hit 100% given the current
// usage and per-day trend, ignoring horizons beyond a year.
func fillExhaustionForecast(forecast *CapacityForecast) {
	now := time.Now()
	if forecast.Trend.CPUTrendPerDay > 0 && forecast.UsagePercentage.CPU < 100 {
		daysLeft := (100 - forecast.UsagePercentage.CPU) / forecast.Trend.CPUTrendPerDay
		if daysLeft > 0 && daysLeft < 365 {
			exhaustion := now.Add(time.Duration(daysLeft*24) * time.Hour)
			forecast.Forecast.CPUExhaustionDate = &exhaustion
			forecast.Forecast.DaysUntilCPUExhaustion = int(daysLeft)
		}
	}
	if forecast.Trend.MemoryTrendPerDay > 0 && forecast.UsagePercentage.Memory < 100 {
		daysLeft := (100 - forecast.UsagePercentage.Memory) / forecast.Trend.MemoryTrendPerDay
		if daysLeft > 0 && daysLeft < 365 {
			exhaustion := now.Add(time.Duration(daysLeft*24) * time.Hour)
			forecast.Forecast.MemoryExhaustionDate = &exhaustion
			forecast.Forecast.DaysUntilMemoryExhaustion = int(daysLeft)
		}
	}
}

// fillIncidentCorrelation counts window incidents and flags the resource as a
// bottleneck when it is tied to more than two of them.
func (cp *CapacityPlanner) fillIncidentCorrelation(ctx context.Context, forecast *CapacityForecast, resource platformv1alpha1.ResourceRef, cutoff time.Time) {
	var issues platformv1alpha1.IssueList
	if err := cp.client.List(ctx, &issues, client.InNamespace(resource.Namespace)); err != nil {
		return
	}
	for _, iss := range issues.Items {
		if iss.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		forecast.IncidentCorrelation.IncidentsInWindow++
		if iss.Spec.Resource.Name == resource.Name {
			forecast.IncidentCorrelation.ResourceRelatedIncidents++
		}
	}
	if forecast.IncidentCorrelation.ResourceRelatedIncidents > 2 {
		forecast.IncidentCorrelation.ResourceIsBottleneck = true
	}
}

func (cp *CapacityPlanner) generateRecommendation(f *CapacityForecast) string {
	if f.Forecast.DaysUntilMemoryExhaustion > 0 && f.Forecast.DaysUntilMemoryExhaustion < 7 {
		return "URGENT: Memory exhaustion projected within 7 days. Increase memory limits or optimize application memory usage."
	}
	if f.Forecast.DaysUntilCPUExhaustion > 0 && f.Forecast.DaysUntilCPUExhaustion < 7 {
		return "URGENT: CPU exhaustion projected within 7 days. Increase CPU limits or scale horizontally."
	}
	if f.Forecast.DaysUntilMemoryExhaustion > 0 && f.Forecast.DaysUntilMemoryExhaustion < 30 {
		return "Memory exhaustion projected within 30 days. Plan capacity increase."
	}
	if f.Forecast.DaysUntilCPUExhaustion > 0 && f.Forecast.DaysUntilCPUExhaustion < 30 {
		return "CPU exhaustion projected within 30 days. Plan capacity increase."
	}
	if f.UsagePercentage.CPU > 80 || f.UsagePercentage.Memory > 80 {
		return "Requests are above 80% of limits. Consider increasing limits proactively."
	}
	if f.IncidentCorrelation.ResourceIsBottleneck {
		return "Repeated incidents on this resource. Review its capacity."
	}
	if f.Trend.Direction == TrendInsufficientHistory {
		return "No usage history is collected, so no trend or exhaustion date can be projected; requests are within limits."
	}
	if f.Trend.Direction == "stable" {
		return "Resource usage is stable. No action needed."
	}
	return "Resource trends are within acceptable ranges."
}
