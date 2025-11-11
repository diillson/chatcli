+++
title = "A Plataforma Agentiva: O Futuro da Automação no Terminal"
linkTitle = "IA Agentiva e Plugins"
weight = 40
description = "Vá além do chat. Transforme o ChatCLI em um ecossistema de automação onde a IA utiliza ferramentas customizadas, criadas por você, para executar fluxos de trabalho complexos de ponta a ponta. Este é o guia definitivo."
icon = "smart_toy"
+++

## De Assistente a Agente: Uma Mudança de Paradigma

Até hoje, as ferramentas de linha de comando com IA têm funcionado como assistentes: você pergunta, elas respondem. Elas são um oráculo. O ChatCLI redefine essa relação, transformando o assistente em um **agente**: uma entidade autônoma que não apenas responde, mas **age**.

O sistema de Plugins e IA Agentiva é a materialização dessa visão. Ele transforma o ChatCLI de uma ferramenta para você em uma **plataforma para a IA**. Você fornece as ferramentas (plugins), define o objetivo, e o agente orquestra a execução, conectando percepção, raciocínio e ação para resolver problemas complexos no seu lugar.

Esta não é uma simples funcionalidade. É a fundação para um novo modo de interagir com seu ambiente de desenvolvimento.

---

## Váriavel: O Ciclo de Vida do Agente

- `CHATCLI_AGENT_PLUGIN_MAX_TURNS` - (número inteiro, padrão: `7`): Define o número máximo de iterações (turnos) que o agente pode executar para alcançar seu objetivo. Isso evita loops infinitos e controla o tempo de execução.
- `CHATCLI_AGENT_PLUGIN_TIMEOUT` - (número inteiro, padrão: `15`): Define o tempo limite de execução para o plugin do agente. Padrão: 15 (Minutos)

---
## O Coração do Agente: O Ciclo ReAct (Raciocínio e Ação)

Quando você ativa o `AgentMode` com um objetivo (`/agent ...`), o ChatCLI inicia um motor de raciocínio sofisticado inspirado no framework **ReAct (Reasoning and Acting)**. Em vez de uma única resposta monolítica, o agente entra em um loop transparente e iterativo:

1.  **Raciocínio (O Monólogo Interno da IA):** O agente analisa seu objetivo e o compara com seu "cinto de utilidades" — a lista de Ferramentas (Plugins) que ele conhece. Ele verbaliza seu plano em uma tag `<pensamento>`, que você pode ver em tempo real.
    > `<pensamento>`
    > O objetivo é analisar a performance de uma função Go. Isso requer profiling. Eu não posso fazer isso diretamente. Olhando minhas ferramentas, vejo `@go-bench-gen` e `@go-bench-run`. O primeiro passo lógico é gerar o arquivo de benchmark.
    > `</pensamento>`

2.  **Ação (A Chamada da Ferramenta):** A IA formaliza sua decisão em uma chamada estruturada.
    > `<tool_call name="@go-bench-gen" args="main.go MinhaFuncao" />`

3.  **Execução (O Corpo do Agente):** O ChatCLI intercepta essa chamada, invoca o plugin correspondente no seu sistema local e captura o resultado.
    > `🤖 Agente está usando a ferramenta: @go-bench-gen main.go MinhaFuncao`

4.  **Observação (O Feedback do Mundo Real):** O resultado da ferramenta, seja um sucesso, um erro ou dados, é formatado e enviado de volta para a IA.
    > `--- Resultado da Ferramenta ---`
    > `main_bench_test.go`

5.  **Reiteração:** O ciclo recomeça. A IA recebe o novo dado, raciocina sobre o próximo passo e seleciona a próxima ferramenta, encadeando ações até que o objetivo seja alcançado.

Este ciclo transforma a IA de uma caixa preta em um colaborador transparente, cujo processo de pensamento você pode acompanhar e auditar a cada passo.

---

## O Arsenal do Agente: Gerenciamento de Plugins com `/plugin`

Um agente é definido por suas ferramentas. O comando `/plugin` é o seu arsenal, a interface para gerenciar o conjunto de habilidades do seu agente.

| Comando | Descrição Detalhada |
| :--- | :--- |
| `/plugin list` | Exibe um inventário completo das ferramentas instaladas. Essencial para saber do que seu agente é capaz. |
| `/plugin install <url>` | **Instala uma nova habilidade.** O ChatCLI clona, compila e instala o plugin de um repositório Git. **A segurança é primordial:** você sempre será avisado e solicitado a confirmar antes de executar código de terceiros. |
| `/plugin show <nome>` | Apresenta o "manual de instruções" de uma ferramenta, detalhando sua descrição e sintaxe de uso (`Usage`). |
| `/plugin inspect <nome>` | O "raio-x" de um plugin. Mostra o caminho do executável, permissões e os metadados brutos em JSON, facilitando a depuração. |
| `/plugin uninstall <nome>`| Remove uma ferramenta do arsenal do agente, desabilitando-a imediatamente. |
| `/plugin reload` | Força uma nova verificação do diretório de plugins. Graças ao monitoramento de arquivos, isso raramente é necessário, mas serve como uma garantia. |

---

## Demonstração de Valor: O Agente Engenheiro de Performance

Para ilustrar o impacto real desta arquitetura, o ChatCLI inclui um conjunto de plugins de exemplo que o transformam em um **Engenheiro de Performance de Go autônomo**.

**O Desafio:** Identificar gargalos de CPU em uma função Go, um processo que exige conhecimento de `go test`, `benchmarking`, `pprof` e análise de perfis.

**A Delegação (Seu único trabalho):**

❯ /agent analise a performance da função 'MinhaFuncaoCPUIntensiva' no arquivo 'main.go' e identifique os gargalos.


**A Orquestração Autônoma (O que o Agente faz por você):**

1.  **Turno 1: Geração de Código.** O Agente raciocina que precisa de um benchmark. Ele invoca **`@go-bench-gen`**, que analisa a AST do seu `main.go` e **gera um novo arquivo `main_bench_test.go`** no seu projeto, com todo o código de benchmark necessário.
2.  **Turno 2: Coleta de Dados.** Com o benchmark pronto, o Agente invoca **`@go-bench-run`**. Este plugin executa `go test` com flags de profiling (`-cpuprofile`), gera um arquivo `cpu.prof`, e então usa `go tool pprof` para converter os dados binários em um **relatório de texto compreensível**, que é retornado para a IA.
3.  **Turno 3: Análise Cognitiva.** Aqui está o salto de valor. O Agente não apenas exibe o relatório. Ele o **interpreta**. Ele entende o significado das colunas `flat` (tempo próprio) e `cum` (tempo cumulativo), identifica a função que é o verdadeiro gargalo e formula uma conclusão técnica.
4.  **Resultado Final:** O Agente apresenta uma resposta em linguagem natural, acionável e precisa, apontando o gargalo e recomendando a otimização.

**O Valor Entregue:** Um fluxo de trabalho de engenharia de múltiplos passos, que exige expertise e tempo, foi **completamente automatizado** e executado em segundos. Isso não é um atalho; é uma multiplicação de produtividade.

---

## Crie Suas Próprias Ferramentas: O Guia Definitivo do Desenvolvedor

O ecossistema de plugins é o que torna o ChatCLI ilimitado. Você pode ensinar ao seu agente novas habilidades para interagir com suas APIs privadas, seu banco de dados, sua plataforma de nuvem ou qualquer outra ferramenta.

#### O Contrato do Plugin: A "API" do Agente

Qualquer programa executável pode se tornar um plugin do ChatCLI, independentemente da linguagem, desde que siga este contrato sagrado:

1.  **Ser um Executável:** Deve ser um binário compilado ou um script com `shebang` (`#!/bin/bash`, `#!/usr/bin/env python3`, etc.), localizado em `~/.chatcli/plugins/` e com permissão de execução.
2.  **Descoberta via `--metadata`:** Ao ser invocado com a flag `--metadata`, o programa **DEVE** imprimir para `stdout` um único objeto JSON com os campos:
    *   `name` (string): O comando de invocação, **obrigatoriamente** começando com `@`.
    *   `description` (string): Descrição clara. A IA usará isso para decidir quando usar sua ferramenta.
    *   `usage` (string): Sintaxe de uso (ex: `@meu-plugin <arg1> [--flag]`).
    *   `version` (string): Versão semântica (ex: "1.0.2").
3.  **Comunicação via I/O Padrão:**
    *   **Argumentos:** Recebidos como argumentos de linha de comando (`os.Args[1:]`).
    *   **Entrada de Dados (stdin):** Se a IA precisar passar um bloco grande de texto (como um código gerado), ela o fará via `stdin`. Seu plugin deve estar preparado para ler do `stdin` se for o caso.
    *   **Resultado (stdout):** O resultado principal da sua ferramenta, que será enviado de volta para a IA, **DEVE** ser impresso em `stdout`.
    *   **Erros e Logs (stderr):** Todas as mensagens de erro, logs de depuração ou feedback de progresso **DEVEM** ser impressos em `stderr`. Isso é crucial para o agente entender quando uma ferramenta falha e por quê.

#### Linguagens Suportadas

**Qualquer linguagem que possa criar um executável e interagir com I/O padrão.**

*   **Go / Rust:** Escolhas ideais. Produzem binários estáticos, rápidos e portáteis.
*   **Python / Bash / Node.js:** Perfeitos para prototipagem rápida e scripts de automação. Apenas certifique-se de incluir o `shebang` correto no topo do arquivo (ex: `#!/usr/bin/env python3`).
*   **C++, Swift, etc.:** Totalmente compatíveis.

#### Exemplo de Ponta a Ponta: Plugin `@dockerhub-tags` em Go

Este plugin de exemplo demonstra uma interação real com uma API web.

```go
// chatcli-plugin-dockerhub/main.go
package main

import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "strings"
    "time"
)

type Metadata struct { /* ... */ }
type DockerHubResponse struct { /* ... */ }

func main() {
    if len(os.Args) > 1 && os.Args[1] == "--metadata" {
            // ... Lógica de Metadados ...
            return
    }

    if len(os.Args) < 2 {
            fmt.Fprintln(os.Stderr, "Erro: Nome da imagem é obrigatório.")
            os.Exit(1)
    }
    imageName := os.Args[1]
    // ... Lógica de chamada à API do Docker Hub ...

    // Extrai os nomes das tags
    var tags []string
    for _, result := range apiResponse.Results {
            tags = append(tags, result.Name)
    }

    // Imprime a lista de tags para stdout, para a IA processar.
    fmt.Println(strings.Join(tags, "\n"))
}
```

Este plugin permite que a IA, ao receber a tarefa  /agent implante a última versão alpine do redis , use a melhor tag disponível, valide se está em execução e retorne o resultado.

O sistema de plugins é a sua porta de entrada para a verdadeira automação. Comece a construir suas ferramentas e transforme seu terminal em um colega de equipe.