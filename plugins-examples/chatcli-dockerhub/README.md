# `@registry-tags`

Plugin CLI para **chatcli**, pipelines CI/CD e automações que busca **todas as tags de uma imagem em registries públicos ou privados**, suportando autenticação automática, tokens, credenciais do Docker e múltiplos backends:

✔️ Docker Hub
✔️ GCR (Google Container Registry)
✔️ GHCR (GitHub Container Registry)
✔️ ACR (Azure Container Registry)
✔️ Harbor (OCI)
✔️ Artifactory (OCI)
✔️ Registries customizados

O plugin funciona **sem interação humana**, com saída limpa e ideal para uso por IA, automação ou para alimentar fluxos DevOps.

---

## ✨ Principais recursos

* 🔍 Descobre automaticamente o registry baseado na imagem (`gcr.io/...`, `ghcr.io/...`, custom, etc.)
* 🔐 Suporte a autenticação:

    * Bearer Token
    * Basic Auth (username/password)
    * Carregamento automático de `~/.docker/config.json`
    * Suporte a env vars: `REGISTRY_USERNAME`, `REGISTRY_PASSWORD`, `REGISTRY_TOKEN`
* 🚀 Compatível com pipelines (stdout limpo, exit codes previsíveis)
* 🔄 Saída em JSON opcional (`--json`)
* 🧠 Modo `--metadata` para que a IA descubra capacidades do plugin

---

## 📦 Instalação

Compile e coloque no PATH:

```bash
go build -o registry-tags
sudo mv registry-tags /usr/local/bin/
chmod +x /usr/local/bin/registry-tags
```

Ou registre como plugin do chatcli:

```
@registry-tags ...
```

---

## 🧪 Uso básico

```
@registry-tags <imagem> [opções]
```

Exemplos:

```bash
@registry-tags redis
@registry-tags meuuser/minhaimg --username=user --password=pass
@registry-tags gcr.io/projeto/app --token=$GCR_TOKEN
@registry-tags ghcr.io/org/repo --token=$GITHUB_TOKEN
@registry-tags registry.empresa.com/app --username=$USER --password=$PASS
```

---

## 🔧 Opções disponíveis

| Flag                | Descrição                                                                      |
| ------------------- | ------------------------------------------------------------------------------ |
| `--registry=<url>`  | Força um registry específico (caso a detecção automática não seja suficiente). |
| `--username=<user>` | Autenticação com usuário.                                                      |
| `--password=<pass>` | Senha para autenticação.                                                       |
| `--token=<token>`   | Token Bearer (GHCR, GCR, Harbor, Artifactory, etc.).                           |
| `--json`            | Saída estruturada em JSON.                                                     |

---

## 🔄 Ordem de prioridade das credenciais

1. **Flags** (`--username`, `--password`, `--token`)
2. **Vars de ambiente**

    * `REGISTRY_USERNAME`
    * `REGISTRY_PASSWORD`
    * `REGISTRY_TOKEN`
3. **`~/.docker/config.json`**

    * Busca automática por auth base64 do registry detectado

---

## 🤖 Modo Metadata

Permite que o chatcli ou uma IA descubra dinamicamente como usar o plugin.

```bash
@registry-tags --metadata
```

Saída:

```json
{
  "name": "@registry-tags",
  "description": "Busca tags de imagens em registries públicos e privados...",
  "usage": "@registry-tags <imagem> [--registry=<url>] ...",
  "version": "3.0.0",
  "tags": ["docker","registry","container",...],
  "examples": ["@registry-tags redis", ...]
}
```

---

## 🔍 Como funciona internamente

### 1. **Detecção automática do registry**

Baseado em prefixos:

| Prefixo                           | Registry         |
| --------------------------------- | ---------------- |
| `gcr.io/`                         | GCR              |
| `ghcr.io/`                        | GHCR             |
| `docker.io/`                      | Docker Hub       |
| `registry.hub.docker.com/`        | Docker Hub       |
| `<custom-domain>/namespace/image` | Registry privado |

---

### 2. API usada em cada registry

| Registry                        | API utilizada                        |
| ------------------------------- | ------------------------------------ |
| Docker Hub                      | `/v2/repositories/<image>/tags/`     |
| GHCR/GCR/ACR/Harbor/Artifactory | `/v2/<image>/tags/list` (OCI padrão) |

---

### 3. Output

Por padrão, retorna **uma tag por linha**, ideal para uso em bash, pipelines e IA.

Exemplo:

```
latest
1.0.1
1.0.0
0.9.9
```

### 4. JSON

```
@registry-tags redis --json
```

Exemplo:

```json
{
  "image": "redis",
  "registry": "https://hub.docker.com",
  "tags": ["latest", "7.2", "7.0", "6.2"],
  "count": 4
}
```

---

## ⚠️ Códigos de erro

| Situação                    | Exit code        |
| --------------------------- | ---------------- |
| Sem imagem informada        | `1`              |
| Autenticação falhou         | `1`              |
| API retornou erro (4xx/5xx) | `1`              |
| Encontrou 0 tags            | `0` (não é erro) |

---

## 📁 Variáveis de ambiente úteis

```bash
export REGISTRY_USERNAME=meuuser
export REGISTRY_PASSWORD=minhasenha
export REGISTRY_TOKEN=meutoken
```

---

## 🧩 Exemplos práticos (ChatCLI)

**Pergunta do usuário:**

> "Quais versões existem para `ghcr.io/org/app`?"

O chatcli executa:

```
@registry-tags ghcr.io/org/app
```

**Pergunta:**

> "Quero o JSON disso."

```
@registry-tags ghcr.io/org/app --json
```

**Imagem privada corporativa:**

```
@registry-tags registry.empresa.com/produto/app --username $REG_USER --password $REG_PASS
```
