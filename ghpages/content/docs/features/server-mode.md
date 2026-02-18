+++
title = "Modo Servidor (chatcli serve)"
linkTitle = "Modo Servidor"
weight = 60
description = "Transforme o ChatCLI em um servidor gRPC para acesso remoto de qualquer terminal, Docker ou Kubernetes."
icon = "dns"
+++

O **Modo Servidor** transforma o ChatCLI em um serviço gRPC de alta performance que pode ser acessado remotamente por qualquer terminal. Isso permite centralizar o acesso a IA em um servidor (bare-metal, VM, Docker ou Kubernetes) e conectar de qualquer lugar.

---

## Por que usar o Modo Servidor?

- **Centralização**: Um único servidor com API keys configuradas atende múltiplos clientes
- **Segurança**: As chaves de API ficam no servidor, nunca expostas nos terminais clientes
- **Flexibilidade**: Clientes podem usar suas **próprias credenciais** (API key ou OAuth) se desejarem
- **Performance**: Comunicação via gRPC com suporte a TLS e streaming progressivo
- **Kubernetes-Ready**: Integração nativa com o K8s Watcher para monitoramento de deployments

---

## Iniciando o Servidor

O comando `chatcli serve` inicia o servidor gRPC:

```bash
# Modo mais simples: servidor na porta padrão (50051)
chatcli serve

# Com porta e autenticação customizados
chatcli serve --port 8080 --token meu-token-secreto

# Com TLS habilitado
chatcli serve --tls-cert cert.pem --tls-key key.pem

# Com K8s Watcher integrado (single-target)
chatcli serve --watch-deployment myapp --watch-namespace production

# Com K8s Watcher multi-target + Prometheus metrics
chatcli serve --watch-config targets.yaml
```

---

## Flags Disponíveis

| Flag | Descrição | Padrão | Env Var |
|------|-----------|--------|---------|
| `--port` | Porta do servidor gRPC | `50051` | `CHATCLI_SERVER_PORT` |
| `--token` | Token de autenticação (vazio = sem auth) | `""` | `CHATCLI_SERVER_TOKEN` |
| `--tls-cert` | Arquivo de certificado TLS | `""` | `CHATCLI_SERVER_TLS_CERT` |
| `--tls-key` | Arquivo de chave TLS | `""` | `CHATCLI_SERVER_TLS_KEY` |
| `--provider` | Provedor de LLM padrão | Auto-detectado | `LLM_PROVIDER` |
| `--model` | Modelo de LLM padrão | Auto-detectado | |
| `--metrics-port` | Porta HTTP para métricas Prometheus (0 = desabilita) | `9090` | `CHATCLI_METRICS_PORT` |

### Prometheus Metrics

O servidor expoe métricas Prometheus em `http://localhost:9090/metrics` por padrão. As métricas incluem:

- **gRPC**: `chatcli_grpc_requests_total`, `chatcli_grpc_request_duration_seconds`, `chatcli_grpc_in_flight_requests`
- **LLM**: `chatcli_llm_requests_total`, `chatcli_llm_request_duration_seconds`, `chatcli_llm_errors_total`
- **Watcher**: `chatcli_watcher_collection_duration_seconds`, `chatcli_watcher_alerts_total`, `chatcli_watcher_pods_ready`
- **Session**: `chatcli_session_active_total`, `chatcli_session_operations_total`
- **Server**: `chatcli_server_uptime_seconds`, `chatcli_server_info`
- **Go runtime**: goroutines, memória, GC (via GoCollector/ProcessCollector)

Para desabilitar, use `--metrics-port 0`.

### Variáveis de Segurança

| Env Var | Descrição | Padrão |
|---------|-----------|--------|
| `CHATCLI_GRPC_REFLECTION` | Habilita gRPC reflection para debugging. **Mantenha desabilitado em produção.** | `false` |
| `CHATCLI_DISABLE_VERSION_CHECK` | Desabilita verificação automática de versão no startup. | `false` |

> O gRPC reflection esta **desabilitado por padrão** para não expor o schema do serviço em produção. Habilite apenas para debugging local. Veja a [documentação de segurança](/docs/features/security/) para todas as medidas de hardening.

### Flags do K8s Watcher (opcionais)

| Flag | Descrição | Padrão | Env Var |
|------|-----------|--------|---------|
| `--watch-config` | Arquivo YAML multi-target | `""` | `CHATCLI_WATCH_CONFIG` |
| `--watch-deployment` | Deployment único (legado) | `""` | `CHATCLI_WATCH_DEPLOYMENT` |
| `--watch-namespace` | Namespace do deployment | `"default"` | `CHATCLI_WATCH_NAMESPACE` |
| `--watch-interval` | Intervalo de coleta | `30s` | `CHATCLI_WATCH_INTERVAL` |
| `--watch-window` | Janela de observação | `2h` | `CHATCLI_WATCH_WINDOW` |
| `--watch-max-log-lines` | Max linhas de log por pod | `100` | `CHATCLI_WATCH_MAX_LOG_LINES` |
| `--watch-kubeconfig` | Caminho do kubeconfig | Auto-detectado | `CHATCLI_KUBECONFIG` |

> Use `--watch-config` para monitorar **múltiplos deployments** simultaneamente com métricas Prometheus. Veja [K8s Watcher](/docs/features/k8s-watcher/) para o formato do arquivo YAML.

---

## Autenticação do Servidor

### Sem Autenticação

Por padrão, o servidor não exige autenticação. Qualquer cliente pode conectar:

```bash
chatcli serve  # sem --token = acesso livre
```

### Com Token

Defina um token para proteger o servidor:

```bash
# Via flag
chatcli serve --token meu-token-secreto

# Via variável de ambiente
export CHATCLI_SERVER_TOKEN=meu-token-secreto
chatcli serve
```

O cliente precisa fornecer o mesmo token ao conectar:

```bash
chatcli connect servidor:50051 --token meu-token-secreto
```

### TLS (HTTPS)

Para conexões encriptadas, forneca certificado e chave TLS:

```bash
chatcli serve --tls-cert /path/to/cert.pem --tls-key /path/to/key.pem
```

O cliente usa a flag `--tls` e opcionalmente `--ca-cert`:

```bash
chatcli connect servidor:50051 --tls --ca-cert /path/to/ca.pem
```

---

## Modos de Credencial

O servidor suporta múltiplos modos de credencial LLM, dando flexibilidade total:

### 1. Credenciais do Servidor (Padrão)

O servidor usa suas próprias API keys configuradas via variáveis de ambiente:

```bash
export OPENAI_API_KEY=sk-xxx
export LLM_PROVIDER=OPENAI
chatcli serve
```

Nenhuma configuração adicional necessária no cliente.

### 2. Credenciais do Cliente (API Key)

O cliente pode enviar sua própria API key, que o servidor usa em vez das suas:

```bash
# Cliente envia sua própria chave
chatcli connect servidor:50051 --llm-key sk-minha-chave --provider OPENAI
```

### 3. Credenciais do Cliente (OAuth Local)

O cliente pode usar tokens OAuth do auth store local (`~/.chatcli/auth-profiles.json`):

```bash
# Primeiro, faca login OAuth localmente
/auth login anthropic

# Depois, conecte usando as credenciais locais
chatcli connect servidor:50051 --use-local-auth
```

### 4. Credenciais StackSpot

Para o provedor StackSpot, envie as credenciais completas:

```bash
chatcli connect servidor:50051 --provider STACKSPOT \
  --client-id <id> --client-key <key> --realm <realm> --agent-id <agent>
```

### 5. Ollama (Sem Credenciais)

Para modelos locais via Ollama, basta informar a URL:

```bash
chatcli connect servidor:50051 --provider OLLAMA --ollama-url http://gpu-server:11434
```

---

## Arquitetura gRPC

O servidor implementa um serviço gRPC com os seguintes RPCs:

| RPC | Descrição |
|-----|-----------|
| `SendPrompt` | Envia um prompt e recebe a resposta completa |
| `StreamPrompt` | Envia um prompt e recebe a resposta em chunks progressivos |
| `InteractiveSession` | Streaming bidirecional para sessões interativas |
| `ListSessions` | Lista sessões salvas no servidor |
| `LoadSession` | Carrega uma sessão salva |
| `SaveSession` | Salva a sessão atual |
| `Health` | Health check do servidor |
| `GetServerInfo` | Informações do servidor (versão, provider, modelo, watcher) |
| `GetWatcherStatus` | Status do K8s Watcher (se ativo) |
| `GetAlerts` | Retorna alertas ativos do K8s Watcher (usado pelo Operator) |
| `AnalyzeIssue` | Envia contexto de um Issue ao LLM e retorna análise + ações sugeridas |

### gRPC com Múltiplas Réplicas

O gRPC usa conexões HTTP/2 persistentes que, por padrão, fixam em um único pod via kube-proxy. Para cenários com múltiplas réplicas no Kubernetes:

- **1 réplica**: Service ClusterIP padrão — sem configuração extra necessária
- **Múltiplas réplicas**: Use um Service headless (`ClusterIP: None`) para que o DNS retorne os IPs individuais dos pods, habilitando balanceamento round-robin client-side via resolver `dns:///` do gRPC
- O client do ChatCLI já possui **keepalive** (ping a cada 10s) e suporte a **round-robin** integrados
- No Helm chart, habilite `service.headless: true` quando `replicaCount > 1`
- No Operator, o headless é ativado **automaticamente** quando `spec.réplicas > 1`

> Para mais detalhes, veja a [documentação do K8s Operator](/docs/features/k8s-operator/) e o [deploy com Helm](/docs/getting-started/docker-deployment/).

### Streaming Progressivo

O RPC `StreamPrompt` divide a resposta em chunks de ~200 caracteres em fronteiras naturais (parágrafos, linhas, frases), proporcionando uma experiência de resposta progressiva no cliente.

### RPCs da Plataforma AIOps

Os RPCs `GetAlerts` e `AnalyzeIssue` são usados pelo [Operator AIOps](/docs/features/k8s-operator/) para alimentar o pipeline autônomo de remediação.

#### GetAlerts

Retorna os alertas ativos detectados pelo K8s Watcher:

```protobuf
rpc GetAlerts(GetAlertsRequest) returns (GetAlertsResponse);

message GetAlertsRequest {
  string namespace = 1;     // Filtrar por namespace (vazio = todos)
  string deployment = 2;    // Filtrar por deployment (vazio = todos)
}

message AlertInfo {
  string alert_type = 1;    // HighRestartCount, OOMKilled, PodNotReady, DeploymentFailing
  string deployment = 2;
  string namespace = 3;
  string message = 4;
  string severity = 5;      // critical, warning
  int64 timestamp = 6;
}
```

O handler itera sobre os `ObservabilityStore` de cada target do MultiWatcher e retorna alertas ativos. Suporta filtragem por namespace e deployment.

#### AnalyzeIssue

Envia o contexto de um Issue ao LLM e retorna análise estruturada com ações sugeridas:

```protobuf
rpc AnalyzeIssue(AnalyzeIssueRequest) returns (AnalyzeIssueResponse);

message AnalyzeIssueRequest {
  string issue_name = 1;
  string namespace = 2;
  string resource_kind = 3;
  string resource_name = 4;
  string signal_type = 5;
  string severity = 6;
  string description = 7;
  int32 risk_score = 8;
  string provider = 9;
  string model = 10;
}

message SuggestedAction {
  string name = 1;          // "Restart deployment"
  string action = 2;        // "RestartDeployment"
  string description = 3;   // "Restart pods to reclaim leaked memory"
  map<string, string> params = 4;
}

message AnalyzeIssueResponse {
  string analysis = 1;
  float confidence = 2;     // 0.0-1.0
  repeated string recommendations = 3;
  string provider = 4;
  string model = 5;
  repeated SuggestedAction suggested_actions = 6;
}
```

O handler constroi um prompt estruturado que inclui a lista de ações válidas (`ScaleDeployment`, `RestartDeployment`, `RollbackDeployment`, `PatchConfig`) e solicita resposta em JSON. O parsing suporta remoção de markdown codeblocks, clamp de confidence e fallback em caso de erro.

---

## Integração com K8s Watcher

Quando o servidor e iniciado com `--watch-config` ou `--watch-deployment`, o K8s Watcher monitora continuamente os deployments e **injeta automaticamente o contexto Kubernetes em todos os prompts** dos clientes remotos.

### Single-Target (legado)

```bash
chatcli serve --watch-deployment myapp --watch-namespace production
```

### Multi-Target (recomendado)

```bash
chatcli serve --watch-config targets.yaml
```

O arquivo `targets.yaml` define múltiplos deployments, métricas Prometheus e budget de contexto. Veja [K8s Watcher](/docs/features/k8s-watcher/#arquivo-de-configuração-multi-target) para o formato completo.

Qualquer usuário conectado pode fazer perguntas sobre os deployments sem configuração adicional:

```
Conectado ao ChatCLI server (version: 1.0.0, provider: OPENAI, model: gpt-4o)
K8s watcher active: 5 targets (interval: 30s)

> Quais deployments precisam de atenção?
> Analise as métricas HTTP do api-gateway
```

O servidor injeta automaticamente informações de pods, eventos, logs, métricas de infra e **métricas Prometheus de aplicação** no prompt antes de enviar ao LLM. O **MultiSummarizer** gerencia o budget de contexto, priorizando targets com problemas.

Para verificar o status do watcher remotamente, use `/watch` no cliente conectado.

---

## Variáveis de Ambiente

Todas as variáveis de ambiente usadas pelo ChatCLI local também funcionam no servidor:

```bash
# Servidor
CHATCLI_SERVER_PORT=50051
CHATCLI_SERVER_TOKEN=meu-token
CHATCLI_SERVER_TLS_CERT=/path/to/cert.pem
CHATCLI_SERVER_TLS_KEY=/path/to/key.pem

# Segurança
CHATCLI_GRPC_REFLECTION=false          # true apenas para debug local
CHATCLI_DISABLE_VERSION_CHECK=false    # true para ambientes air-gapped

# LLM (o servidor usa estas para processar requests)
LLM_PROVIDER=CLAUDEAI
ANTHROPIC_API_KEY=sk-ant-xxx
ANTHROPIC_MODEL=claude-sonnet-4-5

# K8s Watcher (opcional)
CHATCLI_WATCH_DEPLOYMENT=myapp
CHATCLI_WATCH_NAMESPACE=production
CHATCLI_WATCH_INTERVAL=30s
CHATCLI_WATCH_WINDOW=2h
CHATCLI_WATCH_MAX_LOG_LINES=100
```

---

## Próximo Passo

- [Conectar ao servidor remotamente](/docs/features/remote-connect/)
- [K8s Watcher (multi-target + Prometheus)](/docs/features/k8s-watcher/)
- [K8s Operator (AIOps)](/docs/features/k8s-operator/)
- [AIOps Platform (deep-dive)](/docs/features/aiops-platform/)
- [Deploy com Docker e Helm](/docs/getting-started/docker-deployment/)
