/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetAlerts returns current watcher alerts for the AIOps operator.
func (h *Handler) GetAlerts(ctx context.Context, req *pb.GetAlertsRequest) (*pb.GetAlertsResponse, error) {
	if h.watcherAlertsFunc == nil {
		return &pb.GetAlertsResponse{}, nil
	}

	alerts := h.watcherAlertsFunc()
	var result []*pb.WatcherAlert
	for _, a := range alerts {
		if req.Namespace != "" && a.Namespace != req.Namespace {
			continue
		}
		if req.Deployment != "" && a.Deployment != req.Deployment {
			continue
		}
		result = append(result, &pb.WatcherAlert{
			Type:          a.Type,
			Severity:      a.Severity,
			Message:       a.Message,
			Object:        a.Object,
			Namespace:     a.Namespace,
			Deployment:    a.Deployment,
			TimestampUnix: a.Timestamp.Unix(),
		})
	}

	return &pb.GetAlertsResponse{Alerts: result}, nil
}

// AnalyzeIssue uses the LLM to analyze an AIOps issue and return recommendations.
func (h *Handler) AnalyzeIssue(ctx context.Context, req *pb.AnalyzeIssueRequest) (*pb.AnalyzeIssueResponse, error) {
	if req.IssueName == "" {
		return nil, status.Error(codes.InvalidArgument, "issue_name is required")
	}

	llmClient, err := h.getClient(req.Provider, req.Model, "", nil)
	if err != nil {
		h.logger.Error("Failed to get LLM client for analysis", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get LLM client: %v", err)
	}

	prompt := buildAnalysisPrompt(req)

	// Enrich with K8s context if available
	enrichedPrompt := h.enrichPrompt(prompt)

	response, err := llmClient.SendPrompt(ctx, enrichedPrompt, nil, 0)
	if err != nil {
		h.logger.Error("LLM analysis failed", zap.Error(err), zap.String("issue", req.IssueName))
		return nil, status.Errorf(codes.Internal, "LLM analysis failed: %v", err)
	}

	analysis := parseAnalysisResponse(response)

	provider := req.Provider
	if provider == "" {
		provider = h.defaultProvider
	}

	// Map parsed actions to proto SuggestedAction
	var suggestedActions []*pb.SuggestedAction
	for _, a := range analysis.Actions {
		suggestedActions = append(suggestedActions, &pb.SuggestedAction{
			Name:        a.Name,
			Action:      a.Action,
			Description: a.Description,
			Params:      a.Params,
		})
	}

	return &pb.AnalyzeIssueResponse{
		Analysis:         analysis.Analysis,
		Confidence:       analysis.Confidence,
		Recommendations:  analysis.Recommendations,
		Model:            llmClient.GetModelName(),
		Provider:         provider,
		SuggestedActions: suggestedActions,
	}, nil
}

func buildAnalysisPrompt(req *pb.AnalyzeIssueRequest) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are a Kubernetes SRE expert. Analyze the following issue and provide a structured assessment with concrete remediation actions.

Issue Details:
- Name: %s
- Namespace: %s
- Resource: %s/%s
- Signal Type: %s
- Severity: %s
- Description: %s
- Risk Score: %d/100`,
		req.IssueName, req.Namespace, req.ResourceKind, req.ResourceName,
		req.SignalType, req.Severity, req.Description, req.RiskScore))

	if req.KubernetesContext != "" {
		sb.WriteString(fmt.Sprintf(`

Kubernetes Cluster Context:
%s`, req.KubernetesContext))
	}

	if req.PreviousFailureContext != "" {
		sb.WriteString(fmt.Sprintf(`

Previous Remediation Attempts (FAILED — you MUST suggest a DIFFERENT strategy):
%s

IMPORTANT: The previous remediation attempts listed above have FAILED. Do NOT repeat the same actions. Analyze why they failed and suggest a fundamentally different approach.`, req.PreviousFailureContext))
	}

	sb.WriteString(`

Available remediation actions (use ONLY these):

WORKLOAD ACTIONS:
1. RestartDeployment — triggers a rolling restart of all pods. No params needed.
   Best for: stale state, memory leaks, transient errors.

2. ScaleDeployment — scales the deployment up or down. Params: {"replicas": "N"} (N >= 1).
   Best for: load-related issues, insufficient capacity.

3. RollbackDeployment — rolls back to a previous deployment revision.
   Params (optional): {"toRevision": "<number|previous|healthy>"}
   Best for: bad deployments, image bugs, config regressions.

4. AdjustResources — changes CPU/memory requests and limits on a container.
   Params: {"container": "name", "memory_limit": "1Gi", "memory_request": "512Mi", "cpu_limit": "1000m", "cpu_request": "500m"}
   Best for: OOMKilled pods, CPU throttling, resource quota issues.

5. DeletePod — deletes a single unhealthy pod.
   Params (optional): {"pod": "specific-pod-name"}
   Best for: stuck pods, CrashLoopBackOff that won't recover with restart.

6. PatchConfig — updates a ConfigMap. Params: {"configmap": "name", "key1": "value1"}.
   Best for: configuration errors, feature flag toggles.

7. RestartStatefulSetPod — restarts a StatefulSet pod (preserves identity/storage).
   Params (optional): {"pod": "specific-pod-name"} (omit for rolling restart of entire StatefulSet).
   Best for: StatefulSet pod issues, database pod recovery.

GITOPS ACTIONS:
8. HelmRollback — rolls back a Helm release to the previous revision.
   Params (optional): {"revision": "N"} (defaults to previous).
   Best for: failed Helm upgrades, bad chart values.

9. ArgoSyncApp — triggers an ArgoCD Application sync.
   Params (optional): {"revision": "commit-sha"} (defaults to HEAD).
   Best for: ArgoCD OutOfSync state, forcing re-sync after fix.

AUTOSCALING ACTIONS:
10. AdjustHPA — modifies HPA min/max replicas or target utilization.
    Params: {"minReplicas": "N", "maxReplicas": "N", "targetCPUUtilization": "N"}
    Best for: HPA maxed out, autoscaling misconfiguration.

INFRASTRUCTURE ACTIONS:
11. CordonNode — marks a node as unschedulable.
    Params: {"node": "node-name"}
    Best for: node-level issues, disk pressure, hardware problems.

12. DrainNode — cordons and evicts all pods from a node.
    Params: {"node": "node-name"}
    Best for: urgent node evacuation, node maintenance.

STORAGE ACTIONS:
13. ResizePVC — expands a PersistentVolumeClaim (expansion only, no shrinking).
    Params: {"pvc": "pvc-name", "size": "20Gi"}
    Best for: disk pressure, PVC full, storage quota issues.

SECURITY ACTIONS:
14. RotateSecret — updates secret values or copies from a source secret.
    Params: {"secret": "name", "sourceSecret": "new-secret-name"} or {"secret": "name", "key": "value"}.
    Best for: expired credentials, certificate rotation.

NETWORKING ACTIONS:
15. UpdateIngress — modifies Ingress backend or annotations.
    Params: {"ingress": "name", "backendService": "svc-name", "backendPort": "8080"}.
    Best for: routing fixes, backend service changes.

16. PatchNetworkPolicy — adds allowed ports to a NetworkPolicy.
    Params: {"networkPolicy": "name", "allowPort": "8080", "protocol": "TCP"}.
    Best for: connectivity issues caused by restrictive network policies.

ADVANCED ACTIONS:
17. ApplyManifest — applies a JSON manifest from a ConfigMap.
    Params: {"configmap": "fix-manifest", "key": "manifest.yaml"}.
    Best for: applying pre-prepared fix manifests.

18. ExecDiagnostic — runs a whitelisted diagnostic command in a pod.
    Params: {"command": "df -h"} (only pre-approved commands allowed).
    Best for: gathering diagnostic data before making changes.

Respond ONLY with a JSON object (no markdown, no code blocks):
{
  "analysis": "Detailed root cause analysis and impact assessment",
  "confidence": 0.85,
  "recommendations": ["First recommendation", "Second recommendation"],
  "actions": [
    {"name": "Increase memory", "action": "AdjustResources", "description": "Pod is OOMKilled, increase memory limit", "params": {"memory_limit": "1Gi", "memory_request": "512Mi"}},
    {"name": "Rollback to healthy", "action": "RollbackDeployment", "description": "Current image is crashing, roll back", "params": {"toRevision": "healthy"}}
  ]
}

Rules:
- confidence: float between 0.0 and 1.0
- recommendations: human-readable text advice
- actions: concrete remediation steps using ONLY the available actions listed above
- Each action must have a description explaining WHY it is recommended
- For OOMKilled issues, ALWAYS consider AdjustResources before restart/rollback
- For CrashLoopBackOff after a recent deploy, prefer RollbackDeployment with toRevision
- Prefer targeted fixes (AdjustResources, specific rollback) over broad actions (restart)`)

	return sb.String()
}

type actionEntry struct {
	Name        string            `json:"name"`
	Action      string            `json:"action"`
	Description string            `json:"description"`
	Params      map[string]string `json:"params,omitempty"`
}

type analysisResult struct {
	Analysis        string        `json:"analysis"`
	Confidence      float32       `json:"confidence"`
	Recommendations []string      `json:"recommendations"`
	Actions         []actionEntry `json:"actions"`
}

func parseAnalysisResponse(response string) analysisResult {
	// Strip markdown code blocks if present
	cleaned := response
	cleaned = strings.TrimSpace(cleaned)
	if strings.HasPrefix(cleaned, "```json") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		if idx := strings.LastIndex(cleaned, "```"); idx >= 0 {
			cleaned = cleaned[:idx]
		}
	} else if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```")
		if idx := strings.LastIndex(cleaned, "```"); idx >= 0 {
			cleaned = cleaned[:idx]
		}
	}
	cleaned = strings.TrimSpace(cleaned)

	var result analysisResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		// Fallback: treat entire response as analysis text
		return analysisResult{
			Analysis:        response,
			Confidence:      0.5,
			Recommendations: []string{"Review the issue manually — AI response could not be parsed"},
		}
	}

	// Clamp confidence
	if result.Confidence < 0 {
		result.Confidence = 0
	}
	if result.Confidence > 1 {
		result.Confidence = 1
	}

	return result
}

// --- Agentic Remediation ---

// AgenticStep runs one step of the AI-driven remediation loop.
func (h *Handler) AgenticStep(ctx context.Context, req *pb.AgenticStepRequest) (*pb.AgenticStepResponse, error) {
	if req.IssueName == "" {
		return nil, status.Error(codes.InvalidArgument, "issue_name is required")
	}

	llmClient, err := h.getClient(req.Provider, req.Model, "", nil)
	if err != nil {
		h.logger.Error("Failed to get LLM client for agentic step", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get LLM client: %v", err)
	}

	prompt := buildAgenticStepPrompt(req)
	response, err := llmClient.SendPrompt(ctx, prompt, nil, 0)
	if err != nil {
		h.logger.Error("LLM agentic step failed", zap.Error(err), zap.String("issue", req.IssueName), zap.Int32("step", req.CurrentStep))
		return nil, status.Errorf(codes.Internal, "LLM agentic step failed: %v", err)
	}

	return parseAgenticStepResponse(response), nil
}

func buildAgenticStepPrompt(req *pb.AgenticStepRequest) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf(`You are a Kubernetes SRE agent. You are autonomously remediating an active incident by executing actions one at a time. After each action, you observe the result and decide the next step.

Incident Details:
- Issue: %s
- Namespace: %s
- Resource: %s/%s
- Signal Type: %s
- Severity: %s
- Description: %s
- Risk Score: %d/100`,
		req.IssueName, req.Namespace, req.ResourceKind, req.ResourceName,
		req.SignalType, req.Severity, req.Description, req.RiskScore))

	if req.KubernetesContext != "" {
		sb.WriteString(fmt.Sprintf(`

Current Kubernetes Cluster State (LIVE — refreshed before each step):
%s`, req.KubernetesContext))
	}

	sb.WriteString(`

Available Actions (you can execute ONE per step):

WORKLOAD:
1. RestartDeployment — rolling restart of all pods. No params.
2. ScaleDeployment — scale replicas. Params: {"replicas": "N"} (N >= 1).
3. RollbackDeployment — rollback. Params: {"toRevision": "previous|healthy|<number>"}
4. AdjustResources — change CPU/memory. Params: {"container": "name", "memory_limit": "1Gi", "cpu_limit": "500m"}
5. DeletePod — delete unhealthy pod. Params: {"pod": "name"} (optional).
6. PatchConfig — update ConfigMap. Params: {"configmap": "name", "key1": "value1"}
7. RestartStatefulSetPod — restart StatefulSet pod. Params: {"pod": "name"} (optional for rolling restart).

GITOPS:
8. HelmRollback — rollback Helm release. Params: {"revision": "N"} (optional).
9. ArgoSyncApp — trigger ArgoCD sync. Params: {"revision": "sha"} (optional).

AUTOSCALING:
10. AdjustHPA — modify HPA. Params: {"minReplicas": "N", "maxReplicas": "N", "targetCPUUtilization": "N"}

INFRASTRUCTURE:
11. CordonNode — mark node unschedulable. Params: {"node": "name"}
12. DrainNode — evict pods from node. Params: {"node": "name"}

STORAGE:
13. ResizePVC — expand PVC. Params: {"pvc": "name", "size": "20Gi"}

SECURITY:
14. RotateSecret — update secret. Params: {"secret": "name", "key": "new-value"} or {"secret": "name", "sourceSecret": "new-src"}

NETWORKING:
15. UpdateIngress — fix Ingress. Params: {"ingress": "name", "backendService": "svc", "backendPort": "8080"}
16. PatchNetworkPolicy — open port. Params: {"networkPolicy": "name", "allowPort": "8080"}

ADVANCED:
17. ApplyManifest — apply fix from ConfigMap. Params: {"configmap": "fix-cm", "key": "manifest.yaml"}
18. ExecDiagnostic — run diagnostic. Params: {"command": "df -h"} (whitelisted only).

OBSERVATION (no cluster change):
19. Observe — set next_action to null and resolved to false. Wait and see effect of previous action.`)

	// Append conversation history
	if len(req.History) > 0 {
		sb.WriteString("\n\nRemediation History:")
		for _, h := range req.History {
			sb.WriteString(fmt.Sprintf("\n\nStep %d:", h.StepNumber))
			sb.WriteString(fmt.Sprintf("\n  AI Reasoning: %s", h.AiMessage))
			if h.Action != "" {
				sb.WriteString(fmt.Sprintf("\n  Action: %s", h.Action))
				if len(h.Params) > 0 {
					sb.WriteString(fmt.Sprintf(" %v", h.Params))
				}
			} else {
				sb.WriteString("\n  Action: (observation only)")
			}
			if h.Observation != "" {
				sb.WriteString(fmt.Sprintf("\n  Observation: %s", h.Observation))
			}
		}
	}

	sb.WriteString(fmt.Sprintf(`

You are on step %d of %d maximum steps.`, req.CurrentStep, req.MaxSteps))

	sb.WriteString(`

Respond ONLY with a JSON object (no markdown, no code blocks):

If the problem is NOT yet resolved:
{
  "reasoning": "Your analysis of the current state and why you choose this action",
  "resolved": false,
  "next_action": {
    "name": "Step description",
    "action": "ActionType",
    "description": "Why this action helps",
    "params": {"key": "value"}
  }
}

If you need to observe (wait for effect of previous action):
{
  "reasoning": "Waiting to observe the effect of the previous action",
  "resolved": false,
  "next_action": null
}

If the problem IS resolved (cluster is healthy):
{
  "reasoning": "Final assessment of what happened and how it was fixed",
  "resolved": true,
  "next_action": null,
  "postmortem_summary": "Brief incident summary for the PostMortem report",
  "root_cause": "The determined root cause of the incident",
  "impact": "What services/users were affected and for how long",
  "lessons_learned": ["Lesson 1", "Lesson 2"],
  "prevention_actions": ["Prevention step 1", "Prevention step 2"]
}

Rules:
- Execute ONE action per step. Observe the result before deciding the next.
- If a previous action FAILED, try a DIFFERENT approach — do not repeat it.
- Only set resolved=true after confirming the cluster state shows healthy pods.
- For OOMKilled, prefer AdjustResources before restart/rollback.
- For CrashLoopBackOff after a recent deploy, prefer RollbackDeployment with toRevision.
- Prefer targeted fixes over broad actions.
- If you cannot determine what to do, set next_action to null (will escalate).
- When resolved, provide thorough postmortem data — you have the full context.`)

	return sb.String()
}

type agenticStepResult struct {
	Reasoning         string       `json:"reasoning"`
	Resolved          bool         `json:"resolved"`
	NextAction        *actionEntry `json:"next_action"`
	PostmortemSummary string       `json:"postmortem_summary"`
	RootCause         string       `json:"root_cause"`
	Impact            string       `json:"impact"`
	LessonsLearned    []string     `json:"lessons_learned"`
	PreventionActions []string     `json:"prevention_actions"`
}

func parseAgenticStepResponse(response string) *pb.AgenticStepResponse {
	cleaned := strings.TrimSpace(response)
	if strings.HasPrefix(cleaned, "```json") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		if idx := strings.LastIndex(cleaned, "```"); idx >= 0 {
			cleaned = cleaned[:idx]
		}
	} else if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```")
		if idx := strings.LastIndex(cleaned, "```"); idx >= 0 {
			cleaned = cleaned[:idx]
		}
	}
	cleaned = strings.TrimSpace(cleaned)

	var result agenticStepResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		// Parse failure — return safe default (will trigger escalation)
		return &pb.AgenticStepResponse{
			Reasoning: fmt.Sprintf("Failed to parse AI response: %v. Raw: %s", err, response),
			Resolved:  false,
		}
	}

	resp := &pb.AgenticStepResponse{
		Reasoning:         result.Reasoning,
		Resolved:          result.Resolved,
		PostmortemSummary: result.PostmortemSummary,
		RootCause:         result.RootCause,
		Impact:            result.Impact,
		LessonsLearned:    result.LessonsLearned,
		PreventionActions: result.PreventionActions,
	}

	if result.NextAction != nil {
		resp.NextAction = &pb.SuggestedAction{
			Name:        result.NextAction.Name,
			Action:      result.NextAction.Action,
			Description: result.NextAction.Description,
			Params:      result.NextAction.Params,
		}
	}

	return resp
}
