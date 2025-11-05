+++
title = "Leve seu Terminal ao Próximo Nível com IA"

# Parâmetros personalizados que o nosso layout 'index.html' irá usar.
[params]
  # Caminho para a imagem principal (veja o Passo 3)
  hero_image = "/images/chatcli-demo.gif"

  # Botão de Ação Principal (Call to Action)
  [params.cta_button]
    text = "Começar a Usar"
    url = "/docs/introduction/"

  # Botão Secundário
  [params.secondary_button]
    text = "Ver no GitHub"
    url = "https://github.com/diillson/chatcli"

  # Títulos para a seção de funcionalidades
  features_title = "Por que o ChatCLI é Diferente?"
  features_subtitle = "Muito mais que um simples chat. O ChatCLI entende seu ambiente e executa tarefas para você."

  # Lista de funcionalidades para os cartões
  [[params.features]]
    icon = "smart_toy"
    title = "Modo Agente Inteligente"
    description = "Delegue tarefas complexas. A IA planeja e executa sequências de comandos com sua aprovação para resolver problemas."

  [[params.features]]
    icon = "hub"
    title = "Consciência de Contexto"
    description = "Use @file, @git e @command para que a IA entenda seu código, repositório e ambiente de trabalho."

  [[params.features]]
    icon = "dns"
    title = "Suporte Multi-Provedor"
    description = "Alterne facilmente entre OpenAI, Claude, StackspotAI(Plataforma de Agents), Gemini, e modelos locais via Ollama para usar a melhor IA para cada tarefa."

  [[params.features]]
    icon = "save"
    title = "Contextos Persistentes"
    description = "Salve 'snapshots' de seus projetos com /context para reutilizá-los em diferentes sessões sem reenvio de arquivos."

  [[params.features]]
    icon = "terminal"
    title = "Execução Segura de Comandos"
    description = "Validador de segurança integrado que bloqueia comandos perigosos e exige confirmação para ações delicadas."

  [[params.features]]
    icon = "build"
    title = "Integração com Scripts"
    description = "Use o modo one-shot (-p) para integrar o poder da IA em seus pipelines de CI/CD e automações de shell."
+++

A interface de linha de comando que transforma seu terminal em um assistente de desenvolvimento inteligente. Pare de copiar e colar, comece a delegar.

--------