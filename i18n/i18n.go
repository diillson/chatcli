package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

//go:embed locales/*.json
var localesFS embed.FS

var printer *message.Printer
var activeTag language.Tag
var initOnce sync.Once

// rawByTag holds the untemplated catalog strings per language. T consults it
// for ARGLESS lookups: x/text's printer always formats the message, so asking
// it for a string that contains verbs (%s, %d, %w error templates, a literal
// "90%") without arguments stamps %!x(MISSING) into user-facing text and
// corrupts fmt.Errorf wrapping templates. Argless reads must be verbatim.
var rawByTag = map[language.Tag]map[string]string{}

// rawTag is the registered tag whose raw catalog matches the active printer.
var rawTag = defaultLang

// defaultLang é o idioma padrão caso a detecção falhe.
var defaultLang = language.English

// Init inicializa o sistema de internacionalização (thread-safe, idempotent).
// Ele detecta o idioma com a seguinte prioridade:
//  1. Variável de ambiente CHATCLI_LANG (pode ser definida no .env — o
//     entrypoint carrega o .env ANTES de chamar Init)
//  2. Variáveis de ambiente do sistema (LC_ALL, LANG)
//  3. Locale do sistema operacional (Windows: GetUserDefaultLocaleName —
//     cmd/PowerShell não exportam LANG, só o Git Bash exporta)
//  4. Fallback para o idioma padrão (Inglês)
func Init() {
	initOnce.Do(initI18n)
}

// detectLangString percorre a cadeia de detecção de idioma na ordem
// documentada em Init e normaliza o resultado ("pt_BR.UTF-8" → "pt-BR").
func detectLangString() string {
	langStr := os.Getenv("CHATCLI_LANG") // Maior prioridade
	if langStr == "" {
		langStr = os.Getenv("LC_ALL")
		if langStr == "" {
			langStr = os.Getenv("LANG")
		}
	}
	if langStr == "" {
		langStr = systemLocale()
	}

	// Normaliza strings como "pt_BR.UTF-8" para "pt-BR"
	if idx := strings.Index(langStr, "."); idx != -1 {
		langStr = langStr[:idx]
	}
	return strings.Replace(langStr, "_", "-", 1)
}

func initI18n() {
	// 1. Detectar o idioma do usuário com prioridade para CHATCLI_LANG
	userLang, err := language.Parse(detectLangString())
	if err != nil {
		userLang = defaultLang
	}

	// 2. Carregar e registrar todas as traduções dos arquivos JSON embutidos.
	files, err := localesFS.ReadDir("locales")
	if err != nil {
		printer = message.NewPrinter(defaultLang)
		return
	}

	registeredTags := []language.Tag{defaultLang}
	seen := map[language.Tag]bool{defaultLang: true}

	// A locale is the union of its base file (en.json) and its fragments
	// (en.<feature>.json): a feature ships its strings in its own small
	// file instead of editing the shared catalog, so parallel changes
	// never conflict. Base files load first (whatever ReadDir's order),
	// so a fragment may override the base deliberately.
	ordered := make([]string, 0, len(files))
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".json") && !isLocaleFragment(file.Name()) {
			ordered = append(ordered, file.Name())
		}
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".json") && isLocaleFragment(file.Name()) {
			ordered = append(ordered, file.Name())
		}
	}
	for _, fileName := range ordered {

		tagStr, _, _ := strings.Cut(strings.TrimSuffix(fileName, ".json"), ".")
		tag, err := language.Parse(tagStr)
		if err != nil {
			continue
		}

		if !seen[tag] {
			seen[tag] = true
			registeredTags = append(registeredTags, tag)
		}

		content, err := localesFS.ReadFile("locales/" + fileName)
		if err != nil {
			continue
		}

		var translations map[string]string
		if err := json.Unmarshal(content, &translations); err != nil {
			continue
		}

		if rawByTag[tag] == nil {
			rawByTag[tag] = make(map[string]string, len(translations))
		}
		for key, value := range translations {
			rawByTag[tag][key] = value
		}
		for key, value := range translations {
			if err := message.SetString(tag, key, value); err != nil {
				fmt.Printf("aviso i18n: falha ao definir a string para a chave '%s': %v\n", key, err)
			}
		}
	}

	// 3. Criar um matcher para encontrar o melhor idioma disponível.
	matcher := language.NewMatcher(registeredTags)
	bestTag, idx, _ := matcher.Match(userLang)

	// 4. Configurar o printer global para o melhor idioma encontrado. O raw
	// catalog é indexado pela tag REGISTRADA (o matcher pode devolver uma tag
	// com extensões que não é chave do mapa).
	activeTag = bestTag
	if idx >= 0 && idx < len(registeredTags) {
		rawTag = registeredTags[idx]
	}
	printer = message.NewPrinter(bestTag)
}

// isLocaleFragment reports whether name is <tag>.<feature>.json.
func isLocaleFragment(name string) bool {
	return strings.Contains(strings.TrimSuffix(name, ".json"), ".")
}

// ActiveTag returns the resolved language tag (e.g. "pt-BR", "en-US", "en").
func ActiveTag() language.Tag {
	Init() // ensure initialized
	return activeTag
}

// T é a função principal para obter uma string traduzida.
// Com args, usa o printer global para formatar a mensagem. SEM args, devolve
// a string crua do catálogo (com %% desescapado): o Sprintf do x/text
// formataria mesmo assim e estamparia %!s(MISSING) em qualquer valor com
// verbos — incluindo os templates de erro com %w, que precisam chegar
// intactos ao fmt.Errorf do chamador.
// Se o sistema i18n não for inicializado, retorna a chave como fallback.
func T(key string, args ...interface{}) string {
	out := translate(key, args...)
	if fn := renderer.Load(); fn != nil {
		return (*fn)(out)
	}
	return out
}

// renderer, when set, has the last word on every translated string.
var renderer atomic.Pointer[func(string) string]

// SetRenderer installs a function applied to every string T returns, or
// removes it with nil. The interface layer uses it to render messages in
// the active theme's glyph style: the catalogs carry emoji, and a theme
// that shows none cannot rewrite three thousand messages one call site at
// a time. The function must be cheap and safe for concurrent use.
func SetRenderer(fn func(string) string) {
	if fn == nil {
		renderer.Store(nil)
		return
	}
	renderer.Store(&fn)
}

// translate resolves key in the active language and formats it.
func translate(key string, args ...interface{}) string {
	if printer == nil {
		// Fallback caso Init() não tenha sido chamado ou falhou.
		// Retorna a chave para que o desenvolvedor veja que algo está errado.
		if len(args) > 0 {
			return key + " " + fmt.Sprint(args...)
		}
		return key
	}
	if len(args) == 0 {
		if v, ok := rawLookup(key); ok {
			return strings.ReplaceAll(v, "%%", "%")
		}
	}
	return printer.Sprintf(key, args...)
}

// rawLookup resolves key in the active language's raw catalog, falling back
// to the default language (mirroring the matcher's fallback chain).
func rawLookup(key string) (string, bool) {
	if m, ok := rawByTag[rawTag]; ok {
		if v, ok := m[key]; ok {
			return v, true
		}
	}
	if m, ok := rawByTag[defaultLang]; ok {
		if v, ok := m[key]; ok {
			return v, true
		}
	}
	return "", false
}

// LookupIn resolves key in the catalog of the given language tag (for
// example "pt-BR" or "en"), falling back to the default language. It lets
// a surface ship several languages at once, the way the web UI does, while
// T keeps serving the process language.
func LookupIn(tag, key string) (string, bool) {
	parsed, err := language.Parse(tag)
	if err == nil {
		if m, ok := rawByTag[parsed]; ok {
			if v, ok := m[key]; ok {
				return strings.ReplaceAll(v, "%%", "%"), true
			}
		}
	}
	if m, ok := rawByTag[defaultLang]; ok {
		if v, ok := m[key]; ok {
			return strings.ReplaceAll(v, "%%", "%"), true
		}
	}
	return "", false
}
