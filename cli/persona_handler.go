/*
 * ChatCLI - Persona Command Handler
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"os"
	"os/exec"
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
	case "load":
		if len(args) < 3 {
			fmt.Println(colorize(i18n.T("agent.persona.usage.load"), ColorYellow))
			return
		}
		h.LoadAgent(args[2])
	case "attach", "add":
		if len(args) < 3 {
			fmt.Println(colorize(i18n.T("agent.persona.usage.attach"), ColorYellow))
			return
		}
		h.AttachAgent(args[2])
	case "detach", "remove", "rm":
		if len(args) < 3 {
			fmt.Println(colorize(i18n.T("agent.persona.usage.detach"), ColorYellow))
			return
		}
		h.DetachAgent(args[2])
	case "list":
		h.ListAgents()
	case "skills":
		h.ListSkills()
	case "show":
		full := false
		if len(args) > 2 && args[2] == "--full" {
			full = true
		}
		h.ShowActive(full)
	case "status", "attached", "list-attached":
		h.ShowAttachedAgents()
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
		fmt.Printf("\n    📂 %s: %s\n", i18n.T("agent.persona.directory"), h.manager.GetAgentsDir())
		fmt.Println("\n    " + i18n.T("agent.persona.list.create_hint"))
		return
	}

	active := h.manager.GetActiveAgent()

	fmt.Println(colorize("\n 🤖 "+i18n.T("agent.persona.list.header"), ColorCyan))
	fmt.Println(strings.Repeat("─", 50))

	for _, a := range agents {
		status := "  "
		if active != nil && active.Name == a.Name {
			status = colorize("✓ ", ColorGreen)
		}

		name := colorize(a.Name, ColorCyan)

		desc := a.Description
		if desc == "" {
			desc = colorize(i18n.T("agent.persona.no_description"), ColorGray)
		}

		skillCount := ""
		if len(a.Skills) > 0 {
			skillCount = colorize(i18n.T("persona.cmd.skills_count", len(a.Skills)), ColorGray)
		}

		fmt.Printf("  %s%s - %s%s\n", status, name, desc, skillCount)
	}

	fmt.Println()
	fmt.Printf(" 💡 %s\n", i18n.T("agent.persona.list.load_hint", colorize("/agent load "+i18n.T("persona.cmd.arg_name"), ColorCyan)))
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
		fmt.Printf("\n    📂 %s: %s\n", i18n.T("agent.persona.directory"), h.manager.GetSkillsDir())
		return
	}

	fmt.Println(colorize("\n 🛠 "+i18n.T("agent.persona.skills.header"), ColorCyan))
	fmt.Println(strings.Repeat("─", 50))

	for _, s := range skills {
		name := colorize(s.Name, ColorCyan)

		desc := s.Description
		if desc == "" {
			desc = colorize(i18n.T("agent.persona.no_description"), ColorGray)
		}

		fmt.Printf("    • %s - %s\n", name, desc)
	}
	fmt.Println()
}

// LoadAgent loads an agent by name
func (h *PersonaHandler) LoadAgent(name string) {
	result, err := h.manager.LoadAgent(name)
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s: %s", i18n.T("agent.persona.load.error"), err.Error()), ColorRed))
		return
	}

	fmt.Println()
	fmt.Println(colorize(i18n.T("agent.persona.load.success", result.Agent.Name), ColorGreen))

	if result.Agent.Description != "" {
		fmt.Printf("   %s\n", colorize(result.Agent.Description, ColorGray))
	}

	// Show loaded skills
	if len(result.LoadedSkills) > 0 {
		fmt.Println(colorize("\n   "+i18n.T("agent.persona.load.skills_attached"), ColorCyan))
		for _, s := range result.LoadedSkills {
			fmt.Printf("    • %s %s\n", s, colorize("✓", ColorGreen))
		}
	}

	// Show missing skills
	if len(result.MissingSkills) > 0 {
		fmt.Println(colorize("\n   ⚠️ "+i18n.T("agent.persona.load.skills_missing"), ColorYellow))
		for _, s := range result.MissingSkills {
			fmt.Printf("    • %s\n", s)
		}
	}

	// Show plugins
	if len(result.Agent.Plugins) > 0 {
		fmt.Println(colorize("\n   🔤 "+i18n.T("agent.persona.load.plugins_enabled"), ColorCyan))
		for _, p := range result.Agent.Plugins {
			fmt.Printf("    • %s\n", p)
		}
	}

	fmt.Println()
	fmt.Println(colorize("   "+i18n.T("agent.persona.load.ready"), ColorGray))
	fmt.Printf("   %s: %s %s %s\n",
		i18n.T("agent.persona.load.example"),
		colorize("/agent "+i18n.T("persona.cmd.example_agent"), ColorCyan),
		i18n.T("agent.persona.load.or"),
		colorize("/coder "+i18n.T("persona.cmd.example_coder"), ColorCyan))
}

// UnloadAgent deactivates the current agent
func (h *PersonaHandler) UnloadAgent() {
	active := h.manager.GetActiveAgent()
	if active == nil {
		fmt.Println(colorize(i18n.T("agent.persona.off.no_active"), ColorYellow))
		return
	}

	h.manager.UnloadAgent()
	fmt.Println(colorize(i18n.T("agent.persona.unload.success", active.Name), ColorGreen))
	fmt.Println(i18n.T("agent.persona.off.hint"))
}

// ShowActive shows details of the currently active agent
func (h *PersonaHandler) ShowActive(full bool) {
	active := h.manager.GetActiveAgent()
	prompt := h.manager.GetActivePrompt()

	if active == nil {
		fmt.Println(colorize(i18n.T("agent.persona.show.no_active"), ColorYellow))
		return
	}

	fmt.Println(colorize("\n 🎭 "+i18n.T("agent.persona.show.active_header"), ColorCyan))
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("   %s: %s\n", i18n.T("agent.persona.show.name"), colorize(active.Name, ColorGreen))

	if active.Description != "" {
		fmt.Printf("   %s: %s\n", i18n.T("agent.persona.show.description"), active.Description)
	}

	fmt.Printf("   %s: %s\n", i18n.T("agent.persona.show.file"), colorize(active.Path, ColorGray))

	if prompt != nil {
		if len(prompt.SkillsLoaded) > 0 {
			fmt.Printf("   %s: %s\n", i18n.T("agent.persona.show.skills_loaded"), colorize(strings.Join(prompt.SkillsLoaded, ", "), ColorGreen))
		}
		if len(prompt.SkillsMissing) > 0 {
			fmt.Printf("   %s: %s\n", i18n.T("agent.persona.show.skills_missing"), colorize(strings.Join(prompt.SkillsMissing, ", "), ColorYellow))
		}

		fmt.Println(colorize("\n   ["+i18n.T("agent.persona.show.prompt_preview")+"]", ColorCyan))
		fmt.Println(strings.Repeat("-", 60))
		// Show first 800 chars of prompt or full if requested
		preview := prompt.FullPrompt
		if !full && len(preview) > 800 {
			preview = preview[:800] + "\n... " + i18n.T("agent.persona.show.truncated")
		}
		if full {
			// Use less for pagination when --full is used
			cmd := exec.Command("less", "-R")
			cmd.Stdin = strings.NewReader(preview)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				// Fallback to print if less fails
				fmt.Println(preview)
			}
		} else {
			fmt.Println(preview)
		}
		fmt.Println(strings.Repeat("-", 60))
		fmt.Printf("   %s: %d %s\n", i18n.T("agent.persona.show.total_size"), len(prompt.FullPrompt), i18n.T("agent.persona.show.characters"))
	}

	fmt.Println()
}

// ShowAgentStatus shows current agent/persona status (chamado por /agent sem argumentos)
func (h *PersonaHandler) ShowAgentStatus() {
	active := h.manager.GetActiveAgent()

	fmt.Println(colorize("\n 🤖 "+i18n.T("agent.persona.status.header"), ColorCyan+ColorBold))
	fmt.Println(strings.Repeat("─", 50))

	if active != nil {
		fmt.Printf(" 🟢 %s: %s\n", i18n.T("agent.persona.status.active"), colorize(active.Name, ColorGreen))
		if active.Description != "" {
			fmt.Printf("   %s\n", colorize(active.Description, ColorGray))
		}
		if len(active.Skills) > 0 {
			fmt.Printf("   %s: %s\n", i18n.T("persona.cmd.skills_label"), strings.Join(active.Skills, ", "))
		}
	} else {
		fmt.Println(colorize(" ⚠ "+i18n.T("agent.persona.status.none"), ColorYellow))
	}

	fmt.Println()
	h.ShowHelp()
}

// ShowHelp shows usage information for /agent subcommands
func (h *PersonaHandler) ShowHelp() {
	fmt.Println(colorize("📖 "+i18n.T("agent.persona.help.management_header"), ColorCyan))
	fmt.Println()
	fmt.Printf("   %s               - %s\n", colorize("/agent list", ColorCyan), i18n.T("agent.persona.help.list"))
	fmt.Printf("   %s        - %s\n", colorize("/agent load "+i18n.T("persona.cmd.arg_name"), ColorCyan), i18n.T("agent.persona.help.load"))
	fmt.Printf("   %s             - %s\n", colorize("/agent skills", ColorCyan), i18n.T("agent.persona.help.skills"))
	fmt.Printf("   %s               - %s\n", colorize("/agent show [--full]", ColorCyan), i18n.T("agent.persona.help.show"))
	fmt.Printf("   %s           - %s\n", colorize("/agent status", ColorCyan), i18n.T("agent.persona.help.status"))
	fmt.Printf("   %s                - %s\n", colorize("/agent off", ColorCyan), i18n.T("agent.persona.help.off"))

	fmt.Println()
	fmt.Println(colorize("🚀 "+i18n.T("agent.persona.help.exec_header"), ColorCyan))
	fmt.Println()
	fmt.Printf("   %s    - %s\n", colorize("/agent "+i18n.T("persona.cmd.arg_task"), ColorCyan), i18n.T("agent.persona.help.agent_task"))
	fmt.Printf("   %s    - %s\n", colorize("/coder "+i18n.T("persona.cmd.arg_task"), ColorCyan), i18n.T("agent.persona.help.coder_task"))

	fmt.Println()
	fmt.Printf("   📂 %s: %s\n", i18n.T("agent.persona.help.agents_dir"), colorize(h.manager.GetAgentsDir(), ColorGray))
	fmt.Printf("   📂 %s:  %s\n", i18n.T("persona.cmd.skills_label"), colorize(h.manager.GetSkillsDir(), ColorGray))
}

// AttachAgent adds an agent to active pool
func (h *PersonaHandler) AttachAgent(name string) {
	result, err := h.manager.AttachAgent(name)
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s: %s", i18n.T("agent.persona.attach.error"), err.Error()), ColorRed))
		return
	}
	fmt.Printf(" 📓 %s\n", i18n.T("agent.persona.attach.success", colorize(result.Agent.Name, ColorGreen), len(result.LoadedSkills)))
}

// DetachAgent removes an agent from active pool
func (h *PersonaHandler) DetachAgent(name string) {
	err := h.manager.DetachAgent(name)
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s: %s", i18n.T("agent.persona.detach.error"), err.Error()), ColorRed))
		return
	}
	fmt.Printf(" ✂️ %s\n", i18n.T("agent.persona.detach.success", colorize(name, ColorYellow)))
}

// ShowAttachedAgents shows only the list of attached agents without prompt details
func (h *PersonaHandler) ShowAttachedAgents() {
	active := h.manager.GetActiveAgents()
	if len(active) == 0 {
		fmt.Println(colorize(i18n.T("agent.persona.show.no_active"), ColorYellow))
		return
	}
	fmt.Println(colorize("\n 🦾 "+i18n.T("agent.persona.attached.header"), ColorCyan))
	fmt.Println(strings.Repeat("─", 50))
	for i, a := range active {
		fmt.Printf("  %d. %s - %s\n", i+1, colorize(a.Name, ColorGreen), a.Description)
		if len(a.Skills) > 0 {
			fmt.Printf("     %s: %s\n", i18n.T("persona.cmd.skills_label"), strings.Join(a.Skills, ", "))
		}
	}
	fmt.Println()
}

// UnloadAllAgents deactivates all agents
func (h *PersonaHandler) UnloadAllAgents() {
	active := h.manager.GetActiveAgents()
	if len(active) == 0 {
		fmt.Println(colorize(i18n.T("agent.persona.off.no_active"), ColorYellow))
		return
	}
	h.manager.UnloadAllAgents()
	fmt.Println(colorize(i18n.T("agent.persona.unload_all.success"), ColorGreen))
	fmt.Println(i18n.T("agent.persona.off.hint"))
}
