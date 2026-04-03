# ChatCLI Server Helm Chart

Deploy ChatCLI as a gRPC server on Kubernetes with multi-provider LLM support, automatic failover, MCP integration, and AIOps capabilities.

## Prerequisites

- Kubernetes 1.30+
- Helm 3.10+
- At least one LLM provider API key

## Installation

### From OCI Registry

```bash
helm install chatcli oci://ghcr.io/diillson/charts/chatcli \
  --namespace chatcli --create-namespace \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=sk-xxx
```

### From Source

```bash
git clone https://github.com/diillson/chatcli.git
helm install chatcli deploy/helm/chatcli \
  --namespace chatcli --create-namespace \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=sk-xxx
```

## Configuration

### LLM Providers

| Parameter | Description | Default |
|-----------|-------------|---------|
| `llm.provider` | Default LLM provider (`OPENAI`, `CLAUDEAI`, `GOOGLEAI`, `XAI`, `STACKSPOT`, `OLLAMA`, `COPILOT`) | `""` |
| `llm.model` | Model to use | `""` |
| `secrets.openaiApiKey` | OpenAI API key | `""` |
| `secrets.anthropicApiKey` | Anthropic API key | `""` |
| `secrets.googleaiApiKey` | Google AI API key | `""` |
| `secrets.xaiApiKey` | xAI API key | `""` |
| `secrets.existingSecret` | Use an existing Secret instead of creating one | `""` |

### Provider Fallback

Automatic failover between LLM providers when the primary fails (rate limit, timeout, server error).

| Parameter | Description | Default |
|-----------|-------------|---------|
| `fallback.enabled` | Enable automatic provider failover | `false` |
| `fallback.providers` | Ordered list of providers with model overrides | `[]` |
| `fallback.maxRetries` | Max retries per provider | `2` |
| `fallback.cooldownBase` | Base cooldown after failure | `"30s"` |
| `fallback.cooldownMax` | Maximum cooldown duration | `"5m"` |

Example:

```yaml
fallback:
  enabled: true
  providers:
    - name: OPENAI
      model: gpt-4o
    - name: CLAUDEAI
      model: claude-sonnet-4-20250514
    - name: GOOGLEAI
      model: gemini-2.0-flash
```

### gRPC Server

| Parameter | Description | Default |
|-----------|-------------|---------|
| `server.port` | gRPC server port | `50051` |
| `server.metricsPort` | Prometheus metrics port (0 = disabled) | `9090` |
| `server.token` | Authentication token (empty = no auth) | `""` |
| `server.grpcReflection` | Enable gRPC reflection | `false` |

### TLS

| Parameter | Description | Default |
|-----------|-------------|---------|
| `tls.enabled` | Enable TLS | `false` |
| `tls.certFile` | Certificate file path in container | `""` |
| `tls.keyFile` | Key file path in container | `""` |
| `tls.existingSecret` | Use existing TLS Secret | `""` |

### MCP (Model Context Protocol)

| Parameter | Description | Default |
|-----------|-------------|---------|
| `mcp.enabled` | Enable MCP server integration | `false` |
| `mcp.servers` | Inline MCP server definitions | `[]` |
| `mcp.existingConfigMap` | Existing ConfigMap with mcp_servers.json | `""` |

Example:

```yaml
mcp:
  enabled: true
  servers:
    - name: filesystem
      transport: stdio
      command: npx
      args: ["-y", "@anthropic/mcp-server-filesystem", "/workspace"]
      enabled: true
    - name: web-search
      transport: sse
      url: "http://mcp-search:8080/sse"
      enabled: true
```

### Kubernetes Watcher

| Parameter | Description | Default |
|-----------|-------------|---------|
| `watcher.enabled` | Enable K8s resource watching | `false` |
| `watcher.targets` | Multi-target watch list | `[]` |
| `watcher.interval` | Watch interval | `"30s"` |
| `watcher.window` | Analysis time window | `"2h"` |
| `watcher.maxLogLines` | Max log lines to collect | `100` |
| `watcher.maxContextChars` | Max chars for LLM context | `32000` |

Example:

```yaml
watcher:
  enabled: true
  targets:
    - deployment: api-gateway
      namespace: production
      metricsPort: 9090
      metricsPath: "/metrics"
      metricsFilter: ["http_requests_*", "http_request_duration_*"]
    - deployment: auth-service
      namespace: production
      metricsPort: 9090
```

### Agents, Skills & Bootstrap

| Parameter | Description | Default |
|-----------|-------------|---------|
| `agents.enabled` | Enable agent provisioning | `false` |
| `agents.definitions` | Inline agent markdown definitions | `{}` |
| `agents.existingConfigMap` | Existing ConfigMap with agent files | `""` |
| `skills.enabled` | Enable skill provisioning | `false` |
| `skills.definitions` | Inline skill markdown definitions | `{}` |
| `skills.existingConfigMap` | Existing ConfigMap with skill files | `""` |
| `bootstrap.enabled` | Enable bootstrap files (SOUL.md, USER.md, etc.) | `false` |
| `bootstrap.definitions` | Inline bootstrap file definitions | `{}` |
| `bootstrap.existingConfigMap` | Existing ConfigMap with bootstrap files | `""` |

### Storage & Persistence

| Parameter | Description | Default |
|-----------|-------------|---------|
| `persistence.enabled` | Enable persistent storage for sessions | `true` |
| `persistence.storageClass` | StorageClass name | `""` |
| `persistence.accessModes` | PVC access modes | `["ReadWriteOnce"]` |
| `persistence.size` | PVC size | `1Gi` |
| `memory.enabled` | Enable long-term memory persistence | `false` |

### Networking & Security

| Parameter | Description | Default |
|-----------|-------------|---------|
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `50051` |
| `service.headless` | Headless Service for gRPC load balancing | `false` |
| `ingress.enabled` | Enable Ingress | `false` |
| `networkPolicy.enabled` | Enable NetworkPolicy | `false` |
| `rbac.create` | Create RBAC resources | `true` |
| `rbac.clusterWide` | Use ClusterRole instead of Role | `false` |

### Autoscaling & Availability

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of replicas | `1` |
| `autoscaling.enabled` | Enable HPA | `false` |
| `autoscaling.minReplicas` | Min replicas | `1` |
| `autoscaling.maxReplicas` | Max replicas | `5` |
| `autoscaling.targetCPUUtilizationPercentage` | Target CPU utilization | `80` |
| `podDisruptionBudget.enabled` | Enable PDB | `false` |
| `podDisruptionBudget.minAvailable` | Min available pods | `1` |

### Monitoring

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceMonitor.enabled` | Enable Prometheus ServiceMonitor | `false` |
| `serviceMonitor.interval` | Scrape interval | `"30s"` |
| `prometheusUrl` | Prometheus URL for AIOps metrics enrichment | `""` |

### Plugins

| Parameter | Description | Default |
|-----------|-------------|---------|
| `plugins.enabled` | Enable plugin loader | `false` |
| `plugins.initImage` | Init container image with plugin binaries | `""` |
| `plugins.existingPVC` | Existing PVC with plugin binaries | `""` |

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

## Uninstalling

```bash
helm uninstall chatcli -n chatcli
```

> **Note:** CRDs are not removed automatically. To remove them:
> ```bash
> kubectl delete crd -l app.kubernetes.io/managed-by=Helm
> ```
