/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"

	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

type CommandHandler struct {
	cli *ChatCLI
}

func NewCommandHandler(cli *ChatCLI) *CommandHandler {
	return &CommandHandler{cli: cli}
}

func (ch *CommandHandler) HandleCommand(userInput string) bool {
	switch {
	case userInput == "/exit" || userInput == "exit" || userInput == "/quit" || userInput == "quit":
		fmt.Println(i18n.T("status.exiting"))
		return true
	case userInput == "/reload":
		ch.cli.reloadConfiguration()
		return false
	case strings.HasPrefix(userInput, "/agent"):
		// /agent pode ser gerenciamento de personas OU iniciar modo agente
		if !ch.handleAgentPersonaSubcommand(userInput) {
			// Não é um subcomando, inicia modo agente
			ch.cli.pendingAction = "agent"
			panic(agentModeRequest)
		}
		return false
	case strings.HasPrefix(userInput, "/run"):
		// /run inicia o modo agente (com ou sem persona ativa)
		ch.cli.pendingAction = "agent"
		panic(agentModeRequest)
	case strings.HasPrefix(userInput, "/coder"):
		ch.cli.pendingAction = "coder"
		panic(coderModeRequest)
	case strings.HasPrefix(userInput, "/switch"):
		ch.cli.handleSwitchCommand(userInput)
		return false
	case userInput == "/help":
		ch.cli.showHelp()
		return false
	case userInput == "/config" || userInput == "/status" || userInput == "/settings":
		ch.cli.showConfig()
		return false
	case userInput == "/version" || userInput == "/v":
		ch.handleVersionCommand()
		return false
	case userInput == "/nextchunk":
		return ch.cli.handleNextChunk()
	case userInput == "/retry":
		return ch.cli.handleRetryLastChunk()
	case userInput == "/retryall":
		return ch.cli.handleRetryAllChunks()
	case userInput == "/skipchunk":
		return ch.cli.handleSkipChunk()
	case userInput == "/newsession":
		ch.cli.history = []models.Message{}
		ch.cli.currentSessionName = ""
		fmt.Println(i18n.T("session.new_session_started"))
		return false
	case strings.HasPrefix(userInput, "/session"):
		ch.handleSessionCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/context"):
		ch.handleContextCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/auth"):
		ch.handleAuthCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/plugin"):
		ch.handlePluginCommand(userInput)
		return false
	case userInput == "/reset" || userInput == "/redraw" || userInput == "/clear":
		fmt.Print("\033[0m")
		os.Stdout.Sync()
		ch.cli.restoreTerminal()
		time.Sleep(50 * time.Millisecond)
		ch.cli.forceRefreshPrompt()
		return false
	default:
		fmt.Println(i18n.T("error.unknown_command"))
		return false
	}
}

// handleContextCommand - Nova função para rotear comandos de contexto
func (ch *CommandHandler) handleContextCommand(userInput string) {
	sessionID := ch.cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}

	if err := ch.cli.contextHandler.HandleContextCommand(sessionID, userInput); err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s", err.Error()), ColorYellow))
	}
}

// handleSessionCommand foi movido para cá para consolidar a lógica do handler
func (ch *CommandHandler) handleSessionCommand(userInput string) {
	args := strings.Fields(userInput)
	if len(args) < 2 {
		fmt.Println(i18n.T("session.usage_header"))
		fmt.Println(i18n.T("session.usage_save"))
		fmt.Println(i18n.T("session.usage_load"))
		fmt.Println(i18n.T("session.usage_list"))
		fmt.Println(i18n.T("session.usage_delete"))
		fmt.Println(i18n.T("session.usage_new"))
		return
	}

	command := args[1]
	var name string
	if len(args) > 2 {
		name = args[2]
	}

	switch command {
	case "save":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_save"))
			return
		}
		ch.cli.handleSaveSession(name)
	case "load":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_load"))
			return
		}
		ch.cli.handleLoadSession(name)
	case "list":
		ch.cli.handleListSessions()
	case "delete":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_delete"))
			return
		}
		ch.cli.handleDeleteSession(name)
	case "new":
		ch.cli.history = []models.Message{}
		ch.cli.currentSessionName = ""
		fmt.Println(i18n.T("session.new_session_started"))
	default:
		// CORREÇÃO: Usar Println com i18n.T
		fmt.Println(i18n.T("session.unknown_command", command))
	}
}

// autoSwitchProvider switches the active provider and model after a successful OAuth login.
func (ch *CommandHandler) autoSwitchProvider(provider, model string) {
	newClient, err := ch.cli.manager.GetClient(provider, model)
	if err != nil {
		ch.cli.logger.Warn("Auto-switch after OAuth login failed, use /switch manually",
			zap.String("provider", provider), zap.Error(err))
		fmt.Println(i18n.T("cli.switch.change_model_error", model, err))
		return
	}
	ch.cli.Client = newClient
	ch.cli.Provider = provider
	ch.cli.Model = model
	fmt.Println(i18n.T("status.provider_switched", ch.cli.Client.GetModelName(), ch.cli.Provider))
}

func (ch *CommandHandler) handleAuthCommand(userInput string) {
	args := strings.Fields(userInput)
	if len(args) < 2 {
		fmt.Println("Usage: /auth status | login <anthropic|openai-codex> | logout <anthropic|openai-codex>")
		return
	}
	sub := strings.ToLower(args[1])
	switch sub {
	case "status":
		fmt.Println(auth.FormatAuthStatus(ch.cli.logger))
	case "login":
		if len(args) < 3 {
			fmt.Println("Use: /auth login anthropic | openai-codex")
			return
		}
		prov := strings.ToLower(args[2])
		ctx := context.Background()
		switch prov {
		case "anthropic", "claude", "claudeai":
			id, err := auth.LoginAnthropicOAuth(ctx, ch.cli.logger)
			if err != nil {
				fmt.Println("Login failed:", err)
				return
			}
			fmt.Println("Logged in (Anthropic) profile:", id)
			ch.cli.manager.RefreshProviders()
			ch.autoSwitchProvider("CLAUDEAI",
				utils.GetEnvOrDefault("ANTHROPIC_MODEL", config.DefaultClaudeAIModel))
			return
		case "openai-codex", "codex":
			id, err := auth.LoginOpenAICodexOAuth(ctx, ch.cli.logger)
			if err != nil {
				fmt.Println("Login failed:", err)
				return
			}
			fmt.Println("Logged in (OpenAI Codex) profile:", id)
			ch.cli.manager.RefreshProviders()
			ch.autoSwitchProvider("OPENAI",
				utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAIModel))
			return
		default:
			fmt.Println("Unknown provider. Use: anthropic | openai-codex")
			return
		}
	case "logout":
		if len(args) < 3 {
			fmt.Println("Use: /auth logout anthropic | openai-codex")
			return
		}
		prov := strings.ToLower(args[2])
		ctx := context.Background()
		_ = ctx // keep for symmetry
		var pid auth.ProviderID
		switch prov {
		case "anthropic", "claude", "claudeai":
			pid = auth.ProviderAnthropic
		case "openai-codex", "codex":
			pid = auth.ProviderOpenAICodex
		default:
			fmt.Println("Unknown provider. Use: anthropic | openai-codex")
			return
		}
		if err := auth.Logout(pid, ch.cli.logger); err != nil {
			fmt.Println("Logout failed:", err)
			return
		}
		fmt.Println("Logout ok")
		return
	default:
		fmt.Println("Unknown subcommand. Use: status | login | logout")
		return
	}
}

func (ch *CommandHandler) handlePluginCommand(userInput string) {
	if ch.cli.pluginManager == nil {
		ch.cli.logger.Error("O gerenciador de plugins não está inicializado. O comando /plugin está desabilitado.")
		fmt.Println(i18n.T("plugin.error.manager_disabled"))
		return
	}

	args := strings.Fields(userInput)
	if len(args) < 2 {
		fmt.Println(i18n.T("plugin.usage_header"))
		return
	}

	subcommand := args[1]
	pluginManager := ch.cli.pluginManager

	switch subcommand {
	case "list":
		plugins := pluginManager.GetPlugins()
		if len(plugins) == 0 {
			fmt.Println(i18n.T("plugin.list.empty"))
			return
		}
		fmt.Println(i18n.T("plugin.list.header"))
		for _, p := range plugins {
			fmt.Printf("  %s %s - %s\n", colorize(p.Name(), ColorCyan), colorize(p.Version(), ColorGray), p.Description())
		}

	case "show":
		if len(args) < 3 {
			fmt.Println(i18n.T("plugin.show.usage"))
			return
		}
		p, found := pluginManager.GetPlugin(args[2])
		if !found {
			fmt.Println(i18n.T("plugin.error.not_found", args[2]))
			return
		}
		fmt.Println(i18n.T("plugin.show.details_for", p.Name()))
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.show.description"), ColorCyan), p.Description())
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.show.usage_label"), ColorCyan), p.Usage())
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.show.version"), ColorCyan), p.Version())

	case "inspect":
		if len(args) < 3 {
			fmt.Println(i18n.T("plugin.inspect.usage"))
			return
		}
		p, found := pluginManager.GetPlugin(args[2])
		if !found {
			fmt.Println(i18n.T("plugin.error.not_found", args[2]))
			return
		}
		info, _ := os.Stat(p.Path())
		fmt.Println(i18n.T("plugin.inspect.details_for", p.Name()))
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.inspect.path"), ColorCyan), p.Path())
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.inspect.permissions"), ColorCyan), info.Mode().String())

	case "install":
		if len(args) < 3 {
			fmt.Println(i18n.T("plugin.install.usage"))
			return
		}
		rawURL := args[2]

		// Parse the URL: detect GitHub/GitLab tree URLs and extract repo, branch, subdir.
		cloneURL, branch, subDir := parseGitURL(rawURL)

		// AVISO DE SEGURANÆA
		fmt.Println(colorize(i18n.T("plugin.install.security_warning"), ColorYellow))

		if runtime.GOOS != "windows" {
			cmd := exec.Command("stty", "sane")
			cmd.Stdin = os.Stdin
			_ = cmd.Run()
		}

		fmt.Print(i18n.T("plugin.install.confirm", rawURL))
		reader := bufio.NewReader(os.Stdin)
		confirm, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(confirm)) != "s" {
			fmt.Println(i18n.T("plugin.install.cancelled"))
			return
		}

		fmt.Println(i18n.T("plugin.install.installing", rawURL))

		tempDir, err := os.MkdirTemp("", "chatcli-plugin-")
		if err != nil {
			fmt.Println(i18n.T("plugin.install.error.tempdir", err))
			return
		}
		defer os.RemoveAll(tempDir)

		// Build git clone args: use --branch if we parsed one from the URL.
		cloneArgs := []string{"clone", "--depth=1"}
		if branch != "" {
			cloneArgs = append(cloneArgs, "--branch", branch)
		}
		cloneArgs = append(cloneArgs, cloneURL, tempDir)

		cloneCmd := exec.Command("git", cloneArgs...)
		if output, err := cloneCmd.CombinedOutput(); err != nil {
			fmt.Println(i18n.T("plugin.install.error.clone", err))
			fmt.Println(string(output))
			return
		}

		// Determine the build directory (repo root or subdirectory).
		buildDir := tempDir
		if subDir != "" {
			buildDir = filepath.Join(tempDir, subDir)
			if info, err := os.Stat(buildDir); err != nil || !info.IsDir() {
				fmt.Println(i18n.T("plugin.install.error.build",
					fmt.Errorf("subdirectory '%s' not found in repository", subDir), ""))
				return
			}
		}

		// Plugin name comes from the subdirectory (if present) or the repo name.
		pluginName := filepath.Base(buildDir)
		pluginName = strings.TrimSuffix(pluginName, ".git")
		if runtime.GOOS == "windows" {
			pluginName += ".exe"
		}

		buildCmd := exec.Command("go", "build", "-o", filepath.Join(pluginManager.PluginsDir(), pluginName), ".")
		buildCmd.Dir = buildDir
		if output, err := buildCmd.CombinedOutput(); err != nil {
			fmt.Println(i18n.T("plugin.install.error.build", err, string(output)))
			return
		}

		// Torna o arquivo executável para garantir
		if err := os.Chmod(filepath.Join(pluginManager.PluginsDir(), pluginName), 0755); err != nil {
			fmt.Println(i18n.T("plugin.install.error.chmod", err))
			return
		}

		fmt.Println(i18n.T("plugin.reloading"))
		pluginManager.Reload()
		fmt.Println(i18n.T("plugin.reload_success"))

	case "uninstall":
		if len(args) < 3 {
			fmt.Println(i18n.T("plugin.uninstall.usage"))
			return
		}
		p, found := pluginManager.GetPlugin(args[2])
		if !found {
			fmt.Println(i18n.T("plugin.error.not_found", args[2]))
			return
		}
		if err := os.Remove(p.Path()); err != nil {
			fmt.Println(i18n.T("plugin.uninstall.error", p.Name(), err))
			return
		}
		fmt.Println(i18n.T("plugin.uninstall.success", p.Name()))
		pluginManager.Reload()

	case "reload":
		fmt.Println(i18n.T("plugin.reloading"))
		pluginManager.Reload()
		fmt.Println(i18n.T("plugin.reload_success"))

	default:
		fmt.Println(i18n.T("plugin.error.unknown_subcommand", subcommand))
	}
}

// handleAgentPersonaSubcommand verifica se o comando /agent contém um subcomando de persona
// Retorna true se foi tratado como subcomando, false se deve entrar no modo agente
func (ch *CommandHandler) handleAgentPersonaSubcommand(userInput string) bool {
	if ch.cli.personaHandler == nil {
		return false
	}

	args := strings.Fields(userInput)
	if len(args) < 2 {
		// Apenas "/agent" sem argumentos - inicia modo agente (igual /run)
		return false
	}

	subcommand := strings.ToLower(args[1])

	// Subcomandos de gerenciamento de personas
	switch subcommand {
	case "list":
		ch.cli.personaHandler.ListAgents()
		return true
	case "load":
		if len(args) < 3 {
			fmt.Println(colorize(i18n.T("agent.persona.load.usage"), ColorYellow))
			return true
		}
		ch.cli.personaHandler.LoadAgent(args[2])
		return true
	case "attach", "add":
		if len(args) < 3 {
			fmt.Println(colorize("Uso: /agent attach <nome>", ColorYellow))
			return true
		}
		ch.cli.personaHandler.AttachAgent(args[2])
		return true
	case "detach", "remove", "rm":
		if len(args) < 3 {
			fmt.Println(colorize("Uso: /agent detach <nome>", ColorYellow))
			return true
		}
		ch.cli.personaHandler.DetachAgent(args[2])
		return true
	case "show":
		full := false
		if len(args) > 2 && args[2] == "--full" || args[2] == "-f" {
			full = true
		}
		ch.cli.personaHandler.ShowActive(full)
		return true
	case "status", "attached", "list-attached":
		ch.cli.personaHandler.ShowAttachedAgents()
		return true
	case "off", "unload", "reset":
		ch.cli.personaHandler.UnloadAgent()
		return true
	case "skills":
		ch.cli.personaHandler.ListSkills()
		return true
	case "help":
		ch.cli.personaHandler.ShowHelp()
		return true
	default:
		// Não é um subcomando de persona, deve ser uma tarefa para o modo agente
		return false
	}
}

// parseGitURL parses a git URL that may contain a subdirectory path.
// It supports GitHub and GitLab "tree" URLs like:
//
//	https://github.com/owner/repo/tree/branch/path/to/plugin
//	https://gitlab.com/owner/repo/-/tree/branch/path/to/plugin
//
// Returns (cloneURL, branch, subDir). For plain repo URLs, branch and subDir are empty.
func parseGitURL(rawURL string) (cloneURL, branch, subDir string) {
	// GitHub: https://github.com/{owner}/{repo}/tree/{branch}/{path...}
	if idx := strings.Index(rawURL, "/tree/"); idx != -1 {
		repoBase := rawURL[:idx]
		rest := rawURL[idx+len("/tree/"):]

		// rest = "branch/path/to/plugin" or just "branch"
		if slashIdx := strings.IndexByte(rest, '/'); slashIdx != -1 {
			branch = rest[:slashIdx]
			subDir = rest[slashIdx+1:]
		} else {
			branch = rest
		}
		// Remove trailing slashes from subDir
		subDir = strings.TrimRight(subDir, "/")
		return repoBase + ".git", branch, subDir
	}

	// GitLab: https://gitlab.com/{owner}/{repo}/-/tree/{branch}/{path...}
	if idx := strings.Index(rawURL, "/-/tree/"); idx != -1 {
		repoBase := rawURL[:idx]
		rest := rawURL[idx+len("/-/tree/"):]

		if slashIdx := strings.IndexByte(rest, '/'); slashIdx != -1 {
			branch = rest[:slashIdx]
			subDir = rest[slashIdx+1:]
		} else {
			branch = rest
		}
		subDir = strings.TrimRight(subDir, "/")
		return repoBase + ".git", branch, subDir
	}

	// Plain URL — return as-is.
	return rawURL, "", ""
}
