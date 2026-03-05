+++
title = "MCP (Model Context Protocol)"
linkTitle = "MCP"
weight = 77
description = "Integre ferramentas externas via Model Context Protocol com suporte a transporte stdio e SSE."
icon = "extension"
+++

O **MCP (Model Context Protocol)** e um protocolo aberto para interoperabilidade de ferramentas com modelos de IA. O ChatCLI integra com servidores MCP, permitindo que a IA acesse ferramentas externas como sistema de arquivos, buscas web, bancos de dados e qualquer servico compativel.

---

## Visao Geral

```
ChatCLI ←→ MCP Manager ←→ MCP Server (stdio/SSE)
                              ↓
                         Ferramentas externas
                         (filesystem, search, DB, ...)
```

O MCP Manager:
1. Carrega a configuracao de servidores MCP
2. Inicia os servidores configurados
3. Descobre as ferramentas disponíveis em cada servidor
4. Expoe as ferramentas ao modelo de IA como `ToolDefinition`
5. Roteia chamadas de ferramentas para o servidor correto

---

## Configuracao

### Arquivo de Configuracao

Crie `~/.chatcli/mcp_servers.json`:

```json
{
  "mcpServers": [
    {
      "name": "filesystem",
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@anthropic/mcp-server-filesystem", "/workspace"],
      "enabled": true
    },
    {
      "name": "web-search",
      "transport": "sse",
      "url": "http://localhost:8080/sse",
      "enabled": true
    },
    {
      "name": "database",
      "transport": "stdio",
      "command": "/usr/local/bin/mcp-postgres",
      "args": ["--connection-string", "postgresql://localhost/mydb"],
      "env": {
        "PGPASSWORD": "secret"
      },
      "enabled": true
    }
  ]
}
```

### Variaveis de Ambiente

```bash
CHATCLI_MCP_ENABLED=true
CHATCLI_MCP_CONFIG=~/.chatcli/mcp_servers.json
```

### Via Flag do Servidor

```bash
chatcli server --mcp-config ~/.chatcli/mcp_servers.json
```

---

## Transportes

### stdio

O transporte `stdio` inicia um processo local e se comunica via stdin/stdout usando JSON-RPC 2.0:

```json
{
  "name": "filesystem",
  "transport": "stdio",
  "command": "npx",
  "args": ["-y", "@anthropic/mcp-server-filesystem", "/workspace"]
}
```

**Uso**: Servidores locais distribuidos como pacotes npm, binarios standalone ou scripts.

### SSE (Server-Sent Events)

O transporte `sse` conecta a um servidor HTTP remoto via Server-Sent Events:

```json
{
  "name": "remote-tools",
  "transport": "sse",
  "url": "http://mcp-server:8080/sse"
}
```

**Uso**: Servidores remotos, microservicos, ferramentas compartilhadas em rede.

---

## Nomeacao de Ferramentas

Ferramentas MCP sao automaticamente prefixadas com `mcp_` e recebem a descricao do servidor de origem:

```
Nome original: read_file
Nome no ChatCLI: mcp_read_file
Descricao: [MCP:filesystem] Reads a file from the filesystem
```

Isso evita colisoes com ferramentas nativas do ChatCLI.

---

## Deploy via Helm

### Servidores MCP Inline

```yaml
# values.yaml
mcp:
  enabled: true
  servers:
    - name: filesystem
      transport: stdio
      command: npx
      args: ["-y", "@anthropic/mcp-server-filesystem", "/workspace"]
      enabled: true
```

### ConfigMap Existente

```yaml
mcp:
  enabled: true
  existingConfigMap: "my-mcp-config"
```

O Helm chart cria automaticamente um ConfigMap com o `mcp_servers.json` e monta em `/etc/chatcli/mcp/`.

---

## Verificando Status

O MCP Manager expoe o status de cada servidor:

```go
statuses := mcpMgr.GetServerStatus()
for _, s := range statuses {
    fmt.Printf("Server: %s, Connected: %v, Tools: %d\n",
        s.Name, s.Connected, s.ToolCount)
}
```

Para verificar se uma ferramenta e MCP:

```go
if mcpMgr.IsMCPTool("mcp_read_file") {
    result, err := mcpMgr.ExecuteTool(ctx, "read_file", args)
}
```

---

## Servidores MCP Populares

| Servidor | Descricao | Instalacao |
|:---------|:----------|:-----------|
| `@anthropic/mcp-server-filesystem` | Acesso ao sistema de arquivos | `npx -y @anthropic/mcp-server-filesystem /path` |
| `@anthropic/mcp-server-github` | Integracao com GitHub | `npx -y @anthropic/mcp-server-github` |
| `@anthropic/mcp-server-postgres` | Queries PostgreSQL | `npx -y @anthropic/mcp-server-postgres` |
| `@anthropic/mcp-server-slack` | Integracao com Slack | `npx -y @anthropic/mcp-server-slack` |

> Consulte [modelcontextprotocol.io](https://modelcontextprotocol.io) para a lista completa de servidores MCP disponiveis.
