package engine

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func (e *Engine) handleTree(args []string) error {
	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	maxDepth := fs.Int("max-depth", 6, "")
	maxEntries := fs.Int("max-entries", DefaultMaxEntries, "")
	includeHidden := fs.Bool("include-hidden", false, "")
	ignore := fs.String("ignore", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	ignoreSet := parseCSVSet(*ignore)
	defaultIgnore := map[string]bool{".git": true, "node_modules": true, "vendor": true}

	count := 0
	err := filepath.Walk(*dir, func(p string, i os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if count >= *maxEntries {
			return errors.New("limit reached")
		}

		name := i.Name()
		if !*includeHidden && strings.HasPrefix(name, ".") {
			if i.IsDir() && p != *dir {
				return filepath.SkipDir
			}
			return nil
		}
		if defaultIgnore[name] || ignoreSet[name] {
			if i.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if p != *dir {
			rel, _ := filepath.Rel(*dir, p)
			depth := strings.Count(rel, string(os.PathSeparator))
			if depth >= *maxDepth {
				if i.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			e.printf("%s%s\n", strings.Repeat("  ", depth), name)
			count++
		}
		return nil
	})
	if err != nil && !strings.Contains(err.Error(), "limit reached") {
		e.printf("Erro ao gerar árvore: %v\n", err)
	}

	if count >= *maxEntries {
		e.printf("... [LIMITADO EM %d ENTRADAS] ...\n", *maxEntries)
	}
	return nil
}

func (e *Engine) handleSearch(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	term := fs.String("term", "", "")
	dir := fs.String("dir", ".", "")
	useRegex := fs.Bool("regex", false, "")
	caseSensitive := fs.Bool("case-sensitive", false, "")
	contextLines := fs.Int("context", 0, "")
	maxResults := fs.Int("max-results", 0, "")
	glob := fs.String("glob", "", "")
	maxBytes := fs.Int("max-bytes", 1_048_576, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *term == "" {
		return fmt.Errorf("--term requerido")
	}

	if rgPath, err := exec.LookPath("rg"); err == nil {
		return e.runRipgrep(rgPath, *term, *dir, *useRegex, *caseSensitive, *contextLines, *maxResults, *glob)
	}

	return e.fallbackSearch(*term, *dir, *useRegex, *caseSensitive, *contextLines, *maxResults, *glob, *maxBytes)
}

func (e *Engine) runRipgrep(rgPath, term, dir string, useRegex, caseSensitive bool, contextLines, maxResults int, glob string) error {
	args := []string{"--line-number", "--column", "--color", "never"}
	if !caseSensitive {
		args = append(args, "-i")
	}
	if !useRegex {
		args = append(args, "-F")
	}
	if contextLines > 0 {
		args = append(args, "-C", strconv.Itoa(contextLines))
	}
	if maxResults > 0 {
		args = append(args, "--max-count", strconv.Itoa(maxResults))
	}
	for _, g := range splitCSV(glob) {
		args = append(args, "--glob", g)
	}
	args = append(args, term, dir)

	out, err := runCommand("", rgPath, args...)
	return e.printCommandOutput(out, err)
}

func (e *Engine) fallbackSearch(term, dir string, useRegex, caseSensitive bool, contextLines, maxResults int, glob string, maxBytes int) error {
	var re *regexp.Regexp
	var err error
	if useRegex {
		if !caseSensitive {
			term = "(?i)" + term
		}
		re, err = regexp.Compile(term)
		if err != nil {
			return fmt.Errorf("regex inválida: %v", err)
		}
	}

	globSet := splitCSV(glob)
	count := 0

	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}

		if maxResults > 0 && count >= maxResults {
			return errors.New("limit reached")
		}

		if len(globSet) > 0 {
			matched := false
			for _, g := range globSet {
				ok, _ := filepath.Match(g, filepath.Base(path))
				if ok {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}

		if maxBytes > 0 && info.Size() > int64(maxBytes) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		for i, line := range lines {
			match := false
			if useRegex {
				match = re.MatchString(line)
			} else {
				hay := line
				needle := term
				if !caseSensitive {
					hay = strings.ToLower(hay)
					needle = strings.ToLower(needle)
				}
				match = strings.Contains(hay, needle)
			}

			if match {
				start := i - contextLines
				if start < 0 {
					start = 0
				}
				end := i + contextLines + 1
				if end > len(lines) {
					end = len(lines)
				}
				for j := start; j < end; j++ {
					prefix := " "
					if j == i {
						prefix = ">"
					}
					e.printf("%s %s:%d:%s\n", prefix, path, j+1, lines[j])
				}
				count++
				if maxResults > 0 && count >= maxResults {
					return errors.New("limit reached")
				}
			}
		}
		return nil
	})
	return nil
}
