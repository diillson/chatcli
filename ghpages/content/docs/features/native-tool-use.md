+++
title = "Tool Use Nativo (API Estruturada)"
linkTitle = "Tool Use Nativo"
weight = 76
description = "Chamadas de ferramentas via API nativa da OpenAI e Anthropic, com suporte a cache_control para otimizacao de KV cache."
icon = "build"
+++

O ChatCLI suporta **chamadas de ferramentas via API nativa** para OpenAI e Anthropic, substituindo a abordagem de XML embutido no prompt por chamadas estruturadas via API. Isso melhora a precisao, reduz o consumo de tokens e habilita otimizacoes de cache.

---

## Por que Tool Use Nativo?

| Aspecto | XML no Prompt | API Nativa |
|:--------|:-------------|:-----------|
| Precisao | Depende do modelo parsear XML | Estruturado pela API, sem ambiguidade |
| Tokens | XML consome tokens do context window | Campos separados, mais eficientes |
| Cache | Sem otimizacao | `cache_control:ephemeral` na Anthropic |
| Validacao | Manual | Automatica pela API do provedor |
| Compatibilidade | Qualquer provedor | OpenAI e Anthropic (outros continuam com XML) |

---

## Arquitetura

### Interface ToolAwareClient

A interface `ToolAwareClient` estende a `LLMClient` base com suporte a ferramentas:

```go
type ToolAwareClient interface {
    LLMClient
    SendPromptWithTools(ctx context.Context, prompt string, history []models.Message,
        tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error)
    SupportsNativeTools() bool
}
```

### Deteccao Automatica

A deteccao e feita via type assertion, sem configuracao necessaria:

```go
if client.IsToolAware(c) {
    tac, _ := client.AsToolAware(c)
    resp, err := tac.SendPromptWithTools(ctx, prompt, history, tools, maxTokens)
    // resp.ToolCalls contem as chamadas estruturadas
}
```

Provedores que nao implementam `ToolAwareClient` continuam funcionando normalmente via `SendPrompt`.

---

## Tipos de Dados

### ToolDefinition

Define uma ferramenta disponivel para o modelo:

```go
type ToolDefinition struct {
    Type     string          `json:"type"`     // "function"
    Function ToolFunctionDef `json:"function"`
}

type ToolFunctionDef struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    Parameters  map[string]interface{} `json:"parameters"` // JSON Schema
}
```

### ToolCall e ToolResult

Representam uma chamada de ferramenta pelo modelo e seu resultado:

```go
type ToolCall struct {
    ID        string                 `json:"id"`
    Type      string                 `json:"type"`       // "function"
    Name      string                 `json:"name"`
    Arguments map[string]interface{} `json:"arguments"`
}

type ToolResult struct {
    ToolCallID string `json:"tool_call_id"`
    Content    string `json:"content"`
    IsError    bool   `json:"is_error,omitempty"`
}
```

### LLMResponse

Resposta unificada que pode conter texto e/ou tool calls:

```go
type LLMResponse struct {
    Content    string     `json:"content"`
    ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
    Usage      *UsageInfo `json:"usage,omitempty"`
    StopReason string     `json:"stop_reason,omitempty"`
}
```

---

## Implementacoes por Provedor

### OpenAI

Usa o campo `tools` na API de Chat Completions:

- Envia ferramentas como array de `tools` com `tool_choice: "auto"`
- Processa `tool_calls` em `choices[0].message`
- Mensagens `tool` no historico vinculam resultado ao `tool_call_id`

### Anthropic (Claude)

Usa o campo `tools` na Messages API com otimizacao de cache:

- Envia ferramentas no campo `tools` do request
- System prompt inclui `cache_control: { type: "ephemeral" }` para reuso de KV cache
- Tool results sao enviados como content blocks `tool_result` em mensagens `user`
- Suporte a todos os tipos de autenticacao (API key, OAuth token)

### ContentBlock com Cache Control

Para Anthropic, o system prompt e dividido em blocos com controle de cache:

```go
type ContentBlock struct {
    Type         string        `json:"type"`
    Text         string        `json:"text"`
    CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type CacheControl struct {
    Type string `json:"type"` // "ephemeral"
}
```

O `cache_control:ephemeral` informa a Anthropic que o bloco do system prompt pode ser cacheado entre requests, reduzindo significativamente a latencia e custo em conversas longas.

---

## Integracao com Fallback

A cadeia de fallback (`llm/fallback`) suporta `SendPromptWithTools` automaticamente. Provedores sem suporte a tool use nativo sao ignorados na cadeia de tool calls, mas continuam disponiveis para requests de texto simples.
