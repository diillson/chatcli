---
title: "Agentes Customizáveis (Personas)"
description: "Sistema modular para criar personalidades e conhecimentos especializados para a IA, com suporte a despacho automático como workers no sistema multi-agent."
weight: 70
---

O ChatCLI permite que você crie **Agentes Customizáveis** (também chamados de Personas) que definem comportamentos específicos para a IA. Este sistema transforma o ChatCLI de uma ferramenta com um "System Prompt" estático para uma **plataforma polimórfica**.

## Conceito Fundamental

A ideia central é a **composição de prompts**:

- **Agentes** definem *"quem"* a IA é (personalidade, especialização, tom)
- **Skills** definem *"o que"* ela deve saber/obedecer (regras, knowledge, compliance)

Um Agente pode importar múltiplas Skills, criando um **"Super System Prompt"** composto automaticamente.

## Benefícios

| Benefício | Descrição |
|-----------|------------|
| **Reutilização** | Skills podem ser compartilhadas entre múltiplos agentes |
| **Versionamento** | Arquivos .md podem ser versionados no Git |
| **Colaboração** | Equipes podem compartilhar agentes e skills |
| **Consistência** | Regras de coding style aplicadas automaticamente |
| **Especialização** | Crie agentes para Go, Python, DevOps, etc. |
| **Despacho como Worker** | Agents customizados são automaticamente registrados no sistema multi-agent e podem ser despachados via `<agent_call>` pelo LLM |
| **Servidor Remoto** | Ao conectar a um servidor, agents e skills remotos são descobertos automaticamente e mesclados com os locais |

## Estrutura de Diretórios

Os arquivos ficam no diretório ```~/.chatcli/```:

```text
~/.chatcli/
├── agents/            # Arquivos de agentes
│   ├── go-expert.md
│   ├── devops-senior.md
│   ├── security-auditor.md
│   └── python-data-scientist.md
└── skills/            # Arquivos de skills (.md ou diretórios V2)
    ├── clean-code/    # Skill V2 (pacote com subskills + scripts)
    │   ├── SKILL.md
    │   ├── naming-rules.md
    │   └── scripts/
    │       └── lint_check.py
    ├── error-handling.md  # Skill V1 (arquivo único)
    ├── docker-master.md
    └── clean-scripts.md
```

## Formato do Arquivo de Agente

Os agentes são arquivos Markdown com frontmatter YAML:

```yaml
---
name: "devops-senior"
description: "DevOps Senior com foco em CI/CD e infraestrutura"
tools: Read, Grep, Glob, Bash, Write, Edit   # Define quais ferramentas o agent pode usar como worker
skills:                    # Lista de skills a importar
  - clean-code
  - bash-linux
  - architecture
plugins:                   # Plugins habilitados (opcional)
  - "@coder"
---
# Personalidade Base

Você é um Engenheiro DevOps Sênior, especialista em CI/CD,
containers, infraestrutura como código e observabilidade.
```

### Campo `tools` — Integração com Multi-Agent

O campo `tools` no frontmatter YAML é a chave para a integração com o sistema de [orquestração multi-agent](/docs/features/multi-agent-orchestration/). Ele define quais comandos o agent pode usar quando despachado como **worker** pelo LLM orquestrador.

| Tool no YAML | Comando(s) @coder | Descrição |
|--------------|-------------------|-----------|
| `Read` | `read` | Ler conteúdo de arquivos |
| `Grep` | `search` | Buscar padrões em arquivos |
| `Glob` | `tree` | Listar diretórios |
| `Bash` | `exec`, `test`, `git-status`, `git-diff`, `git-log`, `git-changed`, `git-branch` | Execução de comandos e operações git |
| `Write` | `write` | Criar/sobrescrever arquivos |
| `Edit` | `patch` | Edição precisa (search/replace) |

**Regras:**
- Agents **sem** campo `tools` recebem automaticamente `read`, `search`, `tree` e são marcados como **read-only**
- Agents com apenas `Read`, `Grep`, `Glob` são **read-only** (não podem modificar arquivos)
- Agents com `Write`, `Edit` ou `Bash` têm **acesso de escrita/execução**
- Nomes de agents embarcados (`file`, `coder`, `shell`, `git`, `search`, `planner`) são **protegidos** e não podem ser sobrescritos

### Exemplo sem o campo `tools`

```yaml
---
name: "go-expert"
description: "Especialista em Go/Golang com foco em código limpo"
skills:
  - clean-code
  - error-handling
plugins:
  - "@coder"
---
# Personalidade Base

Você é um Engenheiro de Software Sênior, especialista em Go/Golang.

## Princípios Fundamentais

1. **Simplicidade**: Prefira código simples e legível.
2. **Composição**: Use interfaces pequenas e composição ao invés de herança.
3. **Erros**: Trate erros explícitamente, nunca ignore.
4. **Testes**: Escreva testes com table-driven tests.
```

> Este agent será registrado como **read-only** no sistema multi-agent (apenas `read`, `search`, `tree`).

## Formato do Arquivo de Skill

As skills contêm conhecimento puro ou regras de compliance:

```yaml
---
name: "clean-code"
description: "Princípios de Clean Code e boas práticas"
---
# Regras de Clean Code

## Nomenclatura

1. **Nomes significativos**: Variáveis e funções devem revelar seu propósito.
2. **Evite desinformação**: Não use nomes que possam confundir.
3. **Nomes pronunciáveis**: Use nomes que possam ser discutidos verbalmente.

## Funções

1. **Pequenas**: Funções devem fazer uma coisa sós.
2. **Poucos argumentos**: Idealmente 0-2 argumentos, máximo 3.
3. **Sem efeitos colaterais**: Funções devem fazer somente o que prometem.
```

## Skills V2 — Pacotes com Subskills e Scripts

Além das skills V1 (arquivo único `.md`), o ChatCLI suporta **Skills V2**: diretórios contendo múltiplos documentos e scripts executáveis.

### Estrutura de uma Skill V2

```text
skills/
└── clean-code/
    ├── SKILL.md            # Conteúdo principal (frontmatter + body)
    ├── naming-rules.md     # Subskill: regras de nomenclatura
    ├── formatting.md       # Subskill: regras de formatação
    └── scripts/
        └── lint_check.py   # Script executável
```

### Subskills

Arquivos `.md` dentro do diretório da skill (exceto `SKILL.md`) são registrados como **subskills**. Quando o agent é despachado como worker, os caminhos dos subskills aparecem no system prompt do worker, que pode lê-los com o comando `read` conforme necessário.

### Scripts

Arquivos em `scripts/` são registrados como **skills executáveis** no worker. O sistema infere automaticamente o comando de execução com base na extensão:

| Extensão | Comando Inferido |
|----------|-----------------|
| `.sh` | `bash script.sh` |
| `.py` | `python3 script.py` |
| `.js` | `node script.js` |
| `.ts` | `npx ts-node script.ts` |
| `.rb` | `ruby script.rb` |
| Outros | `./script` (execução direta) |

Os scripts são executados via o comando `exec` do @coder e seus resultados retornam ao worker para processamento.

---

## Skills de Registries Remotos

Além de criar skills manualmente, você pode **buscar e instalar** skills de registries remotos com o comando `/skill`:

```bash
# Buscar skills de kubernetes
/skill search kubernetes

# Instalar uma skill
/skill install k8s-ops

# Verificar que aparece nas skills disponíveis
/agent skills
```

Skills instaladas via registry são salvas em `~/.chatcli/skills/<name>/SKILL.md` como pacotes V2 e ficam imediatamente disponíveis para uso com agentes.

O ChatCLI suporta múltiplos registries simultaneamente (ChatCLI.dev, ClawHub, registries corporativos) com busca paralela fan-out. Veja [Skill Registry](/docs/features/skill-registry/) para detalhes completos.

---

## Despacho como Worker (Multi-Agent)

Ao iniciar o `/coder` ou `/agent`, **todos os agents customizados** são automaticamente registrados no sistema de [orquestração multi-agent](/docs/features/multi-agent-orchestration/). O LLM orquestrador pode então despachá-los via `<agent_call>`:

```xml
<agent_call agent="devops-senior" task="Configure CI/CD pipeline with GitHub Actions" />
<agent_call agent="security-auditor" task="Audit the authentication module for OWASP" />
```

### O que o worker recebe

Quando despachado, o CustomAgent executa com:

1. **System prompt personalizado** — Inclui o conteúdo do agent (markdown body), skills carregadas, caminhos de subskills, comandos de scripts, e instruções de tool_call
2. **Mini ReAct loop** — O mesmo loop ReAct dos agents embarcados, com raciocínio → ação → observação
3. **Comandos permitidos** — Baseados no campo `tools` do frontmatter
4. **Leitura paralela** — Tool calls read-only executam em goroutines paralelas
5. **File locks** — Escrita com mutex per-filepath para segurança anti-race
6. **Recuperação de erros** — O orquestrador pode usar `tool_call` direto para diagnosticar e corrigir falhas

### Exemplo End-to-End

```bash
# 1. Crie o agent em ~/.chatcli/agents/devops-senior.md
# 2. Inicie o coder mode
/coder configure the deployment pipeline and monitoring

# O LLM orquestrador pode despachar:
# <agent_call agent="devops-senior" task="Set up CI/CD with GitHub Actions" />
# <agent_call agent="file" task="Read current Dockerfile and docker-compose.yml" />
#
# Ambos rodam em paralelo com seus próprios ReAct loops
```

---

## Comandos de Gerenciamento

Todos os comandos de gerenciamento estão integrados ao `/agent`:

| Comando                                      | Descrição                                                                   |
|----------------------------------------------|-----------------------------------------------------------------------------|
| `/agent`                                     | Mostra status do agente ativo e ajuda                                       |
| `/agent list`                                | Lista todos os agentes disponíveis                                          |
| `/agent status` | Lista apenas os agentes anexados (resumido) - alias{attached/list-attached} |
| `/agent load <nome>`                         | Carrega um agente específico                                                |
| `/agent attach <nome>`                       | Anexa um agente adicional à sessão                                          |
| `/agent detach <nome>`                       | Remove um agente anexado                                                    |
| `/agent skills`                              | Lista todas as skills disponíveis                                           |
| `/agent show [--full]`                       | Mostra os agente ativo com exemplo de prompts (use --full para exibir tudo) |
| `/agent off`                                 | Desativa todos agente atualmente ativados                                   |
| `/agent <tarefa>`                            | Executa uma tarefa no modo agente                                           |

## Ordem de Montagem do Prompt

Quando um agente é carregado, o system prompt é montado na seguinte ordem:

1. **[ROLE]** - Identidade do agente (nome, descrição)
2. **[PERSONALITY]** - Conteúdo base do agente (markdown body)
3. **[SKILLS]** - Conhecimento das skills importadas (numeradas)
4. **[PLUGINS]** - Hints de plugins habilitados
5. **[LEMBRETE]** - Anchor com instruções de aplicação

Essa ordem garante que a IA receba o contexto de forma estruturada.

## Exemplo Prático Completo

### 1. Criar um agente

Crie o arquivo `~/.chatcli/agents/python-data.md`:

```yaml
---
name: "python-data"
description: "Cientista de Dados especialista em Python"
skills:
  - clean-code
plugins:
  - "@coder"
---
# Personalidade Base

Você é um Cientista de Dados Sênior, especialista em Python.

## Ferramentas Preferidas

- Pandas para manipulação de dados
- NumPy para cálculos numéricos
- Matplotlib/Seaborn para visualização
- Scikit-learn para ML clássico
- PyTorch para deep learning
```

### 2. Usar o agente

```bash
# Listar agentes
/agent list

🤖 Available Agents:
─────────────────────────────────────────────
  go-expert - Especialista em Go/Golang [2 skills]
  python-data - Cientista de Dados [1 skills]

# Carregar o agente
/agent load python-data

✓ Agente 'python-data' carregado com sucesso!
   Cientista de Dados especialista em Python

   Skills anexadas:
    • clean-code ✓

# Usar no modo agente
/agent análise este dataset e crie visualizações

# Ou no modo coder
/coder crie um pipeline de ML para classificação
```

## Precedência de Agents e Skills (Projeto > Global)

Tanto agents quanto skills suportam **diretórios por projeto** com precedência sobre os globais. O ChatCLI detecta a raiz do projeto automaticamente buscando um diretório `.agent/` ou `.git/` a partir do diretório atual.

### Ordem de Busca

| Recurso | 1. Projeto (prioridade) | 2. Global (fallback) |
|---------|------------------------|---------------------|
| **Agents** | `./.agent/agents/*.md` | `~/.chatcli/agents/*.md` |
| **Skills** | `./.agent/skills/` | `~/.chatcli/skills/` |

Se um agent ou skill com o mesmo nome existir em ambos os diretórios, a versão do projeto prevalece.

### Estrutura do Projeto

```text
meu-projeto/
├── .agent/                  # Marca a raiz do projeto para o ChatCLI
│   ├── agents/              # Agents específicos do projeto
│   │   └── backend.md       # Sobrescreve ~/.chatcli/agents/backend.md
│   └── skills/              # Skills específicas do projeto
│       └── team-rules.md    # Regras específicas da equipe
├── src/
└── ...
```

> **Dica**: Se seu projeto já tem `.git/`, o ChatCLI usa esse diretório como raiz do projeto automaticamente. O `.agent/` é opcional — use-o quando quiser agents/skills por projeto sem depender do Git.

## Integração com /coder

Quando um agente está carregado:

- `/agent <tarefa>` – Usa a persona do agente
- `/coder <tarefa>` – Combina a persona do agente com o prompt do coder

Isso permite que você tenha um agente especialista em Go usando as ferramentas do `@coder` para editar arquivos, executar testes, etc.

## Dicas

1. **Comece simples**: Crie agentes com poucas skills e vá adicionando conforme necessário.
2. **Versione no Git**: Mantenha seus agentes e skills em um repositório.
3. **Compartilhe com a equipe**: Skills de coding style garantem consistência.
4. **Use descrições claras**: Ajuda a entender o propósito de cada agente/skill.
5. **Teste o prompt**: Use `/agent show` para ver como o prompt ficou montado.

## Exemplos de Skills Úteis

- **clean-code** - Princípios de código limpo
- **error-handling** - Padrões de tratamento de erros
- testing-patterns** - Padrões de testes automatizados
- **docker-master** - Best practices para Dockerfiles
- **clean-scripts** - Padrões para scripts Bash seguros
- **aws-security** - Regras de segurança para AWS
- **team-conventions** - Convenções específicas da equipe

## Agents e Skills Remotos

Quando conectado a um servidor ChatCLI via `chatcli connect`, o client descobre automaticamente os agents e skills disponíveis no servidor. Eles são transferidos ao client e compostos localmente, permitindo merge com resources locais.

```bash
# Ao conectar, o client mostra os recursos disponíveis
Connected to ChatCLI server (version: 1.3.0, provider: CLAUDEAI, model: claude-sonnet-4-5)
 Server has 3 plugins, 2 agents, 4 skills available

# Agents remotos aparecem na listagem
/agent list

🤖 Available Agents:
  • go-expert       - Especialista em Go/Golang            [local]
  • devops-senior   - DevOps Senior com foco em K8s        [remote]

# Carregar um agent remoto funciona da mesma forma
/agent load devops-senior
```

### Provisionamento via Kubernetes

No Helm chart ou no Operator, agents e skills podem ser provisionados via ConfigMaps:

```bash
# Helm: agents e skills inline
helm install chatcli deploy/helm/chatcli \
  --set agents.enabled=true \
  --set-file agents.definitions.devops-senior\\.md=agents/devops-senior.md \
  --set skills.enabled=true \
  --set-file skills.definitions.k8s-best-practices\\.md=skills/k8s-best-practices.md
```

```yaml
# Operator: referência a ConfigMaps existentes
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-prod
spec:
  provider: CLAUDEAI
  agents:
    configMapRef: chatcli-agents
    skillsConfigMapRef: chatcli-skills
```

Os ConfigMaps são montados em `/home/chatcli/.chatcli/agents/` e `/home/chatcli/.chatcli/skills/`, e ficam disponíveis para descoberta remota automaticamente.
