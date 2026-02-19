# ChatCLI Kubernetes Operator — AIOps Platform

Kubernetes operator for managing ChatCLI server instances and an autonomous AIOps pipeline via Custom Resource Definitions (CRDs).

## Overview

The ChatCLI Operator goes beyond simple instance management. It implements a **full AIOps platform** that autonomously detects anomalies, correlates signals, requests AI analysis, and executes remediation — all without external dependencies beyond the LLM provider.

### Architecture

```
                         K8s Cluster
  ┌──────────────────────────────────────────────────────────────┐
  │                                                              │
  │   ChatCLI Server (gRPC)          Operator                   │
  │   ┌──────────────┐         ┌────────────────────────┐       │
  │   │ K8s Watcher  │◄──gRPC──│ WatcherBridge          │       │
  │   │ (collectors) │         │ (polls GetAlerts)       │       │
  │   │              │         └────────┬───────────────┘       │
  │   │ AnalyzeIssue │                  │ creates               │
  │   │ (LLM call)   │                  ▼                       │
  │   └──────┬───────┘         ┌────────────────┐               │
  │          │                 │ Anomaly CR     │               │
  │          │                 └───────┬────────┘               │
  │          │                         │ AnomalyReconciler      │
  │          │                         ▼                        │
  │          │                 ┌────────────────┐               │
  │          │                 │ Issue CR       │               │
  │          │                 │ (correlation)  │               │
  │          │                 └───────┬────────┘               │
  │          │                         │ IssueReconciler        │
  │          │                         ▼                        │
  │          │                 ┌────────────────┐               │
  │          ◄────── gRPC ─────│ AIInsight CR   │               │
  │          │   AnalyzeIssue  │ (AI analysis)  │               │
  │          │                 └───────┬────────┘               │
  │          │                         │                        │
  │          │                         ▼                        │
  │          │                 ┌────────────────────┐           │
  │          │                 │ RemediationPlan CR │           │
  │          │                 │ (auto-generated)   │           │
  │          │                 └────────────────────┘           │
  │          │                         │ RemediationReconciler  │
  │          │                         ▼                        │
  │          │                  Scale / Restart / Rollback      │
  │          │                  / PatchConfig                   │
  └──────────┴──────────────────────────────────────────────────┘
```

## CRDs (API Group: `platform.chatcli.io/v1alpha1`)

| CRD | Short Name | Description |
|-----|-----------|-------------|
| **Instance** | `inst` | ChatCLI server instance (Deployment, Service, RBAC, PVC) |
| **Anomaly** | `anom` | Raw signal from K8s watcher (pod restarts, OOM, deploy failures) |
| **Issue** | `iss` | Correlated incident grouping multiple anomalies |
| **AIInsight** | `ai` | AI-generated root cause analysis and suggested actions |
| **RemediationPlan** | `rp` | Concrete actions to fix the issue (scale, restart, rollback) |
| **Runbook** | `rb` | Optional manual operational procedures (AI actions used as fallback) |

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
                   b) Auto-generate Runbook from AI actions (reusable for future) — OR
                   c) Escalates if neither available

5. EXECUTION     RemediationReconciler executes actions:
                   - ScaleDeployment (adjust replicas)
                   - RestartDeployment (rollout restart)
                   - RollbackDeployment (undo rollout)
                   - PatchConfig (update ConfigMap)

6. RESOLUTION    On success → Issue resolved, dedup entries invalidated
                 On failure → Re-analysis with failure context (different strategy)
                   → up to maxAttempts → Escalate
```

### Issue State Machine

```
Detected → Analyzing → Remediating → Resolved
                │  ↑         │
                │  └─────────┘ Retry (re-analysis with failure context)
                │            │
                └──→ Escalated ←──┘ (max attempts or no actions)
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

## Runbook-First Remediation

The operator uses a Runbook-first approach where all remediation goes through Runbook CRs:

1. **Manual Runbook match**: Tiered matching — first by `SignalType + Severity + ResourceKind`, then by `Severity + ResourceKind`
2. **Auto-generated Runbook**: If no manual Runbook exists, AI suggested actions are materialized as a reusable Runbook CR (labeled `platform.chatcli.io/auto-generated=true`)
3. **RemediationPlan**: Always created from a Runbook (manual or auto-generated)
4. **Reuse**: Auto-generated Runbooks are reused for future issues matching the same trigger

**K8s Context Enrichment**: The AI receives full cluster context via `KubernetesContextBuilder`:
- Deployment status (replicas, conditions, images)
- Pod details (up to 5 pods: phase, restarts, container states)
- Recent events (last 15 Warning/Normal events)
- Revision history (last 5 ReplicaSet revisions with image diffs)

**Retry with Strategy Escalation**: When remediation fails, the operator triggers AI re-analysis with failure evidence from previous attempts. The AI is instructed to suggest a fundamentally different strategy.

**Supported actions**: `ScaleDeployment`, `RestartDeployment`, `RollbackDeployment`, `PatchConfig`, `Custom` (blocked by safety checks)

Priority: **Manual Runbook > Auto-generated Runbook > Escalation**

## Development

```bash
cd operator

# Build
go build ./...

# Test (86 test functions, 115 with subtests)
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
| IssueReconciler | 10 | State machine (Detected→Analyzing→Remediating→Resolved/Escalated), AI fallback, retry |
| RemediationReconciler | 10 | All action types, safety checks, custom action blocking |
| AIInsightReconciler | 12 | Server connectivity, RPC mock, analysis parsing, withAuth, ConnectionOpts |
| WatcherBridge | 22 | Alert mapping, dedup, hash, pruning, anomaly creation, TLS/token buildConnectionOpts |
| CorrelationEngine | 4 | Risk scoring, severity, incident ID, related anomalies |
| Pipeline (E2E) | 3 | Full flow: Anomaly→Issue→Insight→Plan→Resolved, escalation, correlation |
| MapActionType | 6 | All action type string→enum mappings |

## Documentation

Full documentation at [diillson.github.io/chatcli/docs/features/k8s-operator](https://diillson.github.io/chatcli/docs/features/k8s-operator/)

Deep-dive AIOps architecture at [diillson.github.io/chatcli/docs/features/aiops-platform](https://diillson.github.io/chatcli/docs/features/aiops-platform/)
