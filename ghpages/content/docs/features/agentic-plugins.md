+++
title = "IA Agentiva e Sistema de Plugins"
linkTitle = "IA Agentiva e Plugins"
weight = 40
description = "Transforme o ChatCLI em uma plataforma de automação extensível. Crie ferramentas customizadas que a IA pode usar autônomamente para executar fluxos de trabalho complexos de ponta a ponta."
icon = "smart_toy"
+++

## De Assistente a Agente: Uma Mudança de Paradigma

A maioria das ferramentas de IA para linha de comando funciona como **assistentes**: você pergunta, elas respondem. O ChatCLI vai além, transformando a IA em um **agente autônomo** que não apenas responde, mas **age**.

O sistema de Plugins e IA Agentiva materializa essa visão:

- **Você**: Define o objetivo e fornece as ferramentas (plugins)
- **O Agente**: Orquestra a execução, conectando percepção, raciocínio e ação para resolver problemas complexos

Esta não é apenas uma funcionalidade — é a fundação para um novo modo de interagir com seu ambiente de desenvolvimento.

---

## Arquitetura do Sistema de Plugins

### Descoberta e Carregamento Automático

O ChatCLI utiliza um **gerenciador de plugins inteligente** que:

1. **Monitora o diretório** `~/.chatcli/plugins/` usando `fsnotify`
2. **Detecta mudanças** em tempo real (criação, modificação, remoção de arquivos)
3. **Aplica debounce** de 500ms para evitar recarregamentos múltiplos
4. **Valida o contrato** de cada plugin antes de carregá-lo
5. **Recarrega automaticamente** sem necessidade de reiniciar o ChatCLI

```go
// Eventos que acionam hot reload:
// - Write:  Modificação de arquivo existente
// - Create: Novo plugin adicionado
// - Remove: Plugin deletado
// - Rename: Plugin renomeado
```
### Busca Flexível de Plugins

O sistema aceita ambas as formas de invocação:

- Com @ (forma canônica)
  - /agent @hello mundo

- Sem @ (atalho conveniente)
  - /agent hello mundo

Internamente, o gerenciador normaliza automaticamente:
```go
func (m *Manager) GetPlugin(name string) (Plugin, bool) {
    p, ok := m.plugins[name]
    if !ok {
        p, ok = m.plugins["@"+name]  // Fallback automático
    }
    return p, ok
}
```
--------

## Configuração do Agente

### Variáveis de Ambiente

Configure o comportamento do agente através de variáveis de ambiente:

# Controle de Execução
`CHATCLI_AGENT_PLUGIN_MAX_TURNS=50`     # Máximo de iterações (padrão: 50, máximo: 200)

`CHATCLI_AGENT_PLUGIN_TIMEOUT=20m`       # Timeout por plugin (padrão: 15m)

# Segurança
`CHATCLI_AGENT_CMD_TIMEOUT=5m`           # Timeout para comandos shell

`CHATCLI_AGENT_DENYLIST="rm.*-rf.*;dd.*"` # Padrões regex bloqueados

`CHATCLI_AGENT_ALLOW_SUDO=false`         # Bloqueia sudo por padrão

```bash
Variável                        │ Tipo     │ Padrão  │ Descrição                                                                         
────────────────────────────────┼──────────┼─────────┼───────────────────────────────────────────────────────────────────────────────────
CHATCLI_AGENT_PLUGIN_MAX_TURNS  │ inteiro  │  50     │ Número máximo de iterações do ciclo ReAct. Evita loops infinitos (máx: 200).
CHATCLI_AGENT_PLUGIN_TIMEOUT    │ duração  │  15m    │ Tempo limite para execução de cada plugin. Aceita formato Go ( 30s ,  5m ,  1h ).
CHATCLI_AGENT_CMD_TIMEOUT       │ duração  │  10m    │ Timeout para comandos shell executados via  @command  (máx: 1h).                            
CHATCLI_AGENT_DENYLIST          │ string   │ -       │ Expressões regulares separadas por  ;  para bloquear comandos perigosos.          
CHATCLI_AGENT_ALLOW_SUDO        │ booleano │  false  │ Permite comandos  sudo  sem bloqueio automático (use com cautela).
```
--------

## O Ciclo ReAct: Raciocínio e Ação

O AgentMode implementa o framework ReAct (Reasoning and Acting), um loop iterativo transparente:

### 1. Raciocínio (Pensamento)

O agente analisa o objetivo e verbaliza seu plano:
```bash
<pensamento>
O objetivo é analisar a performance de uma função Go. 
Isso requer profiling. Olhando minhas ferramentas, vejo 
@go-bench-gen e @go-bench-run. O primeiro passo lógico 
é gerar o arquivo de benchmark.
</pensamento>
```
### 2. Ação (Chamada de Ferramenta)

A IA formaliza sua decisão em uma chamada estruturada:
```bash
<tool_call name="@go-bench-gen" args="main.go MinhaFuncao" />
```
### 3. Execução (Invocação do Plugin)

O ChatCLI intercepta e executa o plugin:
```go
🤖 Agente está usando a ferramenta: @go-bench-gen main.go MinhaFuncao
   ⏳ Timeout configurado: 15m
   📂 Diretório: /home/user/projeto
```
### 4. Observação (Feedback)

O resultado é formatado e retornado para a IA:
```go
--- Resultado da Ferramenta ---
✅ Arquivo gerado: main_bench_test.go
📊 Benchmark criado: BenchmarkMinhaFuncao
```
### 5. Reiteração

O ciclo recomeça até que o objetivo seja alcançado ou o limite de turnos seja atingido.

--------

## Gerenciamento de Plugins com  /plugin

### Comandos Disponíveis
```bash
Comando                   │ Descrição                                          
──────────────────────────┼────────────────────────────────────────────────────
/plugin list              │ Lista todos os plugins instalados com metadados    
/plugin install <url>     │ Instala plugin de um repositório Git (linguagens copilada)              
/plugin show <nome>       │ Exibe descrição e sintaxe de uso                   
/plugin inspect <nome>    │ Mostra metadados brutos, caminho e permissões      
/plugin uninstall <nome>  │ Remove plugin do sistema                           
/plugin reload            │ Força recarregamento manual (raramente necessário)
```
### Exemplo de Uso

# Listar plugins disponíveis
```go
❯ /plugin list
📦 Plugins Instalados (3):
  • @go-bench-gen  - Gera arquivos de benchmark Go
  • @go-bench-run  - Executa benchmarks e profiling
  • @dockerhub     - Consulta tags do Docker Hub
```
# Ver detalhes de um plugin
```go
❯ /plugin show @go-bench-gen
📋 Plugin: @go-bench-gen
📝 Descrição: Gera arquivos de benchmark Go a partir de funções existentes
💡 Uso: @go-bench-gen <arquivo.go> <NomeDaFuncao>
🏷️  Versão: 1.2.0
```
# Inspecionar metadados técnicos
```go
❯ /plugin inspect @go-bench-gen
🔍 Inspeção Detalhada:
   Caminho: /home/user/.chatcli/plugins/go-bench-gen
   Permissões: -rwxr-xr-x
   Tamanho: 2.3 MB
   
   Metadados (JSON):
   {
     "name": "@go-bench-gen",
     "description": "Gera arquivos de benchmark Go",
     "usage": "@go-bench-gen <arquivo.go> <NomeDaFuncao>",
     "version": "1.2.0"
   }
```
### Instalação de Plugins

# Instalar de um repositório Git
> ❯ /plugin install https://github.com/usuário/chatcli-plugin-k8s.git
```go
⚠️  AVISO DE SEGURANÇA
Você está prestes a instalar código de terceiros que será executado 
em sua máquina. Revise o código-fonte antes de prosseguir.

Repositório: https://github.com/usuário/chatcli-plugin-k8s.git
Confirmar instalação? (s/N): s

📥 Clonando repositório...
🔨 Detectado projeto Go, compilando...
✅ Plugin @k8s instalado com sucesso!
```
--------

## Criando Plugins: O Guia Completo

### O Contrato do Plugin

Todo plugin deve seguir estas regras:

#### 1. Ser um Executável

- Binário compilado (Go, Rust, C++) ou
- Script com shebang ( #!/usr/bin/env python3 ,  #!/bin/bash )
- Localizado em  ~/.chatcli/plugins/
- Permissão de execução obrigatória ( chmod +x )

# Verificar permissões
```bash
ls -l ~/.chatcli/plugins/
-rwxr-xr-x  1 user  staff  2.3M  meu-plugin  # ✅ Correto (x = executável)
-rw-r--r--  1 user  staff  1.8M  outro       # ❌ Sem permissão de execução
```
#### 2. Responder ao Contrato  --metadata  (Obrigatório)

Quando invocado com  --metadata , o plugin DEVE imprimir um JSON válido para  stdout :
```json
{
  "name": "@meu-plugin",
  "description": "Descrição clara do que o plugin faz",
  "usage": "@meu-plugin <arg1> [--flag]",
  "version": "1.0.0"
}
```
Todos os campos são obrigatórios:

-  name : Deve começar com  @
-  description : Usado pela IA para decidir quando usar a ferramenta
-  usage : Sintaxe de invocação
-  version : Versionamento semântico

#### 3. Implementar  --schema  (Opcional, mas Recomendado)

O schema ajuda a IA a entender os parâmetros do plugin:
```json
{
  "parameters": [
    {
      "name": "cluster-name",
      "type": "string",
      "required": true,
      "description": "Nome do cluster Kubernetes"
    },
    {
      "name": "namespace",
      "type": "string",
      "required": false,
      "default": "default",
      "description": "Namespace alvo"
    }
  ]
}
```
#### 4. Comunicação via I/O Padrão
```bash
Canal  │ Uso              │ Descrição                                   
───────┼──────────────────┼─────────────────────────────────────────────
stdout │ Resultado        │ Saída principal enviada para a IA           
stderr │ Logs/Progresso   │ Mensagens de status, avisos e erros         
stdin  │ Entrada de dados │ Blocos grandes de texto (ex: código gerado)
args   │ Parâmetros       │ Argumentos de linha de comando
```
Regra de Ouro:

- ✅  stdout : Apenas o resultado final
- ✅  stderr : Todo o resto (logs, progresso, erros)

--------

## Exemplo Completo: Plugin  @hello  em Go

Este exemplo demonstra todas as melhores práticas:

> // ~/.chatcli/plugins-src/hello/main.go
```go
package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "os"
    "time"
)

// Metadata define a estrutura para --metadata
type Metadata struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Usage       string `json:"usage"`
    Version     string `json:"version"`
}

// Schema define a estrutura para --schema
type Schema struct {
    Parameters []Parameter `json:"parameters"`
}

type Parameter struct {
    Name        string `json:"name"`
    Type        string `json:"type"`
    Required    bool   `json:"required"`
    Description string `json:"description"`
    Default     string `json:"default,omitempty"`
}

// logf envia mensagens de progresso para stderr (visível ao usuário)
func logf(format string, v ...interface{}) {
    fmt.Fprintf(os.Stderr, format, v...)
}

func main() {
    // Flags de descoberta
    metadataFlag := flag.Bool("metadata", false, "Exibe os metadados do plugin")
    schemaFlag := flag.Bool("schema", false, "Exibe o schema de parâmetros")
    flag.Parse()

    // Responder --metadata
    if *metadataFlag {
        meta := Metadata{
            Name:        "@hello",
            Description: "Plugin de exemplo que demonstra o fluxo stdout/stderr",
            Usage:       "@hello [seu-nome]",
            Version:     "1.0.0",
        }
        jsonMeta, _ := json.Marshal(meta)
        fmt.Println(string(jsonMeta)) // stdout para o ChatCLI
        return
    }

    // Responder --schema
    if *schemaFlag {
        schema := Schema{
            Parameters: []Parameter{
                {
                    Name:        "nome",
                    Type:        "string",
                    Required:    false,
                    Description: "Nome da pessoa a ser cumprimentada",
                    Default:     "Mundo",
                },
            },
        }
        jsonSchema, _ := json.Marshal(schema)
        fmt.Println(string(jsonSchema)) // stdout para o ChatCLI
        return
    }

    // Lógica principal do plugin
    logf("🚀 Plugin 'hello' iniciado!\n") // stderr = progresso visível

    time.Sleep(2 * time.Second) // Simula trabalho
    logf("   ⏳ Realizando uma tarefa demorada...\n")
    time.Sleep(2 * time.Second)

    name := "Mundo"
    if len(flag.Args()) > 0 {
        name = flag.Args()[0]
    }

    logf("✅ Tarefa concluída!\n") // stderr = feedback de progresso

    // Resultado final para stdout (será enviado para a IA)
    fmt.Printf("Olá, %s! A hora agora é %s.", name, time.Now().Format(time.RFC1123))
}
```
### Compilação e Instalação

# 1. Compilar
```bash
cd ~/.chatcli/plugins-src/hello
go build -o hello main.go
```
# 2. Dar permissão de execução (CRÍTICO!)
```bash
chmod +x hello
```
# 3. Mover para o diretório de plugins
```bash
mv hello ~/.chatcli/plugins/
```
# 4. Verificar instalação
```bash
❯ /plugin list
📦 Plugins Instalados (1):
  • @hello  - Plugin de exemplo que demonstra o fluxo stdout/stderr
```
### Testando o Plugin

# Dentro do ChatCLI
```bash
❯ /agent @hello Edilson
```
# Saída no terminal (stderr):
```bash
🚀 Plugin 'hello' iniciado!
   ⏳ Realizando uma tarefa demorada...
✅ Tarefa concluída!
```
# Resposta da IA (baseada no stdout):
> O plugin retornou: "Olá, Edilson! A hora agora é Mon, 02 Jan 2024 14:30:00 UTC."

--------

## Debugging de Plugins

### Verificar se o Plugin Foi Carregado

> ❯ /plugin list

Se o plugin não aparecer:

1. Verifique permissões:
ls -l ~/.chatcli/plugins/

Deve mostrar -rwxr-xr-x (com 'x')

2. Teste o contrato  --metadata :
> ~/.chatcli/plugins/seu-plugin --metadata

Deve retornar JSON válido

3. Ative logs de debug:

No .env
```bash
LOG_LEVEL=debug
ENV=dev
```

### Testar Plugin Manualmente

Antes de usar no agente, teste diretamente:

- Testar metadados
  - ~/.chatcli/plugins/seu-plugin --metadata

- Testar schema
  - ~/.chatcli/plugins/seu-plugin --schema

- Testar execução
  - ~/.chatcli/plugins/seu-plugin arg1 arg2

### Resolver Problemas de Timeout

Se o plugin está sendo interrompido:

- Aumentar timeout globalmente
  - export CHATCLI_AGENT_PLUGIN_TIMEOUT=30m

- Ou no .env
  - CHATCLI_AGENT_PLUGIN_TIMEOUT=30m

--------

## Exemplo Avançado: Plugin Docker Hub

Este exemplo demonstra integração com API externa:

// chatcli-plugin-dockerhub/main.go
```go
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

type Metadata struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Usage       string `json:"usage"`
    Version     string `json:"version"`
}

type DockerHubResponse struct {
    Results []struct {
        Name string `json:"name"`
    } `json:"results"`
}

func main() {
    if len(os.Args) > 1 && os.Args[1] == "--metadata" {
        meta := Metadata{
            Name:        "@dockerhub",
            Description: "Consulta tags disponíveis de uma imagem no Docker Hub",
            Usage:       "@dockerhub <imagem>",
            Version:     "1.0.0",
        }
        jsonMeta, _ := json.Marshal(meta)
        fmt.Println(string(jsonMeta))
        return
    }

    if len(os.Args) < 2 {
        fmt.Fprintln(os.Stderr, "❌ Erro: Nome da imagem é obrigatório.")
        fmt.Fprintln(os.Stderr, "💡 Uso: @dockerhub <imagem>")
        os.Exit(1)
    }

    imageName := os.Args[1]
    fmt.Fprintf(os.Stderr, "🔍 Consultando tags para '%s'...\n", imageName)

    // Chamada à API do Docker Hub
    url := fmt.Sprintf("https://hub.docker.com/v2/repositories/library/%s/tags?page_size=25", imageName)
    client := &http.Client{Timeout: 10 * time.Second}
    resp, err := client.Get(url)
    if err != nil {
        fmt.Fprintf(os.Stderr, "❌ Erro na requisição: %v\n", err)
        os.Exit(1)
    }
    defer resp.Body.Close()

    body, _ := io.ReadAll(resp.Body)
    var apiResponse DockerHubResponse
    if err := json.Unmarshal(body, &apiResponse); err != nil {
        fmt.Fprintf(os.Stderr, "❌ Erro ao parsear resposta: %v\n", err)
        os.Exit(1)
    }

    // Extrair tags
    var tags []string
    for _, result := range apiResponse.Results {
        tags = append(tags, result.Name)
    }

    fmt.Fprintf(os.Stderr, "✅ %d tags encontradas\n", len(tags))

    // Resultado final para stdout (para a IA)
    fmt.Println(strings.Join(tags, "\n"))
}
```
### Caso de Uso

> ❯ /agent implante a última versão alpine do redis

- O agente irá:
    - 1. Usar @dockerhub redis para listar tags
    - 2. Filtrar tags com "alpine"
    - 3. Selecionar a versão mais recente
    - 4. Executar docker run redis:<tag-alpine>
    - 5. Validar que o container está rodando

--------

## Linguagens Suportadas

Qualquer linguagem que possa:

- Criar um executável
- Interagir com I/O padrão (stdin/stdout/stderr)
- Processar argumentos de linha de comando

### Recomendações por Caso de Uso
```bash
Linguagem │ Melhor Para         │ Vantagens                              
──────────┼─────────────────────┼────────────────────────────────────────
Go        │ Plugins de produção │ Binários estáticos, rápidos, portáteis
Rust      │ Performance crítica │ Segurança de memória, velocidade       
Python    │ Prototipagem rápida │ Ecossistema rico, fácil debugging      
Bash      │ Scripts de sistema  │ Integração nativa com shell            
Node.js   │ Integração com APIs │ NPM, async/await
```
--------

## Segurança e Melhores Práticas

### Validação de Entrada
```go
if len(os.Args) < 2 {
    fmt.Fprintln(os.Stderr, "❌ Erro: Argumentos insuficientes")
    os.Exit(1)
}
```
### Tratamento de Erros
```go
if err != nil {
    fmt.Fprintf(os.Stderr, "❌ Erro: %v\n", err)
    os.Exit(1) // Exit code != 0 sinaliza erro para o ChatCLI
}
```
### Timeouts Internos
```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
```
### Logs Informativos
```go
fmt.Fprintln(os.Stderr, "⏳ Iniciando operação demorada...")
fmt.Fprintln(os.Stderr, "   - Etapa 1 de 3...")
fmt.Fprintln(os.Stderr, "✅ Operação concluída!")
```
--------

## Plugins no Modo /coder

O modo [/coder](/docs/core-concepts/coder-mode/) é especializado em engenharia de software e depende do plugin [@coder](/docs/features/coder-plugin/) para executar suas ações.

No `/coder`, a IA emite chamadas de ferramenta em um formato estrito:

- Primeiro, escreve um `<reasoning>` curto (2 a 6 linhas)
- Em seguida, emite apenas um `<tool_call name="@coder" args="..."/>`
 - JSON em `args` é recomendado

Exemplos de chamadas reais (que a IA emite no /coder):

- `<tool_call name="@coder" args="{\"cmd\":\"tree\",\"args\":{\"dir\":\".\"}}"/>`
- `<tool_call name="@coder" args="{\"cmd\":\"read\",\"args\":{\"file\":\"cli/agent_mode.go\"}}"/>`
- `<tool_call name="@coder" args="{\"cmd\":\"test\",\"args\":{\"dir\":\".\"}}"/>`

Veja mais em [Modo Coder](/docs/core-concepts/coder-mode/) e [Plugin @coder](/docs/features/coder-plugin/).

--------

## Próximos Passos

1. Explore os plugins de exemplo em  ~/.chatcli/plugins-examples/
2. Crie seu primeiro plugin seguindo o template  @hello
3. Compartilhe plugins com a comunidade via GitHub
4. Contribua com plugins para o ecossistema ChatCLI

--------

## Recursos Adicionais

• Exemplos de Plugins https://github.com/diillson/chatcli/tree/main/plugins-examples

--------

O sistema de plugins é a sua porta de entrada para a verdadeira automação. Comece a construir suas ferramentas e transforme seu terminal em um colega de equipe.
