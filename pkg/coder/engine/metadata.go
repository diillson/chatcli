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

// GetMetadata returns the @coder plugin metadata.
func GetMetadata() Metadata {
	return Metadata{
		Name:        "@coder",
		Description: "Suite de engenharia completa (IO, Search, Exec, Git, Test, Backup, Rollback, Patch).",
		Usage:       `@coder <read|write|patch|tree|search|exec|git-status|git-diff|git-log|git-changed|git-branch|test|rollback|clean> [flags]`,
		Version:     Version,
	}
}

// GetMetadataJSON returns the metadata as a JSON string.
func GetMetadataJSON() string {
	data, _ := json.Marshal(GetMetadata())
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
				Description: "Aplica patch por search/replace ou unified diff.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo (opcional se diff tiver arquivos)."},
					{Name: "--search", Type: "string", Description: "Trecho a substituir (text/base64)."},
					{Name: "--replace", Type: "string", Description: "Substituição (text/base64)."},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64 (para search/replace)"},
					{Name: "--diff", Type: "string", Description: "Unified diff (text/base64)."},
					{Name: "--diff-encoding", Type: "string", Default: "text", Description: "text|base64"},
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
					{Name: "--allow-unsafe", Type: "bool", Description: "Permite comandos perigosos."},
					{Name: "--allow-sudo", Type: "bool", Description: "Permite sudo (ainda bloqueia comandos perigosos)."},
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
	data, _ := json.Marshal(schema)
	return string(data)
}
