/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PrometheusCollector scrapes /metrics endpoints from pods of a deployment.
type PrometheusCollector struct {
	clientset  kubernetes.Interface
	namespace  string
	deployment string
	port       int
	path       string
	filters    []string // glob patterns for metric names
	httpClient *http.Client
	logger     *zap.Logger
}

// NewPrometheusCollector creates a collector for application-level Prometheus metrics.
func NewPrometheusCollector(
	clientset kubernetes.Interface,
	namespace, deployment string,
	port int, path string,
	filters []string,
	logger *zap.Logger,
) *PrometheusCollector {
	if path == "" {
		path = "/metrics"
	}
	return &PrometheusCollector{
		clientset:  clientset,
		namespace:  namespace,
		deployment: deployment,
		port:       port,
		path:       path,
		filters:    filters,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		logger:     logger,
	}
}

// Collect scrapes metrics from the first Ready pod and returns AppMetrics.
func (c *PrometheusCollector) Collect(ctx context.Context) *AppMetrics {
	deploy, err := c.clientset.AppsV1().Deployments(c.namespace).Get(ctx, c.deployment, metav1.GetOptions{})
	if err != nil {
		c.logger.Debug("PrometheusCollector: failed to get deployment", zap.Error(err))
		return nil
	}

	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		c.logger.Debug("PrometheusCollector: bad selector", zap.Error(err))
		return nil
	}

	pods, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		c.logger.Debug("PrometheusCollector: failed to list pods", zap.Error(err))
		return nil
	}

	var podIP string
	for _, pod := range pods.Items {
		if pod.Status.Phase == "Running" && pod.Status.PodIP != "" {
			podIP = pod.Status.PodIP
			break
		}
	}
	if podIP == "" {
		c.logger.Debug("PrometheusCollector: no ready pod with IP found")
		return nil
	}

	url := fmt.Sprintf("http://%s:%d%s", podIP, c.port, c.path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Debug("PrometheusCollector: scrape failed", zap.String("url", url), zap.Error(err))
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logger.Debug("PrometheusCollector: non-200 status", zap.Int("status", resp.StatusCode))
		return nil
	}

	metrics := ParsePrometheusText(resp.Body, c.filters)
	if len(metrics) == 0 {
		return nil
	}

	return &AppMetrics{
		Timestamp: time.Now(),
		Metrics:   metrics,
	}
}

// ParsePrometheusText parses the Prometheus text exposition format.
// It extracts metric lines matching the filter patterns (empty filters = accept all).
func ParsePrometheusText(reader io.Reader, filters []string) map[string]float64 {
	result := make(map[string]float64)
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}

		name, value, ok := ParseMetricLine(line)
		if !ok {
			continue
		}

		if len(filters) > 0 && !MatchesAnyFilter(name, filters) {
			continue
		}

		result[name] = value
	}

	return result
}

// ParseMetricLine parses a single Prometheus metric line.
// Format: metric_name{labels} value [timestamp]
// or:     metric_name value [timestamp]
func ParseMetricLine(line string) (name string, value float64, ok bool) {
	nameEnd := len(line)
	hasLabels := false
	for i, ch := range line {
		if ch == '{' {
			nameEnd = i
			hasLabels = true
			break
		}
		if ch == ' ' || ch == '\t' {
			nameEnd = i
			break
		}
	}
	name = line[:nameEnd]
	if name == "" {
		return "", 0, false
	}

	var valueStr string
	if hasLabels {
		closeBrace := strings.IndexByte(line, '}')
		if closeBrace < 0 {
			return "", 0, false
		}
		rest := strings.TrimSpace(line[closeBrace+1:])
		parts := strings.Fields(rest)
		if len(parts) == 0 {
			return "", 0, false
		}
		valueStr = parts[0]
	} else {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return "", 0, false
		}
		valueStr = parts[1]
	}

	switch valueStr {
	case "NaN", "+Inf", "-Inf":
		return "", 0, false
	}
	v, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return "", 0, false
	}

	return name, v, true
}

// MatchesAnyFilter returns true if name matches at least one glob pattern.
func MatchesAnyFilter(name string, filters []string) bool {
	for _, pattern := range filters {
		if GlobMatch(pattern, name) {
			return true
		}
	}
	return false
}

// GlobMatch matches a simple glob pattern with * wildcards.
func GlobMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}

	idx := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		pos := strings.Index(s[idx:], part)
		if pos < 0 {
			return false
		}
		if i == 0 && pos != 0 {
			return false
		}
		idx += pos + len(part)
	}

	lastPart := parts[len(parts)-1]
	if lastPart != "" && !strings.HasSuffix(s, lastPart) {
		return false
	}
	return true
}
