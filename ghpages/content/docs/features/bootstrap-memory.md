+++
title = "Bootstrap e Memoria Persistente"
linkTitle = "Bootstrap e Memoria"
weight = 78
description = "Personalize a identidade do agente com arquivos bootstrap e mantenha contexto entre sessoes com memoria de longo prazo e notas diarias."
icon = "psychology"
+++

O ChatCLI oferece dois sistemas complementares para personalizar e contextualizar o agente: **arquivos bootstrap** para definir personalidade e regras, e **memoria persistente** para manter contexto entre sessoes.

---

## Arquivos Bootstrap

Os arquivos bootstrap sao documentos Markdown carregados automaticamente no system prompt do agente. Eles definem quem o assistente e, como ele se comporta e quais regras deve seguir.

### Arquivos Suportados

| Arquivo | Proposito | Exemplo |
|:--------|:----------|:--------|
| `SOUL.md` | Personalidade e tom do assistente | "Voce e um assistente DevOps pragmatico e direto" |
| `USER.md` | Preferencias e contexto do usuario | "O usuario prefere Go, usa Kubernetes, trabalha com microservicos" |
| `IDENTITY.md` | Identidade e capacidades do agente | "Voce e o ChatCLI, um assistente de terminal com acesso a ferramentas" |
| `RULES.md` | Regras e restricoes | "Nunca execute comandos destrutivos sem confirmacao" |
| `AGENTS.md` | Definicoes de sub-agentes | "Agente @coder: especialista em codigo..." |

### Prioridade de Carregamento

Os arquivos sao buscados em dois niveis, com o workspace tendo prioridade:

1. **Workspace** (`./`) — configuracoes especificas do projeto
2. **Global** (`~/.chatcli/`) — configuracoes padrao do usuario

Se o mesmo arquivo existe em ambos os niveis, o do workspace prevalece. Arquivos globais servem como fallback.

### Exemplo

Crie `~/.chatcli/SOUL.md`:

```markdown
# Personalidade

Voce e um assistente tecnico especializado em engenharia de software.
Seja conciso e direto. Prefira exemplos praticos a explicacoes teoricas.
Quando sugerir codigo, use boas praticas e testes.

# Tom

- Profissional mas acessivel
- Prefira respostas curtas e objetivas
- Use bullet points para listas
```

Crie `./USER.md` no seu projeto:

```markdown
# Contexto do Projeto

- Stack: Go 1.25, gRPC, Kubernetes
- Banco: PostgreSQL 16
- CI/CD: GitHub Actions
- Estilo: conventional commits, trunk-based development
```

### Cache Inteligente

Os arquivos bootstrap usam cache baseado em mtime (modification time):

- Na primeira leitura, o conteudo e cacheado em memoria
- Leituras subsequentes verificam se o mtime mudou
- Se o arquivo foi modificado, o cache e invalidado automaticamente
- `IsStale()` verifica se algum arquivo mudou desde o ultimo carregamento

---

## Memoria Persistente

O sistema de memoria mantem contexto entre sessoes do ChatCLI, permitindo que o agente "lembre" de fatos, decisoes e padroes ao longo do tempo.

### Estrutura

```
~/.chatcli/memory/
├── MEMORY.md           # Fatos de longo prazo
└── daily/
    ├── 202603/
    │   ├── 20260301.md  # Notas de 1 de marco
    │   ├── 20260302.md  # Notas de 2 de marco
    │   └── 20260304.md  # Notas de hoje
    └── 202602/
        └── 20260228.md  # Notas de fevereiro
```

### MEMORY.md — Memoria de Longo Prazo

O `MEMORY.md` armazena fatos persistentes que o agente deve sempre considerar:

```markdown
# Decisoes Arquiteturais

- Usamos go-prompt v0.2.6 para TUI interativa
- Charmbracelet (glamour, lipgloss) apenas para styling, nao para TUI
- Message bus desacoplado via cli/bus para I/O do agente

# Padroes do Projeto

- json.Compact recebe *bytes.Buffer, nao *strings.Builder
- embed.FS sempre usa / nos caminhos, nunca filepath.Join
- Parsers stateful > regex para XML com atributos especiais
```

### Notas Diarias

As notas diarias sao organizadas por ano-mes (`YYYYMM/`) e nomeadas por data (`YYYYMMDD.md`):

```markdown
# 2026-03-04

## Trabalho Realizado
- Implementado fallback chain para provedores LLM
- Adicionado suporte a MCP via stdio/SSE
- 47 arquivos alterados, +4920 linhas

## Decisoes
- Usar init() auto-registration para provedores no registry
- cache_control:ephemeral para otimizacao de KV cache na Anthropic
```

### Metodos Disponiveis

```go
ms := workspace.NewMemoryStore(dir, logger)

// Memoria de longo prazo
ms.WriteLongTerm("# Key Facts\n- Go project")
ms.AppendLongTerm("\n- Uses gRPC for remote access")
content := ms.ReadLongTerm()

// Notas diarias
ms.WriteDailyNote("Implementei feature X")
path := ms.TodayNotePath()
notes := ms.GetRecentDailyNotes(3) // ultimas 3 notas

// Contexto completo para system prompt
ctx := ms.GetMemoryContext()
// Retorna secao formatada com "Long-term Memory" + "Daily Notes"
```

---

## Context Builder

O `ContextBuilder` combina bootstrap + memoria em um unico prefixo de system prompt:

```go
cb := workspace.NewContextBuilder(bootstrapDir, globalDir, memoryDir, logger)
systemPrefix := cb.BuildContext()
// Retorna: bootstrap content + memory context, prontos para injetar no prompt
```

O resultado e cacheado e invalidado automaticamente quando qualquer arquivo source muda.

---

## Configuracao

### Variaveis de Ambiente

```bash
CHATCLI_BOOTSTRAP_ENABLED=true
CHATCLI_BOOTSTRAP_DIR=/path/to/bootstrap/files
CHATCLI_MEMORY_ENABLED=true
```

### Via Helm Chart

```yaml
# values.yaml
bootstrap:
  enabled: true
  definitions:
    SOUL.md: |
      Voce e um assistente DevOps...
    USER.md: |
      O usuario prefere Go...

memory:
  enabled: true
  # Usa o PVC de persistencia por padrao
```

O Helm chart cria ConfigMaps para os arquivos bootstrap e monta em `/home/chatcli/.chatcli/bootstrap/`. A memoria usa o PVC de sessoes para persistencia.

---

## Boas Praticas

1. **SOUL.md global, USER.md por projeto**: Mantenha sua personalidade preferida globalmente e contexto tecnico por projeto
2. **MEMORY.md conciso**: Mantenha apenas fatos estáveis e confirmados — nao sessao-especificos
3. **Notas diarias para journaling**: Use para registrar decisoes, problemas resolvidos e contexto temporal
4. **Nao duplique CLAUDE.md**: Se voce ja usa CLAUDE.md ou instrucoes do projeto, evite duplicar no bootstrap
5. **Revise periodicamente**: Remova memorias desatualizadas para manter o contexto relevante
