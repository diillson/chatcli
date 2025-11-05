+++
title = "Receita: Gerenciando Múltiplos Projetos com Contextos e Sessões"
linkTitle = "Workflow Multi-Projeto"
weight = 50
description = "Aprenda a combinar /context e /session para criar um fluxo de trabalho eficiente ao alternar entre diferentes projetos."
+++

Alternar entre projetos pode quebrar seu fluxo de trabalho, especialmente quando cada um tem sua própria base de código e histórico de problemas. Esta receita mostra como usar **Contextos Persistentes** e **Gerenciamento de Sessões** para alternar entre projetos de forma instantânea e organizada.

### O Problema

Você trabalha em dois projetos: `Projeto A` (uma API em Go) e `Projeto B` (um frontend em React). Em um momento, você está depurando um bug na API; no outro, precisa implementar uma nova feature no frontend. Manter o histórico da conversa e o contexto dos arquivos separados é um desafio.

### Ingredientes

-   Dois ou mais diretórios de projeto em sua máquina.

---

### O Fluxo de Trabalho

A estratégia é criar um **Contexto** para a base de código de cada projeto e uma **Sessão** para o histórico de conversa de cada tarefa.

#### Passo 1: Crie Contextos Persistentes para Cada Projeto

Primeiro, vamos criar "snapshots" das bases de código. Faremos isso uma única vez para cada projeto.

Navegue até a pasta do `Projeto A`:
{{< command >}}
/context create projeto-a-api ./cmd ./internal --mode smart --description "API principal em Go do Projeto A" --tags go,api
{{< /command >}}

Agora, navegue até a pasta do `Projeto B`:
{{< command >}}
/context create projeto-b-frontend ./src --mode chunked --description "Frontend em React do Projeto B" --tags react,frontend
{{< /command >}}

{{< notice "info" >}}
**O que fizemos?** Criamos dois contextos persistentes. Agora, o ChatCLI "conhece" a estrutura desses dois projetos e podemos nos referir a eles por nome (`projeto-a-api`, `projeto-b-frontend`) a qualquer momento, de qualquer lugar do seu sistema.
{{< /notice >}}

---

#### Passo 2: Inicie o Trabalho no Projeto A

Vamos começar depurando o bug na API.

1.  **Carregue (ou crie) uma sessão de conversa:**

    {{< command >}}
/session load debug-api-projeto-a
    {{< /command >}}

    *(Se a sessão não existir, ela será criada quando você salvar.)*

2.  **Anexe o contexto do projeto:**

    {{< command >}}
/context attach projeto-a-api
    {{< /command >}}

Agora, seu prompt indicará que a sessão e o contexto estão ativos. Você pode começar a fazer perguntas sobre o código da API, e o ChatCLI enviará automaticamente os arquivos relevantes para a IA.

`debug-api-projeto-a ❯ Onde está a lógica de validação de JWT?`

---

#### Passo 3: Alterne para o Projeto B

Seu leader pede para você pausar o debug e trabalhar em uma nova feature no frontend.

1.  **Salve a sessão atual:** Todo o seu histórico de debug será guardado.
    {{< command >}}
/session save debug-api-projeto-a
    {{< /command >}}

2.  **Desanexe o contexto antigo:**
    {{< command >}}
/context detach projeto-a-api
    {{< /command >}}

3.  **Carregue a sessão e o contexto do Projeto B:**
    {{< command >}}
/session load feature-frontend-projeto-b
/context attach projeto-b-frontend
    {{< /command >}}

Pronto! Em segundos, você mudou completamente o ambiente. Seu histórico de conversa agora é sobre a feature do frontend, e a IA tem todo o código do `Projeto B` como contexto.

`feature-frontend-projeto-b ❯ Crie um novo componente React chamado 'UserProfile' que exiba nome e email.`

---

#### Passo 4: Retornando ao Projeto A

Tarefa no frontend concluída? Voltar para o debug da API é igualmente simples.

{{< command >}}
/session save feature-frontend-projeto-b
/context detach projeto-b-frontend
/session load debug-api-projeto-a
/context attach projeto-a-api
{{< /command >}}

{{< notice "success" >}}
**Resultado:** Você criou um fluxo de trabalho onde pode alternar entre projetos complexos em segundos, preservando tanto o histórico da sua conversa quanto o contexto completo da base de código, sem nunca precisar reenviar um único arquivo manualmente.
{{< /notice >}}
