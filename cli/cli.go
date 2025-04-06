package cli

import (
	"bufio"
	"context"
	"fmt"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	ragmanager "github.com/diillson/chatcli/rag/manager"
	"github.com/joho/godotenv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"

	"github.com/charmbracelet/glamour"
	"github.com/peterh/liner"
	"go.uber.org/zap"
)

// Logger interface para facilitar a testabilidade
type Logger interface {
	Info(msg string, fields ...zap.Field)
	Error(msg string, fields ...zap.Field)
	Warn(msg string, fields ...zap.Field)
	Sync() error
}

// FileChunk representa um pedaço do conteúdo de arquivos
type FileChunk struct {
	Index   int
	Total   int
	Content string
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
	client            client.LLMClient
	manager           manager.LLMManager
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
	fileChunks        []FileChunk            // Chunks pendentes para processamento
	failedChunks      []FileChunk            // Chunks que falharam no processamento
	lastFailedChunk   *FileChunk             // Referência ao último chunk que falhou
	ragManager        *ragmanager.RagManager // Novo campo para o gerenciador RAG
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
	utils.CheckEnvVariables(cli.logger)

	manager, err := manager.NewLLMManager(cli.logger, os.Getenv("SLUG_NAME"), os.Getenv("TENANT_NAME"))
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
		cli.provider = config.DefaultLLMProvider
	}

	// Verificar se deve usar APIs de sessão com base nas variáveis de ambiente
	if cli.provider == "OPENAI" {
		cli.model = os.Getenv("OPENAI_MODEL")
		if cli.model == "" {
			cli.model = config.DefaultOpenAIModel
		}

		// Verificar se devemos usar API de sessão
		useSessionEnv := os.Getenv("OPENAI_USE_SESSION")
		if useSessionEnv == "" {
			// Valor padrão com base na configuração
			os.Setenv("OPENAI_USE_SESSION", config.DefaultUseSessionAPI)
		}
	}

	if cli.provider == "CLAUDEAI" {
		cli.model = os.Getenv("CLAUDEAI_MODEL")
		if cli.model == "" {
			cli.model = config.DefaultClaudeAIModel
		}

		// Verificar se devemos usar API de sessão
		useSessionEnv := os.Getenv("CLAUDEAI_USE_SESSION")
		if useSessionEnv == "" {
			// Valor padrão com base na configuração
			os.Setenv("CLAUDEAI_USE_SESSION", config.DefaultUseSessionAPI)
		}
	}
}

// NewChatCLI cria uma nova instância de ChatCLI
func NewChatCLI(manager manager.LLMManager, logger *zap.Logger) (*ChatCLI, error) {
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
	fmt.Println("Para processamento em chunks, use '/nextchunk', '/retry', '/retryall' e '/skipchunk'.")
	fmt.Printf("Ainda ficou com dúvidas? use '/help'.\n\n")

	// Verificar se estamos usando a API de sessões
	var usingSessionAPI bool
	if cli.provider == "OPENAI" && os.Getenv("OPENAI_USE_SESSION") == "true" {
		usingSessionAPI = true
		fmt.Println("🔄 Usando API de sessões OpenAI para manter o contexto no provedor de LLM.")
	} else if cli.provider == "CLAUDEAI" && os.Getenv("CLAUDEAI_USE_SESSION") == "true" {
		usingSessionAPI = true
		fmt.Println("🔄 Usando API de sessões Claude para manter o contexto no proverdor de LLM.")
	}

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

			// Adicionar a mensagem do usuário ao histórico (apenas para cliente não-sessão)
			if !usingSessionAPI {
				cli.history = append(cli.history, models.Message{
					Role:    "user",
					Content: userInput + additionalContext,
				})
			}

			// Exibir mensagem "Pensando..." com animação
			cli.animation.ShowThinkingAnimation(cli.client.GetModelName())

			// Criar um contexto com timeout
			responseCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)

			// Enviar o prompt para o LLM
			var aiResponse string
			var promptErr error

			if usingSessionAPI {
				// Para APIs de sessão, apenas enviamos o prompt atual
				// O histórico é mantido no servidor
				aiResponse, promptErr = cli.client.SendPrompt(responseCtx, userInput+additionalContext, nil)
			} else {
				// Para APIs tradicionais, enviamos o histórico completo
				aiResponse, promptErr = cli.client.SendPrompt(responseCtx, userInput+additionalContext, cli.history)
			}

			cancel() // Cancelar o contexto após a resposta

			// Parar a animação
			cli.animation.StopThinkingAnimation()

			if promptErr != nil {
				cli.logger.Error("Erro do LLM", zap.Error(promptErr))

				// Verifique se o erro contém o código de status 429 explicitamente
				if strings.Contains(promptErr.Error(), "429") || strings.Contains(promptErr.Error(), "RATE_LIMIT_EXCEEDED") {
					fmt.Println("Limite de requisições excedido. Por favor, aguarde antes de tentar novamente.")
				} else {
					fmt.Println("Ocorreu um erro ao processar a requisição.")
				}

				continue
			}

			// Adicionar a resposta da IA ao histórico (apenas para cliente não-sessão)
			if !usingSessionAPI {
				cli.history = append(cli.history, models.Message{
					Role:    "assistant",
					Content: aiResponse,
				})
			}

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
		newModel = utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAIModel)

		// Perguntar se deseja usar a API de sessões
		useSessionInput, err := cli.line.Prompt("Deseja usar a API de sessões para manter o contexto no provedor de LLM? (s/n): ")
		if err == nil && (strings.ToLower(useSessionInput) == "s" || strings.ToLower(useSessionInput) == "sim") {
			os.Setenv("OPENAI_USE_SESSION", "true")
			fmt.Println("Usando API de sessões OpenAI.")
		} else {
			os.Setenv("OPENAI_USE_SESSION", "false")
			fmt.Println("Usando API tradicional OpenAI.")
		}
	}

	if newProvider == "CLAUDEAI" {
		newModel = utils.GetEnvOrDefault("CLAUDEAI_MODEL", config.DefaultClaudeAIModel)

		// Perguntar se deseja usar a API de sessões
		useSessionInput, err := cli.line.Prompt("Deseja usar a API de sessões para manter o contexto no provedor de LLM? (s/n): ")
		if err == nil && (strings.ToLower(useSessionInput) == "s" || strings.ToLower(useSessionInput) == "sim") {
			os.Setenv("CLAUDEAI_USE_SESSION", "true")
			fmt.Println("Usando API de sessões Claude.")
		} else {
			os.Setenv("CLAUDEAI_USE_SESSION", "false")
			fmt.Println("Usando API tradicional Claude.")
		}
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
	fmt.Println("@inrag <consulta> - Consulta o projeto indexado e adiciona ao contexto")
	fmt.Println("/exit ou /quit - Sai do ChatCLI")
	fmt.Println("/switch - Troca o provedor de LLM")
	fmt.Println("/switch --slugname <slug> --tenantname <tenant> - Define slug e tenant")
	fmt.Println("/reload - para recarregar as variáveis e reconfigurar o chatcli.")
	fmt.Println("/nextchunk - Processa o próximo chunk de código quando usando @file com chunks.")
	fmt.Println("/retry - Retenta o processamento do último chunk que falhou.")
	fmt.Println("/retryall - Retenta o processamento de todos os chunks que falharam.")
	fmt.Println("/skipchunk - Pula explicitamente um chunk, ignorando seu conteúdo.")
	fmt.Println("/rag index <caminho> - Indexa um projeto para consultas RAG.")
	fmt.Println("/rag query <consulta> - Consulta o projeto indexado.")
	fmt.Println("/rag clear - Limpa o índice RAG atual.")
	fmt.Println("/projeto <caminho> <pergunta> - Indexa e consulta um projeto em um único comando.")
	fmt.Printf("\n")
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

	userInput, context = cli.processRagCommand(userInput)
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

// Método para processar comandos RAG inline
func (cli *ChatCLI) processRagCommand(userInput string) (string, string) {
	var additionalContext string

	if strings.Contains(strings.ToLower(userInput), "@inrag") {
		// Extrair a consulta (tudo após @inrag)
		index := strings.Index(strings.ToLower(userInput), "@inrag")
		query := strings.TrimSpace(userInput[index+6:])

		// Verificar se o RAG Manager está inicializado
		if cli.ragManager == nil {
			additionalContext += "\n⚠️ RAG não inicializado. Use /rag index [caminho] primeiro.\n"
			userInput = strings.Replace(userInput, "@inrag"+query, "", 1)
			return userInput, additionalContext
		}

		// Recuperar contexto
		ctx := context.Background()
		retrievedContext, err := cli.ragManager.QueryProject(ctx, query)

		if err != nil {
			additionalContext += fmt.Sprintf("\n⚠️ Erro ao consultar RAG: %v\n", err)
		} else {
			additionalContext += "\n📂 CONTEXTO RAG RELEVANTE:\n" + retrievedContext
		}

		// Remover o comando @inrag da entrada do usuário
		userInput = strings.Replace(userInput, "@inrag"+query, "", 1)
	}

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

// processFileCommand adiciona o conteúdo de arquivos ou diretórios ao contexto
func (cli *ChatCLI) processFileCommand(userInput string) (string, string) {
	var additionalContext string

	if strings.Contains(strings.ToLower(userInput), "@file") {
		// Extração de modo e caminho
		mode := config.ModeFull // Modo padrão
		paths, options, err := extractFileCommandOptions(userInput)
		if err != nil {
			cli.logger.Error("Erro ao processar os comandos @file", zap.Error(err))
			return userInput, fmt.Sprintf("\nErro ao processar @file: %s\n", err.Error())
		}

		// Verificar se o modo foi especificado (ex: --mode=summary, --mode=chunked, etc.)
		if modeVal, ok := options["mode"]; ok {
			mode = modeVal
		}

		// Configurar estimador de tokens e obter limite máximo de tokens do LLM atual
		tokenEstimator := cli.getTokenEstimatorForCurrentLLM()
		maxTokens := cli.getMaxTokensForCurrentLLM()

		// Mostrar animação de “pensando” durante o processamento
		cli.animation.ShowThinkingAnimation("Analisando arquivos")

		// Processar cada caminho encontrado após @file
		for _, path := range paths {
			// Escolher a forma de processar (summary, chunked, smartChunk ou full)
			switch mode {
			case config.ModeSummary:
				summary, err := cli.processDirectorySummary(path, tokenEstimator, maxTokens)
				if err != nil {
					additionalContext += fmt.Sprintf("\nErro ao processar '%s': %s\n", path, err.Error())
				} else {
					additionalContext += summary
				}

			case config.ModeChunked:
				chunks, err := cli.processDirectoryChunked(path, tokenEstimator, maxTokens)
				if err != nil {
					additionalContext += fmt.Sprintf("\nErro ao processar '%s': %s\n", path, err.Error())
				} else {
					// Apenas o primeiro chunk é adicionado diretamente ao contexto.
					if len(chunks) > 0 {
						// ===== Nova seção de resumo =====
						totalChunks := len(chunks)
						var totalFiles int
						var totalSize int64

						// Contar estimativa de arquivos e tamanho total
						for _, chunk := range chunks {
							fileCount := strings.Count(chunk.Content, "📄 ARQUIVO")
							totalFiles += fileCount
							totalSize += int64(len(chunk.Content))
						}

						chunkSummary := fmt.Sprintf(
							"📊 PROJETO DIVIDIDO EM CHUNKS\n"+
								"=============================\n"+
								"▶️ Total de chunks: %d\n"+
								"▶️ Arquivos estimados: ~%d\n"+
								"▶️ Tamanho total: %.2f MB\n"+
								"▶️ Você está no chunk 1/%d\n"+
								"▶️ Use '/nextchunk' para avançar para o próximo chunk\n\n"+
								"=============================\n\n",
							totalChunks, totalFiles, float64(totalSize)/1024/1024, totalChunks,
						)

						// Exibir o resumo no console e aguardar 5 segundos para o usuário ler
						fmt.Println()
						fmt.Println(chunkSummary)
						fmt.Println("Aguarde 5 segundos antes de enviar o primeiro chunk...")

						// Usar timer em vez de aguardar input do usuário
						time.Sleep(5 * time.Second)

						fmt.Println("Enviando primeiro chunk para a LLM...")

						// Agora concatenamos ao contexto o resumo + o primeiro chunk
						additionalContext += chunkSummary + chunks[0].Content

						// Guardar os próximos chunks para o /nextchunk
						cli.fileChunks = chunks[1:]

						// Avisar o usuário sobre chunks pendentes
						if len(cli.fileChunks) > 0 {
							additionalContext += fmt.Sprintf(
								"\n\n⚠️ ATENÇÃO: Ainda existem %d chunks adicionais. Use /nextchunk quando terminar de analisar este chunk.\n",
								len(cli.fileChunks),
							)
						}
					}
				}

			case config.ModeSmartChunk:
				// Extrair a consulta do usuário (tudo o que vier após o @file + opções)
				query := extractUserQuery(userInput)
				relevantContent, err := cli.processDirectorySmart(path, query, tokenEstimator, maxTokens)
				if err != nil {
					additionalContext += fmt.Sprintf("\nErro ao processar '%s': %s\n", path, err.Error())
				} else {
					additionalContext += relevantContent
				}

			default: // ModeFull - comportamento atual (inclui todo o conteúdo relevante dentro de um limite)
				scanOptions := utils.DefaultDirectoryScanOptions(cli.logger)
				scanOptions.OnFileProcessed = func(info utils.FileInfo) {
					cli.animation.UpdateMessage(fmt.Sprintf("Processando %s", info.Path))
				}

				// Ajustar limite de tamanho com base em tokens disponíveis
				scanOptions.MaxTotalSize = estimateBytesFromTokens(maxTokens*3/4, tokenEstimator)

				files, err := utils.ProcessDirectory(path, scanOptions)
				if err != nil {
					cli.logger.Error(fmt.Sprintf("Erro ao processar '%s'", path), zap.Error(err))
					additionalContext += fmt.Sprintf("\nErro ao processar '%s': %s\n", path, err.Error())
					continue
				}

				if len(files) == 0 {
					additionalContext += fmt.Sprintf("\nNenhum arquivo relevante encontrado em '%s'\n", path)
					continue
				}

				formattedContent := utils.FormatDirectoryContent(files, scanOptions.MaxTotalSize)
				additionalContext += fmt.Sprintf("\n%s\n", formattedContent)
			}
		}

		// Parar animação de análise de arquivos
		cli.animation.StopThinkingAnimation()

		// Remover o comando @file do input original para não poluir o prompt final
		userInput = removeAllFileCommands(userInput)
	}

	return userInput, additionalContext

}

// extractFileCommandOptions extrai caminhos e opções do comando @file
func extractFileCommandOptions(input string) ([]string, map[string]string, error) {
	var paths []string
	options := make(map[string]string)

	// Regex para capturar @file com suas opções
	// Exemplo: @file --mode=summary ~/project
	re := regexp.MustCompile(`@file\s+((?:--\w+=\w+\s+)*)([\w~/.-]+)`)
	matches := re.FindAllStringSubmatch(input, -1)

	for _, match := range matches {
		if len(match) >= 3 {
			// Extrair opções
			optionStr := strings.TrimSpace(match[1])
			if optionStr != "" {
				optionParts := strings.Fields(optionStr)
				for _, part := range optionParts {
					if strings.HasPrefix(part, "--") {
						keyVal := strings.SplitN(part[2:], "=", 2)
						if len(keyVal) == 2 {
							options[keyVal[0]] = keyVal[1]
						}
					}
				}
			}

			// Adicionar caminho
			paths = append(paths, match[2])
		}
	}

	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("nenhum caminho válido encontrado após @file")
	}

	return paths, options, nil
}

// getTokenEstimatorForCurrentLLM retorna um estimador de tokens para o LLM atual
func (cli *ChatCLI) getTokenEstimatorForCurrentLLM() func(string) int {
	// Função padrão - estimativa conservadora
	return func(text string) int {
		// Aproximadamente 4 caracteres por token para a maioria dos modelos
		return len(text) / 4
	}
}

// getMaxTokensForCurrentLLM retorna o limite máximo de tokens para o LLM atual
func (cli *ChatCLI) getMaxTokensForCurrentLLM() int {
	switch cli.provider {
	case "OPENAI":
		if cli.model == "gpt-4o" {
			return 50000
		} else if cli.model == "gpt-4o-mini" {
			return 50000
		} else if strings.HasPrefix(cli.model, "gpt-4") {
			return 8192
		} else {
			return 8192 // gpt-3.5-turbo padrão
		}
	case "CLAUDEAI":
		if cli.model == "claude-3-5-sonnet-20241022" {
			return 50000
		} else if cli.model == "claude-3-haiku" {
			return 50000
		} else if cli.model == "claude-3-opus" {
			return 50000
		} else {
			return 50000 // valor conservador para outros modelos Claude
		}
	case "STACKSPOT":
		return 8192 // assumindo que usa gpt-4 no backend
	default:
		return 4096 // valor padrão conservador
	}
}

// estimateBytesFromTokens estima a quantidade de bytes baseada em tokens
func estimateBytesFromTokens(tokens int, estimator func(string) int) int64 {
	// Teste com uma string comum para calcular a razão bytes/token
	testString := strings.Repeat("typical code sample with various chars 12345!@#$%", 100)
	tokensInTest := estimator(testString)
	bytesPerToken := float64(len(testString)) / float64(tokensInTest)

	// Retorna bytes estimados com margem de segurança de 90%
	return int64(float64(tokens) * bytesPerToken * 0.9)
}

// processDirectorySummary gera um resumo estrutural do diretório sem conteúdo completo
func (cli *ChatCLI) processDirectorySummary(path string, tokenEstimator func(string) int, maxTokens int) (string, error) {
	path, err := utils.ExpandPath(path)
	if err != nil {
		return "", fmt.Errorf("erro ao expandir o caminho: %w", err)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("erro ao acessar o caminho: %w", err)
	}

	// Se for um arquivo único
	if !fileInfo.IsDir() {
		extension := filepath.Ext(path)
		fileType := utils.DetectFileType(path)
		size := fileInfo.Size()

		return fmt.Sprintf("📄 %s (%s, %.2f KB)\nTipo: %s\nTamanho: %d bytes\n",
			path, extension, float64(size)/1024, fileType, size), nil
	}

	// Se for um diretório, escanear a estrutura
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("📁 ESTRUTURA DO DIRETÓRIO: %s\n\n", path))

	// Mapeamentos para estatísticas
	fileTypes := make(map[string]int)
	var totalSize int64
	var totalFiles int
	var totalDirs int

	// Função recursiva para construir árvore de diretórios
	var buildTree func(dir string, prefix string, depth int) error
	buildTree = func(dir string, prefix string, depth int) error {
		if depth > 3 { // Limitar profundidade para evitar estruturas gigantes
			builder.WriteString(prefix + "...\n")
			return nil
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}

		for i, entry := range entries {
			// Verificar se estamos dentro do limite de tokens
			if tokenEstimator(builder.String()) > maxTokens/2 {
				builder.WriteString(prefix + "... (truncado por limite de tokens)\n")
				return nil
			}

			isLast := i == len(entries)-1

			var newPrefix string
			if isLast {
				builder.WriteString(prefix + "└── ")
				newPrefix = prefix + "    "
			} else {
				builder.WriteString(prefix + "├── ")
				newPrefix = prefix + "│   "
			}

			entryPath := filepath.Join(dir, entry.Name())

			if entry.IsDir() {
				// Verificar se é um diretório que normalmente seria ignorado
				if utils.ShouldSkipDir(entry.Name()) {
					builder.WriteString(entry.Name() + "/ (ignorado)\n")
					continue
				}

				totalDirs++
				builder.WriteString(entry.Name() + "/\n")

				// Recursivamente processar subdiretórios
				err := buildTree(entryPath, newPrefix, depth+1)
				if err != nil {
					return err
				}
			} else {
				totalFiles++
				fInfo, err := entry.Info()
				if err == nil {
					totalSize += fInfo.Size()
					fileExt := filepath.Ext(entry.Name())
					fileType := utils.DetectFileType(entry.Name())
					fileTypes[fileType]++

					// Adicionar informações do arquivo, incluindo a extensão
					builder.WriteString(fmt.Sprintf("%s (%s, %.1f KB, %s)\n",
						entry.Name(), fileExt, float64(fInfo.Size())/1024, fileType))
				} else {
					builder.WriteString(entry.Name() + "\n")
				}
			}
		}
		return nil
	}

	err = buildTree(path, "", 0)
	if err != nil {
		return "", fmt.Errorf("erro ao construir árvore de diretórios: %w", err)
	}

	// Adicionar estatísticas
	builder.WriteString("\n📊 ESTATÍSTICAS:\n")
	builder.WriteString(fmt.Sprintf("Total de Diretórios: %d\n", totalDirs))
	builder.WriteString(fmt.Sprintf("Total de Arquivos: %d\n", totalFiles))
	builder.WriteString(fmt.Sprintf("Tamanho Total: %.2f MB\n", float64(totalSize)/1024/1024))

	builder.WriteString("\n🔍 TIPOS DE ARQUIVO:\n")
	for fileType, count := range fileTypes {
		builder.WriteString(fmt.Sprintf("%s: %d arquivos\n", fileType, count))
	}

	return builder.String(), nil
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

// processDirectoryChunked processa um diretório e divide o conteúdo em chunks
func (cli *ChatCLI) processDirectoryChunked(path string, tokenEstimator func(string) int, maxTokens int) ([]FileChunk, error) {
	path, err := utils.ExpandPath(path)
	if err != nil {
		return nil, fmt.Errorf("erro ao expandir o caminho: %w", err)
	}

	// Configurar opções de processamento de diretório
	scanOptions := utils.DefaultDirectoryScanOptions(cli.logger)
	scanOptions.OnFileProcessed = func(info utils.FileInfo) {
		cli.animation.UpdateMessage(fmt.Sprintf("Processando %s", info.Path))
	}

	// Sem limite de tamanho, vamos coletar tudo e depois dividir
	files, err := utils.ProcessDirectory(path, scanOptions)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("nenhum arquivo relevante encontrado em '%s'", path)
	}

	cli.animation.UpdateMessage(fmt.Sprintf("Analisando e dividindo projeto em chunks (%d arquivos encontrados)", len(files)))

	// Dividir os arquivos em chunks
	var chunks []FileChunk
	var currentChunk strings.Builder
	filesInCurrentChunk := []utils.FileInfo{}

	// Função para finalizar o chunk atual
	finishCurrentChunk := func() {
		if currentChunk.Len() > 0 {
			formattedContent := utils.FormatDirectoryContent(filesInCurrentChunk, int64(currentChunk.Len()))
			chunks = append(chunks, FileChunk{
				Index:   len(chunks) + 1,
				Content: formattedContent,
			})

			// Resetar para o próximo chunk
			currentChunk.Reset()
			filesInCurrentChunk = []utils.FileInfo{}
		}
	}

	// Processar cada arquivo
	for _, file := range files {
		// Estimar tokens do conteúdo do arquivo
		fileTokens := tokenEstimator(file.Content)

		// Se o arquivo for maior que metade do limite, criar um chunk só para ele
		if fileTokens > maxTokens/2 {
			// Finalizar chunk anterior se existir
			finishCurrentChunk()

			// Criar um chunk separado só para este arquivo grande
			chunks = append(chunks, FileChunk{
				Index:   len(chunks) + 1,
				Content: utils.FormatDirectoryContent([]utils.FileInfo{file}, file.Size),
			})
			continue
		}

		// Verificar se adicionar este arquivo excederia o limite de tokens
		currentTokens := tokenEstimator(currentChunk.String())
		if currentTokens+fileTokens > maxTokens*3/4 {
			finishCurrentChunk()
		}

		// Adicionar arquivo ao chunk atual
		currentChunk.WriteString(file.Content)
		filesInCurrentChunk = append(filesInCurrentChunk, file)
	}

	// Finalizar o último chunk se necessário
	finishCurrentChunk()

	// Atualizar total em cada chunk
	for i := range chunks {
		chunks[i].Total = len(chunks)
	}

	return chunks, nil
}

// Método para processar o comando de projeto
func (cli *ChatCLI) handleProjectCommand(userInput string) {
	parts := strings.Fields(userInput)
	if len(parts) < 3 {
		fmt.Println("Uso: /projeto [caminho] [pergunta/tarefa]")
		fmt.Println("Exemplo: /projeto ~/meu-projeto 'Explique a arquitetura deste projeto'")
		return
	}

	projectPath := parts[1]
	query := strings.Join(parts[2:], " ")

	// Inicializar e indexar com RAG
	ctx := context.Background()

	// Usar a chave API do provedor atual
	var apiKey string
	switch cli.provider {
	case "OPENAI":
		apiKey = os.Getenv("OPENAI_API_KEY")
		fmt.Println("🔍 Inicializando RAG com embeddings OpenAI (alta qualidade)")
	case "CLAUDEAI":
		apiKey = os.Getenv("CLAUDEAI_API_KEY")

		// Verificar se temos acesso à API OpenAI para melhor qualidade
		if openaiKey := os.Getenv("OPENAI_API_KEY"); openaiKey != "" {
			fmt.Println("🔍 Inicializando RAG com embeddings OpenAI (alta qualidade)")
			apiKey = openaiKey // Usar OpenAI para embeddings com Claude
		} else {
			fmt.Println("⚠️ API OpenAI não disponível - usando busca híbrida (semântica + palavras-chave)")
			fmt.Println("💡 Dica: para melhores resultados, defina OPENAI_API_KEY no seu .env")
		}
	default:
		fmt.Println("❌ RAG suporta apenas os provedores OpenAI e Claude. Use /switch para mudar.")
		return
	}

	cli.ragManager = ragmanager.NewRagManager(cli.logger, apiKey, cli.provider)

	// Mostrar animação enquanto indexa
	cli.animation.ShowThinkingAnimation("Indexando projeto")
	err := cli.ragManager.IndexProject(ctx, projectPath)
	cli.animation.StopThinkingAnimation()

	if err != nil {
		fmt.Printf("Erro ao indexar projeto: %v\n", err)
		return
	}

	fmt.Println("Projeto indexado com sucesso! Consultando...")

	// Consultar o índice
	cli.animation.ShowThinkingAnimation("Recuperando contexto relevante")
	retrievedContext, err := cli.ragManager.QueryProject(ctx, query)
	cli.animation.StopThinkingAnimation()

	if err != nil {
		fmt.Printf("Erro ao consultar projeto: %v\n", err)
		return
	}

	// Preparar o prompt final com o contexto e a consulta
	prompt := fmt.Sprintf("Consulta: %s\n\n%s\n\nCom base no contexto do código acima, responda à consulta de forma detalhada.",
		query, retrievedContext)

	// Enviar para o LLM
	cli.animation.ShowThinkingAnimation(cli.client.GetModelName())
	aiResponse, err := cli.client.SendPrompt(ctx, prompt, []models.Message{})
	cli.animation.StopThinkingAnimation()

	if err != nil {
		fmt.Printf("Erro ao obter resposta: %v\n", err)
		return
	}

	// Exibir a resposta da IA
	renderedResponse := cli.renderMarkdown(aiResponse)
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.client.GetModelName(), renderedResponse), 2*time.Millisecond)

	// Adicionar ao histórico para contexto
	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: prompt,
	})

	cli.history = append(cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})
}

// handleNextChunk processa o próximo chunk de arquivos com tratamento de falhas
func (cli *ChatCLI) handleNextChunk() bool {
	if len(cli.fileChunks) == 0 {
		if len(cli.failedChunks) > 0 {
			fmt.Printf("Não há mais chunks pendentes, mas existem %d chunks com falha. Use /retry para retentar o último chunk com falha ou /retryall para retentar todos.\n", len(cli.failedChunks))
		} else {
			fmt.Println("Não há mais chunks de arquivos disponíveis.")
		}
		return false
	}

	// Obter o próximo chunk
	nextChunk := cli.fileChunks[0]

	// Não remova o chunk da fila até que tenhamos sucesso
	totalChunks := nextChunk.Total
	currentChunk := nextChunk.Index
	remainingChunks := len(cli.fileChunks) - 1

	fmt.Printf("Enviando chunk %d de %d... (%d restantes após este)\n",
		currentChunk, totalChunks, remainingChunks)

	// Adicionar resumo de progresso
	progressInfo := fmt.Sprintf("📊 PROGRESSO: Chunk %d/%d\n"+
		"=============================\n"+
		"▶️ %d chunks já processados\n"+
		"▶️ %d chunks pendentes\n"+
		"▶️ %d chunks com falha\n"+
		"▶️ Use '/nextchunk' para avançar ou '/retry' se ocorrer falha\n\n"+
		"=============================\n\n",
		currentChunk, totalChunks, currentChunk-1, remainingChunks, len(cli.failedChunks))

	// Preparar a mensagem
	prompt := fmt.Sprintf("Este é o chunk %d/%d do código que solicitei anteriormente. Por favor continue a análise:",
		currentChunk, totalChunks)

	// Adicionar a mensagem ao histórico
	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: prompt + "\n\n" + progressInfo + nextChunk.Content,
	})

	// Mostrar animação "Pensando..."
	cli.animation.ShowThinkingAnimation(cli.client.GetModelName())

	// Criar contexto com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Enviar o prompt para o LLM
	aiResponse, err := cli.client.SendPrompt(ctx, prompt+"\n\n"+nextChunk.Content, cli.history)

	// Parar a animação
	cli.animation.StopThinkingAnimation()

	if err != nil {
		cli.logger.Error("Erro ao processar chunk com a LLM",
			zap.Int("chunkIndex", nextChunk.Index),
			zap.Int("totalChunks", nextChunk.Total),
			zap.Error(err))

		// Armazenar o chunk que falhou
		cli.lastFailedChunk = &cli.fileChunks[0]
		cli.failedChunks = append(cli.failedChunks, cli.fileChunks[0])

		// Remover da fila principal
		cli.fileChunks = cli.fileChunks[1:]

		// Informar ao usuário
		fmt.Printf("\n⚠️ Erro ao processar o chunk %d/%d: %s\n",
			currentChunk, totalChunks, err.Error())
		fmt.Println("O chunk foi movido para a fila de chunks com falha.")
		fmt.Println("Use /retry para tentar novamente este chunk ou /nextchunk para continuar com o próximo.")

		return false
	}

	// Se chegou aqui, o processamento foi bem-sucedido

	// Adicionar a resposta ao histórico
	cli.history = append(cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})

	// Renderizar e mostrar a resposta
	renderedResponse := cli.renderMarkdown(aiResponse)
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.client.GetModelName(), renderedResponse), 2*time.Millisecond)

	// Remover o chunk da fila apenas após processamento bem-sucedido
	cli.fileChunks = cli.fileChunks[1:]

	// Informar sobre chunks restantes
	if len(cli.fileChunks) > 0 || len(cli.failedChunks) > 0 {
		fmt.Printf("\nStatus dos chunks:\n")

		if len(cli.fileChunks) > 0 {
			fmt.Printf("- %d chunks pendentes. Use /nextchunk para continuar.\n", len(cli.fileChunks))
		}

		if len(cli.failedChunks) > 0 {
			fmt.Printf("- %d chunks com falha. Use /retry ou /retryall para reprocessá-los.\n", len(cli.failedChunks))
		}
	} else {
		fmt.Println("\nTodos os chunks foram processados com sucesso.")
	}

	return false
}

// Novo método para reprocessar o último chunk que falhou
func (cli *ChatCLI) handleRetryLastChunk() bool {
	if cli.lastFailedChunk == nil || len(cli.failedChunks) == 0 {
		fmt.Println("Não há chunks com falha para reprocessar.")
		return false
	}

	// Obter o último chunk com falha
	lastFailedIndex := len(cli.failedChunks) - 1
	chunk := cli.failedChunks[lastFailedIndex]

	// Remover da lista de falhas
	cli.failedChunks = cli.failedChunks[:lastFailedIndex]

	fmt.Printf("Retentando chunk %d/%d que falhou anteriormente...\n", chunk.Index, chunk.Total)

	// Preparar resumo de progresso
	progressInfo := fmt.Sprintf("📊 NOVA TENTATIVA: Chunk %d/%d\n"+
		"=============================\n"+
		"▶️ Retentando chunk que falhou anteriormente\n"+
		"▶️ %d chunks pendentes\n"+
		"▶️ %d outros chunks com falha\n"+
		"=============================\n\n",
		chunk.Index, chunk.Total, len(cli.fileChunks), len(cli.failedChunks))

	// Preparar a mensagem
	prompt := fmt.Sprintf("Este é o chunk %d/%d do código (nova tentativa após falha). Por favor continue a análise:",
		chunk.Index, chunk.Total)

	// Adicionar a mensagem ao histórico
	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: prompt + "\n\n" + progressInfo + chunk.Content,
	})

	// Mostrar animação "Pensando..."
	cli.animation.ShowThinkingAnimation(cli.client.GetModelName())

	// Criar contexto com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Enviar o prompt para o LLM
	aiResponse, err := cli.client.SendPrompt(ctx, prompt+"\n\n"+chunk.Content, cli.history)

	// Parar a animação
	cli.animation.StopThinkingAnimation()

	if err != nil {
		cli.logger.Error("Erro ao reprocessar chunk com a LLM",
			zap.Int("chunkIndex", chunk.Index),
			zap.Int("totalChunks", chunk.Total),
			zap.Error(err))

		// Devolver o chunk para a lista de falhas
		cli.failedChunks = append(cli.failedChunks, chunk)

		// Informar ao usuário
		fmt.Printf("\n⚠️ Erro ao reprocessar o chunk %d/%d: %s\n",
			chunk.Index, chunk.Total, err.Error())
		fmt.Println("O chunk permanece na fila de chunks com falha.")

		return false
	}

	// Adicionar a resposta ao histórico
	cli.history = append(cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})

	// Renderizar e mostrar a resposta
	renderedResponse := cli.renderMarkdown(aiResponse)
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.client.GetModelName(), renderedResponse), 2*time.Millisecond)

	// Atualizar o lastFailedChunk
	if len(cli.failedChunks) > 0 {
		lastIndex := len(cli.failedChunks) - 1
		cli.lastFailedChunk = &cli.failedChunks[lastIndex]
	} else {
		cli.lastFailedChunk = nil
	}

	fmt.Println("\nChunk reprocessado com sucesso!")

	// Informar sobre o status dos chunks
	cli.printChunkStatus()

	return false
}

// Método para reprocessar todos os chunks com falha
func (cli *ChatCLI) handleRetryAllChunks() bool {
	if len(cli.failedChunks) == 0 {
		fmt.Println("Não há chunks com falha para reprocessar.")
		return false
	}

	fmt.Printf("Retentando todos os %d chunks com falha...\n", len(cli.failedChunks))

	// Mover todos os chunks com falha para a fila de chunks pendentes
	cli.fileChunks = append(cli.failedChunks, cli.fileChunks...)
	cli.failedChunks = []FileChunk{}
	cli.lastFailedChunk = nil

	// Iniciar o processamento do primeiro chunk
	return cli.handleNextChunk()
}

// Método para pular explicitamente um chunk
func (cli *ChatCLI) handleSkipChunk() bool {
	if len(cli.fileChunks) == 0 {
		fmt.Println("Não há chunks pendentes para pular.")
		return false
	}

	skippedChunk := cli.fileChunks[0]
	cli.fileChunks = cli.fileChunks[1:]

	fmt.Printf("Pulando chunk %d/%d...\n", skippedChunk.Index, skippedChunk.Total)

	// Informar sobre o status dos chunks
	cli.printChunkStatus()

	return false
}

// Método auxiliar para imprimir o status dos chunks
func (cli *ChatCLI) printChunkStatus() {
	fmt.Printf("\nStatus dos chunks:\n")

	if len(cli.fileChunks) > 0 {
		fmt.Printf("- %d chunks pendentes. Use /nextchunk para continuar.\n", len(cli.fileChunks))
	} else {
		fmt.Println("- Não há chunks pendentes.")
	}

	if len(cli.failedChunks) > 0 {
		fmt.Printf("- %d chunks com falha. Use /retry ou /retryall para reprocessá-los.\n", len(cli.failedChunks))
	} else {
		fmt.Println("- Não há chunks com falha.")
	}
}

// extractUserQuery extrai a consulta do usuário do input
func extractUserQuery(input string) string {
	// Remover o comando @file e qualquer opção
	cleaned := removeAllFileCommands(input)
	return strings.TrimSpace(cleaned)
}

func (cli *ChatCLI) handleRagCommand(userInput string) {
	parts := strings.Fields(userInput)
	if len(parts) < 2 {
		fmt.Println("Uso: /rag [index|query|clear] [caminho|consulta]")
		return
	}

	// Inicializa o RAG Manager se ainda não estiver inicializado
	if cli.ragManager == nil {
		// Usar a chave API do provedor atual
		var apiKey string
		switch cli.provider {
		case "OPENAI":
			apiKey = os.Getenv("OPENAI_API_KEY")
			fmt.Println("🔍 Inicializando RAG com embeddings OpenAI (alta qualidade)")
		case "CLAUDEAI":
			apiKey = os.Getenv("CLAUDEAI_API_KEY")

			// Verificar se temos acesso à API OpenAI para melhor qualidade
			if openaiKey := os.Getenv("OPENAI_API_KEY"); openaiKey != "" {
				fmt.Println("🔍 Inicializando RAG com embeddings OpenAI (alta qualidade)")
				apiKey = openaiKey // Usar OpenAI para embeddings com Claude
			} else {
				fmt.Println("⚠️ API OpenAI não disponível - usando busca híbrida (semântica + palavras-chave)")
				fmt.Println("💡 Dica: para melhores resultados, defina OPENAI_API_KEY no seu .env")
			}
		default:
			fmt.Println("❌ RAG suporta apenas os provedores OpenAI e Claude. Use /switch para mudar.")
			return
		}

		cli.ragManager = ragmanager.NewRagManager(cli.logger, apiKey, cli.provider)
	}

	ctx := context.Background()
	switch parts[1] {
	case "index":
		if len(parts) < 3 {
			fmt.Println("Uso: /rag index [caminho do projeto]")
			return
		}

		projectPath := parts[2]

		// Mostrar animação enquanto indexa
		cli.animation.ShowThinkingAnimation("Indexando projeto")

		err := cli.ragManager.IndexProject(ctx, projectPath)
		cli.animation.StopThinkingAnimation()

		if err != nil {
			fmt.Printf("Erro ao indexar projeto: %v\n", err)
			return
		}

		fmt.Println("Projeto indexado com sucesso! Use /rag query [sua pergunta] para consultar.")

	case "query":
		if len(parts) < 3 {
			fmt.Println("Uso: /rag query [sua pergunta sobre o código]")
			return
		}

		query := strings.Join(parts[2:], " ")

		// Mostrar animação enquanto consulta
		cli.animation.ShowThinkingAnimation("Consultando projeto")

		retrievedContext, err := cli.ragManager.QueryProject(ctx, query)
		cli.animation.StopThinkingAnimation()

		if err != nil {
			fmt.Printf("Erro ao consultar projeto: %v\n", err)
			return
		}

		// Enviar a consulta com o contexto recuperado para o LLM
		prompt := fmt.Sprintf("Consulta: %s\n\n%s\n\nBaseado no contexto acima, responda à consulta.",
			query, retrievedContext)

		cli.animation.ShowThinkingAnimation(cli.client.GetModelName())

		aiResponse, err := cli.client.SendPrompt(ctx, prompt, []models.Message{})
		cli.animation.StopThinkingAnimation()

		if err != nil {
			fmt.Printf("Erro ao obter resposta: %v\n", err)
			return
		}

		// Exibir a resposta da IA
		renderedResponse := cli.renderMarkdown(aiResponse)
		cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.client.GetModelName(), renderedResponse), 2*time.Millisecond)

	case "clear":
		err := cli.ragManager.ClearIndex()
		if err != nil {
			fmt.Printf("Erro ao limpar índice: %v\n", err)
			return
		}
		fmt.Println("Índice RAG limpo com sucesso.")

	default:
		fmt.Println("Comando RAG desconhecido. Opções: index, query, clear")
	}
}

// processDirectorySmart processa um diretório e seleciona partes relevantes para a consulta
func (cli *ChatCLI) processDirectorySmart(path string, query string, tokenEstimator func(string) int, maxTokens int) (string, error) {
	path, err := utils.ExpandPath(path)
	if err != nil {
		return "", fmt.Errorf("erro ao expandir o caminho: %w", err)
	}

	// Se a consulta estiver vazia, usar o modo de resumo
	if query == "" {
		return cli.processDirectorySummary(path, tokenEstimator, maxTokens)
	}

	// Configurar opções de processamento de diretório
	scanOptions := utils.DefaultDirectoryScanOptions(cli.logger)
	scanOptions.OnFileProcessed = func(info utils.FileInfo) {
		cli.animation.UpdateMessage(fmt.Sprintf("Analisando %s", info.Path))
	}

	files, err := utils.ProcessDirectory(path, scanOptions)
	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return "", fmt.Errorf("nenhum arquivo relevante encontrado em '%s'", path)
	}

	// Avaliar relevância de cada arquivo para a consulta
	type ScoredFile struct {
		File  utils.FileInfo
		Score float64
	}

	var scoredFiles []ScoredFile

	// Termos importantes da consulta
	queryTerms := strings.Fields(strings.ToLower(query))

	for _, file := range files {
		// Cálculo simples de relevância baseado em correspondência de palavras-chave
		fileContent := strings.ToLower(file.Content)
		fileName := strings.ToLower(filepath.Base(file.Path))

		var score float64

		// Pontuação por nome de arquivo
		for _, term := range queryTerms {
			if strings.Contains(fileName, term) {
				score += 5.0 // Maior peso para correspondência no nome
			}
		}

		// Pontuação por conteúdo
		for _, term := range queryTerms {
			count := strings.Count(fileContent, term)
			score += float64(count) * 0.5
		}

		// Normalizar pela extensão do arquivo (favorecendo arquivos de código)
		ext := filepath.Ext(file.Path)
		if ext == ".go" || ext == ".java" || ext == ".py" || ext == ".js" || ext == ".ts" {
			score *= 1.2 // Bonus para arquivos de código
		}

		// Penalizar arquivos muito grandes
		if file.Size > 1024*50 { // Maior que 50KB
			score *= 0.9
		}

		scoredFiles = append(scoredFiles, ScoredFile{File: file, Score: score})
	}

	// Ordenar arquivos por relevância
	sort.Slice(scoredFiles, func(i, j int) bool {
		return scoredFiles[i].Score > scoredFiles[j].Score
	})

	// Selecionar os arquivos mais relevantes até atingir o limite de tokens
	var selectedFiles []utils.FileInfo
	var currentTokens int
	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("📁 ARQUIVOS MAIS RELEVANTES PARA: \"%s\"\n\n", query))

	for _, scored := range scoredFiles {
		fileTokens := tokenEstimator(scored.File.Content)

		if currentTokens+fileTokens > maxTokens*3/4 {
			// Verificar se podemos incluir pelo menos um arquivo
			if len(selectedFiles) == 0 && fileTokens < maxTokens*3/4 {
				selectedFiles = append(selectedFiles, scored.File)
				currentTokens += fileTokens
				builder.WriteString(fmt.Sprintf("📄 %s (Pontuação de relevância: %.2f)\n",
					scored.File.Path, scored.Score))
			} else {
				break
			}
		} else {
			selectedFiles = append(selectedFiles, scored.File)
			currentTokens += fileTokens
			builder.WriteString(fmt.Sprintf("📄 %s (Pontuação de relevância: %.2f)\n",
				scored.File.Path, scored.Score))
		}
	}

	builder.WriteString(fmt.Sprintf("\n🔍 Foram selecionados %d/%d arquivos mais relevantes para sua consulta.\n\n",
		len(selectedFiles), len(files)))

	// Se não houver arquivos relevantes, retornar resumo
	if len(selectedFiles) == 0 {
		builder.WriteString("Nenhum arquivo teve pontuação suficiente. Aqui está um resumo estrutural:\n\n")
		summary, err := cli.processDirectorySummary(path, tokenEstimator, maxTokens)
		if err != nil {
			return builder.String(), nil
		}
		builder.WriteString(summary)
		return builder.String(), nil
	}

	// Formatar o conteúdo selecionado
	formattedContent := utils.FormatDirectoryContent(selectedFiles, int64(currentTokens))
	builder.WriteString(formattedContent)

	return builder.String(), nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
