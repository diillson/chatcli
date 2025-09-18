<p align="center">
  <a href="https://ai.edilsonfreitas.com/">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

# Aproxime seu Terminal da Inteligência Artificial 🕵️‍♂️✨
 
O **ChatCLI** é uma aplicação de linha de comando (CLI) avançada que integra modelos de Linguagem de Aprendizado (LLMs) poderosos (como OpenAI, StackSpot, GoogleAI, ClaudeAI, xAI e Ollama -> `Modelos Locais`) para facilitar conversas interativas e contextuais diretamente no seu terminal. Projetado para desenvolvedores, cientistas de dados e entusiastas de tecnologia, ele potencializa a produtividade ao agregar diversas fontes de dados contextuais e oferecer uma experiência rica e amigável.

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

- [Recursos Principais](https://www.google.com/search?q=%23recursos-principais)
- [Instalação](https://www.google.com/search?q=%23instala%C3%A7%C3%A3o)
- [Configuração](https://www.google.com/search?q=%23configura%C3%A7%C3%A3o)
- [Uso e Comandos](https://www.google.com/search?q=%23uso-e-comandos)
    - [Modo Interativo](https://www.google.com/search?q=%23modo-interativo)
    - [Modo Não-Interativo (One-Shot)](https://www.google.com/search?q=%23modo-n%C3%A3o-interativo-one-shot)
    - [Comandos da CLI](https://www.google.com/search?q=%23comandos-da-cli)
- [Processamento Avançado de Arquivos](https://www.google.com/search?q=%23processamento-avan%C3%A7ado-de-arquivos)
    - [Modos de Uso do `@file`](https://www.google.com/search?q=%23modos-de-uso-do-file)
    - [Sistema de Chunks em Detalhes](https://www.google.com/search?q=%23sistema-de-chunks-em-detalhes)
- [Modo Agente](https://www.google.com/search?q=%23modo-agente)
    - [Interação com o Agente](https://www.google.com/search?q=%23intera%C3%A7%C3%A3o-com-o-agente)
    - [Modo Agente One-Shot](https://www.google.com/search?q=%23modo-agente-one-shot)
- [Estrutura do Código e Tecnologias](https://www.google.com/search?q=%23estrutura-do-c%C3%B3digo-e-tecnologias)
- [Contribuição](https://www.google.com/search?q=%23contribui%C3%A7%C3%A3o)
- [Licença](https://www.google.com/search?q=%23licen%C3%A7a)
- [Contato](https://www.google.com/search?q=%23contato)

-----

## Recursos Principais

- **Suporte a Múltiplos Provedores**: Alterne entre OpenAI, StackSpot, ClaudeAI, GoogleAI e xAI.
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

## Instalação

### Pré-requisitos

- **Go (versão 1.23+)**: [Disponível em golang.org](https://golang.org/dl/).

### Passos de Instalação

1.  **Clone o Repositório**:
    ```bash
    git clone https://github.com/diillson/chatcli.git
    cd chatcli
    ```
2.  **Instale as Dependências e Compile**:
    ```bash
    go mod tidy
    go build -o chatcli
    ```
    Para compilar com informações de versão:
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
    Isso injeta dados de versão no binário, acessíveis via `/version` ou `chatcli --version`.

### Instalação via `go install` (opcional)

```bash
go install github.com/diillson/chatcli@latest
```

O binário será instalado em `$GOPATH/bin`, permitindo que você o execute diretamente como `chatcli` se o diretório estiver no seu `PATH`.

-----

## Configuração

O ChatCLI utiliza variáveis de ambiente para se conectar aos provedores de LLM e definir seu comportamento. A maneira mais fácil é criar um arquivo `.env` na raiz do projeto.

### Variáveis de Ambiente Essenciais

- **Geral**:
    - `CHATCLI_DOTENV` – **(Opcional)** Define o caminho do seu arquivo `.env`.
    - `LOG_LEVEL` (`debug`, `info`, `warn`, `error`)
    - `LLM_PROVIDER` (`OPENAI`, `STACKSPOT`, `CLAUDEAI`, `GOOGLEAI`, `XAI`)
    - `ENV` - **(Opcional)** Define como o log será exibido (`dev`, `prod`), Padrão: `dev`.
      - `dev` ele mostra os logs direto no terminal e salva no arquivo de log. 
      - `prod` ele apenas salva no arquivo de log mantendo um terminal mais limpo.
- **Provedores**:
    - `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_ASSISTANT_MODEL`, `OPENAI_MAX_TOKENS`, `OPENAI_USE_RESPONSES`
    - `CLAUDEAI_API_KEY`, `CLAUDEAI_MODEL`, `CLAUDEAI_MAX_TOKENS`, `CLAUDEAI_API_VERSION`
    - `GOOGLEAI_API_KEY`, `GOOGLEAI_MODEL`, `GOOGLEAI_MAX_TOKENS`
    - `OLLAMA_ENABLED`, `OLLAMA_BASE_URL`, `OLLAMA_MODEL`, `OLLAMA_MAX_TOKENS`
    - `XAI_API_KEY`, `XAI_MODEL`, `XAI_MAX_TOKENS`
    - `CLIENT_ID`, `CLIENT_SECRET`, `SLUG_NAME`, `TENANT_NAME` (para StackSpot)

### Exemplo de `.env`

```env
# Configurações Gerais

LOG_LEVEL=info
ENV=prod
LLM_PROVIDER=CLAUDEAI
LOG_FILE=app.log
LOG_MAX_SIZE=300MB
HISTORY_MAX_SIZE=300MB

# Configurações do OpenAI
OPENAI_API_KEY=sua-chave-openai
OPENAI_MODEL=gpt-4o-mini
OPENAI_ASSISTANT_MODEL=gpt-4o-mini
OPENAI_USE_RESPONSES=true # use a Responses API (ex.: para gpt-5)
OPENAI_MAX_TOKENS=60000

# Configurações do StackSpot
CLIENT_ID=seu-cliente-id
CLIENT_SECRET=seu-cliente-secreto
SLUG_NAME=seu-slug-stackspot
TENANT_NAME=seu-tenant-name

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
```

-----

## Uso e Comandos

### Modo Interativo

Inicie a aplicação com `./chatcli` e comece a conversar.

### Modo Não-Interativo (One-Shot)

Execute prompts em uma única linha, ideal para scripts e automações.

- **Exemplos rápidos**:
  ```bash
  chatcli -p "Explique rapidamente este repositório."
  chatcli -p "@git @env Monte um release note enxuto."
  chatcli -p "@file ./src --mode summary Faça um panorama da arquitetura."
  ```
- **Entrada via `stdin` (Pipes)**:
  ```bash
  git diff | chatcli -p "Resuma as mudanças e liste possíveis impactos."
  ```
- **Flags disponiveis no oneshoot**:
    - `-p` ou `--prompt`: texto a enviar para a LLM em uma única execução.
    - `--provider`: sobrescreve o provedor de LLM em tempo de execução (`OPENAI`, `OPENAI_ASSISTANT`, `CLAUDEAI`, `GOOGLEAI`, `STACKSPOT`, `XAI`).
    - `--model`: escolhe o modelo do provedor ativo (ex.: `gpt-4o-mini`, `claude-3-5-sonnet-20241022`, `gemini-2.5-flash`, etc.)
    - `--timeout` timeout da chamada one-shot (padrão: 5m)
    - `--no-anim` desabilita animações (útil em scripts/CI).
    - `--agent-auto-exec` executa automaticamente o primeiro comando sugerido pelo agente (modo agente).

Observação: as mesmas features de contexto funcionam dentro do texto do `--prompt`, como `@file`, `@git`, `@env`, `@command` e o operador `>` para adicionar contexto. Lembre-se de colocar o prompt entre aspas duplas no shell para evitar interpretações indesejadas.  

### Comandos da CLI

- **Gerenciamento de Sessão**:
    - `/session save <nome>`, `/session load <nome>`, `/session list`, `/session delete <nome>`, `/session new`
- **Configuração e Status**:
    - `/switch`, `/reload`, `/config` ou `/status` (exibe configurações de runtime, provedor e modelo em uso).
- **Geral**:
    - `/help`: Exibe a ajuda.
    - `/exit`: Para Sair do ChatCLI.
    - `/version` ou `/v`: Mostra a versão, o hash do commit e a data de compilação.
    - `Ctrl+C` (uma vez): Cancela a operação atual.
    - `Ctrl+C` (duas vezes) ou `Ctrl+D`: Encerra a aplicação.
- **Contexto**:
    - `@history`, `@git`, `@env`, `@file`, `@command`.

-----

## Processamento Avançado de Arquivos

O comando `@file <caminho>` é a principal ferramenta para enviar arquivos e diretórios, com suporte à expansão de caminhos (`~`).

### Modos de Uso do `@file`

- **Modo Padrão (`full`)**: Processa todo o conteúdo de um arquivo ou diretório, truncando-o se o limite de tokens for excedido. Ideal para projetos pequenos a médios.
- **Modo de Resumo (`summary`)**: Retorna apenas a estrutura de diretórios, lista de arquivos com tamanhos e estatísticas gerais. Útil para obter uma visão geral sem o conteúdo.
- **Modo Inteligente (`smart`)**: O ChatCLI atribui uma pontuação de relevância a cada arquivo com base em sua pergunta e inclui somente os mais pertinentes.
  ```bash
  @file --mode smart ~/meu-projeto/ Como funciona o sistema de login?
  ```
- **Modo de Chunks (`chunked`)**: Para projetos grandes, divide o conteúdo em pedaços (chunks) gerenciáveis, enviando um de cada vez.

### Sistema de Chunks em Detalhes

Após o envio do primeiro chunk, use `/nextchunk` para processar o próximo. O sistema fornece feedback visual sobre o progresso e o número de chunks restantes. Para gerenciar falhas, use `/retry`, `/retryall` ou `/skipchunk`.

-----

## Modo Agente

O **Modo Agente** permite que a IA interaja com seu sistema, sugerindo ou executando comandos para automatizar tarefas complexas ou repetitivas.

### Interação com o Agente

Inicie o agente com `/agent <consulta>` ou `/run <consulta>`. O agente irá sugerir comandos que você pode aprovar ou refinar.

- **Refinamento**: Use `pCN` para adicionar contexto antes de executar o comando `N`.
- **Adicionando contexto ao output**: Após a execução, use `aCN` para adicionar informações ao output do comando `N` e obter uma nova resposta da IA.

### Modo Agente One-Shot

Perfeito para scripts e automação.

- **Modo Padrão (Dry-Run)**: Apenas sugere o comando e sai.
  ```bash
  chatcli -p "/agent liste todos os arquivos .go neste diretório"
  ```
- **Modo de Execução Automática**: Use a flag `--agent-auto-exec` para que o agente execute o primeiro comando sugerido (comandos perigosos são bloqueados automaticamente).
  ```bash
  chatcli -p "/agent crie um arquivo chamado test_file.txt" --agent-auto-exec
  ```

-----

## Estrutura do Código e Tecnologias

O projeto é modular e organizado em pacotes:

- **`cli`**: Gerencia a interface e o modo agente.
- **`config`**: Lida com a configuração via constantes.
- **`llm`**: Lida com a comunicação e gerência dos clientes LLM.
- **`utils`**: Contém funções auxiliares para arquivos, Git, shell, HTTP, etc.
- **`models`**: Define as estruturas de dados.
- **`version`**: Gerencia informações de versão.

Principais bibliotecas Go utilizadas: **Zap**, **go-prompt**, **Glamour**, **Lumberjack** e **Godotenv**.

-----

## Contribuição

Contribuições são bem-vindas\!

1.  **Fork o repositório.**
2.  **Crie uma nova branch para sua feature:** `git checkout -b feature/minha-feature`.
3.  **Faça seus commits e envie para o repositório remoto.**
4.  **Abra um Pull Request.**

-----

## Licença

Este projeto está licenciado sob a [Licença MIT](https://www.google.com/search?q=/LICENSE).

-----

## Contato

Para dúvidas ou suporte, abra uma [issue](https://www.google.com/search?q=https://github.com/diillson/chatcli/issues) no repositório.

-----

**ChatCLI** une a potência dos LLMs com a simplicidade da linha de comando, oferecendo uma ferramenta versátil para interações contínuas com IA diretamente no seu terminal. Aproveite e transforme sua experiência de produtividade\! 🗨️✨