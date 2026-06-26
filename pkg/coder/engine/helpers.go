package engine

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"
)

// pathFlagNames are the path-bearing string flags whose shell-style references
// (a leading "~" and environment variables) must be expanded after parsing.
// Without this, a model passing "~/proj/main.go" or "$CHATCLI_AGENT_TMPDIR/x"
// makes the engine create a literal "~" / "$CHATCLI_AGENT_TMPDIR" directory
// under the cwd (filepath.Abs leaves both untouched). Expanding here is a
// single chokepoint that covers every command (write/patch/read/tree/search/
// git/...).
var pathFlagNames = []string{"file", "dir", "path"}

// winEnvVarPattern matches Windows-style "%VAR%" environment references. Only
// applied on Windows so a literal "%" on POSIX (valid filename char) is never
// misread as a variable delimiter.
var winEnvVarPattern = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)

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
		// Expand unconditionally: env-var references can sit anywhere in the
		// value ("$DIR/file", "%TEMP%\\x"), not just at the front like "~".
		if v := f.Value.String(); v != "" {
			if exp := expandPath(v); exp != v {
				_ = f.Value.Set(exp)
			}
		}
	}
	return nil
}

// expandPath normalizes a path-bearing value the way a shell would, so the
// engine writes to the intended location instead of creating a literal
// directory named after an unexpanded token. Environment variables are
// resolved first (their values may themselves begin with "~"), then a leading
// "~"/"~/". It is OS-aware (POSIX "$VAR"/"${VAR}" everywhere, Windows "%VAR%")
// and idempotent on already-expanded or plain paths.
func expandPath(path string) string {
	if path == "" {
		return path
	}
	return expandUserPath(expandEnv(path))
}

// expandEnv resolves environment-variable references in a path: "$VAR" and
// "${VAR}" on every OS, plus "%VAR%" on Windows.
//
// Unset variables are deliberately preserved verbatim instead of collapsing to
// the empty string. os.ExpandEnv would turn "$UNSET/file" into "/file" —
// silently retargeting a write to the filesystem root — so we map through
// os.Expand with a lookup that keeps the original reference when a variable is
// missing. validatePath/Write then fails loudly on the literal token rather
// than writing somewhere unexpected.
func expandEnv(path string) string {
	expanded := os.Expand(path, func(name string) string {
		if name == "" {
			return "$" // lone "$" with no following name: keep it literal
		}
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return "${" + name + "}"
	})
	if runtime.GOOS == "windows" {
		expanded = winEnvVarPattern.ReplaceAllStringFunc(expanded, func(tok string) string {
			if v, ok := os.LookupEnv(tok[1 : len(tok)-1]); ok {
				return v
			}
			return tok // unset: leave "%VAR%" literal, never collapse
		})
	}
	return expanded
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
