# ChatCLI Kubernetes Operator -- Enterprise AIOps Platform

Production-grade Kubernetes operator for managing ChatCLI server instances and orchestrating an autonomous, security-hardened AIOps pipeline via Custom Resource Definitions (CRDs). Designed for enterprise environments with fail-closed defaults, structured audit logging, RBAC least-privilege enforcement, and end-to-end TLS.

## Overview

The ChatCLI Operator goes beyond simple instance management. It implements a **full AIOps platform** that autonomously detects anomalies, correlates signals, requests AI analysis, and executes remediation -- all without external dependencies beyond the LLM provider. Every stage of the pipeline enforces security constraints including resource allowlists, log scrubbing, and admission webhooks.

### Architecture

```
                         K8s Cluster
  +-------------------------------------------------------------------+
  |                                                                   |
  |   ChatCLI Server (gRPC)            Operator                      |
  |   +----------------+         +------------------------+          |
  |   | K8s Watcher    |<--gRPC--| WatcherBridge          |          |
  |   | (collectors)   |         | (polls GetAlerts)       |          |
  |   |                |         +--------+---------------+          |
  |   | AnalyzeIssue   |                  | creates                  |
  |   | (LLM call)     |                  v                          |
  |   |                |         +----------------+                  |
  |   | AgenticStep    |         | Anomaly CR     |                  |
  |   | (agentic loop) |         +-------+--------+                  |
  |   +------+---------+                 | AnomalyReconciler         |
  |          |                           v                           |
  |          |                   +----------------+                  |
  |          |                   | Issue CR       |                  |
  |          |                   | (correlation)  |                  |
  |          |                   +-------+--------+                  |
  |          |                           | IssueReconciler           |
  |          |                           v                           |
  |          |                   +----------------+                  |
  |          <--- AnalyzeIssue --| AIInsight CR   |                  |
  |          |                   +-------+--------+                  |
  |          |                           |                           |
  |          |                           v                           |
  |          |                   +--------------------+              |
  |          <--- AgenticStep ---| RemediationPlan CR |              |
  |          |    (loop)         | (runbook or agentic)|              |
  |          |                   +--------+-----------+              |
  |          |                            | RemediationReconciler    |
  |          |                            v                          |
  |          |            Scale / Restart / Rollback / PatchConfig   |
  |          |            / AdjustResources / DeletePod              |
  |          |                            |                          |
  |          |                            v (on agentic resolution)  |
  |          |                   +----------------+                  |
  |          |                   | PostMortem CR  |                  |
  |          |                   | (auto-generated)|                  |
  |          |                   +----------------+                  |
  +----------+------------------------------------------------------- +
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
                      then Severity+Kind) -- takes precedence
                   b) Auto-generate Runbook from AI actions (reusable for future)
                   c) Agentic AI remediation (no runbook/AI actions available):
                      AI drives remediation step-by-step via observe-decide-act loop

5. EXECUTION     RemediationReconciler executes actions:
                   Standard mode: all actions executed sequentially
                   Agentic mode:  AI decides each action, observes result, decides next
                   54 supported actions across 9 categories:
                   Deployment: Scale, Restart, Rollback, AdjustResources, DeletePod, PatchConfig
                   StatefulSet: Scale, Restart, Rollback (ControllerRevision), AdjustResources,
                     DeletePod, ForceDelete, UpdateStrategy, RecreatePVC, PartitionUpdate
                   DaemonSet: Restart, Rollback (ControllerRevision), AdjustResources,
                     DeletePod, UpdateStrategy, PauseRollout, CordonAndDelete
                   Job: Retry, AdjustResources, DeleteFailed, Suspend, Resume,
                     AdjustParallelism, AdjustDeadline, AdjustBackoffLimit, ForceDeletePods
                   CronJob: Suspend, Resume, Trigger, AdjustResources, AdjustSchedule,
                     AdjustDeadline, AdjustHistory, AdjustConcurrency, DeleteActiveJobs, ReplaceTemplate
                   Generic: HelmRollback, ArgoSync, AdjustHPA, CordonNode, DrainNode,
                     ResizePVC, RotateSecret, ExecDiagnostic, UpdateIngress,
                     PatchNetworkPolicy, ApplyManifest

6. RESOLUTION    On success -> Issue resolved, dedup entries invalidated
                 On failure -> Re-analysis with failure context (different strategy)
                   -> up to maxAttempts -> Escalate

7. POSTMORTEM    On agentic resolution -> PostMortem CR auto-generated:
                   Timeline, root cause, impact, actions executed,
                   lessons learned, prevention actions
                 Reusable Runbook also generated from successful agentic steps
```

### Issue State Machine

```
Detected -> Analyzing -> Remediating -> Resolved
                |  ^         |             |
                |  +---------+ Retry       +-->  PostMortem (agentic)
                |            |
                +--> Escalated <--+ (max attempts exhausted)

Analyzing -> Remediating via:
  - Manual Runbook match
  - AI-generated Runbook
  - Agentic AI mode (no runbook/AI actions -> AI drives step-by-step)
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
      # clientCASecretName: chatcli-clients-ca   # mutual TLS: ca.crt client certs are verified against
    token:
      name: chatcli-server-token    # Secret containing the auth token
      key: token                    # Key within the Secret (default: "token")
      # Required unless security.jwtSecretRef is set or the server binds
      # loopback — see "Server authentication is required" below.
    security:
      rateLimitRps: 20
      rateLimitBurst: 50
      bindAddress: "0.0.0.0"
      jwtSecretRef:                 # HS256: the operator mints its own tokens from it
        name: chatcli-jwt
        key: secret
      # jwtPublicKeyRef:            # RS256 instead: PEM public key the server verifies with
      #   name: chatcli-jwt
      #   key: public.pem
      # jwtIssuer: "my-idp"         # required iss / aud claims, when your issuer sets them
      # jwtAudience: "chatcli"
      # operatorTokenRef:           # credential the operator presents (needed with RS256)
      #   name: chatcli-operator-jwt
      #   key: token
      # mtlsRole: user              # role for callers identified by client certificate alone
      # operatorClientCertSecretName: chatcli-operator-cert   # tls.crt/tls.key the operator presents
  # pipeline:                        # host the full ChatCLI turn engine behind the
  #   enabled: true                  # ChatTurn/RunCoder/RunAgent/tool RPCs (serialized turns)
  # features:                        # server capabilities, typed (no extraEnv needed)
  #   memory: { enabled: true, mode: index }
  #   knowledge: true
  #   budget: { sessionUSD: "5", dailyUSD: "50", hardStop: true }
  #   hub: true
  #   caBundleSecretName: corp-ca    # ca.crt trusted by every outbound TLS connection
  #   encryptionKeyRef: { name: chatcli-at-rest, key: key }
  #   logRotation: { maxSizeMB: 200, maxBackups: 3, maxAgeDays: 7, compress: true }
  # scheduling:                      # pod placement
  #   nodeSelector: { pool: ai }
  #   tolerations: [{ key: gpu, operator: Exists }]
  #   imagePullSecrets: [{ name: ghcr }]
  #   priorityClassName: high
  #   podAnnotations: { sidecar.istio.io/inject: "false" }
  # serviceAccount:                  # IRSA / Workload Identity, kept across reconciles
  #   annotations: { eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/chatcli }
  watcher:
    enabled: true
    interval: "30s"
    targets:
      - name: api-gateway
        namespace: production
        metricsPort: 9090
      - name: worker
        namespace: batch
      - name: postgres                # StatefulSet monitoring
        kind: StatefulSet
        namespace: production
      - name: fluentd                 # DaemonSet monitoring
        kind: DaemonSet
        namespace: logging
      - name: etl-pipeline            # CronJob monitoring
        kind: CronJob
        namespace: data
  agents:
    configMapRef: chatcli-agents         # ConfigMap with agent .md files
    skillsConfigMapRef: chatcli-skills   # ConfigMap with skill .md files
  plugins:
    image: myregistry/chatcli-plugins:latest  # Init container with plugin binaries
    # pvcName: chatcli-plugins-pvc           # Or use an existing PVC
  extraEnv:
    - name: CHATCLI_AGENT_SECURITY_MODE
      value: "strict"
```

> **Remote Resource Discovery**: When `agents` or `plugins` are configured, connected clients automatically discover and use these resources via gRPC. Agents/skills are transferred to the client for local prompt composition; plugins can be executed remotely or downloaded.

#### Instance with GitHub Copilot

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-copilot
spec:
  provider: COPILOT
  model: gpt-6-astra     # or gpt-4o, claude-sonnet-4-6, gemini-2.5-flash
  replicas: 1
  apiKeys:
    name: chatcli-copilot-keys   # Secret with GITHUB_COPILOT_TOKEN
  server:
    port: 50051
```

The referenced Secret should contain:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: chatcli-copilot-keys
type: Opaque
stringData:
  GITHUB_COPILOT_TOKEN: "ghu_xxxxxxxxxxxx"   # From /auth login github-copilot
  # COPILOT_MODEL: "gpt-4o"                   # Optional model override
  # COPILOT_API_BASE_URL: "https://..."        # Optional enterprise URL
```

> **Note:** GitHub Copilot tokens obtained via Device Flow (`/auth login github-copilot`) are persistent and do not expire. For server/operator usage, extract the token from `~/.chatcli/auth-profiles.json` after local authentication.

#### Instance with AWS Bedrock

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-bedrock
spec:
  provider: BEDROCK
  # Claude on Bedrock requires an inference profile id
  # (prefix global./us./eu./apac.). Claude 3.x models are retired on Bedrock.
  model: global.anthropic.claude-sonnet-4-6
  replicas: 1
  apiKeys:
    name: chatcli-bedrock-keys
  server:
    port: 50051
```

### Server authentication is required

An Instance provisions a gRPC server. Inside a cluster that server binds
every interface unless `spec.server.security.bindAddress` says otherwise,
and a listener reachable from the network with no credential admits every
caller as an administrator.

So the server refuses to start in that shape, and the operator refuses to
provision the Deployment rather than leaving a pod to crash-loop with the
reason in its logs. Instead it raises a condition on the Instance:

```bash
kubectl get instance my-instance -o jsonpath='{.status.conditions[?(@.type=="AuthenticationConfigured")]}'
```

```
{"type":"AuthenticationConfigured","status":"False","reason":"CredentialMissing",
 "message":"server would listen on a reachable address with no credential ..."}
```

Three ways to satisfy it:

| Option | Field | When |
|---|---|---|
| Shared token | `spec.server.token` | one credential for every caller |
| Per-user JWTs (HS256) | `spec.server.security.jwtSecretRef` | callers carry their own identity and role; tokens must carry an `exp` claim |
| Per-user JWTs (RS256) | `spec.server.security.jwtPublicKeyRef` | tokens are issued by an external identity provider; `jwtIssuer` / `jwtAudience` pin the claims the server requires |
| Mutual TLS | `spec.server.tls.clientCASecretName` | every connection must present a certificate signed by that CA; the server names the caller after it with the role in `spec.server.security.mtlsRole` (default `user`) |
| No credential | `spec.server.security.bindAddress: "127.0.0.1"` | the server is only reached from inside its own pod |

A credential supplied directly through `spec.extraEnv` as
`CHATCLI_SERVER_TOKEN`, `CHATCLI_JWT_SECRET`, `CHATCLI_JWT_PUBLIC_KEY` or
`CHATCLI_SERVER_TLS_CLIENT_CA` counts too.

#### How the operator authenticates

The AIOps pipeline (alerts, analysis, remediation steps) calls the same
server, so the operator needs a credential of its own. It picks one in this
order and reports the choice on the Instance:

| Precedence | Field | What the operator sends |
|---|---|---|
| 1 | `spec.server.token` | the shared token, as before |
| 2 | `spec.server.security.operatorTokenRef` | the referenced value verbatim: a JWT issued for `chatcli-operator` by your identity provider (RS256 servers), or a dedicated shared token |
| 3 | `spec.server.security.jwtSecretRef` | short-lived HS256 tokens it mints itself (`sub: chatcli-operator`, `role: operator`, one hour, renewed at 45 minutes), stamped with `jwtIssuer` / `jwtAudience` when set |
| 4 | `spec.server.security.operatorClientCertSecretName` | its client certificate (`tls.crt` / `tls.key`), presented on every connection; with `clientCASecretName` on the server and no bearer credential the certificate is the whole identity |

```bash
kubectl get instance my-instance -o jsonpath='{.status.conditions[?(@.type=="OperatorCredentialConfigured")]}'
```

`OperatorCredentialConfigured=False` means the server is secured in a way
the operator cannot satisfy (an RS256 public key or a client CA with no
operator credential, or a credential passed only through `extraEnv`): the
Instance still provisions, but `AnalyzeIssue` and `AgenticStep` will be
refused until one of the fields above is set. The minted token carries the
`operator` role, which the AIOps RPCs need and nothing more.

#### What is really running

Once the Deployment is ready the operator probes the server with its own
transport and credential (`Health`, then `GetServerInfo`) and records the
outcome, so `kubectl get instance` shows the version that answered and
whether the AIOps pipeline can drive it:

```bash
kubectl get instance my-instance
# NAME          READY   REPLICAS   PROVIDER   VERSION   AGE
# my-instance   true    1          CLAUDEAI   1.211.0   3d
kubectl get instance my-instance -o jsonpath='{.status.conditions[?(@.type=="ServerReachable")]}'
```

`ServerReachable=False` with reason `ProbeFailed` and a message mentioning
the credential check means the pods are up but the operator's credential
was refused; `NotServing` means the server answered but is draining. The
last known `status.serverVersion` is kept across a failed probe.

<!-- provider auth follows -->

Two authentication modes — pick one:

**IRSA (recommended on EKS)** — declare the IAM role on the Instance and the operator keeps it on the managed ServiceAccount (annotations added out of band survive too):

```yaml
spec:
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/chatcli-bedrock
```

The Secret then only needs the region:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: chatcli-bedrock-keys
type: Opaque
stringData:
  BEDROCK_REGION: "us-east-1"
```

**Static IAM user keys** — full credentials in the Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: chatcli-bedrock-keys
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "AKIA..."
  AWS_SECRET_ACCESS_KEY: "..."
  # AWS_SESSION_TOKEN: "..."                   # optional, for STS
  BEDROCK_REGION: "us-east-1"
  # Corporate proxy / private CA (optional):
  # CHATCLI_BEDROCK_CA_BUNDLE: "/etc/ssl/corp-ca-bundle.pem"
```

Minimum IAM policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream",
        "bedrock:ListFoundationModels",
        "bedrock:ListInferenceProfiles"
      ],
      "Resource": "*"
    }
  ]
}
```

See the [Bedrock feature docs](https://chatcli.edilsonfreitas.com/features/bedrock) for model ids, inference profiles, and corporate-proxy troubleshooting.

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

### Runbook (Optional -- Manual)

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
| Volume (ConfigMap) | `agents.configMapRef` | Agent .md files mounted at `/home/chatcli/.chatcli/agents/` (optional) |
| Volume (ConfigMap) | `agents.skillsConfigMapRef` | Skill .md files mounted at `/home/chatcli/.chatcli/skills/` (optional) |
| Volume (PVC/EmptyDir) | `plugins` | Plugin binaries at `/home/chatcli/.chatcli/plugins/` via init container or PVC (optional) |

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

1. **Manual Runbook match**: Tiered matching -- first by `SignalType + Severity + ResourceKind`, then by `Severity + ResourceKind`
2. **Auto-generated Runbook**: If no manual Runbook exists, AI suggested actions are materialized as a reusable Runbook CR (labeled `platform.chatcli.io/auto-generated=true`)
3. **Agentic AI Remediation**: If neither Runbook nor AI actions are available, the operator creates an **agentic plan** -- the AI drives remediation step-by-step in an observe-decide-act loop (each reconcile = 1 step, max 10 steps, 10min timeout)
4. **Escalation**: Only when agentic mode also fails after max attempts

**On agentic resolution**: The operator auto-generates a **PostMortem CR** (timeline, root cause, impact, lessons learned) and a **reusable Runbook** from the successful agentic steps.

**K8s Context Enrichment**: The AI receives full cluster context via `KubernetesContextBuilder`:
- Deployment status (replicas, conditions, images)
- Pod details (up to 5 pods: phase, restarts, container states)
- Recent events (last 15 Warning/Normal events)
- Revision history (last 5 ReplicaSet revisions with image diffs)

**Retry with Strategy Escalation**: When remediation fails, the operator triggers AI re-analysis with failure evidence from previous attempts. The AI is instructed to suggest a fundamentally different strategy.

**Supported actions (54 types)**: **Deployment**: `ScaleDeployment`, `RestartDeployment`, `RollbackDeployment` (previous/healthy/specific), `AdjustResources`, `DeletePod`, `PatchConfig` | **StatefulSet**: `ScaleStatefulSet`, `RestartStatefulSet`, `RollbackStatefulSet` (ControllerRevision), `AdjustStatefulSetResources`, `DeleteStatefulSetPod`, `ForceDeleteStatefulSetPod`, `UpdateStatefulSetStrategy`, `RecreateStatefulSetPVC`, `PartitionStatefulSetUpdate` | **DaemonSet**: `RestartDaemonSet`, `RollbackDaemonSet` (ControllerRevision), `AdjustDaemonSetResources`, `DeleteDaemonSetPod`, `UpdateDaemonSetStrategy`, `PauseDaemonSetRollout`, `CordonAndDeleteDaemonSetPod` | **Job**: `RetryJob`, `AdjustJobResources`, `DeleteFailedJob`, `SuspendJob`, `ResumeJob`, `AdjustJobParallelism`, `AdjustJobDeadline`, `AdjustJobBackoffLimit`, `ForceDeleteJobPods` | **CronJob**: `SuspendCronJob`, `ResumeCronJob`, `TriggerCronJob`, `AdjustCronJobResources`, `AdjustCronJobSchedule`, `AdjustCronJobDeadline`, `AdjustCronJobHistory`, `AdjustCronJobConcurrency`, `DeleteCronJobActiveJobs`, `ReplaceCronJobTemplate` | **GitOps**: `HelmRollback`, `ArgoSyncApp` | **Infra**: `CordonNode`, `DrainNode` | **Other**: `AdjustHPA`, `ResizePVC`, `RotateSecret`, `ExecDiagnostic`, `UpdateIngress`, `PatchNetworkPolicy`, `ApplyManifest`, `Custom` (blocked)

Priority: **Manual Runbook > Auto-generated Runbook > Agentic AI > Escalation**

## Security Hardening

The operator is designed with a defense-in-depth approach. All security controls default to deny/closed and require explicit opt-in for relaxation.

### Fail-Closed REST API

The operator REST API requires authentication by default. API keys must be provisioned via a Kubernetes Secret (`chatcli-operator-secrets`) or ConfigMap. There is no development bypass unless `CHATCLI_OPERATOR_DEV_MODE=true` is explicitly set as an environment variable. Production deployments must never enable dev mode.

### Resource Type Allowlist

The `ApplyManifest` remediation action uses an allowlist of 17 safe Kubernetes resource types. Dangerous types -- including `ClusterRole`, `Secret`, `Namespace`, `CustomResourceDefinition`, and others that could escalate privileges or cause cluster-wide impact -- are blocked by default and require explicit approval. The allowlist is configurable via the `CHATCLI_ALLOWED_RESOURCE_TYPES` environment variable.

### Log Scrubbing

Before any log data is sent to an LLM provider for analysis, the operator applies 18 regex-based scrubbing patterns that redact sensitive information including AWS access keys, JWT tokens, passwords, database connection strings, IP addresses, and email addresses. Additional custom patterns can be specified via the `CHATCLI_LOG_SCRUB_PATTERNS` environment variable.

### CORS Deny-All

The REST API rejects all cross-origin requests by default. Allowed origins must be explicitly configured. This prevents browser-based attacks against the operator API.

### TLS 1.3 Enforcement

All gRPC communication between the operator and the ChatCLI server enforces TLS 1.3 as the minimum protocol version. Mutual TLS (mTLS) is supported for bidirectional certificate verification.

### RBAC Least-Privilege

The operator requests the minimum Kubernetes RBAC permissions required:
- **ClusterRoles and ClusterRoleBindings**: Read-only access (get, list, watch). No create, update, patch, or delete permissions.
- **Namespace-scoped Roles and RoleBindings**: Used exclusively for the Instance watcher, scoped to the target namespace.

### NetworkPolicy

A default `NetworkPolicy` restricts the operator pod's egress traffic to:
- The Kubernetes API server
- Cluster DNS (UDP/TCP port 53)
- The configured gRPC server endpoint

All other egress is denied, including access to cloud metadata endpoints (169.254.169.254) that could be exploited for credential theft.

### Structured Audit Logging

The operator produces JSON-lines structured audit logs covering:
- Approval decisions for remediation plans
- Remediation action execution and outcomes
- RBAC-related changes
- REST API access events

Audit log output path is configurable via the `CHATCLI_AUDIT_LOG_PATH` environment variable.

### Admission Webhook

A `ValidatingWebhookConfiguration` enforces two critical invariants:
- Prevents deletion of `RemediationPlan` resources that are in an active (non-terminal) state.
- Validates that all action types in a `RemediationPlan` are recognized and permitted.

### Image Pinning

The operator container image is pinned to a specific version tag (never `:latest`). Cosign signature verification is available for supply chain integrity validation.

## Development

```bash
cd operator

# Build
go build ./...

# Test (130 test functions, 185 with subtests)
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
| RemediationReconciler | 38 | All 54 action types (Deployment + StatefulSet + DaemonSet + Job + CronJob), safety constraints, agentic loop, rollback, verification |
| AIInsightReconciler | 12 | Server connectivity, RPC mock, analysis parsing, withAuth, ConnectionOpts |
| PostMortemReconciler | 2 | State initialization, terminal state |
| WatcherBridge | 22 | Alert mapping, dedup, hash, pruning, anomaly creation, TLS/token buildConnectionOpts |
| CorrelationEngine | 4 | Risk scoring, severity, incident ID, related anomalies |
| Pipeline (E2E) | 3 | Full flow: Anomaly->Issue->Insight->Plan->Resolved, escalation, correlation |
| MapActionType | 6+17 | All 54 action type string->enum mappings (Deployment + StatefulSet + DaemonSet + Job + CronJob) |

## Documentation

Full documentation at [https://chatcli.edilsonfreitas.com/features/k8s-operator](https://chatcli.edilsonfreitas.com/features/k8s-operator/)

Deep-dive AIOps architecture at [https://chatcli.edilsonfreitas.com/features/aiops-platform](https://chatcli.edilsonfreitas.com/features/aiops-platform/)
