/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// SensitiveReadPaths enforces read access control in agent mode.
// By default, only files within the workspace are readable, with specific
// sensitive paths blocked even within the workspace.
type SensitiveReadPaths struct {
	workspaceStrict bool
	allowKubeconfig bool
	extraReadPaths  []string
}

// NewSensitiveReadPaths creates a read path validator configured from environment.
func NewSensitiveReadPaths() *SensitiveReadPaths {
	s := &SensitiveReadPaths{
		workspaceStrict: !strings.EqualFold(os.Getenv("CHATCLI_AGENT_WORKSPACE_STRICT"), "false"),
		allowKubeconfig: strings.EqualFold(os.Getenv("CHATCLI_AGENT_ALLOW_KUBECONFIG"), "true"),
	}

	// Parse extra allowed read paths (colon-separated)
	if extra := os.Getenv("CHATCLI_AGENT_EXTRA_READ_PATHS"); extra != "" {
		for _, p := range strings.Split(extra, ":") {
			p = strings.TrimSpace(p)
			if p != "" {
				s.extraReadPaths = append(s.extraReadPaths, p)
			}
		}
	}

	return s
}

// IsReadAllowed checks whether the given path is safe to read in agent mode.
// workspace is the current working directory / project root.
// Returns (allowed, reason).
func (s *SensitiveReadPaths) IsReadAllowed(path, workspace string) (bool, string) {
	// Resolve to absolute path, following symlinks
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, "failed to resolve absolute path: " + err.Error()
	}

	// Resolve symlinks to prevent bypass
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// File might not exist yet — use the non-resolved path
		resolvedPath = absPath
	}

	// Check against sensitive path patterns
	if blocked, reason := s.isSensitivePath(resolvedPath); blocked {
		return false, reason
	}

	// Workspace boundary enforcement
	if s.workspaceStrict && workspace != "" {
		absWorkspace, err := filepath.Abs(workspace)
		if err == nil {
			resolvedWorkspace, err := filepath.EvalSymlinks(absWorkspace)
			if err == nil {
				absWorkspace = resolvedWorkspace
			}

			// Check if path is within workspace
			if strings.HasPrefix(resolvedPath, absWorkspace+string(filepath.Separator)) || resolvedPath == absWorkspace {
				return true, ""
			}

			// Check extra allowed read paths from env
			for _, extra := range s.extraReadPaths {
				extraAbs, err := filepath.Abs(extra)
				if err != nil {
					continue
				}
				if strings.HasPrefix(resolvedPath, extraAbs+string(filepath.Separator)) || resolvedPath == extraAbs {
					return true, ""
				}
			}

			// Check aux read paths registered at runtime (e.g. session workspace,
			// tool-result overflow dir). These are trusted because only the CLI
			// itself registers them. Resolve symlinks so /tmp vs /private/tmp
			// on macOS matches.
			for _, aux := range auxReadPathsSnapshot() {
				if evalAux, errEval := filepath.EvalSymlinks(aux); errEval == nil {
					aux = evalAux
				}
				if strings.HasPrefix(resolvedPath, aux+string(filepath.Separator)) || resolvedPath == aux {
					return true, ""
				}
			}

			// Allow Go module cache and standard library
			gopath := os.Getenv("GOPATH")
			if gopath == "" {
				gopath = filepath.Join(os.Getenv("HOME"), "go")
			}
			goModCache := filepath.Join(gopath, "pkg", "mod")
			if strings.HasPrefix(resolvedPath, goModCache) {
				return true, ""
			}

			// Allow reading from /usr/local, /usr/share (common system includes)
			for _, sysPath := range []string{"/usr/local/", "/usr/share/", "/usr/include/"} {
				if strings.HasPrefix(resolvedPath, sysPath) {
					return true, ""
				}
			}

			return false, "path is outside workspace boundary (CHATCLI_AGENT_WORKSPACE_STRICT=true)"
		}
	}

	return true, ""
}

// isSensitivePath checks if a path matches known sensitive file patterns.
func (s *SensitiveReadPaths) isSensitivePath(path string) (bool, string) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/root"
	}

	// Exact sensitive paths
	sensitivePaths := []string{
		"/etc/shadow",
		"/etc/gshadow",
		"/etc/master.passwd",
	}
	for _, sp := range sensitivePaths {
		if path == sp {
			return true, "access to " + sp + " is blocked for security"
		}
	}

	// Sensitive directory patterns (block everything under these)
	sensitiveGlobs := map[string]string{
		filepath.Join(home, ".ssh"):              "SSH keys and config are blocked",
		filepath.Join(home, ".gnupg"):            "GPG keys are blocked",
		filepath.Join(home, ".aws"):              "AWS credentials are blocked",
		filepath.Join(home, ".gcloud"):           "GCloud credentials are blocked",
		filepath.Join(home, ".azure"):            "Azure credentials are blocked",
		filepath.Join(home, ".config", "gcloud"): "GCloud config is blocked",
	}

	for dir, reason := range sensitiveGlobs {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) || path == dir {
			return true, reason
		}
	}

	// kubeconfig (controlled by CHATCLI_AGENT_ALLOW_KUBECONFIG)
	kubeconfigPath := filepath.Join(home, ".kube", "config")
	if !s.allowKubeconfig && path == kubeconfigPath {
		return true, "kubeconfig access is blocked (set CHATCLI_AGENT_ALLOW_KUBECONFIG=true to allow)"
	}

	// Sensitive individual files
	sensitiveFiles := map[string]string{
		filepath.Join(home, ".netrc"):                       "netrc may contain credentials",
		filepath.Join(home, ".npmrc"):                       "npmrc may contain auth tokens",
		filepath.Join(home, ".docker", "config.json"):       "Docker config may contain registry credentials",
		filepath.Join(home, ".pypirc"):                      "pypirc may contain credentials",
		filepath.Join(home, ".gem", "credentials"):          "gem credentials file",
		filepath.Join(home, ".m2", "settings.xml"):          "Maven settings may contain credentials",
		filepath.Join(home, ".gradle", "gradle.properties"): "Gradle properties may contain credentials",
	}
	for fp, reason := range sensitiveFiles {
		if path == fp {
			return true, reason
		}
	}

	// /proc/*/environ — process environment variables
	if strings.HasPrefix(path, "/proc/") && strings.HasSuffix(path, "/environ") {
		return true, "process environment files are blocked"
	}

	// Sensitive file extensions outside workspace (PEM, key files, etc.)
	ext := strings.ToLower(filepath.Ext(path))
	sensitiveExts := map[string]bool{
		".pem": true, ".key": true, ".p12": true,
		".pfx": true, ".jks": true, ".keystore": true,
		".p8": true, ".der": true,
	}
	if sensitiveExts[ext] && !strings.HasPrefix(path, home) {
		// Only block sensitive extensions outside home directory
		// (within project they might be test fixtures)
		return true, "files with extension " + ext + " outside home directory are blocked"
	}

	return false, ""
}
