# ChatCLI AIOps Operator Helm Chart

Kubernetes operator for autonomous incident detection, AI-powered analysis, and automated remediation. The operator watches cluster resources, correlates anomalies into issues, generates AI insights, and executes approved remediation plans.

## Prerequisites

- Kubernetes 1.26+
- Helm 3.10+

## Installation

### From OCI Registry

```bash
helm install chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system --create-namespace
```

### From Source

```bash
git clone https://github.com/diillson/chatcli.git
helm install chatcli-operator deploy/helm/chatcli-operator \
  --namespace aiops-system --create-namespace
```

## Architecture

```
Anomaly → Issue → AIInsight → RemediationPlan → ApprovalRequest → Execution
   ↑                                                                    │
   └── Watchers (Prometheus, logs, events)                   PostMortem ←┘
```

The operator manages the full incident lifecycle:

1. **Detection** - Watchers emit `Anomaly` resources from metrics, logs, and events
2. **Correlation** - Anomalies are correlated into `Issue` resources
3. **Analysis** - AI generates `AIInsight` with root cause analysis and recommendations
4. **Planning** - `RemediationPlan` is created with specific actions
5. **Approval** - `ApprovalPolicy` rules determine if manual/auto/quorum approval is needed
6. **Execution** - Approved plans are executed (restart, scale, rollback, etc.)
7. **Review** - `PostMortem` captures the full incident lifecycle

## Configuration

### Operator Core

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of operator replicas | `1` |
| `leaderElect` | Enable leader election for HA | `true` |
| `image.repository` | Operator image | `ghcr.io/diillson/chatcli-operator` |
| `image.tag` | Image tag (defaults to appVersion) | `""` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |

### API & Ports

| Parameter | Description | Default |
|-----------|-------------|---------|
| `api.port` | REST API and web dashboard port | `8090` |
| `metrics.port` | Prometheus metrics port | `8080` |
| `health.port` | Health probes port | `8081` |

### Observability

| Parameter | Description | Default |
|-----------|-------------|---------|
| `prometheusUrl` | Prometheus URL for metrics enrichment during analysis | `""` |
| `serviceMonitor.enabled` | Enable Prometheus ServiceMonitor | `false` |
| `serviceMonitor.interval` | Scrape interval | `"30s"` |
| `serviceMonitor.scrapeTimeout` | Scrape timeout | `""` |
| `serviceMonitor.labels` | Additional labels for ServiceMonitor | `{}` |

### RBAC & Security

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rbac.create` | Create ClusterRole and ClusterRoleBinding | `true` |
| `serviceAccount.create` | Create ServiceAccount | `true` |
| `serviceAccount.name` | ServiceAccount name | `""` |
| `serviceAccount.annotations` | ServiceAccount annotations | `{}` |
| `podSecurityContext.runAsNonRoot` | Run as non-root | `true` |
| `securityContext.allowPrivilegeEscalation` | Allow privilege escalation | `false` |
| `securityContext.readOnlyRootFilesystem` | Read-only root filesystem | `true` |

### Resources

| Parameter | Description | Default |
|-----------|-------------|---------|
| `resources.requests.cpu` | CPU request | `100m` |
| `resources.requests.memory` | Memory request | `128Mi` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `256Mi` |

### Networking & Scheduling

| Parameter | Description | Default |
|-----------|-------------|---------|
| `service.type` | Service type | `ClusterIP` |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |

## CRDs

This chart installs 17 Custom Resource Definitions for the AIOps platform:

| CRD | Description |
|-----|-------------|
| `AIInsight` | AI-generated analysis and recommendations |
| `Anomaly` | Raw signal from watchers before correlation |
| `ApprovalPolicy` | Approval requirements for remediation actions |
| `ApprovalRequest` | Pending approval for a remediation action |
| `AuditEvent` | Immutable audit record of platform actions |
| `ChaosExperiment` | Chaos engineering experiment definition |
| `ClusterRegistration` | Multi-cluster federation registration |
| `EscalationPolicy` | Escalation chains for incident management |
| `IncidentSLA` | SLA targets for incident response/resolution |
| `Instance` | ChatCLI instance configuration |
| `Issue` | Detected operational problem |
| `NotificationPolicy` | Notification delivery rules and channels |
| `PostMortem` | Post-incident lifecycle report |
| `RemediationPlan` | Automated remediation plan for an issue |
| `Runbook` | Operational procedures linked to issue types |
| `ServiceLevelObjective` | SLO with burn rate alerting |
| `SourceRepository` | Links workloads to source code for code-aware analysis |

## Example: Approval Policy

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: ApprovalPolicy
metadata:
  name: production-approval
spec:
  enabled: true
  defaultMode: manual
  rules:
    - name: critical-manual
      match:
        severities: ["critical", "high"]
        actionTypes: ["restart", "scale", "rollback"]
        namespaces: ["production"]
      mode: manual
      timeoutMinutes: 15
      changeWindow:
        timezone: "America/Sao_Paulo"
        allowedDays: ["Monday","Tuesday","Wednesday","Thursday","Friday"]
        startHour: 8
        endHour: 18
    - name: low-severity-auto
      match:
        severities: ["low", "warning"]
      mode: auto
      timeoutMinutes: 30
      autoApproveConditions:
        minConfidence: 0.85
        maxSeverity: warning
        historicalSuccessRate: 0.9
```

## Example: Service Level Objective

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: ServiceLevelObjective
metadata:
  name: api-availability
spec:
  service: api-server
  indicator:
    type: availability
    metricQuery: >-
      sum(rate(http_requests_total{code!~"5.."}[5m]))
      / sum(rate(http_requests_total[5m]))
  target: "99.9"
  window: 30d
```

## Uninstalling

```bash
helm uninstall chatcli-operator -n aiops-system
```

> **Note:** CRDs are not removed automatically. To remove them:
> ```bash
> kubectl delete crd -l app.kubernetes.io/managed-by=Helm
> ```
