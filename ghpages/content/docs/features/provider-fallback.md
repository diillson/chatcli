+++
title = "Fallback de Provedores"
linkTitle = "Fallback de Provedores"
weight = 75
description = "Configure failover automatico entre provedores LLM com classificacao inteligente de erros, cooldown exponencial e monitoramento de saude."
icon = "swap_horiz"
+++

O ChatCLI suporta uma **cadeia de failover automatico** entre provedores LLM. Quando o provedor primario falha (rate limit, timeout, erro de servidor), o sistema tenta automaticamente o proximo provedor na cadeia, de forma totalmente transparente.

---

## Como Funciona

A cadeia de fallback e uma lista ordenada de provedores. Cada request percorre a lista ate obter sucesso ou esgotar todas as opcoes:

```
Request → OpenAI (primario)
            ↓ falhou (rate limit)
          Claude (secundario)
            ↓ falhou (timeout)
          Google AI (terciario)
            ↓ sucesso ✓
          Resposta retornada ao usuario
```

---

## Configuracao

### Via Variaveis de Ambiente

```bash
# Lista ordenada de provedores (primeiro = maior prioridade)
export CHATCLI_FALLBACK_PROVIDERS="OPENAI,CLAUDEAI,GOOGLEAI"

# Modelo especifico por provedor (opcional)
export CHATCLI_FALLBACK_MODEL_OPENAI="gpt-4o"
export CHATCLI_FALLBACK_MODEL_CLAUDEAI="claude-sonnet-4-20250514"
export CHATCLI_FALLBACK_MODEL_GOOGLEAI="gemini-2.0-flash"

# Controle de retentativas e cooldown
export CHATCLI_FALLBACK_MAX_RETRIES="2"       # tentativas por provedor
export CHATCLI_FALLBACK_COOLDOWN_BASE="30s"    # cooldown base
export CHATCLI_FALLBACK_COOLDOWN_MAX="5m"      # cooldown maximo
```

### Via Flags do Servidor

```bash
chatcli server \
  --fallback-providers OPENAI,CLAUDEAI,GOOGLEAI \
  --fallback-max-retries 2 \
  --fallback-cooldown-base 30s \
  --fallback-cooldown-max 5m
```

### Via Helm Chart

```yaml
# values.yaml
fallback:
  enabled: true
  providers:
    - name: OPENAI
      model: gpt-4o
    - name: CLAUDEAI
      model: claude-sonnet-4-20250514
    - name: GOOGLEAI
      model: gemini-2.0-flash
  maxRetries: 2
  cooldownBase: "30s"
  cooldownMax: "5m"
```

---

## Classificacao de Erros

O sistema classifica automaticamente cada falha para decidir a estrategia:

| Classe | Comportamento | Exemplos |
|:-------|:-------------|:---------|
| `rate_limit` | Aguarda backoff, depois retenta | HTTP 429, "too many requests" |
| `timeout` | Retenta ate maxRetries | Deadline exceeded, connection timeout |
| `server_error` | Retenta ate maxRetries | HTTP 500, 502, 503 |
| `auth_error` | **Nao retenta** — avanca na cadeia | HTTP 401, 403, "invalid api key" |
| `model_not_found` | **Nao retenta** — avanca na cadeia | HTTP 404, "model not found" |
| `context_too_long` | **Nao retenta** — avanca na cadeia | "context length exceeded" |

---

## Cooldown Exponencial

Apos falhas consecutivas, o provedor entra em cooldown com backoff exponencial:

| Falhas Consecutivas | Cooldown |
|:------------------:|:--------:|
| 1 | 30s |
| 2 | 60s |
| 3 | 120s |
| 4 | 240s |
| 5+ | 300s (max) |

- Erros de autenticacao recebem cooldown maximo imediato (5m)
- Um request bem-sucedido limpa todo o cooldown do provedor
- Use `ResetCooldowns()` para limpar manualmente (ex: apos atualizar credenciais)

---

## Monitoramento de Saude

A cadeia rastreia o estado de cada provedor em tempo real:

```go
health := chain.GetHealth()
for _, h := range health {
    fmt.Printf("Provider: %s, Available: %v, Fails: %d, Cooldown: %v\n",
        h.Name, h.Available, h.ConsecutiveFails, h.CooldownUntil)
}
```

Campos rastreados por provedor:
- `Available` — se o provedor esta disponivel para requests
- `ConsecutiveFails` — numero de falhas consecutivas
- `LastErrorClass` — tipo da ultima falha
- `CooldownUntil` — quando o cooldown expira
- `LastErrorAt` — timestamp da ultima falha

---

## Tool Use com Fallback

A cadeia de fallback tambem suporta `SendPromptWithTools` para provedores que implementam a interface `ToolAwareClient`. Provedores sem suporte a tool use nativo sao automaticamente ignorados na cadeia de tool calls.

---

## Boas Praticas

1. **Ordene por custo-beneficio**: Coloque o provedor mais barato/rapido primeiro
2. **Diversifique provedores**: Misture provedores de diferentes empresas para resiliencia real
3. **Configure modelos por provedor**: Use modelos equivalentes em capacidade para manter qualidade
4. **Monitore a saude**: Verifique regularmente se algum provedor esta em cooldown persistente
5. **Cada provedor precisa de API key**: Configure as chaves de todos os provedores na cadeia
