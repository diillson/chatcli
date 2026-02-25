---
title: "Modo Coder (/coder)"
description: "Um modo especializado em engenharia de software: leitura, edição, testes e refator."
weight: 40
---

O modo `/coder` é um modo especializado para tarefas de engenharia de software com ciclo de leitura → alterações →feedback.

Ele dá mais rigorosidade que o modo `/agent`, porque o assistente precisa seguir um contrato de saída para que o ChatCLI possa executar as ações com segurança (e semântica de reversão).

## Quando usar

Use `/coder` quando você quiser:
1. Fazer alterações reais no repositório (leitura, edição de arquivos);
2. Rodar testes/lint/build de forma automática;
3. Aplicar patches pequenos e seguros (com backup/rollback);
4. Iterar até chegar em um resultado verificável.

Se você quer uma conversa de alto nível (escrita de texto, ideias, planos) sem executar o próprio código, o `/agent` continua mais adequado.

## Diferença em relação ao /agent

- `/agent`: o assistente propõe comandos de execução em blocos (```execute:shell```), com interação e mais flexibilidade no formato.
- `/coder`: o assistente opera em um loop de ferramentas (fs, patch, exec) e deve emitir: <a strong>sempre a mesma estrutura</a> pra o ChatCLI consumir.

```mermaid
sequenceDiagram
    actor User as Usuário
    participant CLI as ChatCLI (Orquestrador)
    participant LLM as IA (Persona Engenheiro)
    participant Plugin as Plugin @coder

    User->>CLI: /coder <tarefa>
    
    loop Ciclo de Engenharia
        CLI->>LLM: Envia Contexto
        LLM-->>CLI: <reasoning>...</reasoning><br/><tool_call name="@coder" args="..."/>
        CLI->>Plugin: Executa @coder
        Plugin-->>CLI: Retorna Saída
        CLI->>LLM: Envia Feedback
    end

    LLM-->>CLI: Resposta Final
    CLI-->>User: Exibe Resultado
```

## Orquestração Multi-Agent (Modo Paralelo)

Com `CHATCLI_AGENT_PARALLEL_MODE=true`, o `/coder` ganha capacidade de **orquestração multi-agent**. O LLM orquestrador pode despachar agents especialistas em paralelo usando tags `<agent_call>`:

- **FileAgent** — leitura e análise de código (read-only)
- **CoderAgent** — escrita e modificação de código
- **ShellAgent** — execução de comandos e testes
- **GitAgent** — operações de controle de versão
- **SearchAgent** — busca no codebase (read-only)
- **PlannerAgent** — raciocínio e decomposição de tarefas (sem tools)

Cada agent possui skills próprias (scripts aceleradores e capacidades descritivas) e executa em seu próprio mini ReAct loop isolado. Múltiplos agents rodam simultaneamente via goroutines com semáforo configurável (`CHATCLI_AGENT_MAX_WORKERS`).

> Quando desativado (padrão), o `/coder` funciona exatamente como antes — sem nenhum impacto. Veja a [documentação completa](/docs/features/multi-agent-orchestration/).

---

## Contrato de saída (obrigatório)

O mais importante em `/coder` é que a resposta do assistente sempre segue este formato:

1) Antes de qualquer ação, escreva um bloco `|reasoning|` curto (2 a 6 linhas).
2) Em seguida, se precisar agir, emita *apenas* um `|tool_call name="@coder" args="..."/|`.
   - JSON em `args` é recomendado: `args="{\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}"`
3) Nunca usa blocos ```` nem comandos shell diretos nesse modo.

## Ferramentas e dependência
O modo `/coder` utiliza o [@coder]([[../../features/coder-plugin]]), que já vem **embutido no ChatCLI** — nenhuma instalação adicional é necessária.

- Verifique com `/plugin list`: o `@coder` aparece com a tag `[builtin]`.
- Detalhes dos subcomandos na seção [Plugin @coder]([[../../features/coder-plugin]]).

## Subcomandos suportados (args)

No `/coder`, o atributo `args` do `<tool_call>` deve usar somente os subcomandos abaixo (em linha única):

- `tree --dir .`
- `--encoding base64|text` (para `read/write/patch`)
- `search --term "x" --dir .`
- `read --file x`
- `write --file x --content "base64" --encoding base64`
- `patch --file x --search "base64" --replace "base64" --encoding base64` (ou `patch --diff "base64" --diff-encoding base64`)
- `exec --cmd "comando"`
- `git-status --dir .`
- `git-diff --dir .`
- `git-log --dir .`
- `git-changed --dir .`
- `git-branch --dir .`
- `test --dir .` (ou `--cmd "comando"`)
- `rollback --file x`
- `clean --dir .`


## Exemplos de Fluxo

### 1) Corrigir testes gerais

1. Listar a árvore: `tree --dir .`
2. Procurar ocorrências: `search --term "FAIL"...`
3. Ler arquivos: `read --file ...`
4. Aplicar patch mínimal: `patch --file ...`
5. Rodar testes: `exec --cmd "go test ./..."`

{{< notice "success">}} Nesse modo, a saída sempre vai via <markup>@coder</markup>. Você não precisa escrever comandos diretamente no shell.{{< /notice >}}

---

## FAQ do /coder

**1) Posso usar JSON em `args`?**  
Sim. É o formato recomendado. Exemplo:  
`<tool_call name="@coder" args="{\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}"/>`

**2) Quando usar `patch --diff`?**  
Quando a alteração envolve múltiplos trechos ou precisa de mais precisão. Você pode enviar um unified diff em `text` ou `base64`.

**3) Preciso instalar o `@coder` separadamente?**
Não. O `@coder` é um plugin **builtin** — já vem embutido no binário do ChatCLI e está disponível imediatamente. Se você instalar uma versão customizada em `~/.chatcli/plugins/`, ela prevalece sobre o builtin.

**4) `exec` é seguro?**  
O `@coder exec` bloqueia padrões perigosos por padrão. Para comandos sensíveis, prefira usar os subcomandos Git e `test`.

**5) Existe limite de leitura?**  
Sim. Use `read --max-bytes`, `--head` ou `--tail` para controlar o tamanho da saída.
