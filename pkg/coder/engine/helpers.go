package engine

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// parseFlags parses flags with ContinueOnError and returns a descriptive error.
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		var flags []string
		fs.VisitAll(func(f *flag.Flag) {
			flags = append(flags, fmt.Sprintf("--%s (%s, default: %q)", f.Name, f.Usage, f.DefValue))
		})
		return fmt.Errorf("flag parse error in '%s': %v\nAvailable flags:\n  %s",
			fs.Name(), err, strings.Join(flags, "\n  "))
	}
	return nil
}

func smartDecode(content, enc string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		reg := regexp.MustCompile("[^a-zA-Z0-9+/=]")
		clean := reg.ReplaceAllString(content, "")
		decoded, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			decoded, err = base64.URLEncoding.DecodeString(clean)
		}
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(clean, "="))
		}
		return decoded, err
	case "auto":
		if !strings.Contains(content, " ") && !strings.Contains(content, "\n") && len(content) >= 4 && len(content)%4 == 0 {
			reg := regexp.MustCompile("[^a-zA-Z0-9+/=]")
			clean := reg.ReplaceAllString(content, "")
			if d, err := base64.StdEncoding.DecodeString(clean); err == nil && utf8.Valid(d) {
				return d, nil
			}
		}
		return []byte(content), nil
	default:
		return []byte(content), nil
	}
}

func collectFiles(primary string, extras []string) []string {
	var files []string
	if primary != "" {
		files = append(files, strings.Trim(primary, "\"'"))
	}
	for _, f := range extras {
		files = append(files, strings.Trim(f, "\"'"))
	}
	return files
}

func readFileWithLimit(path string, maxBytes int) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return string(data[:maxBytes]), true, nil
	}
	return string(data), false, nil
}

func computeLineRange(total, start, end, head, tail int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if head > 0 {
		if head > total {
			head = total
		}
		return 0, head
	}
	if tail > 0 {
		if tail > total {
			tail = total
		}
		return total - tail, total
	}
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > total {
		end = total
	}
	startIdx := start - 1
	endIdx := end
	if startIdx < 0 || startIdx >= total || endIdx < start {
		return -1, -1
	}
	return startIdx, endIdx
}

func createBackup(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	input, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".bak", input, 0600)
}

func parseCSVSet(input string) map[string]bool {
	set := make(map[string]bool)
	for _, item := range strings.Split(input, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		set[item] = true
	}
	return set
}

func splitCSV(input string) []string {
	var out []string
	for _, item := range strings.Split(input, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func detectTestCommand(dir string) string {
	if fileExists(filepath.Join(dir, "go.mod")) {
		return "go test ./..."
	}
	if fileExists(filepath.Join(dir, "package.json")) {
		return "npm test"
	}
	if fileExists(filepath.Join(dir, "pyproject.toml")) || fileExists(filepath.Join(dir, "pytest.ini")) {
		return "pytest -q"
	}
	if fileExists(filepath.Join(dir, "Cargo.toml")) {
		return "cargo test"
	}
	if fileExists(filepath.Join(dir, "pom.xml")) {
		return "mvn test"
	}
	if fileExists(filepath.Join(dir, "build.gradle")) || fileExists(filepath.Join(dir, "build.gradle.kts")) {
		return "gradle test"
	}
	return ""
}
