+++
title = 'Adicionando Contexto (@ Comandos)'
linkTitle = 'Comandos de Contexto'
weight = 20
description = 'Aprenda a usar @file, @git, @command e @env para dar ao ChatCLI consciência total do seu ambiente de trabalho.'
+++

A verdadeira força do **ChatCLI** reside em sua capacidade de ir além de um simples chat e entender o contexto em que você está trabalhando. Isso é feito através dos comandos de contexto, que sempre começam com o símbolo `@`.

Esses comandos coletam informações do seu sistema local e as anexam ao seu prompt antes de enviá-lo para a IA.

---

## `@file`: Fornecer Arquivos e Diretórios

Este é, talvez, o comando mais poderoso. Ele permite que você envie o conteúdo de arquivos específicos ou a estrutura e conteúdo de diretórios inteiros.

**Sintaxe Básica:**
```bash
@file <caminho/para/arquivo_ou_diretorio> [Sua pergunta...]
```
Exemplos:

1. Analisar um arquivo específico:
@file ./src/database/connection.go me explique como a conexão com o banco de dados é feita.

2. Analisar um diretório inteiro:
@file ./src/api/ me ajude a encontrar uma possível causa para o bug no endpoint de login.


#### Modos de Processamento ( --mode )

Para lidar com diferentes cenários, o comando  @file  possui um modificador  --mode  que altera seu comportamento:

-  --mode=full  (Padrão): Envia o conteúdo completo de todos os arquivos encontrados, até atingir um limite de tamanho para evitar sobrecarga. Ideal para análises detalhadas de arquivos ou pequenos componentes.
-  --mode=summary : Envia apenas a estrutura de arquivos e diretórios, sem o conteúdo do código. Útil para obter uma visão geral de um projeto grande.
@file --mode=summary . me dê uma visão geral da arquitetura deste projeto.

-  --mode=chunked : Para projetos muito grandes. Ele divide o conteúdo em "chunks" (pedaços) gerenciáveis. Apenas o primeiro é enviado. Use o comando  /nextchunk  para enviar os pedaços seguintes na conversa.
@file --mode=chunked . Vamos analisar este projeto em partes.

-  --mode=smart : A IA recebe uma lista de todos os arquivos e, com base na sua pergunta, seleciona os mais relevantes para ler. Perfeito para perguntas específicas em grandes bases de código.
@file --mode=smart ./src me explique como o fluxo de autenticação funciona.


--------

##  @git : Contexto do Repositório

Se você está em um repositório Git, este comando é essencial. Ele coleta e anexa informações cruciais sobre o estado atual do projeto.

O que ele inclui?

- Status do repositório ( git status -s )
- Branch atual e status em relação ao remoto
- Diferenças nos arquivos modificados ( git diff )
- Os 5 commits mais recentes

Exemplo de Uso:

@git me ajude a escrever uma mensagem de commit clara e concisa para estas mudanças.

--------

##  @command : Executar e Usar a Saída

Execute qualquer comando do seu terminal e use a saída dele como contexto para sua pergunta.

Sintaxe Básica:

@command <comando> > [Sua pergunta...]

Operador  `>` : O símbolo  >  é usado para separar o comando da sua pergunta para a IA.

Exemplo de Uso:

@command kubectl get pods -n production > por que o pod de login está reiniciando?

#### Execução Interativa e Análise Direta

-  @command -i <comando> : Use a flag  -i  para comandos que exigem interação do usuário, como  vim  ou  ssh . A saída não será capturada.
-  @command --ai <comando> : Envia a saída do comando diretamente para a IA, sem precisar de uma pergunta adicional. É um atalho para análise rápida.
@command --ai cat /var/log/nginx/error.log


--------

##  @env : Fornecer Variáveis de Ambiente

Adiciona as variáveis de ambiente atuais ao contexto.

🔒 Segurança: O ChatCLI automaticamente detecta e remove valores de variáveis com nomes sensíveis (como  API_KEY ,  TOKEN ,  PASSWORD ), substituindo-os por  [REDACTED] .

Exemplo de Uso:

@env quais são as configurações de banco de dados disponíveis?

--------

## Combinando Comandos

A verdadeira magia acontece quando você combina vários comandos de contexto em um único prompt para dar à IA uma visão 360º do seu problema.

Exemplo Combinado:

@git @file ./src/main.go > baseado nas mudanças recentes, revise este arquivo e sugira melhorias de performance.

--------

## Próximos Passos

Agora você sabe como dar "olhos e ouvidos" ao ChatCLI. O próximo passo é aprender a dar a ele "mãos" para agir no seu sistema.

➡️ Próximo: Modo Agente: [Execução de Tarefas](/docs/core-concepts/agent-mode/)


---