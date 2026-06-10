/*
 * ChatCLI - Knowledge-mode ingestion for /context.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Knowledge mode turns a flattened documentation corpus (the JSONL emitted by
 * @docs-flatten, or any directory of docs) into a retrieval-first knowledge
 * base: the conversation receives only a compact index card, and passages are
 * pulled on demand. This file owns the ingestion side — parsing the
 * docs-flatten JSONL schema into the context's file list so every chunk keeps
 * its provenance (source path, title, repo, commit) instead of arriving as one
 * opaque multi-megabyte text file.
 */
package ctxmgr

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// maxKnowledgeChunks bounds one ingestion run; docs-flatten emits ~16KB
	// chunks, so this admits corpora well past 100MB while stopping a runaway
	// or hostile file from exhausting memory.
	maxKnowledgeChunks = 50_000

	// maxKnowledgeLineBytes bounds a single JSONL line (one chunk plus JSON
	// overhead). docs-flatten defaults to 16KB chunks; 4MB tolerates --max-chars 0
	// (whole files) without admitting absurdity.
	maxKnowledgeLineBytes = 4 << 20

	// Context-metadata keys recording corpus provenance from docs-flatten.
	knowledgeMetaRepoURL = "kb.repo_url"
	knowledgeMetaCommit  = "kb.commit"
	knowledgeMetaSources = "kb.sources"
)

// docFlattenChunk mirrors one line of the @docs-flatten JSONL output.
type docFlattenChunk struct {
	ID        string `json:"id"`     // e.g. "docs/intro.md#0001"
	Source    string `json:"source"` // e.g. "docs/intro.md"
	Title     string `json:"title,omitempty"`
	Content   string `json:"content"`
	ChunkSize int    `json:"chunkSize"`
	RepoURL   string `json:"repoUrl,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

// isJSONLPath reports whether path names a JSONL corpus file.
func isJSONLPath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".jsonl")
}

// ingestKnowledgeJSONL parses a docs-flatten JSONL file into per-chunk
// FileInfo entries. Each chunk becomes one virtual file whose Path is the
// chunk id ("source#nnnn"), preserving the corpus structure for segmentation,
// retrieval and the digest. Returns the files plus provenance metadata to be
// merged into the context.
//
// Malformed lines are counted and skipped, never fatal: a 50k-line corpus
// must not be lost to one truncated write. An error is returned only when the
// file cannot be read at all or yields no usable chunk.
func ingestKnowledgeJSONL(path string, logger *zap.Logger) ([]utils.FileInfo, map[string]string, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	f, err := os.Open(path) // #nosec G304 -- user-supplied corpus path, same trust as /context create
	if err != nil {
		return nil, nil, fmt.Errorf("knowledge: open corpus: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxKnowledgeLineBytes)

	files := make([]utils.FileInfo, 0, 256)
	meta := map[string]string{}
	sources := map[string]struct{}{}
	var malformed, lineNo int

	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c docFlattenChunk
		if err := json.Unmarshal([]byte(line), &c); err != nil || strings.TrimSpace(c.Content) == "" {
			malformed++
			continue
		}
		if c.ID == "" {
			// Tolerate generators that omit ids: synthesize a stable one from
			// the source (or the line number as a last resort).
			src := c.Source
			if src == "" {
				src = filepath.Base(path)
			}
			c.ID = fmt.Sprintf("%s#%04d", src, lineNo)
		}
		if c.Source == "" {
			c.Source = strings.SplitN(c.ID, "#", 2)[0]
		}
		files = append(files, utils.FileInfo{
			Path:    c.ID,
			Content: c.Content,
			Size:    int64(len(c.Content)),
			Type:    knowledgeChunkType(c.Source),
		})
		sources[c.Source] = struct{}{}
		if c.RepoURL != "" {
			meta[knowledgeMetaRepoURL] = c.RepoURL
		}
		if c.Commit != "" {
			meta[knowledgeMetaCommit] = c.Commit
		}
		if len(files) >= maxKnowledgeChunks {
			logger.Warn("knowledge: corpus truncated at chunk cap",
				zap.String("path", path), zap.Int("cap", maxKnowledgeChunks))
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("knowledge: read corpus: %w", err)
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("knowledge: no usable chunks in %s (%d malformed line(s))", path, malformed)
	}
	if malformed > 0 {
		logger.Warn("knowledge: skipped malformed corpus lines",
			zap.String("path", path), zap.Int("skipped", malformed))
	}
	meta[knowledgeMetaSources] = fmt.Sprintf("%d", len(sources))
	logger.Info("knowledge: corpus ingested",
		zap.String("path", path), zap.Int("chunks", len(files)), zap.Int("sources", len(sources)))
	return files, meta, nil
}

// knowledgeChunkType derives the file-type tag from the chunk's source path so
// downstream formatting (code fences, digests) can hint the language.
func knowledgeChunkType(source string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(source)), ".")
	if ext == "" {
		return "text"
	}
	return ext
}

// chunkSource recovers the source document path from a chunk's virtual path
// ("docs/intro.md#0001" → "docs/intro.md"). Plain paths pass through, so the
// helper is safe for knowledge contexts built from a directory scan too.
func chunkSource(path string) string {
	if i := strings.LastIndex(path, "#"); i > 0 {
		return path[:i]
	}
	return path
}
