/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/tui"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/k8s"
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

// ChatCLI representa a interface de linha de comando do chat
type ChatCLI struct {
	Client               client.LLMClient
	manager              manager.LLMManager
	logger               *zap.Logger
	Provider             string
	Model                string
	history              []models.Message  // unified conversation history (all modes)
	historyCompactor     *HistoryCompactor // manages history compaction
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
	UserMaxTokens        int
	pluginManager        *plugins.Manager
	contextHandler       *ContextHandler
	personaHandler       *PersonaHandler
	skillHandler         *SkillHandler

	// Remote connection state (for /connect and /disconnect)
	localClient   client.LLMClient           // saved local client when connected to remote
	localProvider string                     // saved local provider name
	localModel    string                     // saved local model name
	remoteConn    interface{ Close() error } // remote connection (for cleanup on disconnect)
	isRemote      bool                       // true when connected to a remote server

	// K8s watcher context injection
	WatcherContextFunc func() string      // returns K8s context to prepend to LLM prompts
	isWatching         bool               // true when K8s watcher is active
	watchStatusFunc    func() string      // returns compact status for prompt prefix
	watcherCancel      context.CancelFunc // cancels the background watcher goroutine

	// Remote resource cache (populated on /connect, cleared on /disconnect)
	remoteAgents []remote.RemoteAgentInfo
	remoteSkills []remote.RemoteSkillInfo

	// MCP server management
	mcpManager *mcp.Manager

	// Workspace context (bootstrap files + memory)
	contextBuilder *workspace.ContextBuilder
	memoryStore    *workspace.MemoryStore

	// Conversation checkpoints for rewind
	checkpoints []conversationCheckpoint

	// Background memory annotation worker
	memWorker *memoryWorker
}

// reconfigureLogger reconfigura o logger após o reload das variáveis de ambiente
func (cli *ChatCLI) reconfigureLogger() {
	cli.logger.Info("Reconfigurando o logger...")

	if err := cli.logger.Sync(); err != nil {
		cli.logger.Error("Erro ao sincronizar o logger", zap.Error(err))
	}

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
	fmt.Println(i18n.T("status.reloading_config"))

	prevProvider := cli.Provider
	prevModel := cli.Model

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
	variablesToUnset := []string{
		"LOG_LEVEL", "ENV", "LLM_PROVIDER", "LOG_FILE", "LOG_MAX_SIZE", "HISTORY_MAX_SIZE",
		"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_ASSISTANT_MODEL",
		"OPENAI_USE_RESPONSES", "OPENAI_MAX_TOKENS",
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_MAX_TOKENS", "ANTHROPIC_API_VERSION",
		"GOOGLEAI_API_KEY", "GOOGLEAI_MODEL", "GOOGLEAI_MAX_TOKENS",
		"CLIENT_ID", "CLIENT_KEY", "STACKSPOT_REALM", "STACKSPOT_AGENT_ID",
		"COPILOT_MODEL", "COPILOT_MAX_TOKENS", "GITHUB_COPILOT_TOKEN",
	}

	for _, variable := range variablesToUnset {
		_ = os.Unsetenv(variable)
	}

	err := godotenv.Overload(envFilePath)
	if err != nil && !os.IsNotExist(err) {
		cli.logger.Error("Erro ao carregar o arquivo .env", zap.Error(err))
	}

	config.Global.Reload(cli.logger)

	cli.reconfigureLogger()

	manager, err := manager.NewLLMManager(cli.logger)
	if err != nil {
		cli.logger.Error("Erro ao reconfigurar o LLMManager", zap.Error(err))
		return
	}

	cli.manager = manager

	if prevProvider != "" && prevModel != "" {
		if client, err := cli.manager.GetClient(prevProvider, prevModel); err == nil {
			cli.Client = client
			cli.Provider = prevProvider
			cli.Model = prevModel
			fmt.Println(i18n.T("status.reload_success_preserved"))
			return
		}
		cli.logger.Warn("Falha ao preservar provider/model após reload; caindo para valores do .env",
			zap.String("provider", prevProvider), zap.String("model", prevModel))
	}
	cli.configureProviderAndModel()
	if client, err := cli.manager.GetClient(cli.Provider, cli.Model); err == nil {
		cli.Client = client
		fmt.Println(i18n.T("status.reload_success"))
	} else {
		cli.logger.Error("Erro ao obter o cliente LLM", zap.Error(err))
		fmt.Println(i18n.T("status.reload_fail_client"))
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
			cli.Model = utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAiAssistModel)
		}
	}
	if cli.Provider == "CLAUDEAI" {
		cli.Model = os.Getenv("ANTHROPIC_MODEL")
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
	if cli.Provider == "COPILOT" {
		cli.Model = os.Getenv("COPILOT_MODEL")
		if cli.Model == "" {
			cli.Model = config.DefaultCopilotModel
		}
	}
}

// NewChatCLI cria uma nova instância de ChatCLI
func NewChatCLI(manager manager.LLMManager, logger *zap.Logger) (*ChatCLI, error) {
	cli := &ChatCLI{
		manager:          manager,
		logger:           logger,
		history:          make([]models.Message, 0),
		historyCompactor: NewHistoryCompactor(logger),
		historyManager:   NewHistoryManager(logger),
		animation:        NewAnimationManager(),
		interactionState: StateNormal,
		processingDone:   make(chan struct{}),
	}

	pluginMgr, err := plugins.NewManager(logger)
	if err != nil {
		// Logamos o erro, mas a aplicação continua. O pluginManager será um objeto válido, mas vazio.
		logger.Error("Falha crítica ao inicializar o gerenciador de plugins, plugins estarão desabilitados", zap.Error(err))
	}
	cli.pluginManager = pluginMgr
	if pluginMgr != nil {
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinCoderPlugin())
	}

	cli.configureProviderAndModel()

	client, err := manager.GetClient(cli.Provider, cli.Model)
	if err != nil {
		// If the configured provider is not available, try to pick any available one
		available := manager.GetAvailableProviders()
		if len(available) > 0 {
			cli.Provider = available[0]
			cli.Model = ""
			client, err = manager.GetClient(cli.Provider, cli.Model)
		}
		if err != nil {
			// No provider available at all — start in auth-only mode
			logger.Warn("Nenhum provedor LLM disponível. Use /auth login para autenticar.", zap.Error(err))
			client = nil
		}
	}

	sessionMgr, err := NewSessionManager(logger)
	if err != nil {
		return nil, fmt.Errorf("erro ao inicializar o SessionManager: %w", err)
	}
	cli.sessionManager = sessionMgr
	cli.currentSessionName = ""

	contextHandler, err := NewContextHandler(logger)
	if err != nil {
		return nil, fmt.Errorf("erro ao inicializar o ContextHandler: %w", err)
	}
	cli.contextHandler = contextHandler

	// Initialize workspace context (bootstrap files + memory)
	homeDir, _ := os.UserHomeDir()
	globalDir := filepath.Join(homeDir, ".chatcli")
	workspaceDir, _ := os.Getwd()

	// CHATCLI_BOOTSTRAP_DIR overrides the global bootstrap directory
	if envDir := os.Getenv("CHATCLI_BOOTSTRAP_DIR"); envDir != "" {
		globalDir = envDir
	}

	bootstrapEnabled := os.Getenv("CHATCLI_BOOTSTRAP_ENABLED") != "false"
	memoryEnabled := os.Getenv("CHATCLI_MEMORY_ENABLED") != "false"

	var bootstrapLoader *workspace.BootstrapLoader
	if bootstrapEnabled {
		bootstrapLoader = workspace.NewBootstrapLoader(workspaceDir, globalDir, logger)
	} else {
		bootstrapLoader = workspace.NewBootstrapLoader("", "", logger) // noop
		logger.Info("Bootstrap disabled via CHATCLI_BOOTSTRAP_ENABLED=false")
	}

	var memStore *workspace.MemoryStore
	if memoryEnabled {
		memDir := filepath.Join(homeDir, ".chatcli")
		memStore = workspace.NewMemoryStore(memDir, logger)
	} else {
		logger.Info("Memory disabled via CHATCLI_MEMORY_ENABLED=false")
	}
	cli.memoryStore = memStore
	cli.contextBuilder = workspace.NewContextBuilder(bootstrapLoader, memStore)

	// Start background memory annotation worker
	if memoryEnabled {
		cli.memWorker = newMemoryWorker(cli)
		cli.memWorker.start()
	}

	// Initialize persona handler
	cli.personaHandler = NewPersonaHandler(logger)

	// Set project directory for local agents/skills precedence
	if projectDir := detectProjectDir(); projectDir != "" {
		cli.personaHandler.GetManager().SetProjectDir(projectDir)
		logger.Debug("Project directory set for persona", zap.String("dir", projectDir))
	}

	// Initialize MCP server manager
	cli.mcpManager = mcp.NewManager(logger)
	if err := cli.mcpManager.LoadConfig(mcp.DefaultConfigPath()); err != nil {
		logger.Warn("Failed to load MCP config", zap.Error(err))
	}

	// Initialize skill registry handler
	cli.skillHandler = NewSkillHandler(logger, cli.personaHandler.GetManager())

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

// detectProjectDir walks up from the current working directory looking for
// project root markers. Returns the project root path, or "" if none found.
// Priority: .agent (explicit ChatCLI marker) > .git (common convention).
func detectProjectDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, ".agent")); err == nil && info.IsDir() {
			return dir
		}
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
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


func (cli *ChatCLI) Start(ctx context.Context) {
	defer cli.cleanup()
	cli.startBubbleTea(ctx)
}

func (cli *ChatCLI) startBubbleTea(_ context.Context) {
	// Ensure agent mode is initialized for /agent and /coder hints
	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	bridge := newTUIBridge(cli)
	adapter := tui.NewAdapter(bridge)
	app := tui.New(adapter)

	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		cli.logger.Fatal("TUI error", zap.Error(err))
	}
}

func (cli *ChatCLI) cleanup() {
	// Stop background memory worker
	if cli.memWorker != nil {
		cli.memWorker.stop()
	}

	if err := cli.historyManager.AppendAndRotateHistory(cli.newCommandsInSession); err != nil {
		cli.logger.Error("Erro ao salvar histórico", zap.Error(err))
	}
	if cli.Client != nil {
		if assistantClient, ok := cli.Client.(*openai_assistant.OpenAIAssistantClient); ok {
			if err := assistantClient.Cleanup(); err != nil {
				cli.logger.Error("Erro na limpeza do OpenAI Assistant", zap.Error(err))
			}
		}
	}
	if cli.pluginManager != nil {
		cli.pluginManager.Close()
	}
	if err := cli.logger.Sync(); err != nil {
		msg := err.Error()
		if !strings.Contains(msg, "/dev/stdout") &&
			!strings.Contains(msg, "/dev/stderr") &&
			!strings.Contains(msg, "invalid argument") &&
			!strings.Contains(msg, "inappropriate ioctl") {
			fmt.Fprintf(os.Stderr, "Falha ao sincronizar logger: %v\n", err)
		}
	}
}

func (cli *ChatCLI) handleSwitchCommand(userInput string) {
	args := strings.Fields(userInput)
	var newModel, newRealm, newAgentID string
	shouldSwitchModel, shouldUpdateStackSpot := false, false
	maxTokensOverride := -1

	for i := 1; i < len(args); i++ {
		switch args[i] {
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
					fmt.Println(i18n.T("cli.switch.invalid_max_tokens", args[i+1]))
				}
				i++
			}
		case "--realm":
			if i+1 < len(args) {
				newRealm = args[i+1]
				shouldUpdateStackSpot = true
				i++
			}
		case "--agent-id":
			if i+1 < len(args) {
				newAgentID = args[i+1]
				shouldUpdateStackSpot = true
				i++
			}
		}
	}
	if maxTokensOverride != -1 {
		cli.UserMaxTokens = maxTokensOverride
		fmt.Println(i18n.T("cli.switch.max_tokens_set", cli.UserMaxTokens))
	}

	if shouldUpdateStackSpot {
		if cli.Provider != "STACKSPOT" {
			fmt.Println(i18n.T("cli.switch.stackspot_only_flags"))
			return
		}
		if newRealm != "" {
			cli.manager.SetStackSpotRealm(newRealm)
			fmt.Println(i18n.T("cli.switch.realm_updated", newRealm))
		}
		if newAgentID != "" {
			cli.manager.SetStackSpotAgentID(newAgentID)
			newClient, err := cli.manager.GetClient("STACKSPOT", "")
			if err != nil {
				fmt.Println(i18n.T("cli.switch.agent_id_error", err))
			} else {
				cli.Client = newClient
				fmt.Println(i18n.T("cli.switch.agent_id_updated", newAgentID))
			}
		}
	}

	if shouldSwitchModel {
		fmt.Println(i18n.T("cli.switch.changing_model", newModel, cli.Provider))
		newClient, err := cli.manager.GetClient(cli.Provider, newModel)
		if err != nil {
			fmt.Println(i18n.T("cli.switch.change_model_error", newModel, err))
		} else {
			cli.Client = newClient
			cli.Model = newModel
			fmt.Println(i18n.T("cli.switch.change_model_success", cli.Client.GetModelName(), cli.Provider))
		}
		return
	}

	if !shouldSwitchModel && maxTokensOverride == -1 && len(args) == 1 {
		cli.switchProvider()
	}
}

func (cli *ChatCLI) switchProvider() {
	fmt.Println(i18n.T("cli.switch.available_providers"))
	availableProviders := cli.manager.GetAvailableProviders()
	for i, provider := range availableProviders {
		fmt.Printf("%d. %s\n", i+1, provider)
	}
	cli.interactionState = StateSwitchingProvider
}

func (cli *ChatCLI) showHelp() {
	printCommand := func(cmd, desc string) {
		cmdColor := ColorCyan
		descColor := ColorGray
		if strings.HasPrefix(cmd, "  ") {
			cmdColor = ColorGray
			descColor = ColorGray
		}
		fmt.Printf("    %s    %s\n", colorize(fmt.Sprintf("%-32s", cmd), cmdColor), colorize(desc, descColor))
	}

	fmt.Println("\n" + colorize(ColorBold, i18n.T("help.header.title")))
	fmt.Println(colorize(i18n.T("help.header.subtitle1"), ColorGray))
	fmt.Println(colorize(i18n.T("help.header.subtitle2"), ColorGray))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.general"), ColorLime))
	printCommand("/help", i18n.T("help.command.help"))
	printCommand("/exit | /quit", i18n.T("help.command.exit"))
	printCommand("/newsession", i18n.T("help.command.newsession"))
	printCommand("/version | /v", i18n.T("help.command.version"))
	printCommand("/compact [instruction]", "Compact history (guided: /compact keep only API patterns)")
	printCommand("/rewind", "Rewind to a previous conversation checkpoint")
	printCommand("Esc+Esc", "Quick rewind (when input is empty)")
	printCommand("/memory [subcommand]", "View/load memory notes (today, yesterday, week, longterm, list, load <date>)")

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.config"), ColorLime))
	printCommand("/switch", i18n.T("help.command.switch"))
	printCommand("/switch --model <nome>", i18n.T("help.command.switch_model"))
	printCommand("  Ex: /switch --model gpt-4o-mini", i18n.T("help.command.switch_model_example"))
	printCommand("/switch --max-tokens <num>", i18n.T("help.command.switch_max_tokens"))
	printCommand("/switch --realm <realm>", i18n.T("help.command.switch_realm"))
	printCommand("/switch --agent-id <id>", i18n.T("help.command.switch_agent_id"))
	printCommand("/config | /status", i18n.T("help.command.config"))
	printCommand("/reload", i18n.T("help.command.reload"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.context"), ColorLime))
	printCommand("@file <caminho>", i18n.T("help.command.file"))
	printCommand("  --mode full", i18n.T("help.command.file_mode_full"))
	printCommand("  --mode chunked", i18n.T("help.command.file_mode_chunked"))
	printCommand("  --mode summary", i18n.T("help.command.file_mode_summary"))
	printCommand("  --mode smart", i18n.T("help.command.file_mode_smart"))
	printCommand("  Ex: @file --mode=smart ./src ...", i18n.T("help.command.file_mode_example"))
	printCommand("@git", i18n.T("help.command.git"))
	printCommand("@history", i18n.T("help.command.history"))
	printCommand("@env", i18n.T("help.command.env"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.chunks"), ColorLime))
	printCommand("/nextchunk", i18n.T("help.command.nextchunk"))
	printCommand("/retry", i18n.T("help.command.retry"))
	printCommand("/retryall", i18n.T("help.command.retryall"))
	printCommand("/skipchunk", i18n.T("help.command.skipchunk"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.contexts"), ColorLime))
	printCommand("/context create <nome> <paths...>", i18n.T("help.command.context_create"))
	printCommand("  --mode <modo>", i18n.T("help.command.context_mode"))
	printCommand("  --description <texto>", i18n.T("help.command.context_description"))
	printCommand("  --tags <tag1,tag2>", i18n.T("help.command.context_tags"))
	printCommand("/context attach <nome>", i18n.T("help.command.context_attach"))
	printCommand("/context detach <nome>", i18n.T("help.command.context_detach"))
	printCommand("/context list", i18n.T("help.command.context_list"))
	printCommand("/context show <nome>", i18n.T("help.command.context_show"))
	printCommand("/context delete <nome>", i18n.T("help.command.context_delete"))
	printCommand("/context merge <novo> <ctx1> <ctx2>...", i18n.T("help.command.context_merge"))
	printCommand("/context attached", i18n.T("help.command.context_attached"))
	printCommand("/context export <nome> <arquivo>", i18n.T("help.command.context_export"))
	printCommand("/context import <arquivo>", i18n.T("help.command.context_import"))
	printCommand("/context metrics", i18n.T("help.command.context_metrics"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.exec"), ColorLime))
	printCommand("@command <cmd>", i18n.T("help.command.command"))
	printCommand("  Ex: @command ls -la", i18n.T("help.command.command_example"))
	printCommand("@command -i <cmd>", i18n.T("help.command.command_i"))
	printCommand("@command --ai <cmd>", i18n.T("help.command.command_ai"))
	printCommand("  Ex: @command --ai git diff", i18n.T("help.command.command_ai_example"))
	printCommand("@command --ai <cmd> > <texto>", i18n.T("help.command.command_ai_context"))
	printCommand("  Ex: @command --ai cat err.log > ...", i18n.T("help.command.command_ai_context_example"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.agent"), ColorLime))
	printCommand("/agent <tarefa>", i18n.T("help.command.agent"))
	printCommand("/run <tarefa>", i18n.T("help.command.run"))
	printCommand("  Ex: /agent ...", i18n.T("help.command.agent_example"))
	printCommand(i18n.T("help.command.agent_inside"), "")
	printCommand("  [1..N]", i18n.T("help.command.agent_exec_n"))
	printCommand("  a", i18n.T("help.command.agent_exec_all"))
	printCommand("  eN", i18n.T("help.command.agent_edit"))
	printCommand("  tN", i18n.T("help.command.agent_dry_run"))
	printCommand("  cN", i18n.T("help.command.agent_continue"))
	printCommand("  pcN", i18n.T("help.command.agent_pre_context"))
	printCommand("  acN", i18n.T("help.command.agent_post_context"))
	printCommand("  vN", i18n.T("help.command.agent_view"))
	printCommand("  wN", i18n.T("help.command.agent_save"))
	printCommand("  p", i18n.T("help.command.agent_toggle_plan"))
	printCommand("  r", i18n.T("help.command.agent_redraw"))
	printCommand("  q", i18n.T("help.command.agent_quit"))
	printCommand(i18n.T("help.command.agent_notes"), "")
	printCommand("  "+i18n.T("help.command.agent_last_result"), "")
	printCommand("  "+i18n.T("help.command.agent_compact_plan"), "")
	printCommand("  "+i18n.T("help.command.agent_full_plan"), "")

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.coder"), ColorLime))
	printCommand("/coder <tarefa>", i18n.T("help.command.coder"))
	printCommand("  Ex: /coder ...", i18n.T("help.command.coder_example"))

	printCommand(i18n.T("help.command.coder_notes"), "")
	printCommand("  "+i18n.T("help.command.coder_note_plugin"), "")
	printCommand("  "+i18n.T("help.command.coder_note_auto_tools"), "")

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.plugins"), ColorLime))
	printCommand("/plugin list", i18n.T("help.command.plugin_list"))
	printCommand("/plugin install <url>", i18n.T("help.command.plugin_install"))
	printCommand("/plugin show <nome>", i18n.T("help.command.plugin_show"))
	printCommand("/plugin inspect <nome>", i18n.T("help.command.plugin_inspect"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.persona"), ColorLime))
	printCommand("/agent list", i18n.T("help.command.persona_list"))
	printCommand("/agent load <nome>", i18n.T("help.command.persona_load"))
	printCommand("/agent skills", i18n.T("help.command.persona_skills"))
	printCommand("/agent show", i18n.T("help.command.persona_show"))
	printCommand("/agent status", i18n.T("help.command.persona_status"))
	printCommand("/agent off", i18n.T("help.command.persona_off"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.sessions"), ColorLime))
	printCommand("/session save <nome>", i18n.T("help.command.session_save"))
	printCommand("/session load <nome>", i18n.T("help.command.session_load"))
	printCommand("/session list", i18n.T("help.command.session_list"))
	printCommand("/session delete <nome>", i18n.T("help.command.session_delete"))
	printCommand("/session new", i18n.T("help.command.session_new"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.oneshot"), ColorLime))
	printCommand("chatcli -p \"<prompt>\"", i18n.T("help.command.oneshot_p"))
	printCommand("  Ex: chatcli -p \"...\"", i18n.T("help.command.oneshot_p_example"))
	printCommand("chatcli --prompt \"<prompt>\"", i18n.T("help.command.oneshot_prompt"))
	printCommand("--provider <nome>", i18n.T("help.command.oneshot_provider"))
	printCommand("--model <nome>", i18n.T("help.command.oneshot_model"))
	printCommand("--agent-id <id>", i18n.T("help.command.oneshot_agent_id"))
	printCommand("--max-tokens <num>", i18n.T("help.command.oneshot_max_tokens"))
	printCommand("--timeout <duração>", i18n.T("help.command.oneshot_timeout"))
	printCommand("--no-anim", i18n.T("help.command.oneshot_no_anim"))
	printCommand("--agent-auto-exec", i18n.T("help.command.oneshot_auto_exec"))
	printCommand(i18n.T("help.command.oneshot_pipes"), "")

	fmt.Printf("\n  %s\n", colorize(i18n.T("help.section.tips"), ColorLime))
	printCommand("Cancelamento (Ctrl+C)", i18n.T("help.command.tips_cancel"))
	printCommand("Saída Rápida (Ctrl+D)", i18n.T("help.command.tips_exit"))
	printCommand("Operador '>'", i18n.T("help.command.tips_operator"))
	printCommand("Modo Agente: p", i18n.T("help.command.tips_agent_p"))
	printCommand("Modo Agente: vN", i18n.T("help.command.tips_agent_v"))
	printCommand("Modo Agente: wN", i18n.T("help.command.tips_agent_w"))
	printCommand("Modo Agente: r", i18n.T("help.command.tips_agent_r"))

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

// SetWatching configures the K8s watcher state for the CLI.
func (cli *ChatCLI) SetWatching(active bool, statusFunc func() string) {
	cli.isWatching = active
	cli.watchStatusFunc = statusFunc
}

// StartWatcher creates and starts a K8s watcher in background from interactive mode.
func (cli *ChatCLI) StartWatcher(cfg k8s.WatchConfig) error {
	if cli.isWatching {
		return fmt.Errorf("watcher already running, use /watch stop first")
	}

	watcher, err := k8s.NewResourceWatcher(cfg, cli.logger)
	if err != nil {
		return fmt.Errorf("failed to create K8s watcher: %w", err)
	}

	store := watcher.GetStore()
	summarizer := k8s.NewSummarizer(store)

	watchCtx, watchCancel := context.WithCancel(context.Background())
	cli.watcherCancel = watchCancel

	watcherReady := make(chan struct{}, 1)
	go func() {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.After(15 * time.Second)
			for {
				select {
				case <-ticker.C:
					if _, ok := store.LatestSnapshot(); ok {
						watcherReady <- struct{}{}
						return
					}
				case <-timeout:
					watcherReady <- struct{}{}
					return
				}
			}
		}()

		if err := watcher.Start(watchCtx); err != nil && err != context.Canceled {
			cli.logger.Error("K8s watcher stopped with error", zap.Error(err))
		}
	}()

	// Wait for first collection
	<-watcherReady

	cli.WatcherContextFunc = summarizer.GenerateContext
	cli.SetWatching(true, summarizer.GenerateStatusSummary)

	if _, ok := store.LatestSnapshot(); ok {
		cli.logger.Info("K8s watcher started with initial data",
			zap.String("deployment", cfg.Deployment),
			zap.String("namespace", cfg.Namespace))
	}

	return nil
}

// StopWatcher stops the running K8s watcher if any.
func (cli *ChatCLI) StopWatcher() {
	if cli.watcherCancel != nil {
		cli.watcherCancel()
		cli.watcherCancel = nil
	}
	cli.WatcherContextFunc = nil
	cli.SetWatching(false, nil)
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
		if v := os.Getenv("ANTHROPIC_MAX_TOKENS"); v != "" {
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
	} else if strings.ToUpper(cli.Provider) == "STACKSPOT" {
		if v := os.Getenv("STACKSPOT_MAX_TOKENS"); v != "" {
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
			fmt.Println(i18n.T("chunk.no_more_pending_but_failed", len(cli.failedChunks)))
		} else {
			fmt.Println(i18n.T("chunk.no_more_chunks"))
		}
		return false
	}

	nextChunk := cli.fileChunks[0]
	totalChunks := nextChunk.Total
	currentChunk := nextChunk.Index
	remainingChunks := len(cli.fileChunks) - 1

	fmt.Println(i18n.T("chunk.sending", currentChunk, totalChunks, remainingChunks))

	progressInfo := fmt.Sprintf(i18n.T("chunk.progress_header", currentChunk, totalChunks)+"\n"+
		"=============================\n"+
		i18n.T("chunk.progress_processed", currentChunk-1)+"\n"+
		i18n.T("chunk.progress_pending", remainingChunks)+"\n"+
		i18n.T("chunk.progress_failed", len(cli.failedChunks))+"\n"+
		i18n.T("chunk.progress_usage")+"\n\n"+
		"=============================\n\n",
		currentChunk, totalChunks, currentChunk-1, remainingChunks, len(cli.failedChunks))

	prompt := i18n.T("chunk.prompt", currentChunk, totalChunks)

	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: prompt + "\n\n" + progressInfo + nextChunk.Content,
	})

	cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	aiResponse, err := cli.Client.SendPrompt(ctx, prompt+"\n\n"+nextChunk.Content, cli.history, 0)
	cli.animation.StopThinkingAnimation()

	if err != nil {
		cli.logger.Error("Erro ao processar chunk com a LLM", zap.Int("chunkIndex", nextChunk.Index), zap.Int("totalChunks", nextChunk.Total), zap.Error(err))
		cli.lastFailedChunk = &cli.fileChunks[0]
		cli.failedChunks = append(cli.failedChunks, cli.fileChunks[0])
		cli.fileChunks = cli.fileChunks[1:]

		fmt.Println(i18n.T("chunk.error_processing", currentChunk, totalChunks, err.Error()))
		fmt.Println(i18n.T("chunk.moved_to_failed_queue"))
		fmt.Println(i18n.T("chunk.retry_or_next"))
		return false
	}

	cli.history = append(cli.history, models.Message{Role: "assistant", Content: aiResponse})
	renderedResponse := cli.renderMarkdown(aiResponse)
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.Client.GetModelName(), renderedResponse), 2*time.Millisecond)
	cli.fileChunks = cli.fileChunks[1:]

	if len(cli.fileChunks) > 0 || len(cli.failedChunks) > 0 {
		cli.printChunkStatus()
	} else {
		fmt.Println(i18n.T("chunk.all_processed_success"))
	}
	return false
}

// Novo método para reprocessar o último chunk que falhou
func (cli *ChatCLI) handleRetryLastChunk() bool {
	if cli.lastFailedChunk == nil || len(cli.failedChunks) == 0 {
		fmt.Println(i18n.T("chunk.no_failed_to_retry"))
		return false
	}

	lastFailedIndex := len(cli.failedChunks) - 1
	chunk := cli.failedChunks[lastFailedIndex]
	cli.failedChunks = cli.failedChunks[:lastFailedIndex]

	fmt.Println(i18n.T("chunk.retrying_last_failed", chunk.Index, chunk.Total))

	progressInfo := fmt.Sprintf(i18n.T("chunk.retry_header", chunk.Index, chunk.Total)+"\n"+
		"=============================\n"+
		i18n.T("chunk.retry_status_retrying")+"\n"+
		i18n.T("chunk.progress_pending", len(cli.fileChunks))+"\n"+
		i18n.T("chunk.retry_status_other_failed", len(cli.failedChunks))+"\n"+
		"=============================\n\n",
		chunk.Index, chunk.Total, len(cli.fileChunks), len(cli.failedChunks))

	prompt := i18n.T("chunk.retry_prompt", chunk.Index, chunk.Total)

	cli.history = append(cli.history, models.Message{Role: "user", Content: prompt + "\n\n" + progressInfo + chunk.Content})
	cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	aiResponse, err := cli.Client.SendPrompt(ctx, prompt+"\n\n"+chunk.Content, cli.history, 0)
	cli.animation.StopThinkingAnimation()

	if err != nil {
		cli.logger.Error("Erro ao reprocessar chunk com a LLM", zap.Int("chunkIndex", chunk.Index), zap.Error(err))
		cli.failedChunks = append(cli.failedChunks, chunk)
		fmt.Println(i18n.T("chunk.retry_error", chunk.Index, chunk.Total, err.Error()))
		fmt.Println(i18n.T("chunk.retry_remains_failed"))
		return false
	}

	cli.history = append(cli.history, models.Message{Role: "assistant", Content: aiResponse})
	renderedResponse := cli.renderMarkdown(aiResponse)
	cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", cli.Client.GetModelName(), renderedResponse), 2*time.Millisecond)

	if len(cli.failedChunks) > 0 {
		cli.lastFailedChunk = &cli.failedChunks[len(cli.failedChunks)-1]
	} else {
		cli.lastFailedChunk = nil
	}
	fmt.Println(i18n.T("chunk.retry_success"))
	cli.printChunkStatus()
	return false
}

// Método para reprocessar todos os chunks com falha
func (cli *ChatCLI) handleRetryAllChunks() bool {
	if len(cli.failedChunks) == 0 {
		fmt.Println(i18n.T("chunk.no_failed_to_retry"))
		return false
	}
	fmt.Println(i18n.T("chunk.retrying_all_failed", len(cli.failedChunks)))
	cli.fileChunks = append(cli.failedChunks, cli.fileChunks...)
	cli.failedChunks = []FileChunk{}
	cli.lastFailedChunk = nil
	return cli.handleNextChunk()
}

// Método para pular explicitamente um chunk
func (cli *ChatCLI) handleSkipChunk() bool {
	if len(cli.fileChunks) == 0 {
		fmt.Println(i18n.T("chunk.no_pending_to_skip"))
		return false
	}
	skippedChunk := cli.fileChunks[0]
	cli.fileChunks = cli.fileChunks[1:]
	fmt.Println(i18n.T("chunk.skipping", skippedChunk.Index, skippedChunk.Total))
	cli.printChunkStatus()
	return false
}

// Método auxiliar para imprimir o status dos chunks
func (cli *ChatCLI) printChunkStatus() {
	fmt.Println(i18n.T("chunk.status_header"))
	if len(cli.fileChunks) > 0 {
		fmt.Println(i18n.T("chunk.status_pending", len(cli.fileChunks)))
	} else {
		fmt.Println(i18n.T("chunk.status_no_pending"))
	}
	if len(cli.failedChunks) > 0 {
		fmt.Println(i18n.T("chunk.status_failed", len(cli.failedChunks)))
	} else {
		fmt.Println(i18n.T("chunk.status_no_failed"))
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
	shellCommand := fmt.Sprintf("source %s && %s", utils.ShellQuote(shellConfigPath), command)

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
// renderMarkdown renderiza o texto em Markdown
func (cli *ChatCLI) renderMarkdown(input string) string {
	// Ajustar a largura para o tamanho do terminal
	//width, _, err := utils.GetTerminalSize()
	//if err != nil || width <= 0 {
	//	width = 80 // valor padrão
	//}
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(0),
	)
	out, err := renderer.Render(input)
	if err != nil {
		return input
	}

	// Garantir que não há sequências ANSI pendentes
	out = strings.TrimRight(out, " \n\t")
	if !strings.HasSuffix(out, "\033[0m") {
		out += "\033[0m"
	}

	return out
}

// ensureANSIReset garante que string termina com reset ANSI
func ensureANSIReset(s string) string {
	if !strings.HasSuffix(s, "\033[0m") && !strings.HasSuffix(s, "\033[m") {
		return s + "\033[0m"
	}
	return s
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

	fmt.Println("\n" + colorize(ColorBold, i18n.T("cli.config.header")))
	fmt.Println(colorize(i18n.T("cli.config.subtitle"), ColorGray))

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_general"), ColorLime))
	printItem(i18n.T("cli.config.key_dotenv_file"), cli.getEnvFilePath())
	printItem(i18n.T("cli.config.key_env"), os.Getenv("ENV"))
	printItem(i18n.T("cli.config.key_log_level"), os.Getenv("LOG_LEVEL"))
	printItem(i18n.T("cli.config.key_log_file"), os.Getenv("LOG_FILE"))
	printItem(i18n.T("cli.config.key_log_max_size"), os.Getenv("LOG_MAX_SIZE"))
	printItem(i18n.T("cli.config.key_history_max_size"), os.Getenv("HISTORY_MAX_SIZE"))
	printItem(i18n.T("cli.config.key_history_file_directory"), cli.historyManager.GetHistoryFilePath())

	fmt.Printf("\n  %s\n", colorize("Coder Mode", ColorLime))
	coderUIRaw := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_UI")))
	coderUIEffective := "full"
	if coderUIRaw == "minimal" || coderUIRaw == "min" || coderUIRaw == "true" || coderUIRaw == "1" {
		coderUIEffective = "minimal"
	}
	coderBannerRaw := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_BANNER")))
	coderBannerEffective := "on"
	if coderBannerRaw == "false" || coderBannerRaw == "0" || coderBannerRaw == "no" {
		coderBannerEffective = "off"
	}
	printItem("CHATCLI_CODER_UI", os.Getenv("CHATCLI_CODER_UI"))
	printItem("CHATCLI_CODER_UI (effective)", coderUIEffective)
	printItem("CHATCLI_CODER_BANNER", os.Getenv("CHATCLI_CODER_BANNER"))
	printItem("CHATCLI_CODER_BANNER (effective)", coderBannerEffective)

	policyPath := "[unknown]"
	localPath := "[none]"
	localMerge := "off"
	rulesCount := "0"
	lastRule := "[none]"
	if pm, err := coder.NewPolicyManager(cli.logger); err == nil {
		policyPath = pm.ActivePolicyPath()
		if lp := pm.LocalPolicyPath(); strings.TrimSpace(lp) != "" {
			localPath = lp
			if pm.LocalMergeEnabled() {
				localMerge = "on"
			}
		}
		rulesCount = fmt.Sprintf("%d", pm.RulesCount())
	}
	printItem("CODER_POLICY (active)", policyPath)
	printItem("CODER_POLICY (local)", localPath)
	printItem("CODER_POLICY (local merge)", localMerge)
	printItem("CODER_POLICY (rules)", rulesCount)
	if cli.agentMode != nil && cli.agentMode.lastPolicyMatch != nil {
		lastRule = fmt.Sprintf("%s => %s", cli.agentMode.lastPolicyMatch.Pattern, cli.agentMode.lastPolicyMatch.Action)
	}
	printItem("CODER_POLICY (last match)", lastRule)

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_current_provider"), ColorLime))
	printItem(i18n.T("cli.config.key_provider_runtime"), cli.Provider)
	printItem(i18n.T("cli.config.key_model_runtime"), cli.Model)
	if cli.Client != nil {
		printItem(i18n.T("cli.config.key_model_name_client"), cli.Client.GetModelName())
	} else {
		printItem(i18n.T("cli.config.key_model_name_client"), "(no provider)")
	}
	printItem(i18n.T("cli.config.key_preferred_api"), string(catalog.GetPreferredAPI(cli.Provider, cli.Model)))
	printItem(i18n.T("cli.config.key_effective_max_tokens"), fmt.Sprintf("%d", cli.getMaxTokensForCurrentLLM()))

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_max_tokens_overrides"), ColorLime))
	printItem("OPENAI_MAX_TOKENS", os.Getenv("OPENAI_MAX_TOKENS"))
	printItem("ANTHROPIC_MAX_TOKENS", os.Getenv("ANTHROPIC_MAX_TOKENS"))
	printItem("GOOGLEAI_MAX_TOKENS", os.Getenv("GOOGLEAI_MAX_TOKENS"))
	printItem("XAI_MAX_TOKENS", os.Getenv("XAI_MAX_TOKENS"))
	printItem("OLLAMA_MAX_TOKENS", os.Getenv("OLLAMA_MAX_TOKENS"))
	printItem("STACKSPOT_MAX_TOKENS", os.Getenv("STACKSPOT_MAX_TOKENS"))

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_sensitive_keys"), ColorLime))
	printItem("OPENAI_API_KEY", presence(os.Getenv("OPENAI_API_KEY")))
	printItem("ANTHROPIC_API_KEY", presence(os.Getenv("ANTHROPIC_API_KEY")))
	printItem("GOOGLEAI_API_KEY", presence(os.Getenv("GOOGLEAI_API_KEY")))
	printItem("XAI_API_KEY", presence(os.Getenv("XAI_API_KEY")))
	printItem(i18n.T("cli.config.key_client_id_stackspot"), presence(os.Getenv("CLIENT_ID")))
	printItem(i18n.T("cli.config.key_client_key_stackspot"), presence(os.Getenv("CLIENT_KEY")))

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_provider_settings"), ColorLime))
	printItem("OPENAI_MODEL", os.Getenv("OPENAI_MODEL"))
	printItem("OPENAI_ASSISTANT_MODEL", os.Getenv("OPENAI_ASSISTANT_MODEL"))
	printItem("OPENAI_USE_RESPONSES", os.Getenv("OPENAI_USE_RESPONSES"))
	printItem("ANTHROPIC_MODEL", os.Getenv("ANTHROPIC_MODEL"))
	printItem("ANTHROPIC_API_VERSION", os.Getenv("ANTHROPIC_API_VERSION"))
	printItem("GOOGLEAI_MODEL", os.Getenv("GOOGLEAI_MODEL"))
	printItem("XAI_MODEL", os.Getenv("XAI_MODEL"))
	printItem("OLLAMA_MODEL", os.Getenv("OLLAMA_MODEL"))
	printItem("OLLAMA_BASE_URL", utils.GetEnvOrDefault("OLLAMA_BASE_URL", config.OllamaDefaultBaseURL))

	isStackSpotAvailable := false
	for _, p := range cli.manager.GetAvailableProviders() {
		if p == "STACKSPOT" {
			isStackSpotAvailable = true
			break
		}
	}
	if cli.Provider == "STACKSPOT" || isStackSpotAvailable {
		printItem(i18n.T("cli.config.key_stackspot_realm"), cli.manager.GetStackSpotRealm())
		printItem(i18n.T("cli.config.key_stackspot_agent_id"), cli.manager.GetStackSpotAgentID())
	}

	fmt.Printf("\n  %s\n", colorize(i18n.T("cli.config.section_available_providers"), ColorLime))
	providers := cli.manager.GetAvailableProviders()
	if len(providers) > 0 {
		for i, p := range providers {
			printItem(i18n.T("cli.config.key_provider_n", i+1), p)
		}
	} else {
		printItem("Nenhum", i18n.T("cli.config.no_providers_configured"))
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

// getRemoteClient extracts the *remote.Client from cli.Client via type assertion.
func (cli *ChatCLI) getRemoteClient() *remote.Client {
	if rc, ok := cli.Client.(*remote.Client); ok {
		return rc
	}
	return nil
}

// askSessionChoice displays an interactive prompt with the given options and returns the user's choice.
// options is a list of i18n keys to display; validChoices maps single-char inputs to return values.
func askSessionChoice(optionKeys []string, validChoices map[string]string, defaultChoice string) string {
	for _, key := range optionKeys {
		fmt.Println(i18n.T(key))
	}
	fmt.Print(i18n.T("session.prompt_choice"))

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if val, ok := validChoices[input]; ok {
		return val
	}
	return defaultChoice
}

// remoteSessionCtx creates a context with a 10-second timeout for remote session operations.
func remoteSessionCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (cli *ChatCLI) handleSaveSession(name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(i18n.T("session.error_save", fmt.Errorf("remote client unavailable")))
			return
		}

		fmt.Println(i18n.T("session.save_where_prompt", name))
		choice := askSessionChoice(
			[]string{"session.save_option_local", "session.save_option_remote", "session.save_option_both"},
			map[string]string{"l": "local", "r": "remote", "b": "both"},
			"local",
		)

		sd := cli.buildSessionData()

		switch choice {
		case "remote":
			ctx, cancel := remoteSessionCtx()
			defer cancel()
			if err := rc.SaveSessionV2(ctx, name, sd); err != nil {
				fmt.Println(i18n.T("session.error_save", err))
			} else {
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.save_success_remote", name))
			}
		case "both":
			var localErr, remoteErr error
			localErr = cli.sessionManager.SaveSessionV2(name, sd)
			ctx, cancel := remoteSessionCtx()
			defer cancel()
			remoteErr = rc.SaveSessionV2(ctx, name, sd)

			if localErr != nil {
				fmt.Println(i18n.T("session.error_save", fmt.Errorf("local: %w", localErr)))
			}
			if remoteErr != nil {
				fmt.Println(i18n.T("session.error_save", fmt.Errorf("remote: %w", remoteErr)))
			}
			if localErr == nil && remoteErr == nil {
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.save_success_both", name))
			}
		default: // "local"
			if err := cli.sessionManager.SaveSessionV2(name, sd); err != nil {
				fmt.Println(i18n.T("session.error_save", err))
			} else {
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.save_success", name))
			}
		}
		return
	}

	// Local only (not connected)
	sd := cli.buildSessionData()
	if err := cli.sessionManager.SaveSessionV2(name, sd); err != nil {
		fmt.Println(i18n.T("session.error_save", err))
	} else {
		cli.currentSessionName = name
		fmt.Println(i18n.T("session.save_success", name))
	}
}

func (cli *ChatCLI) handleLoadSession(name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(i18n.T("session.error_load", fmt.Errorf("remote client unavailable")))
			return
		}

		// Check both sources
		localSD, localErr := cli.sessionManager.LoadSessionV2(name)
		ctx, cancel := remoteSessionCtx()
		defer cancel()
		remoteSD, remoteErr := rc.LoadSessionV2(ctx, name)

		foundLocal := localErr == nil
		foundRemote := remoteErr == nil

		switch {
		case foundLocal && foundRemote:
			// Found in both — ask user
			fmt.Println(i18n.T("session.load_found_both", name))
			choice := askSessionChoice(
				[]string{"session.option_local", "session.option_remote"},
				map[string]string{"l": "local", "r": "remote"},
				"local",
			)
			if choice == "remote" {
				cli.restoreSessionData(remoteSD)
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.load_success_remote", name))
			} else {
				cli.restoreSessionData(localSD)
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.load_success", name))
			}
		case foundLocal:
			cli.restoreSessionData(localSD)
			cli.currentSessionName = name
			fmt.Println(i18n.T("session.load_success", name))
		case foundRemote:
			cli.restoreSessionData(remoteSD)
			cli.currentSessionName = name
			fmt.Println(i18n.T("session.load_success_remote", name))
		default:
			fmt.Println(i18n.T("session.error_load", localErr))
		}
		return
	}

	// Local only
	sd, err := cli.sessionManager.LoadSessionV2(name)
	if err != nil {
		fmt.Println(i18n.T("session.error_load", err))
	} else {
		cli.restoreSessionData(sd)
		cli.currentSessionName = name
		fmt.Println(i18n.T("session.load_success", name))
	}
}

// clearAllHistories resets the unified history.
func (cli *ChatCLI) clearAllHistories() {
	cli.history = make([]models.Message, 0)
	cli.checkpoints = nil
}

// buildSessionData builds a SessionData from the current CLI state.
// Uses ChatHistory field to store the unified history for backwards compatibility.
func (cli *ChatCLI) buildSessionData() *SessionData {
	return &SessionData{
		Version:     2,
		ChatHistory: cli.history,
	}
}

// restoreSessionData restores history from a SessionData.
// Merges legacy separate histories into the unified history for backwards compatibility.
func (cli *ChatCLI) restoreSessionData(sd *SessionData) {
	cli.history = sd.ChatHistory
	if cli.history == nil {
		cli.history = make([]models.Message, 0)
	}

	// Backwards compatibility: merge legacy separate histories if present
	if len(sd.AgentHistory) > 0 || len(sd.CoderHistory) > 0 {
		// Append non-system messages from legacy agent/coder histories
		for _, msg := range sd.AgentHistory {
			if msg.Role != "system" {
				cli.history = append(cli.history, msg)
			}
		}
		for _, msg := range sd.CoderHistory {
			if msg.Role != "system" {
				cli.history = append(cli.history, msg)
			}
		}
	}

	cli.checkpoints = nil
}

func (cli *ChatCLI) handleListSessions() {
	if cli.isRemote {
		rc := cli.getRemoteClient()

		// Fetch both sources
		localSessions, localErr := cli.sessionManager.ListSessions()
		var remoteSessions []string
		var remoteErr error
		if rc != nil {
			ctx, cancel := remoteSessionCtx()
			defer cancel()
			remoteSessions, remoteErr = rc.ListSessions(ctx)
		}

		hasLocal := localErr == nil && len(localSessions) > 0
		hasRemote := remoteErr == nil && len(remoteSessions) > 0

		if !hasLocal && !hasRemote {
			if localErr != nil {
				fmt.Println(i18n.T("session.error_list", localErr))
			}
			if remoteErr != nil {
				fmt.Println(i18n.T("session.error_list", remoteErr))
			}
			if localErr == nil && remoteErr == nil {
				fmt.Println(i18n.T("session.list_empty"))
			}
			return
		}

		if hasLocal {
			fmt.Println(i18n.T("session.list_header_local"))
			for _, s := range localSessions {
				fmt.Printf("  - %s\n", s)
			}
		}
		if hasRemote {
			if hasLocal {
				fmt.Println()
			}
			fmt.Println(i18n.T("session.list_header_remote"))
			for _, s := range remoteSessions {
				fmt.Printf("  - %s\n", s)
			}
		}
		return
	}

	// Local only
	sessions, err := cli.sessionManager.ListSessions()
	if err != nil {
		fmt.Println(i18n.T("session.error_list", err))
		return
	}
	if len(sessions) == 0 {
		fmt.Println(i18n.T("session.list_empty"))
		return
	}
	fmt.Println(i18n.T("session.list_header"))
	for _, session := range sessions {
		fmt.Printf("- %s\n", session)
	}
}

func (cli *ChatCLI) handleDeleteSession(name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(i18n.T("session.error_delete", fmt.Errorf("remote client unavailable")))
			return
		}

		// Check both sources
		_, localErr := cli.sessionManager.LoadSession(name)
		ctx, cancel := remoteSessionCtx()
		defer cancel()
		_, remoteErr := rc.LoadSession(ctx, name)

		foundLocal := localErr == nil
		foundRemote := remoteErr == nil

		switch {
		case foundLocal && foundRemote:
			fmt.Println(i18n.T("session.delete_found_both", name))
			choice := askSessionChoice(
				[]string{"session.option_local", "session.option_remote", "session.option_both"},
				map[string]string{"l": "local", "r": "remote", "b": "both"},
				"local",
			)
			switch choice {
			case "remote":
				ctxDel, cancelDel := remoteSessionCtx()
				defer cancelDel()
				if err := rc.DeleteSession(ctxDel, name); err != nil {
					fmt.Println(i18n.T("session.error_delete", err))
				} else {
					fmt.Println(i18n.T("session.delete_success_remote", name))
				}
			case "both":
				localDelErr := cli.sessionManager.DeleteSession(name)
				ctxDel, cancelDel := remoteSessionCtx()
				defer cancelDel()
				remoteDelErr := rc.DeleteSession(ctxDel, name)
				if localDelErr != nil {
					fmt.Println(i18n.T("session.error_delete", fmt.Errorf("local: %w", localDelErr)))
				}
				if remoteDelErr != nil {
					fmt.Println(i18n.T("session.error_delete", fmt.Errorf("remote: %w", remoteDelErr)))
				}
				if localDelErr == nil && remoteDelErr == nil {
					fmt.Println(i18n.T("session.delete_success_both", name))
					if cli.currentSessionName == name {
						cli.clearAllHistories()
						cli.currentSessionName = ""
						fmt.Println(i18n.T("session.delete_active_cleared"))
					}
				}
			default: // "local"
				if err := cli.sessionManager.DeleteSession(name); err != nil {
					fmt.Println(i18n.T("session.error_delete", err))
				} else {
					fmt.Println(i18n.T("session.delete_success", name))
					if cli.currentSessionName == name {
						cli.clearAllHistories()
						cli.currentSessionName = ""
						fmt.Println(i18n.T("session.delete_active_cleared"))
					}
				}
			}
		case foundLocal:
			if err := cli.sessionManager.DeleteSession(name); err != nil {
				fmt.Println(i18n.T("session.error_delete", err))
			} else {
				fmt.Println(i18n.T("session.delete_success", name))
				if cli.currentSessionName == name {
					cli.clearAllHistories()
					cli.currentSessionName = ""
					fmt.Println(i18n.T("session.delete_active_cleared"))
				}
			}
		case foundRemote:
			ctxDel, cancelDel := remoteSessionCtx()
			defer cancelDel()
			if err := rc.DeleteSession(ctxDel, name); err != nil {
				fmt.Println(i18n.T("session.error_delete", err))
			} else {
				fmt.Println(i18n.T("session.delete_success_remote", name))
				if cli.currentSessionName == name {
					cli.clearAllHistories()
					cli.currentSessionName = ""
					fmt.Println(i18n.T("session.delete_active_cleared"))
				}
			}
		default:
			fmt.Println(i18n.T("session.error_delete", localErr))
		}
		return
	}

	// Local only
	if err := cli.sessionManager.DeleteSession(name); err != nil {
		fmt.Println(i18n.T("session.error_delete", err))
	} else {
		fmt.Println(i18n.T("session.delete_success", name))
		if cli.currentSessionName == name {
			cli.clearAllHistories()
			cli.currentSessionName = ""
			fmt.Println(i18n.T("session.delete_active_cleared"))
		}
	}
}
