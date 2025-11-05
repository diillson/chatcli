+++
title = "Introdução ao ChatCLI"
linkTitle = "Introdução"
weight = 10
description = "Descubra o que é o ChatCLI e como ele pode revolucionar sua interação com o terminal."
icon = "star"
+++

## O que é o ChatCLI?

**ChatCLI** é uma interface de linha de comando (CLI) poderosa e extensível projetada para unir o poder dos grandes modelos de linguagem (LLMs) diretamente ao seu ambiente de desenvolvimento. Ele transforma seu terminal em um assistente inteligente, capaz de entender o contexto do seu trabalho, interagir com arquivos locais, executar comandos, analisar logs e até mesmo automatizar tarefas complexas através de um modo "agente".

Desenvolvido em Go, o ChatCLI é rápido, portátil e leve, criado para ser a ferramenta definitiva para desenvolvedores, sysadmins e entusiastas de tecnologia que desejam maximizar sua produtividade.

---

## Principais Funcionalidades

O ChatCLI foi construído com um conjunto robusto de funcionalidades, analisando a estrutura do próprio projeto:

*   **🧠 Modo Agente Inteligente (`/agent`)**: Delegue tarefas complexas. O ChatCLI pode planejar e executar sequências de comandos para atingir um objetivo, como "verificar os logs de erro do serviço X e reiniciar se necessário".
*   **📚 Consciência de Contexto Total**: O ChatCLI não é apenas um chat. Ele entende seu ambiente:
    *   `@file`: Envie o conteúdo de arquivos ou diretórios inteiros para a IA.
    *   `@git`: Adicione automaticamente o status, a branch e os diffs do seu repositório Git ao prompt.
    *   `@env`: Inclua variáveis de ambiente de forma segura (valores sensíveis são redigidos).
*   **🔌 Suporte Multi-Provedor**: Configure e alterne facilmente entre os principais provedores de LLM, incluindo **OpenAI (GPT-4o, etc.)**, **Anthropic (Claude 3.5)**, **Google (Gemini)**, **xAI (Grok)** e até mesmo modelos locais via **Ollama**.
*   **💾 Gerenciamento Persistente de Contexto (`/context`)**: Crie, salve e anexe "contextos" reutilizáveis. Ideal para trabalhar em múltiplos projetos sem precisar reenviar os mesmos arquivos repetidamente.
*   **🗣️ Suporte a Múltiplos Idiomas**: A interface é internacionalizada, com suporte nativo para Português (pt-BR) e Inglês (en-US).
*   **🛡️ Segurança Integrada**: Comandos perigosos (`rm -rf`, `sudo`, etc.) são bloqueados por padrão no modo agente, e valores sensíveis em variáveis de ambiente ou logs são mascarados.
*   **⚙️ Configuração Flexível**: Gerencie toda a configuração através de um simples arquivo `.env`, com a capacidade de recarregar em tempo real com o comando `/reload`.
*   **⚡ Modo One-Shot**: Integre o ChatCLI em seus scripts e pipelines usando flags (`-p`, `--prompt`) para execuções não interativas.

---

## Para Quem é o ChatCLI?

*   **Desenvolvedores**: Para depurar código, entender bases de código desconhecidas, gerar documentação e automatizar tarefas de build.
*   **Sysadmins e DevOps**: Para analisar logs, gerenciar configurações, automatizar deployments e solucionar problemas em servidores.
*   **Entusiastas de Linha de Comando**: Para turbinar seu terminal e explorar novas formas de interagir com o sistema operacional.

---

## Próximos Passos

Agora que você sabe o que o ChatCLI pode fazer, vamos começar!

➡️ **Próximo:** [**Guia de Instalação**](/docs/getting-started/installation/)

--------