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
 * ./.env is only a candidate in a trusted workspace (SetWorkspaceTrustGate).
 */
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/stateroot"
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
	// Untrusted is ./.env when it exists but was skipped because the working
	// directory is not a trusted workspace; empty otherwise.
	Untrusted string
}

// workspaceTrustGate answers whether a directory is a trusted workspace, so
// its project files may be loaded. It is installed by the workspace trust
// module (pkg/workspacetrust); config cannot import it, since that module
// builds on utils, which imports config.
var (
	workspaceTrustMu   sync.RWMutex
	workspaceTrustGate func(dir string) bool
)

// SetWorkspaceTrustGate installs the function that decides whether a
// directory is a trusted workspace. Without one every directory is
// untrusted: project files are never loaded by default.
func SetWorkspaceTrustGate(gate func(dir string) bool) {
	workspaceTrustMu.Lock()
	defer workspaceTrustMu.Unlock()
	workspaceTrustGate = gate
}

// workspaceTrusted consults the installed gate, failing closed.
func workspaceTrusted(dir string) bool {
	workspaceTrustMu.RLock()
	gate := workspaceTrustGate
	workspaceTrustMu.RUnlock()
	return gate != nil && gate(dir)
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
//     over the user-wide one. Only when the working directory is a trusted
//     workspace: an untrusted ./.env is skipped (reported in Untrusted) and
//     the next candidate becomes the first hit.
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
			candidate{filepath.Join(stateroot.Under(home), ".env"), DotenvFromChatCLIDir},
			candidate{filepath.Join(home, ".env"), DotenvFromHome},
		)
	}
	for _, c := range candidates {
		res.Candidates = append(res.Candidates, c.path)
		if res.Exists {
			continue // keep listing candidates for /config, first hit wins
		}
		if !fileExists(c.path) {
			continue
		}
		if c.origin == DotenvFromWorkdir && !workspaceTrusted(".") {
			res.Untrusted = c.path
			if abs, err := filepath.Abs(c.path); err == nil {
				res.Untrusted = abs
			}
			continue
		}
		res.Path, res.Origin, res.Exists = c.path, c.origin, true
	}
	return res
}

var (
	dotenvMu     sync.RWMutex
	dotenvActive DotenvResolution
	// dotenvOverlays records the project files applied (for /config);
	// dotenvOverlayApplied keeps their contributed values so a /reload can
	// put them back — the overlay itself runs only once per process.
	dotenvOverlays       []ProjectDotenvReport
	dotenvOverlayApplied = map[string]string{}
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
	CaptureBootEnv()
	res := ResolveDotenv()
	SetActiveDotenv(res)
	keepStateDir := KeepStateDir()
	err := godotenv.Load(res.Path)
	keepStateDir()
	return res, err
}

// ProjectDotenvMode is the policy for project-supplied environment files.
type ProjectDotenvMode string

const (
	// ProjectDotenvSafe (default) applies only the harmless per-project keys
	// of a trusted project .env — see projectDotenvAllowed.
	ProjectDotenvSafe ProjectDotenvMode = "safe"
	// ProjectDotenvAll applies every key a trusted project file declares.
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

// projectDotenvSafeKeys is the complete set of keys a project .env may set
// in "safe" mode.
//
// A project directory is attacker-controlled input: cloning a repository and
// opening it in an editor must not let its .env redirect ChatCLI's traffic
// (base URLs, proxies, CDP endpoints), weaken TLS, inject credentials or key
// material, point at executables or config files that spawn processes (MCP
// config, LSP or helper commands, plugin signing), move state directories, or
// relax any security switch. A denylist cannot keep up with that surface — a
// new variable is exposed until someone remembers to block it — so safe mode
// is an allowlist of keys that only SELECT among things the user already
// configured:
//
//   - provider and model choice: a provider without the user's own
//     credentials simply does not resolve, and a model name selects a model,
//     it does not route traffic anywhere new;
//   - output token caps per provider: bounded by the provider itself;
//   - AWS region and named profile: the profile must already exist in the
//     user's own AWS configuration, which the project cannot write;
//   - UI language and theme: presentation only.
//
// Anything else needs CHATCLI_PROJECT_ENV=all, and every mode requires the
// project to be a trusted workspace.
var projectDotenvSafeKeys = map[string]bool{
	"LLM_PROVIDER": true, "LLM_MODEL": true,
	"OPENAI_MODEL": true, "OPENAI_ASSISTANT_MODEL": true, "ANTHROPIC_MODEL": true,
	"GOOGLEAI_MODEL": true, "XAI_MODEL": true, "OLLAMA_MODEL": true,
	"BEDROCK_MODEL": true, "COPILOT_MODEL": true, "DEVIN_MODEL": true,
	"ZAI_MODEL": true, "MINIMAX_MODEL": true, "MOONSHOT_MODEL": true,
	"OPENROUTER_MODEL":  true,
	"OPENAI_MAX_TOKENS": true, "ANTHROPIC_MAX_TOKENS": true, "GOOGLEAI_MAX_TOKENS": true,
	"XAI_MAX_TOKENS": true, "OLLAMA_MAX_TOKENS": true, "BEDROCK_MAX_TOKENS": true,
	"COPILOT_MAX_TOKENS": true, "ZAI_MAX_TOKENS": true, "MINIMAX_MAX_TOKENS": true,
	"MOONSHOT_MAX_TOKENS": true, "OPENROUTER_MAX_TOKENS": true, "STACKSPOT_MAX_TOKENS": true,
	"AWS_PROFILE": true, "BEDROCK_PROFILE": true,
	"AWS_REGION": true, "AWS_DEFAULT_REGION": true, "BEDROCK_REGION": true,
	"CHATCLI_LANG": true, "CHATCLI_THEME": true,
}

// projectDotenvAllowed reports whether a key may be applied in "safe" mode.
func projectDotenvAllowed(key string) bool {
	return projectDotenvSafeKeys[strings.ToUpper(strings.TrimSpace(key))]
}

// ProjectDotenvReport is the outcome of one project overlay.
type ProjectDotenvReport struct {
	Dir     string // directory the client announced
	Path    string // <dir>/.env
	Present bool   // the file exists
	Mode    ProjectDotenvMode
	Applied []string // keys that filled an unset variable
	Skipped []string // keys already set in the process (never overridden)
	Refused []string // keys outside the safe allowlist
	Err     error    // read/parse failure (the file is then ignored)
	// Untrusted is set when the file exists but the project is not a
	// trusted workspace: nothing was read or applied.
	Untrusted bool
}

// ApplyProjectDotenv loads <dir>/.env as a FILL-ONLY overlay: a variable
// already present in the process (shell export, user .env, managed policy)
// is never overridden, so the overlay can add per-project configuration but
// can never take control of an existing setting. Applying the same directory
// twice, or the file already loaded as the active dotenv, is a no-op.
//
// It exists for surfaces that learn about the user's project only after boot
// — the ACP server receives it as session/new's cwd. Whatever the mode, the
// file is only read when the project is a trusted workspace.
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
	if !workspaceTrusted(abs) {
		rep.Untrusted = true
		return rep
	}

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
		case rep.Mode == ProjectDotenvSafe && !projectDotenvAllowed(key):
			rep.Refused = append(rep.Refused, key)
		case isEnvSet(key):
			rep.Skipped = append(rep.Skipped, key)
		default:
			_ = os.Setenv(key, value)
			rep.Applied = append(rep.Applied, key)
			dotenvMu.Lock()
			dotenvOverlayApplied[key] = value
			dotenvMu.Unlock()
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
	dotenvOverlayApplied = map[string]string{}
	dotenvActive = DotenvResolution{}
}

func isEnvSet(key string) bool {
	v, ok := os.LookupEnv(key)
	return ok && v != ""
}

// ─── Boot environment snapshot ────────────────────────────────────────
//
// /reload clears every reloadable variable before re-reading the
// environment file, so that a value REMOVED from the file stops applying
// without a restart. That rule assumes the file is where those values came
// from — true in a terminal, false for an editor-spawned `acp` or an
// MCP client: there the client's own `env` block is often the only source,
// and clearing it made a single /reload silently drop the provider, model
// or AWS profile the client had pinned.
//
// CaptureBootEnv records the process environment BEFORE the environment
// file is loaded, so it holds exactly what the shell or the client passed
// in — never a value that came from the file. RestoreBootEnv puts those
// back after the reload cycle, and only for variables the file did not
// define, which keeps the file authoritative on /reload while a
// client-provided value survives instead of vanishing.

var (
	bootEnvOnce sync.Once
	bootEnvMu   sync.RWMutex
	bootEnv     map[string]string
)

// CaptureBootEnv snapshots the process environment. It is idempotent: only
// the first call (before any dotenv load) is recorded.
func CaptureBootEnv() {
	bootEnvOnce.Do(func() {
		snapshot := make(map[string]string, len(os.Environ()))
		for _, entry := range os.Environ() {
			if key, value, ok := strings.Cut(entry, "="); ok {
				snapshot[key] = value
			}
		}
		bootEnvMu.Lock()
		bootEnv = snapshot
		bootEnvMu.Unlock()
	})
}

// BootEnv returns the value a variable had when the process started.
func BootEnv(key string) (string, bool) {
	bootEnvMu.RLock()
	defer bootEnvMu.RUnlock()
	v, ok := bootEnv[key]
	return v, ok
}

// RestoreBootEnv re-exports the given variables from the boot snapshot when
// they are currently unset — i.e. the environment file did not define them
// and the reload cycle cleared them. Returns the names actually restored.
func RestoreBootEnv(keys []string) []string {
	restored := make([]string, 0, len(keys))
	for _, key := range keys {
		if isEnvSet(key) {
			continue // the file, or a managed policy, owns it now
		}
		if value, ok := BootEnv(key); ok && value != "" {
			_ = os.Setenv(key, value)
			restored = append(restored, key)
		}
	}
	sort.Strings(restored)
	return restored
}

// ResetBootEnvForTest clears the snapshot so a test can take its own.
func ResetBootEnvForTest() {
	bootEnvOnce = sync.Once{}
	bootEnvMu.Lock()
	bootEnv = nil
	bootEnvMu.Unlock()
}

// ReapplyProjectDotenv re-exports the variables that project overlays had
// contributed and that are currently unset. A reload clears reloadable
// variables and re-reads only the process-wide environment file, which
// would otherwise drop the project's contribution for good — the overlay
// itself runs once per process. Fill-only, like the original overlay.
func ReapplyProjectDotenv() []string {
	dotenvMu.RLock()
	pending := make(map[string]string, len(dotenvOverlayApplied))
	for key, value := range dotenvOverlayApplied {
		pending[key] = value
	}
	dotenvMu.RUnlock()

	restored := make([]string, 0, len(pending))
	for key, value := range pending {
		if isEnvSet(key) {
			continue
		}
		_ = os.Setenv(key, value)
		restored = append(restored, key)
	}
	sort.Strings(restored)
	return restored
}
