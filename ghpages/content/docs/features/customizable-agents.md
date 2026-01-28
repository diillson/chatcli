---
title: "Agentes Customizáveis (Personas)"
description: "Sistema modular para criar personalidades e conhecimentos especializados para a IA."
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

## Estrutura de Diretórios

Os arquivos ficam no diretório ```~/.chatcli/```:

```text
~/.chatcli/
├── agents/            # Arquivos de agentes
│   ├── go-expert.md
│   ├── devops-senior.md
│   └── python-data-scientist.md
└── skills/            # Arquivos de skills
    ├── clean-code.md
    ├── error-handling.md
    ├── docker-master.md
    └── clean-scripts.md
```

## Formato do Arquivo de Agente

Os agentes são arquivos Markdown com frontmatter YAML:

```yaml
---
name: "go-expert"
description: "Especialista em Go/Golang com foco em código limpo"
skills:                    # Lista de skills a importar
  - clean-code
  - error-handling
plugins:                   # Plugins habilitados (opcional)
  - "@coder"
---
# Personalidade Base

Você é um Engenheiro de Software Sênior, especialista em Go/Golang.

## Princípios Fundamentais

1. **Simplicidade**: Prefira código simples e legível.
2. **Composição**: Use interfaces pequenas e composição ao invés de herança.
3. **Erros**: Trate erros explicitamente, nunca ignore.
4. **Testes**: Escreva testes com table-driven tests.
```

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

## Comandos de Gerenciamento

Todos os comandos de gerenciamento estão integrados ao `/agent`:

| Comando | Descrição |
|---------|------------|
| `/agent` | Mostra status do agente ativo e ajuda |
| `/agent list` | Lista todos os agentes disponíveis |
| `/agent load <nome>` | Carrega um agente específico |
| `/agent skills` | Lista todas as skills disponíveis |
| `/agent show` | Mostra o agente ativo e preview do prompt |
| `/agent off` | Desativa o agente atual |
| `/agent <tarefa>` | Executa uma tarefa no modo agente |

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
/agent analise este dataset e crie visualizações

# Ou no modo coder
/coder crie um pipeline de ML para classificação
```

## Precedência de Skills

Quando uma skill é referenciada, o sistema busca na seguinte ordem:

1. **Diretório do Projeto** - `./.chatcli/skills/` (se existir)
2. **Diretório Global** - `~/.chatcli/skills/`

Isso permite que projetos tenham skills específicas que sobrescrevem as globais.

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
