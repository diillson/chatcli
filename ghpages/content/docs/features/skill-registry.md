+++
title = "Skill Registry"
linkTitle = "Skill Registry"
weight = 12
description = "Search, install and manage skills from multiple registries — ClawHub, ChatCLI.dev, and custom registries."
icon = "extension"
+++

O ChatCLI suporta um sistema de **Skill Registry** híbrido e multi-registry que permite buscar, instalar e gerenciar skills de múltiplos registries simultaneamente. Skills instaladas ficam imediatamente disponíveis para uso com agentes via `/agent skills`.

---

## Conceitos

| Conceito | Descrição |
|----------|-----------|
| **Registry** | Um servidor remoto que hospeda skills pesquisáveis e instaláveis |
| **Skill** | Um arquivo `.md` com frontmatter YAML que define comportamento especializado para agentes |
| **Fan-out Search** | Busca paralela em todos os registries habilitados, com resultados mesclados e deduplicados |
| **Moderation** | Flags de segurança (malware, conteúdo suspeito) fornecidas pelos registries |
| **Trigram Cache** | Cache fuzzy local que evita requisições de rede para buscas similares |

---

## Registries Padrão

O ChatCLI vem configurado com dois registries:

| Registry | URL | Descrição |
|----------|-----|-----------|
| **chatcli** | `https://registry.chatcli.dev/api/v1` | Registry oficial do ChatCLI |
| **clawhub** | `https://clawhub.ai/api/v1` | Marketplace público de skills (ClawHub) |

Ambos podem ser desabilitados ou estendidos com registries customizados.

---

## Comandos `/skill`

### Buscar Skills

```bash
/skill search <query>
```

Realiza uma busca fan-out paralela em todos os registries habilitados. Resultados são mesclados, deduplicados e exibidos com:
- Nome, versão e autor
- Registry de origem (`[chatcli]`, `[clawhub]`, etc.)
- Flags de moderação (`SUSPICIOUS`, `BLOCKED`)
- Status de instalação (`[installed]`)

**Exemplo:**

```
/skill search kubernetes

  Searching across registries for "kubernetes"...

  Results (3 found):

    1. k8s-ops (v2.1.0) by devopsorg  [chatcli]
       Kubernetes operations and troubleshooting guide
       Tags: kubernetes, devops, k8s

    2. k8s-security (v1.0.3) by secteam  [clawhub]
       Kubernetes security best practices
       Tags: kubernetes, security

    3. helm-charts (v1.5.0) by helmorg  [chatcli]  [installed]
       Helm chart development patterns
       Tags: kubernetes, helm

  Use /skill install <name> to install a skill.
```

### Instalar Skill

```bash
/skill install <name>
```

Busca a skill nos registries, verifica flags de moderação e instala em `~/.chatcli/skills/<name>/SKILL.md`.

- **Skills com malware detectado** são **bloqueadas** automaticamente
- **Skills suspeitas** exibem um aviso e pedem confirmação
- Skills já instaladas são **atualizadas** (substituídas)
- Após instalação, a skill fica disponível imediatamente em `/agent skills`

**Exemplo:**

```
/skill install k8s-ops

  Installing k8s-ops v2.1.0 from chatcli registry...
  Installed k8s-ops v2.1.0 from chatcli
  Path: ~/.chatcli/skills/k8s-ops/

  Skill 'k8s-ops' is now available.
  Verify with: /agent skills
```

### Desinstalar Skill

```bash
/skill uninstall <name>
```

Remove a skill instalada do disco.

```
/skill uninstall k8s-ops

  Removing skill 'k8s-ops'...
  Done. Skill 'k8s-ops' uninstalled.
```

### Listar Skills Instaladas

```bash
/skill list
```

Exibe todas as skills instaladas com versão, origem e caminho.

```
/skill list

  Installed Skills (3):

    clean-code     v1.2.0  [chatcli]   ~/.chatcli/skills/clean-code/
    error-handling         [local]     ~/.chatcli/skills/error-handling.md
    k8s-ops        v2.1.0  [chatcli]   ~/.chatcli/skills/k8s-ops/

  Registries (2):
    chatcli   https://registry.chatcli.dev/api/v1  [enabled]
    clawhub   https://clawhub.ai/api/v1            [enabled]
```

### Informações de uma Skill

```bash
/skill info <name>
```

Exibe metadados completos de uma skill do registry (sem instalar).

### Ver Registries Configurados

```bash
/skill registries
```

Exibe todos os registries configurados, seus URLs e status.

```
/skill registries

  Configured Registries:

    1. chatcli   https://registry.chatcli.dev/api/v1  [enabled]   TTL: 15m
    2. clawhub   https://clawhub.ai/api/v1            [enabled]   TTL: 5m

  Config: ~/.chatcli/registries.yaml
  Edit the config file to add custom registries.
```

### Ajuda

```bash
/skill help
```

---

## Configuração

### Arquivo de Configuração

O arquivo `~/.chatcli/registries.yaml` controla os registries:

```yaml
registries:
  - name: chatcli
    url: https://registry.chatcli.dev/api/v1
    enabled: true
    cache_ttl: 15m
    type: chatcli
  - name: clawhub
    url: https://clawhub.ai/api/v1
    enabled: true
    cache_ttl: 5m
    type: clawhub
  # Custom registry example:
  - name: my-company
    url: https://skills.mycompany.com/api/v1
    enabled: true
    cache_ttl: 10m
    type: custom
    token: "my-auth-token"  # optional
install_dir: ~/.chatcli/skills
max_concurrent: 3
search_cache_size: 50
```

O arquivo é criado automaticamente com os registries padrão na primeira execução.

### Variáveis de Ambiente

| Variável | Descrição |
|----------|-----------|
| `CHATCLI_REGISTRY_URLS` | URLs adicionais de registries (separadas por vírgula). Cada URL é adicionada como registry customizado. |
| `CHATCLI_REGISTRY_DISABLE` | Nomes de registries a desabilitar (separados por vírgula). Ex: `clawhub,chatcli` |
| `CHATCLI_SKILL_INSTALL_DIR` | Diretório de instalação (padrão: `~/.chatcli/skills`) |

**Exemplo:**

```bash
# Adicionar um registry corporativo
export CHATCLI_REGISTRY_URLS="https://skills.corp.com/api/v1"

# Desabilitar o ClawHub
export CHATCLI_REGISTRY_DISABLE="clawhub"
```

---

## Registry Customizado

Qualquer servidor que implemente a API REST padrão pode ser usado como registry customizado:

### Endpoints Esperados

| Endpoint | Método | Descrição |
|----------|--------|-----------|
| `/skills/search?q=<query>` | GET | Busca skills. Retorna `{"skills": [...]}` ou `[...]` |
| `/skills/<slug>` | GET | Metadados de uma skill específica |
| `/skills/<slug>/download` | GET | Download do conteúdo da skill |

### Formato de Resposta (Search)

```json
{
  "skills": [
    {
      "name": "my-skill",
      "slug": "my-skill",
      "description": "Description here",
      "version": "1.0.0",
      "author": "author",
      "tags": ["go", "devops"],
      "downloads": 150,
      "download_url": "https://...",
      "moderation": {
        "malware_detected": false,
        "suspicious_content": false,
        "quarantined": false,
        "reason": ""
      }
    }
  ]
}
```

### Autenticação

Registries customizados podem requerer autenticação via token Bearer. Configure o campo `token` no `registries.yaml`:

```yaml
registries:
  - name: private-reg
    url: https://private.registry.com/api/v1
    enabled: true
    token: "Bearer my-secret-token"
```

---

## Segurança e Moderação

O sistema de skill registry inclui proteções contra conteúdo malicioso:

### Flags de Moderação

| Flag | Comportamento |
|------|---------------|
| `malware_detected` | **BLOQUEIO TOTAL** — instalação recusada automaticamente |
| `quarantined` | **BLOQUEIO TOTAL** — skill em quarentena pelo registry |
| `suspicious_content` | **AVISO** — exibe warning e pede confirmação do usuário |

### Instalação Atômica

Skills são instaladas usando escrita atômica:
1. Conteúdo é baixado para um diretório temporário (`.tmp-*`)
2. Frontmatter YAML é validado
3. Flags de moderação são verificadas
4. Diretório é renomeado atomicamente para o destino final
5. Em caso de falha, o diretório temporário é removido automaticamente

### Sanitização de Nomes

Nomes de skills são sanitizados para prevenir path traversal:
- Convertidos para lowercase
- Espaços substituídos por hífens
- Barras (`/`) removidas
- Sequências `..` removidas

---

## Cache de Busca (Trigram)

O ChatCLI implementa um cache fuzzy baseado em trigramas para reduzir chamadas de rede:

### Como Funciona

1. **Extração de Trigramas**: A query é dividida em subsequências de 3 caracteres
   - `"golang"` → `{"gol", "ola", "lan", "ang"}`

2. **Similaridade de Jaccard**: Para cada query em cache, calcula-se:
   ```
   J(A, B) = |A ∩ B| / |A ∪ B|
   ```

3. **Threshold**: Se J ≥ 0.7, os resultados em cache são retornados
   - `"golan"` → match com `"golang"` (J ≈ 0.75)
   - `"python"` → sem match com `"golang"` (J ≈ 0.0)

### Características

| Parâmetro | Valor Padrão |
|-----------|--------------|
| Tamanho máximo do cache | 50 entradas |
| TTL por entrada | 5 minutos |
| Threshold de similaridade | 0.7 |
| Evicção | LRU (Least Recently Used) |

O cache é invalidado automaticamente após instalar ou desinstalar uma skill.

---

## Integração com Agentes

Skills instaladas via `/skill install` ficam automaticamente disponíveis para o sistema de agentes:

1. Skills são instaladas em `~/.chatcli/skills/<name>/SKILL.md`
2. O `Loader` do sistema de personas (`pkg/persona`) já escaneia esse diretório
3. `/agent skills` lista tanto skills locais quanto instaladas via registry
4. Agentes podem referenciar skills instaladas em seu frontmatter:

```yaml
---
name: devops-agent
description: Expert DevOps agent
skills: [k8s-ops, docker-best-practices]
---
```

### Precedência de Skills

| Prioridade | Local | Descrição |
|------------|-------|-----------|
| 1 (maior) | `.agent/skills/` (projeto) | Skills locais do projeto |
| 2 | `~/.chatcli/skills/` (global) | Skills instaladas via registry ou manualmente |

Skills locais do projeto sempre têm precedência sobre skills globais com o mesmo nome.

---

## Arquitetura

```
/skill commands (cli/skill_handler.go)
        │
        ▼
RegistryManager (pkg/registry/manager.go) — coordenador fan-out
        │
        ├──► ChatCLIRegistry (registry.chatcli.dev)
        ├──► ClawHubRegistry (clawhub.ai)
        └──► CustomRegistry  (registries do usuário)
        │
        ▼  (resultados mesclados + deduplicados)
        ▼  (install → Installer → escrita atômica em ~/.chatcli/skills/)
        │
pkg/persona/Loader — escaneia ~/.chatcli/skills/ automaticamente
```

### Pacotes

| Pacote | Responsabilidade |
|--------|-----------------|
| `pkg/registry/` | Interface, adapters, manager, cache, installer, moderação |
| `cli/skill_handler.go` | Handler de comandos `/skill` |
| `pkg/persona/` | Loader de skills locais (inalterado) |

---

## Próximos Passos

- [**Customizable Agents**](/docs/features/customizable-agents/) — Como criar e usar agentes com skills
- [**Referência de Comandos**](/docs/reference/command-reference/) — Lista completa de todos os comandos
- [**Configuração (.env)**](/docs/reference/configuration/) — Variáveis de ambiente disponíveis
