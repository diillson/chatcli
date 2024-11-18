# ChatCLI

ChatCLI é uma aplicação de interface de linha de comando (CLI) avançada que utiliza modelos de Linguagem de Aprendizado (LLMs) poderosos como StackSpot, OpenAI e ClaudeAI para facilitar conversas interativas e contextuais diretamente no seu terminal. Projetado para desenvolvedores, cientistas de dados e entusiastas de tecnologia, o ChatCLI aumenta a produtividade integrando diversas fontes de dados contextuais e proporcionando uma experiência rica e amigável ao usuário.

## 🚀 Funcionalidades

- **Suporte a Múltiplos Provedores**: Alterne facilmente entre diferentes provedores de LLM como StackSpot, OpenAI e ClaudeAI conforme suas necessidades.
- **Experiência Interativa na CLI**: Desfrute de uma interação suave na linha de comando com recursos como navegação de histórico e auto-completação de comandos.
- **Comandos Contextuais**:
    - `@history` - Integra o histórico recente de comandos do seu shell na conversa (suporta bash, zsh e fish).
    - `@git` - Adiciona informações do repositório Git atual, incluindo status, commits recentes e branches.
    - `@env` - Inclui suas variáveis de ambiente no contexto do chat.
    - `@file <caminho>` - Incorpora o conteúdo de arquivos especificados na conversa. Suporta `~` como atalho para o diretório home do usuário e expande caminhos relativos.
    - `@command <comando>` - Executa o comando de terminal fornecido e adiciona a saída ao contexto da conversa para consultas posteriores com a LLM.
    - **Novo**: `@command --ai <comando> > <contexto>` - Executa o comando de terminal e envia a saída diretamente para a LLM, com a possibilidade de passar um contexto adicional após o sinal de maior `>` para que a IA processe a saída conforme solicitado.
- **Execução de Comandos Diretos**: Execute comandos de sistema diretamente a partir do ChatCLI usando `@command`, e a saída é salva no histórico para referência.
- **Alteração Dinâmica de Configurações**: Mude o provedor de LLM, slug e tenantname diretamente do ChatCLI sem reiniciar a aplicação usando `/switch` com opções.
- **Recarregamento de Variáveis**: Altere suas configurações de variáveis de ambiente usando `/reload` para que o ChatCLI leia e modifique as configurações.
- **Feedback Animado**: Animações visuais de "Pensando..." enquanto o LLM processa suas solicitações, aumentando o engajamento do usuário.
- **Renderização de Markdown**: Respostas são renderizadas com Markdown para melhor legibilidade e formatação.
- **Histórico Persistente**: O histórico de comandos é salvo entre sessões, permitindo revisitar interações anteriores com facilidade.
- **Compatibilidade com Múltiplos Shells**: Suporte a diferentes shells (bash, zsh, fish) ao obter o histórico do shell.
- **Limite de Tamanho de Arquivos Configurável**: Evita a leitura de arquivos muito grandes (acima de 1MB por padrão) ao usar o comando `@file`. O limite pode ser configurado via variáveis de ambiente.
- **Logging Robusto**: Registro abrangente utilizando Zap com rotação de logs e sanitização de informações sensíveis para garantir segurança e manutenibilidade.
- **Tratamento Avançado de Erros**: Mensagens de erro amigáveis e informativas orientam você em caso de problemas, garantindo uma experiência de usuário fluida.
- **Retry com Backoff Exponencial**: Implementa lógica de retry com backoff exponencial para lidar com erros temporários de rede, garantindo maior confiabilidade nas interações com APIs externas.

## 📦 Instalação

### Pré-requisitos

- **Go**: Certifique-se de ter o Go instalado (versão 1.21+). Você pode baixá-lo em [golang.org](https://golang.org/dl/).

### Passos

1. **Clonar o Repositório**:

   ```bash
   git clone https://github.com/diillson/chatcli.git
   cd chatcli
   ```

2. **Instalar Dependências**:

   ```bash
   go mod tidy
   ```

3. **Compilar a Aplicação**:

   ```bash
   go build -o chatcli
   ```

4. **Executar a Aplicação**:

   ```bash
   ./chatcli
   ```

## 🛠 Configuração

O ChatCLI depende de variáveis de ambiente para configurar seu comportamento e conectar-se aos provedores de LLM. Você pode definir essas variáveis em um arquivo `.env` na raiz do projeto ou exportá-las diretamente no seu shell.

---

### Variáveis de Ambiente

O ChatCLI agora suporta o ClaudeAI como um provedor adicional de LLM. Veja como configurar a ClaudeAI e as outras variáveis de ambiente para o funcionamento do ChatCLI:

- **Geral**:
    - `LOG_LEVEL` - (Opcional) Define o nível de log (`debug`, `info`, `warn`, `error`). Padrão é `info`.
    - `ENV` - (Opcional) Define o ambiente (`prod` para produção, caso contrário, padrão é `dev` desenvolvimento) - Essencial pois muda a forma que o log transacional e exibido no terminal.
    - `LLM_PROVIDER` - (Opacional) Especifica o provedor de LLM padrão (`OPENAI`, `STACKSPOT` ou `CLAUDEAI`). Padrão é `STACKSPOT`.
    - `LOG_FILE` - (Opcional) Define o nome do arquivo de log. Padrão é `app.log`.
    - `LOG_MAX_SIZE` (Opacional) Define o tamanho maximo do log antes de realizar o backup (`3`) ao maximo por `28` dias, padrão É `50MB`, pode usar escala de MB KB GB, ex: 10MB, 500KB, 1GB.
    - `HISTORY_MAX_SIZE` - (Opcional) Define o tamanho do historico de comandos do chat `.chatcli_history` padrão é `50MB`, pode usar escala de MB KB GB, ex: 10MB, 500KB, 1GB.

- **Provedor OpenAI**:
    - `OPENAI_API_KEY` - Sua chave de API da OpenAI.
    - `OPENAI_MODEL` - (Opcional) Especifica o modelo da OpenAI a ser usado. Padrão é `gpt-4o-mini`.

- **Provedor StackSpot**:
    - `CLIENT_ID` - ID do cliente StackSpot.
    - `CLIENT_SECRET` - Segredo do cliente StackSpot.
    - `SLUG_NAME` - Nome do slug StackSpot. Padrão é `testeai` se não definido.
    - `TENANT_NAME` - Nome do tenant StackSpot. Padrão é `zup` se não definido.

- **Provedor ClaudeAI**:
    - `CLAUDEAI_API_KEY` - Sua chave de API da ClaudeAI.
    - `CLAUDEAI_MODEL` - (Opcional) Define o modelo da ClaudeAI. Padrão é `claude-3-5-sonnet-20241022`.

### Exemplo de Arquivo `.env`

```env
# Configurações Gerais
LOG_LEVEL=info
ENV=dev
LLM_PROVIDER=CLAUDEAI
LOG_FILE=app.log
HISTORY_MAX_SIZE=50MB

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
```

--- 

Esses ajustes garantem que ClaudeAI esteja configurado e documentado no `README.md`, alinhando com as práticas dos outros provedores, como OpenAI e StackSpot.

## 🎯 Uso

Após instalar e configurar, você pode começar a usar o ChatCLI com diversos comandos.

### Iniciando o ChatCLI

```bash
./chatcli
```

### Comandos Disponíveis

- **Sair do ChatCLI**:
    - `/exit` ou `exit` ou `/quit` ou `quit`

- **Alternar Provedor de LLM ou Configurações**:
    - `/switch` - Troca o provedor de LLM (interativo).
    - `/switch --slugname <slug>` - Atualiza o `slugName` sem trocar o provedor.
    - `/switch --tenantname <tenant>` - Atualiza o `tenantName` sem trocar o provedor.
    - Você pode combinar as opções: `/switch --slugname <slug> --tenantname <tenant>`
    - `/reload` - Atualiza as configurações de variáveis em tempo de execução.

- **Ajuda**:
    - `/help`

- **Comandos Especiais**:
    - `@history` - Adiciona os últimos 10 comandos do shell ao contexto da conversa.
    - `@git` - Incorpora o status atual do repositório Git, commits recentes e branches.
    - `@env` - Inclui variáveis de ambiente no chat.
    - `@file <caminho>` - Adiciona o conteúdo do arquivo especificado ao contexto da conversa. Suporta `~` como atalho para o diretório home e expande caminhos relativos.
    - `@command <comando>` - Executa o comando de terminal fornecido e adiciona a saída ao contexto da conversa.
    - **Novo**: `@command --ai <comando> > <contexto>` - Executa o comando de terminal e envia a saída diretamente para a LLM, com a possibilidade de passar um contexto adicional após o sinal de maior `>` para que a IA processe a saída conforme solicitado.

### Exemplos de Uso

1. **Conversa Básica**:

   ```
   Você: Olá, como você está?
   ```

2. **Incluindo Histórico do Shell**:

   ```
   Você: @history
   ```

3. **Adicionando Informações do Git**:

   ```
   Você: @git
   ```

4. **Incluindo Variáveis de Ambiente**:

   ```
   Você: @env
   ```

5. **Incorporando Conteúdo de Arquivo**:

   ```
   Você: @file ~/documentos/main.go
   ```

   Este comando lerá o conteúdo de `main.go` do diretório `documentos` na pasta home e o incluirá no contexto da conversa.

6. **Executando Comando no Terminal com `@command`**:

   ```
   Você: @command ls -la
   ```

   O comando `@command <comando>` permite a execução de comandos de sistema diretamente no ChatCLI sem interação com a LLM. A saída do comando é salva no histórico, possibilitando que você consulte a LLM posteriormente para análise ou diagnósticos com o contexto do comando.

   **Saída**:

   ```
   Executando comando: ls -la
   Saída do comando:
   total 8
   drwxr-xr-x  3 user  staff    96 Nov  4 12:34 .
   drwxr-xr-x  5 user  staff   160 Nov  4 10:12 ..
   -rw-r--r--  1 user  staff  1024 Nov  4 12:33 example.txt
   ```

7. **Consultando a LLM sobre um Erro em um Comando**:

   ```
   Você: @command cat arquivo_inexistente.txt
   ```

   **Saída**:

   ```
   Executando comando: cat arquivo_inexistente.txt
   Saída do comando:
   Erro: cat: arquivo_inexistente.txt: No such file or directory
   ```

   Posteriormente, você pode consultar a LLM sobre o erro:

   ```
   Você: O que aconteceu no último comando?
   ```

   A LLM terá acesso ao histórico e poderá explicar o erro ou sugerir correções.

8. **Executando Comando e Enviando Saída para a LLM com Contexto**:

   ```
   Você: @command --ai ls > Filtrar apenas os arquivos .go
   ```

   O comando `ls` será executado, e a saída será enviada para a LLM com o contexto "Filtrar apenas os arquivos .go".

9. **Alterando Provedor de LLM**:

   Para trocar o provedor de LLM durante a sessão:

   ```
   Você: /switch
   ```

   Você será solicitado a selecionar o provedor:

   ```
   Provedores disponíveis:
   1. OPENAI
   2. STACKSPOT
   3. CLAUDEAI
   Selecione o provedor (1, 2 ou 3):
   ```

10. **Atualizando `slugName` e `tenantName` sem trocar o provedor**:

```
Você: /switch --slugname novo-slug --tenantname novo-tenant
```

O `TokenManager` será atualizado com os novos valores, e um novo token será obtido.

11. **Refaz a leitura das variáveis de ambiente, identificando as mudanças e reconfigurando o ambiente**:

    ```
    Você: /reload
    ```

    As variáveis serão reconfiguradas com os novos valores, e uma nova validação de todos os recursos ocorrerá.

---
### Capturas de Tela

#### Exemplo de Execução de Comando com `@command`

![Executando Comando_01](/images/05.png)

#### Exemplo de pergunta à LLM após execução do comando

![Executando Comando_02](/images/06.png)

#### Funcionamento Geral

![Operando-1](/images/01.png)

![Operando-2](/images/02.png)

![Operando-3](/images/03.png)

![Operando-4](/images/04.png)

## 📂 Estrutura do Código

O projeto está organizado em vários pacotes, cada um responsável por diferentes aspectos da aplicação. Recentemente, o código foi refatorado para melhorar a separação de responsabilidades e facilitar a manutenção e a escalabilidade. Aqui estão os principais componentes:

- **`cli`**: Gerencia a interface de linha de comando, entrada do usuário, processamento de comandos e interação com os clientes LLM.
    - **`ChatCLI`**: A classe principal que gerencia o loop de interação com o usuário, incluindo a execução de comandos e o envio de prompts para os LLMs.
    - **`CommandHandler`**: Uma nova classe introduzida para lidar com comandos específicos da CLI, como `/exit`, `/switch`, `/reload`, e o novo `@command --ai`. Isso melhora a modularidade e facilita a adição de novos comandos no futuro.
    - **`HistoryManager`**: Gerencia o histórico de comandos do usuário, permitindo que o histórico seja salvo e carregado entre sessões.
    - **`AnimationManager`**: Gerencia animações visuais, como o feedback de "Pensando..." enquanto o LLM processa uma solicitação.

- **`llm`**: Gerencia as interações com os Modelos de Linguagem, suportando múltiplos provedores como OpenAI, StackSpot e ClaudeAI.
    - **`LLMClient`**: Interface que todos os clientes LLM devem implementar.
    - **`OpenAIClient`**: Implementa o cliente para interagir com a API da OpenAI, incluindo tratamento de erros e retries.
    - **`StackSpotClient`**: Implementa o cliente para interagir com a API da StackSpot, gerenciando tokens de acesso e chamadas à API.
    - **`ClaudeAIClient`**: Implementa o cliente para interagir com a API da ClaudeAI.

- **`token_manager.go`**: Gerencia a obtenção e renovação de tokens de acesso para a StackSpot e outros provedores que exigem autenticação.

- **`utils`**: Contém funções utilitárias para operações de arquivo, expansão de caminhos, logging, leitura de histórico do shell, obtenção de informações do Git, entre outros.
    - **`shell_utils.go`**: Contém funções para detectar o shell do usuário e obter o histórico de comandos.
    - **`git_utils.go`**: Fornece funções para obter informações do repositório Git atual.
    - **`env_utils.go`**: Fornece funções para obter as variáveis de ambiente do sistema.

- **`http_client.go`**: Cria um cliente HTTP personalizado com um `LoggingTransport` para registrar requisições e respostas.

- **`logging_transport.go`**: Implementa um `http.RoundTripper` personalizado para adicionar logs às requisições e respostas HTTP, com sanitização de dados sensíveis.

- **`path.go`**: Fornece funções para manipulação de caminhos de arquivos, incluindo expansão de `~` para o diretório home.

- **`models`**: Define as estruturas de dados utilizadas em toda a aplicação, como `Message` e `ResponseData`.

- **`main`**: Inicializa a aplicação, configura dependências e inicia a CLI.

## 📚 Bibliotecas e Dependências

- [Zap](https://github.com/uber-go/zap): Logging estruturado e de alto desempenho.
- [Liner](https://github.com/peterh/liner): Fornece edição de linha e histórico para a CLI.
- [Glamour](https://github.com/charmbracelet/glamour): Renderiza Markdown no terminal.
- [Lumberjack](https://github.com/natefinch/lumberjack): Rotação de arquivos de log.
- [Godotenv](https://github.com/joho/godotenv): Carrega variáveis de ambiente a partir de um arquivo `.env`.
- [Go Standard Library](https://pkg.go.dev/std): Utiliza diversos pacotes padrão para requisições HTTP, manipulação de arquivos, concorrência e mais.

## 🌟 Funcionalidades Avançadas

- **Expansão de Caminhos com Suporte a `~`**: O comando `@file` expande inteligentemente `~` para o diretório home do usuário, permitindo entradas flexíveis de caminhos de arquivos semelhantes aos terminais Unix-like.
- **Animações Concorrentes**: Implementa goroutines para gerenciar animações como "Pensando..." sem bloquear a thread principal, garantindo uma experiência de usuário responsiva.
- **Mecanismo de Retry com Backoff Exponencial**: Tratamento robusto de erros com lógica de retry para requisições de rede aos provedores de LLM, aumentando a confiabilidade.
- **Gerenciamento de Tokens para StackSpot**: Gerencia tokens de acesso de forma segura, lidando com a renovação automática antes da expiração para manter o serviço ininterrupto.
- **Logging Sanitizado**: Garante que informações sensíveis como chaves de API e tokens sejam redigidas nos logs, mantendo as melhores práticas de segurança.
- **Renderização de Markdown**: Utiliza Glamour para renderizar respostas em Markdown, proporcionando uma saída rica e formatada no terminal.
- **Limitação de Tamanho de Arquivo Configurável**: Evita a leitura de arquivos excessivamente grandes (acima de 1MB por padrão) para manter o desempenho e prevenir possíveis problemas.
- **Compatibilidade com Shells**: Detecta automaticamente o shell do usuário (por exemplo, bash, zsh, fish) e lê o arquivo de histórico apropriado, melhorando a compatibilidade em diferentes ambientes.
- **Persistência de Histórico de Comandos**: O ChatCLI salva o histórico de comandos em um arquivo `.chatcli_history` na pasta atual, permitindo que o histórico seja mantido entre sessões.
- **Detecção de Tipo de Arquivo**: Ao usar `@file`, o ChatCLI detecta o tipo de arquivo com base na extensão e formata o conteúdo apropriadamente, incluindo suporte a sintaxe de código em blocos de Markdown.

## 📜 Integração de Logs

O ChatCLI integra Zap para logging estruturado e de alto desempenho. As principais características do sistema de logging incluem:

- **Níveis de Log Configuráveis**: Defina o nível de log desejado (`debug`, `info`, `warn`, `error`) via variáveis de ambiente para controlar a verbosidade.
- **Rotação de Logs**: Utiliza Lumberjack para gerenciar a rotação de arquivos de log, prevenindo que os arquivos cresçam indefinidamente.
- **Sanitização de Dados Sensíveis**: O `LoggingTransport` personalizado assegura que informações sensíveis como chaves de API, tokens e cabeçalhos de autorização sejam redigidas antes de serem registradas.
- **Logging de Multi-Output**: Suporta logging tanto no console quanto em arquivos de log, dependendo do ambiente (desenvolvimento ou produção).
- **Logs Detalhados de Requisições e Respostas**: Registra requisições HTTP e respostas, incluindo método, URL, cabeçalhos (com dados sensíveis redigidos), códigos de status e durações para monitoramento e depuração.

## 🧩 Como Funciona

1. **Inicialização**:
    - A aplicação carrega as variáveis de ambiente e inicializa o logger.
    - Configura o `LLMManager` para gerenciar múltiplos provedores de LLM com base na configuração.
    - Inicializa o `TokenManager` para gerenciar tokens de acesso quando necessário (por exemplo, para StackSpot).

2. **Processamento de Comandos**:
    - Os usuários interagem com o ChatCLI via terminal, inserindo comandos e mensagens.
    - Comandos especiais como `@history`, `@git`, `@env`, `@file` e `@command` são analisados e processados para incluir contexto adicional na conversa.
    - Comandos de sistema como `/exit`, `/switch`,`/reload`, `/help` são tratados separadamente para controlar o fluxo da aplicação.

3. **Interação com LLM**:
    - O ChatCLI envia a entrada do usuário juntamente com o histórico da conversa para o provedor de LLM selecionado.
    - Para o OpenAI, utiliza a API de chat com o histórico completo de mensagens.
    - Para o StackSpot, gerencia a sessão e obtém a resposta através de polling com o `responseID`.
    - A resposta do LLM é recebida, renderizada com Markdown e exibida com um efeito de máquina de escrever para melhor legibilidade.

4. **Logging e Tratamento de Erros**:
    - Todas as interações, incluindo requisições e respostas para/dos provedores de LLM, são registradas com níveis apropriados.
    - Erros são tratados de forma elegante, fornecendo mensagens informativas ao usuário e garantindo que a aplicação permaneça estável.
    - Implementa lógica de retry com backoff exponencial para lidar com erros temporários de rede.

5. **Persistência de Histórico**:
    - O ChatCLI salva o histórico de comandos em `.chatcli_history`, permitindo que o histórico seja carregado em sessões futuras.
    - O histórico da conversa com a LLM é mantido em memória durante a sessão para proporcionar contexto contínuo.

## 🧑‍💻 Contribuindo

Contribuições são bem-vindas! Seja melhorando a documentação, adicionando novos recursos ou corrigindo bugs, sua ajuda é muito apreciada. Siga os passos abaixo para contribuir:

1. **Fork o Repositório**

2. **Crie uma Nova Branch**:

   ```bash
   git checkout -b feature/SeuNomeDeFeature
   ```

3. **Commit suas Alterações**:

   ```bash
   git commit -m "Adiciona sua mensagem"
   ```

4. **Push para a Branch**:

   ```bash
   git push origin feature/SeuNomeDeFeature
   ```

5. **Abra um Pull Request**

Por favor, assegure-se de que seu código segue os padrões de codificação do projeto e passa por todos os testes existentes.

## 📝 Licença

Este projeto está licenciado sob a [Licença MIT](/LICENSE).

## 📞 Contato

Para quaisquer perguntas, feedback ou suporte, por favor, abra uma issue no repositório ou entre em contato pelo [www.edilsonfreitas.com.br/contato](https://www.edilsonfreitas.com/#section-contact).

---

ChatCLI conecta a potência dos LLMs com a simplicidade da linha de comando, oferecendo uma ferramenta versátil para interações contínuas com IA dentro do seu ambiente de terminal. Abrace o futuro da produtividade na linha de comando com o ChatCLI!

Boas conversas! 🗨️✨
