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

| Nome do Modelo (ID) | Nome de Exibição | Janela de Contexto |
| :------------------ | :--------------- | :----------------- |
| `gpt-4o`            | GPT-4o           | 50,000 tokens      |
| `gpt-4o-mini`       | GPT-4o mini      | 50,000 tokens      |
| `gpt-5`             | GPT-5            | 50,000 tokens      |
| `gpt-4`             | GPT-4 family     | 50,000 tokens      |

---

## Anthropic (Claude)

Os modelos da Anthropic são conhecidos por suas grandes janelas de contexto e excelente capacidade de seguir instruções complexas.

| Nome do Modelo (ID)                                   | Nome de Exibição   | Janela de Contexto |
|:------------------------------------------------------| :----------------- | :----------------- |
| `claude-3-5-sonnet-20240620` (ou `claude-3.5-sonnet`) | Claude 3.5 Sonnet | 200,000 tokens     |
| `claude-3-opus-20240229` (ou `claude-3-opus`)         | Claude 3 Opus     | 200,000 tokens     |
| `claude-3-sonnet-20240229` (ou `claude-3-sonnet`)     | Claude 3 Sonnet   | 200,000 tokens     |
| `claude-3-haiku-20240307` (ou `claude-3-haiku`)       | Claude 3 Haiku    | 200,000 tokens     |
| `claude-4`                                            | Claude 4| 200,000 tokens|
| `claude-4-opus`                                       | Claude 4 Opus| 200,000 tokens|
| `claude-4-sonnet`                                     | Claude 4 Sonnet| 200,000 tokens|
| `claude-4-5-sonnet`                                   | Claude 4.5 | 200,000 tokens|

---

## Google (Gemini)

Os modelos Gemini do Google oferecem capacidades multimodais avançadas e grandes janelas de contexto.

| Nome do Modelo (ID)       | Nome de Exibição | Janela de Contexto |
|:--------------------------|:-----------------|:-------------------|
| `gemini-1.5-pro-latest`   | Gemini 1.5 Pro   | 1,000,000 tokens   |
| `gemini-1.5-flash-latest` | Gemini 1.5 Flash | 1,000,000 tokens   |
| `gemini-1.0-pro`/`flash`  | Gemini 1.0 Pro   | 32,000 tokens      |
| `gemini-2.0-pro`/`flash`  | Gemini 2.0 Pro   | 2000000 tokens     |
| `gemini-2.5-pro`/`flash`  | Gemini 2.5 Pro   | 2000000 tokens     |

---

## xAI (Grok)

Modelos da xAI, conhecidos por sua integração de informações em tempo real e uma "personalidade" única.

| Nome do Modelo (ID)     | Nome de Exibição | Janela de Contexto |
| :---------------------- | :--------------- |:-------------------|
| `grok-1`                | Grok-1           | 8,192 tokens       |
| `grok-1.5`              | Grok-1.5         | 128,000 tokens     |
| `grok-1.5-vision-preview` | Grok 1.5 Vision  | 128,000 tokens     |
| `grok-4` | Grok-4 | 200,000 tokens     |
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