+++
title = "Modelos de IA Suportados"
linkTitle = "Modelos Suportados"
weight = 30
description = "Uma lista de referência para todos os modelos de Inteligência Artificial suportados nativamente pelo ChatCLI em cada provedor."
+++

O **ChatCLI** foi projetado para ser flexível, suportando uma ampla gama de modelos dos principais provedores de IA. Esta página serve como uma referência rápida para os modelos que são reconhecidos nativamente pelo nosso catálogo interno.

Você pode trocar de modelo a qualquer momento usando o comando `/switch --model <nome-do-modelo>`.

---

## OpenAI

Os modelos da OpenAI são ideais para uma vasta gama de tarefas, desde a geração de código até o raciocínio complexo.

| Nome do Modelo (ID) | Aliases | Nome de Exibição | Janela de Contexto |
| :------------------ | :------ | :--------------- | :----------------- |
| `gpt-5`             | `gpt-5-mini`, `gpt-5-nano` | GPT-5            | 50,000 tokens      |
| `gpt-4o`            | — | GPT-4o           | 50,000 tokens      |
| `gpt-4o-mini`       | — | GPT-4o mini      | 50,000 tokens      |
| `gpt-4`             | `gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano` | GPT-4 family     | 50,000 tokens      |

---

## Anthropic (Claude)

Os modelos da Anthropic são conhecidos por suas grandes janelas de contexto e excelente capacidade de seguir instruções complexas.

| Nome do Modelo (ID) | Aliases | Nome de Exibição | Janela de Contexto |
|:---------------------|:--------|:-----------------|:-------------------|
| `claude-sonnet-4-5` | `claude-4-5-sonnet`, `sonnet-4-5` | Claude Sonnet 4.5 | 200,000 tokens |
| `claude-opus-4-6` | `opus-4-6` | Claude Opus 4.6 | 400,000 tokens |
| `claude-opus-4-5` | `opus-4-5` | Claude Opus 4.5 | 200,000 tokens |
| `claude-opus-4-1-20250805` | `claude-opus-4-1`, `opus-4-1` | Claude Opus 4.1 | 20,000 tokens |
| `claude-opus-4-20250514` | `opus-4` | Claude Opus 4 | 20,000 tokens |
| `claude-sonnet-4` | `claude-4-sonnet`, `sonnet-4-20250514` | Claude Sonnet 4 | 50,000 tokens |
| `claude-sonnet-3-7-20250219` | `claude-3-7-sonnet` | Claude Sonnet 3.7 | 50,000 tokens |
| `claude-sonnet-3-5-20241022` | `claude-3-5-sonnet` | Claude Sonnet 3.5 | 50,000 tokens |
| `claude-opus-3` | `claude-3-opus` | Claude Opus 3 | 32,000 tokens |
| `claude-haiku-3` | `claude-3-haiku` | Claude Haiku 3 | 42,000 tokens |

---

## Google (Gemini)

Os modelos Gemini do Google oferecém capacidades múltimodais avançadas e grandes janelas de contexto.

| Nome do Modelo (ID) | Aliases | Nome de Exibição | Janela de Contexto |
|:---------------------|:--------|:-----------------|:-------------------|
| `gemini-3` | `gemini-3-pro`, `gemini-3-pro-preview` | Gemini 3 Pro | 2,000,000 tokens |
| `gemini-2.5-pro` | `gemini-2.5-pro-latest` | Gemini 2.5 Pro | 2,000,000 tokens |
| `gemini-2.5-flash` | — | Gemini 2.5 Flash | 1,000,000 tokens |
| `gemini-2.5-flash-lite` | — | Gemini 2.5 Flash Lite | 1,000,000 tokens |
| `gemini-2.0-flash` | — | Gemini 2.0 Flash | 1,000,000 tokens |
| `gemini-2.0-flash-lite` | — | Gemini 2.0 Flash Lite | 1,000,000 tokens |

---

## xAI (Grok)

Modelos da xAI, conhecidos por sua integração de informações em tempo real e uma "personalidade" única.

| Nome do Modelo (ID) | Aliases | Nome de Exibição | Janela de Contexto |
| :------------------- | :------ | :--------------- |:-------------------|
| `grok-4-1` | `grok-4-1-fast` | Grok-4-1 | 2,000,000 tokens |
| `grok-4-fast` | `grok-4-fast-reasoning-latest`, `grok-4-0709` | Grok-4 | 2,000,000 tokens |
| `grok-3` | — | Grok-3 | 128,000 tokens |
| `grok-3-mini` | — | Grok-3 Mini | 128,000 tokens |
| `grok-code-fast-1` | — | Grok Code Fast 1 | 200,000 tokens |
---

## StackSpot (StackSpotAI)

Aceita todos os modelos compativeis na plataforma StackSpotAI, desde que selecionado junto a criação do Agent.

---
## Ollama (Modelos Locais)

O ChatCLI suporta qualquer modelo que você tenha localmente através do Ollama. Para usar, certifique-se de que o provedor Ollama está habilitado e defina o nome do modelo.

1.  **Habilite no seu arquivo `.env`:**
    ```env
    OLLAMA_ENABLED=true
    ```
2.  **Defina o modelo no `.env` ou via comando:**
    ```env
    # No .env
    OLLAMA_MODEL="llama3"
    ```
    Ou no ChatCLI:
    ```bash
    /switch --model llama3
    ```

Você pode usar qualquer modelo que tenha baixado com `ollama pull <nome-do-modelo>`.

---