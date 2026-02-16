# ChatCLI Kubernetes Operator

Kubernetes operator for managing ChatCLI server instances via Custom Resource Definition (CRD).

## Features

- Declarative management of ChatCLI server instances
- Multi-target K8s Watcher with Prometheus metrics scraping
- Automatic RBAC (Role for single-namespace, ClusterRole for multi-namespace)
- Session persistence via PVC
- TLS and token-based authentication
- Security-hardened pods (nonroot, read-only rootfs, seccomp)

## Quick Start

```bash
# Install CRD
kubectl apply -f config/crd/bases/chatcli.diillson.com_chatcliinstances.yaml

# Install RBAC + Manager
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/manager/manager.yaml

# Create API Keys secret
kubectl create secret generic chatcli-api-keys \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-xxx

# Apply a ChatCLIInstance
kubectl apply -f config/samples/chatcli_v1alpha1_chatcliinstance.yaml
```

## CRD Example (Multi-Target with Prometheus)

```yaml
apiVersion: chatcli.diillson.com/v1alpha1
kind: ChatCLIInstance
metadata:
  name: chatcli-prod
spec:
  provider: CLAUDEAI
  apiKeys:
    name: chatcli-api-keys
  watcher:
    enabled: true
    interval: "30s"
    maxContextChars: 32000
    targets:
      - deployment: api-gateway
        namespace: production
        metricsPort: 9090
        metricsFilter: ["http_requests_*", "http_request_duration_*"]
      - deployment: auth-service
        namespace: production
        metricsPort: 9090
      - deployment: worker
        namespace: batch
```

## Resources Created

| Resource | Name | Description |
|----------|------|-------------|
| Deployment | `<name>` | ChatCLI server pods |
| Service | `<name>` | ClusterIP for gRPC |
| ConfigMap | `<name>` | Environment variables |
| ConfigMap | `<name>-watch-config` | Multi-target watch YAML |
| ServiceAccount | `<name>` | Pod identity |
| Role/ClusterRole | `<name>-watcher` | K8s watcher permissions |
| PVC | `<name>-sessions` | Session persistence (optional) |

## Development

```bash
# Build
make build

# Test (23 tests)
make test

# Docker
make docker-build IMG=ghcr.io/diillson/chatcli-operator:latest

# Install CRD
make install

# Deploy
make deploy IMG=ghcr.io/diillson/chatcli-operator:latest
```

## Documentation

Full documentation at [diillson.github.io/chatcli/docs/features/k8s-operator](https://diillson.github.io/chatcli/docs/features/k8s-operator/)
