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

Quando uma ação cai no estado "Ask", você verá uma caixa de segurança:

```text
[SECURITY CHECK]
------------------------------------------------------------
 [!] Acao requer aprovacao: @coder write
 Params: --file main.go --content ...
 Regra: Nenhuma regra encontrada para '@coder write'
-------------------------------------------------------------
Escolha:
  [y] Sim (uma vez)
  [a] ALLOW ALWAYS (Permitir '@coder write' sempre)
  [n] Nao (pular)
  [d] DENY FOREVER (Bloquear '@coder write' sempre)

> _
```

### Opções:

* **y (Yes):** Executa apenas desta vez. Na próxima, perguntará novamente.
* **a (Always):** Cria uma regra permanente de **ALLOW** para esse comando (ex: libera todas as escritas com `@coder write`).
* **n (No)** Pula a execução. A IA recebe um erro informando que o usuário negou.
* **d (Deny):** Cria uma regra permanente de **DENY**. A ação será bloqueada automaticamente no futuro.

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

## Boas Práticas

1. **Inicie com Cautela:** Mantenha os comandos de escrita (`write`, `patch`, `exec`) como `ask` até sentir confiança no agente.
2. **Libere Leituras:** Geralmente, é seguro dar `Always` para `coder read`, `coder tree`, `coder search` e Git read-only (`git-status`, `git-diff`, `git-log`).
3. **Seja Específico:** O matching é feito por prefixo. Você pode liberar `coder exec --cmd 'ls` mas bloquear `coder exec --cmd 'rm`.
4. **Exec Seguro:** O `@coder exec` bloqueia padrões perigosos por padrão. Use `--allow-unsafe` apenas quando necessário.

---

## UI do Modo Coder

Você pode controlar o estilo e o banner do `/coder` via variáveis de ambiente:

- `CHATCLI_CODER_UI`:
  - `full` (padrão)
  - `minimal`
- `CHATCLI_CODER_BANNER`:
  - `true` (padrão, mostra o cheat sheet)
  - `false`

Essas configurações aparecem em `/status` e `/config`.
