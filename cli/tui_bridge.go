package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/tui"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// tuiBridge implements tui.CLIBridge by delegating to a ChatCLI instance.
type tuiBridge struct {
	cli *ChatCLI
}

func newTUIBridge(cli *ChatCLI) *tuiBridge {
	return &tuiBridge{cli: cli}
}

func (b *tuiBridge) GetLLMClient() client.LLMClient {
	return b.cli.Client
}

func (b *tuiBridge) GetHistory() []models.Message {
	return b.cli.history
}

func (b *tuiBridge) SetHistory(h []models.Message) {
	b.cli.history = h
}

func (b *tuiBridge) GetMaxTokens() int {
	return b.cli.getMaxTokensForCurrentLLM()
}

func (b *tuiBridge) GetContextWindow() int {
	return catalog.GetContextWindow(b.cli.Provider, b.cli.Model)
}

func (b *tuiBridge) CancelCurrentOperation() {
	b.cli.mu.Lock()
	cancel := b.cli.operationCancel
	b.cli.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (b *tuiBridge) GetModelName() string {
	if b.cli.Client != nil {
		return b.cli.Client.GetModelName()
	}
	return b.cli.Model
}

func (b *tuiBridge) GetProviderName() string {
	return b.cli.Provider
}

func (b *tuiBridge) GetSessionName() string {
	return b.cli.currentSessionName
}

func (b *tuiBridge) GetWorkingDir() string {
	cwd, _ := os.Getwd()
	return cwd
}

func (b *tuiBridge) HandleSlashCommand(input string) (shouldExit bool) {
	return b.cli.commandHandler.HandleCommand(input)
}

func (b *tuiBridge) GetCompletions(prefix string) []tui.Completion {
	// Reuse the existing completer logic for slash commands
	var completions []tui.Completion

	commands := []struct {
		text string
		desc string
	}{
		{"/help", "Show help"},
		{"/exit", "Exit ChatCLI"},
		{"/clear", "Clear history"},
		{"/switch", "Switch provider/model"},
		{"/session", "Session management"},
		{"/context", "Context management"},
		{"/agent", "Agent mode hint"},
		{"/coder", "Coder mode hint"},
		{"/run", "Run agent task"},
		{"/auth", "Authentication"},
		{"/memory", "Memory management"},
		{"/persona", "Persona management"},
		{"/skill", "Skill management"},
		{"/compact", "Compact history"},
		{"/rewind", "Undo last turn"},
		{"/tokens", "Show token count"},
		{"/plugin", "Plugin management"},
		{"/connect", "Connect to remote"},
		{"/disconnect", "Disconnect remote"},
		{"/watch", "K8s watcher"},
		{"/version", "Show version"},
	}

	for _, cmd := range commands {
		if strings.HasPrefix(cmd.text, prefix) {
			completions = append(completions, tui.Completion{
				Text:        cmd.text,
				Description: cmd.desc,
			})
		}
	}

	// Handle subcommand/flag completions for known commands
	if strings.HasPrefix(prefix, "/switch ") {
		providers := b.cli.manager.GetAvailableProviders()
		partial := strings.TrimPrefix(prefix, "/switch ")
		for _, p := range providers {
			if strings.HasPrefix(p, partial) {
				completions = append(completions, tui.Completion{
					Text:        "/switch " + p,
					Description: "Switch to " + p,
				})
			}
		}
		return completions
	}
	if strings.HasPrefix(prefix, "/session ") {
		subCmds := []struct{ text, desc string }{
			{"/session new", "New session"},
			{"/session save", "Save session"},
			{"/session load", "Load session"},
			{"/session list", "List sessions"},
			{"/session delete", "Delete session"},
		}
		for _, s := range subCmds {
			if strings.HasPrefix(s.text, prefix) {
				completions = append(completions, tui.Completion{Text: s.text, Description: s.desc})
			}
		}
		return completions
	}
	if strings.HasPrefix(prefix, "/context ") {
		subCmds := []struct{ text, desc string }{
			{"/context create", "Create context"},
			{"/context attach", "Attach context"},
			{"/context detach", "Detach context"},
			{"/context list", "List contexts"},
			{"/context show", "Show context"},
			{"/context delete", "Delete context"},
			{"/context merge", "Merge contexts"},
			{"/context attached", "List attached"},
			{"/context export", "Export context"},
			{"/context import", "Import context"},
			{"/context metrics", "Show metrics"},
			{"/context help", "Context help"},
		}
		for _, s := range subCmds {
			if strings.HasPrefix(s.text, prefix) {
				completions = append(completions, tui.Completion{Text: s.text, Description: s.desc})
			}
		}
		return completions
	}
	if strings.HasPrefix(prefix, "/memory ") {
		subCmds := []struct{ text, desc string }{
			{"/memory load", "Load memory"},
			{"/memory save", "Save memory"},
			{"/memory search", "Search memory"},
			{"/memory list", "List memories"},
			{"/memory forget", "Forget memory"},
			{"/memory help", "Memory help"},
		}
		for _, s := range subCmds {
			if strings.HasPrefix(s.text, prefix) {
				completions = append(completions, tui.Completion{Text: s.text, Description: s.desc})
			}
		}
		return completions
	}

	// Handle @ prefix completions: @file, @command, @url
	if strings.HasPrefix(prefix, "@") {
		atCommands := []struct {
			text string
			desc string
		}{
			{"@file ", "Attach a file"},
			{"@command ", "Attach command output"},
			{"@url ", "Attach URL content"},
		}

		if strings.HasPrefix(prefix, "@file ") {
			// List files for the partial path after "@file "
			partial := strings.TrimPrefix(prefix, "@file ")
			completions = append(completions, b.completeFilePath(partial, "@file ")...)
		} else {
			// Show matching @ commands
			for _, cmd := range atCommands {
				if strings.HasPrefix(cmd.text, prefix) {
					completions = append(completions, tui.Completion{
						Text:        cmd.text,
						Description: cmd.desc,
					})
				}
			}
		}
	}

	return completions
}

// completeFilePath lists directory entries matching a partial path relative to cwd.
func (b *tuiBridge) completeFilePath(partial, commandPrefix string) []tui.Completion {
	var completions []tui.Completion

	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	dir := cwd
	namePrefix := partial

	if partial != "" {
		absPartial := filepath.Join(cwd, partial)
		info, err := os.Stat(absPartial)
		if err == nil && info.IsDir() {
			// Partial is a complete directory — list its contents
			dir = absPartial
			namePrefix = ""
		} else {
			// Partial may be "somedir/partialName" — split into dir + prefix
			dir = filepath.Join(cwd, filepath.Dir(partial))
			namePrefix = filepath.Base(partial)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	// Build the relative directory prefix for display
	relDir := ""
	if partial != "" {
		if namePrefix == "" {
			relDir = partial
			if !strings.HasSuffix(relDir, "/") {
				relDir += "/"
			}
		} else {
			d := filepath.Dir(partial)
			if d != "." {
				relDir = d + "/"
			}
		}
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue // skip hidden files
		}
		if namePrefix != "" && !strings.HasPrefix(name, namePrefix) {
			continue
		}

		display := name
		if entry.IsDir() {
			display += "/"
		}

		desc := "file"
		if entry.IsDir() {
			desc = "directory"
		}

		completions = append(completions, tui.Completion{
			Text:        commandPrefix + relDir + display,
			Description: desc,
		})
	}

	return completions
}

func (b *tuiBridge) BuildTempHistory(userInput, additionalContext string) []models.Message {
	sessionID := b.cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}

	var systemParts []models.ContentBlock

	// Part 1: Workspace context
	if b.cli.contextBuilder != nil {
		if wsCtx := b.cli.contextBuilder.BuildSystemPromptPrefix(); wsCtx != "" {
			dynCtx := b.cli.contextBuilder.BuildDynamicContext()
			wsContent := wsCtx
			if dynCtx != "" {
				wsContent += "\n\n" + dynCtx
			}
			systemParts = append(systemParts, models.ContentBlock{
				Type:         "text",
				Text:         wsContent,
				CacheControl: &models.CacheControl{Type: "ephemeral"},
			})
		}
	}

	// Part 2: Attached contexts
	contextMessages, err := b.cli.contextHandler.GetManager().BuildPromptMessages(
		sessionID,
		ctxmgr.FormatOptions{
			IncludeMetadata:  true,
			IncludeTimestamp: false,
			Compact:          false,
			Role:             "system",
		},
	)
	if err != nil {
		b.cli.logger.Warn("Erro ao construir mensagens de contexto", zap.Error(err))
	}
	for _, msg := range contextMessages {
		systemParts = append(systemParts, models.ContentBlock{
			Type:         "text",
			Text:         msg.Content,
			CacheControl: &models.CacheControl{Type: "ephemeral"},
		})
	}

	// Part 3: K8s watcher context
	if b.cli.WatcherContextFunc != nil {
		if k8sCtx := b.cli.WatcherContextFunc(); k8sCtx != "" {
			systemParts = append(systemParts, models.ContentBlock{
				Type: "text",
				Text: k8sCtx,
			})
		}
	}

	// Build tempHistory
	tempHistory := make([]models.Message, 0, len(b.cli.history)+4)

	if len(systemParts) > 0 {
		var combined strings.Builder
		for i, part := range systemParts {
			if i > 0 {
				combined.WriteString("\n\n---\n\n")
			}
			combined.WriteString(part.Text)
		}
		tempHistory = append(tempHistory, models.Message{
			Role:        "system",
			Content:     combined.String(),
			SystemParts: systemParts,
		})
	}

	// System messages from history
	for _, msg := range b.cli.history {
		if msg.Role == "system" {
			tempHistory = append(tempHistory, msg)
		}
	}

	// User/assistant messages
	for _, msg := range b.cli.history {
		if msg.Role != "system" {
			tempHistory = append(tempHistory, msg)
		}
	}

	// Current user message
	fullInput := userInput
	if additionalContext != "" {
		fullInput += additionalContext
	}
	tempHistory = append(tempHistory, models.Message{
		Role:    "user",
		Content: fullInput,
	})

	return tempHistory
}

func (b *tuiBridge) ProcessSpecialCommands(input string) (string, string) {
	return b.cli.processSpecialCommands(input)
}

func (b *tuiBridge) SaveCheckpoint() {
	b.cli.saveCheckpoint()
}

func (b *tuiBridge) SetAgentEmitter(emitter interface{}) {
	if e, ok := emitter.(OutputEmitter); ok && b.cli.agentMode != nil {
		b.cli.agentMode.SetEmitter(e)
	}
}

func (b *tuiBridge) GetAgentTasks() []tui.TaskInfo {
	if b.cli.agentMode == nil || b.cli.agentMode.taskTracker == nil {
		return nil
	}
	plan := b.cli.agentMode.taskTracker.GetPlan()
	if plan == nil || len(plan.Tasks) == 0 {
		return nil
	}
	tasks := make([]tui.TaskInfo, len(plan.Tasks))
	for i, t := range plan.Tasks {
		tasks[i] = tui.TaskInfo{
			Description: t.Description,
			Status:      string(t.Status),
		}
	}
	return tasks
}

func (b *tuiBridge) GetMCPServers() []tui.MCPServerInfo {
	if b.cli.mcpManager == nil {
		return nil
	}
	statuses := b.cli.mcpManager.GetServerStatus()
	if len(statuses) == 0 {
		return nil
	}
	infos := make([]tui.MCPServerInfo, len(statuses))
	for i, s := range statuses {
		infos[i] = tui.MCPServerInfo{
			Name:      s.Name,
			Connected: s.Connected,
			ToolCount: s.ToolCount,
		}
	}
	return infos
}

func (b *tuiBridge) GetCheckpoints() []tui.CheckpointInfo {
	if len(b.cli.checkpoints) == 0 {
		return nil
	}
	infos := make([]tui.CheckpointInfo, len(b.cli.checkpoints))
	for i, cp := range b.cli.checkpoints {
		infos[i] = tui.CheckpointInfo{
			Index:    i + 1,
			Label:    cp.Label,
			Time:     cp.Timestamp.Format("15:04:05"),
			MsgCount: cp.MsgCount,
		}
	}
	return infos
}

func (b *tuiBridge) RestoreCheckpoint(index int) bool {
	if index < 1 || index > len(b.cli.checkpoints) {
		return false
	}
	cpIdx := index - 1
	cp := b.cli.checkpoints[cpIdx]
	b.cli.history = make([]models.Message, len(cp.History))
	copy(b.cli.history, cp.History)
	b.cli.checkpoints = b.cli.checkpoints[:cpIdx+1]
	return true
}

func (b *tuiBridge) GetAttachedContexts() []tui.ContextInfo {
	if b.cli.contextHandler == nil {
		return nil
	}
	sessionID := b.cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}
	mgr := b.cli.contextHandler.GetManager()
	if mgr == nil {
		return nil
	}
	contexts, err := mgr.GetAttachedContexts(sessionID)
	if err != nil || len(contexts) == 0 {
		return nil
	}
	infos := make([]tui.ContextInfo, len(contexts))
	for i, c := range contexts {
		infos[i] = tui.ContextInfo{
			Name:      c.Name,
			FileCount: c.FileCount,
			SizeBytes: c.TotalSize,
		}
	}
	return infos
}

func (b *tuiBridge) RunAgentLoop(ctx context.Context, query string) error {
	if b.cli.agentMode == nil {
		return nil // no agent mode available, caller should fallback
	}
	return b.cli.agentMode.Run(ctx, query, "", "")
}
