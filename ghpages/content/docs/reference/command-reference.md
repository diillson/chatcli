+++
title = "Referência Completa de Comandos"
linkTitle = "Referência de Comandos"
weight = 20
description = "Uma folha de consulta (cheatsheet) para todos os comandos, flags e opções disponíveis no ChatCLI."
+++

Esta página é uma referência rápida e **gerada automaticamente** para todos os comandos e flags disponíveis no **ChatCLI**.
## Comandos Internos (`/`)

Estes comandos controlam a aplicação e o fluxo da conversa.

| Comando | Descrição |
| --- | --- |
| **/agent** | Iniciar modo agente para executar tarefas |
| **/auth** | Gerencia credenciais OAuth (status, login, logout) |
| **/clear** | Força redesenho/limpeza da tela se o prompt estiver corrompido ou com artefatos visuais. |
| **/coder** | Iniciar modo engenheiro (Criação e Edição de Código) |
| **/config** | Mostrar configuração atual |
| **/connect** | Conectar a um servidor ChatCLI remoto (gRPC) |
| **/context** | Gerencia contextos persistentes (create, attach, detach, list, show, etc) |
| **/disconnect** | Desconectar do servidor remoto e voltar ao modo local |
| **/exit** | Sair do ChatCLI |
| **/help** | Mostrar ajuda |
| **/newsession** | Iniciar uma nova sessão de conversa |
| **/nextchunk** | Carregar o próximo chunk de arquivo |
| **/plugin** | Gerencia plugins (install, list, show, etc.) |
| **/quit** | Alias de /exit - Sair do ChatCLI |
| **/reload** | Recarregar configurações do .env |
| **/retry** | Tentar novamente o último chunk que falhou |
| **/retryall** | Tentar novamente todos os chunks que falharam |
| **/run** | Alias para /agent - Iniciar modo agente para executar tarefas |
| **/session** | Gerencia as sessões, new, save, list, load, delete |
| **/skipchunk** | Pular um chunk de arquivo |
| **/status** | Alias de /config - Mostrar configuração atual |
| **/switch** | Trocar o provedor de LLM, seguido por --model troca o modelo |
| &nbsp; `--model <nome>` | Muda o modelo para o provedor atual. |
| &nbsp; `--max-tokens <num>` | Define um limite máximo de tokens para a resposta. |
| &nbsp; `--realm <nome>` | **(StackSpot)** Define o `realm` (tenant). |
| &nbsp; `--agent-id <id>` | **(StackSpot)** Define o `Agent ID` a ser usado. |
| **/version** | Verificar a versão do ChatCLI |
| **/watch** | Exibe o status do K8s watcher (quando ativo) |

---

## Comandos de Contexto (`@`)

Estes comandos injetam informações do seu ambiente local no prompt.

| Comando | Descrição |
| --- | --- |
| **@command** `<...>` | help.command.command |
| &nbsp; `--ai` | Envia a saída do comando diretamente para a IA para análise. |
| &nbsp; `-i`, `--interactive` | Executa o comando em modo interativo (ex: `ssh`, `vim`). |
| **@env** `<...>` | help.command.env |
| **@file** `<...>` | help.command.file |
| &nbsp; `--mode` | Define o modo de processamento: `full`, `summary`, `chunked`, `smart`. |
| &nbsp;&nbsp;&nbsp;`full` | Processa o conteúdo completo (padrão, trunca se necessário) |
| &nbsp;&nbsp;&nbsp;`summary` | Gera resumo estrutural (árvore de arquivos, tamanhos, sem conteúdo) |
| &nbsp;&nbsp;&nbsp;`chunked` | Divide grandes projetos em pedaços gerenciáveis (use /nextchunk para prosseguir) |
| &nbsp;&nbsp;&nbsp;`smart` | Seleciona arquivos relevantes com base no seu prompt (IA decide) |
| **@git** `<...>` | help.command.git |
| **@history** `<...>` | help.command.history |

---

## Modo Agente (`/agent` ou `/run`)

Delega tarefas para a IA planejar e executar. O comando principal é `/agent <tarefa>`.

#### Ações Dentro do Modo Agente

| Ação | Descrição |
| --- | --- |
| `[N]` | Executa o comando de número `N`. |
| `a` | Executa todos os comandos pendentes. |
| `eN` | Edita o comando `N` antes de executar. |
| `tN` | Simula (dry-run) o comando `N`. |
| `cN` | Pede continuação para a IA com a saída do comando `N`. |
| `pcN` | Adiciona contexto pré-execução ao comando `N`. |
| `acN` | Adiciona contexto pós-execução (à saída) do comando `N`. |
| `vN` | Visualiza a saída completa do comando `N` em um pager. |
| `wN` | Salva a saída do comando `N` em um arquivo temporário. |
| `p` | Alterna a visualização do plano (compacta/completa). |
| `r` | Redesenha a tela. |
| `q` | Sai do modo agente. |

---

## Gerenciamento de Sessões e Contextos

#### Comandos de Sessão (`/session`)

| Comando | Descrição |
| --- | --- |
| `/session save <nome>` | Salva a conversa atual com um nome. |
| `/session load <nome>` | Carrega uma conversa salva. |
| `/session list` | Lista todas as sessões salvas. |
| `/session delete <nome>` | Deleta uma sessão salva. |
| `/session new` | Inicia uma nova sessão limpa. |

#### Comandos de Contexto (`/context`)

| Comando | Descrição |
| --- | --- |
| `/context create <nome> ...` | Cria um 'snapshot' persistente de arquivos/diretórios. |
| `/context update <nome> ...` | Atualiza um contexto existente. |
| `/context attach <nome> ...` | Anexa um contexto salvo à sessão atual. |
| `/context detach <nome>` | Desanexa um contexto da sessão. |
| `/context list` | Lista todos os contextos salvos. |
| `/context show <nome>` | Mostra detalhes e arquivos de um contexto. |
| `/context inspect <nome> ...` | Mostra estatísticas detalhadas de um contexto. |
| `/context delete <nome>` | Deleta um contexto permanentemente. |
| `/context merge <novo> <c1> <c2>` | Combina múltiplos contextos em um novo. |
| `/context attached` | Mostra os contextos atualmente anexados. |
| `/context export <nome> <arq>` | Exporta um contexto para um arquivo JSON. |
| `/context import <arq>` | Importa um contexto de um arquivo JSON. |
| `/context metrics` | Exibe estatísticas gerais de uso dos contextos. |

---

## Flags de Linha de Comando (Modo One-Shot)

Use estas flags ao executar `chatcli` diretamente do seu terminal para automações.

| Flag | Descrição |
| --- | --- |
| `-p`, `--prompt "<texto>` | Executa um único prompt e sai. |
| `--provider <nome>` | Sobrescreve o provedor de IA (ex: `GOOGLEAI`). |
| `--model <nome>` | Sobrescreve o modelo de IA (ex: `gemini-1.5-pro-latest`). |
| `--timeout <duração>` | Define o tempo limite para a requisição (ex: `10s`, `1m`). |
| `--max-tokens <num>` | Limita o número de tokens na resposta. |
| `--agent-auto-exec` | No modo agente one-shot, executa o primeiro comando se for seguro. |
| `--no-anim` | Desabilita a animação 'Pensando...', útil para scripts. |
| `-v`, `--version` | Mostra a informação de versão. |
| `-h`, `--help` | Mostra a tela de ajuda. |

---

## Subcomandos

O ChatCLI suporta subcomandos para funcionalidades avançadas de servidor e monitoramento.

### `chatcli serve` — Modo Servidor gRPC

Inicia o ChatCLI como servidor gRPC para acesso remoto.

| Flag | Descrição | Padrão |
| --- | --- | --- |
| `--port <int>` | Porta do servidor gRPC | `50051` |
| `--token <string>` | Token de autenticação (vazio = sem auth) | `""` |
| `--tls-cert <path>` | Arquivo de certificado TLS | `""` |
| `--tls-key <path>` | Arquivo de chave TLS | `""` |
| `--provider <nome>` | Provedor de LLM padrão | Auto-detectado |
| `--model <nome>` | Modelo de LLM padrão | Auto-detectado |
| `--watch-deployment <nome>` | Deployment K8s a monitorar (habilita watcher) | `""` |
| `--watch-namespace <ns>` | Namespace do deployment | `"default"` |
| `--watch-interval <dur>` | Intervalo de coleta do watcher | `30s` |
| `--watch-window <dur>` | Janela de observação do watcher | `2h` |
| `--watch-max-log-lines <n>` | Max linhas de log por pod | `100` |
| `--watch-kubeconfig <path>` | Caminho do kubeconfig | Auto-detectado |

### `chatcli connect` — Conexão Remota

Conecta a um servidor ChatCLI remoto via gRPC.

| Flag | Descrição | Padrão |
| --- | --- | --- |
| `<address>` | Endereço do servidor (posicional) |  |
| `--addr <host:port>` | Endereço do servidor (flag) | `""` |
| `--token <string>` | Token de autenticação | `""` |
| `--provider <nome>` | Sobrescreve o provedor LLM do servidor | `""` |
| `--model <nome>` | Sobrescreve o modelo LLM do servidor | `""` |
| `--llm-key <string>` | Sua própria API key (enviada ao servidor) | `""` |
| `--use-local-auth` | Usa credenciais OAuth do auth store local | `false` |
| `--tls` | Habilita conexão TLS | `false` |
| `--ca-cert <path>` | Certificado CA para TLS | `""` |
| `-p <prompt>` | One-shot: envia prompt e sai | `""` |
| `--raw` | Saída crua (sem formatação) | `false` |
| `--max-tokens <int>` | Máximo de tokens na resposta | `0` |
| `--client-id <string>` | StackSpot Client ID | `""` |
| `--client-key <string>` | StackSpot Client Key | `""` |
| `--realm <string>` | StackSpot Realm/Tenant | `""` |
| `--agent-id <string>` | StackSpot Agent ID | `""` |
| `--ollama-url <url>` | URL base do Ollama | `""` |

### `chatcli watch` — Monitoramento Kubernetes

Monitora um deployment Kubernetes e injeta contexto K8s nas conversas com a IA.

| Flag | Descrição | Padrão |
| --- | --- | --- |
| `--deployment <nome>` | Deployment a monitorar (obrigatório) | `""` |
| `--namespace <ns>` | Namespace do deployment | `"default"` |
| `--interval <dur>` | Intervalo de coleta | `30s` |
| `--window <dur>` | Janela de observação | `2h` |
| `--max-log-lines <n>` | Max linhas de log por pod | `100` |
| `--kubeconfig <path>` | Caminho do kubeconfig | Auto-detectado |
| `--provider <nome>` | Provedor de LLM | `.env` |
| `--model <nome>` | Modelo de LLM | `.env` |
| `-p <prompt>` | One-shot: envia prompt com contexto K8s e sai | `""` |
| `--max-tokens <int>` | Máximo de tokens na resposta | `0` |

---

## Comando `/watch` (Modo Interativo)

Disponível dentro do ChatCLI interativo (local ou remoto):

| Comando | Descrição |
| --- | --- |
| `/watch status` | Mostra o status do K8s Watcher (local ou remoto) |
| `/watch` | Mostra ajuda do comando watch |
