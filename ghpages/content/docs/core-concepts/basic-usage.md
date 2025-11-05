+++
title = "Uso Básico e Comandos Principais"
linkTitle = "Uso Básico"
weight = 10
description = "Aprenda a interagir com o ChatCLI e a usar os comandos essenciais para navegação, configuração e controle."
+++

## Modo Interativo

O modo padrão do ChatCLI é o interativo. Para iniciá-lo, basta executar o comando no seu terminal sem nenhum argumento:

```bash
./chatcli
```
Você será saudado com uma tela de boas-vindas e um prompt ( ❯ ), pronto para receber suas perguntas ou comandos.

🤖 Você está conversando com gpt-4o-mini (OPENAI)

> ❯ me ajude a listar todos os containers docker ativos

Qualquer texto que não comece com  /  ou  @  será tratado como um prompt para a Inteligência Artificial.

## Comandos Essenciais

Os comandos internos são seu centro de controle para o ChatCLI. Eles sempre começam com uma barra ( / ).
```bash
Comando            │ Descrição                                                                                                                      
───────────────────┼────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
/help              │ Exibe a tela de ajuda completa com todos os comandos e opções disponíveis.                                                     
/exit ou /quit     │ Encerra a aplicação do ChatCLI de forma segura.                                                                                
/newsession        │ Limpa o histórico da conversa atual, permitindo que você inicie um novo diálogo do zero.                                       
/config ou /status │ Mostra a configuração atual, incluindo o provedor e modelo de IA ativos, e chaves de ambiente (com valores sensíveis ocultos).
/reload            │ Recarrega a configuração do arquivo  .env  sem precisar reiniciar a aplicação.                                                 
/switch            │ Abre um menu interativo para trocar o provedor de LLM ou alterar o modelo atual.
```
--------

## Uma Breve Visão dos Comandos Avançados

Além dos comandos essenciais, o poder do ChatCLI reside em sua capacidade de entender o contexto e executar ações. Estes são os dois principais tipos de comandos avançados:

#### Comandos de Contexto ( @ )

Estes comandos, que começam com  @ , são usados para injetar informações dinâmicas no seu prompt.

•  @file <caminho> : Anexa o conteúdo de um arquivo ou a estrutura de um diretório.
•  @git : Fornece à IA o estado atual do seu repositório Git.
•  @command <comando> : Executa um comando no seu terminal e usa a saída como contexto.

>Estes comandos serão detalhados na próxima seção.

#### Modo Agente ( /agent )

Este é o modo mais poderoso do ChatCLI. Em vez de apenas obter respostas, você pode delegar uma tarefa.

/agent organize meus arquivos .log na pasta '~/logs' em subpastas por data

A IA irá criar um plano de execução, que consiste em uma série de comandos de shell, e pedir sua aprovação antes de executá-los.

--------

## Próximos Passos

Agora que você conhece o básico, vamos mergulhar na funcionalidade que torna o ChatCLI tão poderoso: a adição de contexto.

➡️ Próximo: [Adicionando Contexto ( @  Comandos)](/docs/core-concepts/context-commands/)


---