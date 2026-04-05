# ChatCLI Server Helm Chart

Deploy **ChatCLI** as a gRPC server on Kubernetes — a multi-provider LLM gateway with intelligent agent modes, automatic failover, MCP integration, Kubernetes-native observability, and AIOps capabilities.

## Features

- **Multi-Provider LLM**: OpenAI, Anthropic Claude, Google Gemini, xAI Grok, ZAI (Zhipu AI), MiniMax, GitHub Copilot, StackSpot AI, Ollama (local)
- **Automatic Failover**: Provider fallback chain with intelligent error classification (rate limit, timeout, auth error, context overflow), exponential cooldown, and health monitoring
- **Agent Mode**: ReAct loop (Reason + Act) with 12 built-in specialized agents running in parallel — File, Coder, Shell, Git, Search, Planner, Reviewer, Tester, Refactor, Diagnostics, Formatter, Deps
- **Coder Mode**: Specialized software engineering agent with strict tool contracts, auto-correction, git integration, and rollback support
- **MCP Integration**: Model Context Protocol support for extending LLM capabilities with external tools (stdio and SSE transports)
- **Kubernetes Watcher**: Real-time multi-target deployment monitoring with metrics, logs, events, HPA, node health, and Prometheus scraping
- **Native Tool Use**: Type-safe tool calling via OpenAI/Anthropic native APIs with XML fallback for other providers
- **Persistent Memory**: Structured long-term memory with facts, patterns, topics, and intelligent decay
- **Plugin System**: Extensible via external plugins with auto-detection, schema validation, and remote plugin support
- **Skill Registry**: Multi-registry skill marketplace (official + community) with fuzzy search and moderation
- **Bootstrap Files**: Customizable system prompt via SOUL.md, USER.md, IDENTITY.md, RULES.md, AGENTS.md
- **Session Management**: Save, load, fork, and export conversation sessions
- **gRPC Server**: High-performance server with optional TLS, token authentication, and Prometheus metrics
- **Security Hardened**: Non-root, read-only filesystem, dropped capabilities, seccomp profile, shell injection prevention

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

### Using an Existing Secret

```bash
kubectl create secret generic chatcli-llm-keys \
  --namespace chatcli \
  --from-literal=OPENAI_API_KEY=sk-xxx \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-xxx

helm install chatcli oci://ghcr.io/diillson/charts/chatcli \
  --namespace chatcli \
  --set llm.provider=OPENAI \
  --set secrets.existingSecret=chatcli-llm-keys
```

## Connecting Clients

Once deployed, connect from any machine using the ChatCLI client:

```bash
# Direct connection
chatcli connect --server chatcli.example.com:50051 --token <server-token>

# With TLS
chatcli connect --server chatcli.example.com:50051 --token <server-token> --tls

# One-shot mode (CI/CD pipelines)
chatcli connect --server chatcli.example.com:50051 --token <server-token> \
  -p "Analyze the last 5 commits for security issues"
```

Clients can use their own API keys (personal mode) or the server's configured provider.

## Configuration

### LLM Providers

| Parameter | Description | Default |
|-----------|-------------|---------|
| `llm.provider` | Default provider: `OPENAI`, `CLAUDEAI`, `GOOGLEAI`, `XAI`, `ZAI`, `MINIMAX`, `STACKSPOT`, `OLLAMA`, `COPILOT` | `""` |
| `llm.model` | Model override | `""` |
| `secrets.openaiApiKey` | OpenAI API key | `""` |
| `secrets.anthropicApiKey` | Anthropic API key | `""` |
| `secrets.googleaiApiKey` | Google AI API key | `""` |
| `secrets.xaiApiKey` | xAI API key | `""` |
| `secrets.zaiApiKey` | ZAI (Zhipu AI) API key | `""` |
| `secrets.minimaxApiKey` | MiniMax API key | `""` |
| `secrets.githubCopilotToken` | GitHub Copilot token | `""` |
| `secrets.stackspotClientId` | StackSpot client ID | `""` |
| `secrets.stackspotClientKey` | StackSpot client key | `""` |
| `secrets.stackspotRealm` | StackSpot realm | `""` |
| `secrets.stackspotAgentId` | StackSpot agent ID | `""` |
| `secrets.existingSecret` | Use an existing Secret instead of creating one | `""` |

### Provider Fallback Chain

Automatic failover between LLM providers when the primary fails. Errors are classified (rate limit, timeout, auth error, context overflow, model not found) and the system automatically tries the next provider with exponential cooldown.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `fallback.enabled` | Enable automatic provider failover | `false` |
| `fallback.providers` | Ordered list of providers (first = highest priority) | `[]` |
| `fallback.maxRetries` | Max retries per provider | `2` |
| `fallback.cooldownBase` | Base cooldown after failure | `"30s"` |
| `fallback.cooldownMax` | Maximum cooldown duration | `"5m"` |

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
    - name: ZAI
      model: glm-4.7
    - name: MINIMAX
      model: MiniMax-M2.7
```

### gRPC Server

| Parameter | Description | Default |
|-----------|-------------|---------|
| `server.port` | gRPC server port | `50051` |
| `server.metricsPort` | Prometheus metrics port (0 = disabled) | `9090` |
| `server.token` | Authentication token (empty = no auth) | `""` |
| `server.grpcReflection` | Enable gRPC reflection (disable in production) | `false` |

### TLS

| Parameter | Description | Default |
|-----------|-------------|---------|
| `tls.enabled` | Enable TLS encryption | `false` |
| `tls.certFile` | Certificate file path in container | `""` |
| `tls.keyFile` | Key file path in container | `""` |
| `tls.existingSecret` | Use existing TLS Secret (e.g., from cert-manager) | `""` |

### MCP (Model Context Protocol)

Extend LLM capabilities with external tools via the Model Context Protocol. Supports both local (stdio) and remote (SSE) transports. Tools are automatically prefixed with `mcp_` to avoid naming collisions.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `mcp.enabled` | Enable MCP server integration | `false` |
| `mcp.servers` | Inline MCP server definitions | `[]` |
| `mcp.existingConfigMap` | Existing ConfigMap with `mcp_servers.json` | `""` |

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

Real-time monitoring of Kubernetes deployments with automatic context injection into LLM prompts. Collects deployment status, pod health, events, logs, metrics (CPU/memory via metrics-server), HPA status, Prometheus metrics, and node health.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `watcher.enabled` | Enable K8s resource watching | `false` |
| `watcher.deployment` | Single-target: deployment name (legacy) | `""` |
| `watcher.namespace` | Single-target: namespace (legacy) | `""` |
| `watcher.targets` | Multi-target watch list | `[]` |
| `watcher.interval` | Watch interval | `"30s"` |
| `watcher.window` | Analysis time window | `"2h"` |
| `watcher.maxLogLines` | Max log lines per container | `100` |
| `watcher.maxContextChars` | Budget for LLM context injection | `32000` |

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
    - deployment: worker
      namespace: batch
```

### Ollama (Local Models)

| Parameter | Description | Default |
|-----------|-------------|---------|
| `ollama.enabled` | Enable Ollama provider | `false` |
| `ollama.baseUrl` | Ollama API endpoint | `"http://ollama:11434"` |
| `ollama.model` | Model name | `""` |

### GitHub Copilot

| Parameter | Description | Default |
|-----------|-------------|---------|
| `copilot.model` | Model (gpt-4o, claude-sonnet-4, gemini-2.0-flash, etc.) | `""` |
| `copilot.maxTokens` | Max response tokens | `""` |
| `copilot.apiBaseUrl` | API URL override for enterprise | `""` |

### Agents, Skills & Bootstrap

| Parameter | Description | Default |
|-----------|-------------|---------|
| `agents.enabled` | Enable custom agent provisioning | `false` |
| `agents.definitions` | Inline agent markdown definitions (key = filename) | `{}` |
| `agents.existingConfigMap` | Existing ConfigMap with agent `.md` files | `""` |
| `skills.enabled` | Enable skill provisioning | `false` |
| `skills.definitions` | Inline skill markdown definitions | `{}` |
| `skills.existingConfigMap` | Existing ConfigMap with skill `.md` files | `""` |
| `bootstrap.enabled` | Enable bootstrap files (SOUL.md, USER.md, IDENTITY.md, RULES.md, AGENTS.md) | `false` |
| `bootstrap.definitions` | Inline bootstrap file definitions | `{}` |
| `bootstrap.existingConfigMap` | Existing ConfigMap with bootstrap `.md` files | `""` |

```yaml
bootstrap:
  enabled: true
  definitions:
    SOUL.md: |
      You are a DevOps assistant specialized in Kubernetes troubleshooting.
      Always explain your reasoning before suggesting actions.
    USER.md: |
      The team uses ArgoCD for GitOps and prefers Helm over Kustomize.
      Production namespace is "prod", staging is "staging".

agents:
  enabled: true
  definitions:
    security-auditor.md: |
      ---
      name: security-auditor
      description: Kubernetes security audit agent
      model: gpt-4o
      skills: [rbac, network-policy, pod-security]
      ---
      You are a security auditor for Kubernetes clusters...
```

### Skill Registry

| Parameter | Description | Default |
|-----------|-------------|---------|
| `skillRegistry.enabled` | Enable multi-registry skill marketplace | `false` |
| `skillRegistry.registryUrls` | Comma-separated additional registry URLs | `""` |
| `skillRegistry.registryDisable` | Comma-separated registries to disable | `""` |
| `skillRegistry.installDir` | Override skill install directory | `""` |

### Storage & Persistence

| Parameter | Description | Default |
|-----------|-------------|---------|
| `persistence.enabled` | Enable persistent storage for sessions | `true` |
| `persistence.storageClass` | StorageClass name | `""` |
| `persistence.accessModes` | PVC access modes | `["ReadWriteOnce"]` |
| `persistence.size` | PVC size | `1Gi` |
| `memory.enabled` | Enable long-term memory persistence (daily notes, facts, patterns) | `false` |

### Networking & Security

| Parameter | Description | Default |
|-----------|-------------|---------|
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `50051` |
| `service.headless` | Headless Service for gRPC client-side load balancing | `false` |
| `ingress.enabled` | Enable Ingress | `false` |
| `ingress.className` | Ingress class | `""` |
| `networkPolicy.enabled` | Enable NetworkPolicy | `false` |
| `rbac.create` | Create RBAC resources | `true` |
| `rbac.clusterWide` | Use ClusterRole for multi-namespace watcher | `false` |
| `rbac.additionalRules` | Additional RBAC rules | `[]` |

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
| `serviceMonitor.scrapeTimeout` | Scrape timeout | `""` |
| `serviceMonitor.labels` | Additional labels | `{}` |
| `prometheusUrl` | Prometheus URL for AIOps metrics enrichment | `""` |

### Plugins

| Parameter | Description | Default |
|-----------|-------------|---------|
| `plugins.enabled` | Enable plugin loader | `false` |
| `plugins.initImage` | Init container image with plugin binaries in `/plugins/` | `""` |
| `plugins.existingPVC` | Existing PVC with pre-installed plugins | `""` |

### Shell Safety

| Parameter | Description | Default |
|-----------|-------------|---------|
| `safety.enabled` | Enable shell command safety validation | `false` |
| `safety.config` | Inline safety config (deny/allow patterns, workspace boundary) | `{}` |
| `safety.existingConfigMap` | Existing ConfigMap with `safety_config.json` | `""` |

### Pod Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `resources.requests.cpu` | CPU request | `100m` |
| `resources.requests.memory` | Memory request | `128Mi` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `512Mi` |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |
| `extraEnv` | Extra environment variables | `[]` |
| `imagePullSecrets` | Image pull secrets | `[]` |

## CRDs

This chart installs 17 Custom Resource Definitions for the AIOps platform:

| CRD | Short Name | Description |
|-----|------------|-------------|
| `AIInsight` | `ai` | AI-generated root cause analysis and recommendations |
| `Anomaly` | `anom` | Raw signal from watchers before correlation into issues |
| `ApprovalPolicy` | `ap` | Approval requirements for remediation (auto/manual/quorum) |
| `ApprovalRequest` | `ar` | Pending approval with blast radius assessment |
| `AuditEvent` | `ae` | Immutable append-only audit trail of platform actions |
| `ChaosExperiment` | `chaos` | Chaos engineering experiments (7 types) |
| `ClusterRegistration` | `cr` | Multi-cluster federation registration |
| `EscalationPolicy` | `ep` | L1 -> L2 -> L3 escalation chains for incidents |
| `IncidentSLA` | `sla` | SLA targets for incident response and resolution by severity |
| `Instance` | `inst` | ChatCLI instance configuration |
| `Issue` | `iss` | Correlated operational problem detected in the cluster |
| `NotificationPolicy` | `np` | Multi-channel notification rules (Slack, PagerDuty, Email, etc.) |
| `PostMortem` | `pm` | Auto-generated post-incident lifecycle report |
| `RemediationPlan` | `rp` | Automated remediation plan with 54+ action types |
| `Runbook` | `rb` | Operational procedures linked to issue types |
| `ServiceLevelObjective` | `slo` | SLO with Google SRE burn rate alerting and error budgets |
| `SourceRepository` | `srcrepo` | Links workloads to source code for code-aware analysis |

> **Note:** CRDs are shared between the chatcli server and operator charts. If both are installed in the same cluster, the CRDs from whichever chart was installed first will be used.

## Upgrading

```bash
helm upgrade chatcli oci://ghcr.io/diillson/charts/chatcli \
  --namespace chatcli \
  --reuse-values
```

## Uninstalling

```bash
helm uninstall chatcli -n chatcli
```

> **Note:** CRDs are not removed automatically by Helm. To remove them:
> ```bash
> kubectl get crd -o name | grep platform.chatcli.io | xargs kubectl delete
> ```

## Documentation

For full documentation, visit [chatcli.edilsonfreitas.com](https://chatcli.edilsonfreitas.com).
