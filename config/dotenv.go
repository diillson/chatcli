/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Dotenv discovery.
 *
 * Every entrypoint (REPL, one-shot, `acp`, `mcp-server`, `gateway`,
 * `daemon`, `update`) used to resolve the environment file the same naive
 * way: $CHATCLI_DOTENV, else the literal ".env" — relative to the process
 * working directory. That silently breaks the moment ChatCLI is NOT started
 * from a shell:
 *
 *   an editor spawning `chatcli acp` (or an MCP client spawning
 *   `chatcli mcp-server`) does not run the user's shell profile, so
 *   CHATCLI_DOTENV never reaches the child, and its working directory is the
 *   project — where there is no .env. Result: not a single variable from the
 *   user's environment file is loaded. Providers keyed by an env var vanish
 *   (the run silently falls back to whatever OAuth/CLI provider survives),
 *   and AWS_PROFILE goes missing, so Bedrock authenticates as the `default`
 *   profile instead of the account the user logged into — the AWS credential
 *   chain then exhausts into the confusing "no EC2 IMDS role found".
 *
 * ResolveDotenv is the single source of truth: an explicit CHATCLI_DOTENV
 * always wins, and otherwise the file is looked up in the working directory
 * first, then in the user's ChatCLI directory, then in the home directory.
 * The resolution is recorded (ActiveDotenv) so /config can show which file is
 * actually in effect and credential errors can say why a variable is missing.
 */
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

// DotenvEnv is the variable that pins the environment file explicitly.
const DotenvEnv = "CHATCLI_DOTENV"

// DotenvOrigin says where the effective environment file came from.
type DotenvOrigin string

const (
	// DotenvFromEnvVar — pinned by $CHATCLI_DOTENV (honored even if missing).
	DotenvFromEnvVar DotenvOrigin = "CHATCLI_DOTENV"
	// DotenvFromWorkdir — ./.env in the process working directory.
	DotenvFromWorkdir DotenvOrigin = "workdir"
	// DotenvFromChatCLIDir — ~/.chatcli/.env.
	DotenvFromChatCLIDir DotenvOrigin = "chatcli-dir"
	// DotenvFromHome — ~/.env.
	DotenvFromHome DotenvOrigin = "home"
	// DotenvNotFound — no candidate exists; nothing was loaded.
	DotenvNotFound DotenvOrigin = "none"
)

// DotenvResolution is the outcome of one discovery pass.
type DotenvResolution struct {
	Path       string       // file to load (kept even when missing, for the warning)
	Origin     DotenvOrigin // where Path came from
	Exists     bool         // the file is present and readable
	ExpandErr  error        // $CHATCLI_DOTENV could not be expanded (path kept verbatim)
	Candidates []string     // every path considered, in order
}

// expandHome expands a leading "~" like utils.ExpandPath does. It is
// duplicated here on purpose: utils imports config, so config must not
// import utils.
func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not resolve the home directory: %w", err)
	}
	if len(path) == 1 {
		return home, nil
	}
	if path[1] == '/' || path[1] == filepath.Separator {
		return filepath.Join(home, path[2:]), nil
	}
	return "", fmt.Errorf("~username expansion is not supported, only ~ for the current user's home directory")
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	// #nosec G703 -- the path is the operator's own configuration ($CHATCLI_DOTENV
	// or a fixed candidate under the working directory / home); stat only, and
	// pointing ChatCLI at your own file is the documented feature.
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// ResolveDotenv finds the environment file to load.
//
// Precedence:
//  1. $CHATCLI_DOTENV — explicit intent always wins, missing file included
//     (the caller warns, exactly as before).
//  2. ./.env — the historical behavior, so a project-local file still wins
//     over the user-wide one.
//  3. ~/.chatcli/.env — ChatCLI's own directory.
//  4. ~/.env — the conventional location, and where users who export
//     CHATCLI_DOTENV=~/.env from their shell profile already keep it.
//
// With no candidate present the result points at ".env" with Origin
// DotenvNotFound, which keeps godotenv.Load's "not exist" path intact.
func ResolveDotenv() DotenvResolution {
	if raw := strings.TrimSpace(os.Getenv(DotenvEnv)); raw != "" {
		res := DotenvResolution{Path: raw, Origin: DotenvFromEnvVar}
		expanded, err := expandHome(raw)
		if err != nil {
			res.ExpandErr = err
		} else {
			res.Path = expanded
		}
		res.Candidates = []string{res.Path}
		res.Exists = fileExists(res.Path)
		return res
	}

	res := DotenvResolution{Path: ".env", Origin: DotenvNotFound}
	type candidate struct {
		path   string
		origin DotenvOrigin
	}
	candidates := []candidate{{".env", DotenvFromWorkdir}}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			candidate{filepath.Join(home, ".chatcli", ".env"), DotenvFromChatCLIDir},
			candidate{filepath.Join(home, ".env"), DotenvFromHome},
		)
	}
	for _, c := range candidates {
		res.Candidates = append(res.Candidates, c.path)
		if res.Exists {
			continue // keep listing candidates for /config, first hit wins
		}
		if fileExists(c.path) {
			res.Path, res.Origin, res.Exists = c.path, c.origin, true
		}
	}
	return res
}

var (
	dotenvMu       sync.RWMutex
	dotenvActive   DotenvResolution
	dotenvOverlays []ProjectDotenvReport
)

// SetActiveDotenv records the resolution the process actually loaded, so
// every later reader (ConfigManager, /config, credential diagnostics) reports
// the same file instead of re-deriving it from a working directory that may
// have changed.
func SetActiveDotenv(res DotenvResolution) {
	dotenvMu.Lock()
	defer dotenvMu.Unlock()
	dotenvActive = res
}

// ActiveDotenv returns the recorded resolution, resolving on the fly when
// the process never registered one (tests, library use).
func ActiveDotenv() DotenvResolution {
	dotenvMu.RLock()
	res := dotenvActive
	dotenvMu.RUnlock()
	if res.Path != "" {
		return res
	}
	return ResolveDotenv()
}

// LoadDotenv resolves, loads and records the environment file in one step.
// godotenv.Load never overrides a variable already present in the process
// environment, so a value exported by the shell keeps winning over the file.
func LoadDotenv() (DotenvResolution, error) {
	res := ResolveDotenv()
	SetActiveDotenv(res)
	return res, godotenv.Load(res.Path)
}

// ProjectDotenvMode is the policy for project-supplied environment files.
type ProjectDotenvMode string

const (
	// ProjectDotenvSafe (default) applies a project .env except for
	// credential and endpoint variables — see projectDotenvBlocked.
	ProjectDotenvSafe ProjectDotenvMode = "safe"
	// ProjectDotenvAll applies every key the project file declares.
	ProjectDotenvAll ProjectDotenvMode = "all"
	// ProjectDotenvOff ignores project files entirely.
	ProjectDotenvOff ProjectDotenvMode = "off"
)

// ProjectDotenvEnv selects the policy above.
const ProjectDotenvEnv = "CHATCLI_PROJECT_ENV"

// ProjectDotenvPolicy reads the configured policy (default: safe).
func ProjectDotenvPolicy() ProjectDotenvMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ProjectDotenvEnv))) {
	case "off", "false", "0", "no":
		return ProjectDotenvOff
	case "all", "true", "1", "yes":
		return ProjectDotenvAll
	default:
		return ProjectDotenvSafe
	}
}

// projectDotenvBlocked reports whether a key is refused in "safe" mode.
//
// A project directory is attacker-controlled input: cloning a repository and
// opening it in an editor must not let its .env redirect ChatCLI's traffic
// (a *_BASE_URL / *_API_URL pointing at an exfiltration host would carry the
// user's own key with it) or inject credentials and encryption material.
// Everything else — LLM_PROVIDER, LLM_MODEL, AWS_PROFILE, BEDROCK_REGION,
// feature toggles — is per-project configuration and passes.
func projectDotenvBlocked(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	for _, suffix := range []string{
		"_API_KEY", "_APIKEY", "_KEY", "_TOKEN", "_SECRET", "_PASSWORD",
		"_BASE_URL", "_API_URL", "_ENDPOINT", "_ENDPOINT_URL", "_CREDENTIALS",
	} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	switch k {
	case "CLIENT_ID", "CLIENT_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONFIG_FILE",
		"AWS_SHARED_CREDENTIALS_FILE", "CHATCLI_DOTENV", "CHATCLI_MANAGED_CONFIG",
		"CHATCLI_AUDIT_LOG_PATH", "CHATCLI_CA_BUNDLE", "CHATCLI_BEDROCK_CA_BUNDLE",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "PATH", "LD_PRELOAD":
		return true
	}
	return false
}

// ProjectDotenvReport is the outcome of one project overlay.
type ProjectDotenvReport struct {
	Dir     string // directory the client announced
	Path    string // <dir>/.env
	Present bool   // the file exists
	Mode    ProjectDotenvMode
	Applied []string // keys that filled an unset variable
	Skipped []string // keys already set in the process (never overridden)
	Refused []string // keys blocked by the safe policy
	Err     error    // read/parse failure (the file is then ignored)
}

// ApplyProjectDotenv loads <dir>/.env as a FILL-ONLY overlay: a variable
// already present in the process (shell export, user .env, managed policy)
// is never overridden, so the overlay can add per-project configuration but
// can never take control of an existing setting. Applying the same directory
// twice, or the file already loaded as the active dotenv, is a no-op.
//
// It exists for surfaces that learn about the user's project only after boot
// — the ACP server receives it as session/new's cwd.
func ApplyProjectDotenv(dir string) ProjectDotenvReport {
	rep := ProjectDotenvReport{Dir: dir, Mode: ProjectDotenvPolicy()}
	if strings.TrimSpace(dir) == "" || rep.Mode == ProjectDotenvOff {
		return rep
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		rep.Err = err
		return rep
	}
	rep.Dir = abs
	rep.Path = filepath.Join(abs, ".env")
	if !fileExists(rep.Path) {
		return rep
	}
	rep.Present = true

	active := ActiveDotenv()
	if activeAbs, err := filepath.Abs(active.Path); err == nil && activeAbs == rep.Path {
		return rep // already loaded as the process-wide environment file
	}
	dotenvMu.RLock()
	for _, prev := range dotenvOverlays {
		if prev.Path == rep.Path {
			dotenvMu.RUnlock()
			return rep
		}
	}
	dotenvMu.RUnlock()

	values, err := godotenv.Read(rep.Path)
	if err != nil {
		rep.Err = err
		return rep
	}
	for key, value := range values {
		switch {
		case rep.Mode == ProjectDotenvSafe && projectDotenvBlocked(key):
			rep.Refused = append(rep.Refused, key)
		case isEnvSet(key):
			rep.Skipped = append(rep.Skipped, key)
		default:
			_ = os.Setenv(key, value)
			rep.Applied = append(rep.Applied, key)
		}
	}
	sort.Strings(rep.Applied)
	sort.Strings(rep.Skipped)
	sort.Strings(rep.Refused)

	dotenvMu.Lock()
	dotenvOverlays = append(dotenvOverlays, rep)
	dotenvMu.Unlock()
	return rep
}

// ProjectDotenvOverlays returns the project files applied so far (for /config).
func ProjectDotenvOverlays() []ProjectDotenvReport {
	dotenvMu.RLock()
	defer dotenvMu.RUnlock()
	return append([]ProjectDotenvReport(nil), dotenvOverlays...)
}

// ResetProjectDotenvForTest clears the overlay bookkeeping. Test-only.
func ResetProjectDotenvForTest() {
	dotenvMu.Lock()
	defer dotenvMu.Unlock()
	dotenvOverlays = nil
	dotenvActive = DotenvResolution{}
}

func isEnvSet(key string) bool {
	v, ok := os.LookupEnv(key)
	return ok && v != ""
}
