# ChatCLI Kubernetes Operator — AIOps Platform

Kubernetes operator for managing ChatCLI server instances and an autonomous AIOps pipeline via Custom Resource Definitions (CRDs).

## Overview

The ChatCLI Operator goes beyond simple instance management. It implements a **full AIOps platform** that autonomously detects anomalies, correlates signals, requests AI analysis, and executes remediation — all without external dependencies beyond the LLM provider.

### Architecture

```
                         K8s Cluster
  ┌───────────────────────────────────────────────────────────────────┐
  │                                                                   │
  │   ChatCLI Server (gRPC)            Operator                      │
  │   ┌────────────────┐         ┌────────────────────────┐          │
  │   │ K8s Watcher    │◄──gRPC──│ WatcherBridge          │          │
  │   │ (collectors)   │         │ (polls GetAlerts)       │          │
  │   │                │         └────────┬───────────────┘          │
  │   │ AnalyzeIssue   │                  │ creates                  │
  │   │ (LLM call)     │                  ▼                          │
  │   │                │         ┌────────────────┐                  │
  │   │ AgenticStep    │         │ Anomaly CR     │                  │
  │   │ (agentic loop) │         └───────┬────────┘                  │
  │   └──────┬─────────┘                 │ AnomalyReconciler         │
  │          │                           ▼                           │
  │          │                   ┌────────────────┐                  │
  │          │                   │ Issue CR       │                  │
  │          │                   │ (correlation)  │                  │
  │          │                   └───────┬────────┘                  │
  │          │                           │ IssueReconciler           │
  │          │                           ▼                           │
  │          │                   ┌────────────────┐                  │
  │          ◄─── AnalyzeIssue ──│ AIInsight CR   │                  │
  │          │                   └───────┬────────┘                  │
  │          │                           │                           │
  │          │                           ▼                           │
  │          │                   ┌────────────────────┐              │
  │          ◄─── AgenticStep ───│ RemediationPlan CR │              │
  │          │    (loop)         │ (runbook or agentic)│              │
  │          │                   └────────┬───────────┘              │
  │          │                            │ RemediationReconciler    │
  │          │                            ▼                          │
  │          │            Scale / Restart / Rollback / PatchConfig   │
  │          │            / AdjustResources / DeletePod              │
  │          │                            │                          │
  │          │                            ▼ (on agentic resolution)  │
  │          │                   ┌────────────────┐                  │
  │          │                   │ PostMortem CR  │                  │
  │          │                   │ (auto-generated)│                  │
  │          │                   └────────────────┘                  │
  └──────────┴───────────────────────────────────────────────────────┘
```

## CRDs (API Group: `platform.chatcli.io/v1alpha1`)

| CRD | Short Name | Description |
|-----|-----------|-------------|
| **Instance** | `inst` | ChatCLI server instance (Deployment, Service, RBAC, PVC) |
| **Anomaly** | `anom` | Raw signal from K8s watcher (pod restarts, OOM, deploy failures) |
| **Issue** | `iss` | Correlated incident grouping multiple anomalies |
| **AIInsight** | `ai` | AI-generated root cause analysis and suggested actions |
| **RemediationPlan** | `rp` | Concrete actions to fix the issue (runbook-based or agentic AI-driven) |
| **Runbook** | `rb` | Optional manual operational procedures (AI actions used as fallback) |
| **PostMortem** | `pm` | Auto-generated incident report after agentic resolution |

## Quick Start

```bash
# Install all CRDs
kubectl apply -f config/crd/bases/

# Install RBAC + Manager
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/manager/manager.yaml

# Create API Keys secret
kubectl create secret generic chatcli-api-keys \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-xxx

# Deploy a ChatCLI Instance
kubectl apply -f config/samples/platform_v1alpha1_instance.yaml
```

## AIOps Pipeline Flow

The operator implements a fully autonomous pipeline:

```
1. DETECTION     WatcherBridge polls GetAlerts from ChatCLI Server every 30s
                 Creates Anomaly CRs for each new alert (dedup via SHA256)
                 Dedup entries invalidated when Issue reaches terminal state

2. CORRELATION   AnomalyReconciler correlates anomalies by resource+timewindow
                 Creates/updates Issue CRs with risk scores, severity, and signalType

3. ANALYSIS      IssueReconciler creates AIInsight CR
                 AIInsightReconciler collects K8s context (deployment status, pods,
                   events, revision history) and calls AnalyzeIssue RPC
                 Returns: root cause, confidence, recommendations, suggested actions

4. REMEDIATION   Runbook-first flow:
                   a) Matching manual Runbook (tiered: SignalType+Severity+Kind,
                      then Severity+Kind) — takes precedence
                   b) Auto-generate Runbook from AI actions (reusable for future)
                   c) Agentic AI remediation (no runbook/AI actions available):
                      AI drives remediation step-by-step via observe-decide-act loop

5. EXECUTION     RemediationReconciler executes actions:
                   Standard mode: all actions executed sequentially
                   Agentic mode:  AI decides each action, observes result, decides next
                   Supported actions:
                   - ScaleDeployment (adjust replicas)
                   - RestartDeployment (rollout restart)
                   - RollbackDeployment (undo rollout — previous/healthy/specific rev)
                   - PatchConfig (update ConfigMap)
                   - AdjustResources (change CPU/memory requests/limits)
                   - DeletePod (remove most-unhealthy pod)

6. RESOLUTION    On success → Issue resolved, dedup entries invalidated
                 On failure → Re-analysis with failure context (different strategy)
                   → up to maxAttempts → Escalate

7. POSTMORTEM    On agentic resolution → PostMortem CR auto-generated:
                   Timeline, root cause, impact, actions executed,
                   lessons learned, prevention actions
                 Reusable Runbook also generated from successful agentic steps
```

### Issue State Machine

```
Detected → Analyzing → Remediating → Resolved
                │  ↑         │             │
                │  └─────────┘ Retry       └──→ PostMortem (agentic)
                │            │
                └──→ Escalated ←──┘ (max attempts exhausted)

Analyzing → Remediating via:
  - Manual Runbook match
  - AI-generated Runbook
  - Agentic AI mode (no runbook/AI actions → AI drives step-by-step)
```

## CRD Examples

### Instance (Server Management)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-prod
spec:
  provider: CLAUDEAI
  replicas: 1
  apiKeys:
    name: chatcli-api-keys
  server:
    port: 50051
    tls:
      enabled: true
      secretName: chatcli-tls       # Secret with tls.crt, tls.key, ca.crt (optional)
    token:
      name: chatcli-server-token    # Secret containing the auth token
      key: token                    # Key within the Secret (default: "token")
  watcher:
    enabled: true
    interval: "30s"
    targets:
      - deployment: api-gateway
        namespace: production
        metricsPort: 9090
      - deployment: worker
        namespace: batch
```

### Anomaly (Auto-created by WatcherBridge)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Anomaly
metadata:
  name: watcher-highrestartcount-api-gateway-1234567890
  namespace: production
spec:
  signalType: pod_restart      # pod_restart, oom_kill, pod_not_ready, deploy_failing
  source: watcher
  severity: warning
  resource:
    kind: Deployment
    name: api-gateway
    namespace: production
  description: "HighRestartCount on api-gateway: container app restarted 8 times"
  detectedAt: "2026-02-16T10:30:00Z"
```

### Issue (Auto-created by AnomalyReconciler)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Issue
metadata:
  name: api-gateway-pod-restart-1771276354
  namespace: production
spec:
  severity: high
  source: watcher
  signalType: pod_restart       # Propagated from Anomaly for tiered Runbook matching
  description: "Correlated incident: pod_restart on api-gateway"
  resource:
    kind: Deployment
    name: api-gateway
    namespace: production
  riskScore: 65
status:
  state: Analyzing
  remediationAttempts: 0
  maxRemediationAttempts: 3
```

### AIInsight (Auto-created by IssueReconciler)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: AIInsight
metadata:
  name: api-gateway-pod-restart-1771276354-insight
  namespace: production
spec:
  issueRef:
    name: api-gateway-pod-restart-1771276354
  provider: CLAUDEAI
  model: claude-sonnet-4-5
status:
  analysis: "High restart count caused by OOMKilled. Container memory limit (512Mi) insufficient for current workload."
  confidence: 0.87
  recommendations:
    - "Increase memory limit to 1Gi"
    - "Investigate memory leak in application"
  suggestedActions:
    - name: "Restart deployment"
      action: RestartDeployment
      description: "Restart pods to reclaim leaked memory"
    - name: "Scale up replicas"
      action: ScaleDeployment
      description: "Add replicas to distribute memory pressure"
      params:
        replicas: "4"
  generatedAt: "2026-02-16T10:31:00Z"
```

### RemediationPlan (Auto-created from Runbook)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: RemediationPlan
metadata:
  name: api-gateway-pod-restart-1771276354-plan-1
  namespace: production
spec:
  issueRef:
    name: api-gateway-pod-restart-1771276354
  attempt: 1
  strategy: "Attempt 1 via runbook 'auto-pod-restart-high-deployment'. AI analysis: High restart count caused by OOMKilled..."
  actions:
    - type: RestartDeployment
    - type: ScaleDeployment
      params:
        replicas: "4"
  safetyConstraints:
    - "No delete operations"
    - "No destructive changes"
    - "Rollback on failure"
status:
  state: Completed
  result: "Deployment restarted and scaled to 4 replicas successfully"
```

### Runbook (Optional — Manual)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Runbook
metadata:
  name: high-error-rate-runbook
  namespace: production
spec:
  description: "Standard procedure for high error rate on Deployments"
  trigger:
    signalType: error_rate
    severity: high
    resourceKind: Deployment
  steps:
    - name: Scale up deployment
      action: ScaleDeployment
      description: "Increase replicas to handle error spike"
      params:
        replicas: "4"
    - name: Rollback if needed
      action: RollbackDeployment
      description: "Rollback to previous version if errors persist"
  maxAttempts: 3
```

### RemediationPlan (Agentic Mode)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: RemediationPlan
metadata:
  name: api-gateway-pod-restart-plan-1
  namespace: production
spec:
  issueRef:
    name: api-gateway-pod-restart-1771276354
  attempt: 1
  strategy: "Agentic AI remediation"
  agenticMode: true
  agenticMaxSteps: 10
  agenticHistory:
    - stepNumber: 1
      aiMessage: "High restart count with OOMKilled. Scaling up to reduce memory pressure."
      action:
        type: ScaleDeployment
        params:
          replicas: "5"
      observation: "SUCCESS: ScaleDeployment executed successfully"
      timestamp: "2026-02-16T10:31:00Z"
    - stepNumber: 2
      aiMessage: "Pods still restarting. Adjusting memory limits to 1Gi."
      action:
        type: AdjustResources
        params:
          memory_limit: "1Gi"
          memory_request: "512Mi"
      observation: "SUCCESS: AdjustResources executed successfully"
      timestamp: "2026-02-16T10:31:35Z"
    - stepNumber: 3
      aiMessage: "All pods running stable. Issue resolved."
      timestamp: "2026-02-16T10:32:10Z"
status:
  state: Completed
  agenticStepCount: 3
```

### PostMortem (Auto-generated)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: PostMortem
metadata:
  name: pm-api-gateway-pod-restart-1771276354
  namespace: production
spec:
  issueRef:
    name: api-gateway-pod-restart-1771276354
  resource:
    kind: Deployment
    name: api-gateway
    namespace: production
  severity: high
status:
  state: Open
  summary: "OOMKilled containers caused cascading restarts on api-gateway"
  rootCause: "Memory limit (512Mi) insufficient for current workload pattern"
  impact: "Service degradation for 5 minutes, 30% error rate increase"
  timeline:
    - timestamp: "2026-02-16T10:30:00Z"
      type: detected
      detail: "Issue detected: pod_restart on api-gateway"
    - timestamp: "2026-02-16T10:31:00Z"
      type: action_executed
      detail: "ScaleDeployment to 5 replicas"
    - timestamp: "2026-02-16T10:31:35Z"
      type: action_executed
      detail: "AdjustResources memory_limit=1Gi"
    - timestamp: "2026-02-16T10:32:10Z"
      type: resolved
      detail: "All pods stable, issue resolved"
  actionsExecuted:
    - action: ScaleDeployment
      params:
        replicas: "5"
      result: success
      timestamp: "2026-02-16T10:31:00Z"
    - action: AdjustResources
      params:
        memory_limit: "1Gi"
        memory_request: "512Mi"
      result: success
      timestamp: "2026-02-16T10:31:35Z"
  lessonsLearned:
    - "Memory limits should account for peak workload patterns"
    - "Set up HPA to auto-scale on memory pressure"
  preventionActions:
    - "Configure HPA with min 3 replicas for api-gateway"
    - "Set memory limit to 1Gi across all environments"
  duration: "2m10s"
  generatedAt: "2026-02-16T10:32:10Z"
```

## Resources Created per Instance

| Resource | Name | Description |
|----------|------|-------------|
| Deployment | `<name>` | ChatCLI server pods |
| Service | `<name>` | gRPC Service (auto-headless when replicas > 1 for client-side LB) |
| ConfigMap | `<name>` | Environment variables |
| ConfigMap | `<name>-watch-config` | Multi-target watch YAML |
| ServiceAccount | `<name>` | Pod identity |
| Role/ClusterRole | `<name>-watcher` | K8s watcher permissions |
| PVC | `<name>-sessions` | Session persistence (optional) |

### gRPC Load Balancing

gRPC uses persistent HTTP/2 connections that pin to a single pod via kube-proxy, leaving extra replicas idle.

- **1 replica** (default): Standard ClusterIP Service
- **Multiple replicas**: Headless Service (`ClusterIP: None`) is created automatically, enabling client-side round-robin via gRPC `dns:///` resolver
- **Keepalive**: WatcherBridge pings every 30s (5s timeout) to detect dead pods quickly. Server accepts pings with minimum interval of 20s (`EnforcementPolicy.MinTime`)
- **Transition**: When scaling from 1 to 2+ replicas (or back), the operator deletes and recreates the Service automatically (ClusterIP is immutable in Kubernetes)

## Correlation Engine

The operator correlates anomalies into issues using:

- **Resource grouping**: Anomalies on the same deployment within a time window are grouped
- **Risk scoring**: Each signal type has a weight (OOM = 30, error_rate = 25, deploy_failing = 25, pod_restart = 20, pod_not_ready = 20)
- **Severity classification**: Based on risk score (Critical >= 80, High >= 60, Medium >= 40, Low < 40)
- **Incident ID**: Deterministic hash from resource + signal type for dedup

## Remediation Strategy

The operator uses a tiered remediation approach:

1. **Manual Runbook match**: Tiered matching — first by `SignalType + Severity + ResourceKind`, then by `Severity + ResourceKind`
2. **Auto-generated Runbook**: If no manual Runbook exists, AI suggested actions are materialized as a reusable Runbook CR (labeled `platform.chatcli.io/auto-generated=true`)
3. **Agentic AI Remediation**: If neither Runbook nor AI actions are available, the operator creates an **agentic plan** — the AI drives remediation step-by-step in an observe-decide-act loop (each reconcile = 1 step, max 10 steps, 10min timeout)
4. **Escalation**: Only when agentic mode also fails after max attempts

**On agentic resolution**: The operator auto-generates a **PostMortem CR** (timeline, root cause, impact, lessons learned) and a **reusable Runbook** from the successful agentic steps.

**K8s Context Enrichment**: The AI receives full cluster context via `KubernetesContextBuilder`:
- Deployment status (replicas, conditions, images)
- Pod details (up to 5 pods: phase, restarts, container states)
- Recent events (last 15 Warning/Normal events)
- Revision history (last 5 ReplicaSet revisions with image diffs)

**Retry with Strategy Escalation**: When remediation fails, the operator triggers AI re-analysis with failure evidence from previous attempts. The AI is instructed to suggest a fundamentally different strategy.

**Supported actions**: `ScaleDeployment`, `RestartDeployment`, `RollbackDeployment` (previous/healthy/specific revision), `PatchConfig`, `AdjustResources` (CPU/memory), `DeletePod` (most-unhealthy), `Custom` (blocked by safety checks)

Priority: **Manual Runbook > Auto-generated Runbook > Agentic AI > Escalation**

## Development

```bash
cd operator

# Build
go build ./...

# Test (96 test functions, 125 with subtests)
go test ./... -v

# Docker (must be built from repo root due to go.mod replace directive)
docker build -f operator/Dockerfile -t ghcr.io/diillson/chatcli-operator:latest .

# Install CRDs
kubectl apply -f config/crd/bases/

# Deploy
make deploy IMG=ghcr.io/diillson/chatcli-operator:latest
```

### Test Coverage

| Controller | Tests | What's Covered |
|-----------|-------|----------------|
| InstanceReconciler | 15 | CRUD, watcher, persistence, replicas, RBAC, deletion |
| AnomalyReconciler | 4 | Creation, correlation, existing issue attachment |
| IssueReconciler | 12 | State machine, AI fallback, retry, agentic plan creation, PostMortem generation |
| RemediationReconciler | 16 | All action types, safety checks, agentic loop (first step, resolved, max steps, timeout, action failed, observation) |
| AIInsightReconciler | 12 | Server connectivity, RPC mock, analysis parsing, withAuth, ConnectionOpts |
| PostMortemReconciler | 2 | State initialization, terminal state |
| WatcherBridge | 22 | Alert mapping, dedup, hash, pruning, anomaly creation, TLS/token buildConnectionOpts |
| CorrelationEngine | 4 | Risk scoring, severity, incident ID, related anomalies |
| Pipeline (E2E) | 3 | Full flow: Anomaly→Issue→Insight→Plan→Resolved, escalation, correlation |
| MapActionType | 6 | All action type string→enum mappings |

## Documentation

Full documentation at [diillson.github.io/chatcli/docs/features/k8s-operator](https://diillson.github.io/chatcli/docs/features/k8s-operator/)

Deep-dive AIOps architecture at [diillson.github.io/chatcli/docs/features/aiops-platform](https://diillson.github.io/chatcli/docs/features/aiops-platform/)
