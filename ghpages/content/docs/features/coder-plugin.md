---
title: "Plugin @coder"
description: "Ferramenta de engenharia para ler, editar, patchar e executar tarefas com rollback."
weight: 60
---

# Plugin @coder

O `@coder` é a suite de engenharia usada pelo [Modo Coder (/coder)]([[../../core-concepts/coder-mode]]). Ele fornece ações para ler/procurar arquivos, aplicar patches com segurança, rodar comandos e reverter alterações.

## Comandos suportados

@ não invente parametros. Os comandos suportados são (no formato do atributo `args` do `<tool_call>` ou uso direto):

- `tree --dir .`
- `search --term "x" --dir .`
- `read --file path`
- `write --file path --content "base64" --encoding base64`
  - (em `write`, o conteúdo de escrita deve ser base64 e todo em uma única linha)
- `patch --file path --search "base64" --replace "base64" --encoding base64`
  - ` search` e `replace` em base64 para evitar problemas de escape e manter conteúdo em linha única
- `exec --cmd "comando"`
- `rollback --file path
- `clean --dir .`


## Rollback e Segurança

- Use `rollback --file x` para reverter uma alteração que tenha gerado backup (ex. file `.bak`).
- Use `clean --dir .` para remover arquivos e artefatos gerados por execuções de teste/build.

## Exemplo de uso (no /coder)

No modo `/coder`, o assistente deve responder com <a href="../../core-concepts/coder-mode/"><code><reasoning></reasoning></code></a> e em seguida apenas um `<tool_call name="@coder" args="..."/>`. Estes são exemplos válidos:

- Ler UM arquivo: `<tool_call name="@coder" args="read --file README.md"/>`
- Rodar testes: `<tool_call name="@coder" args="exec --cmd 'go test ./...'"/>`

## Notas

- O plugin outorga poder de leitura/escrita em arquivos e execução de comandos. Use em repositórios confiaveis.
