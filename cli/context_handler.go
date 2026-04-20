/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// ContextHandler gerencia comandos relacionados a contextos
type ContextHandler struct {
	manager *ctxmgr.Manager
	logger  *zap.Logger
}

// NewContextHandler cria um novo handler de contextos
func NewContextHandler(logger *zap.Logger) (*ContextHandler, error) {
	manager, err := ctxmgr.NewManager(logger)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("ctx.cmd.init_manager_error"), err)
	}

	return &ContextHandler{
		manager: manager,
		logger:  logger,
	}, nil
}

// HandleContextCommand processa comandos /context
func (h *ContextHandler) HandleContextCommand(sessionID, input string) error {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		h.showContextHelp()
		return nil
	}

	subcommand := strings.ToLower(parts[1])

	switch subcommand {
	case "create", "new":
		return h.handleCreate(parts[2:])

	case "update", "edit":
		return h.handleUpdate(parts[2:])

	case "attach", "add":
		return h.handleAttach(sessionID, parts[2:])

	case "detach", "remove":
		return h.handleDetach(sessionID, parts[2:])

	case "delete", "del", "rm":
		return h.handleDelete(parts[2:])

	case "list", "ls":
		return h.handleList(parts[2:])

	case "show", "info", "view":
		return h.handleShow(parts[2:])

	case "inspect":
		return h.handleInspect(parts[2:])

	case "merge", "join":
		return h.handleMerge(parts[2:])

	case "attached":
		return h.handleShowAttached(sessionID)

	case "export":
		return h.handleExport(parts[2:])

	case "import":
		return h.handleImport(parts[2:])

	case "metrics", "stats":
		return h.handleMetrics()

	case "help", "?":
		h.showContextHelp()
		return nil

	default:
		return fmt.Errorf("%s", i18n.T("context.error.unknown_subcommand", subcommand))
	}
}

// handleCreate cria um novo contexto
func (h *ContextHandler) handleCreate(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", i18n.T("context.create.usage"))
	}

	// Parser de argumentos melhorado
	var name, description, modeStr string
	var paths []string
	var tags []string

	// Primeiro argumento é o nome
	name = args[0]

	var force bool
	// Processar flags e paths
	i := 1
	for i < len(args) {
		arg := args[i]

		switch {
		case arg == "--mode" || arg == "-m":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.mode_required"))
			}
			i++ // Avançar para o valor
			modeStr = args[i]
			i++ // Avançar para o próximo argumento

		case arg == "--description" || arg == "--desc" || arg == "-d":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.description_required"))
			}
			i++ // Avançar para o valor
			description = args[i]
			i++ // Avançar para o próximo argumento

		case arg == "--tags" || arg == "-t":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.tags_required"))
			}
			i++ // Avançar para o valor
			tags = strings.Split(args[i], ",")
			i++ // Avançar para o próximo argumento

		case arg == "--force" || arg == "-f":
			force = true
			i++

		case strings.HasPrefix(arg, "--") || strings.HasPrefix(arg, "-"):
			// Flag desconhecida
			return fmt.Errorf("%s", i18n.T("context.io.error.unknown_flag", arg))

		default:
			// É um path
			paths = append(paths, arg)
			i++
		}
	}

	if len(paths) == 0 {
		return fmt.Errorf("%s", i18n.T("context.create.error.no_paths"))
	}

	// Modo padrão
	mode := ctxmgr.ModeFull
	if modeStr != "" {
		mode = ctxmgr.ProcessingMode(strings.ToLower(modeStr))

		// Validar modo
		validModes := map[ctxmgr.ProcessingMode]bool{
			ctxmgr.ModeFull:    true,
			ctxmgr.ModeSummary: true,
			ctxmgr.ModeChunked: true,
			ctxmgr.ModeSmart:   true,
		}

		if !validModes[mode] {
			return fmt.Errorf("%s", i18n.T("context.handler.error.invalid_mode", modeStr))
		}
	}

	// Limpar tags
	for i := range tags {
		tags[i] = strings.TrimSpace(tags[i])
	}

	fmt.Println(i18n.T("context.create.processing"))

	// Debug: mostrar o que vai ser processado
	fmt.Printf("  %s %s\n", i18n.T("context.handler.label.name"), name)
	fmt.Printf("  %s %s\n", i18n.T("context.display.label.mode"), mode)
	fmt.Printf("  %s %v\n", i18n.T("context.handler.label.paths"), paths)
	if description != "" {
		fmt.Printf("  %s %s\n", i18n.T("context.display.label.description"), description)
	}
	if len(tags) > 0 {
		fmt.Printf("  %s %v\n", i18n.T("context.display.label.tags"), tags)
	}
	fmt.Println()

	ctx, err := h.manager.CreateContext(name, description, paths, mode, tags, force)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.create.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.create.success"), ColorGreen))
	h.printContextInfo(ctx, false)

	return nil
}

// handleUpdate atualiza um contexto existente
func (h *ContextHandler) handleUpdate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.update.usage"))
	}

	name := args[0]
	var description, modeStr string
	var paths []string
	var tags []string

	// Parser similar ao create
	i := 1
	for i < len(args) {
		arg := args[i]

		switch {
		case arg == "--mode" || arg == "-m":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.mode_required"))
			}
			i++
			modeStr = args[i]
			i++

		case arg == "--description" || arg == "--desc" || arg == "-d":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.description_required"))
			}
			i++
			description = args[i]
			i++

		case arg == "--tags" || arg == "-t":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.tags_required"))
			}
			i++
			tags = strings.Split(args[i], ",")
			i++

		case strings.HasPrefix(arg, "--") || strings.HasPrefix(arg, "-"):
			return fmt.Errorf("%s", i18n.T("context.io.error.unknown_flag", arg))

		default:
			paths = append(paths, arg)
			i++
		}
	}

	// Validar modo se fornecido
	mode := ctxmgr.ProcessingMode("")
	if modeStr != "" {
		mode = ctxmgr.ProcessingMode(strings.ToLower(modeStr))
		validModes := map[ctxmgr.ProcessingMode]bool{
			ctxmgr.ModeFull:    true,
			ctxmgr.ModeSummary: true,
			ctxmgr.ModeChunked: true,
			ctxmgr.ModeSmart:   true,
		}
		if !validModes[mode] {
			return fmt.Errorf("%s", i18n.T("context.handler.error.invalid_mode", modeStr))
		}
	}

	// Limpar tags
	for i := range tags {
		tags[i] = strings.TrimSpace(tags[i])
	}

	fmt.Println(i18n.T("context.update.processing"))

	ctx, err := h.manager.UpdateContext(name, paths, mode, tags, description)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.update.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.update.success"), ColorGreen))
	h.printContextInfo(ctx, false)

	return nil
}

// handleDelete deleta um contexto permanentemente
func (h *ContextHandler) handleDelete(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.delete.usage"))
	}

	contextName := args[0]

	// Buscar contexto por nome
	ctx, err := h.manager.GetContextByName(contextName)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.delete.error.not_found", contextName))
	}

	fmt.Printf("%s", i18n.T("context.delete.confirm", ctx.Name))

	// Restaurar terminal antes de ler input
	if runtime.GOOS != "windows" {
		cmd := exec.Command("stty", "sane")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("ctx.cmd.read_response_error"), err)
	}

	if strings.ToLower(strings.TrimSpace(response)) != "s" &&
		strings.ToLower(strings.TrimSpace(response)) != "y" {
		fmt.Println(i18n.T("context.delete.canceled"))
		return nil
	}

	// Deletar
	if err := h.manager.DeleteContext(ctx.ID); err != nil {
		return fmt.Errorf("%s", i18n.T("context.delete.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.delete.success", ctx.Name), ColorGreen))

	return nil
}

// handleMerge merges multiple contexts into a new one.
func (h *ContextHandler) handleMerge(args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("%s", i18n.T("context.merge.usage"))
	}

	newName := args[0]
	contextNames := args[1:]

	// Buscar IDs dos contextos
	contextIDs := make([]string, 0, len(contextNames))
	for _, name := range contextNames {
		ctx, err := h.manager.GetContextByName(name)
		if err != nil {
			return fmt.Errorf("%s", i18n.T("context.merge.error.context_not_found", name))
		}
		contextIDs = append(contextIDs, ctx.ID)
	}

	// Opções de merge padrão
	opts := ctxmgr.MergeOptions{
		RemoveDuplicates: true,
		SortByPath:       true,
		PreferNewer:      true,
		Tags:             []string{"merged"},
	}

	fmt.Println(i18n.T("context.merge.processing"))

	mergedCtx, err := h.manager.MergeContexts(newName, "", contextIDs, opts)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.merge.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.merge.success"), ColorGreen))
	h.printContextInfo(mergedCtx, false)

	return nil
}

// GetManager returns the underlying context manager.
func (h *ContextHandler) GetManager() *ctxmgr.Manager {
	return h.manager
}
