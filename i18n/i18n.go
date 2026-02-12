package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

//go:embed locales/*.json
var localesFS embed.FS

var printer *message.Printer

// defaultLang é o idioma padrão caso a detecção falhe.
var defaultLang = language.English

// Init inicializa o sistema de internacionalização.
// Ele detecta o idioma com a seguinte prioridade:
// 1. Variável de ambiente CHATCLI_LANG (pode ser definida no .env)
// 2. Variáveis de ambiente do sistema (LC_ALL, LANG)
// 3. Fallback para o idioma padrão (Inglês)
func Init() {
	// 1. Detectar o idioma do usuário com prioridade para CHATCLI_LANG
	langStr := os.Getenv("CHATCLI_LANG") // Maior prioridade
	if langStr == "" {
		langStr = os.Getenv("LC_ALL")
		if langStr == "" {
			langStr = os.Getenv("LANG")
		}
	}

	// Normaliza strings como "pt_BR.UTF-8" para "pt-BR"
	if idx := strings.Index(langStr, "."); idx != -1 {
		langStr = langStr[:idx]
	}
	langStr = strings.Replace(langStr, "_", "-", 1)

	userLang, err := language.Parse(langStr)
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

	for _, file := range files {
		fileName := file.Name()
		if !strings.HasSuffix(fileName, ".json") {
			continue
		}

		tagStr := strings.TrimSuffix(fileName, ".json")
		tag, err := language.Parse(tagStr)
		if err != nil {
			continue
		}

		registeredTags = append(registeredTags, tag)

		content, err := localesFS.ReadFile("locales/" + fileName)
		if err != nil {
			continue
		}

		var translations map[string]string
		if err := json.Unmarshal(content, &translations); err != nil {
			continue
		}

		for key, value := range translations {
			if err := message.SetString(tag, key, value); err != nil {
				fmt.Printf("aviso i18n: falha ao definir a string para a chave '%s': %v\n", key, err)
			}
		}
	}

	// 3. Criar um matcher para encontrar o melhor idioma disponível.
	matcher := language.NewMatcher(registeredTags)
	bestTag, _, _ := matcher.Match(userLang)

	// 4. Configurar o printer global para o melhor idioma encontrado.
	printer = message.NewPrinter(bestTag)
}

// T é a função principal para obter uma string traduzida.
// Ela usa o printer global para formatar a mensagem com a chave e os argumentos fornecidos.
// Se o sistema i18n não for inicializado, retorna a chave como fallback.
func T(key string, args ...interface{}) string {
	if printer == nil {
		// Fallback caso Init() não tenha sido chamado ou falhou.
		// Retorna a chave para que o desenvolvedor veja que algo está errado.
		if len(args) > 0 {
			return key + " " + fmt.Sprint(args...)
		}
		return key
	}
	return printer.Sprintf(key, args...)
}
