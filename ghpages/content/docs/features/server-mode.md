+++
title = "Modo Servidor (chatcli serve)"
linkTitle = "Modo Servidor"
weight = 60
description = "Transforme o ChatCLI em um servidor gRPC para acesso remoto de qualquer terminal, Docker ou Kubernetes."
icon = "dns"
+++

O **Modo Servidor** transforma o ChatCLI em um servico gRPC de alta performance que pode ser acessado remotamente por qualquer terminal. Isso permite centralizar o acesso a IA em um servidor (bare-metal, VM, Docker ou Kubernetes) e conectar de qualquer lugar.

---

## Por que usar o Modo Servidor?

- **Centralizacao**: Um unico servidor com API keys configuradas atende multiplos clientes
- **Seguranca**: As chaves de API ficam no servidor, nunca expostas nos terminais clientes
- **Flexibilidade**: Clientes podem usar suas **proprias credenciais** (API key ou OAuth) se desejarem
- **Performance**: Comunicacao via gRPC com suporte a TLS e streaming progressivo
- **Kubernetes-Ready**: Integracao nativa com o K8s Watcher para monitoramento de deployments

---

## Iniciando o Servidor

O comando `chatcli serve` inicia o servidor gRPC:

```bash
# Modo mais simples: servidor na porta padrao (50051)
chatcli serve

# Com porta e autenticacao customizados
chatcli serve --port 8080 --token meu-token-secreto

# Com TLS habilitado
chatcli serve --tls-cert cert.pem --tls-key key.pem

# Com K8s Watcher integrado (single-target)
chatcli serve --watch-deployment myapp --watch-namespace production

# Com K8s Watcher multi-target + Prometheus metrics
chatcli serve --watch-config targets.yaml
```

---

## Flags Disponiveis

| Flag | Descricao | Padrao | Env Var |
|------|-----------|--------|---------|
| `--port` | Porta do servidor gRPC | `50051` | `CHATCLI_SERVER_PORT` |
| `--token` | Token de autenticacao (vazio = sem auth) | `""` | `CHATCLI_SERVER_TOKEN` |
| `--tls-cert` | Arquivo de certificado TLS | `""` | `CHATCLI_SERVER_TLS_CERT` |
| `--tls-key` | Arquivo de chave TLS | `""` | `CHATCLI_SERVER_TLS_KEY` |
| `--provider` | Provedor de LLM padrao | Auto-detectado | `LLM_PROVIDER` |
| `--model` | Modelo de LLM padrao | Auto-detectado | |

### Variaveis de Seguranca

| Env Var | Descricao | Padrao |
|---------|-----------|--------|
| `CHATCLI_GRPC_REFLECTION` | Habilita gRPC reflection para debugging. **Mantenha desabilitado em producao.** | `false` |
| `CHATCLI_DISABLE_VERSION_CHECK` | Desabilita verificacao automatica de versao no startup. | `false` |

> O gRPC reflection esta **desabilitado por padrao** para nao expor o schema do servico em producao. Habilite apenas para debugging local. Veja a [documentacao de seguranca](/docs/features/security/) para todas as medidas de hardening.

### Flags do K8s Watcher (opcionais)

| Flag | Descricao | Padrao | Env Var |
|------|-----------|--------|---------|
| `--watch-config` | Arquivo YAML multi-target | `""` | `CHATCLI_WATCH_CONFIG` |
| `--watch-deployment` | Deployment unico (legado) | `""` | `CHATCLI_WATCH_DEPLOYMENT` |
| `--watch-namespace` | Namespace do deployment | `"default"` | `CHATCLI_WATCH_NAMESPACE` |
| `--watch-interval` | Intervalo de coleta | `30s` | `CHATCLI_WATCH_INTERVAL` |
| `--watch-window` | Janela de observacao | `2h` | `CHATCLI_WATCH_WINDOW` |
| `--watch-max-log-lines` | Max linhas de log por pod | `100` | `CHATCLI_WATCH_MAX_LOG_LINES` |
| `--watch-kubeconfig` | Caminho do kubeconfig | Auto-detectado | `CHATCLI_KUBECONFIG` |

> Use `--watch-config` para monitorar **multiplos deployments** simultaneamente com metricas Prometheus. Veja [K8s Watcher](/docs/features/k8s-watcher/) para o formato do arquivo YAML.

---

## Autenticacao do Servidor

### Sem Autenticacao

Por padrao, o servidor nao exige autenticacao. Qualquer cliente pode conectar:

```bash
chatcli serve  # sem --token = acesso livre
```

### Com Token

Defina um token para proteger o servidor:

```bash
# Via flag
chatcli serve --token meu-token-secreto

# Via variavel de ambiente
export CHATCLI_SERVER_TOKEN=meu-token-secreto
chatcli serve
```

O cliente precisa fornecer o mesmo token ao conectar:

```bash
chatcli connect servidor:50051 --token meu-token-secreto
```

### TLS (HTTPS)

Para conexoes encriptadas, forneca certificado e chave TLS:

```bash
chatcli serve --tls-cert /path/to/cert.pem --tls-key /path/to/key.pem
```

O cliente usa a flag `--tls` e opcionalmente `--ca-cert`:

```bash
chatcli connect servidor:50051 --tls --ca-cert /path/to/ca.pem
```

---

## Modos de Credencial

O servidor suporta multiplos modos de credencial LLM, dando flexibilidade total:

### 1. Credenciais do Servidor (Padrao)

O servidor usa suas proprias API keys configuradas via variaveis de ambiente:

```bash
export OPENAI_API_KEY=sk-xxx
export LLM_PROVIDER=OPENAI
chatcli serve
```

Nenhuma configuracao adicional necessaria no cliente.

### 2. Credenciais do Cliente (API Key)

O cliente pode enviar sua propria API key, que o servidor usa em vez das suas:

```bash
# Cliente envia sua propria chave
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

O servidor implementa um servico gRPC com os seguintes RPCs:

| RPC | Descricao |
|-----|-----------|
| `SendPrompt` | Envia um prompt e recebe a resposta completa |
| `StreamPrompt` | Envia um prompt e recebe a resposta em chunks progressivos |
| `InteractiveSession` | Streaming bidirecional para sessoes interativas |
| `ListSessions` | Lista sessoes salvas no servidor |
| `LoadSession` | Carrega uma sessao salva |
| `SaveSession` | Salva a sessao atual |
| `Health` | Health check do servidor |
| `GetServerInfo` | Informacoes do servidor (versao, provider, modelo, watcher) |
| `GetWatcherStatus` | Status do K8s Watcher (se ativo) |

### Streaming Progressivo

O RPC `StreamPrompt` divide a resposta em chunks de ~200 caracteres em fronteiras naturais (paragrafos, linhas, frases), proporcionando uma experiencia de resposta progressiva no cliente.

---

## Integracao com K8s Watcher

Quando o servidor e iniciado com `--watch-config` ou `--watch-deployment`, o K8s Watcher monitora continuamente os deployments e **injeta automaticamente o contexto Kubernetes em todos os prompts** dos clientes remotos.

### Single-Target (legado)

```bash
chatcli serve --watch-deployment myapp --watch-namespace production
```

### Multi-Target (recomendado)

```bash
chatcli serve --watch-config targets.yaml
```

O arquivo `targets.yaml` define multiplos deployments, metricas Prometheus e budget de contexto. Veja [K8s Watcher](/docs/features/k8s-watcher/#arquivo-de-configuracao-multi-target) para o formato completo.

Qualquer usuario conectado pode fazer perguntas sobre os deployments sem configuracao adicional:

```
Conectado ao ChatCLI server (version: 1.0.0, provider: OPENAI, model: gpt-4o)
K8s watcher active: 5 targets (interval: 30s)

> Quais deployments precisam de atencao?
> Analise as metricas HTTP do api-gateway
```

O servidor injeta automaticamente informacoes de pods, eventos, logs, metricas de infra e **metricas Prometheus de aplicacao** no prompt antes de enviar ao LLM. O **MultiSummarizer** gerencia o budget de contexto, priorizando targets com problemas.

Para verificar o status do watcher remotamente, use `/watch` no cliente conectado.

---

## Variaveis de Ambiente

Todas as variaveis de ambiente usadas pelo ChatCLI local tambem funcionam no servidor:

```bash
# Servidor
CHATCLI_SERVER_PORT=50051
CHATCLI_SERVER_TOKEN=meu-token
CHATCLI_SERVER_TLS_CERT=/path/to/cert.pem
CHATCLI_SERVER_TLS_KEY=/path/to/key.pem

# Seguranca
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

## Proximo Passo

- [Conectar ao servidor remotamente](/docs/features/remote-connect/)
- [K8s Watcher (multi-target + Prometheus)](/docs/features/k8s-watcher/)
- [K8s Operator (CRD)](/docs/features/k8s-operator/)
- [Deploy com Docker e Helm](/docs/getting-started/docker-deployment/)
