/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Repeated-read deduplication for the agent loop.
 *
 * Reading the same file five times left five copies of it in the history —
 * the only content dedup the pipeline had was consecutive FORMAT ERROR
 * messages. At the turn boundary this pass finds tool results that contain
 * file reads a LATER read of the same path fully covers (same or wider line
 * range), archives the older result in the CCR store and replaces it with a
 * one-line stub carrying the @recall marker. The newest read of every path
 * stays verbatim, so the model keeps exactly one current view per file.
 */
package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// readBlock is one file block inside a @coder read result.
type readBlock struct {
	Path      string
	FirstLine int // 0 when the block carries no numbered lines (base64 / empty)
	LastLine  int
	Whole     bool // base64 or unnumbered: treated as the whole file
}

var (
	readBlockStartRe = regexp.MustCompile(`(?m)^<<< INÍCIO DO ARQUIVO(?: \(base64\))?: (.+?) >>>$`)
	readBlockEndRe   = regexp.MustCompile(`(?m)^<<< FIM DO ARQUIVO: (.+?) >>>$`)
	readNumberedRe   = regexp.MustCompile(`(?m)^\s*(\d+) \| `)
)

// parseReadBlocks extracts the file blocks a read result contains. Returns
// nil when the content is not a read result.
func parseReadBlocks(content string) []readBlock {
	starts := readBlockStartRe.FindAllStringSubmatchIndex(content, -1)
	if len(starts) == 0 {
		return nil
	}
	blocks := make([]readBlock, 0, len(starts))
	for _, st := range starts {
		path := strings.TrimSpace(content[st[2]:st[3]])
		body := content[st[1]:]
		if end := readBlockEndRe.FindStringIndex(body); end != nil {
			body = body[:end[0]]
		}
		blk := readBlock{Path: path}
		nums := readNumberedRe.FindAllStringSubmatch(body, -1)
		if len(nums) == 0 {
			blk.Whole = true
		} else {
			blk.FirstLine, _ = strconv.Atoi(nums[0][1])
			blk.LastLine, _ = strconv.Atoi(nums[len(nums)-1][1])
		}
		blocks = append(blocks, blk)
	}
	return blocks
}

// covers reports whether newer fully contains older's range.
func (newer readBlock) covers(older readBlock) bool {
	if newer.Path != older.Path {
		return false
	}
	if newer.Whole {
		return true
	}
	if older.Whole {
		return false
	}
	return newer.FirstLine <= older.FirstLine && newer.LastLine >= older.LastLine
}

// ReadDedupReport describes what the pass did.
type ReadDedupReport struct {
	Superseded int   // messages replaced by a stub
	CharsSaved int64 // bytes removed from the model's copy
}

// DedupRepeatedReads replaces older read results whose every file block is
// covered by a later read with a stub. Only tool results and squad feedback
// blocks are candidates; PreserveVerbatim messages, summaries and the most
// recent read of each path are never touched. The last message is never a
// candidate (the model has not seen it yet).
func DedupRepeatedReads(history []models.Message, ccr *compress.Layer, logger *zap.Logger) ([]models.Message, *ReadDedupReport) {
	report := &ReadDedupReport{}
	if len(history) < 2 {
		return history, report
	}
	type cand struct {
		idx    int
		blocks []readBlock
	}
	var cands []cand
	for i := range history {
		msg := &history[i]
		isFeedback := msg.Role == "user" && msg.Meta != nil && msg.Meta.AgentFeedback
		if msg.Role != "tool" && !isFeedback {
			continue
		}
		if msg.Meta != nil && (msg.Meta.PreserveVerbatim || msg.Meta.IsSummary) {
			continue
		}
		if !strings.Contains(msg.Content, "<<< INÍCIO DO ARQUIVO") {
			continue
		}
		if blocks := parseReadBlocks(msg.Content); len(blocks) > 0 {
			cands = append(cands, cand{idx: i, blocks: blocks})
		}
	}
	if len(cands) < 2 {
		return history, report
	}
	// Older candidates are superseded when a strictly later candidate
	// covers every one of their blocks. Walk from the newest backwards so
	// the per-path "latest coverage" is known before an older one is judged.
	latest := map[string][]readBlock{}
	for c := len(cands) - 1; c >= 0; c-- {
		cd := cands[c]
		if c < len(cands)-1 || cd.idx < len(history)-1 {
			allCovered := true
			for _, blk := range cd.blocks {
				covered := false
				for _, later := range latest[blk.Path] {
					if later.covers(blk) {
						covered = true
						break
					}
				}
				if !covered {
					allCovered = false
					break
				}
			}
			if allCovered && len(latest) > 0 {
				msg := &history[cd.idx]
				original := msg.Content
				paths := make([]string, 0, len(cd.blocks))
				for _, blk := range cd.blocks {
					paths = append(paths, blk.Path)
				}
				stub := fmt.Sprintf("[Earlier read of %s superseded by a later read of the same file — %d chars cleared]",
					strings.Join(uniqueStrings(paths), ", "), len(original))
				msg.Content = stub + recallSuffix(ccr, original, stub)
				report.Superseded++
				report.CharsSaved += int64(len(original) - len(msg.Content))
				continue
			}
		}
		for _, blk := range cd.blocks {
			latest[blk.Path] = append(latest[blk.Path], blk)
		}
	}
	if logger != nil && report.Superseded > 0 {
		logger.Debug("Repeated reads deduplicated",
			zap.Int("superseded", report.Superseded),
			zap.Int64("chars_saved", report.CharsSaved))
	}
	return history, report
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
