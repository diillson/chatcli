## 📦 @docker-ps — Plugin para listar contêineres Docker via chatcli

O **@docker-ps** é um plugin desenvolvido para o **chatcli**, permitindo listar contêineres Docker diretamente a partir de comandos conversacionais.
Ele funciona como um wrapper do comando nativo `docker ps`, oferecendo uma interface simples e integrada ao ecossistema de plugins.

---

## 🚀 Funcionalidades

* Lista contêineres Docker em execução.
* Suporte ao parâmetro `--all` ou `-a` para incluir contêineres parados.
* Retorna saída bruta do Docker (exatamente como no terminal).
* Verifica automaticamente se o daemon Docker está disponível.
* Possui endpoint de metadados para descoberta automática pelo chatcli.

---

## 📄 Uso

### Comando básico

```
@docker-ps
```

Lista apenas contêineres em execução.

### Incluir contêineres parados

```
@docker-ps --all
```

ou

```
@docker-ps -a
```

### Obter metadados do plugin

```
./docker-ps --metadata
```

Retorna um JSON como:

```json
{
  "name": "@docker-ps",
  "description": "Lista contêineres Docker. Use --all para incluir contêineres parados.",
  "usage": "@docker-ps [--all]",
  "version": "0.1.0"
}
```

---

## 🛠️ Instalação

Compile o binário:

```sh
go build -o docker-ps .
```

Depois registre no seu chatcli (exemplo):

```sh
chatcli plugins add ./docker-ps
```

---

## 🔧 Requisitos

* Docker instalado e rodando na máquina.
* Go 1.20+ para compilar.
* Permissões para executar comandos Docker.

---

## 🧩 Código Fonte

O plugin simplesmente:

1. Lê argumentos enviados pelo chatcli
2. Mapeia para `docker ps`
3. Verifica se o Docker está acessível
4. Executa o comando nativo
5. Retorna a saída diretamente ao chatcli

---

## 🐳 Exemplos de saída

```
CONTAINER ID   IMAGE            COMMAND               STATUS          NAMES
af3c12cd3bb2   redis:alpine     "docker-entrypoint…"  Up 2 hours      redis_cache
```
