+++
title = "Segurança do Modo Coder (Governança)"
weight = 10
type = "docs"
description = "Entenda como funciona o sistema de governança e permissões do Modo Coder."
+++

O ChatCLI foi projetado para ser uma ferramenta poderosa, mas o poder exige controle. No **Modo Coder** (`/coder`), a IA tem capacidade de ler, escrever, criar e executar comandos no seu sistema. Para garantir que você esteja sempre no comando, implementamos um sistema de governança inspirado no ClaudeCode.

---

## Como Funciona?

Toda a vez que a IA sugere uma ação (como criar um arquivo ou rodar um script), o ChatCLI verifica as suas regras de segurança locais antes de executar.

### Os 3 Estados de Permissão

1. **Allow (Permitido):** A ação é executada automaticamente, sem interrupção. Ideal para comandos de leitura (`read`, `tree`, `search`) e Git read-only (`git-status`, `git-diff`, `git-log`, `git-changed`, `git-branch`).
2. **Deny (Bloqueado):** A ação é bloqueada silenciosamente (ou com erro para a IA). Ideal para proteger arquivos sensíveis ou comandos destrutivos.
3. **Ask (Perguntar - Padrão):** O ChatCLI pausa e exibe um menu interativo para você decidir.

---

## Menu de Aprovação Interativo

Quando uma ação cai no estado "Ask", você verá uma caixa de segurança com informações contextuais sobre a ação:

```text
╔══════════════════════════════════════════════════════════╗
║              🔒 SECURITY CHECK                            ║
╚══════════════════════════════════════════════════════════╝
 ⚡ Ação:   Escrever arquivo
           arquivo: main.go
 📜 Regra:  nenhuma regra para '@coder write'
 ──────────────────────────────────────────────────────────
 Escolha:
   [y] Sim, executar (uma vez)
   [a] Permitir sempre (@coder write)
   [n] Não, pular
   [d] Bloquear sempre (@coder write)

 > _
```

O prompt exibe a **ação em linguagem humana** (ex.: "Escrever arquivo", "Executar comando no shell", "Modificar arquivo (patch)") e os **detalhes parseados** dos argumentos JSON, em vez de mostrar JSON bruto.

#### Tipos de Ação Reconhecidos

| Subcomando | Label no Prompt | Detalhes Exibidos |
|------------|----------------|-------------------|
| `exec` | Executar comando no shell | `$ <comando>`, `dir: <cwd>` |
| `test` | Executar testes | `$ <comando>`, `dir: <cwd>` |
| `write` | Escrever arquivo | `arquivo: <path>` |
| `patch` | Modificar arquivo (patch) | `arquivo: <path>` |
| `read` | Ler arquivo | `arquivo: <path>` |
| `search` | Pesquisar no código | `termo: <pattern>`, `dir: <path>` |
| `tree` | Listar estrutura de diretórios | `dir: <path>` |

### Prompt com Contexto no Modo Paralelo

Quando a ação é solicitada por um **worker do modo multi-agent**, o prompt inclui informações adicionais sobre qual agent está requisitando:

```text
╔══════════════════════════════════════════════════════════╗
║              🔒 SECURITY CHECK                            ║
╚══════════════════════════════════════════════════════════╝
 🤖 Agent:  shell
 📋 Tarefa: Executar testes do módulo auth
 ──────────────────────────────────────────────────────────
 ⚡ Ação:   Executar comando no shell
           $ go test ./pkg/auth/...
 📜 Regra:  nenhuma regra para '@coder exec'
 ──────────────────────────────────────────────────────────
```

Isso permite que você saiba **exatamente** qual agent está solicitando a ação e por que, facilitando decisões de segurança informadas.

### Opções:

* **y (Yes):** Executa apenas desta vez. Na próxima, perguntará novamente.
* **a (Always):** Cria uma regra permanente de **ALLOW** para esse comando (ex: libera todas as escritas com `@coder write`).
* **n (No)** Pula a execução. A IA recebe um erro informando que o usuário negou.
* **d (Deny):** Cria uma regra permanente de **DENY**. A ação será bloqueada automaticamente no futuro.

> **Nota:** Para comandos `exec`, as opções "Always" e "Deny Forever" não são disponibilizadas, pois cada execução é única e requer aprovação individual.

---

## Gerenciamento de Regras

As regras são salvas localmente em `~/.chatcli/coder_policy.json`. Você pode editar esse arquivo manualmente se desejar, mas o menu interativo é a forma mais fácil de configurar.
O matching usa o subcomando efetivo do `@coder` mesmo quando `args` é JSON (ex.: `{"cmd":"read"}` vira `@coder read`).

### Policy Local (Por Projeto)

Você pode adicionar uma policy local no diretório do projeto:

- Local: `./coder_policy.json`
- Global: `~/.chatcli/coder_policy.json`

Comportamento:

- Se `merge: true`, as regras locais **mesclam** com a global (local sobrescreve padrões iguais).
- Se `merge: false` ou ausente, **somente** a policy local é usada.

**Exemplo (local com merge):**
```json
{
  "merge": true,
  "rules": [
    { "pattern": "@coder write", "action": "ask" },
    { "pattern": "@coder exec --cmd 'rm -rf'", "action": "deny" }
  ]
}
```

### Exemplo de Política (coder_policy.json):

```json
{
  "rules": [
    {
      "pattern": "@coder read",
      "action": "allow"
    },
    {
      "pattern": "@coder git-status",
      "action": "allow"
    },
    {
      "pattern": "@coder write",
      "action": "ask"
    },
    {
      "action": "deny",
      "pattern": "@coder exec --cmd 'rm -rf'"}
  ]
}
```

---

## Matching com Word Boundary

O sistema de policies usa **matching com word boundary**, garantindo que regras não casem parcialmente com subcomandos diferentes:

| Regra | Comando | Resultado |
|-------|---------|-----------|
| `@coder read` = allow | `@coder read file.txt` | Permitido |
| `@coder read` = allow | `@coder readlink /tmp` | **Não casa** (vai para Ask) |
| `@coder read --file /etc` = deny | `@coder read --file /etc/passwd` | Deny (path-prefix match) |

Isso significa que `@coder read` **nunca** vai liberar `@coder readlink` ou `@coder readwrite` acidentalmente.

---

## Validação de Comandos (50+ Padrões)

Além da governança de policies, o `@coder exec` valida cada comando contra **50+ padrões regex** que detectam:

- Destruição de dados (`rm -rf`, `dd if=`, `mkfs`, `drop database`)
- Execução remota (`curl | bash`, `base64 | sh`)
- Injeção de código (`python -c`, `eval`, `$(curl ...)`)
- Substituição de processos (`<(cmd)`, `>(cmd)`)
- Manipulação de kernel (`insmod`, `modprobe`, `rmmod`)
- Evasão (`${IFS;cmd}`, `VAR=x; bash`)

Você pode adicionar padrões customizados via `CHATCLI_AGENT_DENYLIST`:

```bash
export CHATCLI_AGENT_DENYLIST="terraform destroy;kubectl delete namespace"
```

> Para a lista completa de proteções de segurança do ChatCLI, veja a [documentação de Segurança e Hardening](/docs/features/security/).

---

## Boas Práticas

1. **Inicie com Cautela:** Mantenha os comandos de escrita (`write`, `patch`, `exec`) como `ask` até sentir confiança no agente.
2. **Libere Leituras:** Geralmente, é seguro dar `Always` para `coder read`, `coder tree`, `coder search` e Git read-only (`git-status`, `git-diff`, `git-log`).
3. **Seja Específico:** O matching usa word boundary para prefixos de subcomando e path-prefix para argumentos. Você pode liberar `coder exec --cmd 'ls` mas bloquear `coder exec --cmd 'rm`.
4. **Exec Seguro:** O `@coder exec` bloqueia padrões perigosos por padrão (50+ regras). Use `--allow-unsafe` apenas quando necessário.

---

## Governança no Modo Multi-Agent (Paralelo)

As policies de segurança são **totalmente respeitadas** pelos workers do modo multi-agent. Quando o `/coder` ou `/agent` opera em modo paralelo, cada worker verifica as regras do `coder_policy.json` antes de executar qualquer ação.

### Comportamento

- **allow**: Ação executada automaticamente pelo worker
- **deny**: Ação bloqueada; o worker recebe `[BLOCKED BY POLICY]` e continua seu fluxo
- **ask**: O worker **pausa**, o spinner de progresso é suspenso, e o prompt de segurança é exibido

Os prompts de segurança de múltiplos workers são **serializados** — apenas um prompt por vez é exibido, evitando sobreposição visual. Regras criadas durante a sessão (via "Always" ou "Deny") são imediatamente visíveis para todos os workers subsequentes.

---

## UI do Modo Coder

Você pode controlar o estilo e o banner do `/coder` via variáveis de ambiente:

- `CHATCLI_CODER_UI`:
  - `full` (padrão)
  - `mínimal`
- `CHATCLI_CODER_BANNER`:
  - `true` (padrão, mostra o cheat sheet)
  - `false`

Essas configurações aparecém em `/status` e `/config`.
