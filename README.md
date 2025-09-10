# ChatCLI

![Lint & Test](https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg)
[![GitHub release](https://img.shields.io/github/v/release/diillson/chatcli)](https://github.com/diillson/chatcli/releases)
![GitHub issues](https://img.shields.io/github/issues/diillson/chatcli)
![GitHub last commit](https://img.shields.io/github/last-commit/diillson/chatcli)
![GitHub code size in bytes](https://img.shields.io/github/languages/code-size/diillson/chatcli)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/diillson/chatcli?label=Go%20Version)
![GitHub](https://img.shields.io/github/license/diillson/chatcli)

O **ChatCLI** é uma aplicação de linha de comando (CLI) avançada que integra modelos de Linguagem de Aprendizado (LLMs) poderosos (como OpenAI, StackSpot, GoogleAI e ClaudeAI) para facilitar conversas interativas e contextuais diretamente no seu terminal. Projetado para desenvolvedores, cientistas de dados e entusiastas de tecnologia, o ChatCLI potencializa a produtividade ao agregar diversas fontes de dados contextuais e oferecer uma experiência rica e amigável.

---

## Índice

- [Características Principais](#características-principais)
- [Instalação](#instalação)
- [Configuração](#configuração)
- [Uso e Comandos](#uso-e-comandos)
    - [Iniciando a Aplicação](#iniciando-a-aplicação)
    - [Modo não-interativo (one-shot via flags)](#modo-não-interativo-one-shot-via-flags)
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

- **Provedor Google AI (Gemini)**:
    - `GOOGLEAI_API_KEY` – Chave de API do Google AI.
    - `GOOGLEAI_MODEL` – (Opcional) Modelo a ser utilizado (padrão: `gemini-2.0-flash-lite`)
    - `GOOGLEAI_MAX_TOKENS` – (Opcional) Máximo de tokens na resposta (padrão: `8192`).

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

# Configurações do Google AI (Gemini)
GOOGLEAI_API_KEY=sua-chave-googleai
GOOGLEAI_MODEL=gemini-2.5-flash
GOOGLEAI_MAX_TOKENS=20000
```

---

## Uso e Comandos

Após a instalação e configuração, o ChatCLI oferece uma série de comandos que facilitam a interação com a LLM.

### Iniciando a Aplicação

- Modo interativo:
```bash
./chatcli
```

- Modo não-interativo (one-shot em linha única):
```bash
./chatcli -p "Seu prompt aqui"
```

---

### Modo não-interativo (one-shot via flags)
    
O ChatCLI agora suporta um modo “one-shot”, no qual você executa um prompt em **uma única linha** e o processo finaliza sem entrar no loop interativo. Esse modo é ideal para scripts, CI/CD, aliases e automações.

Este modo agora também suporta a execução de comandos do **Modo Agente** para automação de tarefas. Veja a seção "Modo Agente One-Shot" para mais detalhes.
    
#### Flags disponíveis
    
- `-p` ou `--prompt`: texto a enviar para a LLM em uma única execução.
- `--provider`: sobrescreve o provedor de LLM em tempo de execução (`OPENAI`, `CLAUDEAI`, `GOOGLEAI`, `OPENAI_ASSISTANT`, `STACKSPOT`).
- `--model`: escolhe o modelo do provedor ativo (ex.: `gpt-4o-mini`, `claude-3-5-sonnet-20241022`, `gemini-2.5-flash`, etc.).
- `--timeout`: timeout da chamada one-shot (padrão: `5m`).
- `--no-anim`: desabilita animações (útil em scripts/CI).
    
Observação: as mesmas features de contexto funcionam dentro do texto do `--prompt`, como `@file`, `@git`, `@env`, `@command` e o operador `>` para adicionar contexto. Lembre-se de colocar o prompt entre aspas no shell para evitar interpretações indesejadas.
    
#### Exemplos rápidos

- Execução simples:
```bash
chatcli -p "Explique rapidamente este repositório."
```
- Com comandos contextuais:
```bash
chatcli -p "@git @env Monte um release note enxuto."
```
- Enviando diretórios/arquivos (com os modos existentes do  @file ):
```bash
    chatcli -p "@file ./src --mode summary Faça um panorama da arquitetura."
```
- Sobrescrevendo provedor/modelo em tempo de execução:
```bash
chatcli -p "Resuma o CHANGELOG" \
  --provider=CLAUDEAI \
  --model=claude-3-5-sonnet-20241022
```
- Sem animação (útil para CI):
```bash
chatcli -p "O que este código faz?" --no-anim
```
- Timeout customizado:
```bash
chatcli -p "Analise detalhadamente a arquitetura" --timeout=15m
```

### Entrada via stdin (pipes)
Além de `-p/--prompt`, o ChatCLI aceita entrada via stdin em modo one-shot. Isso permite usar pipes com facilidade:
    
- Apenas stdin:
```bash
echo "Explique rapidamente este repositório." | chatcli
```
- stdin + prompt (concatena os dois):
```bash
git diff | chatcli -p "Resuma as mudanças e liste possíveis impactos."
ou
echo "Explique rapidamente este repositório." | chatcli -p
```
- Com provider/model override:
```bash
cat README.md | chatcli \
  -p "Resuma o README e sugira melhorias" \
  --provider=CLAUDEAI \
  --model=claude-3-5-sonnet-20241022
```
- Sem animações (CI-friendly):
```bash
cat main.go | chatcli -p "O que este código faz?" --no-anim
```

#### Dicas e boas práticas

- Quoting: use aspas duplas sempre no prompt em modo one-shot para evitar expanções do shell, especialmente se usar  >  para adicionar contexto.
- Pipes: não é necessário pipe/echo no modo one-shot; preferível usar  `-p ou -prompt` mas é possível caso necessário como nos exemplos.
- Se  `-p`  estiver presente e houver stdin, por padrão os textos serão concatenados (o prompt de  `-p`  primeiro, seguido do `stdin`).
- Se desejar priorizar apenas  `-p`  e ignorar `stdin`, ajuste o código conforme a sua preferência (veja comentários no `main.go`).
- Saída: por padrão, a resposta é renderizada em Markdown. Para pipelines/parsings estritos, considere desativar animações com `--no-anim`após a mensagem.
- Exit codes: retorno  `0`  em sucesso,  `1`  em erro de execução,  `2`  em erro de parsing de flags (conforme implementação).
- Integração com scripts (Makefile):
```bash
one-shot:
    chatcli -p "@file ./ --mode summary Gere um resumo para o README."
```

- Exemplo (GitHub Actions):
```bash
- name: ChatCLI one-shot
  run: |
    chatcli -p "@file ./ --mode summary Gere um overview do projeto"
    $VAR_XPTO | chatcli -p "analise os valores"
```

---

### Comandos Gerais

- **Encerrar a Sessão**:
    - `/exit`, `exit`, `/quit` ou `quit`

- **Alternar Provedor ou Configurações**:
    - `/switch` – Troca o provedor de LLM (modo interativo).
    - `/switch --model <nome-do-modelo>`  – Altera o modelo do provedor atual (ex:  `gpt-4o-mini` ,  `claude-3-5-sonnet-20241022` ).
    - `/switch --slugname <slug>` – Atualiza somente o `slugName`.
    - `/switch --tenantname <tenant>` – Atualiza somente o `tenantName`.
    - Combinações: `/switch --slugname <slug> --tenantname <tenant>`
    - `/reload` – Recarrega as variáveis de ambiente em tempo real.
    - `/config` ou `/status` (ou `/settings`) – Exibe as configurações atuais do ChatCLI.
       - Mostra: provedor e modelo em uso (runtime), nome do modelo reportado pelo client, API preferida (catálogo), MaxTokens efetivo (estimado), overrides de tokens por ENV, caminho do `.env`, provedores disponíveis e (quando aplicável) `slugName`/`tenantName` da StackSpot.
       - Segurança: nunca imprime valores de segredos (ex.: chaves de API); exibe apenas a presença como `[SET]`/`[NOT SET]` e não envia nada para a LLM.
       - Exemplo de uso:
         ```
         /config
         ```
         Saída esperada (resumo):
         - Provider atual: OPENAI
         - Modelo atual: gpt-4o-mini (client: GPT-4o mini)
         - API preferida: chat_completions
         - MaxTokens efetivo: 50000

- **Iniciar uma Nova Sessão**:
    - `/newsession` – Limpa o histórico atual e inicia uma nova sessão de conversa.
    - **Uso**: Ideal para começar uma conversa do zero sem o contexto anterior, anteriormente recebia um clean no historico de conversa e contexto ao trocar de provider `LLM`, hoje é possível continuar a sessão em novo provider `LLM` sem perder o histórico anterior, com o comando `/newsession` você pode zerar o histórico e contexto atual e iniciar uma nova sessão de conversa no novo provider se assim desejar.

- **Verificar Versão e Atualizações**:
    - `/version` ou `/v` – Mostra a versão atual, o hash do commit e verifica se há atualizações disponíveis.
    - **Uso**: Útil para confirmar qual versão está instalada e se há novas versões disponíveis.
    - **Alternativa**: Execute `chatcli --version` ou `chatcli -v` diretamente do terminal.  
- **Cancelando Operações em Andamento**:
    -  `Ctrl+C`  (uma vez): Cancela a operação atual (ex: a espera pela resposta da IA, o "Pensando...") sem fechar o ChatCLI. Você retornará ao prompt.
    -  `Ctrl+C`  (duas vezes rápido) ou  `Ctrl+D : Encerra a aplicação.
- **Ajuda**:
    - `/help`

---

### Comandos Contextuais

- `@history` – Insere os últimos 10 comandos do shell.
- `@git` – Incorpora informações do repositório Git.
- `@env` – Insere variáveis de ambiente no contexto.
- `@file <caminho>` – Insere o conteúdo de um arquivo ou diretório.
- `@command <comando>` – Executa um comando do terminal e salva a saída.
- `@command --ai <comando> > <contexto>` – Envia a saída do comando diretamente para a LLM com contexto adicional.
- - Observação: variáveis sensíveis e saídas são sanitizadas (tokens/segredos são redigidos) antes de irem para a LLM.

---

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

  #### Modo Agente One-Shot (Não-Interativo)

Você pode usar o Modo Agente diretamente da linha de comando, o que é perfeito para scripts e automação.

**1. Modo Padrão (Dry-Run): Apenas Sugestão**

Por padrão, ao chamar o agente no modo one-shot, ele apenas **sugere** o melhor comando para a tarefa e sai, sem executar nada.

# A IA irá analisar o pedido e imprimir o comando `find . -name "*.go"`, depois sairá.
```bash
chatcli -p "/agent liste todos os arquivos .go neste diretório"
```

**2. Modo de Execução Automática**

Para que o  chatcli  execute o comando sugerido, adicione o flag  `--agent-auto-exec` .

- Segurança: Por segurança, o agente executará apenas o primeiro comando sugerido e bloqueará automaticamente a execução de comandos considerados perigosos (como  rm -rf ,  sudo ,  drop database , etc.).

# A IA irá gerar um comando como `touch test_file.txt` e executá-lo imediatamente.
```bash
chatcli -p "/agent crie um arquivo chamado test_file.txt" --agent-auto-exec
```

# Usando stdin
```bash
echo "liste todos os arquivos .go e conte suas linhas" | chatcli -p "/agent"
```
# Exemplo com contexto:
```bash
chatcli -p "/agent @git qual o status do git neste repositório?" --agent-auto-exec
```

# Exemplo com contexto de arquivo
```bash
chatcli -p "/agent @file ./README.md resuma este arquivo em uma frase" --agent-auto-exec
```
***3. Comando Perigoso (Será Bloqueado)***

# O chatcli se recusará a executar o comando e sairá com uma mensagem de erro.
```bash
chatcli -p "/agent delete todos os arquivos da pasta tmp" --agent-auto-exec
```

#### Refinando Comandos Antes da Execução

Você pode pedir à IA para refinar um comando sugerido antes de executá-lo, fornecendo contexto adicional.

-  `pCN`  (Pré-Contexto para o comando N): Use esta opção para adicionar instruções antes da execução.

##### Exemplo de Refinamento:

1. A IA sugere o comando #1:  `ls -la
2. Você digita:  `pC1`
3. Você adiciona o contexto:  Na verdade, eu só quero ver os arquivos .go e contar as linhas de cada um.
4. A IA processará seu pedido e sugerirá um novo comando, como  `find . -name "*.go" -exec wc -l {} +` .

#### Adicionando contexto aos outputs no modo Agente !!
- agora você pode adicionar contexto aos outputs dos comandos executados pelo agente

Funcionalidade `aCN` , você poderá:

1. Executar um comando (por exemplo,  `1`  para executar o comando #1)
2. Ver o resultado do comando
3. Digitar  `aC1`  para adicionar contexto ao comando #1
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
      Você: @file --mode chunked ~/meu-projeto-grande/
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
      Você: @file --mode summary ~/meu-projeto/
      ```

4. **Modo Inteligente (Smart)**
    - **Uso**: Análise focada, onde você fornece uma pergunta e o sistema seleciona os arquivos mais relevantes.
    - **Funcionamento**:
        - O ChatCLI atribui uma pontuação de relevância a cada arquivo com base na pergunta e inclui somente os mais pertinentes.
    - **Exemplo**:
      ```
      Você: @file --mode smart ~/meu-projeto/ Como funciona o sistema de login?
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