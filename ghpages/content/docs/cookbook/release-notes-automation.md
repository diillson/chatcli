+++
title = "Receita: Gerando Release Notes com @git"
linkTitle = "Gerar Release Notes"
weight = 10
description = "Aprenda a usar o comando @git para gerar automaticamente um changelog claro e conciso para suas novas versões."
+++

Manter um `CHANGELOG.md` atualizado é crucial, mas pode ser uma tarefa tediosa. Esta receita mostrará como usar o comando `@git` do ChatCLI para analisar suas mudanças recentes e gerar uma proposta de release notes em segundos.

### O Problema

Após finalizar uma série de commits para uma nova versão, você precisa resumir o que foi feito. O processo manual geralmente envolve:
1.  Executar `git log --oneline` para ver os commits.
2.  Analisar cada mensagem de commit.
3.  Agrupar as mudanças por tipo (Novas Features, Correções de Bugs, etc.).
4.  Escrever um resumo em Markdown.

Vamos automatizar isso.

---

### Ingredientes

Para esta receita, você precisará de:
-   Estar na pasta raiz de um repositório Git.
-   Ter algumas mudanças commitadas desde a última tag/versão.

---

### Passo a Passo

#### Passo 1: Inicie o ChatCLI e Use o Comando `@git`

O comando `@git` coleta automaticamente informações cruciais do seu repositório: a branch atual, o status dos arquivos, as diferenças (`diffs`) e, o mais importante, os commits recentes.

Vamos combinar isso com um prompt claro, instruindo a IA sobre o formato que desejamos.

{{< command >}}
@git > Analise os commits e as mudanças no código e gere uma proposta de release notes para a versão v1.2.0. Agrupe as mudanças em seções como "✨ Novas Funcionalidades", "🐛 Correções de Bugs" e "🔨 Melhorias Internas". Use o formato Markdown.
{{< /command >}}

**Desconstruindo o Prompt:**
-   **`@git >`**: O comando injeta todo o contexto do Git. O operador `>` separa o contexto da nossa instrução.
-   **"Analise os commits e as mudanças..."**: Pedimos à IA para não apenas ler as mensagens de commit, mas também correlacioná-las com os `diffs` de código fornecidos.
-   **"gere uma proposta de release notes..."**: Deixamos claro qual é o objetivo final.
-   **"Agrupe as mudanças em seções..."**: Damos à IA uma estrutura clara a seguir, garantindo um resultado consistente.

---

#### Passo 2: Analise e Refine a Resposta da IA

O ChatCLI enviará o contexto para a IA, que processará as informações e retornará uma resposta formatada.

**Exemplo de Resposta da IA:**
```markdown
Claro! Com base nos commits e nas mudanças de código, aqui está uma proposta de release notes para a **v1.2.0**.

---

### Release Notes - v1.2.0

Esta versão introduz o poderoso sistema de gerenciamento de contextos persistentes e melhora a performance do Modo Agente.

#### ✨ Novas Funcionalidades

-   **Gerenciamento de Contexto Persistente (`/context`)**: Agora é possível criar, salvar, anexar e gerenciar "snapshots" de projetos para reutilização em diferentes sessões. Isso inclui os subcomandos `create`, `attach`, `list` e `delete`.
-   **Modo de Arquivo "Smart" (`@file --mode=smart`)**: Um novo modo que permite à IA selecionar os arquivos mais relevantes para a pergunta do usuário, ideal para grandes bases de código.

#### 🐛 Correções de Bugs

-   Corrigido um bug no Modo Agente onde a tecla `p` para alternar a visualização não funcionava corretamente no Windows.
-   Resolvido um problema de concorrência que poderia causar pânico ao cancelar uma requisição (`Ctrl+C`) durante o processamento de arquivos.

#### 🔨 Melhorias Internas

-   Otimizado o processamento de arquivos no comando `@file`, resultando em uma leitura até 30% mais rápida em diretórios com muitos arquivos pequenos.
-   Refatorado o `LLMManager` para permitir a configuração de políticas de retry (tentativas e backoff) de forma mais flexível.

---
```
--------

#### Passo 3: Use o Resultado

Agora você tem um rascunho de alta qualidade para suas release notes. Basta copiar o texto, colar no seu  CHANGELOG.md  ou na descrição da sua release no GitHub, fazer os ajustes finos que julgar necessários, e pronto!

{{< notice "success" >}}
Parabéns! Você transformou uma tarefa de 15 minutos em um único comando de 15 segundos.
{{< /notice >}}

--------

## Próximos Passos

Explore outras receitas para otimizar ainda mais seu fluxo de trabalho:

- Depurando Logs com Pipes e  `@command`
- Entendendo um Novo Projeto com  `@file --mode=summary` 