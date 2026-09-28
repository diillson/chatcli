# ChatCLI Server Helm Chart

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/chatcli)](https://artifacthub.io/packages/helm/chatcli/chatcli)
[![Security Scan](https://github.com/diillson/chatcli/actions/workflows/security-scan.yml/badge.svg)](https://github.com/diillson/chatcli/actions/workflows/security-scan.yml)
![Trivy](https://img.shields.io/badge/Trivy-image%20scanning-00C9A7?logo=aquasecurity)
![Cosign](https://img.shields.io/badge/Sigstore-cosign%20signed-4B32C3?logo=sigstore)
![Distroless](https://img.shields.io/badge/Runtime-distroless%2Fstatic-326CE5?logo=google)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![GitHub](https://img.shields.io/badge/GitHub-diillson%2Fchatcli-181717?logo=github)](https://github.com/diillson/chatcli)

Deploy **ChatCLI** as a security-hardened gRPC server on Kubernetes -- a multi-provider LLM gateway with agent modes, provider failover, MCP integration, Kubernetes-native observability and AIOps signals. This chart deploys the server on its own; to have servers provisioned and wired to the AIOps pipeline, use the [`chatcli-operator`](https://artifacthub.io/packages/helm/chatcli-operator/chatcli-operator) chart and an `Instance` resource instead.

## Features

- **Multi-Provider LLM**: OpenAI, OpenAI Assistants, Anthropic Claude, AWS Bedrock, Google Gemini, xAI Grok, ZAI (Zhipu AI), MiniMax, Moonshot (Kimi), StackSpot AI, GitHub Copilot, OpenRouter, Ollama (local)
- **Automatic Failover**: Provider fallback chain with error classification (rate limit, timeout, auth error, context overflow), exponential cooldown and health tracking
- **Agent & Coder Modes**: ReAct agent loop and a software-engineering agent with strict tool contracts, served over the pipeline RPCs (`pipeline.enabled`)
- **MCP Integration**: Model Context Protocol servers over stdio and SSE
- **Kubernetes Watcher**: Multi-target workload monitoring (status, pods, events, logs, metrics, HPA, Prometheus scraping) injected into LLM context
- **Persistent Memory & Sessions**: Sessions and long-term memory on a PVC
- **Plugins, Skills & Bootstrap files**: Provisioned from ConfigMaps, an init image or a PVC
- **gRPC Server**: TLS 1.3, mutual TLS, shared token or per-user JWT (HS256/RS256), per-client rate limiting, a per-host limit on failed authentications, message-size limits, the standard `grpc.health.v1` health service, Prometheus metrics
- **Hardened pod**: non-root, read-only root filesystem, all capabilities dropped, RuntimeDefault seccomp -- the `restricted` Pod Security Standard

## Prerequisites

- Kubernetes 1.30+
- Helm 3.10+
- At least one LLM provider API key (or IRSA for Bedrock)

## Installation

A credential is **required**: inside Kubernetes the server binds every interface and refuses to start unauthenticated (see [The server needs a credential](#the-server-needs-a-credential-in-cluster)). Every command below sets one.

### From the OCI registry

```bash
helm install chatcli oci://ghcr.io/diillson/charts/chatcli \
  --version <version> \
  --namespace chatcli --create-namespace \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=<your-openai-api-key> \
  --set server.token="$(openssl rand -hex 32)"
```

`<version>` is the chart version without the `v` prefix (e.g. `1.211.2`); the chart's `appVersion` pins the server image to the same release. Omitting `--version` installs the latest release.

### From source

```bash
git clone https://github.com/diillson/chatcli.git
helm install chatcli chatcli/deploy/helm/chatcli \
  --namespace chatcli --create-namespace \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=<your-openai-api-key> \
  --set server.token="$(openssl rand -hex 32)"
```

### Using an existing Secret

The Secret is loaded with `envFrom`, so its keys are environment variable names. Put the server credential in it too:

```bash
kubectl create namespace chatcli
kubectl create secret generic chatcli-llm-keys \
  --namespace chatcli \
  --from-literal=CHATCLI_SERVER_TOKEN="$(openssl rand -hex 32)" \
  --from-literal=OPENAI_API_KEY=<your-openai-api-key> \
  --from-literal=ANTHROPIC_API_KEY=<your-anthropic-api-key>

helm install chatcli oci://ghcr.io/diillson/charts/chatcli \
  --version <version> \
  --namespace chatcli \
  --set llm.provider=OPENAI \
  --set secrets.existingSecret=chatcli-llm-keys
```

Setting `server.token` together with `secrets.existingSecret` also works: the chart then puts the token in a small Secret of its own (`<fullname>-server-token`), and that value wins over a `CHATCLI_SERVER_TOKEN` in your Secret.

### Verify signatures

Chart OCI artifacts and container images are signed with [Cosign](https://github.com/sigstore/cosign) (keyless OIDC via GitHub Actions):

```bash
# The Helm chart
cosign verify ghcr.io/diillson/charts/chatcli:<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/diillson/chatcli/'

# The container image
cosign verify ghcr.io/diillson/chatcli:<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/diillson/chatcli/'
```

## Connecting clients

```bash
# Read the shared token back (chart-managed Secret)
export CHATCLI_REMOTE_TOKEN="$(kubectl -n chatcli get secret chatcli \
  -o jsonpath='{.data.CHATCLI_SERVER_TOKEN}' | base64 -d)"

# Plaintext server (tls.enabled=false), through a port-forward. The client
# dials TLS unless CHATCLI_ALLOW_INSECURE=true.
kubectl -n chatcli port-forward svc/chatcli 50051:50051
CHATCLI_ALLOW_INSECURE=true chatcli connect localhost:50051 --token "$CHATCLI_REMOTE_TOKEN"

# TLS server (--ca-cert implies --tls; plain --tls verifies against the system CAs)
chatcli connect chatcli.example.com:443 --ca-cert ca.crt --token "$CHATCLI_REMOTE_TOKEN"

# One-shot mode (CI/CD pipelines)
chatcli connect chatcli.example.com:443 --tls --token "$CHATCLI_REMOTE_TOKEN" \
  -p "Analyze the last 5 commits for security issues"
```

The address can also be given as `--addr` or `CHATCLI_REMOTE_ADDR`. Clients can use the server's configured provider or forward their own key with `--llm-key`.

## Configuration

### The server needs a credential in-cluster

Inside Kubernetes the server binds every interface, because that is what makes Service routing work. A listener reachable from the network with no credential would admit every caller as an administrator, so the server **refuses to start** in that shape (`refusing to serve an unauthenticated API`) and names the settings that are missing.

Set one of:

| Option | Value | When |
|---|---|---|
| Shared token | `server.token`, or `CHATCLI_SERVER_TOKEN` in `secrets.existingSecret` | one credential for every caller (role admin) |
| Per-user JWTs | `security.jwtSecretRef` / `security.jwtSecret` (HS256) or `security.jwtPublicKeyRef` / `security.jwtPublicKey` (RS256) | callers carry their own identity and role; tokens must carry an `exp` claim |
| Mutual TLS | `tls.*` plus `security.tlsClientCA` | every connection must present a client certificate |
| No credential | `security.bindAddress: "127.0.0.1"` | the server is only reached from inside its own pod |

JWT material fails closed: when `security.jwtPublicKey*` (or a `jwtSecret` that looks like a PEM key) cannot be loaded and JWT is the only credential, the server refuses to start (`refusing to start: CHATCLI_JWT_PUBLIC_KEY is set but no RSA public key could be loaded ...`); with a shared token or mTLS also configured it keeps serving and rejects every JWT caller. Repeated failed authentications from one host are throttled (5 per minute), valid credentials are never counted.

`server.token` is delivered to the server as `CHATCLI_SERVER_TOKEN` from a Secret, never as a command-line argument. Prefer `security.jwtSecretRef` / `jwtPublicKeyRef` over the inline `jwtSecret` / `jwtPublicKey`: inline values land in the Deployment's env, a reference keeps them in a Secret.

### gRPC server

| Parameter | Description | Default |
|-----------|-------------|---------|
| `server.port` | gRPC server port | `50051` |
| `server.metricsPort` | Prometheus metrics and `/healthz` port (`0` disables metrics; probes then use a TCP check on the gRPC port) | `9090` |
| `server.token` | Shared bearer token. **Required in-cluster** unless JWT or mTLS is set | `""` |
| `server.grpcReflection` | Sets `CHATCLI_GRPC_REFLECTION=true`, which registers gRPC server reflection (grpcurl and similar tools can list the services); reflection calls still need the server credential. Keep off in production | `false` |

### Health probes

A kubelet gRPC probe cannot speak TLS, so the chart uses probes that work for every server image version and with or without TLS:

- **startup / liveness**: `GET /healthz` on the metrics port (plain HTTP, no credential), or a TCP check on the gRPC port when `server.metricsPort` is `0`;
- **readiness**: a TCP check on the gRPC port, so the Service only routes to a pod once the gRPC listener is bound (works with and without TLS).

The server also answers the standard `grpc.health.v1.Health` service without a credential (`SERVING` once the listener is up, `NOT_SERVING` while it shuts down), for `grpc-health-probe`, load balancers and service meshes that speak TLS. The image's own `HEALTHCHECK` uses it.

### TLS

| Parameter | Description | Default |
|-----------|-------------|---------|
| `tls.enabled` | Serve gRPC over TLS (1.3) | `false` |
| `tls.certFile` | Certificate path inside the container (with `existingSecret` it defaults to `/etc/chatcli/tls/tls.crt`) | `""` |
| `tls.keyFile` | Key path inside the container (with `existingSecret` it defaults to `/etc/chatcli/tls/tls.key`) | `""` |
| `tls.existingSecret` | Existing TLS Secret (e.g. from cert-manager), mounted read-only at `/etc/chatcli/tls` | `""` |

```yaml
tls:
  enabled: true
  existingSecret: chatcli-tls
```

`tls.enabled: true` with neither `tls.existingSecret` nor both `tls.certFile` and `tls.keyFile` fails the render, instead of starting a listener that would silently serve plaintext.

### LLM providers

| Parameter | Description | Default |
|-----------|-------------|---------|
| `llm.provider` | Default provider: `OPENAI`, `OPENAI_ASSISTANT`, `CLAUDEAI`, `BEDROCK`, `GOOGLEAI`, `XAI`, `ZAI`, `MINIMAX`, `MOONSHOT`, `STACKSPOT`, `OLLAMA`, `COPILOT`, `OPENROUTER` (empty = first provider with credentials). `DEVIN` is a local-CLI transport and is not available in the server image | `""` |
| `llm.model` | Default model | `""` |
| `secrets.existingSecret` | Use an existing Secret (loaded with `envFrom`) instead of creating one | `""` |
| `secrets.openaiApiKey` | `OPENAI_API_KEY` | `""` |
| `secrets.anthropicApiKey` | `ANTHROPIC_API_KEY` | `""` |
| `secrets.googleaiApiKey` | `GOOGLEAI_API_KEY` | `""` |
| `secrets.xaiApiKey` | `XAI_API_KEY` | `""` |
| `secrets.zaiApiKey` | `ZAI_API_KEY` (Zhipu AI) | `""` |
| `secrets.minimaxApiKey` | `MINIMAX_API_KEY` | `""` |
| `secrets.minimaxApiCompat` | `MINIMAX_API_COMPAT`: empty = native API, `anthropic` = Anthropic Messages API compatibility | `""` |
| `secrets.moonshotApiKey` | `MOONSHOT_API_KEY` (Kimi) | `""` |
| `secrets.openrouterApiKey` | `OPENROUTER_API_KEY` | `""` |
| `secrets.githubCopilotToken` | `GITHUB_COPILOT_TOKEN` | `""` |
| `secrets.stackspotClientId` | StackSpot client ID (`CLIENT_ID`) | `""` |
| `secrets.stackspotClientKey` | StackSpot client key (`CLIENT_KEY`) | `""` |
| `secrets.stackspotRealm` | `STACKSPOT_REALM` | `""` |
| `secrets.stackspotAgentId` | `STACKSPOT_AGENT_ID` | `""` |
| `secrets.awsAccessKeyId` | Bedrock static credentials (`AWS_ACCESS_KEY_ID`); leave empty for IRSA via `serviceAccount.annotations` | `""` |
| `secrets.awsSecretAccessKey` | `AWS_SECRET_ACCESS_KEY` | `""` |
| `secrets.awsSessionToken` | `AWS_SESSION_TOKEN` (STS / assumed role only) | `""` |
| `secrets.bedrockRegion` | `BEDROCK_REGION` (falls back to `AWS_REGION`) | `""` |
| `secrets.awsRegion` | `AWS_REGION` | `""` |
| `secrets.chatcliBedrockCaBundle` | `CHATCLI_BEDROCK_CA_BUNDLE`: path inside the pod to a PEM bundle (mount it with `extraVolumes`) | `""` |
| `secrets.chatcliBedrockInsecureSkipVerify` | `CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY`: `"true"` disables TLS verification (troubleshooting only) | `""` |

### Provider fallback chain

When the primary provider fails (rate limit, timeout, auth error, context overflow, model not found) the server tries the next provider, with exponential cooldown. The chain is **`llm.provider` followed by `fallback.providers`**: the chart puts `llm.provider` (with `llm.model`) in front unless you list it yourself, in which case your order is kept. Each entry runs its own `model` (`CHATCLI_FALLBACK_MODEL_<PROVIDER>`); an entry without one uses that provider's default model. The server installs the chain only when at least two of its providers have working credentials, and uses it for requests that bring no credentials or provider of their own. A non-empty `CHATCLI_FALLBACK_PROVIDERS` is what turns it on; the chart sets no separate enable variable.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `fallback.enabled` | Enable provider failover | `false` |
| `fallback.providers` | Ordered providers to try after `llm.provider`, each `{name, model}` | `[]` |
| `fallback.maxRetries` | Max retries (`CHATCLI_FALLBACK_MAX_RETRIES`; `0` = no retries) | `2` |
| `fallback.cooldownBase` | Base cooldown after a failure | `"30s"` |
| `fallback.cooldownMax` | Maximum cooldown | `"5m"` |

```yaml
llm:
  provider: CLAUDEAI
  model: claude-sonnet-4-6
secrets:
  anthropicApiKey: <your-anthropic-api-key>
  openaiApiKey: <your-openai-api-key>
  googleaiApiKey: <your-google-api-key>
fallback:
  enabled: true
  providers:            # effective chain: CLAUDEAI -> OPENAI -> GOOGLEAI
    - name: OPENAI
      model: gpt-4o
    - name: GOOGLEAI
      model: gemini-2.5-flash
```

### MCP (Model Context Protocol)

| Parameter | Description | Default |
|-----------|-------------|---------|
| `mcp.enabled` | Enable MCP integration | `false` |
| `mcp.servers` | Inline MCP servers (`name`, `transport` `stdio`/`sse`, `command`, `args`, `url`, `env`, `enabled`, `overrides`), rendered into a ConfigMap | `[]` |
| `mcp.existingConfigMap` | Existing ConfigMap with a `mcp_servers.json` key; mounted at `/etc/chatcli/mcp` and passed as `--mcp-config` (takes precedence over `servers`) | `""` |

```yaml
mcp:
  enabled: true
  servers:
    - name: filesystem
      transport: stdio
      command: npx
      args: ["-y", "@anthropic/mcp-server-filesystem", "/workspace"]
      env:
        LOG_LEVEL: info
    - name: web-search
      transport: sse
      url: "http://mcp-search:8080/sse"
      overrides: ["@webfetch", "@websearch"]   # built-ins this server replaces
```

### Kubernetes watcher

| Parameter | Description | Default |
|-----------|-------------|---------|
| `watcher.enabled` | Enable workload watching | `false` |
| `watcher.deployment` | Single-target mode: Deployment name (legacy) | `""` |
| `watcher.namespace` | Single-target mode: namespace (empty = the namespace named `default`) | `""` |
| `watcher.targets` | Multi-target list of `{deployment, kind, namespace, metricsPort, metricsPath, metricsFilter}` (takes precedence); `deployment` is the resource name and `kind` is `Deployment` (default), `StatefulSet`, `DaemonSet`, `Job` or `CronJob` | `[]` |
| `watcher.interval` | Collection interval | `"30s"` |
| `watcher.window` | Analysis time window | `"2h"` |
| `watcher.maxLogLines` | Max log lines per pod | `100` |
| `watcher.maxContextChars` | Budget for the LLM context (multi-target) | `32000` |

Targets in other namespaces (including a single-target `watcher.namespace` other than the release namespace) switch the chart's RBAC to a ClusterRole automatically.

```yaml
watcher:
  enabled: true
  targets:
    - deployment: api-gateway
      namespace: production
      metricsPort: 9090
      metricsPath: "/metrics"
      metricsFilter: ["http_requests_*", "http_request_duration_*"]
    - deployment: worker
      namespace: batch
    - deployment: nightly-report
      kind: CronJob
      namespace: batch
```

### Ollama and GitHub Copilot

| Parameter | Description | Default |
|-----------|-------------|---------|
| `ollama.enabled` | Enable the Ollama provider | `false` |
| `ollama.baseUrl` | Ollama API endpoint | `"http://ollama:11434"` |
| `ollama.model` | Ollama model | `""` |
| `copilot.model` | Copilot model | `""` |
| `copilot.maxTokens` | Max response tokens | `""` |
| `copilot.apiBaseUrl` | API URL override (enterprise) | `""` |

### Agents, skills, bootstrap, plugins

| Parameter | Description | Default |
|-----------|-------------|---------|
| `agents.enabled` | Mount agent definitions at `~/.chatcli/agents` | `false` |
| `agents.definitions` | Inline agent markdown files (key = filename) | `{}` |
| `agents.existingConfigMap` | Existing ConfigMap with agent `.md` files | `""` |
| `skills.enabled` | Mount skill definitions at `~/.chatcli/skills` | `false` |
| `skills.definitions` | Inline skill markdown files | `{}` |
| `skills.existingConfigMap` | Existing ConfigMap with skill `.md` files | `""` |
| `bootstrap.enabled` | Mount bootstrap files (SOUL.md, USER.md, IDENTITY.md, RULES.md, AGENTS.md) | `false` |
| `bootstrap.definitions` | Inline bootstrap files | `{}` |
| `bootstrap.existingConfigMap` | Existing ConfigMap with bootstrap files | `""` |
| `skillRegistry.enabled` | Configure the skill registries | `false` |
| `skillRegistry.registryUrls` | Comma-separated additional registry URLs | `""` |
| `skillRegistry.registryDisable` | Comma-separated registries to disable | `""` |
| `skillRegistry.installDir` | Skill install directory override | `""` |
| `plugins.enabled` | Mount a plugins directory at `~/.chatcli/plugins` | `false` |
| `plugins.initImage` | Init container image whose `/plugins/*` is copied into the plugins directory | `""` |
| `plugins.existingPVC` | Existing PVC with pre-installed plugins (instead of an emptyDir) | `""` |

`agents`, `skills` and `bootstrap` mount a ConfigMap only when there is one: inline `definitions` (rendered as `<fullname>-agents`, `-skills`, `-bootstrap`) or an `existingConfigMap`. Enabled with neither, nothing is mounted and the server finds no files in that directory (earlier charts mounted a ConfigMap that did not exist, and the pod stayed in `ContainerCreating`).

The server reads the MCP, agents, skills and bootstrap ConfigMaps only at startup. Each one the chart renders (from `mcp.servers` or `*.definitions`) is hashed into a `checksum/<name>` pod annotation, so a `helm upgrade` that edits it rolls the pods; the watcher config (`watcher.targets`) rides `checksum/config`. A ConfigMap you manage yourself (`*.existingConfigMap`) cannot be hashed by Helm: after editing one, restart the pods with `kubectl -n <namespace> rollout restart deploy/<fullname>`.

```yaml
bootstrap:
  enabled: true
  definitions:
    SOUL.md: |
      You are a DevOps assistant specialized in Kubernetes troubleshooting.
agents:
  enabled: true
  definitions:
    security-auditor.md: |
      ---
      name: security-auditor
      description: Kubernetes security audit agent
      ---
      You are a security auditor for Kubernetes clusters...
```

### Storage and pipeline

| Parameter | Description | Default |
|-----------|-------------|---------|
| `persistence.enabled` | PVC `<fullname>-sessions` for sessions (see the note on rollouts below) | `true` |
| `persistence.storageClass` | StorageClass (empty = cluster default, `-` = `storageClassName: ""`) | `""` |
| `persistence.accessModes` | PVC access modes. Without `ReadWriteMany` a rollout stops the old pod before starting the new one (see below); `ReadWriteOncePod` fails the render with more than one replica | `["ReadWriteOnce"]` |
| `persistence.size` | PVC size | `1Gi` |
| `strategy` | Deployment update strategy, rendered as written; empty = chosen from persistence (see below) | `{}` |
| `memory.enabled` | Long-term memory at `~/.chatcli/memory` (on the sessions PVC when persistence is on, a 200Mi emptyDir otherwise) | `false` |
| `memory.subPath` | Directory of the sessions PVC that holds memory when persistence is on; memory written at the PVC root by older charts is copied into it once, automatically (see below). `""` = the PVC root, shared with the session files (the layout of charts up to 1.211.2) | `memory` |
| `pipeline.enabled` | Host the full turn engine behind the `ChatTurn`, `RunCoder`, `RunAgent` and tool RPCs (`CHATCLI_SERVER_PIPELINE`); exec RPCs require an admin caller; exclusive with the co-located gateway | `false` |

**Rollouts.** With `persistence.enabled` and no `ReadWriteMany` access mode, a rollout stops the old pod before starting the new one: the chart renders `RollingUpdate` with `maxSurge: 0` and `maxUnavailable: 1`. Under the API server default (`maxSurge` 25% = one extra pod, `maxUnavailable` 25% = none), a new pod scheduled on another node cannot attach the `ReadWriteOnce` volume while the old pod holds it, and the old pod is only stopped once the new one is ready, so the rollout never finished. Stopping the old pod first means a short gap in service on every rollout. Without persistence, or with `ReadWriteMany`, nothing is rendered and the API server default applies, as before.

This has the effect of the `Recreate` strategy the operator gives an `Instance` with persistence, but it is not `type: Recreate` on purpose: switching a live Deployment to `Recreate` has to remove its defaulted `rollingUpdate` block, which Helm 4's server-side apply cannot do while no field manager owns it, so an upgrade from a chart that rendered no strategy fails with `spec.strategy.rollingUpdate: Forbidden: may not be specified when strategy type is 'Recreate'`. The stop-first `RollingUpdate` applies under Helm 3 and Helm 4 alike.

An explicit `strategy` wins in every case, and `type: Recreate` without `rollingUpdate` is rendered with `rollingUpdate: null`. A fresh install, a Helm 3 release and a release already upgraded once on this chart's default can move to it directly; a release installed by Helm 4 on a chart up to 1.211.2 needs one upgrade on the default first (or `kubectl patch deploy/<fullname> --type=json -p '[{"op":"remove","path":"/spec/strategy/rollingUpdate"},{"op":"replace","path":"/spec/strategy/type","value":"Recreate"}]'`).

A `ReadWriteOnce` volume attaches to one node at a time, so every replica must run on that node: with `replicaCount` or an HPA above 1, pods scheduled elsewhere stay in `ContainerCreating`. Run one replica or use `ReadWriteMany` for several. `ReadWriteOncePod` admits a single pod, and the render fails when `replicaCount` (or `autoscaling.maxReplicas` with the HPA on) is above 1.

**Memory on the sessions PVC.** With `memory.enabled` and persistence, the sessions stay at the PVC root (mounted at `~/.chatcli/sessions`) and memory lives in the `memory.subPath` directory of the same PVC (mounted at `~/.chatcli/memory`). kubelet creates that directory on first mount; the default `podSecurityContext.fsGroup` makes it writable by the server. Charts up to 1.211.2 mounted memory at the PVC root too, so its JSON stores sat among the session files: the session list showed them, and session expiry could delete them.

Upgrading an install that ran with `memory.enabled` and persistence migrates automatically. No session moves, and the first time the server opens memory on the new layout it copies memory's own files from the PVC root into the memory directory: the chart sets `CHATCLI_MEMORY_LEGACY_DIR=/home/chatcli/.chatcli/sessions` (the PVC root as the server sees it) whenever persistence, memory and a `memory.subPath` are all on. The copy:

- takes only memory's files (`MEMORY.md` and its backups, `memory_index.json`, `memory_tombstones.json`, `episodes.json`, `user_profile.json`, `topics.json`, `projects.json`, `usage_stats.json`, `graph.json`, `vector_index.json`, `memory_archive.json`, `compactor_state.json`, their `.corrupt` quarantines, the `YYYYMM/` daily notes, `weekly/`, `monthly/` and `pending/`); the session files stay where they are;
- never moves, deletes or rewrites anything at the root, and never overwrites a file already in the memory directory; each file lands atomically with mode 0600;
- runs only while the memory directory holds none of memory's files, and writes `.migrated-from-legacy` there when done, so it happens once;
- skips an unreadable file with a warning; a file sealed with `CHATCLI_ENCRYPTION_KEY` is resealed for its new path, and while the key is missing or wrong the copy stays pending (`.migrating-from-legacy`) and resumes on the next start;
- logs what it copied (`memory: adopted the legacy memory directory`).

`memory.subPath: ""` keeps the old shared layout instead (no copy, no `CHATCLI_MEMORY_LEGACY_DIR`). An upgrade with plain `--reuse-values` carries no `memory.subPath` key and keeps the old layout too.

### Service, ingress, network policy

| Parameter | Description | Default |
|-----------|-------------|---------|
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `50051` |
| `service.headless` | Headless Service for gRPC client-side load balancing (recommended with `replicaCount > 1`) | `false` |
| `ingress.enabled` | Create an Ingress (with `className: nginx` the chart adds `backend-protocol: GRPC` and `ssl-redirect: "true"`; the same keys in `ingress.annotations` win, e.g. `backend-protocol: GRPCS` for a TLS listener) | `false` |
| `ingress.className` | Ingress class | `""` |
| `ingress.annotations` | Ingress annotations | `{}` |
| `ingress.hosts` | Hosts and paths | `chatcli.local`, `/` |
| `ingress.tls` | Ingress TLS | `[]` |
| `networkPolicy.enabled` | NetworkPolicy allowing ingress to the gRPC and metrics ports | `false` |
| `networkPolicy.ingressFrom` | NetworkPolicyPeer list allowed to reach those ports (empty = any source) | unset |
| `networkPolicy.egress` | `allowAll`, or `restricted` (DNS, 443, `kubernetesApiPort`, `egressExtraPorts`) | `allowAll` |
| `networkPolicy.kubernetesApiPort` | Kubernetes API port for restricted egress | `6443` |
| `networkPolicy.egressExtraPorts` | Extra `{port, protocol}` for restricted egress | unset |

### RBAC and service account

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceAccount.create` | Create a ServiceAccount | `true` |
| `serviceAccount.name` | ServiceAccount name override | `""` |
| `serviceAccount.annotations` | Annotations (IRSA / Workload Identity) | `{}` |
| `rbac.create` | Create RBAC for the watcher (no access to Secrets: the server never reads one through the API) | `true` |
| `rbac.clusterWide` | ClusterRole instead of a namespaced Role | `false` |
| `rbac.additionalRules` | Extra RBAC rules (e.g. Secret access for a plugin that needs it, scoped with `resourceNames`) | `[]` |

### Security hardening

| Parameter | Description | Default |
|-----------|-------------|---------|
| `security.jwtSecret` | HS256 JWT secret, inline (`CHATCLI_JWT_SECRET`); exclusive with `jwtSecretRef` (setting both fails the render) | `""` |
| `security.jwtSecretRef` | `{name, key}` of a Secret holding the JWT secret (recommended); exclusive with `jwtSecret` | `{}` |
| `security.jwtPublicKey` | RSA public key (PEM or path) -- selects RS256 | `""` |
| `security.jwtPublicKeyRef` | `{name, key}` of a Secret holding the RSA public key | `{}` |
| `security.jwtIssuer` | Expected `iss` claim (empty skips the check) | `""` |
| `security.jwtAudience` | Expected `aud` claim (empty skips the check) | `""` |
| `security.tlsClientCA` | Path of the CA bundle client certificates are verified against (mTLS). Requires `tls.enabled` (with `tls.existingSecret` or both paths); then every connection needs a client certificate. Use `/etc/chatcli/tls/ca.crt` from `tls.existingSecret`, or mount one with `extraVolumes` | `""` |
| `security.mtlsRole` | Role for callers identified by a client certificate alone: `viewer`/`readonly`, `user`/`operator`, `admin` (an unknown value means `readonly`) | `""` (user) |
| `security.rateLimitRps` | Per-client requests/second | `""` (10) |
| `security.rateLimitBurst` | Per-client burst | `""` (20) |
| `security.maxRecvMsgSize` | Max gRPC receive message size (bytes) | `""` (50MB) |
| `security.maxSendMsgSize` | Max gRPC send message size (bytes) | `""` (50MB) |
| `security.maxConcurrentStreams` | Max concurrent gRPC streams | `""` (100) |
| `security.bindAddress` | Bind address | `""` (0.0.0.0 in Kubernetes) |
| `security.auditLogPath` | Absolute path of the hash-chained JSON-lines audit log; must be writable (the root filesystem is read-only) | `""` |
| `security.debug` | Stack traces in error logs | `false` |
| `security.agentSecurityMode` | Agent command validation: `strict` or `permissive` | `""` (strict) |
| `security.sessionTTL` | Session expiry in days | `""` (90) |
| `security.envRedactMode` | Env var redaction: `off`, `permissive` or `strict` | `""` (permissive) |
| `security.allowUnsignedPlugins` | Allow unsigned plugins (dev only) | `false` |
| `security.allowInsecure` | Sets `CHATCLI_ALLOW_INSECURE`, which only the chatcli client reads; it does not change the server listener | `false` |
| `security.encryptionKey` | Session encryption key, inline (prefer `extraEnv` with a `secretKeyRef`) | `""` |
| `extraEnv` | Extra environment variables; an entry for a `CHATCLI_LOG_*` variable replaces the chart's `logging` value | `[]` |
| `extraVolumes` | Extra pod volumes | `[]` |
| `extraVolumeMounts` | Extra mounts for the server container | `[]` |

**Example: JWT from a Secret, mTLS with a separate client CA, audit log on a volume**

```yaml
tls:
  enabled: true
  existingSecret: chatcli-tls
  certFile: /etc/chatcli/tls/tls.crt
  keyFile: /etc/chatcli/tls/tls.key
security:
  jwtSecretRef:
    name: chatcli-jwt
    key: secret
  tlsClientCA: /etc/chatcli/client-ca/ca.crt
  mtlsRole: viewer
  rateLimitRps: 20
  rateLimitBurst: 50
  auditLogPath: /var/log/chatcli/audit.jsonl
extraVolumes:
  - name: client-ca
    secret:
      secretName: chatcli-client-ca
  - name: audit
    emptyDir: {}
extraVolumeMounts:
  - name: client-ca
    mountPath: /etc/chatcli/client-ca
    readOnly: true
  - name: audit
    mountPath: /var/log/chatcli
```

### Logs

Inside a container the server writes JSON log lines to **stderr** (so `kubectl logs` and log collectors see them) as well as a rotated file, `/home/chatcli/.chatcli/app.log`. With the default read-only root filesystem that directory is an `emptyDir` limited to 200Mi, and a volume past its limit gets the pod evicted. The server's own rotation defaults (100 MB, 3 backups: up to 400 MB) do not fit, so the chart sets a rotation that does:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `logging.maxSizeMB` | `CHATCLI_LOG_MAX_SIZE_MB`: size at which the file rotates | `20` |
| `logging.maxBackups` | `CHATCLI_LOG_MAX_BACKUPS`: rotated files kept | `3` |
| `logging.maxAgeDays` | `CHATCLI_LOG_MAX_AGE_DAYS`: days a rotated file is kept | `28` |
| `logging.compress` | `CHATCLI_LOG_COMPRESS`: gzip rotated files | `true` |

That is at most 80 MB of log. While the data emptyDir is in use, the render fails when `maxSizeMB x (maxBackups + 1)` exceeds 100 MB, leaving the rest of the volume to the other state kept there. A field set to `null` renders no variable (server default, counted as such by the check). An `extraEnv` entry for one of these variables replaces the chart's value, and an `extraEnv` entry for the size or backups skips the check (the explicit escape hatch):

```yaml
logging:
  maxSizeMB: 10
  maxBackups: 5
extraEnv:
  - name: LOG_LEVEL              # debug | info | warn | error
    value: info
```

`CHATCLI_LOG_STDERR=false` turns the stderr stream off, `true` forces it on outside a container.

### Autoscaling and availability

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Replicas | `1` |
| `autoscaling.enabled` | Create an HPA | `false` |
| `autoscaling.minReplicas` | Min replicas | `1` |
| `autoscaling.maxReplicas` | Max replicas | `5` |
| `autoscaling.targetCPUUtilizationPercentage` | Target CPU utilization | `80` |
| `autoscaling.targetMemoryUtilizationPercentage` | Optional target memory utilization | unset |
| `podDisruptionBudget.enabled` | Create a PDB (only rendered when `replicaCount > 1`) | `false` |
| `podDisruptionBudget.minAvailable` | Min available pods | `1` |
| `podDisruptionBudget.maxUnavailable` | Max unavailable pods; used only when `minAvailable` is `0` or removed (`--set podDisruptionBudget.minAvailable=null`) | unset |

With `persistence.enabled` and the default `ReadWriteOnce` PVC, more than one replica needs a `ReadWriteMany` storage class (see **Rollouts** under Storage and pipeline; `ReadWriteOncePod` fails the render).

### Monitoring

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceMonitor.enabled` | Prometheus Operator ServiceMonitor for the metrics port | `false` |
| `serviceMonitor.interval` | Scrape interval | `"30s"` |
| `serviceMonitor.scrapeTimeout` | Scrape timeout | `""` |
| `serviceMonitor.labels` | Additional labels | `{}` |
| `prometheusUrl` | Deprecated and ignored by the server; set it on the `chatcli-operator` chart | `""` |

The metrics endpoint is plain HTTP without authentication on every interface of the pod; restrict it with `networkPolicy.ingressFrom` where that matters.

### Pod configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Server image | `ghcr.io/diillson/chatcli` |
| `image.tag` | Image tag (defaults to the chart `appVersion`) | `""` |
| `image.pullPolicy` | Pull policy | `IfNotPresent` |
| `imagePullSecrets` | Image pull secrets | `[]` |
| `nameOverride` / `fullnameOverride` | Name overrides | `""` |
| `resources` | Container resources | `100m/128Mi` requests, `500m/512Mi` limits |
| `podSecurityContext` | Pod security context | `runAsNonRoot`, UID/GID/fsGroup `1000`, `RuntimeDefault` seccomp |
| `securityContext` | Container security context (also applied to the `plugin-loader` init container) | no privilege escalation, read-only root FS, drop `ALL` |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |

### CRD upgrade hook

| Parameter | Description | Default |
|-----------|-------------|---------|
| `crdUpgrade.enabled` | Pre-install/pre-upgrade Job that re-applies the chart's CRDs | `true` |
| `crdUpgrade.image.repository` | kubectl image | `registry.k8s.io/kubectl` |
| `crdUpgrade.image.tag` | kubectl tag (full patch tag) | `v1.31.10` |
| `crdUpgrade.image.pullPolicy` | Pull policy | `IfNotPresent` |
| `crdUpgrade.resources` | Hook Job resources | `50m/64Mi` requests, `200m/128Mi` limits |
| `crdUpgrade.tolerations` | Hook Job tolerations | `[]` |
| `crdUpgrade.nodeSelector` | Hook Job node selector | `{}` |

## CRDs

This chart installs the 17 Custom Resource Definitions of the AIOps platform (the same set as the `chatcli-operator` chart):

| CRD | Short Name | Description |
|-----|------------|-------------|
| `AIInsight` | `ai` | AI-generated root cause analysis and recommendations |
| `Anomaly` | `anom` | Raw signal from watchers before correlation into issues |
| `ApprovalPolicy` | `ap` | Approval requirements for remediation (auto/manual/quorum) |
| `ApprovalRequest` | `ar` | Pending approval with blast radius assessment |
| `AuditEvent` | `ae` | Audit trail of platform actions |
| `ChaosExperiment` | `chaos` | Chaos engineering experiments (5 runnable types; network faults and schedules are rejected) |
| `ClusterRegistration` | `cr` | Multi-cluster federation registration |
| `EscalationPolicy` | `ep` | L1 -> L2 -> L3 escalation chains for incidents |
| `IncidentSLA` | `sla` | SLA targets for incident response and resolution by severity |
| `Instance` | `inst` | A ChatCLI server provisioned by the operator |
| `Issue` | `iss` | Correlated operational problem detected in the cluster |
| `NotificationPolicy` | `np` | Multi-channel notification rules |
| `PostMortem` | `pm` | Auto-generated post-incident report |
| `RemediationPlan` | `rp` | Remediation plan (55 action types) |
| `Runbook` | `rb` | Operational procedures linked to issue types |
| `ServiceLevelObjective` | `slo` | SLO with burn rate alerting and error budgets |
| `SourceRepository` | `srcrepo` | Links workloads to source code for code-aware analysis |

> **Note:** If both charts are installed in the same cluster, keep them on the same version: each chart's hook re-applies its own copy of the CRDs on install and upgrade.

## Upgrading

```bash
helm upgrade chatcli oci://ghcr.io/diillson/charts/chatcli \
  --version <version> \
  --namespace chatcli \
  --reset-then-reuse-values
```

`--reset-then-reuse-values` (Helm 3.14+) starts from the new chart's defaults and re-applies your previous overrides. Plain `--reuse-values` re-applies the values stored in the release and skips the defaults of keys added by newer chart versions, so those keys render as if unset (or fail a template that expects them); with an older Helm, pass your values file (`-f my-values.yaml`) instead. The CRD hook refreshes the CRDs first.

## Uninstalling

```bash
helm uninstall chatcli -n chatcli
```

> **Note:** `helm uninstall` also deletes the sessions PVC (`<fullname>-sessions`) -- back it up first if the sessions matter. Helm does not remove CRDs; deleting them deletes every resource of those kinds in the cluster:
> ```bash
> kubectl get crd -o name | grep platform.chatcli.io | xargs kubectl delete
> ```

## Documentation

- Server mode: [chatcli.edilsonfreitas.com/features/server-mode](https://chatcli.edilsonfreitas.com/features/server-mode)
- Full documentation: [chatcli.edilsonfreitas.com](https://chatcli.edilsonfreitas.com)
