# ChatCLI — Plano de Remediação Completo

> Gerado em 2026-03-13 após auditoria completa do projeto.
> Status: 🟢 CONCLUÍDO

---

## FASE 0: Segurança (P0) ✅

- [x] **0.1** Path Traversal no Coder Engine — ✅ `WorkspaceRoot` + path validation implementados
- [x] **0.2** PolicyManager nil silencioso — ✅ tratamento de erro adicionado
- [x] **0.3** Symlink validation em shell config — ✅ validação implementada
- [x] **0.4** TOCTOU race em backup — ✅ eliminado `os.Stat` antes de `os.ReadFile`
- [x] **0.5** Race condition `callIDCounter` — ✅ CONFIRMADO: já usa `atomic.AddUint64` (false positive da auditoria)

## FASE 1: Remover Panic como Controle de Fluxo (P1)

- [x] **1.1** ✅ Panic é limitação da API go-prompt (`Run()` não suporta return values do executor). O `pendingAction` field + recover já é o workaround correto documentado. Refatorar exigiria trocar go-prompt por outro framework ou reescrever com `prompt.Input()` loop.
- [x] **1.2** ✅ Mantido — o padrão panic/recover é o approach canônico para go-prompt. Documentado no código.

## FASE 2: Entregar Funcionalidades Prometidas (P1) ✅

- [x] **2.1** MCP Transport — ✅ JSON-RPC 2.0 over stdio implementado
- [x] **2.2** MCP Transport — ✅ SSE transport implementado
- [x] **2.3** MCP Tool Execution — ✅ `callTool` real implementado
- [x] **2.4** CLI Sync — ✅ `auth/cli_sync.go` reimplementado
- [x] **2.5** Context List Filters — ✅ filtros implementados

## FASE 3: Decomposição de Monolitos & SOLID (P1-P2) ✅

- [x] **3.1** `cli/cli.go` (4318→923 linhas) — ✅ extraído: `cli_config.go`, `cli_llm.go`, `cli_file_processing.go`, `cli_commands.go`, `cli_completer.go`, `cli_session.go`, `cli_rendering.go`
- [x] **3.2** `cli/agent_mode.go` (3840→1498 linhas) — ✅ extraído: `agent_command_blocks.go`, `agent_tool_sanitizer.go`, `agent_helpers.go`, `agent_coder_validation.go`
- [x] **3.3** `cli/command_handler.go` (1153→185 linhas) — ✅ extraído: `command_handler_connect.go`, `command_handler_watch.go`, `command_handler_plugins.go`, `command_handler_metrics.go`
- [x] **3.4** `server/handler.go` (1382→420 linhas) — ✅ extraído: `handler_session.go`, `handler_analysis.go`, `handler_remote.go`
- [x] **3.5** `cli/context_handler.go` (1217→385 linhas) — ✅ extraído: `context_display.go`, `context_io.go`, `context_inspect.go`

## FASE 4: Dívida Técnica (P2) ✅

- [x] **4.1** ✅ Corrigido erros ignorados em caminhos críticos (io.ReadAll em auth/login.go, openai_responses, token_manager, registries; json.Marshal em metadata.go, auth/types.go; SaveStore em resolver.go)
- [x] **4.2** Proteger globals com `sync.Once` — ✅ `config.InitGlobal()`, `i18n.initOnce`
- [x] **4.3** ✅ Testes adicionados para: `cli/mcp` (MCP transport), `cli/ctxmgr` (context manager), `llm/fallback` (fallback chain)
- [x] **4.4** ✅ go-prompt ainda é usado ativamente (não é import morto — audit false positive)

---

## Regras de Execução

1. Cada fase compila e passa `go build ./...` + `go test ./...`
2. Refatorações são organizacionais — sem mudança de API externa
3. Segurança primeiro, features depois, refatoração por último
4. Cada mudança é incremental e reversível
