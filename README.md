<p align="center">
  <a href="https://ai.edilsonfreitas.com/">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

# Aproxime seu Terminal da Inteligência Artificial 🕵️‍♂️✨
 
O **ChatCLI** é uma aplicação de linha de comando (CLI) avançada que integra modelos de Linguagem de Aprendizado (LLMs) poderosos (como OpenAI, StackSpot, GoogleAI, ClaudeAI, xAI e Ollama -> `Modelos Locais`) para facilitar conversas interativas e contextuais diretamente no seu terminal. Projetado para desenvolvedores, cientistas de dados e entusiastas de tecnologia, ele potencializa a produtividade ao agregar diversas fontes de dados contextuais e oferecer uma experiência rica e amigável.

<p align="center">
  <em>Visualize o ChatCLI em ação, incluindo o Modo Agente e a troca de provedores.</em><br>
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="Demonstração do ChatCLI" width="800">
</p>

<div align="center">
  <img src="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg"/>
  <a href="https://github.com/diillson/chatcli/releases">
    <img src="https://img.shields.io/github/v/release/diillson/chatcli"/>
  </a>
    <img src="https://img.shields.io/github/last-commit/diillson/chatcli"/>  
    <img src="https://img.shields.io/github/languages/code-size/diillson/chatcli"/>  
    <img src="https://img.shields.io/github/go-mod/go-version/diillson/chatcli?label=Go%20Version"/>  
    <img src="https://img.shields.io/github/license/diillson/chatcli"/>
</div>

---

> 📘 Explore a documentação detalhada — incluindo use cases, tutoriais e receitas — em [diillson.github.io/chatcli](https://diillson.github.io/chatcli)

-----

### 📝 Índice

- [Por que Usar o ChatCLI?](#por-que-usar-o-chatcli)
- [Recursos Principais](#recursos-principais)
- [Suporte a Múltiplos Idiomas (i18n)](#suporte-a-múltiplos-idiomas-i18n)
- [Instalação](#instalação)
- [Configuração](#configuração)
- [Uso e Comandos](#uso-e-comandos)
    - [Modo Interativo](#modo-interativo)
    - [Modo Não-Interativo (One-Shot)](#modo-não-interativo-one-shot)
    - [Comandos da CLI](#comandos-da-cli)
- [Processamento Avançado de Arquivos](#processamento-avançado-de-arquivos)
    - [Modos de Uso do `@file`](#modos-de-uso-do-file)
    - [Sistema de Chunks em Detalhes](#sistema-de-chunks-em-detalhes)
    - [Gerenciamento de Contextos Persistentes](#gerenciamento-de-contextos-persistentes)
- [Modo Agente](#modo-agente)
    - [Política de Segurança](#política-de-segurança)
    - [Interação com o Agente](#interação-com-o-agente)
    - [UI Aprimorada do Agente](#ui-aprimorada-do-agente)
    - [Modo Agente One-Shot](#modo-agente-one-shot)
- [Agentes Customizáveis (Personas)](#agentes-customizáveis-personas)
    - [Conceito](#conceito)
    - [Estrutura de Arquivos](#estrutura-de-arquivos)
    - [Comandos de Gerenciamento](#comandos-de-gerenciamento)
    - [Exemplo Prático](#exemplo-prático)
- [Estrutura do Código e Tecnologias](#estrutura-do-código-e-tecnologias)
- [Contribuição](#contribuição)
- [Licença](#licença)
- [Contato](#contato)

-----

## Por que Usar o ChatCLI?

- **Interface Unificada**: Acesse os melhores modelos do mercado (OpenAI, Claude, Gemini, etc.) e modelos locais (Ollama) a partir de uma única interface, sem precisar trocar de ferramenta.
- **Consciência de Contexto**: Comandos como `@git`, `@file` e `@history` injetam contexto relevante diretamente no seu prompt, permitindo que a IA entenda seu ambiente de trabalho e forneça respostas mais precisas.
- **Potencial de Automação**: O **Modo Agente** transforma a IA em um assistente proativo que pode executar comandos, criar arquivos e interagir com seu sistema para resolver tarefas complexas.
- **Foco no Desenvolvedor**: Construído para o fluxo de trabalho de desenvolvimento, com recursos como processamento inteligente de arquivos de código, execução de comandos e integração com Git.

-----

## Recursos Principais

- **Suporte a Múltiplos Provedores**: Alterne entre OpenAI, StackSpot, ClaudeAI, GoogleAI, xAI e Ollama -> `Modelos locais`.
- **Experiência Interativa na CLI**: Navegação de histórico, auto-completação e feedback visual (`"Pensando..."`).
- **Comandos Contextuais Poderosos**:
    - `@history` – Insere os últimos 10 comandos do shell (suporta bash, zsh e fish).
    - `@git` – Adiciona informações do repositório Git atual (status, commits e branches).
    - `@env` – Inclui as variáveis de ambiente no contexto.
    - `@file <caminho>` – Insere o conteúdo de arquivos ou diretórios com suporte à expansão de `~` e caminhos relativos.
    - `@command <comando>` – Executa comandos do sistema e adiciona a saída ao contexto.
    - `@command -i <comando>` – Executa comandos interativos do sistema e `NÃO` adiciona a saída ao contexto.
    - `@command --ai <comando> > <contexto>` – Executa um comando e envia a saída diretamente para a LLM com contexto adicional.
- **Exploração Recursiva de Diretórios**: Processa projetos inteiros ignorando pastas irrelevantes (ex.: `node_modules`, `.git`).
- **Configuração Dinâmica e Histórico Persistente**: Troque provedores, atualize configurações em tempo real e mantenha o histórico entre sessões.
- **Robustez**: Retry com backoff exponencial para lidar com falhas de API.

-----

## Suporte a Múltiplos Idiomas (i18n)

O ChatCLI foi projetado para ser global. A interface do usuário, incluindo menus, dicas e mensagens de status, é totalmente internacionalizada.

- **Detecção Automática**: O idioma é detectado automaticamente a partir das variáveis de ambiente do seu sistema (`CHATCLI_LANG`(maior prioridade), `LANG` ou `LC_ALL`).
- **Idiomas Suportados**: Atualmente, o ChatCLI suporta **Português (pt-BR)** e **Inglês (en)**.
- **Fallback**: Se o idioma do seu sistema não for suportado, a interface será exibida em inglês por padrão.

-----

## Instalação

### Pré-requisitos

- **Go (versão 1.25+)**: [Disponível em golang.org](https://golang.org/dl/).

### 1. Binários Pré-compilados (Recomendado)

A maneira mais fácil de instalar é baixar o binário apropriado para seu sistema operacional e arquitetura na [página de Releases do GitHub](https://github.com/diillson/chatcli/releases).

### 2. Instalação via `go install`

```bash
go install github.com/diillson/chatcli@latest

O binário será instalado em  $GOPATH/bin , permitindo que você o execute diretamente como  chatcli  se o diretório estiver no seu  PATH .
```
### 3. Compilação a partir do Código-Fonte

1. Clone o Repositório:
```bash
   git clone https://github.com/diillson/chatcli.git
   cd chatcli
```
2. Instale as Dependências e Compile:
```bash
   go mod tidy
   go build -o chatcli
````   

3. Para compilar com informações de versão:
```bash   
   VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
   COMMIT_HASH=$(git rev-parse --short HEAD)
   BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    go build -ldflags "\
      -X github.com/diillson/chatcli/version.Version=${VERSION} \
      -X github.com/diillson/chatcli/version.CommitHash=${COMMIT_HASH} \
      -X github.com/diillson/chatcli/version.BuildDate=${BUILD_DATE}" \
      -o chatcli main.go
```
Isso injeta dados de versão no binário, acessíveis via  /version  ou  chatcli --version .

--------

## Configuração

O ChatCLI utiliza variáveis de ambiente para se conectar aos provedores de LLM e definir seu comportamento. A maneira mais fácil é criar um arquivo  .env  na raiz do projeto.

### Variáveis de Ambiente Essenciais

- Geral:
  -  `CHATCLI_DOTENV`  – **(Opcional)** Define o caminho do seu arquivo  .env .
  -  `CHATCLI_IGNORE` – **(Opcional)** Define uma lista de arquivos ou pastas a serem ignoradas pelo ChatCLI.
  -  `CHATCLI_LANG` - **(Opcional)** Força a CLI a usar um idioma específico (ex: `pt-BR`, `en`). Tem prioridade sobre a detecção automática do sistema.
  -  `LOG_LEVEL`  ( `debug` ,  `info` ,  `warn` ,  `error` )
  -  `LLM_PROVIDER`  ( `OPENAI` ,  `STACKSPOT` ,  `CLAUDEAI` ,  `GOOGLEAI` ,  `XAI` )
  -  `MAX_RETRIES`  - **(Opcional)** Número máximo de tentativas para chamadas de API (padrão:  `5` ).
  -  `INITIAL_BACKOFF`  - **(Opcional)** Tempo inicial de espera entre tentativas (padrão:  3  - segundos`).
  -  `LOG_FILE`  - **(Opcional)** Caminho do arquivo de log (padrão:  `$HOME/.chatcli/app.log` ).
  -  `LOG_MAX_SIZE`  - **(Opcional)** Tamanho máximo do arquivo de log antes da rotação (padrão:  100MB ).
  -  `HISTORY_MAX_SIZE`  - **(Opcional)** Tamanho máximo do arquivo de histórico antes da ro t ação (padrão:  100MB ).
  -  `HISTORY_FILE`      - **(Opcional)** Caminho para o arquivo de histórico (suporta `~`). Padrão: `.chatcli_history`.  
  -  `ENV`  - **(Opcional)** Define como o log será exibido ( `dev` ,  `prod` ), Padrão:  `dev` .
      -  dev  mostra os logs direto no terminal e salva no arquivo de log.
      -  prod  apenas salva no arquivo de log mantendo um terminal mais limpo.

- Provedores:
  -  OPENAI_API_KEY ,  OPENAI_MODEL ,  OPENAI_ASSISTANT_MODEL ,  OPENAI_MAX_TOKENS ,  OPENAI_USE_RESPONSES
  -  ANTHROPIC_API_KEY ,  ANTHROPIC_MODEL ,  ANTHROPIC_MAX_TOKENS ,  ANTHROPIC_API_VERSION
  -  GOOGLEAI_API_KEY ,  GOOGLEAI_MODEL ,  GOOGLEAI_MAX_TOKENS
  -  OLLAMA_ENABLED ,  OLLAMA_BASE_URL ,  OLLAMA_MODEL ,  OLLAMA_MAX_TOKENS ,  OLLAMA_FILTER_THINKING  – (Opcional) Filtra "pensamento em voz alta" de modelos como Qwen3 (true/false, padrão: true).
  -  XAI_API_KEY ,  XAI_MODEL ,  XAI_MAX_TOKENS
  -  CLIENT_ID ,  CLIENT_KEY ,  STACKSPOT_REALM ,  STACKSPOT_AGENT_ID  (para StackSpot)
- Agente:
  -  `CHATCLI_AGENT_CMD_TIMEOUT`  – **(Opcional)** Timeout padrão para cada comando executado da lista de ação no Modo Agente. Aceita durações Go (ex.: 30s, 2m, 10m). Padrão:  10m .
  -  `CHATCLI_AGENT_DENYLIST`  – **(Opcional)** Lista de expressões regulares (separadas por “;”) para bloquear comandos perigosos além do padrão. Ex.: rm\s+-rf\s+.;curl\s+[^|;]|\s*(sh|bash).
  -  `CHATCLI_AGENT_ALLOW_SUDO`  – **(Opcional)** Permite comandos com sudo sem bloqueio automático (true/false). Padrão:  false  (bloqueia sudo por segurança).
  -  `CHATCLI_AGENT_PLUGIN_MAX_TURNS` - **(Opcional)** Define o máximo de turnos que o agente pode ter. Padrão: 7.
  -  `CHATCLI_AGENT_PLUGIN_TIMEOUT` - **(Opcional)** Define o tempo limite de execução para o plugin do agente (ex.: 30s, 2m, 10m). Padrão: 15 (Minutos)

> ⚠️ **Importante:** Plugins que realizam operações demoradas (ex.: deploy de infraestrutura, builds complexos) podem precisar de timeouts maiores.

### Exemplo de  .env

    # Configurações Gerais
    LOG_LEVEL=info
    CHATCLI_LANG=pt_BR
    CHATCLI_IGNORE=~/.chatignore
    ENV=prod
    LLM_PROVIDER=CLAUDEAI
    MAX_RETRIES=10
    INITIAL_BACKOFF=2
    LOG_FILE=app.log
    LOG_MAX_SIZE=300MB
    HISTORY_MAX_SIZE=300MB
    HISTORY_FILE=~/.chatcli_history

    # Agente Configurações
    CHATCLI_AGENT_CMD_TIMEOUT=2m    # O comando terá 2m para ser executado após isso é travado e finalizado
    CHATCLI_AGENT_DENYLIST=rm\\s+-rf\\s+.*;curl\\s+[^|;]*\\|\\s*(sh|bash);dd\\s+if=;mkfs\\w*\\s+
    CHATCLI_AGENT_ALLOW_SUDO=false
    CHATCLI_AGENT_PLUGIN_MAX_TURNS=10
    CHATCLI_AGENT_PLUGIN_TIMEOUT=20m
    
    # Configurações do OpenAI
    OPENAI_API_KEY=sua-chave-openai
    OPENAI_MODEL=gpt-4o-mini
    OPENAI_ASSISTANT_MODEL=gpt-4o-mini
    OPENAI_USE_RESPONSES=true    # use a Responses API (ex.: para gpt-5)
    OPENAI_MAX_TOKENS=60000
    
    # Configurações do StackSpot
    CLIENT_ID=seu-cliente-id
    CLIENT_KEY=seu-cliente-secreto
    STACKSPOT_REALM=seu-tenant-name
    STACKSPOT_AGENT_ID=seu-id-agente
    
    # Configurações do ClaudeAI
    ANTHROPIC_API_KEY=sua-chave-claudeai
    ANTHROPIC_MODEL=claude-3-5-sonnet-20241022
    ANTHROPIC_MAX_TOKENS=20000
    ANTHROPIC_API_VERSION=2023-06-01
    
    # Configurações do Google AI (Gemini)
    GOOGLEAI_API_KEY=sua-chave-googleai
    GOOGLEAI_MODEL=gemini-2.5-flash
    GOOGLEAI_MAX_TOKENS=50000
    
    # Configurações da xAI
    XAI_API_KEY=sua-chave-xai
    XAI_MODEL=grok-4-latest
    XAI_MAX_TOKENS=50000
    
    # Configurações da Ollama
    OLLAMA_ENABLED=true      #Obrigatório para habilitar API do Ollama
    OLLAMA_BASE_URL=http://localhost:11434
    OLLAMA_MODEL=gpt-oss:20b
    OLLAMA_MAX_TOKENS=5000
    OLLAMA_FILTER_THINKING=false  # Filtra raciocínio intermediário em respostas (ex.: para Qwen3, llama3... - ISSO É NECESSÁRIO TRUE para o modo Agent Funcionar bem com alguns modelos OLLAMA que tem raciocínio em "voz alta")

--------

## Uso e Comandos

│ Dica Pro: Crie um alias no seu shell para acesso rápido! Adicione  alias c='chatcli'  ao seu  .bashrc ,  .zshrc  ou  config.fish .

### Modo Interativo

Inicie a aplicação com  ./chatcli  e comece a conversar.

### Modo Não-Interativo (One-Shot)

Execute prompts em uma única linha, ideal para scripts e automações.

- Exemplos rápidos:
  - chatcli -p "Explique rapidamente este repositório."
  - chatcli -p "@git @env Monte um release note enxuto."
  - chatcli -p "@file ./src --mode summary Faça um panorama da arquitetura."
  - chatcli -p "@file ./meuprojeto Descreva a arquitetura deste projeto com base nos arquivos .go" \
            --provider STACKSPOT \
            --agent-id "seu-id-de-agente-aqui"

- Entrada via  stdin  (Pipes):
  - git diff | chatcli -p "Resuma as mudanças e liste possíveis impactos."
  - cat error.log | chatcli -p "Explique a causa raiz deste erro e sugira uma solução."

- Flags disponiveis no oneshoot:
  -  -p  ou  --prompt : texto a enviar para a LLM em uma única execução.
  -  --provider : sobrescreve o provedor de LLM em tempo de execução ( OPENAI ,  OPENAI_ASSISTANT ,  CLAUDEAI ,  GOOGLEAI ,  STACKSPOT ,  XAI ).
  -  --model : escolhe o modelo do provedor ativo (ex.:  gpt-4o-mini ,  claude-3-5-sonnet-20241022 ,  gemini-2.5-flash , etc.)
  -  --max-tokens : Define a quantidade maxima de tokens usada para provedor ativo.
  -  --realm : define o realm/tenant para StackSpot.
  -  --agent-id : define o ID do agente a ser utilizado para StackSpot.
  -  --timeout  timeout da chamada one-shot (padrão: 5m)
  -  --no-anim  desabilita animações (útil em scripts/CI).
  -  --agent-auto-exec  executa automaticamente o primeiro comando sugerido pelo agente (modo agente).


Observação: as mesmas features de contexto funcionam dentro do texto do  --prompt , como  @file ,  @git ,  @env ,  @command  e o operador  >  para adicionar contexto. Lembre-se de colocar o prompt entre aspas duplas no shell para evitar interpretações indesejadas.

### Comandos da CLI

- Gerenciamento de Sessão:
  -  /session save <nome> ,  /session load <nome> ,  /session list ,  /session delete <nome> ,  /session new
- Configuração e Status:
  -  /switch ,  /reload ,  /config  ou  /status  (exibe configurações de runtime, provedor e modelo em uso).
- Gerenciamento de Contexto:
  - /context create | attach | list | show | delete
- Geral: 
  - /help : Exibe a ajuda.
  -  /exit : Para Sair do ChatCLI.
  -  /version  ou  /v : Mostra a versão, o hash do commit e a data de compilação.
  -  Ctrl+C  (uma vez): Cancela a operação atual.
  -  Ctrl+C  (duas vezes) ou  Ctrl+D : Encerra a aplicação.
- Contexto:
  -  @history ,  @git ,  @env ,  @file ,  @command .

--------

## Processamento Avançado de Arquivos

O comando  `@file` <caminho>  é a principal ferramenta para enviar arquivos e diretórios, com suporte à expansão de caminhos ( ~ ).

### Modos de Uso do  @file

- Modo Padrão ( full ): Processa todo o conteúdo de um arquivo ou diretório, truncando-o se o limite de tokens for excedido. Ideal para projetos pequenos a médios.
- Modo de Resumo ( summary ): Retorna apenas a estrutura de diretórios, lista de arquivos com tamanhos e estatísticas gerais. Útil para obter uma visão geral sem o conteúdo.
- Modo Inteligente ( smart ): O ChatCLI atribui uma pontuação de relevância a cada arquivo com base em sua pergunta e inclui somente os mais pertinentes.
@file --mode smart ~/meu-projeto/ Como funciona o sistema de login?

- Modo de Chunks ( chunked ): Para projetos grandes, divide o conteúdo em pedaços (chunks) gerenciáveis, enviando um de cada vez.

### Sistema de Chunks em Detalhes

Após o envio do primeiro chunk, use  /nextchunk  para processar o próximo. O sistema fornece feedback visual sobre o progresso e o número de chunks restantes. Para gerenciar falhas, use  /retry ,  /retryall  ou  /skipchunk .

Claro! Aqui está o conteúdo formatado corretamente em **Markdown completo**, pronto para colar no seu `README.md`:


## Gerenciamento de Contextos Persistentes

O **ChatCLI** permite criar, salvar e reutilizar contextos de projetos inteiros — tornando suas conversas com a IA muito mais contextualizadas.  
Isso significa que a IA "lembra" do seu código, diretórios e arquivos sem precisar reenviar tudo a cada interação.


### 🔧 Comandos Principais

#### 🆕 Criar um novo contexto

```bash
/context create <nome> <caminhos...> [opções]

# Exemplo: Criar um contexto "smart" com tags
/context create meu-api ./src ./docs --mode smart --tags "golang,api"
````

**Opções disponíveis:**

* `--mode` ou `-m` : Define o modo de processamento

    * `full` : Conteúdo completo dos arquivos
    * `summary` : Apenas estrutura de diretórios e metadados
    * `chunked` : Divide em chunks gerenciáveis
    * `smart` : IA seleciona arquivos relevantes ao prompt
* `--description` ou `-d` : Adiciona uma descrição textual ao contexto
* `--tags` ou `-t` : Adiciona tags para organização (separadas por vírgula)

#### 📋 Listar todos os contextos

```bash
/context list
```

**Exemplo de saída:**

```
🧩 meu-projeto   Backend API REST — modo:chunked | 4 chunks | 2.3 MB | tags:api,golang
📄 docs          Documentação — modo:full | 12 arquivos | 156 KB | tags:docs
🧩 frontend      Interface React — modo:chunked | 3 chunks | 1.8 MB | tags:react,ui
```

#### 🔍 Visualizar detalhes de um contexto

```bash
/context show <nome>
```

Exibe informações completas e estruturadas sobre o contexto:

##### 📊 Informações Gerais

* Nome, ID e descrição
* Modo de processamento (`full`, `summary`, `chunked`, `smart`)
* Quantidade de arquivos e tamanho total
* Tags associadas
* Datas de criação e última atualização

##### 📂 Distribuição por Tipo

* Estatísticas de tipos de arquivo presentes
* Porcentagem e tamanho ocupado por cada tipo

**Exemplo:**

```
● Go:            98 arquivos (62.8%) | 1847.32 KB
● JSON:          12 arquivos (7.7%)  | 45.67 KB
● Markdown:       8 arquivos (5.1%)  | 123.45 KB
```

##### 🧩 Estrutura em Chunks (para contextos `chunked`)

* Lista todos os chunks com suas respectivas informações
* Descrição e arquivos contidos em cada chunk (em formato de árvore)
* Tamanho e estimativa de tokens por chunk

##### 📁 Estrutura de Arquivos (para contextos `full`/`summary`)

* Árvore de diretórios e arquivos
* Tipo e tamanho de cada arquivo
* Visualização hierárquica organizada

##### 📌 Status de Anexação

* Dicas de como anexar o contexto
* Comandos disponíveis para chunks específicos

#### 🧠 Inspecionar um contexto (análise profunda)

```bash
/context inspect <nome> [--chunk N]
```

O comando `inspect` fornece uma análise estatística detalhada do contexto:

##### 📊 Análise Estatística

* Total de linhas de código
* Média de linhas por arquivo
* Distribuição de tamanho (pequeno, médio, grande)

##### 🗂️ Extensões Encontradas

* Lista de todas as extensões de arquivo
* Quantidade de arquivos por extensão

##### 🧩 Análise de Chunks (se aplicável)

* Tamanho médio, mínimo e máximo dos chunks
* Variação percentual entre chunks
* Distribuição de conteúdo

**Inspecionar chunk específico:**

```bash
/context inspect meu-projeto --chunk 1
```

Exibe:

* Descrição do chunk
* Lista completa de arquivos
* Linhas de código por arquivo
* Tamanho individual de cada arquivo

#### 📎 Anexar contexto à sessão atual

```bash
/context attach <nome> [opções]
```

**Opções disponíveis:**

* `--priority` ou `-p <número>` : Define a prioridade (menor = enviado primeiro)
* `--chunk` ou `-c <número>` : Anexa apenas um chunk específico
* `--chunks` ou `-C <números>` : Anexa múltiplos chunks (ex: `1,2,3`)

**Exemplos:**

```bash
# Anexar contexto completo
/context attach meu-api

# Anexar apenas o chunk 1
/context attach meu-projeto --chunk 1

# Anexar chunks 1, 2 e 3
/context attach meu-projeto --chunks 1,2,3

# Anexar com prioridade alta
/context attach docs --priority 1
```

#### 🔌 Desanexar contexto

```bash
/context detach <nome>
```

#### 📚 Ver contextos anexados

```bash
/context attached
```

Mostra todos os contextos atualmente anexados à sessão,
com suas prioridades e chunks selecionados.

#### 🗑️ Deletar um contexto

```bash
/context delete <nome>
```

> Pede confirmação antes de deletar permanentemente.

### 🎯 Comandos Adicionais

#### 🔀 Mesclar contextos

```bash
/context merge <novo-nome> <contexto1> <contexto2> [...]
```

**Exemplo:**

```bash
/context merge projeto-completo backend frontend infra
```

#### 📤 Exportar contexto

```bash
/context export <nome> <caminho-arquivo.json>
```

**Exemplo:**

```bash
/context export meu-api ./backups/api-context.json
```

#### 📥 Importar contexto

```bash
/context import <caminho-arquivo.json>
```

**Exemplo:**

```bash
/context import ./backups/api-context.json
```

#### 📈 Métricas de uso

```bash
/context metrics
```

Exibe estatísticas sobre:

* Contextos mais utilizados
* Tamanho total ocupado
* Frequência de uso

#### 🆘 Ajuda completa

```bash
/context help
```

💡 **Dica:** combine contextos com comandos como `@git` e `@file` para que a IA tenha visão completa do seu repositório e histórico de mudanças.

---

### Filtragem Avançada de Arquivos com `.chatignore`

Para refinar ainda mais o contexto enviado para a IA, o `ChatCLI` suporta um sistema de exclusão de arquivos e diretórios inspirado no `.gitignore`. Isso permite que você evite enviar arquivos de teste, documentação, logs ou qualquer outro conteúdo irrelevante.

#### Por que Filtrar Arquivos?

*   🎯 **Foco**: Envia apenas o código-fonte relevante para a IA, resultando em respostas mais precisas.
*   💰 **Eficiência**: Economiza tokens, o que pode reduzir custos em APIs pagas.
*   🚀 **Velocidade**: Processa projetos grandes mais rapidamente ao ignorar arquivos desnecessários.
*   🔇 **Redução de Ruído**: Evita poluir o contexto com arquivos compilados, dependências ou logs.

#### Como Funciona: O Arquivo `.chatignore`

A sintaxe é idêntica à do `.gitignore`:

*   Linhas que começam com `#` são comentários.
*   Para ignorar um diretório e todo o seu conteúdo, adicione o nome do diretório seguido de `/` (ex: `docs/`).
*   Use padrões glob (wildcards) para ignorar arquivos (ex: `*_test.go`, `*.log`).

#### Hierarquia de Precedência das Regras

O `ChatCLI` procura por um arquivo de ignore em uma ordem específica. O primeiro que for encontrado será utilizado, e os demais serão ignorados.

1.  **Variável de Ambiente (Maior Prioridade)**: Se a variável de ambiente `CHATCLI_IGNORE` estiver definida com o caminho para um arquivo, **apenas** ele será usado.
    ```bash
    export CHATCLI_IGNORE="~/configs/meu_ignore_global.txt"
    ```

2.  **Arquivo de Projeto**: Se a variável não estiver definida, o `ChatCLI` procurará por um arquivo `.chatignore` na **raiz do diretório** que você está analisando com `@file`. Ideal para regras específicas do projeto.

3.  **Arquivo Global do Usuário**: Se nenhum dos anteriores for encontrado, ele procurará por um arquivo de ignore global em `~/.chatcli/.chatignore`. Perfeito para regras que se aplicam a todos os seus projetos (ex: `.DS_Store`).

4.  **Regras Padrão**: Se nenhum arquivo for encontrado, o `ChatCLI` usará suas regras internas padrão (que já ignoram `.git`, `node_modules`, etc.).

> **Nota Importante:** As regras não são mescladas. Apenas o primeiro arquivo de ignore encontrado na hierarquia é utilizado.

#### Exemplo Prático de um Arquivo `.chatignore`

Você pode criar este arquivo na raiz do seu projeto para ignorar arquivos de teste, documentação e configurações de CI.


**.chatignore:**
```
Ignorar todos os arquivos de teste do Go

*_test.go

Ignorar diretórios inteiros de documentação e testes end-to-end

docs/
e2e/

Ignorar arquivos de configuração de CI e de log

golangci.yml
*.log
```

--------

## Modo Agente

O Modo Agente permite que a IA interaja com seu sistema, sugerindo ou executando comandos para automatizar tarefas complexas ou repetitivas.

-----

### Segurança e Governança do Modo Coder

O Modo Coder (`/coder`) possui um sistema de governança robusto inspirado no ClaudeCode, GeminiCLI, AntiGravity e outros..., garantindo que você tenha controle total sobre as ações da IA.

1. **Allow (Permitido):** Ações de leitura (`ls`, `read`) são executadas automaticamente.
2. **Deny (Bloqueado):** Ações perigosas podem ser bloqueadas permanentemente.
3. **Ask (Perguntar):** Por padrão, escritas e execuções exigem aprovação interativa.

> 🛵 Saiba mais sobre como configurar as regras de segurança na [documentação completa](https://diillson.github.io/chatcli/docs/features/coder-security).

#### Política de Segurança

O ChatCLI prioriza a segurança, bloqueando comandos perigosos por padrão. Você pode reforçar essa política com variáveis de ambiente:

-  CHATCLI_AGENT_DENYLIST  para bloquear padrões adicionais (regex separados por “ ; ”).
-  CHATCLI_AGENT_ALLOW_SUDO  para permitir/recusar  sudo  sem bloqueio automático (por padrão,  false ).
Mesmo quando permitido, comandos perigosos podem exigir confirmação explícita no terminal.

### Interação com o Agente

Inicie o agente com  /agent <consulta>  ou  /run <consulta> . O agente irá sugerir comandos que você pode aprovar ou refinar.

- Refinamento: Use  pCN  para adicionar contexto antes de executar o comando  N .
- Adicionando contexto ao output: Após a execução, use  aCN  para adicionar informações ao output do comando  N  e obter uma nova resposta da IA.

### UI Aprimorada do Agente

- Plano Compacto vs. Completo: Alterne com a tecla  p  para uma visão resumida ou detalhada do plano de execução.
- Último Resultado Ancorado: O resultado do último comando executado fica fixo no rodapé, facilitando a consulta sem precisar rolar a tela.
- Ações Rápidas:
  -  vN : Abre a saída completa do comando  N  no seu pager ( less  ou  more ), ideal para logs extensos.
  -  wN : Salva a saída do comando  N  em um arquivo temporário para análise posterior ou compartilhamento.
  -  r : Redesenha a tela, útil para limpar a visualização.

## 🔌 Sistema de Plugins

O ChatCLI suporta um sistema de plugins para estender suas funcionalidades e automatizar tarefas complexas. Um plugin é um simples executável que segue um contrato específico, permitindo que o  chatcli  o descubra, execute e interaja com ele de forma segura.

Isso permite criar comandos customizados (como  @kind ) que podem orquestrar ferramentas, interagir com APIs ou realizar qualquer lógica que você possa programar.

### Para Usuários: Gerenciando Plugins

Você pode gerenciar os plugins instalados através do comando  /plugin .

#### Listar Plugins Instalados

Para ver todos os comandos de plugin disponíveis:

/plugin list

#### Instalar um Novo Plugin

Você pode instalar um plugin diretamente de um repositório Git. O  chatcli  irá clonar, compilar (se for Go) e instalar o executável no diretório correto.

/plugin install https://github.com/usuario/meu-plugin-chatcli.git

> ⚠️ Aviso de Segurança: A instalação de um plugin envolve baixar e executar código de terceiros em sua máquina. Instale plugins apenas de fontes que você confia plenamente.

#### Ver Detalhes de um Plugin

Para ver a descrição e como usar um plugin específico:

/plugin show <nome-do-plugin>

#### Desinstalar um Plugin

Para remover um plugin:

/plugin uninstall <nome-do-plugin>

#### Recarregar Plugins

O `chatcli` monitora automaticamente o diretório de plugins (`~/.chatcli/plugins/`) e 
**recarrega automaticamente** quando detecta mudanças (criação, remoção, modificação de arquivos).

- **Debounce Inteligente:** Para evitar recarregamentos múltiplos, o sistema aguarda 500ms 
  após a última mudança antes de recarregar.
  
- **Eventos Monitorados:** Write, Create, Remove e Rename.

Se você precisar forçar um recarregamento manual (por exemplo, após editar um plugin 
sem salvar o arquivo), use:

```bash
/plugin reload
````

> 💡 Dica: Você pode desenvolver plugins iterativamente! Basta editar o código, recompilar e enviar ao diretorio de plugins, logo o ChatCLI detectará automaticamente a mudança.

--------

### Para Desenvolvedores: Criando seu Próprio Plugin

Criar um plugin é simples. Basta criar um programa executável que siga o "contrato" do ChatCLI.

#### O Contrato do Plugin

1. Executável: O plugin deve ser um arquivo executável.
2. Localização: O arquivo executável deve ser colocado no diretório  ~/.chatcli/plugins/ .
3. Nome do Comando: O nome do comando será  @  seguido pelo nome do arquivo executável. Ex: um arquivo chamado  kind  será invocado como  @kind .
4. **Metadados (`--metadata`)**: O executável deve responder à flag `--metadata`.
   Quando chamado com essa flag, ele deve imprimir na saída padrão (stdout) um JSON contendo:

```json
{
 "name": "@meu-comando",
 "description": "Uma breve descrição do que o plugin faz.",
 "usage": "@meu-comando <subcomando> [--flag value]",
 "version": "1.0.0"  // ← OBRIGATÓRIO
}
```   

> ⚠️ Importante: Os campos  name ,  description ,  usage  e  version  são obrigatórios.

**Schema Opcional (`--schema`)**: O executável pode opcionalmente responder à flag `--schema`.
Quando chamado com essa flag, ele deve imprimir na saída padrão (stdout) um JSON válido
descrevendo os parâmetros e argumentos que o plugin aceita:
```json
{
  "parameters": [
    {
      "name": "cluster-name",
      "type": "string",
      "required": true,
      "description": "Nome do cluster Kubernetes"
    }
  ]
}
```

> ⚠️ Nota: Se o plugin não implementar  --schema , ele ainda funcionará normalmente.


5. Comunicação e Feedback (stdout vs stderr): Esta é a parte mais importante para uma boa experiência de usuário.
   - Saída Padrão ( stdout ): Use a saída padrão apenas para o resultado final que deve ser retornado ao  chatcli  e, potencialmente, enviado para a IA.
   - Saída de Erro ( stderr ): Use a saída de erro para todos os logs de progresso, status, avisos e mensagens para o usuário. O  chatcli  exibirá o  stderr  em tempo real, evitando a sensação de que o programa travou.

#### Exemplo: Plugin "Hello World" em Go

Este exemplo demonstra como seguir o contrato, incluindo o uso de  stdout  e  stderr .

hello/main.go :
```
package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "os"
    "time"
)

// Metadata define a estrutura para a flag --metadata.
type Metadata struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Usage       string `json:"usage"`
    Version     string `json:"version"`
}

// logf envia mensagens de progresso para o usuário (via stderr).
func logf(format string, v ...interface{}) {
    fmt.Fprintf(os.Stderr, format, v...)
}

func main() {
    // 1. Lidar com a flag --metadata
    metadataFlag := flag.Bool("metadata", false, "Exibe os metadados do plugin")
    schemaFlag := flag.Bool("schema", false, "Exibe o schema de parâmetros do plugin")
    flag.Parse()

    if *metadataFlag {
            meta := Metadata{
                    Name:        "@hello",
                    Description: "Um plugin de exemplo que demonstra o fluxo de stdout/stderr.",
                    Usage:       "@hello [seu-nome]",
                    Version:     "1.0.0",
            }
            jsonMeta, _ := json.Marshal(meta)
            fmt.Println(string(jsonMeta)) // Metadados vão para stdout
            return
    }
    
    if *schemaFlag {
        schema := map[string]interface{}{
            "parameters": []map[string]interface{}{
                {
                    "name":        "nome",
                    "type":        "string",
                    "required":    false,
                    "description": "Nome da pessoa a ser cumprimentada",
                    "default":     "Mundo",
                },
            },
        }
        jsonSchema, _ := json.Marshal(schema)
        fmt.Println(string(jsonSchema))
        return
    }

    // 2. Lógica principal do plugin
    logf("🚀 Plugin 'hello' iniciado!\n") // Log de progresso para stderr

    time.Sleep(2 * time.Second) // Simula um trabalho
    logf("   - Realizando uma tarefa demorada...\n")
    time.Sleep(2 * time.Second)

    name := "Mundo"
    if len(flag.Args()) > 0 {
            name = flag.Args()[0]
    }

    logf("✅ Tarefa concluída!\n") // Mais progresso para stderr

    // 3. Enviar o resultado final para stdout
    // Esta é a única string que será retornada para o chatcli como resultado.
    fmt.Printf("Olá, %s! A hora agora é %s.", name, time.Now().Format(time.RFC1123))
}
```
#### Compilação e Instalação do Exemplo

1. Compile o executável:
>go build -o hello ./hello/main.go

2. Dê permissão de execução (necessário para que o ChatCLI reconheça o plugin):
> chmod +x hello

3. Mova para o diretório de plugins:
>Crie o diretório se ele não existir:
mkdir -p ~/.chatcli/plugins/

4. Mova o executável
>mv hello ~/.chatcli/plugins/

5. Use no ChatCLI: Agora, dentro agent do  chatcli , você pode executar seu novo comando:
>❯ /agent Olá meu nome é Fulano

Você verá os logs de progresso ( 🚀 Plugin 'hello' iniciado!... ) em tempo real no seu terminal, e no final, a mensagem  Olá, Mundo!...  será tratada como a saída do comando.

### Modo Agente One-Shot

Perfeito para scripts e automação.

- Modo Padrão (Dry-Run): Apenas sugere o comando e sai.
  - chatcli -p "/agent liste todos os arquivos .go neste diretório"

- Modo de Execução Automática: Use a flag  --agent-auto-exec  para que o agente execute o primeiro comando sugerido (comandos perigosos são bloqueados automaticamente).
  - chatcli -p "/agent crie um arquivo chamado test_file.txt" --agent-auto-exec

--------

## Agentes Customizáveis (Personas)

O ChatCLI permite que você crie **Agentes Customizáveis** (também chamados de Personas) que definem comportamentos específicos para a IA. É um sistema modular onde:

- **Agentes** definem *"quem"* a IA é (personalidade, especialização)
- **Skills** definem *"o que"* ela deve saber/obedecer (regras, conhecimento)

### Conceito

Um Agente pode importar múltiplas Skills, criando um *"Super System Prompt"** composto. Isso permite:

- Reutilizar conhecimento entre diferentes agentes
- Centralizar regras de coding style, segurança, etc.
- Versionar personas no Git
- Compartilhar entre equipes

### Estrutura de Arquivos

Os arquivos ficam em `~/.chatcli/`:

```
~/.chatcli/
├── agents/            # Arquivos de agentes (.md)
│   ├── go-expert.md
│   └── devops-senior.md
└── skills/            # Arquivos de skills (.md)
    ├── clean-code.md
    ├── error-handling.md
    └── docker-master.md
```

#### Formato do Agente

```yaml
---
name: "go-expert"
description: "Especialista em Go/Golang"
skills:
  - clean-code
  - error-handling
plugins:
  - "@coder"
---
# Personalidade Base

Você é um Engenheiro de Software Sênior, especialista em Go.
Sempre priorize simplicidade e legibilidade.
```

#### Formato da Skill

```yaml
---
name: "clean-code"
description: "Princípios de Clean Code"
---
# Regras de Clean Code

1. Use nomes significativos para variáveis e funções
1. Mantenha funções pequenas (máx 20 linhas)
3. Evite comentários desnecessários - código deve ser autoexplicativo
```

### Comandos de Gerenciamento

| Comando | Descrição |
|---------|------------|
| `/agent list` | Lista todos os agentes disponíveis |
| `/agent status` | Lista apenas os agentes anexados (resumido) |
| `/agent load <nome>` | Carrega um agente específico |
| `/agent attach <nome>` | Anexa um agente adicional à sessão |
| `/agent detach <nome>` | Remove um agente anexado |
| `/agent skills` | Lista todas as skills disponíveis |
| `/agent show [--full]` | Mostra o agente ativo (use --full para exibir tudo) |
| `/agent off` | Desativa o agente atual |

### Exemplo Prático

```bash
# 1. Listar agentes disponíveis
/agent list

# 2. Carregar o agente go-expert
/agent load go-expert

# 3. Usar no modo agente ou coder
/agent crie um servidor HTTP com graceful shutdown
/coder refatore esse código para seguir as best practices

# 4. Desativar quando terminar
/agent off
```

Ao carregar um agente, todas as interações com `/agent <tarefa>` ou `/coder <tarefa>` utilizarão automaticamente a persona do agente carregado, aplicando suas regras e conhecimentos especializados.

--------

## Estrutura do Código e Tecnologias

O projeto é modular e organizado em pacotes:

-  cli : Gerencia a interface e o modo agente.
-  config : Lida com a configuração via constantes.
-  i18n : Centraliza a lógica de internacionalização e os arquivos de tradução.
-  llm : Lida com a comunicação e gerência dos clientes LLM.
-  utils : Contém funções auxiliares para arquivos, Git, shell, HTTP, etc.
-  models : Define as estruturas de dados.
-  version : Gerencia informações de versão.

Principais bibliotecas Go utilizadas: Zap, go-prompt, Glamour, Lumberjack, Godotenv e golang.org/x/text.

--------

## Contribuição

Contribuições são bem-vindas!

1. Fork o repositório.
2. Crie uma nova branch para sua feature:  git checkout -b feature/minha-feature .
3. Faça seus commits e envie para o repositório remoto.
4. Abra um Pull Request.

--------

## Licença

Este projeto está licenciado sob a Licença MIT.

--------

## Contato

Para dúvidas ou suporte, abra uma issue https://github.com/diillson/chatcli/issues no repositório.

--------

ChatCLI une a potência dos LLMs com a simplicidade da linha de comando, oferecendo uma ferramenta versátil para interações contínuas com IA diretamente no seu terminal.

Aproveite e transforme sua experiência de produtividade! 🗨️✨