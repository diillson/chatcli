package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"

	"github.com/charmbracelet/glamour"
	"github.com/peterh/liner"
	"go.uber.org/zap"
)

type ChatCLI struct {
	client         llm.LLMClient
	manager        *llm.LLMManager
	logger         *zap.Logger
	provider       string
	model          string
	history        []models.Message
	terminalWidth  int
	line           *liner.State
	commandHistory []string
}

func NewChatCLI(manager *llm.LLMManager, logger *zap.Logger) (*ChatCLI, error) {
	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "OPENAI" // Provedor padrão
	}

	var model string
	if provider == "OPENAI" {
		model = os.Getenv("OPENAI_MODEL")
		if model == "" {
			model = "gpt-3.5-turbo"
		}
	} else {
		model = "" // Para StackSpot, o modelo pode não ser necessário
	}

	client, err := manager.GetClient(provider, model)
	if err != nil {
		return nil, err
	}

	line := liner.NewLiner()
	line.SetCtrlCAborts(true) // Permite que Ctrl+C aborta o input

	cli := &ChatCLI{
		client:         client,
		manager:        manager,
		logger:         logger,
		provider:       provider,
		model:          model,
		history:        []models.Message{},
		line:           line,
		commandHistory: []string{},
	}

	cli.loadHistory()

	return cli, nil
}

func (cli *ChatCLI) Start() {
	defer func() {
		cli.line.Close()
		cli.saveHistory()
	}()

	fmt.Println("Bem-vindo ao ChatCLI!")
	fmt.Printf("Você está conversando com %s (%s)\n", cli.client.GetModelName(), cli.provider)
	fmt.Println("Digite '/exit' ou 'exit' para sair, '/switch' para trocar de provedor.")
	fmt.Println("Use '@history', '@git', '@env' ou '@file <caminho_do_arquivo>' para adicionar contexto ao prompt.")
	fmt.Println("Use '@command <comando>' para executar comandos no terminal.")
	fmt.Println("Ainda ficou com dúvidas? Use '/help'.\n")

	for {
		input, err := cli.line.Prompt("Você: ")
		if err != nil {
			if err == liner.ErrPromptAborted {
				fmt.Println("\nAborted!")
				return
			}
			fmt.Println("Erro ao ler a entrada:", err)
			continue
		}

		input = strings.TrimSpace(input)

		if input == "" {
			continue
		}

		cli.commandHistory = append(cli.commandHistory, input)
		cli.line.AppendHistory(input)

		// Verificar por comandos
		if strings.HasPrefix(input, "/") || input == "exit" || input == "quit" {
			if cli.handleCommand(input) {
				return
			}
			continue
		}

		// Processar comandos especiais
		userInput, additionalContext := cli.processSpecialCommands(input)

		// Adicionar a mensagem do usuário ao histórico, se houver
		if userInput != "" {
			cli.history = append(cli.history, models.Message{
				Role:    "user",
				Content: userInput,
			})
		}

		// Adicionar a saída do comando ao histórico como assistente, se houver
		if additionalContext != "" {
			cli.history = append(cli.history, models.Message{
				Role:    "assistant",
				Content: additionalContext,
			})

			// Exibir a saída do comando diretamente para o usuário
			fmt.Println(additionalContext)
		}

		// Se há entrada para enviar para a LLM
		if userInput != "" {
			// Exibir mensagem "Pensando..." com animação
			cli.showThinkingAnimation()

			// Criar um contexto com timeout
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			// Enviar o prompt para o LLM
			aiResponse, err := cli.client.SendPrompt(ctx, userInput, cli.history)

			// Parar a animação
			cli.stopThinkingAnimation()

			if err != nil {
				fmt.Println("\nErro do LLM:", err)
				cli.logger.Error("Erro do LLM", zap.Error(err))
				continue
			}

			// Adicionar a resposta da IA ao histórico
			cli.history = append(cli.history, models.Message{
				Role:    "assistant",
				Content: aiResponse,
			})

			// Renderizar a resposta da IA
			renderedResponse := cli.renderMarkdown(aiResponse)
			// Exibir a resposta da IA com efeito de digitação
			cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.client.GetModelName(), renderedResponse))
		}

		// Salvar o histórico ao final de cada interação
		cli.saveHistory()
	}
}

func (cli *ChatCLI) handleCommand(userInput string) bool {
	switch userInput {
	case "/exit", "exit", "/quit", "quit":
		fmt.Println("Até mais!")
		return true
	case "/switch":
		// Alternar o provedor de LLM
		fmt.Println("Provedores disponíveis:")
		fmt.Println("1. OPENAI")
		fmt.Println("2. STACKSPOT")
		choiceInput, err := cli.line.Prompt("Selecione o provedor (1 ou 2): ")
		if err != nil {
			if err == liner.ErrPromptAborted {
				fmt.Println("\nAborted!")
				return false
			}
			fmt.Println("Erro ao ler a escolha:", err)
			return false
		}
		choiceInput = strings.TrimSpace(choiceInput)

		var newProvider string
		var newModel string
		switch choiceInput {
		case "1":
			newProvider = "OPENAI"
			newModel = os.Getenv("OPENAI_MODEL")
			if newModel == "" {
				newModel = "gpt-3.5-turbo"
			}
		case "2":
			newProvider = "STACKSPOT"
			newModel = ""
		default:
			fmt.Println("Escolha inválida.")
			return false
		}

		newClient, err := cli.manager.GetClient(newProvider, newModel)
		if err != nil {
			fmt.Println("Erro ao trocar de provedor:", err)
			return false
		}

		cli.client = newClient
		cli.provider = newProvider
		cli.model = newModel
		cli.history = nil // Reiniciar o histórico da conversa
		fmt.Printf("Trocado para %s (%s)\n\n", cli.client.GetModelName(), cli.provider)
		return false
	case "/help":
		fmt.Println("Comandos disponíveis:")
		fmt.Println("@history - Adiciona o histórico do shell ao contexto")
		fmt.Println("@git - Adiciona informações do Git ao contexto")
		fmt.Println("@env - Adiciona variáveis de ambiente ao contexto")
		fmt.Println("@file <caminho_do_arquivo> - Adiciona o conteúdo de um arquivo ao contexto")
		fmt.Println("/exit ou /quit - Sai do ChatCLI")
		fmt.Println("/switch - Troca o provedor de LLM")
		return false
	default:
		fmt.Println("Comando desconhecido. Use /help para ver os comandos disponíveis.")
		return false
	}
}

//func (cli *ChatCLI) processSpecialCommands(userInput string) (string, string) {
//	var additionalContext string
//
//	// Processar @history
//	if strings.Contains(userInput, "@history") {
//		historyData, err := utils.GetShellHistory()
//		if err != nil {
//			fmt.Println("\nErro ao obter o histórico do shell:", err)
//		} else {
//			lines := strings.Split(historyData, "\n")
//			lines = filterEmptyLines(lines) // Remove linhas vazias
//			n := 10                         // Número de comandos recentes a incluir
//			if len(lines) > n {
//				lines = lines[len(lines)-n:]
//			}
//			// Enumerar os comandos a partir do total de comandos menos n
//			startNumber := len(historyData) - len(lines) + 1
//			formattedLines := make([]string, len(lines))
//			for i, cmd := range lines {
//				formattedLines[i] = fmt.Sprintf("%d: %s", startNumber+i, cmd)
//			}
//			limitedHistoryData := strings.Join(formattedLines, "\n")
//			additionalContext += "\nHistórico do Shell (últimos 10 comandos):\n" + limitedHistoryData
//		}
//		userInput = strings.ReplaceAll(userInput, "@history", "")
//	}
//
//	// Processar @git
//	if strings.Contains(userInput, "@git") {
//		gitData, err := utils.GetGitInfo()
//		if err != nil {
//			fmt.Println("\nErro ao obter informações do Git:", err)
//		} else {
//			additionalContext += "\nInformações do Git:\n" + gitData
//		}
//		userInput = strings.ReplaceAll(userInput, "@git", "")
//	}
//
//	// Processar @env
//	if strings.Contains(userInput, "@env") {
//		envData := utils.GetEnvVariables()
//		additionalContext += "\nVariáveis de Ambiente:\n" + envData
//		userInput = strings.ReplaceAll(userInput, "@env", "")
//	}
//
//	// Processar @file
//	if strings.Contains(userInput, "@file") {
//		// Extrair o caminho do arquivo
//		filePath, err := extractFilePath(userInput)
//		if err != nil {
//			fmt.Println("\nErro ao processar o comando @file:", err)
//		} else {
//			// Ler o conteúdo do arquivo
//			fileContent, err := utils.ReadFileContent(filePath)
//			if err != nil {
//				fmt.Println("\nErro ao ler o arquivo:", err)
//			} else {
//				// Detectar o tipo de arquivo com base na extensão
//				fileType := detectFileType(filePath)
//				// Adicionar o conteúdo ao contexto adicional com formatação de código se aplicável
//				if isCodeFile(fileType) {
//					additionalContext += fmt.Sprintf("\nConteúdo do Arquivo (%s - %s):\n```%s\n%s\n```\n", filePath, fileType, fileType, fileContent)
//				} else {
//					additionalContext += fmt.Sprintf("\nConteúdo do Arquivo (%s - %s):\n%s\n", filePath, fileType, fileContent)
//				}
//			}
//		}
//		userInput = removeFileCommand(userInput)
//	}
//
//	return userInput, additionalContext
//}

// Função principal para processar comandos especiais
func (cli *ChatCLI) processSpecialCommands(userInput string) (string, string) {
	var additionalContext string

	// Processar @history
	if strings.Contains(userInput, "@history") {
		historyData, err := utils.GetShellHistory()
		if err != nil {
			fmt.Println("\nErro ao obter o histórico do shell:", err)
		} else {
			lines := strings.Split(historyData, "\n")
			lines = filterEmptyLines(lines) // Remove linhas vazias
			n := 10                         // Número de comandos recentes a incluir
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			formattedLines := make([]string, len(lines))
			for i, cmd := range lines {
				formattedLines[i] = fmt.Sprintf("%d: %s", i+1, cmd)
			}
			limitedHistoryData := strings.Join(formattedLines, "\n")
			additionalContext += "\nHistórico do Shell (últimos 10 comandos):\n" + limitedHistoryData
		}
		userInput = strings.ReplaceAll(userInput, "@history", "")
	}

	// Processar @git
	if strings.Contains(userInput, "@git") {
		gitData, err := utils.GetGitInfo()
		if err != nil {
			fmt.Println("\nErro ao obter informações do Git:", err)
		} else {
			additionalContext += "\nInformações do Git:\n" + gitData
		}
		userInput = strings.ReplaceAll(userInput, "@git", "")
	}

	// Processar @env
	if strings.Contains(userInput, "@env") {
		envData := utils.GetEnvVariables()
		additionalContext += "\nVariáveis de Ambiente:\n" + envData
		userInput = strings.ReplaceAll(userInput, "@env", "")
	}

	// Processar @file
	if strings.Contains(userInput, "@file") {
		// Extrair o caminho do arquivo
		filePath, err := extractFilePath(userInput)
		if err != nil {
			fmt.Println("\nErro ao processar o comando @file:", err)
		} else {
			// Ler o conteúdo do arquivo
			fileContent, err := utils.ReadFileContent(filePath)
			if err != nil {
				fmt.Println("\nErro ao ler o arquivo:", err)
			} else {
				// Detectar o tipo de arquivo com base na extensão
				fileType := detectFileType(filePath)
				// Adicionar o conteúdo ao contexto adicional com formatação de código se aplicável
				if isCodeFile(fileType) {
					additionalContext += fmt.Sprintf("\nConteúdo do Arquivo (%s - %s):\n```%s\n%s\n```\n", filePath, fileType, fileType, fileContent)
				} else {
					additionalContext += fmt.Sprintf("\nConteúdo do Arquivo (%s - %s):\n%s\n", filePath, fileType, fileContent)
				}
			}
		}
		userInput = strings.ReplaceAll(userInput, "@file", "")
	}

	// Processar @command
	if strings.Contains(userInput, "@command") {
		commandStr, err := extractCommand(userInput)
		if err != nil {
			fmt.Println("\nErro ao processar o comando @command:", err)
		} else {
			// Executar o comando
			output, err := executeCommand(commandStr)
			if err != nil {
				fmt.Printf("\nErro ao executar o comando '%s': %v\n", commandStr, err)
			} else {
				// Adicionar a saída do comando ao contexto adicional
				additionalContext += fmt.Sprintf("\n📟 **Saída do Comando (%s):**\n```\n%s\n```\n", commandStr, output)
			}
		}
		userInput = removeCommandCommand(userInput)
	}

	return userInput, additionalContext
}

// Função auxiliar para extrair o comando após @command
func extractCommand(input string) (string, error) {
	parts := strings.Fields(input)
	for i, part := range parts {
		if part == "@command" && i+1 < len(parts) {
			// Retornar o comando completo após @command
			return strings.Join(parts[i+1:], " "), nil
		}
	}
	return "", fmt.Errorf("comando @command mal formatado. Uso correto: @command <comando>")
}

// Função auxiliar para remover o comando @command da entrada do usuário
func removeCommandCommand(input string) string {
	parts := strings.Fields(input)
	var filtered []string
	skip := false
	for _, part := range parts {
		if skip {
			// Ignorar todas as partes após @command
			continue
		}
		if part == "@command" {
			skip = true
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, " ")
}

// Função para executar o comando no terminal
func executeCommand(commandStr string) (string, error) {
	// Dividir o comando em nome e argumentos
	parts := strings.Fields(commandStr)
	if len(parts) == 0 {
		return "", fmt.Errorf("nenhum comando fornecido")
	}

	cmdName := parts[0]
	cmdArgs := parts[1:]

	//// Verificar se o comando é permitido
	//if !allowedCommands[cmdName] {
	//	return "", fmt.Errorf("comando '%s' não é permitido", cmdName)
	//}

	cmd := exec.Command(cmdName, cmdArgs...)
	var out strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, stderr.String())
	}

	return out.String(), nil
}

// Lista de comandos permitidos para execução
//var allowedCommands = map[string]bool{
//	"ls":    true,
//	"echo":  true,
//	"pwd":   true,
//	"cat":   true,
//	"mkdir": true,
//	"rm":    true,
//	"touch": true,
//	"grep":  true,
//	"find":  true,
//	"wc":    true,
//	// Adicione outros comandos seguros conforme necessário
//}

// Função auxiliar para extrair o caminho do arquivo do comando @file
func extractFilePath(input string) (string, error) {
	// Dividir a string em campos considerando aspas
	fields, err := parseFields(input)
	if err != nil {
		return "", err
	}

	for i, field := range fields {
		if field == "@file" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("comando @file mal formatado. Uso correto: @file <caminho_do_arquivo>")
}

// Função auxiliar para dividir a string em campos considerando aspas
func parseFields(input string) ([]string, error) {
	var fields []string
	var current strings.Builder
	inQuotes := false

	for i := 0; i < len(input); i++ {
		char := input[i]
		if char == '"' {
			inQuotes = !inQuotes
			continue
		}
		if char == ' ' && !inQuotes {
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteByte(char)
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}

	if inQuotes {
		return nil, fmt.Errorf("aspas não fechadas no comando")
	}

	return fields, nil
}

// Função auxiliar para remover o comando @file da entrada do usuário
func removeFileCommand(input string) string {
	fields := strings.Fields(input)
	var filtered []string
	skipNext := false
	for _, field := range fields {
		if skipNext {
			skipNext = false
			continue
		}
		if field == "@file" {
			skipNext = true
			continue
		}
		filtered = append(filtered, field)
	}
	return strings.Join(filtered, " ")
}

// Função auxiliar para detectar o tipo de arquivo com base na extensão
func detectFileType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".yaml", ".yml":
		return "YAML"
	case ".json":
		return "JSON"
	case ".tf":
		return "Terraform"
	case ".go":
		return "Go"
	case ".java":
		return "Java"
	case ".py":
		return "Python"
	default:
		return "Texto"
	}
}

// Função auxiliar para verificar se o tipo de arquivo é código
func isCodeFile(fileType string) bool {
	switch fileType {
	case "Go", "Java", "Python":
		return true
	default:
		return false
	}
}

// Função auxiliar para remover linhas vazias
func filterEmptyLines(lines []string) []string {
	var filtered []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

var thinkingWG sync.WaitGroup
var thinkingDone chan bool

func (cli *ChatCLI) showThinkingAnimation() {
	thinkingWG.Add(1)
	thinkingDone = make(chan bool)

	go func() {
		defer thinkingWG.Done()
		spinner := []string{"|", "/", "-", "\\"}
		i := 0
		for {
			select {
			case <-thinkingDone:
				fmt.Printf("\r\033[K") // Limpa a linha corretamente
				return
			default:
				fmt.Printf("\r%s está pensando... %s", cli.client.GetModelName(), spinner[i%len(spinner)])
				time.Sleep(100 * time.Millisecond)
				i++
			}
		}
	}()
}

func (cli *ChatCLI) stopThinkingAnimation() {
	thinkingDone <- true
	thinkingWG.Wait()
	fmt.Printf("\n") // Garante que a próxima saída comece em uma nova linha
}

func (cli *ChatCLI) renderMarkdown(input string) string {
	// Ajustar a largura para o tamanho do terminal
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(cli.terminalWidth),
	)
	out, err := renderer.Render(input)
	if err != nil {
		return input
	}
	return out
}

func (cli *ChatCLI) typewriterEffect(text string) {
	reader := strings.NewReader(text)
	inEscapeSequence := false

	for {
		char, _, err := reader.ReadRune()
		if err != nil {
			break // Fim do texto
		}

		// Verifica se é o início de uma sequência de escape
		if char == '\033' {
			inEscapeSequence = true
		}

		fmt.Printf("%c", char)

		// Verifica o final da sequência de escape
		if inEscapeSequence {
			if char == 'm' {
				inEscapeSequence = false
			}
			continue // Não aplica delay dentro da sequência de escape
		}

		time.Sleep(2 * time.Millisecond) // Ajuste o delay conforme desejado
	}
}

func (cli *ChatCLI) loadHistory() {
	historyFile := ".chatcli_history"
	f, err := os.Open(historyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return // Nenhum histórico para carregar
		}
		cli.logger.Warn("Não foi possível carregar o histórico:", zap.Error(err))
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		cli.commandHistory = append(cli.commandHistory, line)
		cli.line.AppendHistory(line)
	}

	if err := scanner.Err(); err != nil {
		cli.logger.Warn("Erro ao ler o histórico:", zap.Error(err))
	}
}

func (cli *ChatCLI) saveHistory() {
	historyFile := ".chatcli_history"
	f, err := os.OpenFile(historyFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		cli.logger.Warn("Não foi possível salvar o histórico:", zap.Error(err))
		return
	}
	defer f.Close()

	for _, cmd := range cli.commandHistory {
		fmt.Fprintln(f, cmd)
	}
}
