# @docker-clean

Plugin de linha de comando para o **chatcli** que gerencia e limpa recursos Docker:

* Containers (parados ou em execução)
* Imagens (incluindo dangling e não utilizadas)
* Volumes (incluindo apenas não utilizados ou por nome)
* Redes (custom não utilizadas)
* Limpeza completa e de sistema (`docker system prune`)

Focado em uso via IA (chatcli), com **saída amigável**, **modo dry-run** e **suporte a múltiplos filtros**.

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
    * [system](#system)
5. [Filtros, IDs e nomes](#filtros-ids-e-nomes)
6. [Modo dry-run](#modo-dry-run)
7. [Integração com o chatcli](#integração-com-o-chatcli)
8. [Saída de metadados](#saída-de-metadados)
9. [Notas de implementação](#notas-de-implementação)

---

## Pré-requisitos

* **Docker** instalado e disponível no `PATH`

    * O plugin verifica a dependência e falha com erro claro se `docker` não for encontrado.
* Permissão para executar comandos Docker no host:

    * normalmente ser membro do grupo `docker` ou rodar com `sudo`.
* Go (apenas para construir o binário, se ainda não estiver compilado).

---

## Instalação

### 1. Compilar o binário

A partir do diretório que contém o `main.go`:

```bash
go build -o docker-clean
```

Isso gera um binário chamado `docker-clean`.

### 2. Tornar acessível ao chatcli

Existem duas abordagens comuns:

#### a) Via PATH do sistema

```bash
mv docker-clean /usr/local/bin/
chmod +x /usr/local/bin/docker-clean
```

E configure o plugin no `chatcli` para chamar:

```
@docker-clean ...
```

(Dependendo da configuração do chatcli, o nome do plugin pode ser mapeado para o binário `docker-clean`.)

#### b) Via diretório de plugins do chatcli

Se o chatcli tiver um diretório específico de plugins (por exemplo: `~/.chatcli/plugins`):

```bash
mkdir -p ~/.chatcli/plugins
mv docker-clean ~/.chatcli/plugins/
chmod +x ~/.chatcli/plugins/docker-clean
```

E registre o plugin conforme a documentação do chatcli, apontando o comando `@docker-clean` para esse binário.

> O nome lógico do plugin é `@docker-clean` (ver metadados).

---

## Uso rápido

Formato geral:

```
@docker-clean <comando> [opções]
```

Exemplos:

```
# Listar containers parados que seriam removidos (sem remover)
@docker-clean containers --dry-run

# Remover containers com nome/ID contendo "web" ou "api"
@docker-clean containers --filter web,api

# Remover imagens relacionadas a nginx, redis, postgres
@docker-clean images --filter nginx,redis,postgres

# Remover imagens específicas por ID
@docker-clean images --ids abc123,def456

# Limpeza completa
@docker-clean all

# Limpeza de sistema Docker, incluindo volumes
@docker-clean system --all --volumes
```

---

## Comandos disponíveis

---

### containers

Gerencia remoção de containers.

```
@docker-clean containers [opções]
```

**Opções:**

* `--all`
  Remove todos os containers listados (incluindo em execução, com `docker rm -f`).

* `--filter <filtros>`
  Filtro por nome, ID ou imagem (múltiplos separados por vírgula).

* `--ids <ids>`
  Remove containers específicos por ID ou nome.

* `--dry-run`
  Apenas lista o que seria removido.

**Comportamento padrão:**
Lista apenas containers `exited` ou `created`.

**Exemplos:**

```
@docker-clean containers
@docker-clean containers --all
@docker-clean containers --dry-run
@docker-clean containers --filter web,worker
```

---

### images

Gerencia imagens Docker.

```
@docker-clean images [opções]
```

**Opções:**

* `--dangling`
* `--filter <filtros>`
* `--ids <ids>`
* `--unused`
* `--dry-run`

**Exemplos:**

```
@docker-clean images --dry-run
@docker-clean images --dangling
@docker-clean images --filter nginx,redis
@docker-clean images --ids abc123,def456 --unused
```

---

### volumes

Gerencia volumes Docker.

```
@docker-clean volumes [opções]
```

**Opções:**

* `--filter <filtros>`
* `--names <nomes>`
* `--all`
* `--dry-run`

**Exemplos:**

```
@docker-clean volumes --dry-run
@docker-clean volumes --all --dry-run
@docker-clean volumes
@docker-clean volumes --names db-data,redis-cache
```

---

### networks

Limpa redes custom não utilizadas.

```
@docker-clean networks [opções]
```

**Opções:**

* `--dry-run`

**Exemplos:**

```
@docker-clean networks --dry-run
@docker-clean networks
```

---

### all

Executa limpeza completa.

```
@docker-clean all [opções]
```

**Opções:**

* `--dry-run`
* `--include-running`

**Exemplos:**

```
@docker-clean all
@docker-clean all --include-running
@docker-clean all --dry-run
```

---

### system

Interface para `docker system prune`.

```
@docker-clean system [opções]
```

**Opções:**

* `--all`
* `--volumes`
* `--dry-run`

**Exemplos:**

```
@docker-clean system --dry-run
@docker-clean system
@docker-clean system --all --volumes
```

---

## Filtros, IDs e nomes

1. **Filtros por substring (`--filter`)**
2. **IDs (`--ids`)**
3. **Nomes (`--names`) para volumes**

Aplicados a:

* containers: nome, ID, imagem
* imagens: repo:tag, repo, ID
* volumes: nome

---

## Modo dry-run

Disponível em:

* `containers --dry-run`
* `images --dry-run`
* `volumes --dry-run`
* `networks --dry-run`
* `system --dry-run`
* `all --dry-run`

**Nunca remove nada.** Lista o que seria apagado.

---

## Integração com o chatcli

Exemplos de uso por linguagem natural:

> “Liste imagens Docker de nginx e redis sem remover nada.”
> → `@docker-clean images --filter nginx,redis --dry-run`

> “Remova containers parados.”
> → `@docker-clean containers`

A saída é sempre legível e estruturada.

---

## Saída de metadados

```
@docker-clean --metadata
```

Retorna:

```json
{
  "name": "@docker-clean",
  "description": "Gerencia, remove containers, imagens, volumes e redes Docker (suporta operações em lote)",
  "usage": "@docker-clean <comando> [opções] ...",
  "version": "2.2.0"
}
```

---

## Notas de implementação

* **Linguagem:** Go
* **Execução:** `exec.CommandContext` com timeout
* **Logs e erros:**

    * `fatalf(...)` com prefixo `❌ ERRO:`
    * Logs via `stderr`
* **Timeouts:**

    * Listagens: ~30s
    * Prunes: até 180s
