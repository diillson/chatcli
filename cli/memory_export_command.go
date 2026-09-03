/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /memory export|import|recall|why: the memory stores as a portable file,
 * and the reasons behind what auto-recall injects.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
)

// memoryRecallTrace is what the last auto-recall injected and why.
type memoryRecallTrace struct {
	At    time.Time
	Query string
	Facts []memory.RankedFact
}

// exportMemory is /memory export [path]: JSONL (sealed when encryption at
// rest is on) under ~/.chatcli/exports by default.
func (cli *ChatCLI) exportMemory(path string) {
	mgr := cli.memoryStore.Manager()
	if mgr == nil {
		fmt.Println(colorize("  "+i18n.T("memory.error.not_available"), ColorYellow))
		return
	}
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".chatcli", "exports", "memory-"+time.Now().Format("20060102-150405")+".jsonl")
	} else if expanded, err := expandUserPath(path); err == nil {
		path = expanded
	}
	rep, err := mgr.ExportToFile(path)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("memory.export.error", err), ColorRed))
		return
	}
	sealed := i18n.T("memory.export.plain")
	if rep.Sealed {
		sealed = i18n.T("memory.export.sealed")
	}
	fmt.Println(colorize("  "+i18n.T("memory.export.done", rep.Total(), path, rep.Facts, rep.Episodes, rep.Topics, rep.Projects, sealed), ColorGreen))
}

// importMemory is /memory import <path>: merges an export into the stores.
func (cli *ChatCLI) importMemory(path string) {
	mgr := cli.memoryStore.Manager()
	if mgr == nil {
		fmt.Println(colorize("  "+i18n.T("memory.error.not_available"), ColorYellow))
		return
	}
	if path == "" {
		fmt.Println(colorize("  "+i18n.T("memory.import.usage"), ColorYellow))
		return
	}
	if expanded, err := expandUserPath(path); err == nil {
		path = expanded
	}
	rep, err := mgr.ImportFromFile(path)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("memory.import.error", err), ColorRed))
		return
	}
	fmt.Println(colorize("  "+i18n.T("memory.import.done", rep.Total(), rep.Facts, rep.FactsSkipped, rep.Episodes, rep.EpisodesSkipped, rep.Topics, rep.Projects), ColorGreen))
}

// recallMemory is /memory recall <query>: what auto-recall would inject
// for the query and why (dry run, no reinforcement).
func (cli *ChatCLI) recallMemory(ctx context.Context, query string) {
	if strings.TrimSpace(query) == "" {
		fmt.Println(colorize("  "+i18n.T("memory.recall.usage"), ColorYellow))
		return
	}
	ranked := cli.rankedAutoRecall(ctx, memory.ExtractKeywords([]string{query}), query)
	cli.printRecallTrace(query, ranked, time.Time{})
}

// explainLastRecall is /memory why: the last turn's auto-recall with reasons.
func (cli *ChatCLI) explainLastRecall() {
	cli.recallTraceMu.Lock()
	tr := cli.lastRecallTrace
	cli.recallTraceMu.Unlock()
	if tr == nil {
		fmt.Println(colorize("  "+i18n.T("memory.why.none"), ColorGray))
		return
	}
	cli.printRecallTrace(tr.Query, tr.Facts, tr.At)
}

func (cli *ChatCLI) printRecallTrace(query string, ranked []memory.RankedFact, at time.Time) {
	if len(ranked) == 0 {
		fmt.Println(colorize("  "+i18n.T("memory.recall.none", query), ColorGray))
		return
	}
	when := ""
	if !at.IsZero() {
		when = " · " + at.Format("15:04:05")
	}
	fmt.Println(colorize("  "+i18n.T("memory.recall.header", len(ranked), query)+when, ColorCyan))
	workspace := ""
	if mgr := cli.memoryStore.Manager(); mgr != nil {
		workspace = mgr.WorkspaceDir()
	}
	for _, r := range ranked {
		if r.Fact == nil {
			continue
		}
		content := r.Fact.Content
		if len(content) > 100 {
			content = truncateRunes(content, 100) + "…"
		}
		project := ""
		if label := memory.ProjectLabel(r.Fact.SourceProject, workspace); label != "" {
			project = " " + colorize("["+label+"]", ColorYellow)
		}
		fmt.Printf("  %s [%s] %s%s\n      %s\n",
			colorize(fmt.Sprintf("%.2f", r.Final), ColorGreen), r.Fact.Category, content, project,
			colorize(i18n.T("memory.recall.why", r.Why()), ColorGray))
	}
}

// rememberRecallTrace records the last auto-recall for /memory why.
func (cli *ChatCLI) rememberRecallTrace(query string, ranked []memory.RankedFact) {
	if cli == nil {
		return
	}
	cli.recallTraceMu.Lock()
	cli.lastRecallTrace = &memoryRecallTrace{At: time.Now(), Query: query, Facts: ranked}
	cli.recallTraceMu.Unlock()
}
