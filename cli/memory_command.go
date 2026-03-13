package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

// handleMemoryCommand handles the /memory command.
// Usage:
//
//	/memory              — show today's notes
//	/memory yesterday    — show yesterday's notes
//	/memory 2026-03-04   — show notes from a specific date
//	/memory week         — show last 7 days of notes
//	/memory load <date>  — load a day's notes into conversation context
//	/memory longterm     — show MEMORY.md content
func (cli *ChatCLI) handleMemoryCommand(input string) {
	if cli.memoryStore == nil {
		fmt.Println(colorize("  "+i18n.T("memory.error.not_available"), ColorYellow))
		return
	}

	args := strings.TrimSpace(strings.TrimPrefix(input, "/memory"))

	switch {
	case args == "" || args == "today":
		cli.showDayNotes(time.Now())

	case args == "yesterday":
		cli.showDayNotes(time.Now().AddDate(0, 0, -1))

	case args == "week":
		cli.showWeekNotes()

	case args == "longterm" || args == "long-term" || args == "lt":
		cli.showLongTermMemory()

	case strings.HasPrefix(args, "load "):
		dateStr := strings.TrimSpace(strings.TrimPrefix(args, "load "))
		cli.loadMemoryIntoContext(dateStr)

	case args == "list":
		cli.listMemoryNotes()

	default:
		// Try to parse as a date
		date, err := parseFlexibleDate(args)
		if err != nil {
			fmt.Println(colorize("  "+i18n.T("memory.usage"), ColorGray))
			return
		}
		cli.showDayNotes(date)
	}
}

func (cli *ChatCLI) showDayNotes(date time.Time) {
	dateStr := date.Format("2006-01-02")
	// Build correct path using the memory dir
	dir := filepath.Dir(cli.memoryStore.TodayNotePath())
	memDir := filepath.Dir(dir) // go up from YYYYMM to memory/
	path := filepath.Join(memDir, date.Format("200601"), date.Format("20060102")+".md")

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("  %s %s\n", colorize(i18n.T("memory.no_notes_for"), ColorGray), colorize(dateStr, ColorCyan))
		return
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		fmt.Printf("  %s %s\n", colorize(i18n.T("memory.no_notes_for"), ColorGray), colorize(dateStr, ColorCyan))
		return
	}

	fmt.Println()
	fmt.Printf("  %s %s\n", colorize(i18n.T("memory.notes_for"), ColorCyan+ColorBold), colorize(dateStr, ColorYellow))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))
	for _, line := range strings.Split(content, "\n") {
		fmt.Printf("  %s\n", line)
	}
	fmt.Println()
}

func (cli *ChatCLI) showWeekNotes() {
	notes := cli.memoryStore.GetRecentDailyNotes(7)
	if len(notes) == 0 {
		fmt.Println(colorize("  "+i18n.T("memory.no_notes_week"), ColorGray))
		return
	}

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("memory.header.week"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	for _, note := range notes {
		dateStr := note.Date.Format("2006-01-02 (Mon)")
		fmt.Printf("\n  %s\n", colorize(dateStr, ColorYellow))
		for _, line := range strings.Split(note.Content, "\n") {
			fmt.Printf("    %s\n", line)
		}
	}
	fmt.Println()
}

func (cli *ChatCLI) showLongTermMemory() {
	content := cli.memoryStore.ReadLongTerm()
	if content == "" {
		fmt.Println(colorize("  "+i18n.T("memory.no_longterm"), ColorGray))
		return
	}

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("memory.header.longterm"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		fmt.Printf("  %s\n", line)
	}
	fmt.Println()
}

func (cli *ChatCLI) loadMemoryIntoContext(dateStr string) {
	date, err := parseFlexibleDate(dateStr)
	if err != nil {
		fmt.Printf("  %s %s\n", colorize(i18n.T("memory.error.invalid_date"), ColorYellow), i18n.T("memory.error.date_format_hint"))
		return
	}

	// Check if this date was already loaded (prevent duplicates)
	marker := fmt.Sprintf("[MEMORY CONTEXT — loaded from %s]", date.Format("2006-01-02"))
	for _, msg := range cli.history {
		if strings.Contains(msg.Content, marker) {
			fmt.Printf("  %s %s\n",
				colorize("⚠", ColorYellow),
				i18n.T("memory.already_loaded", date.Format("2006-01-02")))
			return
		}
	}

	notes := cli.memoryStore.GetRecentDailyNotes(30) // search last 30 days
	var found string
	for _, note := range notes {
		if note.Date.Format("2006-01-02") == date.Format("2006-01-02") {
			found = note.Content
			break
		}
	}

	if found == "" {
		fmt.Printf("  %s %s\n", colorize("No notes found for", ColorGray), colorize(date.Format("2006-01-02"), ColorCyan))
		return
	}

	// Also include long-term memory
	longTerm := cli.memoryStore.ReadLongTerm()

	contextContent := marker + "\n\n"
	if longTerm != "" {
		contextContent += "## Long-term Memory\n\n" + longTerm + "\n\n"
	}
	contextContent += "## Notes from " + date.Format("2006-01-02") + "\n\n" + found

	// Inject once as a user message so the AI has the context
	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: contextContent,
	})

	fmt.Printf("  %s %s\n",
		colorize("✓", ColorGreen),
		i18n.T("memory.loaded_into_context", date.Format("2006-01-02"), len(contextContent)),
	)
}

func (cli *ChatCLI) listMemoryNotes() {
	if cli.memoryStore == nil {
		return
	}

	memDir := filepath.Dir(cli.memoryStore.TodayNotePath())
	memDir = filepath.Dir(memDir) // up from YYYYMM to memory/

	var files []string
	_ = filepath.Walk(memDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, ".md") && info.Name() != "MEMORY.md" {
			rel, _ := filepath.Rel(memDir, path)
			files = append(files, rel)
		}
		return nil
	})

	sort.Strings(files)

	// Check if MEMORY.md exists
	longTerm := cli.memoryStore.ReadLongTerm()

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("memory.header.files"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	if longTerm != "" {
		lines := len(strings.Split(longTerm, "\n"))
		fmt.Printf("  %s  MEMORY.md (%d lines)\n", colorize("●", ColorGreen), lines)
	} else {
		fmt.Printf("  %s  MEMORY.md (empty)\n", colorize("○", ColorGray))
	}

	if len(files) == 0 {
		fmt.Printf("  %s  %s\n", colorize("○", ColorGray), i18n.T("memory.no_daily_notes"))
	} else {
		fmt.Printf("  %s  %s\n", colorize("●", ColorGreen), i18n.T("memory.daily_notes_count", len(files)))
		// Show last 10
		start := 0
		if len(files) > 10 {
			start = len(files) - 10
			fmt.Printf("       ... (%d earlier notes)\n", start)
		}
		for _, f := range files[start:] {
			fmt.Printf("       %s\n", f)
		}
	}
	fmt.Println()
}

// getMemorySuggestions provides autocomplete suggestions for /memory subcommands.
func (cli *ChatCLI) getMemorySuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// "/memory" typed but no space yet — suggest the command itself
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/memory", Description: "Ver/carregar anotações de memória"},
		}
	}

	// "/memory " — suggest subcommands
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "today", Description: "Notas de hoje"},
			{Text: "yesterday", Description: "Notas de ontem"},
			{Text: "week", Description: "Notas dos últimos 7 dias"},
			{Text: "longterm", Description: "Memória de longo prazo (MEMORY.md)"},
			{Text: "list", Description: "Listar todos os arquivos de memória"},
			{Text: "load", Description: "Carregar notas de uma data no contexto (ex: load yesterday)"},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// "/memory load " — suggest date options
	if len(args) >= 2 && args[1] == "load" {
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			suggestions := []prompt.Suggest{
				{Text: "today", Description: "Carregar notas de hoje"},
				{Text: "yesterday", Description: "Carregar notas de ontem"},
			}
			// Add recent dates from existing notes
			if cli.memoryStore != nil {
				notes := cli.memoryStore.GetRecentDailyNotes(7)
				for _, note := range notes {
					dateStr := note.Date.Format("2006-01-02")
					suggestions = append(suggestions, prompt.Suggest{
						Text:        dateStr,
						Description: fmt.Sprintf("Notas de %s", note.Date.Format("02/Jan")),
					})
				}
			}
			return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
		}
	}

	return nil
}

func parseFlexibleDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)

	if s == "yesterday" {
		return time.Now().AddDate(0, 0, -1), nil
	}
	if s == "today" {
		return time.Now(), nil
	}

	// Try YYYY-MM-DD
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	// Try YYYYMMDD
	if t, err := time.Parse("20060102", s); err == nil {
		return t, nil
	}
	// Try DD/MM/YYYY
	if t, err := time.Parse("02/01/2006", s); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unrecognized date format: %s", s)
}
