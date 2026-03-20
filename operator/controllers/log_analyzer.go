package controllers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// LogAnalyzer performs advanced log analysis for root cause detection.
// It extracts stack traces, detects error patterns, parses structured logs,
// and provides a distilled summary optimized for LLM consumption.
type LogAnalyzer struct {
	client    client.Client
	clientset kubernetes.Interface
}

// LogAnalysisResult contains the complete analysis of application logs.
type LogAnalysisResult struct {
	// StackTraces extracted from logs, grouped by exception type.
	StackTraces []StackTrace
	// ErrorPatterns detected across all containers.
	ErrorPatterns []ErrorPattern
	// StructuredErrors from JSON-formatted log lines.
	StructuredErrors []StructuredLogEntry
	// CriticalLines are high-severity log lines (FATAL, PANIC, ERROR).
	CriticalLines []CriticalLogLine
	// InitContainerLogs from init containers (often reveal startup failures).
	InitContainerLogs []ContainerLogSummary
	// SidecarLogs from sidecar containers (istio-proxy, envoy, etc.).
	SidecarLogs []ContainerLogSummary
	// Summary is a human-readable summary of all findings.
	Summary string
}

// StackTrace represents an extracted stack trace from logs.
type StackTrace struct {
	ExceptionType   string
	Message         string
	Frames          []string
	ContainerName   string
	PodName         string
	Language        string
	RawText         string
	OccurrenceCount int
}

// ErrorPattern represents a recurring error pattern found in logs.
type ErrorPattern struct {
	Pattern     string
	Count       int
	Severity    string
	Category    string
	FirstSeen   string
	LastSeen    string
	SampleLines []string
}

// StructuredLogEntry represents a parsed JSON log line.
type StructuredLogEntry struct {
	Level     string
	Message   string
	Error     string
	Timestamp string
	Logger    string
	Extra     map[string]interface{}
}

// CriticalLogLine is a single critical log line with context.
type CriticalLogLine struct {
	Line          string
	Level         string
	ContainerName string
	PodName       string
	LinesBefore   []string
	LinesAfter    []string
}

// ContainerLogSummary holds analyzed logs for a single container.
type ContainerLogSummary struct {
	PodName       string
	ContainerName string
	ContainerType string // "init", "sidecar", "main"
	ErrorCount    int
	WarnCount     int
	KeyFindings   []string
}

// NewLogAnalyzer creates a new LogAnalyzer.
func NewLogAnalyzer(c client.Client, clientset kubernetes.Interface) *LogAnalyzer {
	return &LogAnalyzer{client: c, clientset: clientset}
}

// Common error patterns by language/framework
var (
	javaStackTraceStart  = regexp.MustCompile(`(?i)^(Exception|Error|Caused by|java\.\w+\.\w+Exception|javax\.\w+|org\.\w+\.(\w+\.)*\w+Exception)`)
	javaStackTraceFrame  = regexp.MustCompile(`^\s+at\s+[\w.$]+\([\w.]+:\d+\)`)
	javaStackTraceCaused = regexp.MustCompile(`^Caused by:\s+`)

	goStackTraceStart = regexp.MustCompile(`^(goroutine \d+ \[|panic:|runtime error:)`)
	goStackTraceFrame = regexp.MustCompile(`^\s+[\w/.-]+\.go:\d+`)
	goPanicLine       = regexp.MustCompile(`^panic:`)

	pythonStackTraceStart = regexp.MustCompile(`^Traceback \(most recent call last\):`)
	pythonStackTraceFrame = regexp.MustCompile(`^\s+File "`)
	pythonExceptionLine   = regexp.MustCompile(`^(\w+Error|\w+Exception|\w+Warning):`)

	nodeStackTraceStart = regexp.MustCompile(`^(\w*Error):`)
	nodeStackTraceFrame = regexp.MustCompile(`^\s+at\s+`)

	criticalPatterns = []struct {
		Pattern  *regexp.Regexp
		Severity string
		Category string
	}{
		{regexp.MustCompile(`(?i)\bpanic\b`), "critical", "crash"},
		{regexp.MustCompile(`(?i)\bFATAL\b`), "critical", "crash"},
		{regexp.MustCompile(`(?i)\bOOM\b|Out[Oo]f[Mm]emory|cannot allocate memory`), "critical", "resource"},
		{regexp.MustCompile(`(?i)killed|SIGKILL|signal 9`), "critical", "resource"},
		{regexp.MustCompile(`(?i)connection refused`), "high", "connectivity"},
		{regexp.MustCompile(`(?i)connection timed? ?out`), "high", "connectivity"},
		{regexp.MustCompile(`(?i)no such host|DNS|name resolution`), "high", "dns"},
		{regexp.MustCompile(`(?i)permission denied|access denied|unauthorized|forbidden`), "high", "auth"},
		{regexp.MustCompile(`(?i)no space left on device|disk full`), "critical", "storage"},
		{regexp.MustCompile(`(?i)too many open files|file descriptor`), "high", "resource"},
		{regexp.MustCompile(`(?i)deadlock|lock timeout`), "high", "concurrency"},
		{regexp.MustCompile(`(?i)segmentation fault|SIGSEGV|segfault`), "critical", "crash"},
		{regexp.MustCompile(`(?i)certificate.*(expired|invalid|verify)|TLS|SSL`), "high", "tls"},
		{regexp.MustCompile(`(?i)context deadline exceeded|context canceled`), "medium", "timeout"},
		{regexp.MustCompile(`(?i)read: connection reset|broken pipe`), "medium", "connectivity"},
		{regexp.MustCompile(`(?i)dial tcp.*: i/o timeout`), "high", "connectivity"},
		{regexp.MustCompile(`(?i)ImagePullBackOff|ErrImagePull`), "high", "image"},
		{regexp.MustCompile(`(?i)CrashLoopBackOff`), "critical", "crash"},
		{regexp.MustCompile(`(?i)failed to start container`), "high", "startup"},
		{regexp.MustCompile(`(?i)liveness probe failed|readiness probe failed`), "medium", "health"},
		{regexp.MustCompile(`(?i)exec format error|cannot execute binary`), "critical", "binary"},
		{regexp.MustCompile(`(?i)database.*(down|unavailable|connection)|SQLSTATE`), "high", "database"},
		{regexp.MustCompile(`(?i)redis.*(down|unavailable|connection)`), "high", "cache"},
		{regexp.MustCompile(`(?i)kafka.*(down|unavailable|connection|rebalance)`), "high", "messaging"},
	}

	sidecarContainers = map[string]bool{
		"istio-proxy": true, "envoy": true, "linkerd-proxy": true,
		"datadog-agent": true, "fluentd": true, "fluent-bit": true,
		"filebeat": true, "promtail": true, "vault-agent": true,
	}
)

// AnalyzePodLogs performs deep log analysis for all pods of a resource.
func (la *LogAnalyzer) AnalyzePodLogs(ctx context.Context, resource platformv1alpha1.ResourceRef, incidentTime time.Time) (*LogAnalysisResult, error) {
	if la.clientset == nil {
		return nil, fmt.Errorf("kubernetes clientset not available")
	}

	var podList corev1.PodList
	if err := la.client.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	// Filter pods related to this resource
	var relatedPods []corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if isResourcePod(pod, resource) {
			relatedPods = append(relatedPods, *pod)
		}
	}

	if len(relatedPods) == 0 {
		return &LogAnalysisResult{Summary: "No pods found for resource."}, nil
	}

	// Sort: unhealthy first, then by restart count
	sort.Slice(relatedPods, func(i, j int) bool {
		iReady := isPodReady(&relatedPods[i])
		jReady := isPodReady(&relatedPods[j])
		if iReady != jReady {
			return !iReady
		}
		return podRestartCount(&relatedPods[i]) > podRestartCount(&relatedPods[j])
	})

	// Limit analysis to top 5 pods
	if len(relatedPods) > 5 {
		relatedPods = relatedPods[:5]
	}

	result := &LogAnalysisResult{}

	for _, pod := range relatedPods {
		// Analyze init containers
		for _, cs := range pod.Status.InitContainerStatuses {
			la.analyzeContainer(ctx, result, pod.Name, cs.Name, resource.Namespace,
				"init", cs.RestartCount, incidentTime)
		}

		// Analyze all regular containers
		for _, cs := range pod.Status.ContainerStatuses {
			containerType := "main"
			if sidecarContainers[cs.Name] {
				containerType = "sidecar"
			}
			la.analyzeContainer(ctx, result, pod.Name, cs.Name, resource.Namespace,
				containerType, cs.RestartCount, incidentTime)
		}
	}

	// Deduplicate stack traces
	result.StackTraces = deduplicateStackTraces(result.StackTraces)

	// Aggregate error patterns
	result.ErrorPatterns = aggregateErrorPatterns(result.ErrorPatterns)

	// Build summary
	result.Summary = buildLogAnalysisSummary(result)

	return result, nil
}

// analyzeContainer fetches and analyzes logs from a single container.
func (la *LogAnalyzer) analyzeContainer(ctx context.Context, result *LogAnalysisResult,
	podName, containerName, namespace, containerType string, restartCount int32,
	incidentTime time.Time) {

	// Fetch current logs (up to 500 lines for deeper analysis)
	lines := la.fetchLogs(ctx, podName, containerName, namespace, 500, false, incidentTime)

	if len(lines) > 0 {
		// Extract stack traces
		traces := extractStackTraces(lines, podName, containerName)
		result.StackTraces = append(result.StackTraces, traces...)

		// Detect error patterns
		patterns := detectErrorPatterns(lines)
		result.ErrorPatterns = append(result.ErrorPatterns, patterns...)

		// Parse structured JSON logs
		structured := parseStructuredLogs(lines)
		result.StructuredErrors = append(result.StructuredErrors, structured...)

		// Extract critical lines with context
		critical := extractCriticalLines(lines, podName, containerName)
		result.CriticalLines = append(result.CriticalLines, critical...)

		// Build container summary
		summary := buildContainerSummary(lines, podName, containerName, containerType)
		switch containerType {
		case "init":
			result.InitContainerLogs = append(result.InitContainerLogs, summary)
		case "sidecar":
			result.SidecarLogs = append(result.SidecarLogs, summary)
		}
	}

	// Fetch previous container logs if restarts occurred
	if restartCount > 0 {
		prevLines := la.fetchLogs(ctx, podName, containerName, namespace, 300, true, incidentTime)
		if len(prevLines) > 0 {
			traces := extractStackTraces(prevLines, podName, containerName+" (previous)")
			result.StackTraces = append(result.StackTraces, traces...)

			patterns := detectErrorPatterns(prevLines)
			result.ErrorPatterns = append(result.ErrorPatterns, patterns...)

			critical := extractCriticalLines(prevLines, podName, containerName+" (previous)")
			result.CriticalLines = append(result.CriticalLines, critical...)
		}
	}
}

// fetchLogs retrieves logs from a container with intelligent windowing.
func (la *LogAnalyzer) fetchLogs(ctx context.Context, podName, containerName, namespace string,
	tailLines int64, previous bool, incidentTime time.Time) []string {

	opts := &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &tailLines,
		Previous:  previous,
	}

	// If we have an incident time, use sinceTime to get logs around the incident
	if !incidentTime.IsZero() && !previous {
		sinceTime := incidentTime.Add(-10 * time.Minute)
		sinceTimeMeta := &sinceTime
		opts.SinceTime = &metav1.Time{Time: *sinceTimeMeta}
		opts.TailLines = nil // use sinceTime instead
	}

	req := la.clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil
	}
	defer stream.Close()

	var lines []string
	scanner := bufio.NewScanner(io.LimitReader(stream, 512*1024)) // 512KB max
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines
}

// extractStackTraces identifies and extracts stack traces from log lines.
func extractStackTraces(lines []string, podName, containerName string) []StackTrace {
	var traces []StackTrace
	i := 0
	for i < len(lines) {
		// Try Java stack trace
		if trace, consumed := tryExtractJavaStackTrace(lines, i, podName, containerName); consumed > 0 {
			traces = append(traces, trace)
			i += consumed
			continue
		}

		// Try Go panic/stack trace
		if trace, consumed := tryExtractGoStackTrace(lines, i, podName, containerName); consumed > 0 {
			traces = append(traces, trace)
			i += consumed
			continue
		}

		// Try Python traceback
		if trace, consumed := tryExtractPythonTraceback(lines, i, podName, containerName); consumed > 0 {
			traces = append(traces, trace)
			i += consumed
			continue
		}

		// Try Node.js error
		if trace, consumed := tryExtractNodeStackTrace(lines, i, podName, containerName); consumed > 0 {
			traces = append(traces, trace)
			i += consumed
			continue
		}

		i++
	}
	return traces
}

func tryExtractJavaStackTrace(lines []string, start int, podName, containerName string) (StackTrace, int) {
	if start >= len(lines) {
		return StackTrace{}, 0
	}

	line := lines[start]
	if !javaStackTraceStart.MatchString(line) && !strings.Contains(line, "Exception") && !strings.Contains(line, "Error:") {
		return StackTrace{}, 0
	}

	// Look ahead for "at" frames
	if start+1 < len(lines) && !javaStackTraceFrame.MatchString(lines[start+1]) {
		return StackTrace{}, 0
	}

	trace := StackTrace{
		Language:        "java",
		PodName:         podName,
		ContainerName:   containerName,
		OccurrenceCount: 1,
	}

	// Parse exception type and message
	parts := strings.SplitN(line, ":", 2)
	trace.ExceptionType = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		trace.Message = strings.TrimSpace(parts[1])
	}

	var rawLines []string
	rawLines = append(rawLines, line)

	consumed := 1
	for i := start + 1; i < len(lines) && i < start+100; i++ {
		l := lines[i]
		if javaStackTraceFrame.MatchString(l) {
			trace.Frames = append(trace.Frames, strings.TrimSpace(l))
			rawLines = append(rawLines, l)
			consumed++
		} else if javaStackTraceCaused.MatchString(l) {
			rawLines = append(rawLines, l)
			consumed++
		} else {
			break
		}
	}

	if len(trace.Frames) == 0 {
		return StackTrace{}, 0
	}

	trace.RawText = strings.Join(rawLines, "\n")
	return trace, consumed
}

func tryExtractGoStackTrace(lines []string, start int, podName, containerName string) (StackTrace, int) {
	if start >= len(lines) {
		return StackTrace{}, 0
	}

	line := lines[start]
	if !goStackTraceStart.MatchString(line) && !goPanicLine.MatchString(line) {
		return StackTrace{}, 0
	}

	trace := StackTrace{
		Language:        "go",
		PodName:         podName,
		ContainerName:   containerName,
		OccurrenceCount: 1,
	}

	if goPanicLine.MatchString(line) {
		trace.ExceptionType = "panic"
		trace.Message = strings.TrimPrefix(line, "panic: ")
	} else {
		trace.ExceptionType = "goroutine"
		trace.Message = line
	}

	var rawLines []string
	rawLines = append(rawLines, line)

	consumed := 1
	for i := start + 1; i < len(lines) && i < start+80; i++ {
		l := lines[i]
		if goStackTraceFrame.MatchString(l) || strings.HasPrefix(l, "goroutine ") ||
			strings.Contains(l, ".go:") || strings.HasPrefix(l, "\t") {
			trace.Frames = append(trace.Frames, strings.TrimSpace(l))
			rawLines = append(rawLines, l)
			consumed++
		} else if l == "" {
			consumed++
			break
		} else {
			rawLines = append(rawLines, l)
			consumed++
			if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") && consumed > 2 {
				break
			}
		}
	}

	trace.RawText = strings.Join(rawLines, "\n")
	return trace, consumed
}

func tryExtractPythonTraceback(lines []string, start int, podName, containerName string) (StackTrace, int) {
	if start >= len(lines) {
		return StackTrace{}, 0
	}

	if !pythonStackTraceStart.MatchString(lines[start]) {
		return StackTrace{}, 0
	}

	trace := StackTrace{
		Language:        "python",
		PodName:         podName,
		ContainerName:   containerName,
		OccurrenceCount: 1,
	}

	var rawLines []string
	rawLines = append(rawLines, lines[start])

	consumed := 1
	for i := start + 1; i < len(lines) && i < start+60; i++ {
		l := lines[i]
		rawLines = append(rawLines, l)
		consumed++

		if pythonStackTraceFrame.MatchString(l) {
			trace.Frames = append(trace.Frames, strings.TrimSpace(l))
		} else if pythonExceptionLine.MatchString(l) {
			parts := strings.SplitN(l, ":", 2)
			trace.ExceptionType = strings.TrimSpace(parts[0])
			if len(parts) > 1 {
				trace.Message = strings.TrimSpace(parts[1])
			}
			break
		}
	}

	if trace.ExceptionType == "" {
		return StackTrace{}, 0
	}

	trace.RawText = strings.Join(rawLines, "\n")
	return trace, consumed
}

func tryExtractNodeStackTrace(lines []string, start int, podName, containerName string) (StackTrace, int) {
	if start >= len(lines) {
		return StackTrace{}, 0
	}

	if !nodeStackTraceStart.MatchString(lines[start]) {
		return StackTrace{}, 0
	}

	// Must have "at" frames following
	if start+1 >= len(lines) || !nodeStackTraceFrame.MatchString(lines[start+1]) {
		return StackTrace{}, 0
	}

	trace := StackTrace{
		Language:        "nodejs",
		PodName:         podName,
		ContainerName:   containerName,
		OccurrenceCount: 1,
	}

	parts := strings.SplitN(lines[start], ":", 2)
	trace.ExceptionType = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		trace.Message = strings.TrimSpace(parts[1])
	}

	var rawLines []string
	rawLines = append(rawLines, lines[start])

	consumed := 1
	for i := start + 1; i < len(lines) && i < start+40; i++ {
		l := lines[i]
		if nodeStackTraceFrame.MatchString(l) {
			trace.Frames = append(trace.Frames, strings.TrimSpace(l))
			rawLines = append(rawLines, l)
			consumed++
		} else {
			break
		}
	}

	trace.RawText = strings.Join(rawLines, "\n")
	return trace, consumed
}

// detectErrorPatterns scans log lines for known error patterns.
func detectErrorPatterns(lines []string) []ErrorPattern {
	patternCounts := make(map[string]*ErrorPattern)

	for _, line := range lines {
		for _, cp := range criticalPatterns {
			if cp.Pattern.MatchString(line) {
				key := cp.Category + ":" + cp.Pattern.String()
				if existing, ok := patternCounts[key]; ok {
					existing.Count++
					existing.LastSeen = line
					if len(existing.SampleLines) < 3 {
						existing.SampleLines = append(existing.SampleLines, truncateLine(line, 200))
					}
				} else {
					patternCounts[key] = &ErrorPattern{
						Pattern:     cp.Pattern.String(),
						Count:       1,
						Severity:    cp.Severity,
						Category:    cp.Category,
						FirstSeen:   truncateLine(line, 200),
						LastSeen:    truncateLine(line, 200),
						SampleLines: []string{truncateLine(line, 200)},
					}
				}
			}
		}
	}

	var patterns []ErrorPattern
	for _, p := range patternCounts {
		patterns = append(patterns, *p)
	}

	// Sort by severity then count
	sort.Slice(patterns, func(i, j int) bool {
		sevOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
		if sevOrder[patterns[i].Severity] != sevOrder[patterns[j].Severity] {
			return sevOrder[patterns[i].Severity] < sevOrder[patterns[j].Severity]
		}
		return patterns[i].Count > patterns[j].Count
	})

	return patterns
}

// parseStructuredLogs extracts error-level entries from JSON-formatted logs.
func parseStructuredLogs(lines []string) []StructuredLogEntry {
	var entries []StructuredLogEntry

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue
		}

		entry := StructuredLogEntry{Extra: make(map[string]interface{})}

		// Extract level (common field names)
		for _, key := range []string{"level", "severity", "log_level", "loglevel", "lvl"} {
			if v, ok := parsed[key]; ok {
				entry.Level = fmt.Sprintf("%v", v)
				delete(parsed, key)
				break
			}
		}

		// Only keep error-level entries
		level := strings.ToLower(entry.Level)
		if level != "error" && level != "fatal" && level != "panic" &&
			level != "err" && level != "critical" && level != "warn" && level != "warning" {
			continue
		}

		// Extract message
		for _, key := range []string{"msg", "message", "text", "error_message"} {
			if v, ok := parsed[key]; ok {
				entry.Message = fmt.Sprintf("%v", v)
				delete(parsed, key)
				break
			}
		}

		// Extract error
		for _, key := range []string{"error", "err", "exception", "error_details", "stack_trace"} {
			if v, ok := parsed[key]; ok {
				entry.Error = fmt.Sprintf("%v", v)
				delete(parsed, key)
				break
			}
		}

		// Extract timestamp
		for _, key := range []string{"time", "timestamp", "ts", "@timestamp", "datetime"} {
			if v, ok := parsed[key]; ok {
				entry.Timestamp = fmt.Sprintf("%v", v)
				delete(parsed, key)
				break
			}
		}

		// Extract logger
		for _, key := range []string{"logger", "caller", "source", "component"} {
			if v, ok := parsed[key]; ok {
				entry.Logger = fmt.Sprintf("%v", v)
				delete(parsed, key)
				break
			}
		}

		// Remaining fields as extra context
		for k, v := range parsed {
			entry.Extra[k] = v
		}

		entries = append(entries, entry)
	}

	// Limit entries to avoid overwhelming the LLM
	if len(entries) > 20 {
		entries = entries[len(entries)-20:]
	}

	return entries
}

// extractCriticalLines extracts FATAL/PANIC/ERROR lines with surrounding context.
func extractCriticalLines(lines []string, podName, containerName string) []CriticalLogLine {
	var critical []CriticalLogLine
	critRegex := regexp.MustCompile(`(?i)\b(FATAL|PANIC|CRITICAL)\b`)

	for i, line := range lines {
		if !critRegex.MatchString(line) {
			continue
		}

		cl := CriticalLogLine{
			Line:          truncateLine(line, 300),
			Level:         "critical",
			ContainerName: containerName,
			PodName:       podName,
		}

		// Context: 3 lines before
		start := i - 3
		if start < 0 {
			start = 0
		}
		for j := start; j < i; j++ {
			cl.LinesBefore = append(cl.LinesBefore, truncateLine(lines[j], 200))
		}

		// Context: 3 lines after
		end := i + 4
		if end > len(lines) {
			end = len(lines)
		}
		for j := i + 1; j < end; j++ {
			cl.LinesAfter = append(cl.LinesAfter, truncateLine(lines[j], 200))
		}

		critical = append(critical, cl)
	}

	// Limit to 10 critical lines
	if len(critical) > 10 {
		critical = critical[len(critical)-10:]
	}

	return critical
}

// buildContainerSummary creates a summary of findings for a container's logs.
func buildContainerSummary(lines []string, podName, containerName, containerType string) ContainerLogSummary {
	summary := ContainerLogSummary{
		PodName:       podName,
		ContainerName: containerName,
		ContainerType: containerType,
	}

	errorRegex := regexp.MustCompile(`(?i)\b(ERROR|FATAL|PANIC|CRITICAL)\b`)
	warnRegex := regexp.MustCompile(`(?i)\b(WARN|WARNING)\b`)

	for _, line := range lines {
		if errorRegex.MatchString(line) {
			summary.ErrorCount++
		} else if warnRegex.MatchString(line) {
			summary.WarnCount++
		}
	}

	if summary.ErrorCount > 0 {
		summary.KeyFindings = append(summary.KeyFindings,
			fmt.Sprintf("%d error-level log lines detected", summary.ErrorCount))
	}
	if summary.WarnCount > 0 {
		summary.KeyFindings = append(summary.KeyFindings,
			fmt.Sprintf("%d warning-level log lines detected", summary.WarnCount))
	}

	return summary
}

// deduplicateStackTraces merges identical stack traces and increments their count.
func deduplicateStackTraces(traces []StackTrace) []StackTrace {
	seen := make(map[string]int) // key -> index in result
	var result []StackTrace

	for _, t := range traces {
		key := t.ExceptionType + ":" + t.Message
		if idx, ok := seen[key]; ok {
			result[idx].OccurrenceCount++
		} else {
			seen[key] = len(result)
			result = append(result, t)
		}
	}
	return result
}

// aggregateErrorPatterns merges patterns with the same category.
func aggregateErrorPatterns(patterns []ErrorPattern) []ErrorPattern {
	seen := make(map[string]int)
	var result []ErrorPattern

	for _, p := range patterns {
		key := p.Category + ":" + p.Severity
		if idx, ok := seen[key]; ok {
			result[idx].Count += p.Count
			if len(result[idx].SampleLines) < 3 {
				for _, sl := range p.SampleLines {
					if len(result[idx].SampleLines) < 3 {
						result[idx].SampleLines = append(result[idx].SampleLines, sl)
					}
				}
			}
		} else {
			seen[key] = len(result)
			result = append(result, p)
		}
	}
	return result
}

// FormatForAI formats the log analysis result as a text block suitable for LLM context.
func (r *LogAnalysisResult) FormatForAI() string {
	if r == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Application Log Analysis\n\n")

	// Stack traces (most valuable for root cause)
	if len(r.StackTraces) > 0 {
		sb.WriteString("### Stack Traces Found\n")
		for i, t := range r.StackTraces {
			if i >= 5 {
				sb.WriteString(fmt.Sprintf("... and %d more stack traces\n", len(r.StackTraces)-5))
				break
			}
			sb.WriteString(fmt.Sprintf("\n**[%s] %s: %s** (pod=%s, container=%s, occurrences=%d)\n",
				t.Language, t.ExceptionType, t.Message, t.PodName, t.ContainerName, t.OccurrenceCount))
			// Show top 10 frames
			maxFrames := 10
			if len(t.Frames) < maxFrames {
				maxFrames = len(t.Frames)
			}
			for _, f := range t.Frames[:maxFrames] {
				sb.WriteString(fmt.Sprintf("  %s\n", f))
			}
			if len(t.Frames) > 10 {
				sb.WriteString(fmt.Sprintf("  ... %d more frames\n", len(t.Frames)-10))
			}
		}
		sb.WriteString("\n")
	}

	// Error patterns
	if len(r.ErrorPatterns) > 0 {
		sb.WriteString("### Error Patterns Detected\n")
		for i, p := range r.ErrorPatterns {
			if i >= 10 {
				break
			}
			sb.WriteString(fmt.Sprintf("- [%s/%s] %s (count=%d)\n",
				p.Severity, p.Category, p.SampleLines[0], p.Count))
		}
		sb.WriteString("\n")
	}

	// Structured log errors
	if len(r.StructuredErrors) > 0 {
		sb.WriteString("### Structured Log Errors\n")
		for i, e := range r.StructuredErrors {
			if i >= 10 {
				break
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s", e.Level, e.Message))
			if e.Error != "" {
				sb.WriteString(fmt.Sprintf(" error=%s", truncateLine(e.Error, 150)))
			}
			if e.Logger != "" {
				sb.WriteString(fmt.Sprintf(" logger=%s", e.Logger))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Critical lines
	if len(r.CriticalLines) > 0 {
		sb.WriteString("### Critical Log Lines\n")
		for i, cl := range r.CriticalLines {
			if i >= 5 {
				break
			}
			sb.WriteString(fmt.Sprintf("Pod=%s Container=%s:\n", cl.PodName, cl.ContainerName))
			for _, b := range cl.LinesBefore {
				sb.WriteString(fmt.Sprintf("    %s\n", b))
			}
			sb.WriteString(fmt.Sprintf(" >> %s\n", cl.Line))
			for _, a := range cl.LinesAfter {
				sb.WriteString(fmt.Sprintf("    %s\n", a))
			}
			sb.WriteString("\n")
		}
	}

	// Init container findings
	if len(r.InitContainerLogs) > 0 {
		sb.WriteString("### Init Container Findings\n")
		for _, s := range r.InitContainerLogs {
			if s.ErrorCount > 0 || len(s.KeyFindings) > 0 {
				sb.WriteString(fmt.Sprintf("- %s/%s: errors=%d warnings=%d\n",
					s.PodName, s.ContainerName, s.ErrorCount, s.WarnCount))
				for _, f := range s.KeyFindings {
					sb.WriteString(fmt.Sprintf("  - %s\n", f))
				}
			}
		}
		sb.WriteString("\n")
	}

	// Sidecar findings
	if len(r.SidecarLogs) > 0 {
		for _, s := range r.SidecarLogs {
			if s.ErrorCount > 0 {
				sb.WriteString(fmt.Sprintf("### Sidecar %s/%s: errors=%d\n",
					s.PodName, s.ContainerName, s.ErrorCount))
				for _, f := range s.KeyFindings {
					sb.WriteString(fmt.Sprintf("- %s\n", f))
				}
			}
		}
		sb.WriteString("\n")
	}

	result := sb.String()
	if len(result) > 6000 {
		result = result[:5997] + "..."
	}
	return result
}

// isResourcePod checks if a pod belongs to the given resource (Deployment, StatefulSet, DaemonSet, Job).
func isResourcePod(pod *corev1.Pod, resource platformv1alpha1.ResourceRef) bool {
	for _, ref := range pod.OwnerReferences {
		switch resource.Kind {
		case "Deployment":
			if ref.Kind == "ReplicaSet" && strings.HasPrefix(ref.Name, resource.Name+"-") {
				return true
			}
		case "StatefulSet":
			if ref.Kind == "StatefulSet" && ref.Name == resource.Name {
				return true
			}
		case "DaemonSet":
			if ref.Kind == "DaemonSet" && ref.Name == resource.Name {
				return true
			}
		case "Job":
			if ref.Kind == "Job" && ref.Name == resource.Name {
				return true
			}
		}
	}
	// Fallback for CronJob-spawned Jobs
	if resource.Kind == "CronJob" {
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "Job" && strings.HasPrefix(ref.Name, resource.Name+"-") {
				return true
			}
		}
	}
	return false
}

// buildLogAnalysisSummary creates a concise summary of all findings.
func buildLogAnalysisSummary(result *LogAnalysisResult) string {
	var parts []string

	if len(result.StackTraces) > 0 {
		languages := make(map[string]int)
		for _, t := range result.StackTraces {
			languages[t.Language] += t.OccurrenceCount
		}
		for lang, count := range languages {
			parts = append(parts, fmt.Sprintf("%d %s stack trace(s)", count, lang))
		}
		// Most critical stack trace
		parts = append(parts, fmt.Sprintf("Primary exception: %s: %s",
			result.StackTraces[0].ExceptionType, truncateLine(result.StackTraces[0].Message, 100)))
	}

	criticalCount := 0
	highCount := 0
	for _, p := range result.ErrorPatterns {
		if p.Severity == "critical" {
			criticalCount += p.Count
		} else if p.Severity == "high" {
			highCount += p.Count
		}
	}
	if criticalCount > 0 {
		parts = append(parts, fmt.Sprintf("%d critical error pattern occurrences", criticalCount))
	}
	if highCount > 0 {
		parts = append(parts, fmt.Sprintf("%d high-severity error pattern occurrences", highCount))
	}

	if len(result.StructuredErrors) > 0 {
		parts = append(parts, fmt.Sprintf("%d structured error log entries", len(result.StructuredErrors)))
	}

	initErrors := 0
	for _, s := range result.InitContainerLogs {
		initErrors += s.ErrorCount
	}
	if initErrors > 0 {
		parts = append(parts, fmt.Sprintf("%d init container errors (possible startup failure)", initErrors))
	}

	if len(parts) == 0 {
		return "No significant errors found in application logs."
	}

	return strings.Join(parts, "; ")
}

func truncateLine(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// Ensure metav1 import is used.
var _ metav1.Time
