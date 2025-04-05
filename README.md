# ChatCLI

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

  ## Suporte a APIs de Sessão

  O ChatCLI agora oferece suporte às APIs de Sessão dos principais provedores de LLM, permitindo manter o contexto da conversa no lado do provedor de LLM:

  ### Benefícios das APIs de Sessão

    - **Economia de Tokens**: Elimina a necessidade de enviar todo o histórico de conversa em cada requisição
    - **Conversas Mais Longas**: Supera as limitações locais de contexto, permitindo conversas praticamente ilimitadas
    - **Respostas Mais Rápidas**: Processamento mais eficiente por não precisar interpretar o contexto completo a cada vez

  ### Provedores Suportados

    - **OpenAI Assistants API**: Utiliza threads e assistentes para manter contexto persistente
    - **Claude Messages API**: Utiliza conversation_id para rastrear a conversa

  ### Como Usar

    1. **Via Variáveis de Ambiente**:
       ```bash
       # Para OpenAI
       export OPENAI_USE_SESSION=true
       
       # Para Claude
       export CLAUDEAI_USE_SESSION=true
       
       ps: pode adicionar em seu .env
       ```
    2. Interativamente: Ao usar  /switch  para trocar de provedor, o ChatCLI perguntará se você deseja usar a API de sessão

  Para mais detalhes, consulte a documentação completa sobre APIs de Sessão /docs/SESSION_API.md.

---

## Requisitos de Sistema
    
- **Sistema Operacional**: Linux, macOS ou Windows (via WSL recomendado)
- **Go**: Versão 1.23 ou superior
- **Terminal**: Com suporte a cores e Unicode para melhor visualização
- **Shells Suportados**: bash, zsh, fish (para funcionalidades completas de @history)
- **Git**: (opcional) Para uso do comando @git

---

## Segurança e Privacidade

- As chaves de API e tokens são armazenados apenas em variáveis de ambiente, não em arquivos persistentes.
- O histórico de comandos é armazenado localmente (`.chatcli_history`).
- Comunicações com as APIs dos provedores são feitas via HTTPS.
- Em logs, os valores sensíveis (tokens, senhas) são automaticamente redatados.
- Para ambientes corporativos, considere políticas adicionais de segurança para chaves de API.

---

## Compatibilidade de Modelos

### OpenAI

- Modelos Suportados: gpt-4o, gpt-4o-mini, gpt-4, gpt-3.5-turbo
- Modelo Padrão: gpt-4o-mini

### ClaudeAI
- Modelos Suportados: claude-3-5-sonnet-20241022, claude-3-7-sonnet-20250219, claude-3-haiku, claude-3-opus
- Modelo Padrão: claude-3-5-sonnet-20241022

### StackSpot
- Usa o modelo padrão configurado na plataforma StackSpot

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
    - `OPENAI_MODEL` – (Opcional) Modelo a ser utilizado (padrão: `gpt-4o-mini`).

- **Provedor StackSpot**:
    - `CLIENT_ID` – ID do cliente.
    - `CLIENT_SECRET` – Segredo do cliente.
    - `SLUG_NAME` – (Opcional) Nome do slug (padrão: `testeai`).
    - `TENANT_NAME` – (Opcional) Nome do tenant (padrão: `zup`).

- **Provedor ClaudeAI**:
    - `CLAUDEAI_API_KEY` – Chave de API da ClaudeAI.
    - `CLAUDEAI_MODEL` – (Opcional) Modelo (padrão: `claude-3-5-sonnet-20241022`).
    - `CLAUDEAI_MAX_TOKENS` – (Opcional) Máximo de tokens na resposta (padrão: `8192`).

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

# Configurações do StackSpot
CLIENT_ID=seu-cliente-id
CLIENT_SECRET=seu-cliente-secreto
SLUG_NAME=seu-slug-stackspot
TENANT_NAME=seu-tenant-name

# Configurações do ClaudeAI
CLAUDEAI_API_KEY=sua-chave-claudeai
CLAUDEAI_MODEL=claude-3-5-sonnet-20241022
CLAUDEAI_MAX_TOKENS=8192
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

- **Ajuda**:
    - `/help`

### Comandos Contextuais

- `@history` – Insere os últimos 10 comandos do shell.
- `@git` – Incorpora informações do repositório Git.
- `@env` – Insere variáveis de ambiente no contexto.
- `@file <caminho>` – Insere o conteúdo de um arquivo ou diretório.
- `@command <comando>` – Executa um comando do terminal e salva a saída.
- **Novo**: `@command --ai <comando> > <contexto>` – Envia a saída do comando diretamente para a LLM com contexto adicional.

---

## Depuração com Contexto
### Buscar ajuda para resolver um erro com contexto de ambiente
```    
Você: @command npm test > Ajude-me a entender por que estes testes estão falhando.
```   

### Análise de logs com contexto Git
```
Você: @command docker logs app-container | grep ERROR @git > O que pode estar causando estes erros em relação às alterações recentes?
```
---

## Desenvolvimento Assistido

### Gerar testes unitários para um arquivo
```
Você: @file ~/projeto/src/utils.js Gere testes Jest abrangentes para estas funções.
```    

### Criar documentação
```
Você: @file ~/projeto/api.go > Gere uma documentação OpenAPI 3.0 baseada nesta implementação.
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

## RAG - (Retrieval Augmented Generation) - ESPECIFICO para projetos GRANDES e COMPLEXOS
    
O ChatCLI agora inclui suporte a RAG (Retrieval Augmented Generation) em memória, permitindo analisar e consultar projetos de código grandes de forma eficiente:
    
### Benefícios do RAG
    
- **Consulta Semântica**: Encontre partes relevantes do código com base no significado, não apenas em correspondências exatas
- **Superação de Limites de Contexto**: Analise projetos muito maiores que o limite de contexto do LLM
- **Respostas Mais Precisas**: Fornece ao LLM apenas o código relevante para sua pergunta
- **Economia de Tokens**: Reduz significativamente o uso de tokens ao enviar apenas o conteúdo necessário
    
### Comandos RAG Principais
    
- **`/rag index <caminho>`**: Indexa um projeto para consultas
- **`/rag query <pergunta>`**: Consulta o projeto indexado e recebe uma resposta
- **`/rag clear`**: Limpa o índice atual
- **`@inrag <consulta>`**: Incorpora resultados RAG em um prompt regular
- **`/projeto <caminho> <pergunta>`**: Combina indexação e consulta em um único comando
    
### Exemplo de Uso
    
```bash
  # Indexar um projeto
  /rag index ~/projetos/minha-aplicacao
    
  # Consultar o código indexado
  /rag query Como funciona o sistema de autenticação?
    
  # Incorporar contexto RAG em uma pergunta normal
  Explique este algoritmo @inrag processamento de imagens
    
  # Analisar um projeto em um único comando
  /projeto ~/projetos/minha-aplicacao Explique a arquitetura e os padrões de design usados
```

Para detalhes completos sobre o sistema RAG, consulte [RAG.md](/docs/RAG.md).

---

## Desempenho

- **Processamento de Arquivos**: O sistema pode processar projetos de até ~50MB com divisão automática em chunks.
- **Tempo de Resposta**: Varia de acordo com o provedor de LLM utilizado.
- **Tempo de Inicialização**: <1 segundo em máquinas modernas.
- **Uso de Memória**: Tipicamente <100MB para a aplicação base, variando conforme o tamanho dos arquivos processados.


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

## Solução de Problemas
    
### Erros Comuns
    
- **"Token Limit Exceeded"**: Ao processar arquivos grandes, tente usar o modo chunked (`@file --mode=chunked`) para dividir em partes menores.
      
- **"Rate Limit Exceeded"**: O ChatCLI implementa backoff exponencial, mas em caso de muitas requisições rápidas:
- Aguarde alguns minutos antes de tentar novamente
- Reduza o número de requisições consecutivas
- Verifique os limites da sua conta no provedor utilizado
    
- **Erro ao Carregar o Arquivo .env**: Verifique se o arquivo está no mesmo diretório ou especifique o caminho correto via `CHATCLI_DOTENV`.
    
- **Erros de Autenticação**:
- Verifique se as chaves de API estão corretas e não expiraram
- Para o StackSpot, use `/switch --slugname <slug> --tenantname <tenant>` para atualizar credenciais
    
### Como Depurar
    
O ChatCLI implementa logs detalhados. Defina `LOG_LEVEL=debug` no seu `.env` para aumentar a verbosidade dos logs.

---

## Estrutura do Código

O projeto está dividido em pacotes com responsabilidades específicas:

- **`cli`**: Gerencia a interface de usuário.
    - `ChatCLI`: Loop principal de interação.
    - `CommandHandler`: Processa comandos especiais (ex.: `/exit`, `/switch`).
    - `HistoryManager`: Gerencia o histórico de comandos entre sessões.
    - `AnimationManager`: Controla animações visuais durante o processamento.
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

## Como Citar
    
Se você utilizar o ChatCLI em um artigo, blog ou projeto, por favor cite:


Freitas, E. (2025). ChatCLI: Uma interface de linha de comando para LLMs. GitHub. https://github.com/diillson/chatcli

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