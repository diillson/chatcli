# ChatCLI AIOps Operator Helm Chart

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/chatcli-operator)](https://artifacthub.io/packages/helm/chatcli-operator/chatcli-operator)
[![Security Scan](https://github.com/diillson/chatcli/actions/workflows/security-scan.yml/badge.svg)](https://github.com/diillson/chatcli/actions/workflows/security-scan.yml)
![Trivy](https://img.shields.io/badge/Trivy-image%20scanning-00C9A7?logo=aquasecurity)
![Cosign](https://img.shields.io/badge/Sigstore-cosign%20signed-4B32C3?logo=sigstore)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![GitHub](https://img.shields.io/badge/GitHub-diillson%2Fchatcli-181717?logo=github)](https://github.com/diillson/chatcli)

A Kubernetes operator for **autonomous incident detection, AI-powered root cause analysis, and automated remediation**. It provisions ChatCLI servers (`Instance` resources), turns their watcher alerts into anomalies and issues, asks the server for an AI analysis, and executes approved remediation plans -- closing the incident lifecycle automatically. Security controls include fail-closed REST authentication, a remediation resource allowlist, log scrubbing before anything reaches an LLM, and TLS 1.3 on every operator-to-server connection.

## Features

- **Autonomous Incident Pipeline**: Detection -> Correlation -> AI Analysis -> Remediation -> PostMortem
- **17 Custom Resource Definitions**: Complete AIOps platform modeled as Kubernetes-native resources
- **55 Remediation Actions**: Across Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, nodes, storage, secrets, networking, and GitOps (Helm rollback, ArgoCD sync)
- **Approval Workflows**: Auto, manual, and quorum modes with blast radius prediction, change windows, and configurable timeouts
- **Decision Engine** (opt-in): confidence adjustment and a per-namespace circuit breaker in front of every plan
- **SLO Monitoring**: Google SRE burn rate alerting with error budget tracking
- **Chaos Engineering**: 7 experiment types for proactive resilience testing
- **Multi-Cluster Federation**: Register clusters and gate severities by cluster tier
- **Escalation Policies**: L1 -> L2 -> L3 escalation chains with configurable timeouts
- **Multi-Channel Notifications**: Slack, PagerDuty, Opsgenie, Email, Microsoft Teams, and webhooks, with throttling and deduplication
- **Audit Trail**: every platform action recorded as an `AuditEvent` resource, queryable and exportable over the REST API
- **Code-Aware Analysis**: Link workloads to git repositories for source-level incident diagnostics
- **REST API**: `/api/v1/` endpoints for incidents, SLOs, runbooks, approvals, postmortems, analytics, clusters, policies and audit (port 8090)
- **Web Dashboard**: Built-in web interface for incident management, served on the same port
- **Prometheus Metrics**: 30+ `chatcli_operator_*` metrics; 4 Grafana dashboards in [`deploy/grafana`](https://github.com/diillson/chatcli/tree/main/deploy/grafana)

## Prerequisites

- Kubernetes 1.30+
- Helm 3.10+

## Installation

### From the OCI registry

```bash
helm install chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --version <version> \
  --namespace chatcli-system --create-namespace
```

`<version>` is the chart version without the `v` prefix (e.g. `1.211.2`); the chart's `appVersion` pins the operator image to the same release. Omitting `--version` installs the latest release.

### From source

```bash
git clone https://github.com/diillson/chatcli.git
helm install chatcli-operator chatcli/deploy/helm/chatcli-operator \
  --namespace chatcli-system --create-namespace
```

### With Prometheus integration

```bash
helm install chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --version <version> \
  --namespace chatcli-system --create-namespace \
  --set prometheusUrl=http://prometheus-server.monitoring.svc:9090 \
  --set serviceMonitor.enabled=true
```

### Verify signatures

Chart OCI artifacts and container images are signed with [Cosign](https://github.com/sigstore/cosign) (keyless OIDC via GitHub Actions):

```bash
# The Helm chart
cosign verify ghcr.io/diillson/charts/chatcli-operator:<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/diillson/chatcli/'

# The container image
cosign verify ghcr.io/diillson/chatcli-operator:<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/diillson/chatcli/'
```

## After installing

### 1. Create the dashboard / REST API keys

The REST API and dashboard (port 8090) are fail-closed: until API keys exist every `/api/` call returns 401 (unless `security.devMode=true`, which is for development only). Keys are sent in the `X-API-Key` header.

The operator reads them from a Secret with the fixed name **`chatcli-operator-secrets`**, key **`api-keys`**, in its own namespace, and re-reads it every 30 seconds. The value is a YAML list of `key` / `role` / `description`; roles are `viewer` (read), `operator` (acknowledge, snooze, resolve, approve, review) and `admin` (everything, including deletes).

Create it yourself (recommended; also works with External Secrets or Vault):

```bash
kubectl -n chatcli-system create secret generic chatcli-operator-secrets \
  --from-literal=api-keys="$(printf -- '- key: "%s"\n  role: admin\n  description: platform team\n' "$(openssl rand -hex 32)")"
```

or let the chart render it (the keys are then stored in the Helm release):

```yaml
apiKeys:
  create: true
  entries:
    - key: "<generate with: openssl rand -hex 32>"
      role: admin
      description: platform team
```

A ConfigMap `chatcli-operator-config` with the same `api-keys` key is still read as a fallback when the Secret is absent.

Open the dashboard:

```bash
kubectl -n chatcli-system port-forward svc/chatcli-operator 8090:8090
# http://localhost:8090 -- log in with one of the keys
```

### 2. Create an Instance -- with a credential and TLS

An `Instance` provisions a ChatCLI server. Two things are mandatory:

- **A credential.** The server binds every interface inside a cluster, and a reachable listener with no credential would admit every caller as an administrator -- so the server refuses to start, and the operator refuses to create the Deployment. It sets `AuthenticationConfigured=False` instead. Satisfy it with `spec.server.token`, `spec.server.security.jwtSecretRef` (tokens must carry an `exp` claim), `spec.server.security.jwtPublicKeyRef` (plus `operatorTokenRef` or `operatorClientCertSecretName`, so the operator itself can authenticate), or mTLS with `spec.server.tls.clientCASecretName`. A credential passed through `spec.extraEnv` as `CHATCLI_SERVER_TOKEN` or `CHATCLI_JWT_SECRET` counts too. Binding loopback with `spec.server.security.bindAddress: "127.0.0.1"` removes the requirement, but then nothing outside the pod -- the operator included -- can reach the server.
- **TLS.** The operator always dials the server over **TLS 1.3**; there is no plaintext mode. Without `spec.server.tls.enabled: true` the server runs, but `ServerReachable=False` and the AIOps pipeline (alerts, AI insights, remediation) cannot talk to it. The certificate must be valid for `<instance>.<namespace>.svc.cluster.local`, the name the operator dials. See the [TLS cookbook](#tls-cookbook----connecting-the-operator-to-the-server) below.

```bash
kubectl create namespace chatcli
kubectl -n chatcli create secret generic chatcli-api-keys \
  --from-literal=ANTHROPIC_API_KEY=<your-anthropic-api-key>
kubectl -n chatcli create secret generic chatcli-server-token \
  --from-literal=token="$(openssl rand -hex 32)"
# chatcli-tls: tls.crt, tls.key and ca.crt -- see the TLS cookbook
```

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli
  namespace: chatcli
spec:
  provider: CLAUDEAI
  apiKeys:
    name: chatcli-api-keys          # envFrom'd: provider API keys
  server:
    token:
      name: chatcli-server-token
      key: token
    tls:
      enabled: true
      secretName: chatcli-tls       # valid for chatcli.chatcli.svc.cluster.local
```

Check the conditions:

```bash
kubectl -n chatcli get instance chatcli \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
```

`AuthenticationConfigured`, `Available` and `ServerReachable` should all be `True`. With `spec.image.tag` unset the server image is pinned to the operator's own release. Only one Instance per cluster drives the AIOps pipeline: the operator connects to the first ready Instance it finds.

A complete sample (namespace, Secrets and Instance) is in [`operator/config/samples/platform_v1alpha1_instance.yaml`](https://github.com/diillson/chatcli/blob/main/operator/config/samples/platform_v1alpha1_instance.yaml).

## Architecture

```
                    ┌─────────────────────────────────────────────┐
                    │            ChatCLI AIOps Operator            │
                    └─────────────────────────────────────────────┘

  ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌───────────────┐    ┌──────────┐
  │ Anomaly  │───>│  Issue   │───>│AIInsight │───>│Remediation    │───>│PostMortem│
  │(Detection│    │(Correlate│    │(AI Root  │    │Plan (Execute  │    │(Auto     │
  │ Signals) │    │ & Score) │    │ Cause)   │    │ 55 Actions)   │    │ Report)  │
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

### Incident lifecycle

1. **Detection** -- the WatcherBridge receives the ChatCLI server's watcher alerts over a `StreamAlerts` gRPC stream (or polls `GetAlerts` every 30s with `alertTransport: poll`) and creates `Anomaly` resources in the workload's namespace
2. **Correlation** -- AnomalyReconciler groups anomalies by resource and time window, calculates risk scores, creates `Issue` resources
3. **Analysis** -- AIInsightReconciler enriches issues with K8s context, logs (stack trace extraction), metrics, GitOps status, code correlation, and cascade analysis
4. **Planning** -- IssueReconciler selects a matching runbook or generates AI-suggested remediation actions
5. **Approval** -- ApprovalPolicy rules (in the Issue's namespace) decide whether auto/manual/quorum approval is needed, with blast radius prediction
6. **Execution** -- RemediationReconciler executes approved plans (restart, scale, rollback, Helm rollback, ArgoCD sync, etc.)
7. **Resolution** -- Success marks the issue as resolved; failure triggers re-analysis with failure context
8. **PostMortem** -- Generated for remediated incidents with timeline, root cause, and recommendations
9. **Notifications** -- Multi-channel delivery with throttling, deduplication, and escalation
10. **SLO Tracking** -- Burn rate alerting and error budget consumption monitoring

## Configuration

### Operator core

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Operator replicas. Only the leader serves the REST API and runs the alert bridge | `1` |
| `leaderElect` | Leader election (lease `chatcli-operator-lock`); keep it on with more than one replica | `true` |
| `image.repository` | Operator image | `ghcr.io/diillson/chatcli-operator` |
| `image.tag` | Image tag (defaults to the chart `appVersion`) | `""` |
| `image.pullPolicy` | Image pull policy (`Always`, `IfNotPresent`, `Never`) | `IfNotPresent` |
| `imagePullSecrets` | Image pull secrets | `[]` |
| `nameOverride` | Override chart name | `""` |
| `fullnameOverride` | Override full name | `""` |

The chart also sets `CHATCLI_OPERATOR_APP_VERSION` to the chart `appVersion`: Instances that leave `spec.image.tag` empty run the server image of the same release, and a `helm upgrade` of the operator rolls them in lockstep.

### AIOps behavior

| Parameter | Description | Default |
|-----------|-------------|---------|
| `alertTransport` | How alerts arrive from the server: `stream` (StreamAlerts, falls back to polling while the server lacks the RPC) or `poll` (GetAlerts every 30s) | `stream` |
| `decisionEngine.enabled` | Adjust AI confidence by history, patterns, time of day, concurrent issues and severity before a plan runs; 3 failed remediations in a namespace within an hour trip a circuit breaker | `false` |
| `clusterName` | Name of this cluster's `ClusterRegistration`; its tier decides which severities wait for a human. A name no registration carries sends every plan to manual approval | `""` |
| `prometheusUrl` | Prometheus URL queried for CPU/memory/latency/error-rate trends during analysis | `""` |

### API & ports

| Parameter | Description | Default |
|-----------|-------------|---------|
| `api.port` | REST API and web dashboard port | `8090` |
| `metrics.port` | Prometheus metrics port (plain HTTP) | `8080` |
| `health.port` | Health probe port (`/healthz`, `/readyz`) | `8081` |
| `service.type` | Service type (`ClusterIP`, `NodePort`, `LoadBalancer`) | `ClusterIP` |

### Dashboard / REST API keys

| Parameter | Description | Default |
|-----------|-------------|---------|
| `apiKeys.create` | Render the `chatcli-operator-secrets` Secret from `apiKeys.entries` | `false` |
| `apiKeys.entries` | List of `{key, role, description}`; `role` is `viewer`, `operator` or `admin` | `[]` |

### Observability

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceMonitor.enabled` | Create a Prometheus Operator ServiceMonitor for the `metrics` port | `false` |
| `serviceMonitor.interval` | Scrape interval | `"30s"` |
| `serviceMonitor.scrapeTimeout` | Scrape timeout | `""` |
| `serviceMonitor.labels` | Additional labels for the ServiceMonitor | `{}` |

### RBAC & pod security

The operator needs cluster-wide access to watch and remediate workloads. Its ClusterRole includes read/write on workloads, Secrets and ConfigMaps (it provisions Instance resources and rotates secrets as a remediation), RoleBindings/ClusterRoleBindings for Instance watchers, and `bind` only on the ClusterRoles the chart pre-provisions (`chatcli-watcher`, `chatcli-role-viewer|operator|admin|superadmin`). Review `templates/rbac.yaml` before installing in a regulated cluster.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rbac.create` | Create the ClusterRole/ClusterRoleBinding and the shared ClusterRoles | `true` |
| `serviceAccount.create` | Create the ServiceAccount | `true` |
| `serviceAccount.name` | ServiceAccount name override | `""` |
| `serviceAccount.annotations` | ServiceAccount annotations (e.g. IAM roles) | `{}` |
| `podSecurityContext` | Pod security context | `runAsNonRoot: true`, `seccompProfile: RuntimeDefault` |
| `securityContext` | Container security context | `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]` |

The default pod and container security contexts satisfy the `restricted` Pod Security Standard.

### Security hardening

- **Fail-closed REST API authentication** -- API keys are required for every `/api/` endpoint; there is no anonymous access unless `security.devMode` is enabled
- **Resource type allowlist for remediation** -- 17 resource kinds are allowed by default; any other kind is refused, and 18 dangerous kinds (Namespace, Node, PersistentVolume, ...) are refused with an explicit reason unless added with `security.allowedResourceTypes`
- **Log scrubbing before LLM submission** -- 18 built-in regex patterns redact secrets, tokens, passwords and PII from log data before it is sent for analysis; extend them with `security.logScrubPatterns`
- **CORS deny-all default** -- cross-origin requests are rejected unless an origin is configured
- **TLS 1.3 to every server** -- the operator always dials Instances over TLS 1.3, verifying against the Instance's `ca.crt` (or system CAs), and can present a client certificate for mTLS
- **REST API over TLS 1.3** -- optional, with `security.apiTLS.*`
- **Optional NetworkPolicy** -- `networkPolicy.enabled` restricts ingress to the API, metrics and health ports and egress to DNS, HTTPS, the Kubernetes API and the Instances' gRPC port
- **Audit trail** -- operator actions are recorded as `AuditEvent` resources

| Parameter | Description | Default |
|-----------|-------------|---------|
| `security.devMode` | Dev mode: with no API keys configured the REST API admits every call as admin. Never in production | `false` |
| `security.apiTLS.certFile` | TLS certificate path (inside the container) for the REST API | `""` |
| `security.apiTLS.keyFile` | TLS key path for the REST API | `""` |
| `security.grpcTLS.certFile` | Operator-wide client certificate for dialing servers (used when an Instance sets no `operatorClientCertSecretName`) | `""` |
| `security.grpcTLS.keyFile` | Key for `security.grpcTLS.certFile` | `""` |
| `security.grpcTLS.caFile` | Operator-wide CA (absolute path), used when an Instance's TLS Secret has no `ca.crt` | `""` |
| `security.allowedResourceTypes` | Extra comma-separated resource kinds remediation may touch, added to the 17 defaults | `""` |
| `security.logScrubPatterns` | Extra comma-separated regex patterns scrubbed from logs before analysis | `""` |
| `security.allowedDiagnosticCommands` | Extra read-only diagnostic commands the remediation engine may run (comma-separated, appended to the 100 built-ins). Read by the operator, not by Instances | `""` |
| `security.corsOrigin` | A single allowed CORS origin (kept for compatibility) | `""` |
| `security.corsAllowedOrigins` | Allowed CORS origins, or `["*"]` | `[]` |
| `security.corsAllowedMethods` | Allowed CORS methods (empty = GET, POST, PUT, DELETE, OPTIONS) | `[]` |
| `security.corsAllowCredentials` | Allow cookies / Authorization on cross-origin calls | `false` |
| `security.auditLogPath` | **Deprecated, ignored.** The operator no longer writes a file audit log; the key is accepted only so existing values files keep validating | `""` |
| `extraEnv` | Extra environment variables for the operator container | `[]` |
| `extraVolumes` | Extra pod volumes (e.g. the Secrets behind `security.apiTLS.*` / `security.grpcTLS.*`) | `[]` |
| `extraVolumeMounts` | Extra volume mounts for the operator container | `[]` |
| `tmpVolume.sizeLimit` | Size limit of the writable `/tmp` emptyDir (the root filesystem is read-only); SourceRepository clones and per-sync git credential files live there | `1Gi` |

**Example: REST API over TLS and an operator-wide CA**

```yaml
extraVolumes:
  - name: api-tls
    secret:
      secretName: chatcli-operator-api-tls   # tls.crt, tls.key
  - name: grpc-ca
    secret:
      secretName: chatcli-internal-ca        # ca.crt
extraVolumeMounts:
  - name: api-tls
    mountPath: /etc/chatcli-operator/api-tls
    readOnly: true
  - name: grpc-ca
    mountPath: /etc/chatcli-operator/grpc-ca
    readOnly: true
security:
  allowedResourceTypes: "Rollout"   # e.g. an Argo Rollouts kind, on top of the defaults
  corsAllowedOrigins: ["https://dashboard.example.com"]
  apiTLS:
    certFile: /etc/chatcli-operator/api-tls/tls.crt
    keyFile: /etc/chatcli-operator/api-tls/tls.key
  grpcTLS:
    caFile: /etc/chatcli-operator/grpc-ca/ca.crt
```

### NetworkPolicy

| Parameter | Description | Default |
|-----------|-------------|---------|
| `networkPolicy.enabled` | Create a NetworkPolicy for the operator pod | `false` |
| `networkPolicy.apiIngressFrom` | NetworkPolicyPeer list allowed to reach the API/dashboard port (empty = any) | `[]` |
| `networkPolicy.metricsIngressFrom` | NetworkPolicyPeer list allowed to reach the metrics port (empty = any) | `[]` |
| `networkPolicy.egress` | `restricted` (DNS 53, 443, `kubernetesApiPort`, `instanceGrpcPort`, the `prometheusUrl` port, `egressExtraPorts`) or `allowAll` | `restricted` |
| `networkPolicy.kubernetesApiPort` | Kubernetes API server port | `6443` |
| `networkPolicy.instanceGrpcPort` | gRPC port of the managed Instances (`spec.server.port`) | `50051` |
| `networkPolicy.egressExtraPorts` | Extra egress ports, e.g. SMTP (587/465) for email notifications or 22 for git over SSH | `[]` |

The health port stays open to any source (kubelet probes are not exempt on every CNI).

### Resources

| Parameter | Description | Default |
|-----------|-------------|---------|
| `resources.requests.cpu` | CPU request | `100m` |
| `resources.requests.memory` | Memory request | `128Mi` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `256Mi` |

### Scheduling

| Parameter | Description | Default |
|-----------|-------------|---------|
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |

### CRD upgrade hook

Helm applies `crds/` only on first install. A pre-install/pre-upgrade hook Job re-applies every CRD so the schema always matches the binary.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `crdUpgrade.enabled` | Run the CRD re-apply hook | `true` |
| `crdUpgrade.image.repository` | kubectl image | `registry.k8s.io/kubectl` |
| `crdUpgrade.image.tag` | kubectl tag (full patch tag) | `v1.31.10` |
| `crdUpgrade.image.pullPolicy` | Pull policy | `IfNotPresent` |
| `crdUpgrade.resources` | Hook Job resources | `50m/64Mi` requests, `200m/128Mi` limits |
| `crdUpgrade.tolerations` | Hook Job tolerations | `[]` |
| `crdUpgrade.nodeSelector` | Hook Job node selector | `{}` |

## CRDs

This chart installs 17 Custom Resource Definitions for the AIOps platform:

| CRD | Short Name | Description |
|-----|------------|-------------|
| `AIInsight` | `ai` | AI-generated root cause analysis and recommendations |
| `Anomaly` | `anom` | Raw signal from watchers before correlation into issues |
| `ApprovalPolicy` | `ap` | Approval requirements for remediation (auto/manual/quorum) |
| `ApprovalRequest` | `ar` | Pending approval with blast radius assessment |
| `AuditEvent` | `ae` | Audit trail of platform actions |
| `ChaosExperiment` | `chaos` | Chaos engineering experiments (7 types) |
| `ClusterRegistration` | `cr` | Multi-cluster federation registration |
| `EscalationPolicy` | `ep` | L1 -> L2 -> L3 escalation chains for incidents |
| `IncidentSLA` | `sla` | SLA targets for incident response and resolution by severity |
| `Instance` | `inst` | A ChatCLI server provisioned by the operator (needs a credential and TLS, see above) |
| `Issue` | `iss` | Correlated operational problem detected in the cluster |
| `NotificationPolicy` | `np` | Multi-channel notification rules |
| `PostMortem` | `pm` | Auto-generated post-incident report |
| `RemediationPlan` | `rp` | Remediation plan with 55 action types |
| `Runbook` | `rb` | Operational procedures linked to issue types |
| `ServiceLevelObjective` | `slo` | SLO with burn rate alerting and error budgets |
| `SourceRepository` | `srcrepo` | Links workloads to source code for code-aware analysis |

The `chatcli` server chart ships the same CRDs. If both charts are installed, keep them on the same version: each chart's hook re-applies its own copy.

## Examples

### Approval Policy

ApprovalPolicies are read from the namespace of the RemediationPlan, which is the namespace of the affected workload -- create one per workload namespace.

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: ApprovalPolicy
metadata:
  name: production-approval
  namespace: production
spec:
  enabled: true
  defaultMode: manual
  rules:
    - name: critical-manual
      match:
        severities: ["critical", "high"]
        actionTypes: ["RestartDeployment", "ScaleDeployment", "RollbackDeployment"]
      mode: manual
      timeoutMinutes: 15
      changeWindow:
        timezone: "America/Sao_Paulo"
        allowedDays: ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday"]
        startHour: 8
        endHour: 18
    - name: low-severity-auto
      match:
        severities: ["low", "medium"]
        actionTypes: ["RestartDeployment"]
      mode: auto
      timeoutMinutes: 30
      autoApproveConditions:
        minConfidence: 0.85
        maxSeverity: medium
        historicalSuccessRate: 0.9
    - name: rollback-quorum
      match:
        severities: ["critical"]
        actionTypes: ["RollbackDeployment"]
      mode: quorum
      requiredApprovers: 2
      timeoutMinutes: 60
```

Approve or reject an `ApprovalRequest` with an annotation whose value is `<approver>:<reason>`:

```bash
kubectl annotate approvalrequest <name> platform.chatcli.io/approve="oncall-sre:tested in staging"
kubectl annotate approvalrequest <name> platform.chatcli.io/reject="oncall-sre:needs a rollback instead"
```

### Service Level Objective

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: ServiceLevelObjective
metadata:
  name: api-availability
  namespace: production
spec:
  enabled: true
  serviceName: api-server
  indicator:
    type: availability          # availability | latency | error_rate | throughput
    metricSource: prometheus    # prometheus | watcher | issues
    prometheusQuery: >-
      sum(rate(http_requests_total{code!~"5.."}[5m]))
      / sum(rate(http_requests_total[5m]))
  target:
    percentage: 99.9
    window: 30d
  alertPolicy:
    burnRateWindows:
      - shortWindow: 5m
        longWindow: 1h
        burnRateThreshold: 14.4
        severity: critical
```

### Escalation Policy

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: EscalationPolicy
metadata:
  name: production-escalation
  namespace: chatcli-system
spec:
  enabled: true
  severities: ["critical", "high"]
  levels:
    - name: L1
      timeoutMinutes: 15
      targets:
        - type: oncall            # channel | user | team | oncall
          name: primary
      notifyChannels: ["slack-oncall"]
    - name: L2
      timeoutMinutes: 30
      targets:
        - type: team
          name: sre
      notifyChannels: ["slack-oncall", "pagerduty-team"]
```

### Notification Policy

Channel settings go in `config`, or in a Secret named by `secretRef` whose keys are merged into it (Slack and Teams read `webhook_url`, PagerDuty `routing_key`, webhooks `url`).

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: NotificationPolicy
metadata:
  name: production-alerts
  namespace: chatcli-system
spec:
  enabled: true
  channels:
    - name: slack-oncall
      type: slack                 # slack | pagerduty | opsgenie | email | webhook | teams
      config: {}
      secretRef:
        name: slack-webhook       # key: webhook_url
    - name: pagerduty-team
      type: pagerduty
      config: {}
      secretRef:
        name: pagerduty-routing   # key: routing_key
  rules:
    - name: critical-page
      severities: ["critical"]
      channels: ["slack-oncall", "pagerduty-team"]
    - name: contained-needs-human
      severities: ["critical", "high"]
      states: ["Contained"]
      channels: ["pagerduty-team"]
    - name: high-to-slack
      severities: ["high", "medium"]
      channels: ["slack-oncall"]
  throttle:
    maxPerHour: 20
    deduplicationWindow: 10m
```

### Chaos Experiment

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: ChaosExperiment
metadata:
  name: api-pod-failure
  namespace: chaos-testing
spec:
  enabled: true
  experimentType: pod_failure   # pod_kill | pod_failure | cpu_stress | memory_stress | network_delay | network_loss | disk_stress
  target:
    kind: Deployment
    name: api-server
    namespace: production
  duration: 5m
  schedule: "0 3 * * 1"
  dryRun: true
  safetyChecks:
    minHealthyPods: 1
    abortOnIssueDetected: true
```

### Source Repository (code-aware analysis)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: SourceRepository
metadata:
  name: api-server-repo
  namespace: production
spec:
  url: "https://github.com/example-org/api-server"
  branch: main
  authType: none                # none | ssh | token | basic (credentials via secretRef)
  resource:
    kind: Deployment
    name: api-server
    namespace: production
```

Credentials come from the Secret named by `secretRef` in the same namespace: `token` (authType `token`), `username` and `password` (authType `basic`), or `ssh-key` plus `known_hosts` (authType `ssh`). They are handed to git for each command and never written to the clone's `.git/config`; clones made by earlier versions, which kept the token in the origin URL, are cleaned on their next sync. An ssh repository checks the server's host key against `known_hosts`; without that key the sync fails unless `spec.sshHostKeyPolicy: acceptNew` trusts the first key the host presents (and rejects a different one afterwards):

```bash
ssh-keyscan github.com > known_hosts
kubectl -n production create secret generic api-server-git \
  --from-file=ssh-key=./deploy_key --from-file=known_hosts=./known_hosts
```

## Upgrading

```bash
helm upgrade chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --version <version> \
  --namespace chatcli-system \
  --reset-then-reuse-values
```

`--reset-then-reuse-values` (Helm 3.14+) starts from the new chart's defaults and re-applies your previous overrides. Plain `--reuse-values` skips the defaults of keys added by newer chart versions (for example `apiKeys`, `networkPolicy`, `extraVolumes`) and can fail the render; with an older Helm, pass your values file (`-f my-values.yaml`) instead.

The CRD hook refreshes the CRDs first; Instances without `spec.image.tag` then roll to the matching server release.

## Uninstalling

```bash
helm uninstall chatcli-operator -n chatcli-system
```

> **Note:** Helm does not remove CRDs. Deleting them deletes every Instance, Issue and policy in the cluster:
> ```bash
> kubectl get crd -o name | grep platform.chatcli.io | xargs kubectl delete
> ```

## TLS cookbook -- connecting the operator to the server

The operator dials each Instance at **`<instance>.<namespace>.svc.cluster.local:<spec.server.port>`**, always over TLS 1.3. Two things must be right or the connection fails (`ServerReachable=False`).

### 1. The certificate must carry SANs for the in-cluster DNS name

`openssl req -x509` without a SAN emits a certificate modern TLS clients reject:

```
transport: authentication handshake failed: x509: certificate is not valid for any names, but wanted to match chatcli-prod.chatcli-system.svc.cluster.local
```

Create it with SANs that match the name the operator dials:

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

### 2. The Secret must carry `ca.crt` for a private CA

The operator uses the **`ca.crt` key of the TLS Secret the Instance references** as the trust root, and the system CAs when it is absent. For a self-signed certificate the certificate is its own CA; without `ca.crt` you get:

```
transport: authentication handshake failed: x509: certificate signed by unknown authority
```

Create the Secret with all three keys:

```bash
kubectl -n chatcli-system create secret generic chatcli-tls \
  --from-file=tls.crt=tls.crt \
  --from-file=tls.key=tls.key \
  --from-file=ca.crt=tls.crt   # self-signed: the cert is its own CA
```

Then reference it from the Instance (with a credential, as always):

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-prod
  namespace: chatcli-system
spec:
  provider: CLAUDEAI
  apiKeys:
    name: chatcli-api-keys
  server:
    token:
      name: chatcli-server-token
      key: token
    tls:
      enabled: true
      secretName: chatcli-tls
```

No `CHATCLI_GRPC_TLS_CA` or extra mount is needed on the operator on this path -- the CA travels with the Instance's TLS Secret. `security.grpcTLS.caFile` (with `extraVolumes` + `extraVolumeMounts`) is the alternative when several Instances share one CA and their Secrets carry no `ca.crt`.

### Certificates from cert-manager or ACM

| Issuer | `ca.crt` in the Secret? | SAN must cover |
|---|---|---|
| cert-manager + internal CA issuer | Yes -- cert-manager writes it | `<instance>.<namespace>.svc.cluster.local` in `dnsNames` |
| AWS ACM Private CA | Yes -- add the Private CA bundle as `ca.crt` | set at issuance |
| Self-signed (openssl) | Yes -- `ca.crt=tls.crt` | `subjectAltName` in `openssl.cnf` |
| Public CA (Let's Encrypt, ACM Public) | Not needed (system trust store) | but a public CA will not issue for `*.svc.cluster.local`, so this does not fit the in-cluster name the operator dials |

cert-manager with an internal CA is the cleanest path:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: chatcli-tls
  namespace: chatcli-system
spec:
  secretName: chatcli-tls
  issuerRef:
    name: internal-ca
    kind: ClusterIssuer
  commonName: chatcli-prod.chatcli-system.svc.cluster.local
  dnsNames:
    - chatcli-prod.chatcli-system.svc.cluster.local
    - chatcli-prod.chatcli-system.svc
    - chatcli-prod
  duration: 8760h
  renewBefore: 720h
```

### Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `AuthenticationConfigured=False`, no Deployment | No credential on the Instance | Set `spec.server.token` (or JWT / mTLS) |
| `ServerReachable=False`, `tls: first record does not look like a TLS handshake` | `spec.server.tls` not enabled -- the server speaks plaintext | Enable TLS with a Secret as above |
| `x509: certificate is not valid for any names` | No SAN for `<instance>.<namespace>.svc.cluster.local` | Regenerate the cert with the SANs above |
| `x509: certificate signed by unknown authority` | Private CA, no `ca.crt` in the Secret | Add `ca.crt` to the TLS Secret |
| `connection refused` after fixing TLS | Server not listening / Service without endpoints | `kubectl get endpoints <instance> -n <namespace>` must list pod IPs |

### Production checklist (TLS)

- [ ] The TLS Secret is in the Instance's namespace and contains `tls.crt`, `tls.key` and (private CA) `ca.crt`
- [ ] `tls.crt` has SANs for `<instance>.<ns>.svc.cluster.local`, `<instance>.<ns>.svc` and `<instance>` (check with `openssl x509 -in tls.crt -noout -ext subjectAltName`)
- [ ] The Instance reports `ServerReachable=True`, and the operator logs `Connected to Instance` without `x509:` errors

## Exposing the web dashboard via Ingress

The chart does not ship an Ingress -- reach the dashboard with `kubectl port-forward svc/<fullname> 8090:8090` (the Service is named `chatcli-operator` for a release named `chatcli-operator`). To expose it, add your own Ingress pointing at the operator Service, and serve it over TLS: the dashboard sends the API key on every request.

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: chatcli-dashboard
  namespace: chatcli-system
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /$2
spec:
  ingressClassName: nginx
  tls:
    - hosts: ["chatcli.example.com"]
      secretName: chatcli-dashboard-tls
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

The `rewrite-target` + capture group is required when mounting the dashboard under a sub-path -- its static assets are served from `/` and would 404 otherwise.

## Documentation

- Operator guide: [chatcli.edilsonfreitas.com/features/k8s-operator](https://chatcli.edilsonfreitas.com/features/k8s-operator)
- Production setup: [chatcli.edilsonfreitas.com/cookbook/aiops-production-setup](https://chatcli.edilsonfreitas.com/cookbook/aiops-production-setup)
