+++
title = 'Adicionando Contexto (@ Comandos)'
linkTitle = 'Comandos de Contexto'
weight = 20
description = 'Aprenda a usar @file, @git, @command e @env para dar ao ChatCLI consciência total do seu ambiente de trabalho.'
icon = "hub"
+++

A verdadeira força do **ChatCLI** reside em sua capacidade de entender o contexto em que você está trabalhando. Os comandos de contexto, que começam com `@`, são a ponte entre seu ambiente local e a inteligência da IA.

Eles coletam informações do seu sistema e as anexam ao seu prompt, permitindo que a IA forneça respostas precisas e relevantes.

---

## `@file`: O Coração do Contexto de Código

Este é o comando mais essencial para desenvolvedores. Ele permite que você envie o conteúdo de arquivos ou a estrutura de diretórios inteiros para análise.

{{< command >}}
@file ./src/database/connection.go me explique como a conexão é feita.
{{< /command >}}

Para lidar com diferentes tamanhos de projeto e tipos de pergunta, o `@file` possui quatro modos de processamento poderosos:

| Modo | Cenário Ideal | O que Acontece | Exemplo de Uso |
| :--- | :--- | :--- | :--- |
| `full` | Análise profunda de um **arquivo ou componente pequeno**. | Envia o conteúdo completo de todos os arquivos, respeitando um limite de tamanho para evitar sobrecarga. | `@file db.go explique esta query` |
| `summary`| Entender a **arquitetura de um projeto desconhecido**. | Envia uma árvore de arquivos e metadados (tamanhos, tipos), **sem** o código. Perfeito para uma visão geral. | `@file . me dê um overview da arquitetura` |
| `chunked`| Análise completa de uma **base de código grande**. | Divide o projeto em "pedaços" (chunks) gerenciáveis. Apenas o primeiro é enviado. Use `/nextchunk` para navegar. | `@file . vamos analisar este projeto legado em partes` |
| `smart` | Fazer uma **pergunta específica em um projeto grande**. | A IA recebe a lista de todos os arquivos e, com base na sua pergunta, escolhe quais são os mais relevantes para ler. | `@file ./src como funciona o fluxo de autenticação?` |

{{< notice "info" >}}
**Dica:** Você pode definir o modo usando a flag `--mode`. Exemplo: `@file --mode=smart ./src`.
{{< /notice >}}

---

## `@command`: A Ponte com Seu Terminal

Execute qualquer comando do shell e use sua saída como contexto para sua pergunta. Isso elimina a necessidade de copiar e colar a saída de logs, diagnósticos ou listagens.

**Fluxo de Trabalho: Antes vs. Depois**

| Sem ChatCLI (o jeito antigo) | Com ChatCLI (o jeito inteligente) |
| :--- | :--- |
| 1. `kubectl get pods -n prod > pods.txt` | 1. `@command kubectl get pods -n prod > por que o pod de login está em CrashLoopBackOff?` |
| 2. Abrir `pods.txt` | |
| 3. Copiar o conteúdo | |
| 4. Colar no navegador | |
| 5. Digitar a pergunta | |

#### Execução Interativa e Análise Direta

-   `@command -i <comando>`: Use a flag `-i` para comandos que exigem interação, como `vim` ou `ssh`. A saída não será capturada.
-   `@command --ai <comando>`: Um atalho poderoso que envia a saída do comando diretamente para a IA para análise, sem precisar de uma pergunta adicional.

{{< command >}}
@command --ai cat /var/log/nginx/error.log
{{< /command >}}

---

## `@git` e `@env`: Contexto Adicional

-   **`@git`**: Se você está em um repositório Git, este comando anexa o status (`git status -s`), branch, `diffs` de arquivos modificados e os 5 commits mais recentes. É perfeito para gerar mensagens de commit ou resumir alterações.
    {{< command >}}
@git me ajude a escrever uma mensagem de commit clara para estas mudanças.
    {{< /command >}}

-   **`@env`**: Adiciona as variáveis de ambiente atuais ao contexto.
    {{< notice "warning" >}}
**Segurança:** O ChatCLI automaticamente detecta e remove valores de variáveis com nomes sensíveis (como `API_KEY`, `TOKEN`, `PASSWORD`), substituindo-os por `[REDACTED]`.
    {{< /notice >}}

---

## Combinando Comandos de Contexto

A verdadeira magia acontece quando você combina múltiplos comandos de contexto em um único prompt para dar à IA uma visão 360º do seu problema.

{{< command >}}
@git @file ./src/main.go > baseado nas mudanças recentes, revise este arquivo e sugira melhorias de performance.
{{< /command >}}

---

## Próximos Passos

Agora que você sabe como dar "olhos e ouvidos" ao ChatCLI, o próximo passo é aprender a dar a ele "mãos" para agir no seu sistema.

➡️ **Próximo:** [Modo Agente: Execução de Tarefas](/chatcli/docs/core-concepts/agent-mode/)