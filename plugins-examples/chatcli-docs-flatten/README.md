O **@docs-flatten** varre arquivos Markdown (`.md`) de um diretório local ou de um repositório Git remoto, gera *chunks* de texto e exporta em vários formatos (`text`, `jsonl`, `json`, `yaml`).

---

### Flags principais

- `--root <dir>` : diretório raiz local da documentação.  
- `--repo <git-url>` : URL de repositório Git com a documentação.  
- `--branch <name>` : branch a ser usada ao clonar (default: `main`).  
- `--subdir <path>` : subdiretório dentro do repo que contém os `.md` (ex: `docs`).  
- `--format <text|jsonl|json|yaml>` : formato de saída (default: `text`).  
- `--output <file>` : arquivo de saída (se vazio, usa stdout).  
- `--max-chars <N>` : tamanho máximo (em caracteres) por chunk (`0` = sem divisão).  
- `--include <globs>` : padrões glob incluídos, separados por vírgula.  
- `--exclude <globs>` : padrões glob excluídos, separados por vírgula.  
- `--strip-front-matter <true|false>` : remove front matter (default: `true`).  
- `--keep-clone <true|false>` : não apagar o clone temporário do repo (default: `false`).  
- `--metadata` : imprime metadados do plugin em JSON e sai.

**Observação:** flags booleanas aceitam valores explícitos como `--strip-front-matter=false` para evitar ambiguidades ao usar com IA.

---

## 1. Diretório local (`--root`)

### 1.1. Mínimo, saída em texto no stdout

```sh
@docs-flatten --root ./docs
````

### 1.2. Texto em arquivo

```sh
@docs-flatten \
  --root ./docs \
  --format text \
  --output ./out/docs.txt
```

### 1.3. JSONL para RAG

```sh
@docs-flatten \
  --root ./docs \
  --format jsonl \
  --output ./out/docs.jsonl
```

### 1.4. JSON (array de chunks)

```sh
@docs-flatten \
  --root ./docs \
  --format json \
  --output ./out/docs.json
```

### 1.5. YAML (array de chunks)

```sh
@docs-flatten \
  --root ./docs \
  --format yaml \
  --output ./out/docs.yaml
```

### 1.6. Controle de tamanho de chunk

```sh
@docs-flatten \
  --root ./docs \
  --max-chars 8000 \
  --format jsonl \
  --output ./out/docs-8k.jsonl
```

### 1.7. Sem divisão (um chunk por arquivo)

```sh
@docs-flatten \
  --root ./docs \
  --max-chars 0 \
  --format jsonl \
  --output ./out/docs-single-chunk.jsonl
```

### 1.8. Incluindo / excluindo padrões de arquivos

```sh
@docs-flatten \
  --root ./site \
  --include "docs/**.md,content/**.md,**/README.md" \
  --exclude "node_modules/**,public/**,build/**" \
  --format jsonl \
  --output ./out/site-docs.jsonl
```

### 1.9. Preservando ou removendo front matter

Padrão (`--strip-front-matter true`): front matter é removido, mas o `title` vira um `# Título` no início.

Para preservar exatamente como está:

```sh
@docs-flatten \
  --root ./docs \
  --strip-front-matter false \
  --format text \
  --output ./out/docs-with-frontmatter.txt
```

---

## 2. Repositório Git remoto (`--repo`)

Com `--repo`, o plugin clona o repositório automaticamente, processa os `.md` e remove o clone ao final (a menos que `--keep-clone true`).

Defaults ao usar `--repo`:

* **include**: `docs/**.md,content/**.md,**/README.md`
* **exclude**: `.git/**,node_modules/**,public/**,build/**,dist/**`

Chunks gerados podem incluir metadados como `repoUrl` e `commit`.

### 2.1. Repositório simples, branch `main`, auto-include

```sh
@docs-flatten \
  --repo https://github.com/org/docs-repo.git \
  --format jsonl \
  --output ./out/docs.jsonl
```

### 2.2. Repositório + branch específica

```sh
@docs-flatten \
  --repo https://github.com/org/docs-repo.git \
  --branch release-1.0 \
  --format json \
  --output ./out/docs-release-1.0.json
```

### 2.3. Repositório + subdiretório de docs

```sh
@docs-flatten \
  --repo https://github.com/org/monorepo.git \
  --branch main \
  --subdir docs \
  --format yaml \
  --output ./out/monorepo-docs.yaml
```

Outro exemplo:

```sh
@docs-flatten \
  --repo https://github.com/org/monorepo.git \
  --subdir site/content \
  --format jsonl \
  --output ./out/site-content.jsonl
```

### 2.4. Repositório + filtros finos de include/exclude

```sh
@docs-flatten \
  --repo https://github.com/org/docs-repo.git \
  --branch main \
  --subdir docs \
  --include "docs/**.md,**/README.md" \
  --exclude "docs/drafts/**,docs/old/**" \
  --format jsonl \
  --output ./out/docs-clean.jsonl
```

Mesmo que a IA gere com aspas duplas/encadeadas, o parser resolve corretamente.

### 2.5. Manter o clone local (debug/cache)

```sh
@docs-flatten \
  --repo https://github.com/org/docs-repo.git \
  --keep-clone true \
  --format jsonl \
  --output ./out/docs.jsonl
```

O caminho será algo como: `/tmp/docs-flatten-123456789`.

### 2.6. Repositório + saída em texto

```sh
@docs-flatten \
  --repo https://github.com/org/docs-repo.git \
  --subdir docs \
  --format text \
  --output ./out/docs.txt
```

---

## 3. Combinando `--root` e `--repo`

* Pelo menos um é obrigatório: `--root` ou `--repo`.
* Se ambos forem usados, `--repo` tem prioridade.

Exemplo:

```sh
@docs-flatten \
  --root ./fallback-docs \
  --repo https://github.com/org/docs-repo.git \
  --format jsonl \
  --output ./out/docs.jsonl
```

Nesse caso, `./fallback-docs` é ignorado.

---

## 4. Metadados do plugin

Para descoberta automática pelo ChatCLI:

```sh
@docs-flatten --metadata
```

Saída (JSON):

* `name`
* `description`
* `usage`
* `version`

```
