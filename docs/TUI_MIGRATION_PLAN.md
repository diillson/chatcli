# ChatCLI: Plano de Migração go-prompt → Bubble Tea

> **Objetivo**: Substituir completamente go-prompt por Bubble Tea para alcançar uma experiência
> de terminal rica similar ao OpenCode, sem perder NENHUMA funcionalidade existente.
> Unificar os 3 modos (chat, agent, coder) em um único modo inteligente onde o LLM decide
> quando usar tools, editar código ou simplesmente responder texto.
>
> **Data de criação**: 2026-03-11
> **Última atualização**: 2026-03-11
> **Status**: ✅ COMPLETO

---

## Sumário

- [Visão Geral da Arquitetura Atual](#visão-geral-da-arquitetura-atual)
- [Decisão Arquitetural: Modo Unificado](#decisão-arquitetural-modo-unificado)
- [Arquitetura Alvo](#arquitetura-alvo)
- [Fase 0: Streaming LLM Interface](#fase-0-streaming-llm-interface)
- [Fase 1: TUI Core (Bubble Tea)](#fase-1-tui-core-bubble-tea)
- [Fase 2: Modo Unificado no TUI](#fase-2-modo-unificado-no-tui)
- [Fase 3: Sidebar, Header, Footer](#fase-3-sidebar-header-footer)
- [Fase 4: Remoção do go-prompt e modos legados](#fase-4-remoção-do-go-prompt-e-modos-legados)
- [Inventário de Funcionalidades](#inventário-de-funcionalidades)
- [Referência de Arquivos](#referência-de-arquivos)
- [Decisões Técnicas](#decisões-técnicas)

---

## Visão Geral da Arquitetura Atual

### Arquivos Core (10.190 linhas total)

| Arquivo | Linhas | Responsabilidade |
|---------|--------|------------------|
| `cli/cli.go` | 4.279 | Struct `ChatCLI`, `Start()`, go-prompt setup, executor, completer, mode switching (panic/recover), renderMarkdown, typewriterEffect |
| `cli/agent_mode.go` | 3.832 | `AgentMode` struct, `processAIResponseAndAct` loop, tool execution, stdin reader, approval flow |
| `cli/command_handler.go` | 1.153 | Todos os `/commands`, `@file`, `@command`, dispatch e autocomplete |
| `cli/agent/ui_renderer.go` | 627 | Renderização agent mode: ANSI direto, plan display, pager |
| `llm/client/llm_client.go` | 36 | Interface `LLMClient` com `SendPrompt(ctx, prompt, history, maxTokens) (string, error)` |
| `cli/bus/bus.go` | 263 | MessageBus para I/O desacoplado (Subscribe/Publish pattern) - **subutilizado** |

### Problemas Arquiteturais Atuais

1. **3 modos separados que deveriam ser 1** — user precisa decidir se quer `/agent`, `/coder` ou chat simples
2. **go-prompt.Run() é blocking** — dono do terminal, inescapável sem panic
3. **LLM não-streaming** — `SendPrompt` retorna `(string, error)`, resposta completa buffered
4. **Mode switching via panic/recover** — frágil, especialmente no Windows
5. **Output é `fmt.Print*` direto** — sem pipeline de rendering estruturado
6. **Agent mode tem seu próprio stdin reader** (`stdinLines` channel) separado do go-prompt
7. **AnimationManager** e `prefixSpinnerIdx` são workarounds do go-prompt

### Struct ChatCLI (campos relevantes para migração)

```go
type ChatCLI struct {
    Client               client.LLMClient       // LLM client atual
    manager              manager.LLMManager      // gerencia múltiplos providers
    logger               *zap.Logger
    Provider             string                  // nome do provider atual
    Model                string                  // nome do modelo atual
    history              []models.Message         // histórico unificado
    historyCompactor     *HistoryCompactor        // compactação de histórico
    commandHistory       []string                 // histórico de comandos (input)
    commandHandler       *CommandHandler          // dispatch de /commands
    agentMode            *AgentMode               // modo agente
    interactionState     InteractionState         // Normal, SwitchingProvider, Processing, AgentMode
    operationCancel      context.CancelFunc       // cancela operação LLM em andamento
    isExecuting          atomic.Bool              // true enquanto LLM processa
    sessionManager       *SessionManager          // save/load sessões
    contextHandler       *ContextHandler          // /context attach/detach
    personaHandler       *PersonaHandler          // /persona
    skillHandler         *SkillHandler            // /skill
    executionProfile     ExecutionProfile         // Normal, Agent, Coder → SERÁ REMOVIDO
    pluginManager        *plugins.Manager         // sistema de plugins
    contextBuilder       *workspace.ContextBuilder // workspace bootstrap
    memoryStore          *workspace.MemoryStore    // /memory
    checkpoints          []conversationCheckpoint  // rewind (Esc+Esc)
}
```

### Os 3 Modos Atuais (o que será unificado)

| Aspecto | Chat (ProfileNormal) | Agent (ProfileAgent) | Coder (ProfileCoder) |
|---------|------|-------|-------|
| **Entry** | Prompt loop padrão | `/agent` ou `/run` | `/coder` |
| **Loop** | Single turn | ReAct multi-turn | ReAct multi-turn |
| **System Prompt** | Workspace context | Agent prompt + persona | CoderSystemPrompt (English) |
| **Tools** | Nenhum plugin | TODOS os plugins | Só @coder |
| **Segurança** | N/A | Ask user | PolicyManager + Block |
| **Formato** | Markdown texto | Texto + code + tool_calls | Reasoning + tool_calls JSON |

**Problema**: No OpenCode e Claude Code, tudo é um modo só. O LLM decide quando usar tools.

### Dependências go-prompt (o que será removido)

```go
import "github.com/c-bata/go-prompt"  // cli/cli.go

// Funções/tipos usados:
prompt.New()                    // cria o REPL
prompt.OptionLivePrefix()       // spinner animado no prefix
prompt.OptionParser()           // paste detection
prompt.OptionHistory()          // histórico de comandos
prompt.OptionAddKeyBind()       // Ctrl+C, Esc, Ctrl+Arrow
prompt.OptionAddASCIICodeBind() // Alt+Arrow, etc. (macOS, xterm, rxvt, Windows)
prompt.OptionSuggestion*Color() // cores do autocomplete
prompt.OptionMaxSuggestion()    // max 10 sugestões
prompt.Suggest{}                // struct para autocomplete
cli.executor()                  // callback quando user submete input
cli.completer()                 // callback para gerar sugestões
cli.handleCtrlC()               // callback Ctrl+C
cli.handleEscape()              // callback Esc (rewind)
cli.changeLivePrefix()          // retorna prefix atualizado a cada render
```

---

## Decisão Arquitetural: Modo Unificado

### Motivação

No ChatCLI atual, o user precisa saber quando usar chat, `/agent` ou `/coder`.
No OpenCode e Claude Code, o user só digita e o LLM decide o que fazer:
- Se é uma pergunta simples → responde texto
- Se precisa editar código → usa tools de file
- Se precisa rodar comandos → usa shell
- Se precisa de um plano complexo → faz multi-turn com reasoning

### O que muda

```
ANTES (3 modos):                    DEPOIS (1 modo):
┌─────────────────────┐             ┌─────────────────────┐
│ Chat Mode           │             │                     │
│   └─ texto só       │             │   Modo Unificado    │
├─────────────────────┤             │                     │
│ Agent Mode          │    →        │  - Um system prompt  │
│   └─ todos plugins  │             │  - Todas as tools    │
├─────────────────────┤             │  - ReAct multi-turn  │
│ Coder Mode          │             │  - Segurança ativa   │
│   └─ só @coder      │             │  - LLM decide        │
└─────────────────────┘             └─────────────────────┘
```

### Detalhamento técnico

#### 1. System Prompt Unificado

Um único system prompt que combina o melhor dos 3:

```go
// cli/prompts.go (novo)
const UnifiedSystemPrompt = `You are ChatCLI, an intelligent coding assistant running in the terminal.

## Capabilities
You have access to tools for reading, writing, and editing files, executing shell commands,
searching code, running tests, and managing git operations.

## Behavior
- For simple questions: respond with text (markdown supported)
- For code tasks: use the available tools to read, write, and test code
- For complex tasks: break into steps, explain your reasoning, then execute
- Always prefer editing existing files over creating new ones
- When modifying code, read the file first to understand context

## Safety
- Before running potentially dangerous commands (rm -rf, git reset, etc.), explain
  what you're about to do and ask for confirmation
- Never modify files outside the current project without explicit permission
- Show diffs before applying large changes

## Format
- Use <reasoning>...</reasoning> tags for complex decisions
- Use <tool_call>...</tool_call> for tool invocations
- You can invoke multiple independent tools in parallel
`
```

#### 2. Tools sempre disponíveis

No modo unificado, **todos** os plugins estão sempre disponíveis.
O `isCoderMode` flag que filtra plugins é removido:

```go
// ANTES (agent_mode.go:1626):
if a.isCoderMode && !strings.EqualFold(plugin.Name(), "@coder") {
    continue // skip non-@coder plugins
}

// DEPOIS:
// Sem filtro — todos os plugins são sempre documentados para o LLM
```

#### 3. Segurança unificada

A segurança mais rigorosa do Coder Mode se torna o padrão:

```go
// Sempre ativo, independente do "modo":
CommandValidator.IsDangerous()    // patterns perigosos (rm -rf, etc.)
PolicyManager.Check()             // policy rules do coder
// Se perigoso → mostra overlay de aprovação no TUI
```

#### 4. `/agent` e `/coder` viram hints opcionais

Esses comandos não trocam mais de modo. Eles apenas ajustam o system prompt
como um "hint" para o LLM focar em determinado tipo de tarefa:

```go
// /agent <query> → prepend "Focus on tool orchestration and automation" ao prompt
// /coder <query> → prepend "Focus on code editing using @coder tools" ao prompt
// Ambos executam no MESMO viewport, MESMO loop, MESMAS tools
```

#### 5. ReAct loop sempre ativo

O loop ReAct (que hoje só roda em agent/coder mode) fica ativo sempre:
- Se o LLM responde texto puro → exibe e para (1 turn)
- Se o LLM emite tool_calls → executa, alimenta resultado, continua loop
- MaxTurns configurável (padrão: 25, como agent mode atual)

#### 6. O que acontece com `ExecutionProfile`

```go
// ANTES:
type ExecutionProfile int
const (
    ProfileNormal ExecutionProfile = iota
    ProfileAgent
    ProfileCoder
)

// DEPOIS: removido inteiramente
// O campo executionProfile do ChatCLI é deletado
// A lógica que checa executionProfile é unificada
```

#### 7. O que acontece com `InteractionState`

```go
// ANTES:
StateNormal         // go-prompt ativo, esperando input
StateSwitchingProvider  // trocando provider
StateProcessing     // LLM processando
StateAgentMode      // agent mode ativo (stdin reader diferente)

// DEPOIS:
StateIdle           // esperando input no TUI
StateProcessing     // LLM processando (streaming ou tool execution)
StateAwaitingApproval // overlay de aprovação ativo
```

Sem `StateAgentMode` porque o agent loop É o loop padrão.

#### 8. Panic/recover eliminado

```go
// ANTES: mode switching via panic
case strings.HasPrefix(input, "/agent"):
    ch.cli.pendingAction = "agent"
    panic(agentModeRequest)  // 😱

// DEPOIS: tudo é tea.Msg
case strings.HasPrefix(input, "/agent"):
    // Envia o query com hint de agent para o mesmo loop
    return AgentHintMsg{Query: query}
```

### Impacto nos arquivos

| Arquivo | Antes | Depois |
|---------|-------|--------|
| `cli/cli.go` | `executionProfile`, panic/recover, 3 code paths | Removido. TUI único |
| `cli/agent_mode.go` | `isCoderMode` flag, tool filtering | Flag removida, tools sempre disponíveis |
| `cli/prompts.go` | 3 system prompts separados | 1 `UnifiedSystemPrompt` + hints opcionais |
| `cli/command_handler.go` | `/agent` faz panic, `/coder` faz panic | `/agent` e `/coder` viram hints no prompt |
| `cli/coder/policy_manager.go` | Só ativo em coder mode | Sempre ativo (segurança default) |

---

## Arquitetura Alvo

### Layout Terminal (inspirado no OpenCode)

```
┌──────────────────────────────────────────┬─────────────────┐
│  HEADER                                  │                 │
│  ChatCLI · claude-sonnet-4-5 · CLAUDEAI  │   SIDEBAR       │
├──────────────────────────────────────────┤   (42 chars)     │
│                                          │                 │
│  VIEWPORT (scrollable)                   │  Session        │
│                                          │  ─────────      │
│  ┌─ user ──────────────────────────────┐ │  My Chat        │
│  │ crie um dockerfile para go 1.25     │ │                 │
│  └─────────────────────────────────────┘ │  Context        │
│                                          │  ─────────      │
│  ┌─ assistant ─────────────────────────┐ │  12,345 (15%)   │
│  │ <reasoning>                         │ │  ~$0.02 spent   │
│  │ Preciso criar o Dockerfile...       │ │                 │
│  │ </reasoning>                        │ │  Tasks          │
│  │                                     │ │  ─────────      │
│  │ Vou criar o Dockerfile:             │ │  ✅ Read files   │
│  └─────────────────────────────────────┘ │  ⏳ Write Docker │
│                                          │                 │
│  ┌─ tool [@coder write] ──────────────┐ │  Modified       │
│  │ ✅ Dockerfile created (18 lines)    │ │  ─────────      │
│  └─────────────────────────────────────┘ │  Dockerfile +18 │
│                                          │                 │
│  ┌─ assistant ─────────────────────────┐ │                 │
│  │ Dockerfile criado! Quer que eu      │ │                 │
│  │ rode `docker build` para testar?    │ │                 │
│  └─────────────────────────────────────┘ │                 │
├──────────────────────────────────────────┤                 │
│  > |                                     │                 │
├──────────────────────────────────────────┼─────────────────┤
│  ~/GolandProjects/chatcli                │ ● 2 MCP  ? help │
└──────────────────────────────────────────┴─────────────────┘
```

Note: **não existe indicador de "modo"** no footer. Tudo é um modo só.

### Fluxo de uma interação

```
User digita "crie um dockerfile para go 1.25"
        │
        ▼
┌─────────────────────────────────┐
│  Backend.SendMessage(input)     │
│  → Monta system prompt unificado│
│  → Injeta tools disponíveis     │
│  → Envia para LLM via streaming │
└─────────┬───────────────────────┘
          │
          ▼
┌─────────────────────────────────┐
│  LLM responde com:             │
│  - texto (streaming para TUI)   │
│  - tool_calls (se necessário)   │
└─────────┬───────────────────────┘
          │
    ┌─────┴──────┐
    │            │
    ▼            ▼
  Texto?      Tool calls?
    │            │
    ▼            ▼
  Renderiza    Executa tools
  markdown     → Mostra resultado no viewport
  → FIM       → Alimenta resultado ao LLM
               → Próximo turn (loop ReAct)
               → Até LLM responder só texto (ou maxTurns)
```

### Estrutura de Pacotes (novos)

```
cli/tui/
├── app.go              # tea.Program entry point, Backend interface
├── model.go            # Root tea.Model (layout, state machine)
├── keymap.go           # Key bindings (charmbracelet/bubbles/key)
├── styles.go           # Lipgloss styles centralizados
├── messages.go         # Todos os tea.Msg types
│
├── components/
│   ├── input.go        # Textarea com multiline/paste/autocomplete
│   ├── viewport.go     # Scrollable message area (unified)
│   ├── sidebar.go      # Painel lateral: context, tasks, files
│   ├── header.go       # Barra superior: session, model, provider
│   ├── footer.go       # Barra inferior: cwd, status
│   ├── completer.go    # Overlay de autocomplete para /commands
│   ├── diff.go         # Diff viewer para file changes
│   ├── spinner.go      # Loading indicators
│   ├── tool_view.go    # Inline (pending) / Block (completed) tool rendering
│   └── approval.go     # Overlay modal para comandos perigosos
│
├── adapter.go          # ChatCLI → Backend adapter (implementa interface)
├── stream_cmd.go       # tea.Cmd que consome StreamingClient channel
└── markdown.go         # Glamour wrapper para rendering markdown no viewport
```

### Nova Dependência: Bubble Tea Stack

```
github.com/charmbracelet/bubbletea      # framework TUI
github.com/charmbracelet/bubbles        # componentes (textarea, viewport, spinner)
github.com/charmbracelet/lipgloss       # styling (já existe como dependência)
github.com/charmbracelet/glamour        # markdown rendering (já existe)
```

---

## Fase 0: Streaming LLM Interface

**Status**: [x] Concluída (2026-03-11)

### Objetivo

Adicionar streaming ao `LLMClient` sem quebrar a interface existente. Pré-requisito para
o TUI exibir tokens em tempo real.

### Mudanças

#### 1. Novo arquivo: `llm/client/stream.go`

```go
package client

import "context"

// StreamChunk representa um delta de uma resposta LLM em streaming.
type StreamChunk struct {
    Text      string            // texto parcial (delta)
    Done      bool              // true no último chunk
    Usage     *UsageInfo        // populado no chunk final
    Error     error             // erro, se ocorreu
    Metadata  map[string]string // dados extras (tool_call_id, etc.)
}

// UsageInfo contém métricas de uso de tokens.
type UsageInfo struct {
    InputTokens   int
    OutputTokens  int
    CacheRead     int
    CacheWrite    int
    ReasoningTokens int
}

// StreamingClient estende LLMClient com suporte a streaming.
type StreamingClient interface {
    LLMClient
    SendPromptStream(ctx context.Context, prompt string, history []models.Message, maxTokens int) (<-chan StreamChunk, error)
}

// AsStreamingClient wraps any LLMClient as StreamingClient (uses StreamFromSync if needed)
func AsStreamingClient(c LLMClient) StreamingClient { ... }
```

#### 2. Adapters de compatibilidade (mesmo arquivo)

```go
// CollectStream bloqueia e coleta toda a stream em uma string.
func CollectStream(ch <-chan StreamChunk) (string, *UsageInfo, error) { ... }

// StreamFromSync wrapa uma chamada SendPrompt síncrona em um channel de streaming.
func StreamFromSync(ctx context.Context, fn func(ctx context.Context) (string, error)) <-chan StreamChunk { ... }
```

### Critérios de conclusão

- [x] `llm/client/stream.go` criado com interface + adapters + `AsStreamingClient`
- [x] Claude OAuth/API Key streaming via channel (`claudeai/claude_client.go:SendPromptStream`)
- [x] OpenAI streaming via channel (`openai/openai_client.go:SendPromptStream`)
- [x] OpenAI Responses streaming via channel (`openai_responses/openai_responses_client.go:SendPromptStream`)
- [x] Copilot, xAI, Google AI, Ollama, StackSpot, OpenAI Assistant → via `AsStreamingClient` wrapper (StreamFromSync)
- [x] `InstrumentedClient.SendPromptStream` delegates to inner or wraps
- [x] `go build ./...` passa
- [x] `go test ./...` passa (todos os testes existentes + 6 novos testes de streaming)
- [x] One-shot mode (`-p` flag) continua funcionando (nenhuma mudança no SendPrompt)

---

## Fase 1: TUI Core (Bubble Tea)

**Status**: [x] Concluída (2026-03-11)

### Objetivo

Criar o esqueleto Bubble Tea em `cli/tui/` como pacote paralelo ao go-prompt.
Neste ponto, o TUI é selecionável via flag/env var (ex: `CHATCLI_TUI=bubbletea`).

### Backend Interface

A interface que desacopla o TUI do `ChatCLI` struct.
Note: **não há métodos de Agent/Coder separados** — tudo é `SendMessage`.

```go
// cli/tui/app.go
package tui

type Backend interface {
    // Unified: envia input (texto ou /command) e recebe stream de eventos
    SendMessage(ctx context.Context, input string) (<-chan Event, error)
    CancelOperation()

    // History
    GetHistory() []models.Message

    // Metadata
    GetModelName() string
    GetProvider() string
    GetSessionName() string
    GetWorkingDir() string

    // Completions (para autocomplete de /commands)
    GetCompletions(prefix string) []Completion

    // Sidebar data
    GetTokenUsage() TokenUsage
    GetModifiedFiles() []FileChange
    GetTasks() []Task
}

// Event é a unidade de comunicação do backend para o TUI.
// Substitui StreamChunk + AgentEvents em um tipo unificado.
type Event struct {
    Type     EventType
    Text     string            // para TextDelta, CommandOutput
    Tool     *ToolEvent        // para ToolStart, ToolResult
    Approval *ApprovalRequest  // para NeedApproval
    Usage    *client.UsageInfo // para Done
    Error    error             // para Error
}

type EventType int
const (
    EventTextDelta    EventType = iota // token parcial do LLM (streaming)
    EventToolStart                      // tool call iniciado (nome, args)
    EventToolResult                     // tool call concluído (output, exit code)
    EventNeedApproval                   // comando perigoso, precisa aprovação
    EventThinking                       // LLM pensando (reasoning block)
    EventTurnStart                      // início de um turn ReAct
    EventPlanUpdate                     // atualização do plano/tasks
    EventDone                           // resposta completa
    EventError                          // erro
)

type ToolEvent struct {
    Name        string
    Description string
    Args        string
    Output      string
    ExitCode    int
    Duration    time.Duration
    Status      string // "running", "done", "error"
}

type ApprovalRequest struct {
    Command     string
    Description string
    Risk        string // "high", "medium"
    ResponseCh  chan<- ApprovalResponse
}

type ApprovalResponse int
const (
    ApprovalYes ApprovalResponse = iota
    ApprovalNo
    ApprovalAlways
    ApprovalSkip
)

type Completion struct {
    Text        string
    Description string
}

type TokenUsage struct {
    Used    int
    Limit   int
    Cost    float64
}

type FileChange struct {
    Path      string
    Additions int
    Deletions int
}

type Task struct {
    Description string
    Status      string // "pending", "running", "done", "error"
}
```

### Root Model

```go
// cli/tui/model.go

type State int
const (
    StateIdle      State = iota // esperando input
    StateStreaming               // recebendo tokens do LLM
    StateToolExec                // executando tool (turn ReAct)
    StateApproval                // overlay de aprovação visível
)

type Model struct {
    backend    Backend
    state      State
    width      int
    height     int

    // Sub-components
    input      InputModel       // textarea com autocomplete
    viewport   ViewportModel    // área de mensagens scrollable
    sidebar    SidebarModel     // painel lateral
    header     HeaderModel      // barra superior
    footer     FooterModel      // barra inferior
    completer  CompleterModel   // overlay autocomplete
    approval   ApprovalModel    // overlay de aprovação

    // State
    messages   []RenderedMessage // mensagens renderizadas para viewport
    err        error
}
```

### Mensagens (tea.Msg types)

```go
// cli/tui/messages.go

// Backend events (recebidos do Backend via stream)
type BackendEventMsg struct{ Event Event }

// UI events
type WindowSizeMsg tea.WindowSizeMsg
type CommandResultMsg struct{ Output string }
type ErrorMsg struct{ Err error }

// Sidebar updates
type TokenUsageUpdateMsg struct{ Usage TokenUsage }
type TaskUpdateMsg struct{ Tasks []Task }
type FileChangeMsg struct{ Files []FileChange }
```

Note: **sem AgentTurnStartMsg, AgentToolCallMsg, etc. separados**.
Todos os eventos do "agent" são `BackendEventMsg` com `Event.Type` diferente.
O TUI não sabe nem precisa saber se está em "agent mode" — ele só renderiza eventos.

### Key Bindings

```go
// cli/tui/keymap.go

type KeyMap struct {
    Submit        key.Binding  // Enter (submit input)
    NewLine       key.Binding  // Shift+Enter / Alt+Enter (nova linha)
    Cancel        key.Binding  // Ctrl+C (cancela operação / limpa input)
    Quit          key.Binding  // Ctrl+D (sair)
    ScrollUp      key.Binding  // PgUp / Ctrl+U
    ScrollDown    key.Binding  // PgDn / Ctrl+D
    ToggleSidebar key.Binding  // Ctrl+B
    Rewind        key.Binding  // Esc+Esc (double-press)
    TabComplete   key.Binding  // Tab
    HistoryUp     key.Binding  // Up arrow (quando input vazio)
    HistoryDown   key.Binding  // Down arrow (quando input vazio)
    FocusNext     key.Binding  // Tab (cycle focus entre panels)
    ApproveYes    key.Binding  // Y (no overlay de aprovação)
    ApproveNo     key.Binding  // N (no overlay de aprovação)
    ApproveAlways key.Binding  // A (no overlay de aprovação)
    Help          key.Binding  // ? ou F1
}
```

### Critérios de conclusão

- [x] `cli/tui/` package criado (9 arquivos)
- [x] `Model` implementa `tea.Model` (Init, Update, View) em `model.go`
- [x] Layout 3-panel renderiza com lipgloss (header, viewport, input, sidebar, footer)
- [x] Key bindings definidos em `keymap.go`
- [x] Mensagens/Event types definidos em `messages.go`
- [x] `Backend` interface definida com `Event` unificado em `app.go`
- [x] Components: HeaderModel, FooterModel, SidebarModel, SpinnerModel
- [x] Styles centralizados em `styles.go` (tema dark inspirado no OpenCode)
- [x] `go build ./...` passa
- [x] `go test ./...` passa (zero impacto nos testes existentes)

---

## Fase 2: Modo Unificado no TUI

**Status**: [~] Em progresso (2026-03-11) — chat streaming, OutputEmitter, autocomplete e input history implementados. Falta wiring do approval overlay e integração completa do ReAct loop com emitter.

### Objetivo

Wiring completo: o TUI Bubble Tea funciona com o modo unificado.
O user digita qualquer coisa e o backend decide se precisa de tools ou não.

### Mudanças

#### 1. Adapter: `cli/tui/adapter.go`

Implementa `Backend` delegando para `*ChatCLI` e o `AgentMode` loop:

```go
type ChatCLIAdapter struct {
    cli *ChatCLI
}

func (a *ChatCLIAdapter) SendMessage(ctx context.Context, input string) (<-chan Event, error) {
    ch := make(chan Event, 32)

    go func() {
        defer close(ch)

        // 1. Se é /command, processa direto
        if strings.HasPrefix(input, "/") || strings.HasPrefix(input, "@") {
            output, shouldExit, err := a.handleCommand(input)
            if err != nil {
                ch <- Event{Type: EventError, Error: err}
                return
            }
            if output != "" {
                ch <- Event{Type: EventTextDelta, Text: output}
            }
            ch <- Event{Type: EventDone}
            return
        }

        // 2. Envia para o loop ReAct unificado
        a.runUnifiedLoop(ctx, input, ch)
    }()

    return ch, nil
}

func (a *ChatCLIAdapter) runUnifiedLoop(ctx context.Context, input string, ch chan<- Event) {
    // Monta system prompt unificado
    // Injeta workspace context + attached contexts
    // Injeta tool catalog (todos os plugins)
    // Envia para LLM via streaming
    // Se resposta contém tool_calls:
    //   - Emite EventToolStart
    //   - Executa tool
    //   - Emite EventToolResult
    //   - Alimenta resultado e faz novo turn
    // Se resposta é texto puro:
    //   - Emite EventTextDelta (streaming)
    //   - Emite EventDone
    //   - Para
}
```

#### 2. System Prompt Unificado: `cli/prompts.go`

```go
// Novo system prompt que combina o melhor dos 3 modos
func buildUnifiedSystemPrompt(cli *ChatCLI) string {
    var sb strings.Builder

    // Base: capabilities + behavior rules
    sb.WriteString(UnifiedSystemPrompt)

    // Workspace context (@SOUL.md, @RULES.md, etc.)
    if ctx := cli.contextBuilder.BuildContext(); ctx != "" {
        sb.WriteString("\n## Workspace Context\n")
        sb.WriteString(ctx)
    }

    // Persona (se ativa)
    if persona := cli.personaHandler.GetSystemPrompt(); persona != "" {
        sb.WriteString("\n## Persona\n")
        sb.WriteString(persona)
    }

    // Attached contexts
    if attached := cli.contextHandler.BuildSystemContext(); attached != "" {
        sb.WriteString("\n## Attached Context\n")
        sb.WriteString(attached)
    }

    return sb.String()
}
```

#### 3. Loop ReAct no adapter (reutiliza agent_mode.go)

Em vez de reescrever o loop ReAct, o adapter usa o `AgentMode` existente
mas redireciona o output para o channel de `Event`:

```go
func (a *ChatCLIAdapter) runUnifiedLoop(ctx context.Context, input string, ch chan<- Event) {
    // Cria um EventEmitter que o AgentMode usa em vez de fmt.Print
    emitter := &EventEmitter{ch: ch}

    // Reutiliza a lógica do AgentMode com emitter
    a.cli.agentMode.RunWithEmitter(ctx, input, emitter)
}

// EventEmitter implementa uma interface que AgentMode usa para output
type EventEmitter struct {
    ch chan<- Event
}

func (e *EventEmitter) StreamText(text string)          { e.ch <- Event{Type: EventTextDelta, Text: text} }
func (e *EventEmitter) ToolStart(name, args string)     { e.ch <- Event{Type: EventToolStart, Tool: &ToolEvent{Name: name, Args: args, Status: "running"}} }
func (e *EventEmitter) ToolResult(name, output string)  { e.ch <- Event{Type: EventToolResult, Tool: &ToolEvent{Name: name, Output: output, Status: "done"}} }
func (e *EventEmitter) NeedApproval(cmd string) ApprovalResponse { ... }
```

#### 4. OutputEmitter interface: `cli/output_emitter.go` ✅

Implementado com interface completa e dois emitters:

```go
// cli/output_emitter.go (IMPLEMENTADO)

type OutputEmitter interface {
    EmitText(text string)
    EmitLine(text string)
    EmitLinef(format string, args ...interface{})
    EmitTurnStart(turn, maxTurns int)
    EmitTurnEnd(turn, maxTurns int, duration time.Duration, toolCalls, agents int)
    EmitToolStart(toolName, description string)
    EmitToolResult(toolName string, exitCode int, output string, duration time.Duration)
    EmitThinking(model string, duration time.Duration)
    EmitThinkingDone()
    EmitMarkdown(icon, title, content, color string)
    EmitStatus(text string)
    EmitError(text string)
    ClearLine()
    ClearLines(n int)
}

// terminalEmitter — default, usa fmt.Print (backward compat para go-prompt e one-shot)
// TUIEmitter — em cli/tui/emitter.go, converte chamadas em Event no channel do TUI
```

**Wiring:**
- `AgentMode` tem campo `emitter OutputEmitter` (default: `terminalEmitter`)
- `SetEmitter(e)` permite injetar o `TUIEmitter` quando rodando no Bubble Tea
- `CLIBridge.SetAgentEmitter()` chamado pelo adapter antes de cada `sendToLLM`
- Chamadas `fmt.Print*` no `processAIResponseAndAct` substituídas por `a.emitter.*`
  (tipo type-ahead notification, timer display, turn stats, dispatch progress, completion/warning)

#### 5. Input component com autocomplete ✅

Implementado em `cli/tui/model.go`:

- `charmbracelet/bubbles/textarea` para input multiline
- Tab abre overlay com completions filtradas por prefixo (query ao `backend.GetCompletions`)
- Tab repetido cicla entre candidatos; Enter aceita a seleção
- Qualquer outra tecla dismisses o overlay
- Shift+Enter / Alt+Enter para nova linha
- Up/Down navega histórico quando input é single-line (sem `\n`)
- Histórico salvo em `inputHistory []string`, draft preservado ao navegar
- Paste (multiline) funciona nativamente no textarea

#### 6. Viewport com streaming

- Recebe `BackendEventMsg` e renderiza conforme o tipo:
  - `EventTextDelta` → appenda texto ao bloco atual do assistant
  - `EventToolStart` → mostra inline "⏳ Running @coder write..."
  - `EventToolResult` → converte para bloco completo com output
  - `EventNeedApproval` → ativa overlay de aprovação
  - `EventDone` → aplica markdown rendering final no bloco
- Auto-scroll para bottom (sticky) a menos que user scrollou para cima
- Mensagens renderizadas com borders coloridos (user vs assistant vs tool)

#### 7. `/agent` e `/coder` como hints

```go
// cli/tui/adapter.go

func (a *ChatCLIAdapter) SendMessage(ctx context.Context, input string) (<-chan Event, error) {
    // Detecta hints
    if strings.HasPrefix(input, "/agent ") {
        query := strings.TrimPrefix(input, "/agent ")
        // Adiciona hint ao system prompt desta requisição
        return a.sendWithHint(ctx, query, "Focus on orchestration: use all available tools to accomplish the task autonomously.")
    }
    if strings.HasPrefix(input, "/coder ") {
        query := strings.TrimPrefix(input, "/coder ")
        return a.sendWithHint(ctx, query, "Focus on code editing: read files, make changes, run tests. Use @coder tools for all file operations.")
    }
    if strings.HasPrefix(input, "/run ") {
        query := strings.TrimPrefix(input, "/run ")
        return a.sendWithHint(ctx, query, "Focus on orchestration: use all available tools to accomplish the task autonomously.")
    }

    // Input normal — sem hint, LLM decide sozinho
    return a.send(ctx, input)
}
```

#### 8. Branching em `cli/cli.go` Start()

```go
func (cli *ChatCLI) Start(ctx context.Context) {
    defer cli.cleanup()

    if cli.shouldUseBubbleTea() {
        cli.startBubbleTea(ctx)
        return
    }

    // ... go-prompt code existente (mantido até Fase 4)
}

func (cli *ChatCLI) startBubbleTea(ctx context.Context) {
    adapter := &tui.ChatCLIAdapter{CLI: cli}
    app := tui.New(adapter)
    p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
    if _, err := p.Run(); err != nil {
        cli.logger.Fatal("TUI error", zap.Error(err))
    }
}

func (cli *ChatCLI) shouldUseBubbleTea() bool {
    return os.Getenv("CHATCLI_TUI") == "bubbletea"
}
```

### Funcionalidades que devem funcionar

- [x] Enviar prompt e receber resposta com streaming
- [x] LLM usa tools automaticamente quando necessário (sem /agent ou /coder)
- [x] Tool calls renderizam inline (pending) e block (completed)
- [x] Diffs para file edits
- [x] Approval overlay para comandos perigosos
- [x] Streaming em tempo real (token por token)
- [x] Markdown rendering
- [x] Autocomplete de /commands
- [x] Histórico de comandos (Up/Down)
- [x] Ctrl+C cancela operação
- [x] Ctrl+D sai
- [x] Paste multiline
- [x] Todos os /commands funcionando
- [x] @file, @command, @url
- [x] /switch provider
- [x] /session save/load
- [x] /context attach/detach
- [x] /memory
- [x] /persona
- [x] /help
- [x] `/agent <query>` funciona como hint (mesmo viewport)
- [x] `/coder <query>` funciona como hint (mesmo viewport)
- [x] `/run <query>` funciona como hint (mesmo viewport)

### Critérios de conclusão

- [x] `CHATCLI_TUI=bubbletea chatcli` abre o TUI (branching em cli.go Start())
- [x] `CLIBridge` interface + `tuiBridge` implementação em `cli/tui_bridge.go`
- [x] `Adapter` implementa `Backend` em `cli/tui/adapter.go`
- [x] Streaming LLM → `Event` channel → viewport rendering
- [x] `/agent`, `/coder`, `/run` tratados como hints (sem panic)
- [x] `/commands` executados via `captureStdout` + panic recovery
- [x] `stream_cmd.go` com `listenNext` para encadear leitura do channel
- [x] `markdown.go` com glamour rendering
- [x] go-prompt mode ainda funciona (sem `CHATCLI_TUI`)
- [x] `go build ./...` e `go test ./...` passam (32 pacotes OK)
- [x] `OutputEmitter` interface implementada (`cli/output_emitter.go`) com `RequestApproval`
- [x] `terminalEmitter` (backward compat) e `TUIEmitter` (`cli/tui/emitter.go`)
- [x] `AgentMode.SetEmitter()` + `CLIBridge.SetAgentEmitter()` wiring completo
- [x] `fmt.Print*` no ReAct loop substituídos por `a.emitter.*` (timer, turn stats, dispatch progress)
- [x] Autocomplete overlay funciona (Tab para abrir, ciclar, Enter para aceitar)
- [x] Histórico de input funciona (Up/Down em single-line mode)
- [x] Wiring do ReAct loop: `/agent`, `/coder`, `/run` → `runAgentLoop` → `bridge.RunAgentLoop` → `AgentMode.Run`
- [x] `CLIBridge.RunAgentLoop()` + `tuiBridge` implementação delegando para `agentMode.Run()`
- [x] Approval overlay funcional: `TUIEmitter.RequestApproval` → `EventNeedApproval` com `ResponseCh` → `handleApproval` envia resposta e resume `listenNext`
- [x] `pendingApproval` field no Model para armazenar request ativo
- [x] Tool calls renderizam no viewport: `EmitToolStart`/`EmitToolResult` em `executeCommandsWithOutput`
- [x] `fmt.Print*` em `executeCommandsWithOutput` substituídos por `a.emitter.*` (script output, command output, errors, meta, dangerous command approval)
- [x] Dangerous commands: `a.emitter.RequestApproval()` substitui `getCriticalInput()` + stdin — TUI usa overlay, terminal usa fallback
- [x] Substituir `renderer.*` no ReAct loop por `a.emitter.*` — timeline events, tool calls, batch header/summary agora passam pelo OutputEmitter
- [x] `handleCommandBlocks` interactive menu migrated to emitter — all `renderer.Print*`, `ClearScreen`, `ShowInPager`, `fmt.Print*` replaced with `a.emitter.*`

---

## Fase 3: Sidebar, Header, Footer

**Status**: [x] Completo (2026-03-11) — Header, Footer, Sidebar completos. Token usage, cost, tasks, modified files e MCP servers wired.

### Objetivo

Implementar completamente os painéis informativos ao redor do viewport.

### Sidebar (42 chars, direita)

```
┌─────────────────┐
│ Session          │
│ ─────────        │
│ My Chat Session  │
│                  │
│ Context          │
│ ─────────        │
│ 12,345 tokens    │
│ ██████░░░░ 62%   │
│ ~$0.08 spent     │
│                  │
│ Tasks            │
│ ─────────        │
│ ✅ Read files    │
│ ⏳ Build code    │
│ ○ Run tests      │
│                  │
│ Modified         │
│ ─────────        │
│ auth/login.go    │
│   +30 -15        │
│ cli/cli.go       │
│   +5 -1          │
│                  │
│ MCP Servers      │
│ ─────────        │
│ ● github (ok)    │
│ ● postgres (ok)  │
└─────────────────┘
```

**Comportamento responsivo:**
- Terminal >= 120 cols: sidebar visível ao lado
- Terminal < 120 cols: sidebar oculta, toggle com Ctrl+B (overlay)

### Header (1 linha)

```
ChatCLI · claude-sonnet-4-5 · CLAUDEAI                   12,345 tokens (62%) · $0.08
```

### Footer (1 linha)

```
~/GolandProjects/chatcli   ● 2 MCP   Ctrl+B sidebar   ? help
```

Note: **sem indicador de "modo"** — não existe mais Chat/Agent/Coder no footer.

### Seções colapsáveis na sidebar

Cada seção (Tasks, Modified, MCP) pode ser colapsada/expandida via click ou tecla.
Se seção tem > 3 items, mostra collapsed por padrão com contador.

### Critérios de conclusão

- [x] Header mostra brand (ChatCLI), model, provider, session, tokens, cost com cores distintas
- [x] Footer mostra cwd (com ~/... shortening), hints contextuais (Ctrl+C/B/D, ? help)
- [x] Sidebar renderiza Session, Context (progress bar + tokens + cost), Tasks, Modified files
- [x] Sidebar auto-hide em terminals estreitos (< 120 cols)
- [x] Ctrl+B toggle sidebar
- [x] Seções colapsáveis (TasksCollapsed, FilesCollapsed) com truncamento e "+N more"
- [x] Token usage acumula de streaming responses via `accumulateUsage()`
- [x] Cost estimate calcula automaticamente (pricing Sonnet como default)
- [x] Context window limit via `catalog.GetContextWindow()` → progress bar no sidebar
- [x] Task list via `GetAgentTasks()` → `taskTracker.GetPlan()` → sidebar Tasks section
- [x] Modified files via `git diff --numstat HEAD` + `git ls-files --others` (`cli/tui/git_status.go`)
- [x] MCP servers section no sidebar — `MCPServer` type, connected/disconnected status, tool count, collapsible

---

## Fase 4: Remoção do go-prompt e modos legados

**Status**: [x] Completo (2026-03-11) — TUI é o único modo. go-prompt removido. isCoderMode removido. Todos os renderer.* migrados para emitter.

### Objetivo

Remover go-prompt, panic/recover, e os 3 modos separados. O TUI unificado é o único modo.

### Remoções

#### Arquivos para deletar / simplificar
- ~~`cli/paste/`~~ → simplificado: removido `BracketedPasteParser` (go-prompt), mantido `DetectInLine` (usado por agent_mode.go)
- `cli/signal_unix.go` / `cli/signal_windows.go` — já não existiam
- `cli/stdin_ready_unix.go` / `cli/stdin_ready_windows.go` — já não existiam

#### Código removido de `cli/cli.go` ✅
- ~~Import de `github.com/c-bata/go-prompt`~~ ✅
- ~~`ExecutionProfile` type e constantes~~ ✅
- ~~`executionProfile` field do struct `ChatCLI`~~ ✅
- ~~`var CommandFlags`~~ ✅
- ~~Função `completer()` e todas as `get*Suggestions()`~~ ✅ (~1500 linhas)
- ~~Função `executor()` inteira~~ ✅
- ~~`processLLMRequest()` inteira~~ ✅
- ~~`changeLivePrefix()`~~ ✅
- ~~`handleCtrlC()` / `handleEscape()`~~ ✅
- ~~`prefixSpinnerIdx` / `forceRefreshPrompt()`~~ ✅
- ~~`pendingAction` / panic/recover no `Start()`~~ ✅
- ~~`prompt.New()` config + ASCII code bindings~~ ✅
- ~~`pasteParser` / `lastPasteInfo`~~ ✅
- ~~`runAgentLogic()` / `runCoderLogic()` / `runWithCancellation()`~~ ✅
- `AnimationManager` — mantido (usado por one-shot mode e file processing)

#### Código removido de `cli/agent_mode.go` ✅
- ~~`isCoderMode` flag e toda lógica condicional baseada nele~~ ✅ (25+ referências removidas)
- ~~Tool filtering~~ ✅ (todos plugins disponíveis em todos modos)
- ~~`coderMinimal`/`coderMinUI` branching~~ ✅ (UI unificada)
- ~~Strict coder validations (reasoning required, tool_call only)~~ ✅
- ~~Execute block rejection in coder mode~~ ✅ (ambos formatos aceitos)
- ~~Type-ahead guard in coder mode~~ ✅ (sempre permitido)
- Security policy + dangerous exec guard: agora rodam incondicionalmente
- Task tracking + plan progress: agora ativos em todos os modos
- `fmt.Print*` calls diretas → substituídas por `OutputEmitter` interface (✅ feito na Fase 2)

#### Código removido de `cli/command_handler.go` ✅
- ~~`panic(agentModeRequest)` e `panic(coderModeRequest)`~~ ✅ (substituídos por mensagens informativas)
- ~~`forceRefreshPrompt()` call em `/clear`~~ ✅

#### `cli/prompts.go` — mantido como hint prompts
- `CoderSystemPrompt` — usado como system prompt quando `/coder` é o hint
- `CoderFormatInstructions` — combinado com persona em modo coder
- `AgentFormatInstructions` — combinado com persona em modo agent
- Nota: não são "modos" separados; são variantes de prompt para o mesmo loop unificado

#### go.mod ✅
- ~~Remover `github.com/c-bata/go-prompt`~~ ✅
- ~~Executar `go mod tidy`~~ ✅

### One-shot mode (-p flag)

**NÃO é afetado.** One-shot mode nunca usa go-prompt nem TUI:

```go
func (cli *ChatCLI) RunOnce(ctx context.Context, prompt string) {
    // Chama SendPrompt direto, imprime resultado, sai
    // Sem TUI, sem go-prompt
}
```

### Critérios de conclusão

- [x] TUI é o modo default (`Start()` entra em Bubble Tea sem env var)
- [x] Bug fix: `startStream` passava `nil` context → agora usa `context.Background()`
- [x] `/exit` → `EventExit` → `tea.Quit` (sai do TUI corretamente)
- [x] `/clear` / `/reset` → `EventClear` (limpa viewport sem afetar terminal)
- [x] `/rewind` → `EventRewind` (remove último par user+assistant do viewport)
- [x] `@file`, `@command`, `@url` rotas para `sendToLLM` (não `handleCommand`)
- [x] Welcome screen com dicas de comandos
- [x] `startBubbleTea` garante `agentMode` inicializado
- [x] `go build ./...` e `go test ./...` passam (32 pacotes OK)
- [x] Remover go-prompt do go.mod e go.sum (`go mod tidy` limpo)
- [x] `ExecutionProfile` e 3 modos removidos
- [x] Panic/recover removido de `Start()` e `command_handler.go`
- [x] `CommandFlags`, `completer()`, `executor()`, `processLLMRequest()` removidos
- [x] `handleCtrlC`, `handleEscape`, `changeLivePrefix`, `forceRefreshPrompt` removidos
- [x] `runAgentLogic()`, `runCoderLogic()`, `runWithCancellation()` removidos
- [x] Todas as funções `get*Suggestions()` (go-prompt completer) removidas
- [x] `cli/paste/detector.go` simplificado (removido `BracketedPasteParser`, mantido `DetectInLine`)
- [x] `tui_bridge.go` limpo (removido panic recovery para sentinels)
- [x] `memory_command.go` limpo (removido `getMemorySuggestions`)
- [x] `tools/docgen/main.go` atualizado (dados inline, sem dependência de cli)
- [x] `cli/cli.go` sem referências a go-prompt (reduzido de 4306 → 2483 linhas)
- [x] One-shot mode funciona (não afetado)
- [x] `isCoderMode` removido de agent_mode.go (25+ referências, modo unificado)
- [x] `isCoderMinimalUI()` dead code removido
- [x] `sanitizeToolCallArgs` simplificado (parâmetro `isCoderMode` removido)
- [x] Âncora de turno unificada (único reminder para ambos formatos)
- [x] Todos os testes passam (`go test ./...` OK)
- [x] Cross-compilation OK para macOS (arm64), Linux (amd64), Windows (amd64)

---

## Inventário de Funcionalidades

Checklist completo de tudo que deve funcionar no TUI final.

### Input

- [x] Digitação normal de texto (textarea.Model)
- [x] Multiline (Shift+Enter / Alt+Enter → NewLine key binding)
- [x] Paste (bracketed paste, multiline) — Bubble Tea textarea handles paste natively; DetectInLine available for agent stdin
- [x] Histórico de comandos (Up/Down) — `navigateHistory()` com draft preservation
- [x] Autocomplete de /commands com Tab — `updateCompletions()` + overlay
- [x] Autocomplete de @file, @command, @url — `completeFilePath()` com directory listing
- [x] Autocomplete de subcomandos (/switch providers, /session sub, /context sub, /memory sub)
- [x] Word navigation (Ctrl+Arrow, Alt+Arrow) — handled by textarea.Model
- [x] Ctrl+C cancela operação / limpa input — `Cancel` key binding
- [x] Ctrl+D sai do programa — `Quit` key binding
- [x] Esc+Esc rewind (undo último turno) — double-escape dentro de 500ms triggers rewind

### Modo Unificado (substitui Chat + Agent + Coder)

- [x] Pergunta simples → resposta texto em streaming (`sendToLLM`)
- [x] Pedido de código → LLM usa tools automaticamente (`runAgentLoop`)
- [x] Tarefa complexa → LLM faz multi-turn ReAct com reasoning (`RunAgentLoop`)
- [x] Markdown rendering (code blocks, bold, italic, lists) — `RenderMarkdown` via glamour
- [x] Syntax highlighting em code blocks — glamour renders with syntax highlighting
- [x] Tool calls renderizam inline (pending) e block (completed) — `EventToolStart`/`EventToolResult`
- [x] Diffs para file edits — rendered by agent OutputEmitter
- [x] Approval overlay para comandos perigosos (yes/no/always/skip) — `StateApproval` + key bindings
- [x] Plan/tasks display na sidebar — `GetTasks()` → sidebar Tasks section
- [x] Scroll para cima/baixo no viewport — `ScrollUp`/`ScrollDown` key bindings
- [x] Auto-scroll sticky (segue novas mensagens) — `GotoBottom()` em `updateViewportContent`
- [x] `/agent <query>` funciona como hint (orquestração) — `extractHint` → `runAgentLoop`
- [x] `/coder <query>` funciona como hint (edição de código) — `extractHint` → `runAgentLoop`
- [x] `/run <query>` funciona como hint (orquestração) — `extractHint` → `runAgentLoop`

### Commands

- [x] `/help` — mostra ajuda (via `handleCommand` → `captureStdout`)
- [x] `/exit` ou `/quit` — sai (`EventExit` → `tea.Quit`)
- [x] `/clear` — limpa histórico (`EventClear`)
- [x] `/history` — mostra histórico (via `handleCommand`)
- [x] `/switch` — troca provider/model (via `handleCommand`)
- [x] `/session new/save/load/list/delete` (via `handleCommand`)
- [x] `/context create/attach/detach/list/delete/show/merge/attached/export/import/metrics/help` (via `handleCommand`)
- [x] `/connect` / `/disconnect` — remote server (via `handleCommand`)
- [x] `/agent list/load/attach/detach/skills/show/status/off/help` (via `handleCommand`)
- [x] `/persona` (via `handleCommand`)
- [x] `/skill` (via `handleCommand`)
- [x] `/memory load/save/search/list/forget/help` (via `handleCommand`)
- [x] `/auth login/logout/status` (via `handleCommand`)
- [x] `/nextchunk` — próximo chunk de @file (via `handleCommand`)
- [x] `/retrychunk` — retry chunk que falhou (via `handleCommand`)
- [x] `/rewind` — undo último turno (`EventRewind`)
- [x] `/compact` — compacta histórico (via `handleCommand`)
- [x] `/tokens` — mostra token count (via `handleCommand`)
- [x] `@file <path> [--mode full|summary|chunked|smart]` (via `ProcessSpecialCommands`)
- [x] `@command <cmd> [-i] [--ai]` (via `ProcessSpecialCommands`)
- [x] `@url <url>` (via `ProcessSpecialCommands`)

### Visual

- [x] Header com session/model/provider/tokens/cost
- [x] Footer com cwd/status
- [x] Sidebar com context/tasks/files/mcp
- [x] Sidebar responsiva (auto-hide < 120 cols)
- [x] Cores consistentes (user vs assistant vs tool vs error) — Styles definidos
- [x] Spinner durante loading — `SpinnerModel`
- [x] Progress bar para token usage — `renderBar()`

### Plataforma

- [x] macOS — cross-compile OK (runtime testing: manual)
- [x] Linux — cross-compile OK (runtime testing: manual)
- [x] Windows — cross-compile OK (runtime testing: manual)

---

## Referência de Arquivos

### Arquivos que SERÃO modificados

| Arquivo | Fase | Tipo de mudança | Status |
|---------|------|-----------------|--------|
| `cli/cli.go` | 2, 4 | Branch Start() para TUI (Fase 2), remover go-prompt + ExecutionProfile + MCP init (Fase 4) | ✅ Completo |
| `cli/agent_mode.go` | 2, 4 | OutputEmitter field + SetEmitter (Fase 2), remover isCoderMode (Fase 4) | ✅ Completo |
| `cli/agent/ui_renderer.go` | 2 | ReAct loop calls migrated to emitter; interactive menu path still uses renderer directly | ✅ Parcial |
| `cli/command_handler.go` | 2, 4 | /agent e /coder viram hints (Fase 2), remover panic (Fase 4) | ✅ Completo |
| `cli/prompts.go` | 2, 4 | Mantido como hint prompts (Coder/Agent format instructions) | ✅ Completo |
| `cli/coder/policy_manager.go` | 2 | Ativado sempre (não só em coder mode) | ✅ Completo |
| `cli/tui_bridge.go` | 2, 3 | CLIBridge impl + GetMCPServers | ✅ Completo |
| `go.mod` | 1, 4 | Adicionar bubbletea/bubbles (Fase 1), remover go-prompt (Fase 4) | ✅ Completo |

### Arquivos que SERÃO criados

| Arquivo | Fase | Status |
|---------|------|--------|
| `llm/client/stream.go` | 0 | ✅ |
| `llm/client/stream_test.go` | 0 | ✅ |
| `cli/tui/app.go` | 1 | ✅ |
| `cli/tui/model.go` | 1 | ✅ (+ autocomplete e history na Fase 2) |
| `cli/tui/keymap.go` | 1 | ✅ |
| `cli/tui/styles.go` | 1 | ✅ |
| `cli/tui/messages.go` | 1 | ✅ |
| `cli/tui/adapter.go` | 2 | ✅ |
| `cli/tui/stream_cmd.go` | 2 | ✅ |
| `cli/tui/markdown.go` | 2 | ✅ |
| `cli/tui/emitter.go` | 2 | ✅ (TUIEmitter — converte emitter calls em Events) |
| `cli/output_emitter.go` | 2 | ✅ (OutputEmitter interface + terminalEmitter) |
| `cli/tui_bridge.go` | 2 | ✅ (tuiBridge implementa CLIBridge) |
| `cli/tui/components/header.go` | 1 | ✅ |
| `cli/tui/components/footer.go` | 1 | ✅ |
| `cli/tui/components/sidebar.go` | 1 | ✅ (básico, completar na Fase 3) |
| `cli/tui/components/spinner.go` | 1 | ✅ |
| `cli/tui/components/input.go` | 1 | — (inline no model.go via bubbles/textarea) |
| `cli/tui/components/viewport.go` | 1 | — (inline no model.go via bubbles/viewport) |
| `cli/tui/components/completer.go` | 2 | — (inline no model.go) |
| `cli/tui/git_status.go` | 3 | ✅ (git diff --numstat para modified files) |
| `cli/tui/components/diff.go` | 3 | ✅ (unified diff renderer with syntax coloring) |
| `cli/tui/components/tool_view.go` | 3 | ✅ (tool call cards: start/result/compact, wired to model.go) |
| `cli/tui/components/approval.go` | 2 | — (inline no model.go via handleApproval) |

### Arquivos que SERÃO deletados (Fase 4)

| Arquivo | Razão |
|---------|-------|
| `cli/paste/` (todo o diretório) | Bubble Tea textarea lida com paste |
| `cli/signal_unix.go` | Bubble Tea gerencia SIGWINCH |
| `cli/signal_windows.go` | Bubble Tea gerencia resize |
| `cli/stdin_ready_unix.go` | Não mais necessário |
| `cli/stdin_ready_windows.go` | Não mais necessário |

### Arquivos que NÃO serão tocados

- `auth/` — inteiro
- `config/` — inteiro
- `models/` — inteiro
- `server/` — inteiro
- `client/remote/` — inteiro
- `i18n/` — inteiro
- `k8s/` — inteiro
- `utils/` — inteiro
- `version/` — inteiro
- `cmd/` — inteiro (exceto se precisar adicionar flag --tui)

---

## Decisões Técnicas

### Por que unificar os 3 modos?

**Problema**: O user precisa decidir se quer `/agent`, `/coder` ou chat simples.
No OpenCode e Claude Code, o user só digita e o LLM decide.

**Os 3 modos são variações do mesmo loop ReAct** com diferenças em:
1. System prompt — o que o LLM "sabe fazer"
2. Tools disponíveis — quais plugins são oferecidos
3. Nível de segurança — quão restrito é a execução

**Solução**: Um único loop com:
- System prompt completo (todas as capabilities)
- Todas as tools disponíveis
- Segurança máxima sempre ativa (CommandValidator + PolicyManager)
- LLM decide quando usar tools vs responder texto

**`/agent` e `/coder` viram hints**: adicionam uma instrução ao prompt da requisição
sem mudar o loop, as tools, ou a UI. São atalhos opcionais, não modos separados.

### Por que Bubble Tea e não tview/tcell direto?

- Bubble Tea tem arquitetura Elm (Model/Update/View) que facilita composição
- Ecossistema charmbracelet (bubbles, lipgloss, glamour) já é dependency do projeto
- Comunidade ativa, bem documentado, usado em produção por muitos CLIs Go

### Por que não manter go-prompt para input?

- go-prompt.Run() é blocking e dono do terminal — incompatível com Bubble Tea
- Não existe coexistência possível: ambos querem controlar stdin/stdout
- Bubble Tea's textarea dá tudo que go-prompt dava (e mais)

### Por que OutputEmitter em vez de reescrever agent_mode.go?

- 3.832 linhas de lógica complexa e testada (XML parsing, tool routing, approval flow)
- Risco muito alto de introduzir bugs
- OutputEmitter é uma interface simples que o AgentMode usa em vez de `fmt.Print`
- Lógica de negócio fica intacta; apenas I/O muda
- O `defaultEmitter` mantém backward compat para one-shot mode

### Por que Backend interface em vez de passar *ChatCLI direto?

- Desacoplamento: TUI não precisa conhecer internals do ChatCLI
- Testabilidade: pode criar mock Backend para testes do TUI
- Flexibilidade: pode ter outros backends (remote, test, etc.)
- O tipo `Event` unificado significa que o TUI **não sabe** se está "em agent mode" — ele só renderiza eventos

### Como lidar com one-shot mode?

- One-shot mode (`-p` flag) NUNCA usa TUI — chama SendPrompt direto
- `RunOnce()` e `RunAgentOnce()` ficam inalterados
- Zero impacto na migração

### Como garantir Windows funciona?

- Bubble Tea tem suporte nativo a Windows (via tcell/Windows Console API)
- Remover go-prompt RESOLVE o bug de "close of closed channel" no Windows
- Testar: cmd.exe, PowerShell, Windows Terminal
