package cli

import (
	"bufio"
	"context"
	"fmt"
	"github.com/joho/godotenv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

const (
	llmdDefault          = "STACKSPOT"
	defaultSlugName      = "testeai"
	defaultTenantName    = "zup"
	defaultClaudeAIModel = "claude-3-5-sonnet-20241022"
	defaultOpenAIModel   = "gpt-4o-mini"
)

// Logger interface para facilitar a testabilidade
type Logger interface {
	Info(msg string, fields ...zap.Field)
	Error(msg string, fields ...zap.Field)
	Warn(msg string, fields ...zap.Field)
	Sync() error
}

// Adicione a interface Liner
type Liner interface {
	Prompt(string) (string, error)
	Close() error
	SetCtrlCAborts(bool)
	AppendHistory(string)
	SetCompleter(liner.Completer)
}

// ChatCLI representa a interface de linha de comando do chat
type ChatCLI struct {
	client            llm.LLMClient
	manager           llm.LLMManager
	logger            *zap.Logger
	provider          string
	model             string
	history           []models.Message
	line              Liner
	terminalWidth     int
	commandHistory    []string
	historyManager    *HistoryManager
	animation         *AnimationManager
	commandHandler    *CommandHandler
	lastCommandOutput string
}

// reconfigureLogger reconfigura o logger após o reload das variáveis de ambiente
func (cli *ChatCLI) reconfigureLogger() {
	cli.logger.Info("Reconfigurando o logger...")

	// Sincronizar e fechar o logger atual
	if err := cli.logger.Sync(); err != nil {
		cli.logger.Error("Erro ao sincronizar o logger", zap.Error(err))
	}

	// Recriar o logger com base no novo valor da variável ENV
	newLogger, err := utils.InitializeLogger()
	if err != nil {
		cli.logger.Error("Erro ao reinicializar o logger", zap.Error(err))
		return
	}

	cli.logger = newLogger
	cli.logger.Info("Logger reconfigurado com sucesso")
}

// reloadConfiguration recarrega as variáveis de ambiente e reconfigura o LLMManager
func (cli *ChatCLI) reloadConfiguration() {
	fmt.Println("Recarregando configurações...")

	// Limpar variáveis de ambiente
	variablesToUnset := []string{
		"LOG_LEVEL", "ENV", "LLM_PROVIDER", "LOG_FILE", "OPENAI_API_KEY", "OPENAI_MODEL",
		"CLAUDEAI_API_KEY", "CLAUDEAI_MODEL", "CLIENT_ID", "CLIENT_SECRET", "SLUG_NAME", "TENANT_NAME",
	}

	for _, variable := range variablesToUnset {
		os.Unsetenv(variable)
	}

	// Recarregar o arquivo .env
	err := godotenv.Load()
	if err != nil && !os.IsNotExist(err) {
		cli.logger.Error("Erro ao carregar o arquivo .env", zap.Error(err))
	}

	cli.reconfigureLogger()

	// Recarregar a configuração do LLMManager
	utils.CheckEnvVariables(cli.logger, defaultSlugName, defaultTenantName)

	manager, err := llm.NewLLMManager(cli.logger, os.Getenv("SLUG_NAME"), os.Getenv("TENANT_NAME"))
	if err != nil {
		cli.logger.Error("Erro ao reconfigurar o LLMManager", zap.Error(err))
		return
	}

	cli.manager = manager
	cli.configureProviderAndModel()

	client, err := cli.manager.GetClient(cli.provider, cli.model)
	if err != nil {
		cli.logger.Error("Erro ao obter o cliente LLM", zap.Error(err))
		return
	}

	cli.client = client
	fmt.Println("Configurações recarregadas com sucesso!")
}

func (cli *ChatCLI) configureProviderAndModel() {
	cli.provider = os.Getenv("LLM_PROVIDER")
	if cli.provider == "" {
		cli.provider = "STACKSPOT" // Usar padrão se não estiver definido
	}
	if cli.provider == "OPENAI" {
		cli.model = os.Getenv("OPENAI_MODEL")
		if cli.model == "" {
			cli.model = defaultOpenAIModel
		}
	}
	if cli.provider == "CLAUDEAI" {
		cli.model = os.Getenv("CLAUDEAI_MODEL")
		if cli.model == "" {
			cli.model = defaultClaudeAIModel
		}
	}
}

// NewChatCLI cria uma nova instância de ChatCLI
func NewChatCLI(manager llm.LLMManager, logger *zap.Logger) (*ChatCLI, error) {
	cli := &ChatCLI{
		manager:        manager,
		logger:         logger,
		history:        make([]models.Message, 0),
		historyManager: NewHistoryManager(logger),
		animation:      NewAnimationManager(),
	}

	cli.configureProviderAndModel()

	client, err := manager.GetClient(cli.provider, cli.model)
	if err != nil {
		logger.Error("Erro ao obter o cliente LLM", zap.Error(err))
		return nil, err
	}

	line := liner.NewLiner()
	line.SetCtrlCAborts(true) // Permite que Ctrl+C aborte o input

	cli.client = client
	cli.line = line
	cli.history = []models.Message{}
	cli.commandHistory = []string{}
	cli.commandHandler = NewCommandHandler(cli)

	// Definir a função de autocompletar
	cli.line.SetCompleter(cli.completer)

	// Carregar o histórico
	history, err := cli.historyManager.LoadHistory()
	if err != nil {
		cli.logger.Error("Erro ao carregar o histórico", zap.Error(err))
	} else {
		cli.commandHistory = history
		for _, cmd := range history {
			cli.line.AppendHistory(cmd) // Adicionar o histórico ao liner
		}
	}

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
	fmt.Println("Digite '/reload' para recarregar as variáveis e reconfigurar o chatcli.")
	fmt.Println("Use '@history', '@git', '@env', '@file <caminho_do_arquivo>' para adicionar contexto ao prompt.")
	fmt.Println("Use '@command <seu_comando>' para adicionar contexto ao prompt ou '@command -i <seu_comando>' para interativo.")
	fmt.Println("Use '@command --ai <seu_comando>' para enviar o ouput para a AI de forma direta e '>' {maior} <seu contexto> para que a AI faça algo.")
	fmt.Printf("Ainda ficou com dúvidas? use '/help'.\n\n")
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
				cli.logger.Error("Erro ao ler a entrada", zap.Error(err))
				continue
			}

			input = strings.TrimSpace(input)

			if input != "" {
				cli.line.AppendHistory(input)
				cli.commandHistory = append(cli.commandHistory, input)
			}

			// Verificar se o input é um comando direto do sistema
			if strings.Contains(strings.ToLower(input), "@command ") {
				command := strings.TrimPrefix(input, "@command ")
				cli.executeDirectCommand(command)
				continue
			}

			if input == "" {
				continue
			}

			// Verificar por comandos
			if strings.HasPrefix(input, "/") || input == "exit" || input == "quit" {
				if cli.commandHandler.HandleCommand(input) {
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
			cli.animation.ShowThinkingAnimation(cli.client.GetModelName())

			// Criar um contexto com timeout
			responseCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			// Enviar o prompt para o LLM
			aiResponse, err := cli.client.SendPrompt(responseCtx, userInput+additionalContext, cli.history)

			// Parar a animação
			cli.animation.StopThinkingAnimation()

			if err != nil {
				cli.logger.Error("Erro do LLM", zap.Error(err))

				// Verifique se o erro contém o código de status 429 explicitamente
				if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "RATE_LIMIT_EXCEEDED") {
					fmt.Println("Limite de requisições excedido. Por favor, aguarde antes de tentar novamente.")
				} else {
					fmt.Println("Ocorreu um erro ao processar a requisição.")
				}

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
	//cli.historyManager.SaveHistory(cli.commandHistory) // Salvar o histórico
	if err := cli.historyManager.SaveHistory(cli.commandHistory); err != nil {
		cli.logger.Error("Erro ao salvar histórico", zap.Error(err))
	}
	cli.logger.Sync()
}

func (cli *ChatCLI) handleCommand(userInput string) bool {
	switch {
	case userInput == "/exit" || userInput == "exit" || userInput == "/quit" || userInput == "quit":
		fmt.Println("Até mais!")
		return true
	case userInput == "/reload":
		cli.reloadConfiguration()
		return false
	case strings.HasPrefix(userInput, "/switch"):
		cli.handleSwitchCommand(userInput)
		return false
	case userInput == "/help":
		cli.showHelp()
		return false
	default:
		fmt.Println("Comando desconhecido. Use /help para ver os comandos disponíveis.")
		return false
	}
}

func (cli *ChatCLI) handleSwitchCommand(userInput string) {
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
					cli.logger.Error("Erro ao atualizar o token", zap.Error(err))
				} else {
					fmt.Println("Token atualizado com sucesso!")
				}
			}
		} else {
			fmt.Println("TokenManager não configurado. O provedor STACKSPOT não está disponível.")
		}
		return
	}

	// Se não houver argumentos, processar a troca de provedor
	cli.switchProvider()
}

func (cli *ChatCLI) switchProvider() {
	fmt.Println("Provedores disponíveis:")
	availableProviders := cli.manager.GetAvailableProviders()
	for i, provider := range availableProviders {
		fmt.Printf("%d. %s\n", i+1, provider)
	}

	choiceInput, err := cli.line.Prompt("Selecione o provedor pelo número: ")
	if err != nil {
		if err == liner.ErrPromptAborted {
			fmt.Println("\nEntrada abortada!")
			return
		}
		cli.logger.Error("Erro ao ler a escolha", zap.Error(err))
		return
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
		return
	}

	newProvider := availableProviders[choiceIndex]
	var newModel string
	if newProvider == "OPENAI" {
		newModel = utils.GetEnvOrDefault("OPENAI_MODEL", defaultOpenAIModel)
	}

	if newProvider == "CLAUDEAI" {
		newModel = utils.GetEnvOrDefault("CLAUDEAI_MODEL", defaultClaudeAIModel)
	}

	newClient, err := cli.manager.GetClient(newProvider, newModel)
	if err != nil {
		cli.logger.Error("Erro ao trocar de provedor", zap.Error(err))
		return
	}

	cli.client = newClient
	cli.provider = newProvider
	cli.model = newModel
	cli.history = nil // Reiniciar o histórico da conversa
	fmt.Printf("Trocado para %s (%s)\n\n", cli.client.GetModelName(), cli.provider)
}

func (cli *ChatCLI) showHelp() {
	fmt.Println("Comandos disponíveis:")
	fmt.Println("@history - Adiciona o histórico do shell ao contexto")
	fmt.Println("@git - Adiciona informações do Git ao contexto")
	fmt.Println("@env - Adiciona variáveis de ambiente ao contexto")
	fmt.Println("@file <caminho_do_arquivo> - Adiciona o conteúdo de um arquivo ao contexto")
	fmt.Println("@command <seu_comando> - para executar um comando diretamente no sistema")
	fmt.Println("@command --ai <seu_comando> para enviar o ouput para a AI de forma direta e '>' {maior} <seu contexto> para que a AI faça algo.")
	fmt.Println("@command -i <seu_comando> - para executar um comando interativo")
	fmt.Println("/exit ou /quit - Sai do ChatCLI")
	fmt.Println("/switch - Troca o provedor de LLM")
	fmt.Println("/switch --slugname <slug> --tenantname <tenant> - Define slug e tenant")
	fmt.Printf("/reload para recarregar as variáveis e reconfigurar o chatcli.\n\n")
}

func (cli *ChatCLI) getConversationHistory() string {
	var historyBuilder strings.Builder
	for _, msg := range cli.history {
		role := "Usuário"
		if msg.Role == "assistant" {
			role = "Assistente"
		} else if msg.Role == "system" {
			role = "Sistema"
		}
		historyBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
	}
	return historyBuilder.String()
}

// processSpecialCommands processa comandos especiais como @history, @git, @env, @file
func (cli *ChatCLI) processSpecialCommands(userInput string) (string, string) {
	var additionalContext string

	// Processar comandos especiais
	userInput, context := cli.processHistoryCommand(userInput)
	additionalContext += context

	userInput, context = cli.processGitCommand(userInput)
	additionalContext += context

	userInput, context = cli.processEnvCommand(userInput)
	additionalContext += context

	userInput, context = cli.processFileCommand(userInput)
	additionalContext += context

	//userInput, context = cli.processCommandCommand(userInput)
	//additionalContext += context

	// Processar '>' como um operador para adicionar contexto
	if idx := strings.Index(userInput, ">"); idx != -1 {
		additionalContext += userInput[idx+1:] + "\n"
		userInput = userInput[:idx]
	}

	// Remover espaços extras
	userInput = strings.TrimSpace(userInput)

	return userInput, additionalContext
}

func removeCommandAndNormalizeSpaces(userInput, command string) string {
	regexPattern := fmt.Sprintf(`(?i)\s*%s\s*`, regexp.QuoteMeta(command))
	re := regexp.MustCompile(regexPattern)
	userInput = re.ReplaceAllString(userInput, " ")
	userInput = regexp.MustCompile(`\s+`).ReplaceAllString(userInput, " ")
	userInput = strings.TrimSpace(userInput)
	return userInput
}

// processHistoryCommand adiciona o histórico do shell ao contexto
func (cli *ChatCLI) processHistoryCommand(userInput string) (string, string) {
	var additionalContext string
	if strings.Contains(strings.ToLower(userInput), "@history") {
		historyData, err := utils.GetShellHistory()
		if err != nil {
			cli.logger.Error("Erro ao obter o histórico do shell", zap.Error(err))
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
			additionalContext += "\nHistórico do Shell (últimos 30 comandos):\n" + limitedHistoryData
		}
		userInput = removeCommandAndNormalizeSpaces(userInput, "@history")
	}
	return userInput, additionalContext
}

// processGitCommand adiciona informações do Git ao contexto
func (cli *ChatCLI) processGitCommand(userInput string) (string, string) {
	var additionalContext string
	if strings.Contains(strings.ToLower(userInput), "@git") {
		gitData, err := utils.GetGitInfo()
		if err != nil {
			cli.logger.Error("Erro ao obter informações do Git", zap.Error(err))
		} else {
			additionalContext += "\nInformações do Git:\n" + gitData
		}
		userInput = removeCommandAndNormalizeSpaces(userInput, "@git")
	}
	return userInput, additionalContext
}

// processEnvCommand adiciona as variáveis de ambiente ao contexto
func (cli *ChatCLI) processEnvCommand(userInput string) (string, string) {
	var additionalContext string
	if strings.Contains(strings.ToLower(userInput), "@env") {
		envData := utils.GetEnvVariables()
		additionalContext += "\nVariáveis de Ambiente:\n" + envData
		userInput = removeCommandAndNormalizeSpaces(userInput, "@env")
	}
	return userInput, additionalContext
}

// processFileCommand adiciona o conteúdo de um arquivo ao contexto
func (cli *ChatCLI) processFileCommand(userInput string) (string, string) {
	var additionalContext string
	if strings.Contains(strings.ToLower(userInput), "@file") {
		// Extrair todos os caminhos de arquivos
		filePaths, err := extractAllFilePaths(userInput)
		if err != nil {
			cli.logger.Error("Erro ao processar os comandos @file", zap.Error(err))
		} else {
			for _, filePath := range filePaths {
				// Ler o conteúdo do arquivo
				fileContent, err := utils.ReadFileContent(filePath, 5000000)
				if err != nil {
					cli.logger.Error(fmt.Sprintf("Erro ao ler o arquivo '%s'", filePath), zap.Error(err))
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

// Função auxiliar para extrair todos os caminhos de arquivos após @file
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

// Função auxiliar para analisar campos, considerando aspas
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

// removeAllFileCommands remove todos os comandos @file da entrada do usuário
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

// detectFileType detecta o tipo de arquivo com base na extensão
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

// isCodeFile verifica se o tipo de arquivo é código
func isCodeFile(fileType string) bool {
	switch fileType {
	case "Go", "Java", "Python":
		return true
	default:
		return false
	}
}

// filterEmptyLines remove linhas vazias
func filterEmptyLines(lines []string) []string {
	var filtered []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

// executeDirectCommand executa um comando diretamente no sistema
func (cli *ChatCLI) executeDirectCommand(command string) {
	fmt.Println("Executando comando:", command)

	// Verificar se o comando é interativo
	isInteractive := false
	if strings.HasPrefix(command, "-i ") || strings.HasPrefix(command, "--interactive ") {
		isInteractive = true
		// Remover a flag do comando
		command = strings.TrimPrefix(command, "-i ")
		command = strings.TrimPrefix(command, "--interactive ")
	}

	// Verificar se o comando contém a flag --send-ai e pipe |
	sendToAI := false
	var aiContext string
	if strings.Contains(command, "-ai") {
		sendToAI = true
		// Remover a flag do comando
		command = strings.Replace(command, "-ai", "", 1)
	}

	// Verificar se há um maior > no comando
	if strings.Contains(command, ">") {
		parts := strings.Split(command, ">")
		command = strings.TrimSpace(parts[0])
		aiContext = strings.TrimSpace(parts[1])
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
		cli.line.SetCompleter(cli.completer) // Reconfigurar o autocompletar

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
		cli.lastCommandOutput = ""
	} else {
		// Capturar a saída do comando
		output, err := cmd.CombinedOutput()

		// Exibir a saída
		fmt.Println("Saída do comando:\n\n", string(output))

		if err != nil {
			fmt.Println("Erro ao executar comando:", err)
		}

		// Armazenar a saída no histórico
		cli.history = append(cli.history, models.Message{
			Role:    "system",
			Content: fmt.Sprintf("Comando: %s\nSaída:\n%s", command, string(output)),
		})
		cli.lastCommandOutput = string(output)

		// se a flag --ai foi passada enviar o output para a IA
		if sendToAI {
			cli.sendOutputToAI(cli.lastCommandOutput, aiContext)
		}
	}

	// Adicionar o comando ao histórico do liner para persistir em .chatcli_history
	//cli.line.AppendHistory(fmt.Sprintf("@command %s", command))
}

// sendOutputToAI envia o output do comando para a IA com o contexto adicional
func (cli *ChatCLI) sendOutputToAI(output string, aiContext string) {
	fmt.Println("Enviando sáida do comando para a IA...")

	// Adicionar o output do comando ao histórico como mensagem do usuário
	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: fmt.Sprintf("Saída do comando:\n%s\n\nContexto: %s", output, aiContext),
	})
	// Exibir mensagem "Pensando..." com animação
	cli.animation.ShowThinkingAnimation(cli.client.GetModelName())

	//Criar um contexto com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	//Enviar o output e o contexto para a IA
	aiResponse, err := cli.client.SendPrompt(ctx, fmt.Sprintf("Saída do comando:\n%s\n\nContexto: %s", output, aiContext), cli.history)

	//parar a animação
	cli.animation.StopThinkingAnimation()

	if err != nil {
		cli.logger.Error("Erro do LLM", zap.Error(err))
		fmt.Println("Ocorreu um erro ao processar a requisição.")
		return
	}

	// Adicionar a resposta da IA ao histórico
	cli.history = append(cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})

	// Renderizar a resposta da IA
	renderResponse := cli.renderMarkdown(aiResponse)

	// Exibir a resposta da IA com efeito de digitação
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.client.GetModelName(), renderResponse), 2*time.Millisecond)
}

// loadHistory carrega o histórico do arquivo
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
		cli.line.AppendHistory(line) // Adicionar ao liner para navegação
	}

	if err := scanner.Err(); err != nil {
		cli.logger.Warn("Erro ao ler o histórico:", zap.Error(err))
	}
}

// saveHistory salva o histórico no arquivo
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

// Função de autocompletar
func (cli *ChatCLI) completer(line string) []string {
	var completions []string
	trimmedLine := strings.TrimSpace(line)

	commands := []string{"/exit", "/quit", "/switch", "/help", "/reload"}
	specialCommands := []string{"@history", "@git", "@env", "@file", "@command"}

	if strings.HasPrefix(trimmedLine, "/") {
		for _, cmd := range commands {
			if strings.HasPrefix(cmd, trimmedLine) {
				completions = append(completions, cmd)
			}
		}
		return completions
	}

	// Verifica comandos especiais
	if strings.HasPrefix(trimmedLine, "@") {
		for _, scmd := range specialCommands {
			if strings.HasPrefix(scmd, trimmedLine) {
				completions = append(completions, scmd)
			}
		}

		// Caso para @file
		if strings.HasPrefix(trimmedLine, "@file ") {
			prefix := strings.TrimPrefix(trimmedLine, "@file ")
			fileCompletions := cli.completeFilePath(prefix)
			// Reconstruir a linha com @file, mantendo o prefixo já digitado
			for _, fc := range fileCompletions {
				completions = append(completions, "@file "+fc)
			}
			return completions
		}

		// Caso para @command
		if strings.HasPrefix(trimmedLine, "@command ") {
			commandLine := strings.TrimPrefix(trimmedLine, "@command ")
			tokens := strings.Fields(commandLine)

			if len(tokens) == 0 {
				systemCmds := cli.completeSystemCommands("")
				for _, sc := range systemCmds {
					completions = append(completions, "@command "+sc)
				}
				return completions
			}

			if len(tokens) == 1 {
				lastToken := tokens[0]
				systemCmds := cli.completeSystemCommands(lastToken)
				for _, sc := range systemCmds {
					completions = append(completions, "@command "+sc)
				}
				return completions
			}

			// Mais de um token, último é path
			lastToken := tokens[len(tokens)-1]
			fileCompletions := cli.completeFilePath(lastToken)

			// Montar prefixo com @command + todos os tokens menos o último
			prefix := "@command " + strings.Join(tokens[:len(tokens)-1], " ") + " "
			for _, fc := range fileCompletions {
				completions = append(completions, prefix+fc)
			}
			return completions
		}

		return completions
	}

	// Caso não seja / nem @, tratamos o último token
	tokens := strings.Fields(line)
	var lastToken string
	var prefix string
	if len(tokens) > 0 {
		lastToken = tokens[len(tokens)-1]
		// prefixo é tudo antes do último token
		prefix = strings.Join(tokens[:len(tokens)-1], " ")
		if prefix != "" {
			prefix += " "
		}
	} else {
		lastToken = trimmedLine
	}

	// Histórico
	for _, historyCmd := range cli.commandHistory {
		if strings.HasPrefix(historyCmd, lastToken) {
			// Reconstituir a linha adicionando o completion do histórico
			completions = append(completions, prefix+historyCmd)
		}
	}

	// Caminhos de arquivos
	fileCompletions := cli.completeFilePath(lastToken)
	for _, fc := range fileCompletions {
		// Agora mantemos o que foi digitado antes do lastToken
		completions = append(completions, prefix+fc)
	}

	// Comandos do sistema
	commandCompletions := cli.completeSystemCommands(lastToken)
	for _, cc := range commandCompletions {
		completions = append(completions, prefix+cc)
	}

	return completions
}

// completeFilePath autocompleta caminhos de arquivos
func (cli *ChatCLI) completeFilePath(prefix string) []string {
	var completions []string

	dir, filePrefix := filepath.Split(prefix)
	if dir == "" {
		dir = "."
	}

	// Expandir "~" para o diretório home
	dir = os.ExpandEnv(dir)
	if strings.HasPrefix(dir, "~") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			dir = filepath.Join(homeDir, dir[1:])
		}
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return completions
	}

	for _, entry := range files {
		name := entry.Name()
		if strings.HasPrefix(name, filePrefix) {
			path := filepath.Join(dir, name)
			if entry.IsDir() {
				path += string(os.PathSeparator)
			}
			completions = append(completions, path)
		}
	}

	return completions
}

// completeSystemCommands autocompleta comandos do sistema
func (cli *ChatCLI) completeSystemCommands(prefix string) []string {
	var completions []string

	// Obter o PATH do sistema
	pathEnv := os.Getenv("PATH")
	paths := strings.Split(pathEnv, string(os.PathListSeparator))

	seen := make(map[string]bool)

	for _, pathDir := range paths {
		files, err := os.ReadDir(pathDir)
		if err != nil {
			continue
		}

		for _, file := range files {
			name := file.Name()
			if strings.HasPrefix(name, prefix) && !seen[name] {
				seen[name] = true
				completions = append(completions, name)
			}
		}
	}

	return completions
}

// Animação de "Pensando..."
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

// renderMarkdown renderiza o texto em Markdown
func (cli *ChatCLI) renderMarkdown(input string) string {
	// Ajustar a largura para o tamanho do terminal
	//width, _, err := utils.GetTerminalSize()
	//if err != nil || width <= 0 {
	//	width = 80 // valor padrão
	//}
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(0),
	)
	out, err := renderer.Render(input)
	if err != nil {
		return input
	}
	return out
}

// typewriterEffect exibe o texto com efeito de máquina de escrever
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
