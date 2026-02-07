---
title: "Plugin @coder"
description: "Ferramenta de engenharia para ler, editar, patchar e executar tarefas com rollback."
weight: 60
---

O `@coder` é a suite de engenharia usada pelo [Modo Coder (/coder)]([[../../core-concepts/coder-mode]]). Ele fornece ações para ler/procurar arquivos, aplicar patches com segurança, rodar comandos e reverter alterações.

## Comandos suportados

Os comandos suportados são (no formato do atributo `args` do `<tool_call>` ou uso direto).
JSON em `args` é recomendado, por exemplo: `args="{\"cmd\":\"read\",\"args\":{\"file\":\"README.md\"}}"`.

- `tree --dir .`
- `search --term "x" --dir .`
- `read --file path`
- `write --file path --content "base64" --encoding base64`
  - (em `write`, o conteúdo de escrita deve ser base64 e todo em uma única linha)
- `patch --file path --search "base64" --replace "base64" --encoding base64`
  - `search` e `replace` em base64 para evitar problemas de escape e manter conteúdo em linha única
- `patch --diff "base64" --diff-encoding base64`
- `exec --cmd "comando"`
- `git-status --dir .`
- `git-diff --dir .`
- `git-log --dir .`
- `git-changed --dir .`
- `git-branch --dir .`
- `test --dir .` (ou `--cmd "comando"`)
- `rollback --file path`
- `clean --dir .`


## Rollback e Segurança

- Use `rollback --file x` para reverter uma alteração que tenha gerado backup (ex. file `.bak`).
- Use `clean --dir .` para remover backups `.bak` (dry-run por padrão, use `--force`).

## Exemplo de uso (no /coder)

No modo `/coder`, o assistente deve responder com `<reasoning> Raciocinio da IA </reasoning>` e em seguida apenas um `<tool_call name="@coder" args="..."/>`. Estes são exemplos válidos:

- Ler UM arquivo (JSON): `<tool_call name="@coder" args="{\"cmd\":\"read\",\"args\":{\"file\":\"README.md\"}}"/>`
- Rodar testes (auto): `<tool_call name="@coder" args="{\"cmd\":\"test\",\"args\":{\"dir\":\".\"}}"/>`
- Ver diff: `<tool_call name="@coder" args="{\"cmd\":\"git-diff\",\"args\":{\"dir\":\".\",\"stat\":true}}"/>`

## Notas

- O plugin outorga poder de leitura/escrita em arquivos e execução de comandos tudo passeivel de rollback quando solicitado. Use em repositórios confiaveis.

---

## FAQ do Plugin @coder

**1) O `@coder` aceita JSON em `args`?**  
Sim. O formato recomendado é JSON. Exemplo:  
`<tool_call name="@coder" args="{\"cmd\":\"read\",\"args\":{\"file\":\"README.md\"}}"/>`

**2) Quando usar `patch --diff`?**  
Use quando precisar aplicar múltiplos trechos ou quando o search/replace simples não for suficiente. O diff pode ser `text` ou `base64`.

**3) O `exec` é perigoso?**  
O `@coder exec` bloqueia padrões perigosos por padrão. Use `--allow-unsafe` apenas quando necessário e com supervisão.

**4) Existe limite de leitura?**  
Sim. Use `read --max-bytes`, `--head` ou `--tail` para evitar saídas muito grandes.
