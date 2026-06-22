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

// pathFlagNames are the path-bearing string flags whose leading "~" must be
// expanded to the user's home directory after parsing. Without this, a model
// passing "~/proj/main.go" makes the engine create a literal "~" directory
// under the cwd (filepath.Abs leaves "~" untouched). Expanding here is a single
// chokepoint that covers every command (write/patch/read/tree/search/git/...).
var pathFlagNames = []string{"file", "dir", "path"}

// parseFlags parses flags with ContinueOnError and returns a descriptive error.
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		var flags []string
		fs.VisitAll(func(f *flag.Flag) {
			flags = append(flags, fmt.Sprintf("--%s (%s, default: %q)", f.Name, f.Usage, f.DefValue))
		})
		return fmt.Errorf("flag parse error in '%s': %w\nAvailable flags:\n  %s",
			fs.Name(), err, strings.Join(flags, "\n  "))
	}
	for _, name := range pathFlagNames {
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if v := f.Value.String(); strings.HasPrefix(v, "~") {
			_ = f.Value.Set(expandUserPath(v))
		}
	}
	return nil
}

// expandUserPath expands a leading "~" or "~/" to the user's home directory.
// Mirrors utils.ExpandPath semantics but is kept local so pkg/coder/engine
// stays dependency-light. "~username" is intentionally left untouched (we only
// resolve the current user's home), and the original path is returned on any
// failure so a write never silently retargets.
func expandUserPath(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	// Only "~" alone or "~/" (or "~\" on Windows) — never "~username".
	if path != "~" && !strings.HasPrefix(path, "~/") && !(len(path) > 1 && path[1] == filepath.Separator) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
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
	files := make([]string, 0, len(extras)+1)
	if primary != "" {
		files = append(files, strings.Trim(primary, "\"'"))
	}
	for _, f := range extras {
		files = append(files, strings.Trim(f, "\"'"))
	}
	return files
}

func readFileWithLimit(path string, maxBytes int) (string, bool, error) {
	data, err := os.ReadFile(path) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
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
	input, err := os.ReadFile(path) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if os.IsNotExist(err) {
		return nil // no file to back up
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path+".bak", input, 0600) //#nosec G703 -- path validated by engine.validatePath / SensitiveReadPaths.IsReadAllowed
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
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, item := range parts {
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
