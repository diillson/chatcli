+++
title = "Introdução ao ChatCLI"
linkTitle = "Introdução"
weight = 10
description = "Descubra o que é o ChatCLI e como ele pode revolucionar sua interação com o terminal."
icon = "star"
+++

## O que é o ChatCLI?

**ChatCLI** é uma interface de linha de comando (CLI) que integra o poder dos grandes modelos de linguagem (LLMs) diretamente ao seu terminal. Ele transforma seu shell de um simples executor de comandos em um **assistente de desenvolvimento inteligente**, capaz de entender o contexto do seu trabalho, interagir com arquivos, executar comandos e até mesmo automatizar tarefas complexas.

Desenvolvido em Go, o ChatCLI é rápido e portátil, projetado para ser a ferramenta definitiva para quem vive no terminal e busca maximizar a produtividade.

---

## Problemas que o ChatCLI Resolve

Você se identifica com alguma destas situações?

{{< notice "info" >}}
- **Copiar e Colar Interminável:** `cat arquivo.js`, selecionar, `Ctrl+C`, ir para o navegador, `Ctrl+V`, e então digitar a pergunta.
- **Mensagens de Commit Genéricas:** Lutar para escrever uma boa mensagem de commit após horas de codificação.
- **Análise de Logs Intimidante:** Tentar encontrar uma agulha no palheiro em um arquivo de log com milhares de linhas.
- **Curva de Aprendizagem:** Entrar em um novo projeto e se sentir perdido na base de código.
{{< /notice >}}

O ChatCLI foi criado para eliminar esses atritos, trazendo a inteligência da IA para o seu fluxo de trabalho, onde você já está.

---

## Para Quem é o ChatCLI?

-   **Desenvolvedores de Software:** Para depurar código, entender bases de código desconhecidas, gerar testes unitários, refatorar funções e criar documentação a partir do código-fonte.
-   **Engenheiros de DevOps e SREs:** Para analisar logs de `kubectl`, gerenciar configurações com `awk` e `sed`, automatizar deployments e solucionar problemas em tempo real em servidores.
-   **Entusiastas de Linha de Comando:** Para turbinar o terminal, criar aliases poderosos e explorar novas formas de interagir com o sistema operacional.
-   **DB'as e Engenheiros de Dados:** Para automatizar tarefas repetitivas e gerenciar bases de dados.

---

## Principais Funcionalidades em Ação

O ChatCLI foi construído com um conjunto robusto de funcionalidades:

*   **🧠 Modo Agente Inteligente (`/agent`)**: Delegue tarefas. O ChatCLI planeja e executa sequências de comandos para atingir um objetivo, como *"verificar os logs de erro do serviço X e reiniciá-lo se estiver travado"*.
*   **📚 Consciência de Contexto Total**: Dê "olhos" à IA no seu ambiente:
    *   `@file`: Envie o conteúdo de arquivos ou a estrutura de diretórios inteiros.
    *   `@git`: Adicione automaticamente o status, a branch e os `diffs` do seu repositório.
    *   `@command`: Execute um comando e use sua saída como contexto.
    * `/session`: Gerenciamento de sessão, salve seu trabalho e sua iteração.
*   **🔌 Suporte Multi-Provedor**: Alterne facilmente entre **OpenAI**, **Claude**, **Stackspot(Agents)**, **Gemini**, **Grok (xAI)** e modelos locais via **Ollama**.
*   **💾 Contextos Persistentes (`/context`)**: Crie "snapshots" de projetos para reutilizá-los em diferentes conversas sem precisar reenviar os mesmos arquivos.
*   **🛡️ Segurança Integrada**: Comandos perigosos (`rm -rf`, `sudo`) são bloqueados por padrão no modo agente, e valores sensíveis são mascarados.
*   **⚡ Modo One-Shot**: Integre o ChatCLI em seus scripts e pipelines de CI/CD usando a flag `-p`.

---

## Próximos Passos

Agora que você sabe o que o ChatCLI pode fazer, vamos começar!

➡️ **Próximo:** [**Guia de Instalação**](/docs/getting-started/installation/)