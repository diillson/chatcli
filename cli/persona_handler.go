/*
 * ChatCLI - Persona Command Handler
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// PersonaHandler handles agent/persona commands
type PersonaHandler struct {
	manager *persona.Manager
	logger  *zap.Logger
}

// NewPersonaHandler creates a new persona handler
func NewPersonaHandler(logger *zap.Logger) *PersonaHandler {
	mgr := persona.NewManager(logger)

	// Initialize directories
	if err := mgr.Initialize(); err != nil {
		logger.Warn("Failed to initialize persona directories", zap.Error(err))
	}

	return &PersonaHandler{
		manager: mgr,
		logger:  logger,
	}
}

// GetManager returns the underlying persona manager
func (h *PersonaHandler) GetManager() *persona.Manager {
	return h.manager
}

// HandleCommand processes /persona commands (retrocompatibilidade)
// Redireciona para os comandos /agent equivalentes
func (h *PersonaHandler) HandleCommand(userInput string) {
	args := strings.Fields(userInput)

	// Just "/persona" with no args - show status
	if len(args) < 2 {
		h.ShowAgentStatus()
		return
	}

	subcommand := strings.ToLower(args[1])

	switch subcommand {
	case "list":
		h.ListAgents()
	case "skills":
		h.ListSkills()
	case "show":
		h.ShowActive()
	case "off", "unload", "reset":
		h.UnloadAgent()
	case "help":
		h.ShowHelp()
	default:
		// Treat as agent name to load
		h.LoadAgent(subcommand)
	}
}

// ListAgents shows all available agents
func (h *PersonaHandler) ListAgents() {
	agents, err := h.manager.ListAgents()
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s", err.Error()), ColorRed))
		return
	}

	if len(agents) == 0 {
		fmt.Println(colorize(i18n.T("agent.persona.list.empty"), ColorYellow))
		fmt.Printf("\n    📂 Diretório: %s\n", h.manager.GetAgentsDir())
		fmt.Println("\n    Crie um arquivo .md com frontmatter YAML para definir um agente.")
		return
	}

	active := h.manager.GetActiveAgent()

	fmt.Println(colorize("\n 🤖 Agentes Disponíveis:", ColorCyan))
	fmt.Println(strings.Repeat("─", 50))

	for _, a := range agents {
		status := "  "
		if active != nil && active.Name == a.Name {
			status = colorize("✓ ", ColorGreen)
		}

		name := colorize(a.Name, ColorCyan)

		desc := a.Description
		if desc == "" {
			desc = colorize("(sem descrição)", ColorGray)
		}

		skillCount := ""
		if len(a.Skills) > 0 {
			skillCount = colorize(fmt.Sprintf(" [%d skills]", len(a.Skills)), ColorGray)
		}

		fmt.Printf("  %s%s - %s%s\n", status, name, desc, skillCount)
	}

	fmt.Println()
	fmt.Printf(" 💡 Use %s para carregar um agente\n", colorize("/agent load <nome>", ColorCyan))
}

// ListSkills shows all available skills
func (h *PersonaHandler) ListSkills() {
	skills, err := h.manager.ListSkills()
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s", err.Error()), ColorRed))
		return
	}

	if len(skills) == 0 {
		fmt.Println(colorize(i18n.T("agent.persona.skills.empty"), ColorYellow))
		fmt.Printf("\n    📂 Diretório: %s\n", h.manager.GetSkillsDir())
		return
	}

	fmt.Println(colorize("\n 🛠 Skills Disponíveis:", ColorCyan))
	fmt.Println(strings.Repeat("─", 50))

	for _, s := range skills {
		name := colorize(s.Name, ColorCyan)

		desc := s.Description
		if desc == "" {
			desc = colorize("(sem descrição)", ColorGray)
		}

		fmt.Printf("    • %s - %s\n", name, desc)
	}
	fmt.Println()
}

// LoadAgent loads an agent by name
func (h *PersonaHandler) LoadAgent(name string) {
	result, err := h.manager.LoadAgent(name)
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ Erro ao carregar agente: %s", err.Error()), ColorRed))
		return
	}

	fmt.Println()
	fmt.Println(colorize(fmt.Sprintf("✓ Agente '%s' carregado com sucesso!", result.Agent.Name), ColorGreen))

	if result.Agent.Description != "" {
		fmt.Printf("   %s\n", colorize(result.Agent.Description, ColorGray))
	}

	// Show loaded skills
	if len(result.LoadedSkills) > 0 {
		fmt.Println(colorize("\n   Skills anexadas:", ColorCyan))
		for _, s := range result.LoadedSkills {
			fmt.Printf("    • %s %s\n", s, colorize("✓", ColorGreen))
		}
	}

	// Show missing skills
	if len(result.MissingSkills) > 0 {
		fmt.Println(colorize("\n   ⚠️ Skills não encontradas (ignoradas):", ColorYellow))
		for _, s := range result.MissingSkills {
			fmt.Printf("    • %s\n", s)
		}
	}

	// Show plugins
	if len(result.Agent.Plugins) > 0 {
		fmt.Println(colorize("\n   🔤 Plugins habilitados:", ColorCyan))
		for _, p := range result.Agent.Plugins {
			fmt.Printf("    • %s\n", p)
		}
	}

	fmt.Println()
	fmt.Println(colorize("   Pronto para uso! A persona será aplicada automaticamente no próximo comando.", ColorGray))
	fmt.Printf("   Exemplo: %s ou %s\n",
		colorize("/agent crie um servidor HTTP", ColorCyan),
		colorize("/coder refatore o código", ColorCyan))
}

// UnloadAgent deactivates the current agent
func (h *PersonaHandler) UnloadAgent() {
	active := h.manager.GetActiveAgent()
	if active == nil {
		fmt.Println(colorize(i18n.T("agent.persona.off.no_active"), ColorYellow))
		return
	}

	h.manager.UnloadAgent()
	fmt.Println(colorize(fmt.Sprintf("✓ Agente '%s' desativado.", active.Name), ColorGreen))
	fmt.Println(i18n.T("agent.persona.off.hint"))
}

// ShowActive shows details of the currently active agent
func (h *PersonaHandler) ShowActive() {
	active := h.manager.GetActiveAgent()
	prompt := h.manager.GetActivePrompt()

	if active == nil {
		fmt.Println(colorize(i18n.T("agent.persona.show.no_active"), ColorYellow))
		return
	}

	fmt.Println(colorize("\n 🎭 Agente Ativo:", ColorCyan))
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("   Nome: %s\n", colorize(active.Name, ColorGreen))

	if active.Description != "" {
		fmt.Printf("   Descrição: %s\n", active.Description)
	}

	fmt.Printf("   Arquivo: %s\n", colorize(active.Path, ColorGray))

	if prompt != nil {
		if len(prompt.SkillsLoaded) > 0 {
			fmt.Printf("   Skills carregadas: %s\n", colorize(strings.Join(prompt.SkillsLoaded, ", "), ColorGreen))
		}
		if len(prompt.SkillsMissing) > 0 {
			fmt.Printf("   Skills faltando: %s\n", colorize(strings.Join(prompt.SkillsMissing, ", "), ColorYellow))
		}

		fmt.Println(colorize("\n   [Preview do Prompt Composto]", ColorCyan))
		fmt.Println(strings.Repeat("-", 60))
		// Show first 800 chars of prompt
		preview := prompt.FullPrompt
		if len(preview) > 800 {
			preview = preview[:800] + "\n... (truncado, use /agent show --full para ver completo)"
		}
		fmt.Println(preview)
		fmt.Println(strings.Repeat("-", 60))
		fmt.Printf("   Tamanho total: %d caracteres\n", len(prompt.FullPrompt))
	}

	fmt.Println()
}

// ShowAgentStatus shows current agent/persona status (chamado por /agent sem argumentos)
func (h *PersonaHandler) ShowAgentStatus() {
	active := h.manager.GetActiveAgent()

	fmt.Println(colorize("\n 🤖 Gerenciamento de Agentes (Personas)", ColorCyan+ColorBold))
	fmt.Println(strings.Repeat("─", 50))

	if active != nil {
		fmt.Printf(" 🟢 Agente ativo: %s\n", colorize(active.Name, ColorGreen))
		if active.Description != "" {
			fmt.Printf("   %s\n", colorize(active.Description, ColorGray))
		}
		if len(active.Skills) > 0 {
			fmt.Printf("   Skills: %s\n", strings.Join(active.Skills, ", "))
		}
	} else {
		fmt.Println(colorize(" ⚠ Nenhum agente ativo (usando persona padrão)", ColorYellow))
	}

	fmt.Println()
	h.ShowHelp()
}

// ShowHelp shows usage information for /agent subcommands
func (h *PersonaHandler) ShowHelp() {
	fmt.Println(colorize("📖 Subcomandos de Gerenciamento:", ColorCyan))
	fmt.Println()
	fmt.Printf("   %s               - Lista agentes disponíveis\n", colorize("/agent list", ColorCyan))
	fmt.Printf("   %s        - Carrega um agente específico\n", colorize("/agent load <nome>", ColorCyan))
	fmt.Printf("   %s             - Lista skills disponíveis\n", colorize("/agent skills", ColorCyan))
	fmt.Printf("   %s               - Mostra agente ativo e seu prompt\n", colorize("/agent show", ColorCyan))
	fmt.Printf("   %s                - Desativa o agente atual\n", colorize("/agent off", ColorCyan))

	fmt.Println()
	fmt.Println(colorize("🚀 Modo Execução (com tarefa):", ColorCyan))
	fmt.Println()
	fmt.Printf("   %s    - Executa uma tarefa no modo agente\n", colorize("/agent <tarefa>", ColorCyan))
	fmt.Printf("   %s    - Executa no modo engenheiro de software\n", colorize("/coder <tarefa>", ColorCyan))

	fmt.Println()
	fmt.Printf("   📂 Agentes: %s\n", colorize(h.manager.GetAgentsDir(), ColorGray))
	fmt.Printf("   📂 Skills:  %s\n", colorize(h.manager.GetSkillsDir(), ColorGray))
}
