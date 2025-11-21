# @docker-list

Plugin de linha de comando para o **chatcli** que lista recursos Docker com filtros e resumo:

* Containers (todos, apenas em execução ou apenas parados)
* Imagens (todas ou apenas dangling)
* Volumes (todos ou apenas não utilizados)
* Redes
* Resumo geral do estado do Docker (`all`)

Saída formatada e amigável para leitura por humanos e por IA, com suporte a **filtros múltiplos** e modo **verbose**.

---

## Índice

1. [Pré-requisitos](#pré-requisitos)
2. [Instalação](#instalação)
3. [Uso rápido](#uso-rápido)
4. [Comandos disponíveis](#comandos-disponíveis)

    * [containers](#containers)
    * [images](#images)
    * [volumes](#volumes)
    * [networks](#networks)
    * [all](#all)
5. [Filtros](#filtros)
6. [Modo verbose](#modo-verbose)
7. [Integração com o chatcli](#integração-com-o-chatcli)
8. [Saída de metadados](#saída-de-metadados)
9. [Notas de implementação](#notas-de-implementação)

---

## Pré-requisitos

* **Docker** instalado e disponível no `PATH`

    * O plugin aborta com erro se `docker` não for encontrado.
* Permissão para executar comandos Docker:

    * ser membro do grupo `docker` ou usar `sudo`.
* Go instalado (somente necessário para compilar o binário).

---

## Instalação

### 1. Compilar o binário

No diretório onde está o `main.go`:

```bash
go build -o docker-list
```

Isso gera o binário `docker-list`.

### 2. Disponibilizar para o chatcli

Depende de como o chatcli encontra plugins. Duas opções comuns:

#### a) Colocar no PATH

```bash
mv docker-list /usr/local/bin/
chmod +x /usr/local/bin/docker-list
```

Configure o chatcli para usar o comando lógico:

```
@docker-list ...
```

mapeando para o binário `docker-list`.

#### b) Diretório de plugins do chatcli

Se o chatcli usar algo como `~/.chatcli/plugins`:

```bash
mkdir -p ~/.chatcli/plugins
mv docker-list ~/.chatcli/plugins/
chmod +x ~/.chatcli/plugins/docker-list
```

E registre o plugin com o nome lógico `@docker-list`.

---

## Uso rápido

Formato geral:

```
@docker-list <comando> [opções]
```

Exemplos:

```
# Listar todos os containers
@docker-list containers

# Listar containers em execução, com detalhes
@docker-list containers --running --verbose

# Listar containers parados contendo "nginx" ou "redis"
@docker-list containers --stopped --filter nginx,redis

# Listar imagens contendo "postgres"
@docker-list images --filter postgres

# Apenas imagens dangling
@docker-list images --dangling

# Volumes não utilizados
@docker-list volumes --dangling

# Redes
@docker-list networks

# Resumo completo
@docker-list all

# Resumo com uso de disco
@docker-list all --verbose
```

---

## Comandos disponíveis

---

### containers

Lista containers Docker com filtros por estado e nome/imagem.

```
@docker-list containers [opções]
```

**Opções:**

* `--running`
  Lista apenas containers em execução (`docker ps`).

* `--stopped`
  Lista apenas containers parados (`exited` ou `created`).

* `--filter <filtros>`
  Filtros por nome ou imagem (múltiplos separados por vírgula).
  Ex.:

  ```
  @docker-list containers --filter nginx,redis,api
  ```

  Substring, case-insensitive.

* `--verbose`
  Exibe portas (`Ports`) e data de criação (`CreatedAt`).

**Padrão:**

* Lista todos os containers (`docker ps -a`)
* Aplica filtros se houver
* Exibe:

    * índice
    * ícone (✅ Up / ❌ Exited / ⏹️ outro)
    * nome
    * ID (12 chars)
    * imagem
    * status
    * tamanho
    * (verbose) portas e criação

**Exemplos:**

```
@docker-list containers --verbose
@docker-list containers --running
@docker-list containers --stopped
@docker-list containers --filter web,worker
```

---

### images

Lista imagens Docker, com filtros e suporte a dangling.

```
@docker-list images [opções]
```

**Opções:**

* `--filter <filtros>`
  Filtra por `repository:tag` ou por nome do repositório.

* `--dangling`
  Apenas imagens sem tag.

* `--verbose`
  Exibe `CreatedAt`.

**Comportamento:**

* Usa:

  ```
  docker images --format "{{.ID}}|{{.Repository}}|{{.Tag}}|{{.Size}}|{{.CreatedAt}}"
  ```
* Aplica filtros em:

    * `repo:tag`
    * `repository`

**Exemplos:**

```
@docker-list images
@docker-list images --dangling
@docker-list images --filter redis,postgres
@docker-list images --verbose
```

---

### volumes

Lista volumes Docker.

```
@docker-list volumes [opções]
```

**Opções:**

* `--filter <filtros>`
  Filtra por substring no nome.

* `--dangling`
  Apenas volumes não utilizados.

* `--verbose`
  Exibe `Mountpoint` e `Scope`.

**Comportamento:**

* Usa:

  ```
  docker volume ls --format "{{.Name}}|{{.Driver}}|{{.Mountpoint}}|{{.Scope}}"
  ```

**Exemplos:**

```
@docker-list volumes
@docker-list volumes --dangling
@docker-list volumes --filter db,backup
@docker-list volumes --verbose
```

---

### networks

Lista redes Docker.

```
@docker-list networks [opções]
```

**Opções:**

* `--filter <filtros>`
  Filtra por substring no nome.

* `--verbose`
  Mostra `Scope`.

**Exibe ícones:**

* 🔧 redes padrão: `bridge`, `host`, `none`
* 🔗 redes custom

**Exemplos:**

```
@docker-list networks
@docker-list networks --filter proxy,internal
@docker-list networks --verbose
```

---

### all

Resumo completo do Docker.

```
@docker-list all [opções]
```

**Opção:**

* `--verbose`
  Inclui `docker system df`.

**Inclui:**

1. Containers
2. Imagens
3. Volumes
4. Redes
5. (verbose) uso de disco

**Exemplos:**

```
@docker-list all
@docker-list all --verbose
```

---

## Filtros

Quase todos os comandos aceitam:

```
--filter valor1,valor2,...
```

* Case-insensitive
* Substring
* Múltiplos valores (`split` por vírgula)

**Aplicação:**

* Containers → Nome e imagem
* Imagens → repo:tag e repository
* Volumes → nome
* Networks → nome

---

## Modo verbose

`--verbose` adiciona:

* Containers → portas + criação
* Imagens → criação
* Volumes → mountpoint + scope
* Networks → scope
* All → `docker system df`

---

## Integração com o chatcli

Exemplos de uso conversacional:

Usuário:

> “Liste todos os containers em execução e mostre as portas.”

chatcli converte:

```
@docker-list containers --running --verbose
```

Usuário:

> “Me dê um resumo do Docker incluindo uso de disco.”

chatcli converte:

```
@docker-list all --verbose
```

---

## Saída de metadados

```
@docker-list --metadata
```

Retorna:

```json
{
  "name": "@docker-list",
  "description": "Lista containers, imagens, volumes e redes Docker com filtros avançados",
  "usage": "@docker-list <comando> [opções]\n\nExemplos:\n  @docker-list containers\n  @docker-list containers --running\n  @docker-list containers --filter nginx,redis\n  @docker-list images --filter postgres\n  @docker-list volumes\n  @docker-list networks\n  @docker-list all",
  "version": "1.0.0"
}
```

---

## Notas de implementação

* Linguagem: **Go**
* Execução:

    * `exec.CommandContext` com `context.WithTimeout`
    * stdout e stderr unidos
* Verificações:

    * `ensureDependencies("docker")`
* Logs:

    * erros: `❌ ERRO:` + `os.Exit(1)`
    * progresso: `logf(...)` no stderr
* Timeouts:

    * 30s para listagens e `docker system df`.
