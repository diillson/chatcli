# 🐳 @docker-run — Plugin para iniciar contêineres Docker via chatcli

O **@docker-run** é um plugin para o **chatcli** que permite iniciar contêineres Docker usando uma interface simples, estruturada e totalmente integrada à experiência de conversação.
Ele funciona como um wrapper inteligente do comando `docker run`, suportando:

* Imagem e tag
* Nome do contêiner
* Mapeamento de portas
* Variáveis de ambiente (`-e` / `--env`)
* Volumes (`-v` / `--volume`)
* Múltiplas flags repetidas
* Execução em modo *detached*
* Log de depuração no `stderr`

---

## ✨ Funcionalidades

✔️ Inicia contêineres Docker rapidamente
✔️ Suporte completo a múltiplos `-e` e `-v`
✔️ Alias de flags (`--env`, `--volume`)
✔️ Validação de campos obrigatórios
✔️ Suporta imagens com tag (`nginx:latest`)
✔️ Retorna o ID do contêiner iniciado
✔️ Compatível com o sistema de metadados do chatcli

---

## 📄 Uso

### ▶️ Executar um contêiner

```
@docker-run --image nginx --name web
```

Inicia:

```
docker run -d --name web nginx:latest
```

---

### ▶️ Informar tag

```
@docker-run --image postgres --tag 15 --name db
```

---

### ▶️ Mapeamento de portas

```
@docker-run --image redis --name cache --port 6379:6379
```

---

### ▶️ Variáveis de ambiente

```
@docker-run --image postgres --name db \
  -e POSTGRES_PASSWORD=admin \
  -e POSTGRES_USER=root
```

Ambos funcionam:

```
-e KEY=value
--env KEY=value
```

---

### ▶️ Volumes

```
@docker-run --image mysql --name mysql \
  -v /data/mysql:/var/lib/mysql \
  -v /logs/mysql:/var/log/mysql
```

Alias funcional:

```
--volume host:container
```

---

### ▶️ Gerar metadados do plugin

```
./docker-run --metadata
```

Saída:

```json
{
  "name": "@docker-run",
  "description": "Inicia um contêiner Docker. Suporta flags para imagem, tag, porta, nome, variáveis de ambiente (-e) e volumes (-v).",
  "usage": "@docker-run --image <img> --tag <tag> --port <p:p> --name <nome> [-e VAR=val] [-v /host:/cont]",
  "version": "1.2.0"
}
```

---

## 🛠️ Instalação

### 1. Compile o binário

```sh
go build -o docker-run .
```

### 2. Adicione ao chatcli

```sh
chatcli plugins add ./docker-run
```

---

## 🔧 Requisitos

* Docker instalado e rodando
* Go 1.20+
* Permissão para executar `docker run`

---

## 🧠 Funcionamento interno

O plugin:

1. Lê e valida flags (`--image` e `--name` são obrigatórias)
2. Suporta múltiplos valores repetidos para `-e` e `-v`
3. Monta o comando `docker run`
4. Emite log de debug no `stderr`:

   ```
   Debug: Executando comando: docker run -d ...
   ```
5. Executa o comando real
6. Imprime o ID do contêiner no `stdout`

---

## 🐳 Exemplo de saída

```
Contêiner 'web' iniciado com sucesso. ID: 3a4c55d9a81234bc567
```
