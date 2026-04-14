# ChatCLI AIOps Operator Helm Chart

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/chatcli-operator)](https://artifacthub.io/packages/helm/chatcli-operator/chatcli-operator)
[![Security Scan](https://github.com/diillson/chatcli/actions/workflows/security-scan.yml/badge.svg)](https://github.com/diillson/chatcli/actions/workflows/security-scan.yml)
![Trivy](https://img.shields.io/badge/Trivy-image%20scanning-00C9A7?logo=aquasecurity)
![Cosign](https://img.shields.io/badge/Sigstore-cosign%20signed-4B32C3?logo=sigstore)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![GitHub](https://img.shields.io/badge/GitHub-diillson%2Fchatcli-181717?logo=github)](https://github.com/diillson/chatcli)

A production-grade, security-hardened Kubernetes operator for **autonomous incident detection, AI-powered root cause analysis, and automated remediation**. The operator watches cluster resources, correlates anomalies into actionable issues, generates AI insights with full root cause analysis, and executes approved remediation plans -- closing the entire incident lifecycle automatically. Built with defense-in-depth security controls including fail-closed authentication, resource allowlists, log scrubbing, and TLS 1.3 enforcement.

## Features

- **Autonomous Incident Pipeline**: Detection -> Correlation -> AI Analysis -> Remediation -> PostMortem
- **17 Custom Resource Definitions**: Complete AIOps platform modeled as Kubernetes-native resources
- **54+ Remediation Actions**: Across Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, GitOps (Helm, ArgoCD, Flux), infra, storage, security, and networking
- **Approval Workflows**: Auto, manual, and quorum modes with blast radius prediction, change windows, and configurable timeouts
- **SLO Monitoring**: Google SRE burn rate alerting with error budget tracking and business hours support
- **Chaos Engineering**: 7 experiment types for proactive resilience testing
- **Multi-Cluster Federation**: Manage incidents across multiple Kubernetes clusters
- **Escalation Policies**: L1 -> L2 -> L3 escalation chains with configurable thresholds
- **Multi-Channel Notifications**: Slack, PagerDuty, Email, webhooks with throttling and deduplication
- **Immutable Audit Trail**: Append-only audit events for compliance and forensics
- **Code-Aware Analysis**: Link workloads to git repositories for source-level incident diagnostics
- **REST API Gateway**: 30+ endpoints for programmatic access (port 8090)
- **Web Dashboard**: Built-in web interface for incident management
- **Prometheus Metrics**: 20+ operator metrics with 4 pre-configured Grafana dashboards
- **Enterprise Security**: Fail-closed auth, resource allowlist, log scrubbing, CORS hardening, TLS 1.3, RBAC least-privilege, structured audit logging

## Prerequisites

- Kubernetes 1.30+
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

### Verify Signature

All chart OCI artifacts and container images are signed with [Cosign](https://github.com/sigstore/cosign) using keyless OIDC via GitHub Actions:

```bash
# Verify the Helm chart
cosign verify ghcr.io/diillson/charts/chatcli-operator:<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/diillson/chatcli/'

# Verify the container image
cosign verify ghcr.io/diillson/chatcli-operator:<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/diillson/chatcli/'
```

### With Prometheus Integration

```bash
helm install chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system --create-namespace \
  --set prometheusUrl=http://prometheus-server.monitoring.svc:9090 \
  --set serviceMonitor.enabled=true
```

## Architecture

```
                    ┌─────────────────────────────────────────────┐
                    │            ChatCLI AIOps Operator            │
                    └─────────────────────────────────────────────┘

  ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌───────────────┐    ┌──────────┐
  │ Anomaly  │───>│  Issue   │───>│AIInsight │───>│Remediation    │───>│PostMortem│
  │(Detection│    │(Correlate│    │(AI Root  │    │Plan (Execute  │    │(Auto     │
  │ Signals) │    │ & Score) │    │ Cause)   │    │ 54+ Actions)  │    │ Report)  │
  └──────────┘    └──────────┘    └──────────┘    └───────┬───────┘    └──────────┘
       ↑                                                  │
       │                                                  ▼
  ┌──────────┐                                    ┌───────────────┐
  │ Watchers │                                    │  Approval     │
  │(Metrics, │                                    │  Policy       │
  │Logs,     │                                    │(Auto/Manual/  │
  │Events)   │                                    │ Quorum)       │
  └──────────┘                                    └───────────────┘
                                                         │
                              ┌───────────────────────────┼──────────────────────┐
                              ▼                           ▼                      ▼
                        ┌──────────┐              ┌──────────────┐       ┌──────────────┐
                        │Escalation│              │Notification  │       │  SLO / SLA   │
                        │Policy    │              │Policy        │       │  Monitoring  │
                        │(L1→L2→L3)│              │(Slack,PD,...)│       │(Burn Rate)   │
                        └──────────┘              └──────────────┘       └──────────────┘
```

### Incident Lifecycle

1. **Detection** -- WatcherBridge polls ChatCLI server alerts every 30s, creates `Anomaly` resources
2. **Correlation** -- AnomalyReconciler groups anomalies by resource and time window, calculates risk scores, creates `Issue` resources
3. **Analysis** -- AIInsightReconciler enriches issues with K8s context, logs (stack trace extraction), metrics, GitOps status, code correlation, and cascade analysis
4. **Planning** -- IssueReconciler selects a matching runbook or generates AI-suggested remediation actions
5. **Approval** -- ApprovalPolicy rules determine if auto/manual/quorum approval is needed, with blast radius prediction
6. **Execution** -- RemediationReconciler executes approved plans (restart, scale, rollback, Helm upgrade, ArgoCD sync, etc.)
7. **Resolution** -- Success marks the issue as resolved; failure triggers re-analysis with failure context
8. **PostMortem** -- Auto-generated for all remediations with timeline, root cause, and recommendations
9. **Notifications** -- Multi-channel delivery with throttling, deduplication, and escalation
10. **SLO Tracking** -- Burn rate alerting and error budget consumption monitoring

## Configuration

### Operator Core

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of operator replicas | `1` |
| `leaderElect` | Enable leader election for HA (recommended with replicas > 1) | `true` |
| `image.repository` | Operator image | `ghcr.io/diillson/chatcli-operator` |
| `image.tag` | Image tag (defaults to appVersion) | `""` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `imagePullSecrets` | Image pull secrets | `[]` |
| `nameOverride` | Override chart name | `""` |
| `fullnameOverride` | Override full name | `""` |

### API & Ports

| Parameter | Description | Default |
|-----------|-------------|---------|
| `api.port` | REST API and web dashboard port | `8090` |
| `metrics.port` | Prometheus metrics port | `8080` |
| `health.port` | Health probes port (liveness + readiness) | `8081` |

### Observability

| Parameter | Description | Default |
|-----------|-------------|---------|
| `prometheusUrl` | Prometheus URL for metrics enrichment during AI analysis | `""` |
| `serviceMonitor.enabled` | Enable Prometheus ServiceMonitor (requires Prometheus Operator) | `false` |
| `serviceMonitor.interval` | Scrape interval | `"30s"` |
| `serviceMonitor.scrapeTimeout` | Scrape timeout | `""` |
| `serviceMonitor.labels` | Additional labels for ServiceMonitor | `{}` |

When `prometheusUrl` is set, the operator queries Prometheus for CPU, memory, latency, and error rate trends around incident time, providing quantitative context to the AI analysis.

### RBAC & Security

The operator requires cluster-wide access to monitor and remediate resources across all namespaces.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rbac.create` | Create ClusterRole and ClusterRoleBinding | `true` |
| `serviceAccount.create` | Create ServiceAccount | `true` |
| `serviceAccount.name` | ServiceAccount name override | `""` |
| `serviceAccount.annotations` | ServiceAccount annotations (e.g., for IAM roles) | `{}` |
| `podSecurityContext.runAsNonRoot` | Run as non-root user | `true` |
| `securityContext.allowPrivilegeEscalation` | Allow privilege escalation | `false` |
| `securityContext.readOnlyRootFilesystem` | Read-only root filesystem | `true` |
| `securityContext.capabilities.drop` | Dropped capabilities | `["ALL"]` |

### Security Hardening

The operator ships with defense-in-depth security controls suitable for production and regulated environments. Key security properties:

- **Fail-closed REST API authentication** -- API keys are required for all REST endpoints; there is no anonymous access unless `security.devMode` is explicitly enabled
- **Resource type allowlist for remediation** -- Only 17 safe resource types are permitted by default; 18 dangerous types (Namespace, Node, PersistentVolume, etc.) are blocked unless explicitly allowed
- **Log scrubbing before LLM submission** -- 18 regex patterns automatically redact secrets, tokens, passwords, and PII from log data before it is sent to any AI provider
- **CORS deny-all default** -- Cross-origin requests are rejected unless an explicit origin is configured
- **TLS 1.3 enforced for gRPC** -- All gRPC communication between components uses TLS 1.3 with mutual TLS support
- **RBAC least-privilege** -- ClusterRoles grant read-only access to ClusterRoles and ClusterRoleBindings; write access is scoped to operator-managed resources only
- **NetworkPolicy for operator pod** -- Restricts ingress and egress traffic to known required endpoints
- **Structured audit logging** -- All operator actions are recorded as JSON lines for integration with SIEM and compliance tooling

| Parameter | Description | Default |
|-----------|-------------|---------|
| `security.devMode` | Enable dev mode (disables REST API auth) | `false` |
| `security.apiTLS.certFile` | TLS certificate for REST API | `""` |
| `security.apiTLS.keyFile` | TLS key for REST API | `""` |
| `security.grpcTLS.certFile` | mTLS client certificate for gRPC | `""` |
| `security.grpcTLS.keyFile` | mTLS client key for gRPC | `""` |
| `security.grpcTLS.caFile` | CA certificate for gRPC server verification | `""` |
| `security.allowedResourceTypes` | Comma-separated allowed resource types for remediation | `""` |
| `security.logScrubPatterns` | Custom regex patterns for log scrubbing before LLM | `""` |
| `security.corsOrigin` | Allowed CORS origin (empty = deny all) | `""` |
| `security.auditLogPath` | Audit log file path (JSON lines) | `""` |
| `extraEnv` | Extra environment variables for operator pod | `[]` |

**Example: Production security configuration**

```yaml
security:
  allowedResourceTypes: "Deployment,Service,ConfigMap,HPA,PDB"
  corsOrigin: "https://dashboard.example.com"
  auditLogPath: "/var/log/operator/audit.jsonl"
  grpcTLS:
    certFile: "/etc/tls/client.crt"
    keyFile: "/etc/tls/client.key"
    caFile: "/etc/tls/ca.crt"
```

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

## Examples

### Approval Policy

Define approval requirements with change windows and auto-approve conditions:

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
    - name: infra-quorum
      match:
        severities: ["critical"]
        actionTypes: ["scale", "rollback"]
        namespaces: ["infrastructure"]
      mode: quorum
      requiredApprovers: 2
      timeoutMinutes: 60
```

Approve or reject via annotations:

```bash
kubectl annotate approvalrequest <name> platform.chatcli.io/approve="Approved: tested in staging"
kubectl annotate approvalrequest <name> platform.chatcli.io/reject="Rejected: needs rollback instead"
```

### Service Level Objective

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

### Escalation Policy

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: EscalationPolicy
metadata:
  name: production-escalation
spec:
  levels:
    - name: L1
      notifyChannels: ["slack-oncall"]
      waitMinutes: 15
    - name: L2
      notifyChannels: ["slack-oncall", "pagerduty-team"]
      waitMinutes: 30
    - name: L3
      notifyChannels: ["slack-oncall", "pagerduty-team", "email-engineering-leads"]
      waitMinutes: 0
```

### Notification Policy

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: NotificationPolicy
metadata:
  name: production-alerts
spec:
  channels:
    - name: slack-oncall
      type: slack
      config:
        webhookUrl: "https://hooks.slack.com/services/XXX/YYY/ZZZ"
    - name: pagerduty-team
      type: pagerduty
      config:
        routingKey: "your-pagerduty-routing-key"
  rules:
    - match:
        severities: ["critical"]
      channels: ["slack-oncall", "pagerduty-team"]
    - match:
        severities: ["high", "warning"]
      channels: ["slack-oncall"]
```

### Chaos Experiment

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: ChaosExperiment
metadata:
  name: api-pod-failure
spec:
  target:
    kind: Deployment
    name: api-server
    namespace: production
  experimentType: pod-failure
  duration: 5m
  schedule: "0 3 * * 1"
```

### Source Repository (Code-Aware Analysis)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: SourceRepository
metadata:
  name: api-server-repo
spec:
  workloadRef:
    kind: Deployment
    name: api-server
    namespace: production
  repository:
    url: "https://github.com/myorg/api-server"
    branch: main
    path: "/"
```

## Upgrading

```bash
helm upgrade chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system \
  --reuse-values
```

## Uninstalling

```bash
helm uninstall chatcli-operator -n aiops-system
```

> **Note:** CRDs are not removed automatically by Helm. To remove them:
> ```bash
> kubectl get crd -o name | grep platform.chatcli.io | xargs kubectl delete
> ```

## TLS Cookbook — connecting the operator to a self-signed gRPC server

This is the flow most production clusters actually use: the `chatcli` server exposes gRPC over TLS with a self-signed certificate, and the operator's `WatcherBridge` must dial it over an in-cluster DNS name. Two things must be right or the connection fails silently.

### 1. The TLS Secret must include SANs for the in-cluster DNS names

`openssl req -x509` without a config emits a cert with **no** `subjectAltName`, and modern gRPC/TLS clients reject it with:

```
transport: authentication handshake failed: x509: certificate is not valid for any names, but wanted to match chatcli-prod.chatcli-system.svc.cluster.local
```

Create the cert with SANs that match how the operator dials the service (see `instance.spec.server.address`):

```bash
cat > openssl.cnf <<'EOF'
[req]
distinguished_name = req_dn
x509_extensions    = v_ext
prompt             = no

[req_dn]
CN = chatcli-prod.chatcli-system.svc.cluster.local

[v_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = chatcli-prod.chatcli-system.svc.cluster.local
DNS.2 = chatcli-prod.chatcli-system.svc
DNS.3 = chatcli-prod
DNS.4 = localhost
EOF

openssl req -x509 -newkey rsa:4096 -sha256 -days 825 -nodes \
  -keyout tls.key -out tls.crt -config openssl.cnf -extensions v_ext
```

### 2. The Secret must also carry `ca.crt` so the operator can trust the self-signed cert

For self-signed certs (issuer == subject), the `tls.crt` *is* its own CA. The operator reads the CA from the **`ca.crt` key of the same TLS Secret referenced by the Instance CR** and uses it as the trust root (see `WatcherBridge.buildConnectionOpts`). If `ca.crt` is missing, you get:

```
transport: authentication handshake failed: x509: certificate signed by unknown authority
```

Fix — create the Secret with all three keys:

```bash
kubectl -n chatcli-system create secret generic chatcli-tls \
  --from-file=tls.crt=tls.crt \
  --from-file=tls.key=tls.key \
  --from-file=ca.crt=tls.crt   # self-signed: cert is its own CA
```

Then reference it from the Instance CR:

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-prod
  namespace: chatcli-system
spec:
  server:
    address: chatcli-prod.chatcli-system.svc.cluster.local:50051
    tls:
      enabled: true
      secretName: chatcli-tls
```

No `SSL_CERT_FILE` / `CHATCLI_GRPC_TLS_CA` env or volume mount is needed on the operator when you take this path — the CA travels with the Instance's TLS Secret.

> Using `CHATCLI_GRPC_TLS_CA` is a secondary path for cases where multiple Instances share a CA. It requires `extraEnv` + your own volume/volumeMount; the Helm chart does not mount a CA file automatically.

### Troubleshooting

| Error (operator logs) | Cause | Fix |
|---|---|---|
| `x509: certificate is not valid for any names` | Server cert has no SAN matching `spec.server.address` | Regenerate cert with `openssl.cnf` + `subjectAltName` covering the in-cluster FQDN |
| `x509: certificate signed by unknown authority` | Self-signed cert, no CA trust on operator | Add `ca.crt` key to the `chatcli-tls` Secret referenced by the Instance |
| `connection refused` after fixing TLS | Service selector mismatch or server not listening on 50051 | `kubectl get endpoints chatcli-prod -n chatcli-system` — must list pod IPs |

### Production checklist (TLS delta)

In addition to the main checklist, verify:

- [ ] `chatcli-tls` Secret exists in the same namespace as the Instance and contains **`tls.crt`, `tls.key`, and `ca.crt`**
- [ ] `tls.crt` has `subjectAltName` entries for `<instance>.<ns>.svc.cluster.local`, `<instance>.<ns>.svc`, and `<instance>` (verify with `openssl x509 -in tls.crt -noout -text | grep -A1 'Subject Alternative Name'`)
- [ ] `instance.spec.server.address` exactly matches one of the SANs
- [ ] Operator logs show `Connected to Instance` (no `x509:` errors) within ~30s of the Instance becoming `Ready`

## Exposing the Web Dashboard via Ingress

The operator chart does **not** ship an Ingress — the dashboard is reachable via `kubectl port-forward svc/<release>-chatcli-operator 8090:8090`. To expose it cluster-externally, add your own Ingress pointing at the operator Service:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: chatcli-dashboard
  namespace: aiops-system
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /$2
spec:
  ingressClassName: nginx
  rules:
    - host: chatcli.example.com
      http:
        paths:
          - path: /chatcli(/|$)(.*)
            pathType: ImplementationSpecific
            backend:
              service:
                name: chatcli-operator
                port:
                  number: 8090
```

The `rewrite-target` + capture group is required when mounting the dashboard under a sub-path — the dashboard's static assets are served from `/` and would 404 otherwise.

## Documentation

For full documentation including cookbook recipes, architecture deep-dives, and production setup guides, visit [chatcli.edilsonfreitas.com](https://chatcli.edilsonfreitas.com).
