+++
title = "Segurança e Hardening"
linkTitle = "Segurança"
weight = 11
description = "Visão completa das medidas de segurança do ChatCLI: proteção contra injeção, autenticação, infraestrutura hardened e boas práticas para produção."
icon = "shield"
+++

O ChatCLI foi projetado com **segurança em profundidade** (defense-in-depth). Esta página documenta todas as medidas de proteção implementadas, como configurá-las e as boas práticas para ambientes de produção.

---

## Resumo das Proteções

| Camada | Proteção | Status |
|--------|----------|--------|
| **Autenticação** | Comparação de tokens em tempo constante (crypto/subtle) | Ativo |
| **Shell** | Quoting POSIX para prevenir injeção em argumentos de shell | Ativo |
| **Editores** | Validação de EDITOR contra allowlist de editores conhecidos | Ativo |
| **Comandos** | 50+ padrões regex para detecção de comandos perigosos | Ativo |
| **Policies** | Matching com word-boundary para evitar escalação de permissões | Ativo |
| **gRPC** | Reflection desabilitado por padrão (oculta schema do serviço) | Ativo |
| **Binários** | Resolução de stty via exec.LookPath (evita injeção via PATH) | Ativo |
| **Containers** | read-only filesystem, no-new-privileges, drop ALL capabilities | Ativo |
| **Kubernetes** | RBAC namespace-scoped por padrão, SecurityContext restritivo | Ativo |
| **Rede** | TLS opcional com warning quando desabilitado | Ativo |

---

## Autenticação do Servidor gRPC

### Token Bearer com Comparação em Tempo Constante

O servidor gRPC usa autenticação via Bearer token no header `authorization` de cada request. A comparação do token utiliza `crypto/subtle.ConstantTimeCompare`, que **previne ataques de timing** — um atacante não consegue inferir caracteres corretos medindo o tempo de resposta.

```bash
# Definir token via flag
chatcli server --token meu-token-secreto

# Ou via variável de ambiente
export CHATCLI_SERVER_TOKEN=meu-token-secreto
chatcli server
```

O endpoint `/Health` é sempre acessível sem autenticação para permitir health checks de load balancers e orquestradores.

### TLS (Opcional)

O TLS é **totalmente opcional**. Em ambiente de desenvolvimento local, você pode rodar sem TLS sem problemas. Para produção, recomendamos fortemente habilitar TLS:

```bash
# Produção: com TLS
chatcli server --tls-cert cert.pem --tls-key key.pem --token meu-token

# Desenvolvimento: sem TLS (um warning será logado)
chatcli server
```

Quando o cliente se conecta sem TLS, um log de warning é emitido para lembrar sobre o uso em produção. O comportamento funcional não muda — a conexão continua funcionando normalmente.

---

## Proteção contra Injeção de Shell

### ShellQuote — Quoting POSIX Seguro

Todos os pontos do código onde valores dinâmicos são interpolados em comandos shell utilizam a função `utils.ShellQuote()`, que aplica quoting POSIX com aspas simples:

```go
// Entrada: it's a "test" $(whoami)
// Saída:   'it'\''s a "test" $(whoami)'
```

Isso protege contra:
- **Injeção via aspas**: `'; rm -rf /; echo '`
- **Substituição de comandos**: `$(malicious)` ou `` `malicious` ``
- **Expansão de variáveis**: `$HOME`, `${PATH}`
- **Pipe/redirecionamento**: `| cat /etc/passwd`, `> /etc/crontab`

#### Pontos Protegidos

| Arquivo | Contexto |
|---------|----------|
| `cli/agent_mode.go` | Dry-run echo de comandos (simulação) |
| `cli/cli.go` | Source do arquivo de configuração do shell (`~/.bashrc`, etc.) |
| `cli/agent/command_executor.go` | Source do arquivo de configuração do shell (execução interativa) |

### Resolução de Binários via LookPath

O binário `stty` (usado para restaurar o terminal) é resolvido **uma única vez** no startup via `exec.LookPath("stty")`, retornando o caminho absoluto. Isso evita que um atacante coloque um `stty` malicioso no PATH.

### Validação do EDITOR

Quando o usuário edita comandos no modo agente, a variável `EDITOR` é validada contra uma **allowlist de editores conhecidos**:

```
vim, vi, nvim, nano, emacs, code, subl, micro, helix, hx,
ed, pico, joe, ne, kate, gedit, kwrite, notepad++, atom
```

Se `EDITOR` contiver um valor desconhecido (ex: `EDITOR="/tmp/exploit.sh"`), a operação é recusada com erro. O editor validado é então resolvido via `exec.LookPath` para obter o caminho absoluto.

---

## Validação de Comandos

### 50+ Padrões de Detecção

O `CommandValidator` analisa cada comando sugerido pela IA antes da execução, verificando contra mais de 50 padrões regex que cobrem:

| Categoria | Exemplos |
|-----------|----------|
| **Destruição de dados** | `rm -rf /`, `dd if=`, `mkfs`, `drop database` |
| **Execução remota** | `curl \| bash`, `wget \| sh`, `base64 \| bash` |
| **Injeção de código** | `python -c`, `perl -e`, `ruby -e`, `node -e`, `php -r`, `eval` |
| **Substituição de comandos** | `$(curl ...)`, `` `wget ...` `` |
| **Substituição de processos** | `<(cmd)`, `>(cmd)` |
| **Escalação de privilégios** | `sudo`, `chmod 777 /`, `chown -R / ` |
| **Manipulação de rede** | `nc -l`, `iptables -F`, `/dev/tcp/` |
| **Kernel** | `insmod`, `modprobe`, `rmmod`, `sysctl -w` |
| **Evasão** | `${IFS;cmd}`, `VAR=x; bash`, `export PATH=` |

### Denylist Customizada

Adicione seus próprios padrões via variável de ambiente:

```bash
# Bloquear terraform destroy e kubectl delete namespace
export CHATCLI_AGENT_DENYLIST="terraform destroy;kubectl delete namespace"
```

### Controle de sudo

```bash
# Permitir sudo (use com cautela)
export CHATCLI_AGENT_ALLOW_SUDO=true
```

---

## Governança do Modo Coder (Policy Manager)

### Matching com Word Boundary

O sistema de policies usa **matching com word boundary** para prevenir escalação de permissões por prefixo. Exemplo:

| Regra | Comando | Resultado |
|-------|---------|-----------|
| `@coder read` = allow | `@coder read file.txt` | **Permitido** |
| `@coder read` = allow | `@coder readlink /tmp` | **Bloqueado** (ask) |
| `@coder read --file /etc` = deny | `@coder read --file /etc/passwd` | **Bloqueado** (deny) |

A lógica verifica se o próximo caractere após o match é um separador (espaço, `/`, `=`, etc.) e não a continuação de uma palavra (letra, dígito, `-`, `_`). Isso garante que `read` não case com `readlink`.

### Regras Padrão

Os comandos de leitura são permitidos por padrão:

```json
{
  "rules": [
    { "pattern": "@coder read", "action": "allow" },
    { "pattern": "@coder tree", "action": "allow" },
    { "pattern": "@coder search", "action": "allow" },
    { "pattern": "@coder git-status", "action": "allow" },
    { "pattern": "@coder git-diff", "action": "allow" },
    { "pattern": "@coder git-log", "action": "allow" },
    { "pattern": "@coder git-changed", "action": "allow" },
    { "pattern": "@coder git-branch", "action": "allow" }
  ]
}
```

Para mais detalhes sobre o sistema de governança, veja a [documentação do Modo Coder](/docs/features/coder-security/).

---

## Segurança do Servidor gRPC

### gRPC Reflection (Desabilitado por Padrão)

O gRPC reflection expoe o schema completo do serviço, permitindo que ferramentas como `grpcurl` e `grpcui` descubram e chamem todos os RPCs. Em produção, isso pode facilitar reconhecimento por atacantes.

**Por padrão, o reflection está desabilitado.** Para habilitar (desenvolvimento/debug):

```bash
# Via variável de ambiente
export CHATCLI_GRPC_REFLECTION=true
chatcli server

# Ou via campo EnableReflection no Config (programatico)
```

| Variável | Descrição | Padrão |
|----------|-----------|--------|
| `CHATCLI_GRPC_REFLECTION` | Habilita gRPC reflection (`true`/`false`) | `false` |

### Interceptors de Segurança

Todas as requests passam por uma cadeia de interceptors:

1. **Recovery**: Captura panics e retorna erro gRPC em vez de derrubar o servidor
2. **Logging**: Registra metodo, duração e status de cada request
3. **Auth**: Valida Bearer token (quando configurado)

---

## Verificação de Versão

O ChatCLI verifica automaticamente se há uma versão mais recente no GitHub. Para desabilitar (ex: ambientes air-gapped ou CI/CD):

```bash
export CHATCLI_DISABLE_VERSION_CHECK=true
```

| Variável | Descrição | Padrão |
|----------|-----------|--------|
| `CHATCLI_DISABLE_VERSION_CHECK` | Desabilita a verificação automática de versão (`true`/`false`) | `false` |

---

## Segurança de Containers (Docker)

O `docker-compose.yml` do projeto inclui as seguintes medidas de hardening:

```yaml
services:
  chatcli-server:
    read_only: true           # Filesystem somente-leitura
    tmpfs:
      - /tmp:size=100M        # Diretório temporário em memória
    security_opt:
      - no-new-privileges:true  # Impede escalação de privilégios
    deploy:
      resources:
        limits:
          cpus: "2.0"         # Limite de CPU
          memory: 1G          # Limite de memória
```

### O que cada medida faz

| Medida | Proteção |
|--------|----------|
| `read_only: true` | Impede que malware grave arquivos no filesystem do container |
| `tmpfs` | Fornece diretório `/tmp` em memória com tamanho limitado |
| `no-new-privileges` | Impede que processos filhos ganhem mais privilégios que o pai |
| Resource limits | Previne consumo excessivo de CPU/memória (DoS) |

---

## Segurança no Kubernetes (Helm)

### Pod SecurityContext

O Helm chart define um SecurityContext restritivo por padrão:

```yaml
# values.yaml
podSecurityContext:
  runAsNonRoot: true          # Obriga execução como usuário não-root
  runAsUser: 1000
  runAsGroup: 1000
  fsGroup: 1000
  seccompProfile:
    type: RuntimeDefault       # Filtro de syscalls do kernel

securityContext:
  allowPrivilegeEscalation: false  # Sem escalação de privilégios
  readOnlyRootFilesystem: true     # Filesystem somente-leitura
  capabilities:
    drop:
      - ALL                        # Remove TODAS as capabilities Linux
```

### RBAC Namespace-Scoped (Padrão)

Por padrão, o chart cria **Role** e **RoleBinding** (namespace-scoped) em vez de ClusterRole. Isso garante que o ChatCLI só tenha acesso ao namespace onde está deployado.

```yaml
# values.yaml
rbac:
  create: true
  clusterWide: false   # false = Role (namespace-scoped, padrão)
                       # true  = ClusterRole (cluster-wide)
```

Para monitorar deployments em **múltiplos namespaces**, habilite `clusterWide`:

```bash
helm install chatcli deploy/helm/chatcli \
  --set rbac.clusterWide=true \
  --set watcher.enabled=true
```

### tmpfs Automático

Quando `securityContext.readOnlyRootFilesystem` está `true`, o chart automaticamente monta um volume `emptyDir` em `/tmp` (limitado a 100Mi) para que a aplicação possa gravar arquivos temporários.

---

## Variáveis de Ambiente de Segurança

Resumo de todas as variáveis relacionadas a segurança:

| Variável | Descrição | Padrão |
|----------|-----------|--------|
| `CHATCLI_SERVER_TOKEN` | Token de autenticação do servidor gRPC | `""` (sem auth) |
| `CHATCLI_SERVER_TLS_CERT` | Certificado TLS do servidor | `""` |
| `CHATCLI_SERVER_TLS_KEY` | Chave TLS do servidor | `""` |
| `CHATCLI_GRPC_REFLECTION` | Habilita gRPC reflection | `false` |
| `CHATCLI_DISABLE_VERSION_CHECK` | Desabilita verificação de versão | `false` |
| `CHATCLI_AGENT_ALLOW_SUDO` | Permite sudo no modo agente | `false` |
| `CHATCLI_AGENT_DENYLIST` | Padrões regex adicionais para bloquear (`;` separados) | `""` |
| `CHATCLI_AGENT_CMD_TIMEOUT` | Timeout de execução por comando | `10m` |

---

## Criptografia de Credenciais

As credenciais OAuth são armazenadas com **criptografia AES-256-GCM** em `~/.chatcli/auth-profiles.json`. A chave de criptografia é gerada automaticamente e salva em `~/.chatcli/.auth-key` com permissão `0600` (somente o dono pode ler).

| Arquivo | Permissão | Conteúdo |
|---------|-----------|----------|
| `~/.chatcli/auth-profiles.json` | `0600` | Credenciais OAuth criptografadas |
| `~/.chatcli/.auth-key` | `0600` | Chave AES-256-GCM |
| `~/.chatcli/coder_policy.json` | `0600` | Regras de policy do Coder |

---

## Boas Práticas para Produção

### 1. Sempre use token de autenticação

```bash
export CHATCLI_SERVER_TOKEN=$(openssl rand -hex 32)
chatcli server --token $CHATCLI_SERVER_TOKEN
```

### 2. Habilite TLS em produção

```bash
chatcli server --tls-cert cert.pem --tls-key key.pem
```

### 3. Mantenha gRPC reflection desabilitado

Não defina `CHATCLI_GRPC_REFLECTION=true` em produção. Use apenas para debugging local.

### 4. Use RBAC namespace-scoped

Mantenha `rbac.clusterWide: false` (padrão) a menos que precise monitorar múltiplos namespaces.

### 5. Revise as policies do Coder regularmente

```bash
cat ~/.chatcli/coder_policy.json
```

### 6. Configure resource limits

Sempre defina limites de CPU e memória para evitar consumo excessivo:

```yaml
resources:
  requests:
    memory: "128Mi"
    cpu: "100m"
  limits:
    memory: "512Mi"
    cpu: "500m"
```

### 7. Mantenha o ChatCLI atualizado

A verificação de versão é habilitada por padrão. Se você desabilitou com `CHATCLI_DISABLE_VERSION_CHECK`, verifique periodicamente:

```bash
chatcli --version
```

---

## Próximo Passo

- [Governança do Modo Coder](/docs/features/coder-security/)
- [Configurar o servidor](/docs/features/server-mode/)
- [Deploy com Docker e Helm](/docs/getting-started/docker-deployment/)
- [Referencia de configuração (.env)](/docs/reference/configuration/)
