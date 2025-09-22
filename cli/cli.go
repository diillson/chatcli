/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/llm/openai_assistant"
	"github.com/diillson/chatcli/version"
	"github.com/joho/godotenv"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"

	"github.com/charmbracelet/glamour"
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

type InteractionState int

const (
	StateNormal InteractionState = iota
	StateSwitchingProvider
	StateProcessing
	StateAgentMode
)

var agentModeRequest = errors.New("request to enter agent mode")
var errExitRequest = errors.New("request to exit")
var commandFlags = map[string]map[string][]prompt.Suggest{
	"@file": {
		"--mode": {
			{Text: "full", Description: "Processa o conteúdo completo (padrão, trunca se necessário)"},
			{Text: "summary", Description: "Gera resumo estrutural (árvore de arquivos, tamanhos, sem conteúdo)"},
			{Text: "chunked", Description: "Divide grandes projetos em pedaços gerenciáveis (use /nextchunk para prosseguir)"},
			{Text: "smart", Description: "Seleciona arquivos relevantes com base no seu prompt (IA decide)"},
		},
	},
	"@command": {
		"-i":   {},
		"--ai": {},
	},
	"/switch": {
		"--model":      {},
		"--max-tokens": {},
		"--slugname":   {},
		"--tenantname": {},
	},
	"/session": {
		"new":    {},
		"save":   {},
		"load":   {},
		"list":   {},
		"delete": {},
	},
}

// ChatCLI representa a interface de linha de comando do chat
type ChatCLI struct {
	Client               client.LLMClient
	manager              manager.LLMManager
	logger               *zap.Logger
	Provider             string
	Model                string
	history              []models.Message
	commandHistory       []string
	newCommandsInSession []string
	historyManager       *HistoryManager
	animation            *AnimationManager
	commandHandler       *CommandHandler
	lastCommandOutput    string
	fileChunks           []FileChunk // Chunks pendentes para processamento
	failedChunks         []FileChunk // Chunks que falharam no processamento
	lastFailedChunk      *FileChunk  // Referência ao último chunk que falhou
	agentMode            *AgentMode  // Modo de agente
	interactionState     InteractionState
	mu                   sync.Mutex
	operationCancel      context.CancelFunc
	isExecuting          atomic.Bool
	processingDone       chan struct{}
	sessionManager       *SessionManager
	currentSessionName   string
	MaxTokensOverride    int
	UserMaxTokens        int
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

	// Preservar provider/model atuais do runtime
	prevProvider := cli.Provider
	prevModel := cli.Model

	// Determinar o arquivo .env (mesma lógica do main.go) e expandir caminho se necessário
	envFilePath := os.Getenv("CHATCLI_DOTENV")
	if envFilePath == "" {
		envFilePath = ".env"
	} else {
		if expanded, err := utils.ExpandPath(envFilePath); err == nil {
			envFilePath = expanded
		} else {
			fmt.Printf("Aviso: não foi possível expandir o caminho '%s': %v\n", envFilePath, err)
		}
	}
	// Limpar variáveis de ambiente relevantes para garantir reload consistente
	variablesToUnset := []string{
		// Gerais
		"LOG_LEVEL", "ENV", "LLM_PROVIDER", "LOG_FILE", "LOG_MAX_SIZE", "HISTORY_MAX_SIZE",
		// OpenAI
		"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_ASSISTANT_MODEL",
		"OPENAI_USE_RESPONSES", "OPENAI_MAX_TOKENS",
		// ClaudeAI
		"CLAUDEAI_API_KEY", "CLAUDEAI_MODEL", "CLAUDEAI_MAX_TOKENS", "CLAUDEAI_API_VERSION",
		// Google AI (Gemini)
		"GOOGLEAI_API_KEY", "GOOGLEAI_MODEL", "GOOGLEAI_MAX_TOKENS",
		// StackSpot
		"CLIENT_ID", "CLIENT_SECRET", "SLUG_NAME", "TENANT_NAME",
	}

	for _, variable := range variablesToUnset {
		_ = os.Unsetenv(variable)
	}

	// Recarregar o arquivo .env sobrescrevendo valores (garante atualização mesmo se já havia env setado)
	err := godotenv.Overload(envFilePath)
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

	// Tentar reaproveitar o provider/model escolhidos pelo usuário
	if prevProvider != "" && prevModel != "" {
		if client, err := cli.manager.GetClient(prevProvider, prevModel); err == nil {
			cli.Client = client
			cli.Provider = prevProvider
			cli.Model = prevModel
			fmt.Println("Configurações recarregadas com sucesso! (preservado provider/model atuais)")
			return
		}
		// Se falhar (ex.: provider indisponível), caímos para o comportamento padrão
		cli.logger.Warn("Falha ao preservar provider/model após reload; caindo para valores do .env",
			zap.String("provider", prevProvider), zap.String("model", prevModel))
	}
	// Fallback: usar valores do .env
	cli.configureProviderAndModel()
	if client, err := cli.manager.GetClient(cli.Provider, cli.Model); err == nil {
		cli.Client = client
		fmt.Println("Configurações recarregadas com sucesso!")
	} else {
		cli.logger.Error("Erro ao obter o cliente LLM", zap.Error(err))
		fmt.Println("Falha ao reconfigurar cliente LLM após reload.")
	}
}

func (cli *ChatCLI) configureProviderAndModel() {
	cli.Provider = os.Getenv("LLM_PROVIDER")
	if cli.Provider == "" {
		cli.Provider = config.DefaultLLMProvider
	}
	if cli.Provider == "OPENAI" {
		cli.Model = os.Getenv("OPENAI_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultOpenAIModel
		}
	}
	if cli.Provider == "OPENAI_ASSISTANT" {
		cli.Model = os.Getenv("OPENAI_ASSISTANT_MODEL")
		if cli.Model == "" {
			cli.Model = utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAiAssistModel) // se não houver, usa o mesmo dos completions ou seta default
		}
	}
	if cli.Provider == "CLAUDEAI" {
		cli.Model = os.Getenv("CLAUDEAI_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultClaudeAIModel
		}
	}
	if cli.Provider == "GOOGLEAI" {
		cli.Model = os.Getenv("GOOGLEAI_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultGoogleAIModel
		}
	}
	if cli.Provider == "XAI" {
		cli.Model = os.Getenv("XAI_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultXAIModel
		}
	}
	if cli.Provider == "OLLAMA" {
		cli.Model = os.Getenv("OLLAMA_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultOllamaModel
		}
	}
}

// NewChatCLI cria uma nova instância de ChatCLI
func NewChatCLI(manager manager.LLMManager, logger *zap.Logger) (*ChatCLI, error) {
	cli := &ChatCLI{
		manager:           manager,
		logger:            logger,
		history:           make([]models.Message, 0),
		historyManager:    NewHistoryManager(logger),
		animation:         NewAnimationManager(),
		interactionState:  StateNormal,
		processingDone:    make(chan struct{}),
		MaxTokensOverride: 0,
	}

	cli.configureProviderAndModel()

	client, err := manager.GetClient(cli.Provider, cli.Model)
	if err != nil {
		logger.Error("Erro ao obter o cliente LLM", zap.Error(err))
		return nil, err
	}

	sessionMgr, err := NewSessionManager(logger)
	if err != nil {
		return nil, fmt.Errorf("erro ao inicializar o SessionManager: %w", err)
	}
	cli.sessionManager = sessionMgr
	cli.currentSessionName = ""

	cli.Client = client
	cli.commandHandler = NewCommandHandler(cli)
	cli.agentMode = NewAgentMode(cli, logger)

	history, err := cli.historyManager.LoadHistory()
	if err != nil {
		cli.logger.Error("Erro ao carregar o histórico", zap.Error(err))
	} else {
		cli.commandHistory = history
	}

	return cli, nil
}

func (cli *ChatCLI) executor(in string) {
	// Não resetar a flag aqui. O wrapper controla isso.
	// cli.shouldEnterAgentMode = false

	in = strings.TrimSpace(in)
	if in != "" {
		cli.commandHistory = append(cli.commandHistory, in)
		cli.newCommandsInSession = append(cli.newCommandsInSession, in)
	}

	// Se o comando for para o agente, apenas defina a flag e retorne.
	if strings.HasPrefix(in, "/agent") || strings.HasPrefix(in, "/run") {
		panic(agentModeRequest)
	}

	// Se a entrada estiver vazia, não faça nada.
	if in == "" {
		return
	}

	// Lida com a seleção de provedor
	if cli.interactionState == StateSwitchingProvider {
		cli.handleProviderSelection(in)
		cli.interactionState = StateNormal
		return
	}

	// Comandos especiais
	if strings.Contains(strings.ToLower(in), "@command ") {
		command := strings.TrimPrefix(in, "@command ")
		cli.executeDirectCommand(command)
		return
	}

	if strings.HasPrefix(in, "/") || in == "exit" || in == "quit" {
		if cli.commandHandler.HandleCommand(in) {
			panic(errExitRequest)
		}
		return
	}

	// Para prompts LLM, processar de forma assíncrona
	cli.interactionState = StateProcessing
	go cli.processLLMRequest(in)
}

// IsExecuting retorna true se uma operação está em andamento
func (cli *ChatCLI) IsExecuting() bool {
	return cli.isExecuting.Load()
}

// CancelOperation cancela a operação atual se houver uma
func (cli *ChatCLI) CancelOperation() {
	cli.mu.Lock()
	defer cli.mu.Unlock()

	if cli.operationCancel != nil {
		cli.operationCancel()
	}
}

func (cli *ChatCLI) processLLMRequest(in string) {
	cli.isExecuting.Store(true)

	defer func() {
		cli.isExecuting.Store(false)
		cli.interactionState = StateNormal
		cli.forceRefreshPrompt()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cli.mu.Lock()
	cli.operationCancel = cancel
	cli.mu.Unlock()

	defer func() {
		cli.mu.Lock()
		cli.operationCancel = nil
		cli.mu.Unlock()
		cancel()
	}()

	fmt.Println()
	cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())

	userInput, additionalContext := cli.processSpecialCommands(in)
	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: userInput + additionalContext,
	})

	effectiveMaxTokens := cli.getMaxTokensForCurrentLLM()

	aiResponse, err := cli.Client.SendPrompt(ctx, userInput+additionalContext, cli.history, effectiveMaxTokens)

	cli.animation.StopThinkingAnimation()

	//resetTerminal(cli.logger)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println("🛑 Operação cancelada com sucesso!")
			if len(cli.history) > 0 && cli.history[len(cli.history)-1].Role == "user" {
				cli.history = cli.history[:len(cli.history)-1]
			}
		} else {
			// Simplificado para sempre mostrar o erro real vindo do cliente.
			// O erro já deve vir sanitizado se necessário.
			fmt.Printf("❌ Erro: %s\n", err.Error())
		}
	} else {
		cli.history = append(cli.history, models.Message{
			Role:    "assistant",
			Content: aiResponse,
		})
		modelName := cli.Client.GetModelName()
		coloredPrefix := colorize(modelName+":", ColorPurple)
		renderedResponse := cli.renderMarkdown(aiResponse)
		fmt.Printf("%s ", coloredPrefix)
		cli.typewriterEffect(renderedResponse, 2*time.Millisecond)
		fmt.Println()
	}
}

func (cli *ChatCLI) handleProviderSelection(in string) {
	availableProviders := cli.manager.GetAvailableProviders()
	choiceIndex, err := strconv.Atoi(in)
	if err != nil || choiceIndex < 1 || choiceIndex > len(availableProviders) {
		fmt.Println("Escolha inválida. Voltando ao modo normal.")
		return
	}

	newProvider := availableProviders[choiceIndex-1]
	var newModel string
	if newProvider == "OPENAI" {
		newModel = utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAIModel)
	}
	if newProvider == "CLAUDEAI" {
		newModel = utils.GetEnvOrDefault("CLAUDEAI_MODEL", config.DefaultClaudeAIModel)
	}
	if newProvider == "OPENAI_ASSISTANT" {
		newModel = utils.GetEnvOrDefault("OPENAI_ASSISTANT_MODEL", utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAiAssistModel))
	}
	if newProvider == "GOOGLEAI" {
		newModel = utils.GetEnvOrDefault("GOOGLEAI_MODEL", config.DefaultGoogleAIModel)
	}
	if newProvider == "XAI" {
		newModel = utils.GetEnvOrDefault("XAI_MODEL", config.DefaultXAIModel)
	}
	if newProvider == "OLLAMA" {
		newModel = utils.GetEnvOrDefault("OLLAMA_MODEL", config.DefaultOllamaModel)
	}

	newClient, err := cli.manager.GetClient(newProvider, newModel)
	if err != nil {
		cli.logger.Error("Erro ao trocar de provedor", zap.Error(err))
		fmt.Println("Erro ao trocar de provedor.")
		return
	}

	cli.Client = newClient
	cli.Provider = newProvider
	cli.Model = newModel
	fmt.Printf("Trocado para %s (%s)\n\n", cli.Client.GetModelName(), cli.Provider)
}

func (cli *ChatCLI) Start(ctx context.Context) {
	defer cli.cleanup()
	cli.PrintWelcomeScreen()

	shouldContinue := true
	for shouldContinue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// Verificamos qual tipo de pânico ocorreu
					if r == agentModeRequest {
						// Mantém shouldContinue = true para entrar no modo agente
					} else if r == errExitRequest {
						// Se for um pedido de saída, definimos shouldContinue como false
						shouldContinue = false
					} else {
						// Se for outro pânico, relançamos
						panic(r)
					}
				}
			}()

			p := prompt.New(
				cli.executor,
				cli.completer,
				prompt.OptionTitle("ChatCLI - LLM no seu Terminal"),
				prompt.OptionLivePrefix(cli.changeLivePrefix),
				prompt.OptionPrefixTextColor(prompt.Green),
				prompt.OptionInputTextColor(prompt.White),
				prompt.OptionSuggestionBGColor(prompt.DarkGray),
				prompt.OptionDescriptionBGColor(prompt.Black),
				prompt.OptionSuggestionTextColor(prompt.White),
				prompt.OptionDescriptionTextColor(prompt.Yellow),
				prompt.OptionSelectedSuggestionBGColor(prompt.Blue),
				prompt.OptionSelectedDescriptionBGColor(prompt.DarkGray),
				prompt.OptionHistory(cli.commandHistory),
				prompt.OptionMaxSuggestion(10),
				prompt.OptionAddKeyBind(prompt.KeyBind{
					Key: prompt.ControlC,
					Fn:  cli.handleCtrlC,
				}),
			)

			p.Run()
			shouldContinue = false
		}()

		// Se o loop 'for' deve continuar, é porque o modo agente foi solicitado.
		if shouldContinue {
			cli.restoreTerminal()
			cli.runAgentLogic()
		}
	}
}

// restoreTerminal executa `stty sane` para restaurar o estado do terminal
// para o modo normal após o go-prompt deixá-lo em "raw mode".
// Isso é necessário em sistemas não-Windows.
func (cli *ChatCLI) restoreTerminal() {
	if runtime.GOOS == "windows" {
		return // stty não está disponível no Windows
	}
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin // Garante que o comando opere no terminal correto
	if err := cmd.Run(); err != nil {
		cli.logger.Warn("Falha ao restaurar o terminal com 'stty sane'", zap.Error(err))
	}
}

func (cli *ChatCLI) runAgentLogic() {
	// O último comando no histórico é o que ativou o agente
	if len(cli.commandHistory) == 0 {
		return
	}
	lastCommand := cli.commandHistory[len(cli.commandHistory)-1]

	// Extrair a consulta
	query := ""
	if strings.HasPrefix(lastCommand, "/agent") {
		query = strings.TrimSpace(strings.TrimPrefix(lastCommand, "/agent"))
	} else if strings.HasPrefix(lastCommand, "/run") {
		query = strings.TrimSpace(strings.TrimPrefix(lastCommand, "/run"))
	} else {
		fmt.Println("Erro: não foi possível extrair a consulta do agente.")
		return
	}

	fmt.Printf("\n🤖 Entrando no modo agente com a consulta: \"%s\"\n", query)
	fmt.Println("O agente analisará sua solicitação e sugerirá comandos para resolver.")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	query, additionalContext := cli.processSpecialCommands(query)

	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	err := cli.agentMode.Run(ctx, query, additionalContext)
	if err != nil {
		fmt.Printf(" ❌ Erro no modo agente: %v\n", err)
	}

	fmt.Println("\n ✅ Retornando ao chat...")
	time.Sleep(1 * time.Second)
}

func (cli *ChatCLI) handleCtrlC(buf *prompt.Buffer) {
	if cli.isExecuting.Load() {
		fmt.Println("\n⚠️ Ctrl + C acionado - Cancelando operação...")

		cli.mu.Lock()
		if cli.operationCancel != nil {
			cli.operationCancel()
		}
		cli.mu.Unlock()

		// Forçar volta ao estado normal
		cli.interactionState = StateNormal

		// reset
		//resetTerminal(cli.logger)

		cli.forceRefreshPrompt()

	} else {
		fmt.Println("\nAté breve!! CTRL + C Duplo...")
		cli.cleanup()
		os.Exit(0)
	}
}

func (cli *ChatCLI) changeLivePrefix() (string, bool) {
	switch cli.interactionState {
	case StateSwitchingProvider:
		return "Escolha o provedor (pelo número): ", true
	case StateProcessing, StateAgentMode:
		return "", true
	default:
		// Mostra o nome da sessão no prompt
		if cli.currentSessionName != "" {
			return fmt.Sprintf("%s ❯ ", cli.currentSessionName), true // <--- CORRIGIDO: Retorna texto puro
		}
		return "❯ ", true
	}
}

// cleanup realiza a limpeza de recursos ao encerrar o ChatCLI
func (cli *ChatCLI) cleanup() {
	if err := cli.historyManager.AppendAndRotateHistory(cli.newCommandsInSession); err != nil {
		cli.logger.Error("Erro ao salvar histórico", zap.Error(err))
	}
	if assistantClient, ok := cli.Client.(*openai_assistant.OpenAIAssistantClient); ok {
		if err := assistantClient.Cleanup(); err != nil {
			cli.logger.Error("Erro na limpeza do OpenAI Assistant", zap.Error(err))
		}
	}
	if err := cli.logger.Sync(); err != nil {
		fmt.Printf("Falha ao sincronizar logger: %v\n", err)
	}
}

// handleSwitchCommand Processa os comando na entrada para atualizar o slug/tenant, ou mudar o modelo para LLM atual.
func (cli *ChatCLI) handleSwitchCommand(userInput string) {
	args := strings.Fields(userInput)
	var newSlugName, newTenantName, newModel string
	shouldUpdateToken := false
	shouldSwitchModel := false
	maxTokensOverride := -1

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--slugname":
			if i+1 < len(args) {
				newSlugName = args[i+1]
				shouldUpdateToken = true
				i++ // Pular o valor
			}
		case "--tenantname":
			if i+1 < len(args) {
				newTenantName = args[i+1]
				shouldUpdateToken = true
				i++ // Pular o valor
			}
		case "--model":
			if i+1 < len(args) {
				newModel = args[i+1]
				shouldSwitchModel = true
				i++ // Pular o valor
			}
		case "--max-tokens":
			if i+1 < len(args) {
				val, err := strconv.Atoi(args[i+1])
				if err == nil && val >= 0 {
					maxTokensOverride = val
				} else {
					fmt.Printf(" ❌ Valor inválido para --max-tokens: '%s'. Deve ser um número >= 0.\n", args[i+1])
				}
				i++
			}
		}
	}
	if maxTokensOverride != -1 {
		cli.UserMaxTokens = maxTokensOverride
		fmt.Printf(" ✅ Limite máximo de tokens definido para: %d (0 = usar padrão do provedor)\n", cli.UserMaxTokens)
	}

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
	}

	if shouldSwitchModel {
		fmt.Printf("Tentando trocar para o modelo '%s' no provedor '%s'...\n", newModel, cli.Provider)
		newClient, err := cli.manager.GetClient(cli.Provider, newModel)
		if err != nil {
			fmt.Printf(" ❌ Erro ao trocar para o modelo '%s': %v\n", newModel, err)
			fmt.Println("   Verifique se o nome do modelo está correto para o provedor atual.")
		} else {
			cli.Client = newClient
			cli.Model = newModel // Atualiza o modelo no estado do CLI
			fmt.Printf(" ✅ Modelo trocado com sucesso para %s (%s)\n", cli.Client.GetModelName(), cli.Provider)
		}
		return // Finaliza após a tentativa de troca de modelo
	}

	if !shouldUpdateToken && !shouldSwitchModel && maxTokensOverride == -1 && len(args) == 1 {
		cli.switchProvider()
	}
}

func (cli *ChatCLI) switchProvider() {
	fmt.Println("Provedores disponíveis:")
	availableProviders := cli.manager.GetAvailableProviders()
	for i, provider := range availableProviders {
		fmt.Printf("%d. %s\n", i+1, provider)
	}
	cli.interactionState = StateSwitchingProvider
}

func (cli *ChatCLI) showHelp() {
	// Helper para formatar linhas com alinhamento
	printCommand := func(cmd, desc string) {
		cmdColor := ColorCyan
		descColor := ColorGray
		// Deixa a descrição em branco se o comando for um exemplo
		if strings.HasPrefix(cmd, "  ") {
			cmdColor = ColorGray
			descColor = ColorGray
		}
		fmt.Printf("    %s    %s\n", colorize(fmt.Sprintf("%-32s", cmd), cmdColor), colorize(desc, descColor))
	}

	// Cabeçalho
	fmt.Println("\n" + colorize(ColorBold, "Guia Completo de Comandos do ChatCLI"))
	fmt.Println(colorize("Aqui está um mapa exaustivo de todos os comandos, subcomandos, flags e opções disponíveis.", ColorGray))
	fmt.Println(colorize("Use-os para controlar a aplicação, adicionar contexto ou automatizar tarefas.", ColorGray))

	// --- Controle Geral da Aplicação ---
	fmt.Printf("\n  %s\n", colorize("Controle Geral", ColorLime))
	printCommand("/help", "Mostra esta tela de ajuda completa.")
	printCommand("/exit | /quit", "Encerra a aplicação.")
	printCommand("/newsession", "Limpa o histórico da conversa atual e inicia uma nova sessão (alias de /session new).")
	printCommand("/version | /v", "Mostra a versão, commit hash, data de build e verifica atualizações.")

	// --- Configuração e Provedores de IA ---
	fmt.Printf("\n  %s\n", colorize("Configuração e Provedores de IA", ColorLime))
	printCommand("/switch", "Abre o menu interativo para trocar o provedor de LLM (ex.: OPENAI, CLAUDEAI).")
	printCommand("/switch --model <nome>", "Muda o modelo do provedor atual (ex.: gpt-4o-mini, grok-4, gpt5... etc).")
	printCommand("/switch --max-tokens <num>", "Define o máximo de tokens para as próximas respostas (0 para padrão).")
	printCommand("  Ex: /switch --model gpt-4o-mini", "(Muda para o modelo GPT-4o Mini na OpenAI)")
	printCommand("/switch --slugname <slug>", "Atualiza o 'slugName' (apenas para StackSpot).")
	printCommand("/switch --tenantname <tenant>", "Atualiza o 'tenantName' (apenas para StackSpot).")
	printCommand("/config | /status", "Exibe a configuração atual (provedor, modelo, chaves, etc.).")
	printCommand("/reload", "Recarrega as variáveis ou configurações do seu arquivo .env em tempo real.")

	// --- Comandos de Contexto (usados em prompts) ---
	fmt.Printf("\n  %s\n", colorize("Adicionando Contexto aos Prompts", ColorLime))
	printCommand("@file <caminho>", "Adiciona o conteúdo de um arquivo ou diretório ao prompt.")
	printCommand("  --mode full", "(Padrão) Envia o conteúdo completo, truncando se necessário.")
	printCommand("  --mode chunked", "Para projetos grandes, divide em pedaços (chunks).")
	printCommand("  --mode summary", "Envia apenas a estrutura de arquivos, sem o conteúdo.")
	printCommand("  --mode smart", "A IA seleciona os arquivos mais relevantes para sua pergunta.")
	printCommand("  Ex: @file --mode=smart ./src Como funciona o login?", "")
	printCommand("@git", "Adiciona status, branch, remotos e commits recentes do Git.")
	printCommand("@history", "Adiciona os últimos comandos do seu histórico de shell (bash/zsh/fish).")
	printCommand("@env", "Adiciona as variáveis de ambiente (valores sensíveis são ocultados).")

	// --- Gerenciamento de Chunks (para @file --mode chunked) ---
	fmt.Printf("\n  %s\n", colorize("Gerenciamento de Arquivos Grandes (Chunks)", ColorLime))
	printCommand("/nextchunk", "Envia o próximo pedaço (chunk) do projeto para a IA.")
	printCommand("/retry", "Tenta reenviar o último chunk que falhou.")
	printCommand("/retryall", "Tenta reenviar todos os chunks que falharam.")
	printCommand("/skipchunk", "Pula um chunk com erro e continua para o próximo.")

	// --- Execução de Comandos (@command) ---
	fmt.Printf("\n  %s\n", colorize("Execução de Comandos no Terminal", ColorLime))
	printCommand("@command <cmd>", "Executa um comando e adiciona sua saída ao prompt.")
	printCommand("  Ex: @command ls -la", "(Executa 'ls -la' e anexa o resultado)")
	printCommand("@command -i <cmd>", "Executa um comando interativo (ex.: vim) sem adicionar saída ao prompt.")
	printCommand("@command --ai <cmd>", "Executa um comando e envia a saída DIRETAMENTE para a IA.")
	printCommand("  Ex: @command --ai git diff", "(Envia as diferenças do git para análise da IA)")
	printCommand("@command --ai <cmd> > <texto>", "Igual ao anterior, mas adiciona um contexto/pergunta.")
	printCommand("  Ex: @command --ai cat err.log > resuma este erro", "")

	// --- Modo Agente (Automação de Tarefas) ---
	fmt.Printf("\n  %s\n", colorize("Modo Agente (Execução de Tarefas)", ColorLime))
	printCommand("/agent <tarefa>", "Pede à IA para planejar e executar comandos para resolver uma tarefa.")
	printCommand("/run <tarefa>", "Um atalho (alias) para o comando /agent.")
	printCommand("  Ex: /agent liste todos os arquivos .go e conte suas linhas", "")
	printCommand("Dentro do modo agente:", "")
	printCommand("  [1..N]", "Executa um comando específico (ex: 1 para o primeiro).")
	printCommand("  a", "Executa TODOS os comandos sugeridos em sequência.")
	printCommand("  eN", "Edita o comando N antes de executar (ex: e1).")
	printCommand("  tN", "Simula (dry-run) o comando N (ex: t2).")
	printCommand("  cN", "Pede continuação à IA com a saída do comando N (ex: c2).")
	printCommand("  pcN", "Adiciona contexto ao comando N ANTES de executar (ex: pc1).")
	printCommand("  acN", "Adiciona contexto à SAÍDA do comando N (ex: ac1).")
	printCommand("  vN", "Abre a saída completa do comando N no pager (less -R/more).")
	printCommand("  wN", "Salva a saída do comando N em um arquivo temporário.")
	printCommand("  p", "Alterna a visualização do plano: COMPACTO ↔ COMPLETO.")
	printCommand("  r", "Atualiza a tela (clear + redraw).")
	printCommand("  q", "Sai do modo agente.")
	printCommand("Observações:", "")
	printCommand("  • Último Resultado", "sempre ancorado ao rodapé da tela (preview).")
	printCommand("  • Plano COMPACTO", "mostra 1 linha por comando (status + descrição + 1ª linha do código).")
	printCommand("  • Plano COMPLETO", "mostra cartão com descrição, tipo, risco e bloco de código formatado.")

	// --- Gerenciamento de Sessões (/session) ---
	fmt.Printf("\n  %s\n", colorize("Gerenciamento de Sessões", ColorLime))
	printCommand("/session save <nome>", "Salva a sessão atual com um nome (ex.: /session save minha-conversa).")
	printCommand("/session load <nome>", "Carrega uma sessão salva (ex.: /session load minha-conversa).")
	printCommand("/session list", "Lista todas as sessões salvas.")
	printCommand("/session delete <nome>", "Deleta uma sessão salva (ex.: /session delete minha-conversa).")
	printCommand("/session new", "Inicia uma nova sessão limpa (alias de /newsession).")

	// --- Modo Não-Interativo (One-Shot) ---
	fmt.Printf("\n  %s\n", colorize("Modo Não-Interativo (One-Shot, para scripts e pipes)", ColorLime))
	printCommand("chatcli -p \"<prompt>\"", "Executa um prompt uma única vez e sai.")
	printCommand("  Ex: chatcli -p \"Explique este repositório.\"", "")
	printCommand("chatcli --prompt \"<prompt>\"", "Alias de -p.")
	printCommand("--provider <nome>", "Override do provedor (ex.: --provider OPENAI).")
	printCommand("--model <nome>", "Override do modelo (ex.: --model gpt-4o-mini).")
	printCommand("--max-tokens <num>", "Override do máximo de tokens para a resposta.")
	printCommand("--timeout <duração>", "Timeout da chamada (ex.: --timeout 5m, padrão: 5m).")
	printCommand("--no-anim", "Desabilita animações (útil em scripts/CI).")
	printCommand("--agent-auto-exec", "No modo agente one-shot, executa o primeiro comando sugerido automaticamente se for seguro.")
	printCommand("Uso com pipes (stdin):", "Envia dados via pipe (ex.: git diff | chatcli -p \"Resuma as mudanças.\").")

	// --- Dicas de Uso e Atalhos ---
	fmt.Printf("\n  %s\n", colorize("Dicas e Atalhos Gerais", ColorLime))
	printCommand("Cancelamento (Ctrl+C)", "Pressione Ctrl+C uma vez durante o 'Pensando...' para cancelar.")
	printCommand("Saída Rápida (Ctrl+D)", "Pressione Ctrl+D no prompt vazio para sair do ChatCLI.")
	printCommand("Operador '>'", "Use '>' para adicionar contexto em prompts (ex.: @git > Crie um release note).")
	printCommand("Modo Agente: p", "Alterna COMPACTO/COMPLETO do plano (útil para focar no fluxo).")
	printCommand("Modo Agente: vN", "Abre a saída do comando N no pager (leitura longa sem poluir a tela).")
	printCommand("Modo Agente: wN", "Salva a saída do comando N em arquivo temporário (para compartilhar ou anexar).")
	printCommand("Modo Agente: r", "Redesenha a tela (clear) mantendo o foco no 'Último Resultado'.")

	fmt.Println()
}

// ApplyOverrides atualiza provider/model e reobtém o client correspondente
func (cli *ChatCLI) ApplyOverrides(mgr manager.LLMManager, provider, model string) error {
	if provider == "" && model == "" {
		return nil
	}
	prov := cli.Provider
	mod := cli.Model
	if provider != "" {
		prov = strings.ToUpper(provider)
	}
	if model != "" {
		mod = model
	}
	if prov == cli.Provider && mod == cli.Model {
		return nil
	}
	newClient, err := mgr.GetClient(prov, mod)
	if err != nil {
		return err
	}
	cli.Client = newClient
	cli.Provider = prov
	cli.Model = mod
	return nil
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
			allLines := filterEmptyLines(strings.Split(historyData, "\n"))
			total := len(allLines)

			n := 30
			var lastLines []string
			if total > n {
				lastLines = allLines[total-n:]
			} else {
				lastLines = allLines
			}

			startNumber := total - len(lastLines) + 1
			formatted := make([]string, len(lastLines))
			for i, cmd := range lastLines {
				formatted[i] = fmt.Sprintf("%d: %s", startNumber+i, cmd)
			}
			limitedHistoryData := strings.Join(formatted, "\n")
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
		executor := utils.NewOSCommandExecutor()
		gitData, err := utils.GetGitInfo(executor)
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
		envData := utils.GetEnvVariablesSanitized()
		additionalContext += "\nVariáveis de Ambiente:\n" + envData
		userInput = removeCommandAndNormalizeSpaces(userInput, "@env")
	}
	return userInput, additionalContext
}

// processFileCommand adiciona o conteúdo de arquivos ou diretórios ao contexto
func (cli *ChatCLI) processFileCommand(userInput string) (string, string) {
	var additionalContext string

	if strings.Contains(strings.ToLower(userInput), "@file") {
		// Removida a verificação especial para o OpenAI Assistant
		// Agora, sempre usamos o mesmo processamento de arquivos, independentemente do modelo

		paths, options, err := extractFileCommandOptions(userInput)
		if err != nil {
			cli.logger.Error("Erro ao processar os comandos @file", zap.Error(err))
			return userInput, fmt.Sprintf("\nErro ao processar @file: %s\n", err.Error())
		}

		// Usar o modo a partir das opções já extraídas
		mode := config.ModeFull // Modo padrão
		if modeVal, ok := options["mode"]; ok {
			mode = modeVal
		}

		// Configurar estimador de tokens e obter limite máximo de tokens do LLM atual
		tokenEstimator := cli.getTokenEstimatorForCurrentLLM()
		maxTokens := cli.getMaxTokensForCurrentLLM()

		// Processar cada caminho encontrado após @file
		for _, path := range paths {
			// Configurações de escaneamento
			scanOptions := utils.DefaultDirectoryScanOptions(cli.logger)

			// 1. Pré-escanear para obter a contagem total de arquivos
			totalFiles, err := utils.CountMatchingFiles(path, scanOptions)
			if err != nil {
				cli.logger.Error("Erro ao contar arquivos", zap.String("path", path), zap.Error(err))
				additionalContext += fmt.Sprintf("\nErro ao analisar o diretório '%s': %s\n", path, err.Error())
				continue
			}

			// 2. Inicializar contador para os arquivos processados
			var processedFiles int32 = 0

			// 3. Definir o callback para atualizar a animação com progresso rico
			scanOptions.OnFileProcessed = func(info utils.FileInfo) {
				// Usar atomic para segurança em concorrência, embora o callback seja chamado em série aqui
				atomic.AddInt32(&processedFiles, 1)
				// Atualiza a mensagem da animação com o progresso
				cli.animation.UpdateMessage(
					fmt.Sprintf("Analisando... [%d/%d] %s", atomic.LoadInt32(&processedFiles), totalFiles, info.Path),
				)
			}

			// Escolher a forma de processar (summary, chunked, smartChunk ou full)
			switch mode {
			case config.ModeSummary:
				// Atualizar a mensagem da animação para o modo summary
				cli.animation.UpdateMessage(fmt.Sprintf("Gerando resumo para %s...", path))
				summary, err := cli.processDirectorySummary(path, tokenEstimator, maxTokens)
				if err != nil {
					additionalContext += fmt.Sprintf("\nErro ao processar '%s': %s\n", path, err.Error())
				} else {
					additionalContext += summary
				}

			case config.ModeChunked:
				// Atualizar a mensagem da animação para o modo chunked
				cli.animation.UpdateMessage(fmt.Sprintf("Dividindo %s em chunks...", path))
				chunks, err := cli.processDirectoryChunked(path, tokenEstimator, maxTokens)
				if err != nil {
					additionalContext += fmt.Sprintf("\nErro ao processar '%s': %s\n", path, err.Error())
				} else {
					// Apenas o primeiro chunk é adicionado diretamente ao contexto.
					if len(chunks) > 0 {
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
				// Atualizar a mensagem da animação para o modo smart
				cli.animation.UpdateMessage(fmt.Sprintf("Analisando relevância dos arquivos em %s...", path))

				// Extrair a consulta do usuário (tudo o que vier após o @file + opções)
				query := extractUserQuery(userInput)
				relevantContent, err := cli.processDirectorySmart(path, query, tokenEstimator, maxTokens)
				if err != nil {
					additionalContext += fmt.Sprintf("\nErro ao processar '%s': %s\n", path, err.Error())
				} else {
					additionalContext += relevantContent
				}

			default: // ModeFull - comportamento atual (inclui todo o conteúdo relevante dentro de um limite)
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

		// Remover o comando @file do input original para não poluir o prompt final
		userInput = removeAllFileCommands(userInput)
	}

	return userInput, additionalContext
}

// extractFileCommandOptions extrai caminhos e opções do comando @file
func extractFileCommandOptions(input string) ([]string, map[string]string, error) {
	var paths []string
	options := make(map[string]string)

	// Regex atualizada para encontrar blocos @file com opções e caminho
	// Aceita tanto "--key=value" quanto "--key value"
	re := regexp.MustCompile(`@file((?:\s+--\w+(?:(?:=|\s+)\S+)?)*\s+[\w~/.-]+/?[\w.-]*)`)
	matches := re.FindAllStringSubmatch(input, -1)

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		// Divide o bloco de comando em tokens
		tokens := strings.Fields(match[1])
		var currentPath string

		// Itera sobre os tokens para separar opções do caminho
		i := 0
		for i < len(tokens) {
			token := tokens[i]
			if strings.HasPrefix(token, "--") {
				key := strings.TrimPrefix(token, "--")
				// Formato --key=value
				if parts := strings.SplitN(key, "=", 2); len(parts) == 2 {
					options[parts[0]] = parts[1]
					i++
					// Formato --key value
				} else if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "--") {
					options[key] = tokens[i+1]
					i += 2 // Pula a chave e o valor
				} else {
					// Opção sem valor (flag booleana)
					options[key] = "true"
					i++
				}
			} else {
				// O primeiro token que não é opção é o caminho do arquivo
				currentPath = token
				break // Para a análise de opções para este comando @file
			}
		}
		if currentPath != "" {
			paths = append(paths, currentPath)
		}
	}

	if len(paths) == 0 && len(matches) > 0 {
		return nil, nil, fmt.Errorf("comando @file encontrado, mas nenhum caminho válido foi especificado")
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

func (cli *ChatCLI) getMaxTokensForCurrentLLM() int {
	// 1. Prioridade máxima para o override do usuário via flag
	if cli.UserMaxTokens > 0 {
		return cli.UserMaxTokens
	}

	// Overrides por ENV têm precedência e dão flexibilidade operacional
	var override int
	if strings.ToUpper(cli.Provider) == "OPENAI" {
		if v := os.Getenv("OPENAI_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "CLAUDEAI" {
		if v := os.Getenv("CLAUDEAI_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "GOOGLEAI" {
		if v := os.Getenv("GOOGLEAI_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "XAI" {
		if v := os.Getenv("XAI_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "OLLAMA" {
		if v := os.Getenv("OLLAMA_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	}
	return catalog.GetMaxTokens(cli.Provider, cli.Model, override)
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
	cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())

	// Criar contexto com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Enviar o prompt para o LLM
	aiResponse, err := cli.Client.SendPrompt(ctx, prompt+"\n\n"+nextChunk.Content, cli.history, 0)

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
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.Client.GetModelName(), renderedResponse), 2*time.Millisecond)

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
	cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())

	// Criar contexto com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Enviar o prompt para o LLM
	aiResponse, err := cli.Client.SendPrompt(ctx, prompt+"\n\n"+chunk.Content, cli.history, 0)

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
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.Client.GetModelName(), renderedResponse), 2*time.Millisecond)

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

// removeAllFileCommands remove todos os comandos @file da entrada do usuário
func removeAllFileCommands(input string) string {
	// Usa uma regex similar à de extração para encontrar e remover todos os blocos @file
	re := regexp.MustCompile(`@file((?:\s+--\w+(?:(?:=|\s+)\S+)?)*\s+[\w~/.-]+/?[\w.-]*)`)
	cleaned := re.ReplaceAllString(input, "")

	// Limpa espaços em branco extras que podem ter sido deixados para trás
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(cleaned, " "))
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
		fmt.Println("Aviso: Executando comando interativo. O controle será devolvido ao ChatCLI ao final.")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Run()
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
		outputRaw, err := cmd.CombinedOutput()
		safeOutput := utils.SanitizeSensitiveText(string(outputRaw))
		safeCmd := utils.SanitizeSensitiveText(command)

		// Exibir a saída
		fmt.Println("Saída do comando:\n\n", safeOutput)

		if err != nil {
			fmt.Println("Erro ao executar comando:", err)
		}

		// Armazenar a saída no histórico
		cli.history = append(cli.history, models.Message{
			Role:    "system",
			Content: fmt.Sprintf("Comando: %s\nSaída:\n%s", safeCmd, safeOutput),
		})
		cli.lastCommandOutput = safeOutput

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

	safeOutput := utils.SanitizeSensitiveText(output)
	safeContext := utils.SanitizeSensitiveText(aiContext)

	// Adicionar o output do comando ao histórico como mensagem do usuário
	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: fmt.Sprintf("Saída do comando:\n%s\n\nContexto: %s", safeOutput, safeContext),
	})
	// Exibir mensagem "Pensando..." com animação
	cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())

	//Criar um contexto com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	//Enviar o output e o contexto para a IA
	aiResponse, err := cli.Client.SendPrompt(ctx, fmt.Sprintf("Saída do comando:\n%s\n\nContexto: %s", safeOutput, safeContext), cli.history, 0)

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
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.Client.GetModelName(), renderResponse), 2*time.Millisecond)
}

// completer (versão final, flexível para @comandos, restritiva para /comandos)
func (cli *ChatCLI) completer(d prompt.Document) []prompt.Suggest {
	// 1. Lidar com estados especiais primeiro (como a troca de provedor)
	if cli.interactionState == StateSwitchingProvider {
		providers := cli.manager.GetAvailableProviders()
		s := make([]prompt.Suggest, len(providers))
		for i, p := range providers {
			s[i] = prompt.Suggest{Text: strconv.Itoa(i + 1), Description: p}
		}
		return prompt.FilterHasPrefix(s, d.GetWordBeforeCursor(), true)
	}

	// 2. Extrair informações do documento atual
	lineBeforeCursor := d.TextBeforeCursor()
	wordBeforeCursor := d.GetWordBeforeCursor()
	args := strings.Fields(lineBeforeCursor)

	// --- Lógica de Autocomplete Contextual ---

	// 3. Autocomplete para argumentos de comandos @ (como caminhos para @file)
	if len(args) > 0 {
		var previousWord string
		if strings.HasSuffix(lineBeforeCursor, " ") {
			previousWord = args[len(args)-1]
		} else if len(args) > 1 {
			previousWord = args[len(args)-2]
		}

		// Apenas autocompletar caminhos se a palavra atual NÃO for uma flag
		if previousWord == "@file" && !strings.HasPrefix(wordBeforeCursor, "-") {
			return cli.filePathCompleter(wordBeforeCursor)
		}

		if previousWord == "@command" && !strings.HasPrefix(wordBeforeCursor, "-") {
			suggestions := cli.systemCommandCompleter(wordBeforeCursor)
			suggestions = append(suggestions, cli.filePathCompleter(wordBeforeCursor)...)
			return suggestions
		}
	}

	// 4. Autocomplete para iniciar comandos
	if !strings.Contains(lineBeforeCursor, " ") {
		if strings.HasPrefix(wordBeforeCursor, "/") {
			return prompt.FilterHasPrefix(cli.getInternalCommands(), wordBeforeCursor, true)
		}
	}

	if strings.HasPrefix(wordBeforeCursor, "@") {
		return prompt.FilterHasPrefix(cli.getContextCommands(), wordBeforeCursor, true)
	}

	// 5. Sugestões de flags e valores (LÓGICA FINAL CORRIGIDA)
	if len(args) > 1 {
		command := args[0]
		prevWord := args[len(args)-1]
		if !strings.HasSuffix(lineBeforeCursor, " ") && len(args) > 1 {
			prevWord = args[len(args)-2]
		}
		currWord := d.GetWordBeforeCursor()

		if flagsForCommand, commandExists := commandFlags[command]; commandExists {
			// Cenário 1: O usuário digitou uma flag (ex: "--mode ") e agora quer ver os valores.
			if values, flagHasValues := flagsForCommand[prevWord]; flagHasValues && len(values) > 0 {
				return prompt.FilterHasPrefix(values, currWord, true)
			}

			// Cenário 2: O usuário está digitando uma flag (ex: "--m").
			if strings.HasPrefix(currWord, "-") {
				var flagSuggests []prompt.Suggest
				for flag, values := range flagsForCommand {
					var desc string
					// 1. Primeiro, procurar por descrições personalizadas
					if flag == "--mode" {
						desc = "Define o modo de processamento de arquivos (full, summary, chunked, smart)"
					} else if flag == "--model" {
						desc = "Troque o modelo (Runtime) baseado no provedor atual (grpt-5, grok-4, etc.)"
					} else if flag == "--max-tokens" {
						desc = "Define o máximo de tokens para as próximas respostas (0 para padrão)"
					} else if flag == "--slugname" {
						desc = "Altera o Slug em tempo de execução (Apenas para STACKSPOT)"
					} else if flag == "--tenantname" {
						desc = "Altera o Tenant em tempo de execução (Apenas para STACKSPOT)"
					} else if flag == "-i" {
						desc = "Ideal para comandos interativos evitando sensação de bloqueio do terminal"
					} else if flag == "--ai" {
						desc = "Envia a saída do comando direto para a IA analisar, para contexto adicional digite ( @command --ai <comando> > <contexto>)"
					} else {
						// 2. Se não houver descrição personalizada, criar uma genérica
						desc = fmt.Sprintf("Opção para %s", command)
						if len(values) > 0 {
							desc += " (valores: " + strings.Join(extractTexts(values), ", ") + ")"
						}
					}
					flagSuggests = append(flagSuggests, prompt.Suggest{Text: flag, Description: desc})
				}
				return prompt.FilterHasPrefix(flagSuggests, currWord, true)
			}
		}
	}

	// 6. Se nenhum dos casos acima se aplicar, não sugira nada.
	return []prompt.Suggest{}
}

// Helper para extrair só os Texts de um []Suggest (para descrições de flags)
func extractTexts(suggests []prompt.Suggest) []string {
	texts := make([]string, len(suggests))
	for i, s := range suggests {
		texts[i] = s.Text
	}
	return texts
}

func (cli *ChatCLI) getInternalCommands() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "/exit", Description: "Sair do ChatCLI"},
		{Text: "/quit", Description: "Alias de /exit - Sair do ChatCLI"},
		{Text: "/switch", Description: "Trocar o provedor de LLM, seguido por --model troca o modelo"},
		{Text: "/help", Description: "Mostrar ajuda"},
		{Text: "/reload", Description: "Recarregar configurações do .env"},
		{Text: "/config", Description: "Mostrar configuração atual"},
		{Text: "/status", Description: "Alias de /config - Mostrar configuração atual"},
		{Text: "/agent", Description: "Iniciar modo agente para executar tarefas"},
		{Text: "/run", Description: "Alias para /agent - Iniciar modo agente para executar tarefas"},
		{Text: "/newsession", Description: "Iniciar uma nova sessão de conversa"},
		{Text: "/version", Description: "Verificar a versão do ChatCLI"},
		{Text: "/nextchunk", Description: "Carregar o próximo chunk de arquivo"},
		{Text: "/retry", Description: "Tentar novamente o último chunk que falhou"},
		{Text: "/retryall", Description: "Tentar novamente todos os chunks que falharam"},
		{Text: "/skipchunk", Description: "Pular um chunk de arquivo"},
		{Text: "/session", Description: "Gerencia as sessões, new, save, list, load, delete"},
	}
}

// getContextCommands retorna a lista de sugestões para comandos com @
func (cli *ChatCLI) getContextCommands() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "@history", Description: "Adicionar histórico do shell ao contexto"},
		{Text: "@git", Description: "Adicionar informações do Git ao contexto"},
		{Text: "@env", Description: "Adicionar variáveis de ambiente ao contexto"},
		{Text: "@file", Description: "Adicionar conteúdo de um arquivo ou diretório"},
		{Text: "@command", Description: "Executar um comando do sistema e usar a saída"},
	}
}

// filePathCompleter é uma função dedicada para autocompletar caminhos de arquivo
func (cli *ChatCLI) filePathCompleter(prefix string) []prompt.Suggest {
	var suggestions []prompt.Suggest
	completions := cli.completeFilePath(prefix)
	for _, c := range completions {
		suggestions = append(suggestions, prompt.Suggest{Text: c})
	}
	return suggestions
}

// systemCommandCompleter é uma função dedicada para autocompletar comandos do sistema
func (cli *ChatCLI) systemCommandCompleter(prefix string) []prompt.Suggest {
	var suggestions []prompt.Suggest
	completions := cli.completeSystemCommands(prefix)
	for _, c := range completions {
		suggestions = append(suggestions, prompt.Suggest{Text: c})
	}
	return suggestions
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
		os.Stdout.Sync()

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

// presence retorna "[SET]" ou "[NOT SET]" para uma env sensível
func presence(v string) string {
	if strings.TrimSpace(v) == "" {
		return "[NOT SET]"
	}
	return "[SET]"
}

// getEnvFilePath retorna o caminho do arquivo .env configurado (expandido).
func (cli *ChatCLI) getEnvFilePath() string {
	envFilePath := os.Getenv("CHATCLI_DOTENV")
	if envFilePath == "" {
		envFilePath = ".env"
	}
	expanded, err := utils.ExpandPath(envFilePath)
	if err != nil {
		cli.logger.Warn("Não foi possível expandir o caminho do .env", zap.Error(err))
		return envFilePath // Retorna o original se falhar
	}
	return expanded
}

func (cli *ChatCLI) showConfig() {
	// Helper para formatar linhas com alinhamento e cores (similar ao /help)
	printItem := func(key, value string) {
		keyColor := ColorCyan
		valueColor := ColorGray
		if strings.Contains(value, "[SET]") {
			valueColor = ColorGreen
		} else if strings.Contains(value, "[NOT SET]") {
			valueColor = ColorYellow
		}
		fmt.Printf("    %s    %s\n", colorize(fmt.Sprintf("%-25s", key+":"), keyColor), colorize(value, valueColor))
	}

	// Cabeçalho
	fmt.Println("\n" + colorize(ColorBold, "Configuração Atual do ChatCLI"))
	fmt.Println(colorize("Aqui está um resumo das configurações em tempo de execução, provedores e variáveis de ambiente.", ColorGray))

	// --- Geral ---
	fmt.Printf("\n  %s\n", colorize("Configurações Gerais", ColorLime))
	printItem("Arquivo .env", cli.getEnvFilePath())
	printItem("Ambiente (ENV)", os.Getenv("ENV"))
	printItem("Nível de Log (LOG_LEVEL)", os.Getenv("LOG_LEVEL"))
	printItem("Arquivo de Log (LOG_FILE)", os.Getenv("LOG_FILE"))
	printItem("Tamanho Máx. Log (LOG_MAX_SIZE)", os.Getenv("LOG_MAX_SIZE"))
	printItem("Tamanho Máx. Histórico (HISTORY_MAX_SIZE)", os.Getenv("HISTORY_MAX_SIZE"))

	// --- Provedor e Modelo Atuais ---
	fmt.Printf("\n  %s\n", colorize("Provedor e Modelo Atuais", ColorLime))
	printItem("Provedor (Runtime)", cli.Provider)
	printItem("Modelo (Runtime)", cli.Model)
	printItem("Nome do Modelo (Client)", cli.Client.GetModelName())
	printItem("API Preferida (Catálogo)", string(catalog.GetPreferredAPI(cli.Provider, cli.Model)))
	printItem("MaxTokens Efetivo", fmt.Sprintf("%d", cli.getMaxTokensForCurrentLLM()))

	// --- Overrides por ENV ---
	fmt.Printf("\n  %s\n", colorize("Overrides de MaxTokens por Provedor (ENV)", ColorLime))
	printItem("OPENAI_MAX_TOKENS", os.Getenv("OPENAI_MAX_TOKENS"))
	printItem("CLAUDEAI_MAX_TOKENS", os.Getenv("CLAUDEAI_MAX_TOKENS"))
	printItem("GOOGLEAI_MAX_TOKENS", os.Getenv("GOOGLEAI_MAX_TOKENS"))
	printItem("XAI_MAX_TOKENS", os.Getenv("XAI_MAX_TOKENS"))
	printItem("OLLAMA_MAX_TOKENS", os.Getenv("OLLAMA_MAX_TOKENS"))

	// --- Chaves Sensíveis (Presença Apenas) ---
	fmt.Printf("\n  %s\n", colorize("Chaves Sensíveis (Presença Apenas)", ColorLime))
	printItem("OPENAI_API_KEY", presence(os.Getenv("OPENAI_API_KEY")))
	printItem("CLAUDEAI_API_KEY", presence(os.Getenv("CLAUDEAI_API_KEY")))
	printItem("GOOGLEAI_API_KEY", presence(os.Getenv("GOOGLEAI_API_KEY")))
	printItem("XAI_API_KEY", presence(os.Getenv("XAI_API_KEY")))
	printItem("CLIENT_ID (StackSpot)", presence(os.Getenv("CLIENT_ID")))
	printItem("CLIENT_SECRET (StackSpot)", presence(os.Getenv("CLIENT_SECRET")))

	// --- Configurações por Provedor ---
	fmt.Printf("\n  %s\n", colorize("Configurações por Provedor", ColorLime))
	//if strings.ToUpper(cli.Provider) == "OPENAI" || strings.ToUpper(cli.Provider) == "OPENAI_ASSISTANT" {
	printItem("OPENAI_MODEL", os.Getenv("OPENAI_MODEL"))
	printItem("OPENAI_ASSISTANT_MODEL", os.Getenv("OPENAI_ASSISTANT_MODEL"))
	printItem("OPENAI_USE_RESPONSES", os.Getenv("OPENAI_USE_RESPONSES"))
	//}
	//if strings.ToUpper(cli.Provider) == "CLAUDEAI" {
	printItem("CLAUDEAI_MODEL", os.Getenv("CLAUDEAI_MODEL"))
	printItem("CLAUDEAI_API_VERSION", os.Getenv("CLAUDEAI_API_VERSION"))
	//}
	//if strings.ToUpper(cli.Provider) == "GOOGLEAI" {
	printItem("GOOGLEAI_MODEL", os.Getenv("GOOGLEAI_MODEL"))
	//}
	//if strings.ToUpper(cli.Provider) == "XAI" {
	printItem("XAI_MODEL", os.Getenv("XAI_MODEL"))
	printItem("OLLAMA_MODEL", os.Getenv("OLLAMA_MODEL"))
	printItem("OLLAMA_BASE_URL", utils.GetEnvOrDefault("OLLAMA_BASE_URL", config.OllamaDefaultBaseURL))
	//}
	if tm, ok := cli.manager.GetTokenManager(); ok {
		slug, tenant := tm.GetSlugAndTenantName()
		printItem("STACKSPOT: slugName", slug)
		printItem("STACKSPOT: tenantName", tenant)
	}

	// --- Provedores Disponíveis ---
	fmt.Printf("\n  %s\n", colorize("Provedores Disponíveis", ColorLime))
	providers := cli.manager.GetAvailableProviders()
	if len(providers) > 0 {
		for i, p := range providers {
			printItem(fmt.Sprintf("Provedor %d", i+1), p)
		}
	} else {
		printItem("Nenhum", "Configure as chaves de API no .env")
	}
}

// handleVersionCommand exibe informações detalhadas sobre a versão atual
// do ChatCLI e verifica se há atualizações disponíveis no GitHub.
//
// O comando mostra:
// - Versão atual (tag ou hash do commit)
// - Hash do commit exato
// - Data e hora de build
// - Status de atualização (verificando o GitHub quando possível)
// handleVersionCommand exibe informações detalhadas sobre a versão atual
// do ChatCLI e verifica se há atualizações disponíveis no GitHub.
func (ch *CommandHandler) handleVersionCommand() {
	versionInfo := version.GetCurrentVersion()

	// Checagem com timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	latest, hasUpdate, err := version.CheckLatestVersionWithContext(ctx)

	// Exibir as informações formatadas
	fmt.Println(version.FormatVersionInfo(versionInfo, latest, hasUpdate, err))
}

// RunAgentOnce executa o modo agente de forma não-interativa (one-shot)
func (cli *ChatCLI) RunAgentOnce(ctx context.Context, input string, autoExecute bool) error {
	var query string
	if strings.HasPrefix(input, "/agent ") {
		query = strings.TrimPrefix(input, "/agent ")
	} else if strings.HasPrefix(input, "/run ") {
		query = strings.TrimPrefix(input, "/run ")
	} else {
		return fmt.Errorf("entrada inválida para o modo agente one-shot: %s", input)
	}

	// Processar contextos especiais como @file, @git, etc.
	query, additionalContext := cli.processSpecialCommands(query)
	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	// Assegurar que o modo agente está inicializado
	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	// Chama a nova função não-interativa do AgentMode
	return cli.agentMode.RunOnce(ctx, fullQuery, autoExecute)
}

func (cli *ChatCLI) handleSaveSession(name string) {
	if err := cli.sessionManager.SaveSession(name, cli.history); err != nil {
		fmt.Printf(" ❌ Erro ao salvar sessão: %v\n", err)
	} else {
		cli.currentSessionName = name
		fmt.Printf(" ✅ Sessão '%s' salva com sucesso.\n", name)
	}
}

func (cli *ChatCLI) handleLoadSession(name string) {
	history, err := cli.sessionManager.LoadSession(name)
	if err != nil {
		fmt.Printf(" ❌ Erro ao carregar sessão: %v\n", err)
	} else {
		cli.history = history
		cli.currentSessionName = name
		fmt.Printf(" ✅ Sessão '%s' carregada. A conversa anterior foi restaurada.\n", name)
	}
}

func (cli *ChatCLI) handleListSessions() {
	sessions, err := cli.sessionManager.ListSessions()
	if err != nil {
		fmt.Printf(" ❌ Erro ao listar sessões: %v\n", err)
		return
	}

	if len(sessions) == 0 {
		fmt.Println("Nenhuma sessão salva encontrada.")
		return
	}

	fmt.Println("Sessões salvas:")
	for _, session := range sessions {
		fmt.Printf("- %s\n", session)
	}
}

func (cli *ChatCLI) handleDeleteSession(name string) {
	if err := cli.sessionManager.DeleteSession(name); err != nil {
		fmt.Printf(" ❌ Erro ao deletar sessão: %v\n", err)
	} else {
		fmt.Printf(" ✅ Sessão '%s' deletada com sucesso do disco.\n", name)
		// Se a sessão deletada era a que estava ativa...
		if cli.currentSessionName == name {
			// ...limpamos o histórico em memória e resetamos o nome.
			cli.history = []models.Message{}
			cli.currentSessionName = ""
			fmt.Println("A sessão atual foi limpa. Você está em uma nova conversa.")
		}
	}
}
