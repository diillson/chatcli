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

- **Go (versão 1.23+)**: [Disponível em golang.org](https://golang.org/dl/).

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
  -  `LOG_FILE`  - **(Opcional)** Caminho do arquivo de log (padrão:  `$HOME/app.log` ).
  -  `LOG_MAX_SIZE`  - **(Opcional)** Tamanho máximo do arquivo de log antes da rotação (padrão:  100MB ).
  -  `HISTORY_MAX_SIZE`  - **(Opcional)** Tamanho máximo do arquivo de histórico antes da ro t ação (padrão:  100MB ).
  -  `ENV`  - **(Opcional)** Define como o log será exibido ( `dev` ,  `prod` ), Padrão:  `dev` .
      -  dev  mostra os logs direto no terminal e salva no arquivo de log.
      -  prod  apenas salva no arquivo de log mantendo um terminal mais limpo.

- Provedores:
  -  OPENAI_API_KEY ,  OPENAI_MODEL ,  OPENAI_ASSISTANT_MODEL ,  OPENAI_MAX_TOKENS ,  OPENAI_USE_RESPONSES
  -  CLAUDEAI_API_KEY ,  CLAUDEAI_MODEL ,  CLAUDEAI_MAX_TOKENS ,  CLAUDEAI_API_VERSION
  -  GOOGLEAI_API_KEY ,  GOOGLEAI_MODEL ,  GOOGLEAI_MAX_TOKENS
  -  OLLAMA_ENABLED ,  OLLAMA_BASE_URL ,  OLLAMA_MODEL ,  OLLAMA_MAX_TOKENS ,  OLLAMA_FILTER_THINKING  – (Opcional) Filtra "pensamento em voz alta" de modelos como Qwen3 (true/false, padrão: true).
  -  XAI_API_KEY ,  XAI_MODEL ,  XAI_MAX_TOKENS
  -  CLIENT_ID ,  CLIENT_KEY ,  STACKSPOT_REALM ,  STACKSPOT_AGENT_ID  (para StackSpot)
- Agente:
  -  CHATCLI_AGENT_CMD_TIMEOUT  – (Opcional) Timeout padrão para cada comando executado no Modo Agente. Aceita durações Go (ex.: 30s, 2m, 10m). Padrão:  10m .
  -  CHATCLI_AGENT_DENYLIST  – (Opcional) Lista de expressões regulares (separadas por “;”) para bloquear comandos perigosos além do padrão. Ex.: rm\s+-rf\s+.;curl\s+[^|;]|\s*(sh|bash).
  -  CHATCLI_AGENT_ALLOW_SUDO  – (Opcional) Permite comandos com sudo sem bloqueio automático (true/false). Padrão:  false  (bloqueia sudo por segurança).


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
    CHATCLI_AGENT_CMD_TIMEOUT=2m    # O comando terá 2m para ser executado após isso é travado e finalizado
    CHATCLI_AGENT_DENYLIST=rm\\s+-rf\\s+.*;curl\\s+[^|;]*\\|\\s*(sh|bash);dd\\s+if=;mkfs\\w*\\s+
    CHATCLI_AGENT_ALLOW_SUDO=false
    
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
    CLAUDEAI_API_KEY=sua-chave-claudeai
    CLAUDEAI_MODEL=claude-3-5-sonnet-20241022
    CLAUDEAI_MAX_TOKENS=20000
    CLAUDEAI_API_VERSION=2023-06-01
    
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


### Modo Agente One-Shot

Perfeito para scripts e automação.

- Modo Padrão (Dry-Run): Apenas sugere o comando e sai.
  - chatcli -p "/agent liste todos os arquivos .go neste diretório"

- Modo de Execução Automática: Use a flag  --agent-auto-exec  para que o agente execute o primeiro comando sugerido (comandos perigosos são bloqueados automaticamente).
  - chatcli -p "/agent crie um arquivo chamado test_file.txt" --agent-auto-exec

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