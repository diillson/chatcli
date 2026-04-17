package cli

import (
	"context"
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
//	/memory profile      — show user profile
//	/memory topics       — show tracked topics
//	/memory projects     — show tracked projects
//	/memory stats        — show usage statistics
//	/memory facts [cat]  — list facts, optionally filtered by category
//	/memory compact      — force memory compaction
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

	case args == "profile":
		cli.showMemoryProfile()

	case args == "topics":
		cli.showMemoryTopics()

	case args == "projects":
		cli.showMemoryProjects()

	case args == "stats":
		cli.showMemoryStats()

	case strings.HasPrefix(args, "facts"):
		category := strings.TrimSpace(strings.TrimPrefix(args, "facts"))
		cli.showMemoryFacts(category)

	case args == "compact":
		cli.runMemoryCompact()

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
	dir := filepath.Dir(cli.memoryStore.TodayNotePath())
	memDir := filepath.Dir(dir) // go up from YYYYMM to memory/
	path := filepath.Join(memDir, date.Format("200601"), date.Format("20060102")+".md")

	data, err := os.ReadFile(path) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
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
				colorize("!", ColorYellow),
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
		fmt.Printf("  %s %s\n", colorize(i18n.T("mem.cmd.no_notes_found_for"), ColorGray), colorize(date.Format("2006-01-02"), ColorCyan))
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
		colorize("OK", ColorGreen),
		i18n.T("memory.loaded_into_context", date.Format("2006-01-02"), len(contextContent)),
	)
}

// --- New commands ---

func (cli *ChatCLI) showMemoryProfile() {
	mgr := cli.memoryStore.Manager()
	profile := mgr.Profile.Get()

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("mem.cmd.profile.title"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	if profile.Name == "" && profile.Role == "" && profile.ExpertiseLevel == "" &&
		profile.PreferredLang == "" && profile.CommStyle == "" &&
		len(profile.TopCommands) == 0 && len(profile.Preferences) == 0 {
		fmt.Println(colorize("  "+i18n.T("mem.cmd.profile.empty"), ColorGray))
		fmt.Println()
		return
	}

	if profile.Name != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("mem.cmd.profile.name"), ColorYellow), profile.Name)
	}
	if profile.Role != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("mem.cmd.profile.role"), ColorYellow), profile.Role)
	}
	if profile.ExpertiseLevel != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("mem.cmd.profile.expertise"), ColorYellow), profile.ExpertiseLevel)
	}
	if profile.PreferredLang != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("mem.cmd.profile.language"), ColorYellow), profile.PreferredLang)
	}
	if profile.CommStyle != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("mem.cmd.profile.style"), ColorYellow), profile.CommStyle)
	}

	if len(profile.TopCommands) > 0 {
		fmt.Printf("\n  %s\n", colorize(i18n.T("mem.cmd.profile.top_commands"), ColorYellow))
		type cmdCount struct {
			cmd   string
			count int
		}
		var cmds []cmdCount
		for c, n := range profile.TopCommands {
			cmds = append(cmds, cmdCount{c, n})
		}
		sort.Slice(cmds, func(i, j int) bool {
			return cmds[i].count > cmds[j].count
		})
		limit := 10
		if len(cmds) < limit {
			limit = len(cmds)
		}
		for _, c := range cmds[:limit] {
			fmt.Printf("    %s  (%d)\n", c.cmd, c.count)
		}
	}

	if len(profile.Preferences) > 0 {
		fmt.Printf("\n  %s\n", colorize(i18n.T("mem.cmd.profile.preferences"), ColorYellow))
		for k, v := range profile.Preferences {
			fmt.Printf("    %s: %s\n", k, v)
		}
	}

	if !profile.LastUpdated.IsZero() {
		fmt.Printf("\n  %s %s\n", colorize(i18n.T("mem.cmd.profile.last_updated"), ColorGray), profile.LastUpdated.Format("2006-01-02 15:04"))
	}
	fmt.Println()
}

func (cli *ChatCLI) showMemoryTopics() {
	mgr := cli.memoryStore.Manager()
	topics := mgr.Topics.GetAll()

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("mem.cmd.topics.title"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	if len(topics) == 0 {
		fmt.Println(colorize("  "+i18n.T("mem.cmd.topics.empty"), ColorGray))
		fmt.Println()
		return
	}

	for _, t := range topics {
		lastSeen := t.LastSeen.Format("2006-01-02")
		fmt.Printf("  %s  %s\n",
			colorize(t.Name, ColorYellow),
			colorize(i18n.T("mem.cmd.topics.mentions_last", t.Mentions, lastSeen), ColorGray))
	}
	fmt.Println()
}

func (cli *ChatCLI) showMemoryProjects() {
	mgr := cli.memoryStore.Manager()
	projects := mgr.Projects.GetAll()

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("mem.cmd.projects.title"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	if len(projects) == 0 {
		fmt.Println(colorize("  "+i18n.T("mem.cmd.projects.empty"), ColorGray))
		fmt.Println()
		return
	}

	for _, p := range projects {
		status := p.Status
		statusColor := ColorGray
		switch status {
		case "active":
			statusColor = ColorGreen
		case "paused":
			statusColor = ColorYellow
		case "completed":
			statusColor = ColorCyan
		}

		fmt.Printf("  %s  [%s]\n", colorize(p.Name, ColorYellow), colorize(status, statusColor))
		if p.Path != "" {
			fmt.Printf("    %s %s\n", i18n.T("mem.cmd.projects.path"), p.Path)
		}
		if p.Description != "" {
			fmt.Printf("    %s\n", p.Description)
		}
		if len(p.Technologies) > 0 {
			fmt.Printf("    %s %s\n", i18n.T("mem.cmd.projects.tech"), strings.Join(p.Technologies, ", "))
		}
		if !p.LastActive.IsZero() {
			fmt.Printf("    %s %s\n", i18n.T("mem.cmd.projects.last_active"), colorize(p.LastActive.Format("2006-01-02"), ColorGray))
		}
	}
	fmt.Println()
}

func (cli *ChatCLI) showMemoryStats() {
	mgr := cli.memoryStore.Manager()
	stats := mgr.Patterns.GetStats()

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("mem.cmd.stats.title"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	fmt.Printf("  %s  %d\n", colorize(i18n.T("mem.cmd.stats.facts_stored"), ColorYellow), mgr.Facts.Count())
	fmt.Printf("  %s  %d\n", colorize(i18n.T("mem.cmd.stats.topics_tracked"), ColorYellow), len(mgr.Topics.GetAll()))
	fmt.Printf("  %s  %d\n", colorize(i18n.T("mem.cmd.stats.projects_tracked"), ColorYellow), len(mgr.Projects.GetAll()))

	fmt.Printf("\n  %s\n", colorize(i18n.T("mem.cmd.stats.usage_patterns"), ColorYellow))
	fmt.Printf("    %s %d\n", i18n.T("mem.cmd.stats.sessions"), stats.SessionCount)
	fmt.Printf("    %s %d\n", i18n.T("mem.cmd.stats.total_messages"), stats.TotalMessages)

	if stats.AvgSessionSecs > 0 {
		fmt.Printf("    %s\n", i18n.T("mem.cmd.stats.avg_session", stats.AvgSessionSecs/60.0))
	}

	// Peak hours
	peakHours := mgr.Patterns.GetPeakHours(3)
	if len(peakHours) > 0 {
		var hourStrs []string
		for _, h := range peakHours {
			hourStrs = append(hourStrs, fmt.Sprintf("%02d:00", h))
		}
		fmt.Printf("    %s %s\n", i18n.T("mem.cmd.stats.peak_hours"), strings.Join(hourStrs, ", "))
	}

	// Top commands
	topCmds := mgr.Patterns.GetTopCommands(5)
	if len(topCmds) > 0 {
		fmt.Printf("    %s %s\n", i18n.T("mem.cmd.stats.top_commands"), strings.Join(topCmds, ", "))
	}

	// Top features
	if len(stats.FeatureUsage) > 0 {
		var features []string
		for f, c := range stats.FeatureUsage {
			features = append(features, fmt.Sprintf("%s(%d)", f, c))
		}
		fmt.Printf("    %s %s\n", i18n.T("mem.cmd.stats.features"), strings.Join(features, ", "))
	}

	// Common errors
	if len(stats.CommonErrors) > 0 {
		fmt.Printf("\n  %s\n", colorize(i18n.T("mem.cmd.stats.common_errors"), ColorYellow))
		limit := 5
		if len(stats.CommonErrors) < limit {
			limit = len(stats.CommonErrors)
		}
		for _, ep := range stats.CommonErrors[:limit] {
			fmt.Printf("    %s\n", i18n.T("mem.cmd.stats.error_pattern", ep.Pattern, ep.Count))
			if ep.Resolution != "" {
				fmt.Printf("      %s %s\n", i18n.T("mem.cmd.stats.fix"), ep.Resolution)
			}
		}
	}

	if !stats.LastSession.IsZero() {
		fmt.Printf("\n  %s %s\n", colorize(i18n.T("mem.cmd.stats.last_session"), ColorGray), stats.LastSession.Format("2006-01-02 15:04"))
	}
	fmt.Println()
}

func (cli *ChatCLI) showMemoryFacts(category string) {
	mgr := cli.memoryStore.Manager()

	var facts []*struct {
		ID       string
		Content  string
		Category string
		Score    float64
		Accessed int
	}

	if category != "" {
		rawFacts := mgr.Facts.GetByCategory(category)
		for _, f := range rawFacts {
			facts = append(facts, &struct {
				ID       string
				Content  string
				Category string
				Score    float64
				Accessed int
			}{f.ID, f.Content, f.Category, f.Score, f.AccessCount})
		}
	} else {
		rawFacts := mgr.Facts.GetAll()
		for _, f := range rawFacts {
			facts = append(facts, &struct {
				ID       string
				Content  string
				Category string
				Score    float64
				Accessed int
			}{f.ID, f.Content, f.Category, f.Score, f.AccessCount})
		}
	}

	fmt.Println()
	title := i18n.T("mem.cmd.facts.title")
	if category != "" {
		title += " [" + category + "]"
	}
	fmt.Println(colorize("  "+title, ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	if len(facts) == 0 {
		fmt.Println(colorize("  "+i18n.T("mem.cmd.facts.empty"), ColorGray))
		fmt.Println()
		return
	}

	for i, f := range facts {
		scoreColor := ColorGray
		if f.Score > 0.7 {
			scoreColor = ColorGreen
		} else if f.Score > 0.3 {
			scoreColor = ColorYellow
		}

		fmt.Printf("  %s %s [%s] %s\n",
			colorize(fmt.Sprintf("%3d.", i+1), ColorGray),
			f.Content,
			colorize(f.Category, ColorCyan),
			i18n.T("mem.cmd.facts.score_access", colorize(fmt.Sprintf("%.2f", f.Score), scoreColor), f.Accessed),
		)
	}

	fmt.Printf("\n  %s %s\n", colorize(i18n.T("mem.cmd.facts.total"), ColorGray), i18n.T("mem.cmd.facts.total_count", len(facts)))
	fmt.Println()
}

func (cli *ChatCLI) runMemoryCompact() {
	mgr := cli.memoryStore.Manager()

	fmt.Println(colorize("  "+i18n.T("mem.cmd.compact.running"), ColorCyan))

	llmClient := cli.getClient()
	var sendPrompt func(ctx context.Context, prompt string) (string, error)

	if llmClient != nil {
		sendPrompt = func(ctx context.Context, prompt string) (string, error) {
			history := []models.Message{
				{Role: "user", Content: prompt},
			}
			return llmClient.SendPrompt(ctx, prompt, history, 0)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	factsBefore := mgr.Facts.Count()

	if err := mgr.RunCompaction(ctx, sendPrompt); err != nil {
		fmt.Printf("  %s %s %s\n", colorize("ERR", ColorRed), i18n.T("mem.cmd.compact.failed"), err.Error())
		return
	}

	factsAfter := mgr.Facts.Count()

	// Also cleanup daily notes
	deleted, _ := mgr.CleanupDailyNotes()

	fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("mem.cmd.compact.complete"))
	fmt.Printf("    %s\n", i18n.T("mem.cmd.compact.facts_change", factsBefore, factsAfter))
	if deleted > 0 {
		fmt.Printf("    %s\n", i18n.T("mem.cmd.compact.notes_removed", deleted))
	}
	fmt.Println()
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

	longTerm := cli.memoryStore.ReadLongTerm()
	mgr := cli.memoryStore.Manager()

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("memory.header.files"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	if longTerm != "" {
		factCount := mgr.Facts.Count()
		fmt.Printf("  %s  %s\n", colorize("*", ColorGreen), i18n.T("mem.cmd.list.memory_md_facts", factCount))
	} else {
		fmt.Printf("  %s  %s\n", colorize("o", ColorGray), i18n.T("mem.cmd.list.memory_md_empty"))
	}

	// Show structured files
	structuredFiles := []string{"user_profile.json", "topics.json", "projects.json", "usage_stats.json", "memory_index.json"}
	for _, f := range structuredFiles {
		path := filepath.Join(memDir, f)
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			fmt.Printf("  %s  %s (%s)\n", colorize("*", ColorGreen), f, formatSize(info.Size()))
		}
	}

	if len(files) == 0 {
		fmt.Printf("  %s  %s\n", colorize("o", ColorGray), i18n.T("memory.no_daily_notes"))
	} else {
		fmt.Printf("  %s  %s\n", colorize("*", ColorGreen), i18n.T("memory.daily_notes_count", len(files)))
		start := 0
		if len(files) > 10 {
			start = len(files) - 10
			fmt.Printf("       %s\n", i18n.T("mem.cmd.list.earlier_notes", start))
		}
		for _, f := range files[start:] {
			fmt.Printf("       %s\n", f)
		}
	}
	fmt.Println()
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

// getMemorySuggestions provides autocomplete suggestions for /memory subcommands.
func (cli *ChatCLI) getMemorySuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// "/memory" typed but no space yet — suggest the command itself
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/memory", Description: i18n.T("mem.cmd.suggest.memory")},
		}
	}

	// "/memory " — suggest subcommands
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "today", Description: i18n.T("mem.cmd.suggest.today")},
			{Text: "yesterday", Description: i18n.T("mem.cmd.suggest.yesterday")},
			{Text: "week", Description: i18n.T("mem.cmd.suggest.week")},
			{Text: "longterm", Description: i18n.T("mem.cmd.suggest.longterm")},
			{Text: "list", Description: i18n.T("mem.cmd.suggest.list")},
			{Text: "load", Description: i18n.T("mem.cmd.suggest.load")},
			{Text: "profile", Description: i18n.T("mem.cmd.suggest.profile")},
			{Text: "topics", Description: i18n.T("mem.cmd.suggest.topics")},
			{Text: "projects", Description: i18n.T("mem.cmd.suggest.projects")},
			{Text: "stats", Description: i18n.T("mem.cmd.suggest.stats")},
			{Text: "facts", Description: i18n.T("mem.cmd.suggest.facts")},
			{Text: "compact", Description: i18n.T("mem.cmd.suggest.compact")},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// "/memory load " — suggest date options
	if len(args) >= 2 && args[1] == "load" {
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			suggestions := []prompt.Suggest{
				{Text: "today", Description: i18n.T("mem.cmd.suggest.load_today")},
				{Text: "yesterday", Description: i18n.T("mem.cmd.suggest.load_yesterday")},
			}
			if cli.memoryStore != nil {
				notes := cli.memoryStore.GetRecentDailyNotes(7)
				for _, note := range notes {
					dateStr := note.Date.Format("2006-01-02")
					suggestions = append(suggestions, prompt.Suggest{
						Text:        dateStr,
						Description: i18n.T("mem.cmd.suggest.load_date", note.Date.Format("02/Jan")),
					})
				}
			}
			return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
		}
	}

	// "/memory facts " — suggest categories
	if len(args) >= 2 && args[1] == "facts" {
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			suggestions := []prompt.Suggest{
				{Text: "architecture", Description: i18n.T("mem.cmd.suggest.cat_architecture")},
				{Text: "pattern", Description: i18n.T("mem.cmd.suggest.cat_pattern")},
				{Text: "preference", Description: i18n.T("mem.cmd.suggest.cat_preference")},
				{Text: "gotcha", Description: i18n.T("mem.cmd.suggest.cat_gotcha")},
				{Text: "project", Description: i18n.T("mem.cmd.suggest.cat_project")},
				{Text: "personal", Description: i18n.T("mem.cmd.suggest.cat_personal")},
				{Text: "general", Description: i18n.T("mem.cmd.suggest.cat_general")},
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
