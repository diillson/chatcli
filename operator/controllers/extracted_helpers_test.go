/*
 * ChatCLI Operator - tests for the phase/section helpers extracted by the
 * gocyclo decomposition (PR: lint baseline zero). Each helper is a pure (or
 * nearly pure) function encoding report/forecast/formatting business logic
 * that previously lived untested inside oversized functions.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// --- compliance_reporter helpers ---

func mkIssue(created time.Time, sev platformv1alpha1.IssueSeverity, state platformv1alpha1.IssueState, detectedAfter, resolvedAfter time.Duration, attempts int32) platformv1alpha1.Issue {
	iss := platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
		Spec:       platformv1alpha1.IssueSpec{Severity: sev},
		Status:     platformv1alpha1.IssueStatus{State: state, RemediationAttempts: attempts},
	}
	if detectedAfter > 0 {
		d := metav1.NewTime(created.Add(detectedAfter))
		iss.Status.DetectedAt = &d
	}
	if resolvedAfter > 0 {
		r := metav1.NewTime(created.Add(resolvedAfter))
		iss.Status.ResolvedAt = &r
	}
	return iss
}

func TestFillIncidentMetrics(t *testing.T) {
	now := time.Now()
	start := now.Add(-24 * time.Hour)
	issues := &platformv1alpha1.IssueList{Items: []platformv1alpha1.Issue{
		// In window: detected in 2m, resolved 10m after detection, 2 attempts.
		mkIssue(now.Add(-2*time.Hour), platformv1alpha1.IssueSeverityHigh, platformv1alpha1.IssueStateResolved, 2*time.Minute, 12*time.Minute, 2),
		// In window: detected in 4m, never resolved, 1 attempt.
		mkIssue(now.Add(-1*time.Hour), platformv1alpha1.IssueSeverityLow, platformv1alpha1.IssueStateRemediating, 4*time.Minute, 0, 1),
		// Out of window: must be ignored entirely.
		mkIssue(now.Add(-48*time.Hour), platformv1alpha1.IssueSeverityHigh, platformv1alpha1.IssueStateResolved, time.Minute, 2*time.Minute, 5),
	}}

	report := &ComplianceReport{}
	detectCount, resolveCount := fillIncidentMetrics(report, issues, start)

	if report.IncidentMetrics.TotalIncidents != 2 {
		t.Fatalf("TotalIncidents = %d, want 2 (window filter)", report.IncidentMetrics.TotalIncidents)
	}
	if detectCount != 2 || resolveCount != 1 {
		t.Fatalf("counts = (%d, %d), want (2, 1)", detectCount, resolveCount)
	}
	if got := report.IncidentMetrics.MTTD; got != 3*time.Minute {
		t.Fatalf("MTTD = %v, want 3m (mean of 2m and 4m)", got)
	}
	if got := report.IncidentMetrics.MTTR; got != 10*time.Minute {
		t.Fatalf("MTTR = %v, want 10m (resolution minus detection)", got)
	}
	if got := report.IncidentMetrics.MeanRemediationAttempts; got != 1.5 {
		t.Fatalf("MeanRemediationAttempts = %v, want 1.5", got)
	}
	if report.IncidentMetrics.BySeverity[string(platformv1alpha1.IssueSeverityHigh)] != 1 {
		t.Fatalf("BySeverity[high] = %d, want 1", report.IncidentMetrics.BySeverity[string(platformv1alpha1.IssueSeverityHigh)])
	}
}

func TestFillSLAMetricsNoIncidentsIsFullCompliance(t *testing.T) {
	report := &ComplianceReport{}
	fillSLAMetrics(report, &platformv1alpha1.IssueList{}, time.Now().Add(-time.Hour), 0, 0)
	if report.SLAMetrics.CompliancePercentage != 100 {
		t.Fatalf("CompliancePercentage = %v, want 100 for zero incidents", report.SLAMetrics.CompliancePercentage)
	}
}

func TestFillSLAMetricsCountsEscalationsAsViolations(t *testing.T) {
	now := time.Now()
	start := now.Add(-24 * time.Hour)
	issues := &platformv1alpha1.IssueList{Items: []platformv1alpha1.Issue{
		mkIssue(now.Add(-2*time.Hour), platformv1alpha1.IssueSeverityHigh, platformv1alpha1.IssueStateEscalated, 2*time.Minute, 0, 1),
		mkIssue(now.Add(-1*time.Hour), platformv1alpha1.IssueSeverityLow, platformv1alpha1.IssueStateResolved, 4*time.Minute, 20*time.Minute, 1),
	}}
	report := &ComplianceReport{}
	detectCount, resolveCount := fillIncidentMetrics(report, issues, start)
	fillSLAMetrics(report, issues, start, detectCount, resolveCount)

	if report.SLAMetrics.ResolutionSLAViolations != 1 {
		t.Fatalf("ResolutionSLAViolations = %d, want 1 (the escalated issue)", report.SLAMetrics.ResolutionSLAViolations)
	}
	if report.SLAMetrics.CompliancePercentage != 50 {
		t.Fatalf("CompliancePercentage = %v, want 50 (1 violation / 2 incidents)", report.SLAMetrics.CompliancePercentage)
	}
}

// --- capacity_planner helpers ---

func TestFillUsageFromDeployment(t *testing.T) {
	deploy := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			}},
		}}},
	}
	forecast := &CapacityForecast{}
	fillUsageFromDeployment(forecast, deploy)

	if forecast.Limits.CPUMillicores != 2000 {
		t.Fatalf("Limits.CPUMillicores = %d, want 2000", forecast.Limits.CPUMillicores)
	}
	if forecast.UsagePercentage.CPU != 25 {
		t.Fatalf("UsagePercentage.CPU = %v, want 25 (500m of 2)", forecast.UsagePercentage.CPU)
	}
	if forecast.UsagePercentage.Memory != 50 {
		t.Fatalf("UsagePercentage.Memory = %v, want 50 (512Mi of 1Gi)", forecast.UsagePercentage.Memory)
	}
}

func TestFillExhaustionForecast(t *testing.T) {
	forecast := &CapacityForecast{}
	forecast.UsagePercentage.CPU = 80
	forecast.Trend.CPUTrendPerDay = 5 // +5%/day → 4 days to 100%
	forecast.UsagePercentage.Memory = 90
	forecast.Trend.MemoryTrendPerDay = -1 // decreasing → no exhaustion

	fillExhaustionForecast(forecast)

	if forecast.Forecast.DaysUntilCPUExhaustion != 4 {
		t.Fatalf("DaysUntilCPUExhaustion = %d, want 4", forecast.Forecast.DaysUntilCPUExhaustion)
	}
	if forecast.Forecast.CPUExhaustionDate == nil {
		t.Fatal("CPUExhaustionDate not set")
	}
	if forecast.Forecast.MemoryExhaustionDate != nil {
		t.Fatal("decreasing memory trend must not forecast exhaustion")
	}
}

func TestFillExhaustionForecastIgnoresBeyondOneYear(t *testing.T) {
	forecast := &CapacityForecast{}
	forecast.UsagePercentage.CPU = 10
	forecast.Trend.CPUTrendPerDay = 0.01 // 9000 days → ignored
	fillExhaustionForecast(forecast)
	if forecast.Forecast.CPUExhaustionDate != nil {
		t.Fatal("horizons beyond a year must be ignored")
	}
}

// --- rune-safe truncation helpers ---

func TestTruncateContextRuneSafe(t *testing.T) {
	if got := truncateContextRuneSafe("short", 100); got != "short" {
		t.Fatalf("under-limit content must pass through, got %q", got)
	}
	// 1 ASCII byte + 3-byte runes: the limit lands mid-rune.
	s := "a" + strings.Repeat("→", 200)
	got := truncateContextRuneSafe(s, 300)
	if !utf8.ValidString(got) {
		t.Fatal("truncation produced invalid UTF-8")
	}
	if !strings.HasSuffix(got, "(context truncated)") {
		t.Fatalf("missing truncation marker: %q", got[len(got)-30:])
	}
}

func TestAuditTruncateRuneSafe(t *testing.T) {
	s := "x" + strings.Repeat("ç", 100)
	got := truncate(s, 51) // mid-rune position for 2-byte runes offset by 1
	if !utf8.ValidString(got) {
		t.Fatal("audit truncate produced invalid UTF-8")
	}
	if got := truncate("ok", 10); got != "ok" {
		t.Fatalf("under-limit input must pass through, got %q", got)
	}
}

// --- log_analyzer section writers (exercised through FormatForAI) ---

func TestFormatForAIRendersAllSections(t *testing.T) {
	r := &LogAnalysisResult{
		StackTraces: []StackTrace{{
			Language: "go", ExceptionType: "panic", Message: "nil deref",
			PodName: "api-0", ContainerName: "api", OccurrenceCount: 3,
			Frames: []string{"main.go:10", "svc.go:22"},
		}},
		ErrorPatterns: []ErrorPattern{{
			Severity: "error", Category: "db", SampleLines: []string{"conn refused"}, Count: 7,
		}},
		StructuredErrors: []StructuredLogEntry{{
			Level: "error", Message: "query failed", Error: "timeout", Logger: "repo",
		}},
		CriticalLines: []CriticalLogLine{{
			PodName: "api-0", ContainerName: "api",
			LinesBefore: []string{"before"}, Line: "FATAL boom", LinesAfter: []string{"after"},
		}},
		InitContainerLogs: []ContainerLogSummary{{
			PodName: "api-0", ContainerName: "init-db", ErrorCount: 1, KeyFindings: []string{"migration failed"},
		}},
		SidecarLogs: []ContainerLogSummary{{
			PodName: "api-0", ContainerName: "envoy", ErrorCount: 2, KeyFindings: []string{"upstream timeout"},
		}},
	}
	out := r.FormatForAI()

	for _, want := range []string{
		"Stack Traces Found", "panic", "main.go:10",
		"Error Patterns Detected", "conn refused",
		"Structured Log Errors", "query failed",
		"Critical Log Lines", "FATAL boom",
		"Init Container Findings", "migration failed",
		"Sidecar api-0/envoy", "upstream timeout",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("FormatForAI missing %q in output", want)
		}
	}
	if (&LogAnalysisResult{}).FormatForAI() == "" {
		t.Fatal("empty result should still render the header")
	}
	var nilResult *LogAnalysisResult
	if nilResult.FormatForAI() != "" {
		t.Fatal("nil result must render empty")
	}
}

func TestFormatForAITruncatesOnRuneBoundary(t *testing.T) {
	frames := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		frames = append(frames, strings.Repeat("ção", 40))
	}
	r := &LogAnalysisResult{}
	for i := 0; i < 30; i++ {
		r.StackTraces = append(r.StackTraces, StackTrace{
			Language: "go", ExceptionType: "erro", Message: strings.Repeat("çã", 60),
			Frames: frames,
		})
	}
	out := r.FormatForAI()
	if len(out) > 6000+len("...") {
		t.Fatalf("FormatForAI exceeded budget: %d bytes", len(out))
	}
	if !utf8.ValidString(out) {
		t.Fatal("FormatForAI truncation produced invalid UTF-8")
	}
}

// --- resources: buildInstanceVolumes (exercised through buildPodSpec) ---

func TestBuildPodSpecAssemblesFeatureVolumes(t *testing.T) {
	r := &InstanceReconciler{}
	enabled := true
	pvcPlugins := "plugins-pvc"
	agentsCM := "agents-cm"
	skillsCM := "skills-cm"
	instance := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: platformv1alpha1.InstanceSpec{
			Persistence: &platformv1alpha1.PersistenceSpec{Enabled: enabled},
			Server: platformv1alpha1.ServerSpec{
				TLS: &platformv1alpha1.TLSSpec{Enabled: true, SecretName: "demo-tls"},
			},
			Agents:  &platformv1alpha1.AgentProvisionSpec{ConfigMapRef: &agentsCM, SkillsConfigMapRef: &skillsCM},
			MCP:     &platformv1alpha1.MCPSpec{Enabled: true},
			Plugins: &platformv1alpha1.PluginProvisionSpec{PVCName: pvcPlugins},
		},
	}

	spec := r.buildPodSpec(instance)
	if len(spec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(spec.Containers))
	}

	wantVolumes := []string{"tmp", "data", "sessions", "tls", "agents", "skills", "mcp-config", "plugins"}
	names := map[string]bool{}
	for _, v := range spec.Volumes {
		names[v.Name] = true
	}
	for _, w := range wantVolumes {
		if !names[w] {
			t.Fatalf("volume %q missing (got %v)", w, names)
		}
	}
	mounts := map[string]bool{}
	for _, m := range spec.Containers[0].VolumeMounts {
		mounts[m.Name] = true
	}
	for _, w := range wantVolumes {
		if !mounts[w] {
			t.Fatalf("volume mount %q missing", w)
		}
	}
	if spec.SecurityContext == nil || spec.SecurityContext.RunAsNonRoot == nil || !*spec.SecurityContext.RunAsNonRoot {
		t.Fatal("default pod security context must enforce runAsNonRoot")
	}
}

func TestBuildPodSpecPluginInitContainer(t *testing.T) {
	r := &InstanceReconciler{}
	instance := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: platformv1alpha1.InstanceSpec{
			Plugins: &platformv1alpha1.PluginProvisionSpec{Image: "ghcr.io/x/plugins:v1"},
		},
	}
	spec := r.buildPodSpec(instance)
	if len(spec.InitContainers) != 1 || spec.InitContainers[0].Name != "plugin-loader" {
		t.Fatalf("expected plugin-loader init container, got %+v", spec.InitContainers)
	}
}

// --- blast_radius: checkQuota (the SA4004 loop→head-take rewrite) ---

func TestCheckQuotaUsesFirstQuotaAndWarnsNearLimit(t *testing.T) {
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "team-quota", Namespace: "prod"},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU:    resource.MustParse("10"),
				corev1.ResourceRequestsMemory: resource.MustParse("10Gi"),
			},
			Used: corev1.ResourceList{
				corev1.ResourceRequestsCPU:    resource.MustParse("9500m"), // >90%
				corev1.ResourceRequestsMemory: resource.MustParse("4Gi"),   // <90%
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(quota).Build()
	bp := NewBlastRadiusPredictor(c)

	prediction := &BlastRadiusPrediction{}
	bp.checkQuota(context.Background(), platformv1alpha1.ResourceRef{Namespace: "prod", Name: "api", Kind: "Deployment"}, prediction)

	if prediction.QuotaCheck == nil || prediction.QuotaCheck.QuotaName != "team-quota" {
		t.Fatalf("QuotaCheck = %+v, want the first quota", prediction.QuotaCheck)
	}
	if prediction.QuotaCheck.CPURemaining != "500m" {
		t.Fatalf("CPURemaining = %q, want 500m", prediction.QuotaCheck.CPURemaining)
	}
	warned := false
	for _, w := range prediction.Warnings {
		if strings.Contains(w, "CPU quota team-quota is >90%") {
			warned = true
		}
		if strings.Contains(w, "Memory quota") {
			t.Fatalf("memory under 90%% must not warn: %v", prediction.Warnings)
		}
	}
	if !warned {
		t.Fatalf("expected CPU >90%% warning, got %v", prediction.Warnings)
	}
}

func TestCheckQuotaNoQuotasIsNoOp(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	bp := NewBlastRadiusPredictor(c)
	prediction := &BlastRadiusPrediction{}
	bp.checkQuota(context.Background(), platformv1alpha1.ResourceRef{Namespace: "prod", Name: "api"}, prediction)
	if prediction.QuotaCheck != nil {
		t.Fatal("no quotas in namespace must leave QuotaCheck nil")
	}
}
