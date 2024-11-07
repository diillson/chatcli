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

// ChatCLI representa a interface de linha de comando do chat
type ChatCLI struct {
	client         llm.LLMClient
	manager        *llm.LLMManager
	logger         *zap.Logger
	provider       string
	model          string
	history        []models.Message
	line           *liner.State
	terminalWidth  int
	commandHistory []string
}

// NewChatCLI cria uma nova instância de ChatCLI
func NewChatCLI(manager *llm.LLMManager, logger *zap.Logger) (*ChatCLI, error) {
	provider := utils.GetEnvOrDefault("LLM_PROVIDER", "STACKSPOT")
	if provider == "STACKSPOT" {
		logger.Warn("LLM_PROVIDER não definido ou usando STACKSPOT como padrão")
	}

	var model string
	if provider == "OPENAI" {
		model = utils.GetEnvOrDefault("OPENAI_MODEL", "gpt-40-mini")
	}

	client, err := manager.GetClient(provider, model)
	if err != nil {
		fmt.Println("Erro ao obter o cliente LLM:", err)
		return nil, err
	}

	line := liner.NewLiner()
	line.SetCtrlCAborts(true) // Permite que Ctrl+C aborte o input

	// Obter a largura do terminal
	terminalWidth, _, err := utils.GetTerminalSize()
	if err != nil {
		logger.Warn("Não foi possível obter o tamanho do terminal, usando largura padrão de 80")
		terminalWidth = 80 // Largura padrão
	}

	cli := &ChatCLI{
		client:         client,
		manager:        manager,
		logger:         logger,
		provider:       provider,
		model:          model,
		history:        []models.Message{},
		line:           line,
		commandHistory: []string{},
		terminalWidth:  terminalWidth,
	}

	cli.loadHistory()

	return cli, nil
}

// Start inicia o loop principal do ChatCLI
func (cli *ChatCLI) Start(ctx context.Context) {
	defer cli.cleanup()

	fmt.Println("\n\nBem-vindo ao ChatCLI!")
	fmt.Printf("Você está conversando com %s (%s)\n", cli.client.GetModelName(), cli.provider)
	fmt.Println("Digite '/exit', 'exit', '/quit' ou 'quit' para sair.")
	fmt.Println("Digite '/switch' para trocar de provedor.")
	fmt.Println("Digite '/switch --slugname <slug>' para trocar o slug.")
	fmt.Println("Digite '/switch --tenantname <tenant-id>' para trocar o tenant.")
	fmt.Println("Use '@history', '@git', '@env', '@file <caminho_do_arquivo>' para adicionar contexto ao prompt.")
	fmt.Println("Use '@command <seu_comando>' para adicionar contexto ao prompt ou '@command -i <seu_comando>' para interativo.")
	fmt.Println("Ainda ficou com dúvidas? use '/help'.\n\n")

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nAplicação encerrada.")
			return
		default:
			input, err := cli.line.Prompt("Você: ")
			if err != nil {
				if err == liner.ErrPromptAborted {
					fmt.Println("\nEntrada abortada!")
					return
				}
				fmt.Println("Erro ao ler a entrada:", err)
				continue
			}

			input = strings.TrimSpace(input)

			// Verificar se o input é um comando direto do sistema
			if strings.Contains(input, "@command") {
				command := strings.TrimPrefix(input, "@command ")
				cli.executeDirectCommand(command)
				continue
			}

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

			// Adicionar a mensagem do usuário ao histórico
			cli.history = append(cli.history, models.Message{
				Role:    "user",
				Content: userInput + additionalContext,
			})

			// Exibir mensagem "Pensando..." com animação
			cli.showThinkingAnimation()

			// Criar um contexto com timeout
			responseCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			// Enviar o prompt para o LLM
			aiResponse, err := cli.client.SendPrompt(responseCtx, userInput+additionalContext, cli.history)

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
			cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.client.GetModelName(), renderedResponse), 2*time.Millisecond)
		}
	}
}

// cleanup realiza a limpeza de recursos ao encerrar o ChatCLI
func (cli *ChatCLI) cleanup() {
	cli.line.Close()
	cli.saveHistory()
	cli.logger.Sync()
}

func (cli *ChatCLI) handleCommand(userInput string) bool {
	switch {
	case userInput == "/exit" || userInput == "exit" || userInput == "/quit" || userInput == "quit":
		fmt.Println("Até mais!")
		return true
	case strings.HasPrefix(userInput, "/switch"):
		args := strings.Fields(userInput)
		var newSlugName, newTenantName string
		shouldUpdateToken := false

		// Processar os argumentos de --slugname e --tenantname sem impactar um ao outro
		for i := 1; i < len(args); i++ {
			if args[i] == "--slugname" && i+1 < len(args) {
				newSlugName = args[i+1]
				shouldUpdateToken = true
				i++
			} else if args[i] == "--tenantname" && i+1 < len(args) {
				newTenantName = args[i+1]
				shouldUpdateToken = true
				i++
			}
		}

		// Atualizar o TokenManager se houver mudanças
		if newSlugName != "" || newTenantName != "" {
			tokenManager, ok := cli.manager.GetTokenManager()
			if ok {
				currentSlugName, currentTenantName := tokenManager.GetSlugAndTenantName()

				if newSlugName != "" {
					fmt.Printf("Atualizando slugName de '%s' para '%s'\n", currentSlugName, newSlugName)
					currentSlugName = newSlugName
				}
				if newTenantName != "" {
					fmt.Printf("Atualizando tenantName de '%s' para '%s'\n", currentTenantName, newTenantName)
					currentTenantName = newTenantName
				}

				tokenManager.SetSlugAndTenantName(currentSlugName, currentTenantName)

				if shouldUpdateToken {
					fmt.Println("Atualizando token com os novos valores...")
					_, err := tokenManager.RefreshToken(context.Background())
					if err != nil {
						fmt.Println("Erro ao atualizar o token:", err)
					} else {
						fmt.Println("Token atualizado com sucesso!")
					}
				}
			} else {
				fmt.Println("TokenManager não configurado. O provedor STACKSPOT não está disponível.")
			}
			return false
		}

		// Se não houver argumentos, processar a troca de provedor
		fmt.Println("Provedores disponíveis:")
		availableProviders := cli.manager.GetAvailableProviders()
		for i, provider := range availableProviders {
			fmt.Printf("%d. %s\n", i+1, provider)
		}

		choiceInput, err := cli.line.Prompt("Selecione o provedor pelo número: ")
		if err != nil {
			if err == liner.ErrPromptAborted {
				fmt.Println("\nEntrada abortada!")
				return false
			}
			fmt.Println("Erro ao ler a escolha:", err)
			return false
		}
		choiceInput = strings.TrimSpace(choiceInput)

		choiceIndex := -1
		for i := range availableProviders {
			if fmt.Sprintf("%d", i+1) == choiceInput {
				choiceIndex = i
				break
			}
		}

		if choiceIndex == -1 {
			fmt.Println("Escolha inválida.")
			return false
		}

		newProvider := availableProviders[choiceIndex]
		var newModel string
		if newProvider == "OPENAI" {
			newModel = os.Getenv("OPENAI_MODEL")
			if newModel == "" {
				newModel = "gpt-3.5-turbo"
			}
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
	case userInput == "/help":
		fmt.Println("Comandos disponíveis:")
		fmt.Println("@history - Adiciona o histórico do shell ao contexto")
		fmt.Println("@git - Adiciona informações do Git ao contexto")
		fmt.Println("@env - Adiciona variáveis de ambiente ao contexto")
		fmt.Println("@file <caminho_do_arquivo> - Adiciona o conteúdo de um arquivo ao contexto")
		fmt.Println("@command <seu_comando> - para executar um comando diretamente no sistema")
		fmt.Println("@command -i <seu_comando> - para executar um comando interativo")
		fmt.Println("/exit ou /quit - Sai do ChatCLI")
		fmt.Println("/switch - Troca o provedor de LLM")
		fmt.Println("/switch --slugname <slug> --tenantname <tenant> - Define slug e tenant")
		return false
	default:
		fmt.Println("Comando desconhecido. Use /help para ver os comandos disponíveis.")
		return false
	}
}

func (cli *ChatCLI) processSpecialCommands(userInput string) (string, string) {
	userInput, historyContext := cli.processHistoryCommand(userInput)
	userInput, gitContext := cli.processGitCommand(userInput)
	userInput, envContext := cli.processEnvCommand(userInput)
	userInput, fileContext := cli.processFileCommand(userInput)

	additionalContext := historyContext + gitContext + envContext + fileContext
	return userInput, additionalContext
}

func (cli *ChatCLI) processHistoryCommand(userInput string) (string, string) {
	var additionalContext string
	if strings.Contains(userInput, "@history") {
		historyData, err := utils.GetShellHistory()
		if err != nil {
			fmt.Println("\nErro ao obter o histórico do shell:", err)
		} else {
			lines := strings.Split(historyData, "\n")
			lines = filterEmptyLines(lines) // Remove linhas vazias
			n := 30                         // Número de comandos recentes a incluir
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			// Enumerar os comandos a partir do total de comandos menos n
			startNumber := len(historyData) - len(lines) + 1
			formattedLines := make([]string, len(lines))
			for i, cmd := range lines {
				formattedLines[i] = fmt.Sprintf("%d: %s", startNumber+i, cmd)
			}
			limitedHistoryData := strings.Join(formattedLines, "\n")
			additionalContext += "\nHistórico do Shell (últimos 10 comandos):\n" + limitedHistoryData
		}
		userInput = strings.ReplaceAll(userInput, "@history", "")
	}
	return userInput, additionalContext
}

func (cli *ChatCLI) processGitCommand(userInput string) (string, string) {
	var additionalContext string
	if strings.Contains(userInput, "@git") {
		gitData, err := utils.GetGitInfo()
		if err != nil {
			fmt.Println("\nErro ao obter informações do Git:", err)
		} else {
			additionalContext += "\nInformações do Git:\n" + gitData
		}
		userInput = strings.ReplaceAll(userInput, "@git", "")
	}
	return userInput, additionalContext
}

func (cli *ChatCLI) processEnvCommand(userInput string) (string, string) {
	var additionalContext string
	if strings.Contains(userInput, "@env") {
		envData := utils.GetEnvVariables()
		additionalContext += "\nVariáveis de Ambiente:\n" + envData
		userInput = strings.ReplaceAll(userInput, "@env", "")
	}
	return userInput, additionalContext
}

func (cli *ChatCLI) processFileCommand(userInput string) (string, string) {
	var additionalContext string
	if strings.Contains(userInput, "@file") {
		// Extrair todos os caminhos de arquivos
		filePaths, err := extractAllFilePaths(userInput)
		if err != nil {
			fmt.Println("\nErro ao processar os comandos @file:", err)
		} else {
			for _, filePath := range filePaths {
				// Ler o conteúdo do arquivo
				fileContent, err := utils.ReadFileContent(filePath, 5000000)
				if err != nil {
					fmt.Printf("\nErro ao ler o arquivo '%s': %v\n", filePath, err)
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
		}
		// Remover todos os comandos @file da entrada do usuário
		userInput = removeAllFileCommands(userInput)
	}
	return userInput, additionalContext
}

func extractAllFilePaths(input string) ([]string, error) {
	var filePaths []string
	tokens, err := parseFields(input)
	if err != nil {
		return nil, err
	}

	skipNext := false
	for i, token := range tokens {
		if skipNext {
			skipNext = false
			continue
		}
		if token == "@file" {
			if i+1 < len(tokens) {
				filePaths = append(filePaths, tokens[i+1])
				skipNext = true
			} else {
				return nil, fmt.Errorf("comando @file sem caminho de arquivo")
			}
		}
	}
	return filePaths, nil
}

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

func removeAllFileCommands(input string) string {
	tokens, _ := parseFields(input) // Ignoramos o erro aqui porque já foi tratado
	var filtered []string
	skipNext := false
	for i := 0; i < len(tokens); i++ {
		if skipNext {
			skipNext = false
			continue
		}
		if tokens[i] == "@file" {
			skipNext = true
			continue
		}
		filtered = append(filtered, tokens[i])
	}
	return strings.Join(filtered, " ")
}

func detectFileType(filePath string) string {
	fileTypes := map[string]string{
		".yaml": "YAML",
		".yml":  "YAML",
		".json": "JSON",
		".tf":   "Terraform",
		".go":   "Go",
		".java": "Java",
		".py":   "Python",
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if fileType, exists := fileTypes[ext]; exists {
		return fileType
	}
	return "Texto"
}

func (cli *ChatCLI) executeDirectCommand(command string) {
	fmt.Println("Executando comando:", command)

	// Verificar se o comando é interativo
	isInteractive := false
	if strings.Contains(command, "-i ") || strings.Contains(command, "--interactive ") {
		isInteractive = true
		// Remover a flag do comando
		command = strings.TrimPrefix(command, "-i ")
		command = strings.TrimPrefix(command, "--interactive ")
	}

	// Obter o shell do usuário
	userShell := utils.GetUserShell()
	shellPath, err := exec.LookPath(userShell)
	if err != nil {
		cli.logger.Error("Erro ao localizar o shell", zap.Error(err))
		fmt.Println("Erro ao localizar o shell:", err)
		return
	}

	// Obter o caminho do arquivo de configuração do shell
	shellConfigPath := utils.GetShellConfigFilePath(userShell)
	if shellConfigPath == "" {
		fmt.Println("Não foi possível determinar o arquivo de configuração para o shell:", userShell)
		return
	}

	// Construir o comando para carregar o arquivo de configuração e executar o comando do usuário
	shellCommand := fmt.Sprintf("source %s && %s", shellConfigPath, command)

	cmd := exec.Command(shellPath, "-c", shellCommand)

	if isInteractive {
		// Conectar os streams de entrada, saída e erro do comando ao terminal
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		// Fechar o liner para liberar o terminal antes de executar o comando interativo
		cli.line.Close()

		// Executar o comando
		err = cmd.Run()

		// Reabrir o liner após a execução do comando
		cli.line = liner.NewLiner()
		cli.line.SetCtrlCAborts(true)
		cli.loadHistory()

		if err != nil {
			fmt.Println("Erro ao executar comando:", err)
		}

		// Informar que a saída não foi capturada
		fmt.Println("A saída do comando não pôde ser capturada para o histórico.")

		// Armazenar apenas o comando no histórico
		cli.history = append(cli.history, models.Message{
			Role:    "system",
			Content: fmt.Sprintf("Comando executado: %s", command),
		})
	} else {
		// Capturar a saída do comando
		output, err := cmd.CombinedOutput()

		// Exibir a saída
		fmt.Println("Saída do comando:", string(output))

		if err != nil {
			fmt.Println("Erro ao executar comando:", err)
		}

		// Armazenar a saída no histórico
		cli.history = append(cli.history, models.Message{
			Role:    "system",
			Content: fmt.Sprintf("Comando: %s\nSaída:\n%s", command, string(output)),
		})
	}

	// Adicionar o comando ao histórico do liner para persistir em .chatcli_history
	cli.line.AppendHistory(fmt.Sprintf("@command %s", command))
}

func isCodeFile(fileType string) bool {
	switch fileType {
	case "Go", "Java", "Python":
		return true
	default:
		return false
	}
}

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
	close(thinkingDone)
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

func (cli *ChatCLI) typewriterEffect(text string, delay time.Duration) {
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

		time.Sleep(delay) // Ajuste o delay conforme desejado
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
