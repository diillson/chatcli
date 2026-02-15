+++
title = "Seguranca e Hardening"
linkTitle = "Seguranca"
weight = 11
description = "Visao completa das medidas de seguranca do ChatCLI: protecao contra injecao, autenticacao, infraestrutura hardened e boas praticas para producao."
icon = "shield"
+++

O ChatCLI foi projetado com **seguranca em profundidade** (defense-in-depth). Esta pagina documenta todas as medidas de protecao implementadas, como configura-las e as boas praticas para ambientes de producao.

---

## Resumo das Protecoes

| Camada | Protecao | Status |
|--------|----------|--------|
| **Autenticacao** | Comparacao de tokens em tempo constante (crypto/subtle) | Ativo |
| **Shell** | Quoting POSIX para prevenir injecao em argumentos de shell | Ativo |
| **Editores** | Validacao de EDITOR contra allowlist de editores conhecidos | Ativo |
| **Comandos** | 50+ padroes regex para deteccao de comandos perigosos | Ativo |
| **Policies** | Matching com word-boundary para evitar escalacao de permissoes | Ativo |
| **gRPC** | Reflection desabilitado por padrão (oculta schema do servico) | Ativo |
| **Binarios** | Resolucao de stty via exec.LookPath (evita injecao via PATH) | Ativo |
| **Containers** | read-only filesystem, no-new-privileges, drop ALL capabilities | Ativo |
| **Kubernetes** | RBAC namespace-scoped por padrao, SecurityContext restritivo | Ativo |
| **Rede** | TLS opcional com warning quando desabilitado | Ativo |

---

## Autenticacao do Servidor gRPC

### Token Bearer com Comparacao em Tempo Constante

O servidor gRPC usa autenticacao via Bearer token no header `authorization` de cada request. A comparacao do token utiliza `crypto/subtle.ConstantTimeCompare`, que **previne ataques de timing** — um atacante nao consegue inferir caracteres corretos medindo o tempo de resposta.

```bash
# Definir token via flag
chatcli serve --token meu-token-secreto

# Ou via variavel de ambiente
export CHATCLI_SERVER_TOKEN=meu-token-secreto
chatcli serve
```

O endpoint `/Health` e sempre acessivel sem autenticacao para permitir health checks de load balancers e orquestradores.

### TLS (Opcional)

O TLS e **totalmente opcional**. Em ambiente de desenvolvimento local, voce pode rodar sem TLS sem problemas. Para producao, recomendamos fortemente habilitar TLS:

```bash
# Producao: com TLS
chatcli serve --tls-cert cert.pem --tls-key key.pem --token meu-token

# Desenvolvimento: sem TLS (um warning sera logado)
chatcli serve
```

Quando o cliente se conecta sem TLS, um log de warning e emitido para lembrar sobre o uso em producao. O comportamento funcional nao muda — a conexao continua funcionando normalmente.

---

## Protecao contra Injecao de Shell

### ShellQuote — Quoting POSIX Seguro

Todos os pontos do codigo onde valores dinamicos sao interpolados em comandos shell utilizam a funcao `utils.ShellQuote()`, que aplica quoting POSIX com aspas simples:

```go
// Entrada: it's a "test" $(whoami)
// Saida:   'it'\''s a "test" $(whoami)'
```

Isso protege contra:
- **Injecao via aspas**: `'; rm -rf /; echo '`
- **Substituicao de comandos**: `$(malicious)` ou `` `malicious` ``
- **Expansao de variaveis**: `$HOME`, `${PATH}`
- **Pipe/redirecionamento**: `| cat /etc/passwd`, `> /etc/crontab`

#### Pontos Protegidos

| Arquivo | Contexto |
|---------|----------|
| `cli/agent_mode.go` | Dry-run echo de comandos (simulacao) |
| `cli/cli.go` | Source do arquivo de configuracao do shell (`~/.bashrc`, etc.) |
| `cli/agent/command_executor.go` | Source do arquivo de configuracao do shell (execucao interativa) |

### Resolucao de Binarios via LookPath

O binario `stty` (usado para restaurar o terminal) e resolvido **uma unica vez** no startup via `exec.LookPath("stty")`, retornando o caminho absoluto. Isso evita que um atacante coloque um `stty` malicioso no PATH.

### Validacao do EDITOR

Quando o usuario edita comandos no modo agente, a variavel `EDITOR` e validada contra uma **allowlist de editores conhecidos**:

```
vim, vi, nvim, nano, emacs, code, subl, micro, helix, hx,
ed, pico, joe, ne, kate, gedit, kwrite, notepad++, atom
```

Se `EDITOR` contiver um valor desconhecido (ex: `EDITOR="/tmp/exploit.sh"`), a operacao e recusada com erro. O editor validado e entao resolvido via `exec.LookPath` para obter o caminho absoluto.

---

## Validacao de Comandos

### 50+ Padroes de Deteccao

O `CommandValidator` analisa cada comando sugerido pela IA antes da execucao, verificando contra mais de 50 padroes regex que cobrem:

| Categoria | Exemplos |
|-----------|----------|
| **Destruicao de dados** | `rm -rf /`, `dd if=`, `mkfs`, `drop database` |
| **Execucao remota** | `curl \| bash`, `wget \| sh`, `base64 \| bash` |
| **Injecao de codigo** | `python -c`, `perl -e`, `ruby -e`, `node -e`, `php -r`, `eval` |
| **Substituicao de comandos** | `$(curl ...)`, `` `wget ...` `` |
| **Substituicao de processos** | `<(cmd)`, `>(cmd)` |
| **Escalacao de privilegios** | `sudo`, `chmod 777 /`, `chown -R / ` |
| **Manipulacao de rede** | `nc -l`, `iptables -F`, `/dev/tcp/` |
| **Kernel** | `insmod`, `modprobe`, `rmmod`, `sysctl -w` |
| **Evasao** | `${IFS;cmd}`, `VAR=x; bash`, `export PATH=` |

### Denylist Customizada

Adicione seus proprios padroes via variavel de ambiente:

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

## Governanca do Modo Coder (Policy Manager)

### Matching com Word Boundary

O sistema de policies usa **matching com word boundary** para prevenir escalacao de permissoes por prefixo. Exemplo:

| Regra | Comando | Resultado |
|-------|---------|-----------|
| `@coder read` = allow | `@coder read file.txt` | **Permitido** |
| `@coder read` = allow | `@coder readlink /tmp` | **Bloqueado** (ask) |
| `@coder read --file /etc` = deny | `@coder read --file /etc/passwd` | **Bloqueado** (deny) |

A logica verifica se o proximo caractere apos o match e um separador (espaco, `/`, `=`, etc.) e nao a continuacao de uma palavra (letra, digito, `-`, `_`). Isso garante que `read` nao case com `readlink`.

### Regras Padrao

Os comandos de leitura sao permitidos por padrao:

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

Para mais detalhes sobre o sistema de governanca, veja a [documentacao do Modo Coder](/docs/features/coder-security/).

---

## Seguranca do Servidor gRPC

### gRPC Reflection (Desabilitado por Padrao)

O gRPC reflection expoe o schema completo do servico, permitindo que ferramentas como `grpcurl` e `grpcui` descubram e chamem todos os RPCs. Em producao, isso pode facilitar reconhecimento por atacantes.

**Por padrao, o reflection esta desabilitado.** Para habilitar (desenvolvimento/debug):

```bash
# Via variavel de ambiente
export CHATCLI_GRPC_REFLECTION=true
chatcli serve

# Ou via campo EnableReflection no Config (programatico)
```

| Variavel | Descricao | Padrao |
|----------|-----------|--------|
| `CHATCLI_GRPC_REFLECTION` | Habilita gRPC reflection (`true`/`false`) | `false` |

### Interceptors de Seguranca

Todas as requests passam por uma cadeia de interceptors:

1. **Recovery**: Captura panics e retorna erro gRPC em vez de derrubar o servidor
2. **Logging**: Registra metodo, duracao e status de cada request
3. **Auth**: Valida Bearer token (quando configurado)

---

## Verificacao de Versao

O ChatCLI verifica automaticamente se ha uma versao mais recente no GitHub. Para desabilitar (ex: ambientes air-gapped ou CI/CD):

```bash
export CHATCLI_DISABLE_VERSION_CHECK=true
```

| Variavel | Descricao | Padrao |
|----------|-----------|--------|
| `CHATCLI_DISABLE_VERSION_CHECK` | Desabilita a verificacao automatica de versao (`true`/`false`) | `false` |

---

## Seguranca de Containers (Docker)

O `docker-compose.yml` do projeto inclui as seguintes medidas de hardening:

```yaml
services:
  chatcli-server:
    read_only: true           # Filesystem somente-leitura
    tmpfs:
      - /tmp:size=100M        # Diretorio temporario em memoria
    security_opt:
      - no-new-privileges:true  # Impede escalacao de privilegios
    deploy:
      resources:
        limits:
          cpus: "2.0"         # Limite de CPU
          memory: 1G          # Limite de memoria
```

### O que cada medida faz

| Medida | Protecao |
|--------|----------|
| `read_only: true` | Impede que malware grave arquivos no filesystem do container |
| `tmpfs` | Fornece diretorio `/tmp` em memoria com tamanho limitado |
| `no-new-privileges` | Impede que processos filhos ganhem mais privilegios que o pai |
| Resource limits | Previne consumo excessivo de CPU/memoria (DoS) |

---

## Seguranca no Kubernetes (Helm)

### Pod SecurityContext

O Helm chart define um SecurityContext restritivo por padrao:

```yaml
# values.yaml
podSecurityContext:
  runAsNonRoot: true          # Obriga execucao como usuario nao-root
  runAsUser: 1000
  runAsGroup: 1000
  fsGroup: 1000
  seccompProfile:
    type: RuntimeDefault       # Filtro de syscalls do kernel

securityContext:
  allowPrivilegeEscalation: false  # Sem escalacao de privilegios
  readOnlyRootFilesystem: true     # Filesystem somente-leitura
  capabilities:
    drop:
      - ALL                        # Remove TODAS as capabilities Linux
```

### RBAC Namespace-Scoped (Padrao)

Por padrao, o chart cria **Role** e **RoleBinding** (namespace-scoped) em vez de ClusterRole. Isso garante que o ChatCLI so tenha acesso ao namespace onde esta deployado.

```yaml
# values.yaml
rbac:
  create: true
  clusterWide: false   # false = Role (namespace-scoped, padrao)
                       # true  = ClusterRole (cluster-wide)
```

Para monitorar deployments em **multiplos namespaces**, habilite `clusterWide`:

```bash
helm install chatcli deploy/helm/chatcli \
  --set rbac.clusterWide=true \
  --set watcher.enabled=true
```

### tmpfs Automatico

Quando `securityContext.readOnlyRootFilesystem` esta `true`, o chart automaticamente monta um volume `emptyDir` em `/tmp` (limitado a 100Mi) para que a aplicacao possa gravar arquivos temporarios.

---

## Variaveis de Ambiente de Seguranca

Resumo de todas as variaveis relacionadas a seguranca:

| Variavel | Descricao | Padrao |
|----------|-----------|--------|
| `CHATCLI_SERVER_TOKEN` | Token de autenticacao do servidor gRPC | `""` (sem auth) |
| `CHATCLI_SERVER_TLS_CERT` | Certificado TLS do servidor | `""` |
| `CHATCLI_SERVER_TLS_KEY` | Chave TLS do servidor | `""` |
| `CHATCLI_GRPC_REFLECTION` | Habilita gRPC reflection | `false` |
| `CHATCLI_DISABLE_VERSION_CHECK` | Desabilita verificacao de versao | `false` |
| `CHATCLI_AGENT_ALLOW_SUDO` | Permite sudo no modo agente | `false` |
| `CHATCLI_AGENT_DENYLIST` | Padroes regex adicionais para bloquear (`;` separados) | `""` |
| `CHATCLI_AGENT_CMD_TIMEOUT` | Timeout de execucao por comando | `10m` |

---

## Criptografia de Credenciais

As credenciais OAuth sao armazenadas com **criptografia AES-256-GCM** em `~/.chatcli/auth-profiles.json`. A chave de criptografia e gerada automaticamente e salva em `~/.chatcli/.auth-key` com permissao `0600` (somente o dono pode ler).

| Arquivo | Permissao | Conteudo |
|---------|-----------|----------|
| `~/.chatcli/auth-profiles.json` | `0600` | Credenciais OAuth criptografadas |
| `~/.chatcli/.auth-key` | `0600` | Chave AES-256-GCM |
| `~/.chatcli/coder_policy.json` | `0600` | Regras de policy do Coder |

---

## Boas Praticas para Producao

### 1. Sempre use token de autenticacao

```bash
export CHATCLI_SERVER_TOKEN=$(openssl rand -hex 32)
chatcli serve --token $CHATCLI_SERVER_TOKEN
```

### 2. Habilite TLS em producao

```bash
chatcli serve --tls-cert cert.pem --tls-key key.pem
```

### 3. Mantenha gRPC reflection desabilitado

Nao defina `CHATCLI_GRPC_REFLECTION=true` em producao. Use apenas para debugging local.

### 4. Use RBAC namespace-scoped

Mantenha `rbac.clusterWide: false` (padrao) a menos que precise monitorar multiplos namespaces.

### 5. Revise as policies do Coder regularmente

```bash
cat ~/.chatcli/coder_policy.json
```

### 6. Configure resource limits

Sempre defina limites de CPU e memoria para evitar consumo excessivo:

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

A verificacao de versao e habilitada por padrao. Se voce desabilitou com `CHATCLI_DISABLE_VERSION_CHECK`, verifique periodicamente:

```bash
chatcli --version
```

---

## Proximo Passo

- [Governanca do Modo Coder](/docs/features/coder-security/)
- [Configurar o servidor](/docs/features/server-mode/)
- [Deploy com Docker e Helm](/docs/getting-started/docker-deployment/)
- [Referencia de configuracao (.env)](/docs/reference/configuration/)
