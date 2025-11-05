+++
title = "Referência Completa de Comandos"
linkTitle = "Referência de Comandos"
weight = 20
description = "Uma folha de consulta (cheatsheet) para todos os comandos, flags e opções disponíveis no ChatCLI."
+++

Esta página é uma referência rápida para todos os comandos e flags disponíveis no **ChatCLI**. Use-a como uma folha de consulta para encontrar rapidamente a sintaxe que você precisa.

## Comandos Internos (`/`)

Estes comandos controlam a aplicação e o fluxo da conversa.

| Comando                       | Descrição                                                                      |
| ----------------------------- | ------------------------------------------------------------------------------ |
| **/help**                     | Mostra a tela de ajuda completa.                                                |
| **/exit** ou **/quit**         | Encerra a aplicação.                                                           |
| **/newsession**               | Limpa o histórico da conversa e inicia uma nova sessão.                        |
| **/version** ou **/v**         | Exibe a versão, build e verifica se há atualizações.                          |
| **/config** ou **/status**     | Mostra todas as configurações ativas (provedor, modelo, chaves, etc.).          |
| **/reload**                   | Recarrega a configuração do arquivo `.env` em tempo real.                        |
| **/switch [opções]**          | Abre o menu para trocar de provedor de LLM.                                    |
| &nbsp; `--model <nome>`       | Muda o modelo para o provedor atual, sem trocar de provedor.                    |
| &nbsp; `--max-tokens <num>`   | Define um limite máximo de tokens para a resposta da IA.                         |
| &nbsp; `--realm <nome>`       | **(StackSpot)** Define o `realm` (tenant) para autenticação.                       |
| &nbsp; `--agent-id <id>`      | **(StackSpot)** Define o `Agent ID` a ser usado.                                   |
| **/nextchunk**                | Envia o próximo "chunk" de um contexto adicionado com `@file --mode=chunked`.      |
| **/retry**                    | Reenvia o último chunk que falhou.                                              |
| **/retryall**                 | Reenvia todos os chunks que falharam.                                            |
| **/skipchunk**                | Pula o chunk atual e remove-o da fila de pendentes.                           |

---

## Comandos de Contexto (`@`)

Estes comandos injetam informações do seu ambiente local no prompt.

| Comando                     | Descrição                                                                      |
| --------------------------- | ------------------------------------------------------------------------------ |
| **@file** `<caminho> [opções]` | Adiciona o conteúdo de um arquivo ou diretório.                                |
| &nbsp; `--mode=full`        | **(Padrão)** Envia o conteúdo completo (pode ser truncado).                       |
| &nbsp; `--mode=summary`     | Envia apenas a estrutura de arquivos (árvore de diretórios).                    |
| &nbsp; `--mode=chunked`      | Divide projetos grandes em "chunks" para processamento sequencial.            |
| &nbsp; `--mode=smart`       | A IA seleciona os arquivos mais relevantes para a sua pergunta.                 |
| **@git**                    | Adiciona o status do Git (`diff`, `status`, logs recentes) ao contexto.          |
| **@command** `<comando>`      | Executa um comando e usa sua saída como contexto.                              |
| &nbsp; `-i`, `--interactive`  | Executa o comando em modo interativo (ex: `ssh`, `vim`).                         |
| &nbsp; `--ai`               | Envia a saída do comando diretamente para a IA para análise.                     |
| **@env**                    | Adiciona as variáveis de ambiente (valores sensíveis são ocultados).             |

---

## Modo Agente (`/agent` ou `/run`)

Delega tarefas para a IA planejar e executar.

| Comando                      | Descrição                                                                      |
| ---------------------------- | ------------------------------------------------------------------------------ |
| **/agent <tarefa>**          | Inicia o modo agente com uma instrução em linguagem natural.                   |
| **/run <tarefa>**            | Atalho (alias) para `/agent`.                                                  |

#### Ações Dentro do Modo Agente

| Ação    | Descrição                                                                |
| :-------- | :----------------------------------------------------------------------- |
| `[N]`     | Executa o comando de número `N`.                                         |
| `a`       | Executa todos os comandos pendentes.                                     |
| `eN`      | Edita o comando `N` antes de executar.                                   |
| `tN`      | Simula (dry-run) o comando `N`.                                             |
| `cN`      | Pede continuação para a IA com a saída do comando `N`.                     |
| `pcN`     | Adiciona contexto pré-execução ao comando `N`.                             |
| `acN`     | Adiciona contexto pós-execução (à saída) do comando `N`.                     |
| `vN`      | Visualiza a saída completa do comando `N`.                               |
| `wN`      | Salva a saída do comando `N` em um arquivo.                                |
| `p`       | Alterna a visualização do plano (compacta/completa).                     |
| `r`       | Redesenha a tela.                                                        |
| `q`       | Sai do modo agente.                                                      |

---

## Gerenciamento de Sessões (`/session`)

| Subcomando            | Descrição                                                                      |
| --------------------- | ------------------------------------------------------------------------------ |
| **/session save** `<nome>` | Salva a conversa atual com um nome.                                            |
| **/session load** `<nome>` | Carrega uma conversa salva.                                                    |
| **/session list**          | Lista todas as sessões salvas.                                                 |
| **/session delete** `<nome>`| Deleta uma sessão salva.                                                       |
| **/session new**           | Inicia uma nova sessão limpa.                                                  |

---

## Gerenciamento de Contexto (`/context`)

| Subcomando                       | Descrição                                                                      |
| -------------------------------- | ------------------------------------------------------------------------------ |
| **/context create** `<nome> ...`   | Cria um "snapshot" persistente de arquivos/diretórios.                           |
| **/context update** `<nome> ...`   | Atualiza um contexto existente com novos arquivos ou metadados.                 |
| **/context attach** `<nome> ...`   | Anexa um contexto salvo à sua sessão atual.                                    |
| **/context detach** `<nome>`       | Desanexa um contexto da sua sessão.                                             |
| **/context list**                | Lista todos os contextos salvos.                                                 |
| **/context show** `<nome>`         | Mostra detalhes e arquivos de um contexto.                                     |
| **/context inspect** `<nome> ...`  | Mostra estatísticas detalhadas (linhas, tipos de arquivo) de um contexto.    |
| **/context delete** `<nome>`       | Deleta um contexto permanentemente.                                            |
| **/context merge** `<novo> <c1> <c2>` | Combina múltiplos contextos em um novo.                                         |
| **/context attached**            | Mostra os contextos atualmente anexados.                                         |
| **/context export** `<nome> <arq>` | Exporta um contexto para um arquivo JSON.                                        |
| **/context import** `<arq>`        | Importa um contexto de um arquivo JSON.                                        |
| **/context metrics**             | Exibe estatísticas gerais de uso dos contextos.                                |

---

## Flags de Linha de Comando (Modo One-Shot)

Use estas flags ao executar `chatcli` diretamente do seu terminal para automações.

| Flag                             | Descrição                                                                      |
| -------------------------------- | ------------------------------------------------------------------------------ |
| `-p`, `--prompt "<texto>"`       | Executa um único prompt e sai.                                                   |
| `--provider <nome>`              | Sobrescreve o provedor de IA (ex: `GOOGLEAI`).                                   |
| `--model <nome>`                 | Sobrescreve o modelo de IA (ex: `gemini-1.5-pro-latest`).                        |
| `--timeout <duração>`            | Define o tempo limite para a requisição (ex: `10s`, `1m`).                       |
| `--max-tokens <num>`             | Limita o número de tokens na resposta.                                         |
| `--agent-auto-exec`              | No modo agente one-shot, executa o primeiro comando se for seguro.                 |
| `--no-anim`                      | Desabilita a animação "Pensando...", útil para scripts.                           |
| `-v`, `--version`                | Mostra a informação de versão.                                                   |
| `-h`, `--help`                   | Mostra a tela de ajuda.                                                          |

--------