package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// MetricsCollector queries Prometheus for metric data relevant to incident analysis.
// It collects CPU, memory, latency, error rate, and custom metric snapshots,
// providing quantitative context that the AI can use for root cause determination.
type MetricsCollector struct {
	prometheusURL string
	httpClient    *http.Client
}

// MetricDataPoint represents a single metric value at a point in time.
type MetricDataPoint struct {
	Timestamp time.Time
	Value     float64
}

// MetricSeries is a named time series of metric data points.
type MetricSeries struct {
	Name       string
	Labels     map[string]string
	DataPoints []MetricDataPoint
	Unit       string
}

// MetricsSnapshot contains all collected metrics for an incident.
type MetricsSnapshot struct {
	// Resource-level metrics
	CPUUsage       *MetricSeries
	MemoryUsage    *MetricSeries
	RestartCount   *MetricSeries
	NetworkReceive *MetricSeries
	NetworkTransmit *MetricSeries

	// Application-level metrics
	RequestRate  *MetricSeries
	ErrorRate    *MetricSeries
	LatencyP50   *MetricSeries
	LatencyP95   *MetricSeries
	LatencyP99   *MetricSeries

	// HPA metrics
	HPACurrentReplicas *MetricSeries
	HPADesiredReplicas *MetricSeries
	HPACPUTarget       *MetricSeries

	// Node-level metrics (if applicable)
	NodeCPU    *MetricSeries
	NodeMemory *MetricSeries
	NodeDisk   *MetricSeries

	// Analysis
	Trends       []MetricTrend
	Correlations []MetricCorrelation
	Summary      string
}

// MetricTrend describes a significant change in a metric around the incident.
type MetricTrend struct {
	MetricName   string
	BeforeValue  float64
	DuringValue  float64
	AfterValue   float64
	ChangePercent float64
	Direction    string // "spike", "drop", "sustained_high", "sustained_low"
	Significance string // "critical", "high", "medium", "low"
}

// MetricCorrelation describes a temporal correlation between a metric change and the incident.
type MetricCorrelation struct {
	MetricName string
	EventType  string // "deploy", "config_change", "traffic_spike"
	TimeDelta  time.Duration
	Detail     string
}

// NewMetricsCollector creates a MetricsCollector that queries the given Prometheus URL.
func NewMetricsCollector(prometheusURL string) *MetricsCollector {
	return &MetricsCollector{
		prometheusURL: strings.TrimRight(prometheusURL, "/"),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// CollectIncidentMetrics gathers all relevant metrics for an incident.
func (mc *MetricsCollector) CollectIncidentMetrics(ctx context.Context, resource platformv1alpha1.ResourceRef, incidentTime time.Time) (*MetricsSnapshot, error) {
	if mc.prometheusURL == "" {
		return nil, fmt.Errorf("prometheus URL not configured")
	}

	snapshot := &MetricsSnapshot{}
	namespace := resource.Namespace
	name := resource.Name

	// Time windows: 30min before incident, during (incident time), 15min after
	beforeStart := incidentTime.Add(-30 * time.Minute)
	afterEnd := incidentTime.Add(15 * time.Minute)
	if afterEnd.After(time.Now()) {
		afterEnd = time.Now()
	}
	step := "60s"

	// Container CPU usage
	cpuQuery := fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{namespace="%s",pod=~"%s-.*",container!="",container!="POD"}[5m])) by (pod)`,
		namespace, name)
	snapshot.CPUUsage = mc.queryRange(ctx, cpuQuery, beforeStart, afterEnd, step, "cpu_usage", "cores")

	// Container memory usage
	memQuery := fmt.Sprintf(`sum(container_memory_working_set_bytes{namespace="%s",pod=~"%s-.*",container!="",container!="POD"}) by (pod)`,
		namespace, name)
	snapshot.MemoryUsage = mc.queryRange(ctx, memQuery, beforeStart, afterEnd, step, "memory_usage", "bytes")

	// Restart count
	restartQuery := fmt.Sprintf(`sum(kube_pod_container_status_restarts_total{namespace="%s",pod=~"%s-.*"}) by (pod)`,
		namespace, name)
	snapshot.RestartCount = mc.queryRange(ctx, restartQuery, beforeStart, afterEnd, step, "restarts", "count")

	// Network receive/transmit
	netRxQuery := fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{namespace="%s",pod=~"%s-.*"}[5m])) by (pod)`,
		namespace, name)
	snapshot.NetworkReceive = mc.queryRange(ctx, netRxQuery, beforeStart, afterEnd, step, "network_rx", "bytes/s")

	netTxQuery := fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{namespace="%s",pod=~"%s-.*"}[5m])) by (pod)`,
		namespace, name)
	snapshot.NetworkTransmit = mc.queryRange(ctx, netTxQuery, beforeStart, afterEnd, step, "network_tx", "bytes/s")

	// HTTP request rate (common metrics names)
	for _, metricName := range []string{
		"http_requests_total", "http_server_requests_seconds_count",
		"istio_requests_total", "envoy_http_downstream_rq_total",
		"nginx_http_requests_total",
	} {
		reqQuery := fmt.Sprintf(`sum(rate(%s{namespace="%s",pod=~"%s-.*"}[5m]))`, metricName, namespace, name)
		if series := mc.queryRange(ctx, reqQuery, beforeStart, afterEnd, step, "request_rate", "req/s"); series != nil && len(series.DataPoints) > 0 {
			snapshot.RequestRate = series
			break
		}
	}

	// HTTP error rate (5xx responses)
	for _, metricName := range []string{
		`http_requests_total{status=~"5.."}`, `http_server_requests_seconds_count{status=~"5.."}`,
		`istio_requests_total{response_code=~"5.."}`,
	} {
		errQuery := fmt.Sprintf(`sum(rate(%s{namespace="%s",pod=~"%s-.*"}[5m]))`, metricName, namespace, name)
		if series := mc.queryRange(ctx, errQuery, beforeStart, afterEnd, step, "error_rate", "errors/s"); series != nil && len(series.DataPoints) > 0 {
			snapshot.ErrorRate = series
			break
		}
	}

	// Latency percentiles
	for _, metricName := range []string{
		"http_request_duration_seconds", "http_server_requests_seconds",
		"istio_request_duration_milliseconds",
	} {
		p50Query := fmt.Sprintf(`histogram_quantile(0.50, sum(rate(%s_bucket{namespace="%s",pod=~"%s-.*"}[5m])) by (le))`,
			metricName, namespace, name)
		if series := mc.queryRange(ctx, p50Query, beforeStart, afterEnd, step, "latency_p50", "seconds"); series != nil && len(series.DataPoints) > 0 {
			snapshot.LatencyP50 = series
			p95Query := fmt.Sprintf(`histogram_quantile(0.95, sum(rate(%s_bucket{namespace="%s",pod=~"%s-.*"}[5m])) by (le))`,
				metricName, namespace, name)
			snapshot.LatencyP95 = mc.queryRange(ctx, p95Query, beforeStart, afterEnd, step, "latency_p95", "seconds")
			p99Query := fmt.Sprintf(`histogram_quantile(0.99, sum(rate(%s_bucket{namespace="%s",pod=~"%s-.*"}[5m])) by (le))`,
				metricName, namespace, name)
			snapshot.LatencyP99 = mc.queryRange(ctx, p99Query, beforeStart, afterEnd, step, "latency_p99", "seconds")
			break
		}
	}

	// HPA metrics
	hpaCurrentQuery := fmt.Sprintf(`kube_horizontalpodautoscaler_status_current_replicas{namespace="%s",horizontalpodautoscaler="%s"}`, namespace, name)
	snapshot.HPACurrentReplicas = mc.queryRange(ctx, hpaCurrentQuery, beforeStart, afterEnd, step, "hpa_current", "replicas")

	hpaDesiredQuery := fmt.Sprintf(`kube_horizontalpodautoscaler_status_desired_replicas{namespace="%s",horizontalpodautoscaler="%s"}`, namespace, name)
	snapshot.HPADesiredReplicas = mc.queryRange(ctx, hpaDesiredQuery, beforeStart, afterEnd, step, "hpa_desired", "replicas")

	// Analyze trends
	snapshot.Trends = mc.analyzeTrends(snapshot, incidentTime)

	// Build summary
	snapshot.Summary = mc.buildMetricsSummary(snapshot)

	return snapshot, nil
}

// queryRange executes a Prometheus range query.
func (mc *MetricsCollector) queryRange(ctx context.Context, query string, start, end time.Time, step, name, unit string) *MetricSeries {
	params := url.Values{
		"query": {query},
		"start": {fmt.Sprintf("%d", start.Unix())},
		"end":   {fmt.Sprintf("%d", end.Unix())},
		"step":  {step},
	}

	reqURL := fmt.Sprintf("%s/api/v1/query_range?%s", mc.prometheusURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil
	}

	resp, err := mc.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil
	}

	var promResp prometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil
	}

	if promResp.Status != "success" || promResp.Data.ResultType != "matrix" {
		return nil
	}

	series := &MetricSeries{
		Name:   name,
		Labels: make(map[string]string),
		Unit:   unit,
	}

	for _, result := range promResp.Data.Result {
		for k, v := range result.Metric {
			series.Labels[k] = v
		}
		for _, val := range result.Values {
			if len(val) == 2 {
				ts, ok1 := val[0].(float64)
				valStr, ok2 := val[1].(string)
				if ok1 && ok2 {
					var v float64
					fmt.Sscanf(valStr, "%f", &v)
					if !math.IsNaN(v) && !math.IsInf(v, 0) {
						series.DataPoints = append(series.DataPoints, MetricDataPoint{
							Timestamp: time.Unix(int64(ts), 0),
							Value:     v,
						})
					}
				}
			}
		}
	}

	if len(series.DataPoints) == 0 {
		return nil
	}

	return series
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string  `json:"metric"`
			Values [][]interface{}    `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// analyzeTrends identifies significant metric changes around the incident.
func (mc *MetricsCollector) analyzeTrends(snapshot *MetricsSnapshot, incidentTime time.Time) []MetricTrend {
	var trends []MetricTrend

	analyzeOne := func(series *MetricSeries) {
		if series == nil || len(series.DataPoints) < 3 {
			return
		}

		var before, during, after []float64
		for _, dp := range series.DataPoints {
			diff := dp.Timestamp.Sub(incidentTime)
			switch {
			case diff < -5*time.Minute:
				before = append(before, dp.Value)
			case diff >= -5*time.Minute && diff <= 5*time.Minute:
				during = append(during, dp.Value)
			case diff > 5*time.Minute:
				after = append(after, dp.Value)
			}
		}

		avgBefore := avg(before)
		avgDuring := avg(during)
		avgAfter := avg(after)

		if avgBefore == 0 && avgDuring == 0 {
			return
		}

		var changePct float64
		if avgBefore > 0 {
			changePct = ((avgDuring - avgBefore) / avgBefore) * 100
		} else if avgDuring > 0 {
			changePct = 100
		}

		direction := "stable"
		significance := "low"

		absPct := math.Abs(changePct)
		switch {
		case absPct > 200:
			significance = "critical"
		case absPct > 100:
			significance = "high"
		case absPct > 50:
			significance = "medium"
		}

		if changePct > 20 {
			direction = "spike"
		} else if changePct < -20 {
			direction = "drop"
		}

		if avgAfter > avgBefore*1.5 {
			direction = "sustained_high"
		} else if avgAfter < avgBefore*0.5 {
			direction = "sustained_low"
		}

		if significance != "low" {
			trends = append(trends, MetricTrend{
				MetricName:    series.Name,
				BeforeValue:   avgBefore,
				DuringValue:   avgDuring,
				AfterValue:    avgAfter,
				ChangePercent: changePct,
				Direction:     direction,
				Significance:  significance,
			})
		}
	}

	analyzeOne(snapshot.CPUUsage)
	analyzeOne(snapshot.MemoryUsage)
	analyzeOne(snapshot.RestartCount)
	analyzeOne(snapshot.RequestRate)
	analyzeOne(snapshot.ErrorRate)
	analyzeOne(snapshot.LatencyP50)
	analyzeOne(snapshot.LatencyP95)
	analyzeOne(snapshot.LatencyP99)
	analyzeOne(snapshot.NetworkReceive)
	analyzeOne(snapshot.NetworkTransmit)
	analyzeOne(snapshot.HPACurrentReplicas)

	// Sort by significance
	sort.Slice(trends, func(i, j int) bool {
		sevOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
		return sevOrder[trends[i].Significance] < sevOrder[trends[j].Significance]
	})

	return trends
}

func avg(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// buildMetricsSummary creates a human-readable metrics summary.
func (mc *MetricsCollector) buildMetricsSummary(snapshot *MetricsSnapshot) string {
	if snapshot == nil {
		return ""
	}

	var parts []string

	for _, t := range snapshot.Trends {
		parts = append(parts, fmt.Sprintf("%s: %s (%.1f%% change, before=%.4f, during=%.4f, after=%.4f) [%s]",
			t.MetricName, t.Direction, t.ChangePercent,
			t.BeforeValue, t.DuringValue, t.AfterValue, t.Significance))
	}

	if len(parts) == 0 {
		return "No significant metric changes detected around incident time."
	}

	return strings.Join(parts, "; ")
}

// FormatForAI formats the metrics snapshot as text for LLM context.
func (s *MetricsSnapshot) FormatForAI() string {
	if s == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Prometheus Metrics Analysis\n\n")

	// Significant trends first (most valuable)
	if len(s.Trends) > 0 {
		sb.WriteString("### Metric Trends (before → during → after incident)\n")
		for _, t := range s.Trends {
			icon := "▲"
			if t.Direction == "drop" || t.Direction == "sustained_low" {
				icon = "▼"
			}
			sb.WriteString(fmt.Sprintf("- **%s** [%s] %s %s: %.4f → %.4f → %.4f (%.1f%% change)\n",
				t.MetricName, t.Significance, icon, t.Direction,
				t.BeforeValue, t.DuringValue, t.AfterValue, t.ChangePercent))
		}
		sb.WriteString("\n")
	}

	// Current metric values
	sb.WriteString("### Current Metric Values\n")

	formatSeries := func(label string, series *MetricSeries) {
		if series == nil || len(series.DataPoints) == 0 {
			return
		}
		latest := series.DataPoints[len(series.DataPoints)-1]
		sb.WriteString(fmt.Sprintf("- %s: %.4f %s (at %s)\n",
			label, latest.Value, series.Unit, latest.Timestamp.Format(time.RFC3339)))
	}

	formatSeries("CPU Usage", s.CPUUsage)
	formatSeries("Memory Usage", s.MemoryUsage)
	formatSeries("Restart Count", s.RestartCount)
	formatSeries("Request Rate", s.RequestRate)
	formatSeries("Error Rate", s.ErrorRate)
	formatSeries("Latency P50", s.LatencyP50)
	formatSeries("Latency P95", s.LatencyP95)
	formatSeries("Latency P99", s.LatencyP99)
	formatSeries("Network RX", s.NetworkReceive)
	formatSeries("Network TX", s.NetworkTransmit)
	formatSeries("HPA Current Replicas", s.HPACurrentReplicas)
	formatSeries("HPA Desired Replicas", s.HPADesiredReplicas)

	sb.WriteString("\n")

	// Correlations
	if len(s.Correlations) > 0 {
		sb.WriteString("### Metric-Event Correlations\n")
		for _, c := range s.Correlations {
			sb.WriteString(fmt.Sprintf("- %s: %s (%s, delta=%s)\n",
				c.MetricName, c.Detail, c.EventType, c.TimeDelta.Round(time.Second)))
		}
		sb.WriteString("\n")
	}

	result := sb.String()
	if len(result) > 4000 {
		result = result[:3997] + "..."
	}
	return result
}
