# ChatCLI

![Lint & Test](https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg)
[![GitHub release](https://img.shields.io/github/v/release/diillson/chatcli)](https://github.com/diillson/chatcli/releases)
![GitHub issues](https://img.shields.io/github/issues/diillson/chatcli)
![GitHub last commit](https://img.shields.io/github/last-commit/diillson/chatcli)
![GitHub code size in bytes](https://img.shields.io/github/languages/code-size/diillson/chatcli)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/diillson/chatcli?label=Go%20Version)
![GitHub](https://img.shields.io/github/license/diillson/chatcli)

O **ChatCLI** é uma aplicação de linha de comando (CLI) avançada que integra modelos de Linguagem de Aprendizado (LLMs) poderosos (como StackSpot, OpenAI e ClaudeAI) para facilitar conversas interativas e contextuais diretamente no seu terminal. Projetado para desenvolvedores, cientistas de dados e entusiastas de tecnologia, o ChatCLI potencializa a produtividade ao agregar diversas fontes de dados contextuais e oferecer uma experiência rica e amigável.

---

## Índice

- [Características Principais](#características-principais)
- [Instalação](#instalação)
- [Configuração](#configuração)
- [Uso e Comandos](#uso-e-comandos)
    - [Iniciando a Aplicação](#iniciando-a-aplicação)
    - [Comandos Gerais](#comandos-gerais)
    - [Comandos Contextuais](#comandos-contextuais)
- [Processamento Avançado de Arquivos](#processamento-avançado-de-arquivos)
    - [Envio de Arquivos e Diretórios](#envio-de-arquivos-e-diretórios)
    - [Modos de Uso do Comando `@file`](#modos-de-uso-do-comando-file)
    - [Sistema de Chunks em Detalhes](#sistema-de-chunks-em-detalhes)
- [Estrutura do Código](#estrutura-do-código)
- [Bibliotecas e Dependências](#bibliotecas-e-dependências)
- [Integração de Logs](#integração-de-logs)
- [Contribuindo](#contribuindo)
- [Licença](#licença)
- [Contato](#contato)

---

## Características Principais

- **Suporte a Múltiplos Provedores**: Alterna entre StackSpot, OpenAI e ClaudeAI conforme a necessidade.
- **Experiência Interativa na CLI**: Navegação de histórico, auto-completação e feedback animado (ex.: “Pensando…”).
- **Comandos Contextuais Poderosos**:
    - `@history` – Insere o histórico recente do shell (suporta bash, zsh e fish).
    - `@git` – Incorpora informações do repositório Git atual (status, commits e branches).
    - `@env` – Inclui as variáveis de ambiente no contexto.
    - `@file <caminho>` – Insere conteúdo de arquivos ou diretórios com suporte à expansão de `~` e caminhos relativos.
    - `@command <comando>` – Executa comandos do sistema e adiciona sua saída ao contexto.
    - `@command --ai <comando> > <contexto>` – Executa o comando e envia a saída diretamente para a LLM com contexto adicional.
- **Exploração Recursiva de Diretórios**: Processa projetos inteiros ignorando pastas irrelevantes (ex.: `node_modules`, `.git`).
- **Configuração Dinâmica e Histórico Persistente**: Troque provedores, atualize configurações em tempo real e mantenha o histórico entre sessões.
- **Retry com Backoff Exponencial**: Robustez no tratamento de erros e instabilidades na comunicação com APIs externas.

---

## Instalação

### Pré-requisitos

- **Go (versão 1.23+)** – Disponível em [golang.org](https://golang.org/dl/).

### Passos de Instalação

1. **Clone o Repositório**:

```bash
git clone https://github.com/diillson/chatcli.git
cd chatcli
```

2. **Instale as Dependências**:

```bash
go mod tidy
```

3. **Compile a Aplicação**:

```bash
go build -o chatcli
```

4. **Execute a Aplicação**:

```bash
./chatcli
```
#### Compilação com Informações de Versão

Para compilar a aplicação com informações completas de versão:

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
Estas flags injetam informações de versão no binário, permitindo que o comando  /version  exiba dados precisos.
   
### Instalação via Go Install (opcional)
Para instalar o ChatCLI diretamente via Go, você pode usar o seguinte comando:

```bash
go install github.com/diillson/chatcli@latest
```
Isso instalará o ChatCLI na sua pasta `$GOPATH/bin`, permitindo que você execute o comando `chatcli` diretamente no terminal caso seu `$GOPATH/bin` esteja no seu PATH.

---

## Configuração

O ChatCLI utiliza variáveis de ambiente para definir seu comportamento e conectar-se aos provedores de LLM. Essas variáveis podem ser configuradas via arquivo `.env` ou diretamente no shell.

### Variáveis de Ambiente

- **Local do `.env`**:
    - `CHATCLI_DOTENV` – (Opcional) Define o caminho do seu arquivo `.env`.

- **Geral**:
    - `LOG_LEVEL` – (Opcional) Níveis: `debug`, `info`, `warn`, `error` (padrão: `info`).
    - `ENV` – (Opcional) Ambiente: `prod` para produção ou `dev` para desenvolvimento (padrão: `dev`).
    - `LLM_PROVIDER` – (Opcional) Provedor padrão: `OPENAI`, `STACKSPOT` ou `CLAUDEAI` (padrão: `OPENAI`).
    - `LOG_FILE` – (Opcional) Nome do arquivo de log (padrão: `app.log`).
    - `LOG_MAX_SIZE` – (Opcional) Tamanho máximo do log antes da rotação (padrão: `50MB`).
    - `HISTORY_MAX_SIZE` – (Opcional) Tamanho do histórico do ChatCLI (padrão: `50MB`).

- **Provedor OpenAI**:
    - `OPENAI_API_KEY` – Chave de API da OpenAI.
    - `OPENAI_MODEL` – (Opcional) Modelo a ser utilizado (padrão: `gpt-4o-mini`)
    - `OPENAI_ASSISTANT_MODEL` – (Opcional) Modelo a ser utilizado (padrão: `gpt-4o-mini`)
    - `OPENAI_USE_RESPONSES`  – (Opcional) Quando  true , usa a OpenAI Responses API para o provedor  OPENAI  (ex.: GPT‑5).
    - `OPENAI_MAX_TOKENS`  – (Opcional) Override do limite de tokens usado internamente para chunking/truncamento.

- **Provedor StackSpot**:
    - `CLIENT_ID` – ID do cliente.
    - `CLIENT_SECRET` – Segredo do cliente.
    - `SLUG_NAME` – (Opcional) Nome do slug (padrão: `testeai`).
    - `TENANT_NAME` – (Opcional) Nome do tenant (padrão: `zup`).

- **Provedor ClaudeAI**:
    - `CLAUDEAI_API_KEY` – Chave de API da ClaudeAI.
    - `CLAUDEAI_MODEL` – (Opcional) Modelo (padrão: `claude-3-5-sonnet-20241022`).
    - `CLAUDEAI_MAX_TOKENS` – (Opcional) Máximo de tokens na resposta (padrão: `8192`).
    - `CLAUDEAI_API_VERSION`  – (Opcional) Versão da API da Anthropic (padrão: `2023-06-01`)

### Exemplo de Arquivo `.env`

```env
# Configurações Gerais
LOG_LEVEL=info
ENV=dev
LLM_PROVIDER=CLAUDEAI
LOG_FILE=app.log
LOG_MAX_SIZE=300MB
HISTORY_MAX_SIZE=300MB

# Configurações do OpenAI
OPENAI_API_KEY=sua-chave-openai
OPENAI_MODEL=gpt-4o-mini
OPENAI_ASSISTANT_MODEL=gpt-4o-mini
OPENAI_USE_RESPONSES=true  # use a Responses API (ex.: para gpt-5)
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
```

---

## Uso e Comandos

Após a instalação e configuração, o ChatCLI oferece uma série de comandos que facilitam a interação com a LLM.

### Iniciando a Aplicação

```bash
./chatcli
```

### Comandos Gerais

- **Encerrar a Sessão**:
    - `/exit`, `exit`, `/quit` ou `quit`

- **Alternar Provedor ou Configurações**:
    - `/switch` – Troca o provedor de LLM (modo interativo).
    - `/switch --slugname <slug>` – Atualiza somente o `slugName`.
    - `/switch --tenantname <tenant>` – Atualiza somente o `tenantName`.
    - Combinações: `/switch --slugname <slug> --tenantname <tenant>`
    - `/reload` – Recarrega as variáveis de ambiente em tempo real.

- **Iniciar uma Nova Sessão**:
    - `/newsession` – Limpa o histórico atual e inicia uma nova sessão de conversa.
    - **Uso**: Ideal para começar uma conversa do zero sem o contexto anterior, anteriormente recebia um clean no historico de conversa e contexto ao trocar de provider `LLM`, hoje é possível continuar a sessão em novo provider `LLM` sem perder o histórico anterior, com o comando `/newsession` você pode zerar o histórico e contexto atual e iniciar uma nova sessão de conversa no novo provider se assim desejar.

- **Verificar Versão e Atualizações**:
    - `/version` ou `/v` – Mostra a versão atual, o hash do commit e verifica se há atualizações disponíveis.
    - **Uso**: Útil para confirmar qual versão está instalada e se há novas versões disponíveis.
    - **Alternativa**: Execute `chatcli --version` ou `chatcli -v` diretamente do terminal.  

- **Ajuda**:
    - `/help`

### Comandos Contextuais

- `@history` – Insere os últimos 10 comandos do shell.
- `@git` – Incorpora informações do repositório Git.
- `@env` – Insere variáveis de ambiente no contexto.
- `@file <caminho>` – Insere o conteúdo de um arquivo ou diretório.
- `@command <comando>` – Executa um comando do terminal e salva a saída.
- **Novo**: `@command --ai <comando> > <contexto>` – Envia a saída do comando diretamente para a LLM com contexto adicional.

### Modo Agente

O Modo Agente permite que a IA execute tarefas no seu sistema através de comandos do terminal:

-  `/agent <consulta>`  ou  `/run <consulta>`  – Inicia o modo agente com uma tarefa específica.
- O agente analisará sua solicitação e sugerirá comandos apropriados para resolver a tarefa.
- Você pode selecionar comandos específicos para executar ou executar todos os comandos sugeridos.
- Exemplos de uso:
```bash
  "/agent" Liste todos os arquivos PDF no diretório atual
  "/run" Crie um backup compactado da pasta src/
  "/agent" Quais processos estão consumindo mais memória?
```
- O agente pode executar comandos complexos, como listar arquivos, criar backups, verificar processos em execução e muito mais.
- Você pode interagir com o agente, fornecendo feedback ou solicitando ajustes nas tarefas sugeridas.
- O Modo Agente é ideal para automatizar tarefas repetitivas ou complexas, permitindo que você se concentre em atividades mais importantes.
- O agente mantém um histórico de comandos executados, permitindo que você revise as ações tomadas e os resultados obtidos.
- O Modo Agente é uma ferramenta poderosa para aumentar sua produtividade, permitindo que você delegue tarefas ao ChatCLI e obtenha resultados rapidamente.
- O agente é projetado para ser seguro e respeitar as permissões do sistema, garantindo que apenas comandos autorizados sejam execut
- O Modo Agente pode ser desativado a qualquer momento, retornando ao modo de conversa normal.

#### Adicionando contexto aos outputs no modo Agente !!
- agora você pode adicionar contexto aos outputs dos comandos executados pelo agente

Quando você usar a nova funcionalidade "aCN" , você poderá:

1. Executar um comando (por exemplo,  1  para executar o comando #1)
2. Ver o resultado do comando
3. Digitar  aC1  para adicionar contexto ao comando #1
4. Adicionar suas observações, informações adicionais ou perguntas (terminando com  .  em uma linha vazia)
5. A IA responderá com base no comando, no resultado e no seu contexto adicional

#### Exemplo:
```text

📋 Saída do comando executado:
---------------------------------------
🚀 Executando comandos (tipo: shell):
---------------------------------------
⌛ Processando: Exibir lista de arquivos

⚙️ Comando 1/1: ls -la
📝 Saída do comando (stdout/stderr):
total 24
drwxr-xr-x  5 user  staff   160 May 15 10:23 .
drwxr-xr-x  3 user  staff    96 May 15 10:22 ..
-rw-r--r--  1 user  staff  2489 May 15 10:23 main.go
-rw-r--r--  1 user  staff   217 May 15 10:23 go.mod
-rw-r--r--  1 user  staff   358 May 15 10:23 go.sum
✓ Executado com sucesso

---------------------------------------
Execução concluída.
---------------------------------------

Você: aC1
Digite seu contexto adicional (termine com uma linha contendo apenas '.') ou pressione Enter para continuar:
Eu preciso criar um script que liste apenas os arquivos .go neste diretório
e que conte quantas linhas cada um tem.
.

[A IA então responderá com uma explicação e um novo comando para atender à sua solicitação específica]
```
---

## Processamento Avançado de Arquivos

O ChatCLI possui um sistema robusto para o envio e processamento de arquivos e diretórios, com modos de operação que atendem desde análises rápidas até explorações detalhadas de projetos inteiros.

### Envio de Arquivos e Diretórios

Para enviar um arquivo ou diretório, utilize o comando `@file` seguido do caminho desejado. O comando suporta:

- **Expansão de Caminhos**:
    - `~` é expandido para o diretório home.
    - Suporta caminhos relativos (`./src/utils.js`) e absolutos (`/usr/local/etc/config.json`).

**Exemplos**:

- Enviar um arquivo específico:

  ```
  Você: @file ~/documentos/main.go
  ```

- Enviar um diretório completo:

  ```
  Você: @file ~/projetos/minha-aplicacao/
  ```

---

### Modos de Uso do Comando `@file`

O comando `@file` pode operar em diferentes modos para atender às suas necessidades:

1. **Modo Padrão (Full)**
    - **Uso**: Projetos pequenos a médios.
    - **Funcionamento**:
        - Escaneia o diretório e inclui o conteúdo dos arquivos até atingir os limites do modelo.
        - Pode truncar conteúdos se o limite de tokens for excedido.

2. **Modo de Chunks (Dividido)**
    - **Uso**: Projetos grandes que precisam ser divididos em partes menores.
    - **Funcionamento**:
        - Divide o conteúdo em “chunks” (pedaços) gerenciáveis.
        - Envia apenas o primeiro chunk inicialmente e armazena os demais.
        - Você pode utilizar o comando `/nextchunk` para avançar manualmente entre os chunks.
    - **Exemplo**:
      ```
      Você: @file --mode=chunked ~/meu-projeto-grande/
      ```
      Após o envio do primeiro chunk, a mensagem exibirá:
      ```
      📊 PROJETO DIVIDIDO EM CHUNKS
      =============================
      ▶️ Total de chunks: 5
      ▶️ Arquivos estimados: ~42
      ▶️ Tamanho total: 1.75 MB
      ▶️ Você está no chunk 1/5
      ▶️ Use '/nextchunk' para avançar para o próximo chunk
      =============================
      ```

3. **Modo de Resumo (Summary)**
    - **Uso**: Quando você deseja apenas uma visão geral da estrutura do projeto, sem os conteúdos dos arquivos.
    - **Funcionamento**:
        - Retorna informações sobre a estrutura de diretórios, lista de arquivos com tamanhos e tipos e estatísticas gerais.
    - **Exemplo**:
      ```
      Você: @file --mode=summary ~/meu-projeto/
      ```

4. **Modo Inteligente (Smart)**
    - **Uso**: Análise focada, onde você fornece uma pergunta e o sistema seleciona os arquivos mais relevantes.
    - **Funcionamento**:
        - O ChatCLI atribui uma pontuação de relevância a cada arquivo com base na pergunta e inclui somente os mais pertinentes.
    - **Exemplo**:
      ```
      Você: @file --mode=smart ~/meu-projeto/ Como funciona o sistema de login?
      ```

---

### Sistema de Chunks em Detalhes

Para projetos grandes, quando o modo `chunked` é utilizado:

1. **Inicialização dos Chunks**:
    - O ChatCLI escaneia todo o diretório e divide o conteúdo em múltiplos chunks.
    - Cada chunk recebe metadados (ex.: número do chunk, total de chunks).
    - Apenas o primeiro chunk é enviado imediatamente, com os demais armazenados para envio subsequente.

2. **Navegação entre Chunks**:
    - Após receber o primeiro chunk, utilize o comando `/nextchunk` para enviar o próximo.
    - O sistema atualiza o progresso e informa quantos chunks ainda faltam.

3. **Tratamento de Falhas**:
    - Se ocorrer um erro em um chunk, ele é listado separadamente.
    - Comandos para gerenciar falhas:
        - `/retry` – Tenta novamente o último chunk que falhou.
        - `/retryall` – Retenta todos os chunks com falha.
        - `/skipchunk` – Pula um chunk problemático e continua.
        - `/nextchunk` – Avança para o próximo chunk, mantendo o fluxo.

4. **Feedback Visual**:
    - Cada chunk enviado inclui um cabeçalho detalhado com informações de progresso, como:
      ```
      📊 PROGRESSO: Chunk 3/5
      =============================
      ▶️ 2 chunks já processados
      ▶️ 2 chunks restantes
      ▶️ 1 chunk com falha
      ▶️ Use '/nextchunk' para avançar após analisar este chunk
      =============================
      ```

---

## Estrutura do Código

O projeto está dividido em pacotes com responsabilidades específicas:

- **`cli`**: Gerencia a interface de usuário.
    - `ChatCLI`: Loop principal de interação.
    - `CommandHandler`: Processa comandos especiais (ex.: `/exit`, `/switch`).
    - `HistoryManager`: Gerencia o histórico de comandos entre sessões.
    - `AnimationManager`: Controla animações visuais durante o processamento.
    - `AgentMode` : Implementa o modo agente para execução de comandos.
- **`llm`**: Comunicação com os provedores de LLM.
    - `LLMClient`: Interface para os clientes de LLM.
    - `OpenAIClient`, `StackSpotClient`, `ClaudeAIClient`: Clientes específicos.
    - `LLMManager`: Gerencia os clientes.
    - `token_manager.go`: Gerencia tokens e suas renovações.
- **`utils`**: Funções auxiliares.
    - `file_utils.go`: Processamento de arquivos e diretórios.
    - `shell_utils.go`: Interação com o shell e histórico.
    - `git_utils.go`: Informações sobre o Git.
    - `http_client.go` e `logging_transport.go`: Clientes HTTP com logging.
    - `path.go`: Manipulação de caminhos.
- **`models`**: Estruturas de dados (ex.: `Message`, `ResponseData`).
- **`main`**: Inicialização da aplicação e configuração das dependências.

---

## Bibliotecas e Dependências

- [Zap](https://github.com/uber-go/zap) – Logging estruturado de alto desempenho.
- [Liner](https://github.com/peterh/liner) – Edição de linha e histórico na CLI.
- [Glamour](https://github.com/charmbracelet/glamour) – Renderização de Markdown no terminal.
- [Lumberjack](https://github.com/natefinch/lumberjack) – Rotação de arquivos de log.
- [Godotenv](https://github.com/joho/godotenv) – Carregamento de variáveis de ambiente.
- [Go Standard Library](https://pkg.go.dev/std) – Diversos pacotes para HTTP, manipulação de arquivos e concorrência.

---

## Integração de Logs

O ChatCLI utiliza o Zap para um logging robusto e estruturado, contando com:

- **Níveis Configuráveis**: (`debug`, `info`, `warn`, `error`).
- **Rotação de Logs**: Gerenciada pelo Lumberjack.
- **Sanitização de Dados Sensíveis**: Chaves de API, tokens e outros dados críticos são redigidos.
- **Multi-Output**: Logs exibidos no console e salvos em arquivo.
- **Detalhamento de Requisições**: Informações completas sobre métodos, URLs, cabeçalhos (com dados sensíveis removidos) e tempos de resposta.

---

## Contribuindo

Contribuições são sempre bem-vindas! Para colaborar:

1. **Fork o Repositório.**
2. **Crie uma Nova Branch**:

   ```bash
   git checkout -b feature/SeuNomeDeFeature
   ```

3. **Faça Commits com suas Alterações**:

   ```bash
   git commit -m "Descrição da alteração"
   ```

4. **Envie a Branch para o Repositório Remoto**:

   ```bash
   git push origin feature/SeuNomeDeFeature
   ```

5. **Abra um Pull Request.**

Certifique-se de seguir os padrões do projeto e que os testes estejam passando.

---

## Licença

Este projeto está licenciado sob a [Licença MIT](/LICENSE).

---

## Contato

Para dúvidas, sugestões ou suporte, abra uma issue no repositório ou acesse:  
[www.edilsonfreitas.com.br/contato](https://www.edilsonfreitas.com/#section-contact)

---

**ChatCLI** une a potência dos LLMs com a simplicidade da linha de comando, oferecendo uma ferramenta versátil para interações contínuas com IA diretamente no seu terminal. Aproveite e transforme sua experiência de produtividade!

Boas conversas! 🗨️✨