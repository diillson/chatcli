package controllers

import (
	"context"
	"math"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

type CapacityPlanner struct {
	client client.Client
}

type CapacityForecast struct {
	Resource            platformv1alpha1.ResourceRef
	CurrentUsage        ResourceUsage
	Limits              ResourceUsage
	UsagePercentage     ResourcePercentage
	Trend               ResourceTrend
	Forecast            ForecastResult
	IncidentCorrelation IncidentResourceCorrelation
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

type dataPoint struct {
	X float64 // timestamp as seconds from epoch
	Y float64 // value
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

	// Calculate usage percentage
	if forecast.Limits.CPUMillicores > 0 {
		forecast.UsagePercentage.CPU = float64(forecast.CurrentUsage.CPUMillicores) / float64(forecast.Limits.CPUMillicores) * 100
	}
	if forecast.Limits.MemoryBytes > 0 {
		forecast.UsagePercentage.Memory = float64(forecast.CurrentUsage.MemoryBytes) / float64(forecast.Limits.MemoryBytes) * 100
	}

	// Query anomalies for trend analysis
	var anomalies platformv1alpha1.AnomalyList
	if err := cp.client.List(ctx, &anomalies, client.InNamespace(resource.Namespace)); err != nil {
		return forecast, nil
	}

	cutoff := time.Now().Add(-window)
	var cpuPoints, memPoints []dataPoint
	for _, a := range anomalies.Items {
		if a.CreationTimestamp.Time.Before(cutoff) || a.Spec.Resource.Name != resource.Name {
			continue
		}
		ts := float64(a.CreationTimestamp.Unix())
		switch a.Spec.SignalType {
		case platformv1alpha1.SignalCPUHigh:
			cpuPoints = append(cpuPoints, dataPoint{X: ts, Y: forecast.UsagePercentage.CPU})
		case platformv1alpha1.SignalMemoryHigh:
			memPoints = append(memPoints, dataPoint{X: ts, Y: forecast.UsagePercentage.Memory})
		}
	}

	// Linear regression for trends
	if len(cpuPoints) >= 2 {
		slope, _ := linearRegression(cpuPoints)
		forecast.Trend.CPUTrendPerDay = slope * 86400 // convert per-second to per-day
	}
	if len(memPoints) >= 2 {
		slope, _ := linearRegression(memPoints)
		forecast.Trend.MemoryTrendPerDay = slope * 86400
	}

	// Determine direction
	if forecast.Trend.CPUTrendPerDay > 1 || forecast.Trend.MemoryTrendPerDay > 1 {
		forecast.Trend.Direction = "increasing"
	} else if forecast.Trend.CPUTrendPerDay < -1 || forecast.Trend.MemoryTrendPerDay < -1 {
		forecast.Trend.Direction = "decreasing"
	} else {
		forecast.Trend.Direction = "stable"
	}

	// Forecast exhaustion
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

	// Recommendation
	forecast.Forecast.Recommendation = cp.generateRecommendation(forecast)

	// Incident correlation
	var issues platformv1alpha1.IssueList
	if err := cp.client.List(ctx, &issues, client.InNamespace(resource.Namespace)); err == nil {
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

	return forecast, nil
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
		return "Resource usage above 80%. Consider increasing limits proactively."
	}
	if f.Trend.Direction == "stable" {
		return "Resource usage is stable. No action needed."
	}
	return "Resource trends are within acceptable ranges."
}

// linearRegression performs least-squares linear regression on data points.
func linearRegression(points []dataPoint) (slope, intercept float64) {
	n := float64(len(points))
	if n < 2 {
		return 0, 0
	}

	var sumX, sumY, sumXY, sumX2 float64
	for _, p := range points {
		sumX += p.X
		sumY += p.Y
		sumXY += p.X * p.Y
		sumX2 += p.X * p.X
	}

	denom := n*sumX2 - sumX*sumX
	if math.Abs(denom) < 1e-10 {
		return 0, sumY / n
	}

	slope = (n*sumXY - sumX*sumY) / denom
	intercept = (sumY - slope*sumX) / n
	return slope, intercept
}
