package engine

import (
	"encoding/json"
	"strconv"
)

// Metadata holds @coder plugin identification.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

// FlagDefinition describes a subcommand flag.
type FlagDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// SubcommandDefinition describes a plugin subcommand.
type SubcommandDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Flags       []FlagDefinition `json:"flags"`
	Examples    []string         `json:"examples,omitempty"`
}

// PluginSchema is the full plugin schema for LLM context.
type PluginSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	ArgsFormat  string                 `json:"args_format"`
	Subcommands []SubcommandDefinition `json:"subcommands"`
}

// PluginVersion is the version of the @coder tool surface the model sees:
// bumped whenever a subcommand, flag or its documented behavior changes
// (the builtin versions test enforces it). Version above is the engine's
// original contract constant and stays as it is for API compatibility.
const PluginVersion = "2.2.0"

// GetMetadata returns the @coder plugin metadata.
func GetMetadata() Metadata {
	return Metadata{
		Name:        "@coder",
		Description: "Suite de engenharia completa (IO, Search, Exec, Git, Test, Backup, Rollback, Patch).",
		Usage:       `@coder <read|write|patch|multipatch|find|move|delete|tree|search|outline|map|checkpoint|exec|git-status|git-diff|git-log|git-changed|git-branch|git-show|git-blame|test|rollback|clean> [flags]`,
		Version:     PluginVersion,
	}
}

// GetMetadataJSON returns the metadata as a JSON string.
func GetMetadataJSON() string {
	data, err := json.Marshal(GetMetadata())
	if err != nil {
		return "{}"
	}
	return string(data)
}

// GetSchema returns the @coder plugin schema as a JSON string.
func GetSchema() string {
	schema := PluginSchema{
		Name:        "@coder",
		Description: "Ferramentas de engenharia para leitura, escrita, patch, busca, execução e Git.",
		ArgsFormat:  "Aceita argumentos estilo CLI ou JSON (ex.: args=\"{\\\"cmd\\\":\\\"read\\\",\\\"args\\\":{\\\"file\\\":\\\"main.go\\\"}}\")",
		Subcommands: []SubcommandDefinition{
			{
				Name:        "read",
				Description: "Lê arquivos com range, head/tail e limite de bytes.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo.", Required: true},
					{Name: "--start", Type: "int", Description: "Linha inicial (1-based)."},
					{Name: "--end", Type: "int", Description: "Linha final (1-based)."},
					{Name: "--head", Type: "int", Description: "Primeiras N linhas (incompatível com --tail)."},
					{Name: "--tail", Type: "int", Description: "Últimas N linhas (incompatível com --head)."},
					{Name: "--max-bytes", Type: "int", Default: strconv.Itoa(DefaultMaxBytes), Description: "Limite de bytes lidos."},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64"},
				},
				Examples: []string{
					"read --file main.go --start 1 --end 120",
				},
			},
			{
				Name:        "checkpoint",
				Description: "Snapshots shadow-git do workspace (automáticos antes de write/patch/multipatch/exec): list, create e restore para rebobinar a árvore a qualquer snapshot sem tocar o .git do usuário.",
				Flags: []FlagDefinition{
					{Name: "--list", Type: "bool", Description: "Lista os snapshots (default)."},
					{Name: "--create", Type: "bool", Description: "Grava um snapshot agora."},
					{Name: "--restore", Type: "string", Description: "Hash do snapshot para restaurar o workspace."},
					{Name: "--label", Type: "string", Default: "manual", Description: "Rótulo do snapshot em --create."},
				},
				Examples: []string{
					"checkpoint --list",
					"checkpoint --restore a1b2c3d",
				},
			},
			{
				Name:        "outline",
				Description: "Esqueleto de símbolos de um arquivo (declarações + linhas): go/ast para Go, padrões para outras linguagens.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo.", Required: true},
				},
				Examples: []string{
					"outline --file cli/cli.go",
				},
			},
			{
				Name:        "map",
				Description: "Mapa estrutural do repositório dentro de um budget de caracteres: arquivos ranqueados por estrutura, cada um com seu esqueleto de declarações. Use para se orientar em codebases grandes sem ler arquivos inteiros.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório raiz."},
					{Name: "--budget", Type: "int", Default: strconv.Itoa(DefaultMapBudget), Description: "Limite de caracteres do mapa."},
				},
				Examples: []string{
					"map --dir . --budget 8000",
				},
			},
			{
				Name:        "write",
				Description: "Escreve arquivo (com backup) com suporte a base64 e append.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo.", Required: true},
					{Name: "--content", Type: "string", Description: "Conteúdo a escrever.", Required: true},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64"},
					{Name: "--append", Type: "bool", Description: "Anexa ao final do arquivo."},
				},
			},
			{
				Name:        "patch",
				Description: "Aplica patch por search/replace ou unified diff. O trecho buscado deve ocorrer UMA vez (ambíguo = erro com as linhas; ausente = erro com a linha mais parecida); diferença só de indentação é aceita e a substituição é reindentada.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo (opcional se diff tiver arquivos)."},
					{Name: "--search", Type: "string", Description: "Trecho a substituir (text/base64). Inclua linhas de contexto até ser único no arquivo."},
					{Name: "--replace", Type: "string", Description: "Substituição (text/base64)."},
					{Name: "--all", Type: "bool", Default: "false", Description: "Substitui TODAS as ocorrências do trecho (sem isso, um trecho repetido é recusado)."},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64 (para search/replace)"},
					{Name: "--diff", Type: "string", Description: "Unified diff (text/base64)."},
					{Name: "--diff-encoding", Type: "string", Default: "text", Description: "text|base64"},
				},
			},
			{
				Name:        "multipatch",
				Description: "Aplica multiplas edicoes search/replace em multiplos arquivos como uma transacao atomica (all-or-nothing rollback).",
				Flags: []FlagDefinition{
					{Name: "--edits", Type: "string", Required: true, Description: "Array JSON de {file,search,replace,encoding?}. Edicoes sao validadas todas antes de qualquer escrita; falha em qualquer uma restaura todos os arquivos tocados."},
				},
			},
			{
				Name:        "search",
				Description: "Busca por termo/regex com contexto e limites.",
				Flags: []FlagDefinition{
					{Name: "--term", Type: "string", Description: "Texto ou regex.", Required: true},
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--regex", Type: "bool", Description: "Interpreta --term como regex."},
					{Name: "--case-sensitive", Type: "bool", Description: "Busca case-sensitive (default: false)."},
					{Name: "--context", Type: "int", Description: "Linhas de contexto."},
					{Name: "--max-results", Type: "int", Description: "Limite de resultados."},
					{Name: "--glob", Type: "string", Description: "Filtro glob (ex: *.go,*.md)."},
					{Name: "--max-bytes", Type: "int", Default: "1048576", Description: "Ignora arquivos maiores que N bytes (fallback sem rg)."},
				},
			},
			{
				Name:        "tree",
				Description: "Lista árvore de diretórios com limites.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--max-depth", Type: "int", Default: "6", Description: "Profundidade máxima."},
					{Name: "--max-entries", Type: "int", Default: strconv.Itoa(DefaultMaxEntries), Description: "Limite de itens."},
					{Name: "--include-hidden", Type: "bool", Description: "Inclui arquivos ocultos."},
					{Name: "--ignore", Type: "string", Description: "Nomes/padrões separados por vírgula."},
				},
			},
			{
				Name:        "exec",
				Description: "Executa comando shell (com proteção a comandos perigosos).",
				Flags: []FlagDefinition{
					{Name: "--cmd", Type: "string", Description: "Comando a executar.", Required: true},
					{Name: "--dir", Type: "string", Description: "Diretório de execução."},
					{Name: "--timeout", Type: "int", Default: "600", Description: "Timeout em segundos."},
				},
			},
			{
				Name:        "git-status",
				Description: "Status git resumido.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
				},
			},
			{
				Name:        "git-diff",
				Description: "Diff git com opções.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
					{Name: "--staged", Type: "bool", Description: "Diff staged."},
					{Name: "--name-only", Type: "bool", Description: "Somente nomes de arquivos."},
					{Name: "--stat", Type: "bool", Description: "Resumo estatístico."},
					{Name: "--path", Type: "string", Description: "Filtra por caminho."},
					{Name: "--context", Type: "int", Default: "3", Description: "Linhas de contexto."},
				},
			},
			{
				Name:        "git-log",
				Description: "Log git simplificado.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
					{Name: "--limit", Type: "int", Default: "20", Description: "Quantidade de commits."},
					{Name: "--path", Type: "string", Description: "Filtra por caminho."},
				},
			},
			{
				Name:        "git-changed",
				Description: "Lista arquivos alterados (status porcelain).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
				},
			},
			{
				Name:        "git-branch",
				Description: "Branch atual.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
				},
			},
			{
				Name:        "git-show",
				Description: "Mostra um commit (mensagem e patch, ou --stat) ou um arquivo como estava em um ref (--path).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
					{Name: "--ref", Type: "string", Default: "HEAD", Description: "Commit, tag ou branch."},
					{Name: "--path", Type: "string", Description: "Arquivo a mostrar como estava no ref (sem o patch)."},
					{Name: "--stat", Type: "bool", Description: "Só o resumo de arquivos tocados."},
				},
			},
			{
				Name:        "git-blame",
				Description: "Autor, commit e data de cada linha de um arquivo (ou de um intervalo --start/--end).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
					{Name: "--file", Type: "string", Description: "Arquivo.", Required: true},
					{Name: "--start", Type: "int", Description: "Primeira linha (1-based)."},
					{Name: "--end", Type: "int", Description: "Última linha (1-based)."},
				},
			},
			{
				Name:        "find",
				Description: "Lista arquivos pelo NOME (glob) ou por trecho do caminho — o que search não faz; ignora .git, node_modules, vendor e ocultos.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--pattern", Type: "string", Description: "Glob do nome do arquivo, ex.: *_test.go, Dockerfile*."},
					{Name: "--contains", Type: "string", Description: "Trecho (sem distinção de caixa) do caminho relativo."},
					{Name: "--limit", Type: "int", Default: "200", Description: "Máximo de resultados."},
					{Name: "--dirs", Type: "bool", Description: "Inclui diretórios no resultado."},
				},
			},
			{
				Name:        "move",
				Description: "Move ou renomeia um arquivo dentro do workspace (backup .bak da origem; recusa destino existente sem --force).",
				Flags: []FlagDefinition{
					{Name: "--from", Type: "string", Description: "Caminho atual.", Required: true},
					{Name: "--to", Type: "string", Description: "Caminho novo (diretórios criados).", Required: true},
					{Name: "--force", Type: "bool", Description: "Substitui o destino se existir."},
				},
			},
			{
				Name:        "delete",
				Description: "Remove um arquivo (nunca diretório) deixando um .bak ao lado; rollback restaura.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Arquivo a remover.", Required: true},
				},
			},
			{
				Name:        "test",
				Description: "Roda testes detectando stack ou via --cmd.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--cmd", Type: "string", Description: "Comando de teste customizado."},
					{Name: "--timeout", Type: "int", Default: "1800", Description: "Timeout em segundos."},
				},
			},
			{
				Name:        "rollback",
				Description: "Restaura arquivo via backup .bak.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo.", Required: true},
				},
			},
			{
				Name:        "clean",
				Description: "Limpa backups .bak (dry-run por padrão).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--force", Type: "bool", Description: "Aplica a limpeza (remove arquivos)."},
					{Name: "--pattern", Type: "string", Default: "*.bak", Description: "Padrão de arquivos."},
				},
			},
		},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return "{}"
	}
	return string(data)
}
