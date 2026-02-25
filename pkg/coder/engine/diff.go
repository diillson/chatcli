package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type diffHunk struct {
	oldStart int
	oldLines int
	newStart int
	newLines int
	lines    []diffLine
}

type diffLine struct {
	kind byte
	text string
}

func (e *Engine) applyUnifiedDiff(fileArg, diffText, enc string) error {
	decoded, err := smartDecode(diffText, enc)
	if err != nil {
		return fmt.Errorf("erro decode diff: %v", err)
	}

	files, err := parseUnifiedDiff(string(decoded))
	if err != nil {
		return fmt.Errorf("diff inválido: %v", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("diff vazio")
	}

	if fileArg != "" {
		hunks, ok := files[fileArg]
		if !ok {
			clean := filepath.Clean(fileArg)
			for k, h := range files {
				if filepath.Clean(k) == clean {
					hunks = h
					ok = true
					break
				}
			}
			if len(files) == 1 {
				for _, h := range files {
					hunks = h
					ok = true
					break
				}
			}
		}
		if !ok {
			return fmt.Errorf("arquivo não encontrado no diff e --file informado não confere")
		}
		return e.applyHunksToFile(fileArg, hunks)
	}

	if _, ok := files["__single__"]; ok {
		return fmt.Errorf("diff sem headers de arquivo: informe --file")
	}
	for path, hunks := range files {
		if err := e.applyHunksToFile(path, hunks); err != nil {
			return err
		}
	}
	return nil
}

func parseUnifiedDiff(diffText string) (map[string][]diffHunk, error) {
	text := strings.ReplaceAll(diffText, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	files := make(map[string][]diffHunk)

	var currentFile string
	var i int
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "--- ") {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
				currentFile = normalizeDiffPath(strings.TrimPrefix(lines[i+1], "+++ "))
				i += 2
				continue
			}
		}

		if strings.HasPrefix(line, "@@ ") {
			hunk, next, err := parseHunk(lines, i)
			if err != nil {
				return nil, err
			}
			if currentFile == "" {
				currentFile = "__single__"
			}
			files[currentFile] = append(files[currentFile], hunk)
			i = next
			continue
		}
		i++
	}

	if _, ok := files["__single__"]; ok {
		only := files["__single__"]
		delete(files, "__single__")
		files["__single__"] = only
	}

	return files, nil
}

func parseHunk(lines []string, start int) (diffHunk, int, error) {
	header := lines[start]
	re := regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	m := re.FindStringSubmatch(header)
	if m == nil {
		return diffHunk{}, start + 1, fmt.Errorf("header de hunk inválido: %s", header)
	}
	oldStart, _ := strconv.Atoi(m[1])
	oldLines := 1
	if m[2] != "" {
		oldLines, _ = strconv.Atoi(m[2])
	}
	newStart, _ := strconv.Atoi(m[3])
	newLines := 1
	if m[4] != "" {
		newLines, _ = strconv.Atoi(m[4])
	}

	hunk := diffHunk{oldStart: oldStart, oldLines: oldLines, newStart: newStart, newLines: newLines}
	i := start + 1
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "--- ") {
			break
		}
		if strings.HasPrefix(line, "\\ No newline at end of file") {
			i++
			continue
		}
		if len(line) == 0 {
			hunk.lines = append(hunk.lines, diffLine{kind: ' ', text: ""})
			i++
			continue
		}
		kind := line[0]
		if kind != ' ' && kind != '+' && kind != '-' {
			return diffHunk{}, start + 1, fmt.Errorf("linha inválida no diff: %s", line)
		}
		hunk.lines = append(hunk.lines, diffLine{kind: kind, text: line[1:]})
		i++
	}

	return hunk, i, nil
}

func normalizeDiffPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return p
}

func (e *Engine) applyHunksToFile(path string, hunks []diffHunk) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("erro leitura: %v", err)
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	trailingNewline := strings.HasSuffix(text, "\n")

	offset := 0
	for _, h := range hunks {
		idx := h.oldStart - 1 + offset
		if idx < 0 || idx > len(lines) {
			return fmt.Errorf("hunk fora do range do arquivo: %s", path)
		}

		cur := idx
		newChunk := make([]string, 0, len(h.lines))
		for _, dl := range h.lines {
			switch dl.kind {
			case ' ':
				if cur >= len(lines) || lines[cur] != dl.text {
					return fmt.Errorf("hunk mismatch no arquivo %s", path)
				}
				newChunk = append(newChunk, lines[cur])
				cur++
			case '-':
				if cur >= len(lines) || lines[cur] != dl.text {
					return fmt.Errorf("hunk mismatch no arquivo %s", path)
				}
				cur++
			case '+':
				newChunk = append(newChunk, dl.text)
			}
		}

		lines = append(lines[:idx], append(newChunk, lines[cur:]...)...)
		offset += len(newChunk) - (cur - idx)
	}

	newText := strings.Join(lines, "\n")
	if trailingNewline && !strings.HasSuffix(newText, "\n") {
		newText += "\n"
	}

	_ = createBackup(path)
	if err := os.WriteFile(path, []byte(newText), 0600); err != nil {
		return fmt.Errorf("erro escrita: %v", err)
	}
	e.printf("✅ Diff aplicado em '%s'.\n", path)
	return nil
}
