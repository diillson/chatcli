+++
title = "Segurança do Modo Coder (Governança)"
weight = 10
type = "docs"
description = "Entenda como funciona o sistema de governança e permissões do Modo Coder."
+++

O ChatCLI foi projetado para ser uma ferramenta poderosa, mas o poder exige controle. No **Modo Coder ** (`/coder`), a IA tem capacidade de ler, escrever, criar e executar comandos no seu sistema. Para garantir que você esteja sempre no comando, implementamos um sistema de governança inspirado no ClaudeCode.

---

## Como Funciona?

Toda a vez que a IA sugere uma ação (como criar um arquivo ou rodar um script), o ChatCLI verifica as suas regras de segurança locais antes de executar.

### Os 3 Estados de Permissão

1. **Allow (Permitido):** A ação é executada automaticamente, sem interrupção. Ideal para comandos de leitura (`ls`, `read`, `tree`).
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

As regras são salvas localmente em `.~/.chatcli/coder_policy.json`. Você pode editar esse arquivo manualmente se desejar, mas o menu interativo é a forma mais fácil de configurar.

### Exemplo de Política (coder_policy.json):

```json
{
  "rules": [
    {
      "pattern": "@coder read",
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
2. **Libere Leituras:** Geralmente, é seguro dar `Always` para `coder read`, `coder tree` e `coder search`, pois não alteram o seu código.
3. **Seja Específico:** O matching é feito por prefixo. Você pode liberar `coder exec --cmd 'ls` mas bloquear `coder exec --cmd 'rm`.
