package controllers

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const (
	sourceRepoBaseDir     = "/tmp/chatcli-repos"
	maxRecentCommits      = 20
	maxSourceContextChars = 5000
)

// SourceRepositoryReconciler reconciles SourceRepository objects.
// It clones/syncs git repositories and indexes them for code-aware incident analysis.
type SourceRepositoryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=sourcerepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=sourcerepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *SourceRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var repo platformv1alpha1.SourceRepository
	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		if errors.IsNotFound(err) {
			// Clean up local clone if the CR was deleted
			localPath := filepath.Join(sourceRepoBaseDir, req.Namespace, req.Name)
			os.RemoveAll(localPath)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	localPath := filepath.Join(sourceRepoBaseDir, repo.Namespace, repo.Name)

	// Check if we need to sync
	needsSync := !repo.Status.Ready ||
		repo.Status.LastSyncedAt == nil ||
		time.Since(repo.Status.LastSyncedAt.Time) > time.Duration(repo.Spec.SyncIntervalMinutes)*time.Minute

	if !needsSync {
		interval := time.Duration(repo.Spec.SyncIntervalMinutes) * time.Minute
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	logger.Info("Syncing source repository", "name", repo.Name, "url", repo.Spec.URL)

	// Resolve auth credentials
	authEnv, err := r.resolveAuth(ctx, &repo)
	if err != nil {
		repo.Status.Ready = false
		repo.Status.Error = fmt.Sprintf("auth error: %v", err)
		_ = r.Status().Update(ctx, &repo)
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Clone or pull
	if _, err := os.Stat(filepath.Join(localPath, ".git")); os.IsNotExist(err) {
		if err := r.cloneRepo(ctx, &repo, localPath, authEnv); err != nil {
			repo.Status.Ready = false
			repo.Status.Error = fmt.Sprintf("clone failed: %v", err)
			_ = r.Status().Update(ctx, &repo)
			return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
		}
	} else {
		if err := r.pullRepo(ctx, &repo, localPath, authEnv); err != nil {
			repo.Status.Ready = false
			repo.Status.Error = fmt.Sprintf("pull failed: %v", err)
			_ = r.Status().Update(ctx, &repo)
			return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
		}
	}

	// Index the repository
	if err := r.indexRepo(ctx, &repo, localPath); err != nil {
		repo.Status.Ready = false
		repo.Status.Error = fmt.Sprintf("index failed: %v", err)
		_ = r.Status().Update(ctx, &repo)
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Update status
	now := metav1.Now()
	repo.Status.Ready = true
	repo.Status.Error = ""
	repo.Status.LastSyncedAt = &now
	repo.Status.LocalPath = localPath

	if err := r.Status().Update(ctx, &repo); err != nil {
		return ctrl.Result{}, err
	}

	interval := time.Duration(repo.Spec.SyncIntervalMinutes) * time.Minute
	logger.Info("Source repository synced", "name", repo.Name, "commits", len(repo.Status.RecentCommits))
	return ctrl.Result{RequeueAfter: interval}, nil
}

// resolveAuth resolves authentication credentials from a Secret reference.
func (r *SourceRepositoryReconciler) resolveAuth(ctx context.Context, repo *platformv1alpha1.SourceRepository) ([]string, error) {
	if repo.Spec.AuthType == platformv1alpha1.SourceRepoAuthNone || repo.Spec.SecretRef == "" {
		return nil, nil
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Name: repo.Spec.SecretRef, Namespace: repo.Namespace,
	}, &secret); err != nil {
		return nil, fmt.Errorf("secret %s not found: %w", repo.Spec.SecretRef, err)
	}

	var env []string
	switch repo.Spec.AuthType {
	case platformv1alpha1.SourceRepoAuthToken:
		token := string(secret.Data["token"])
		if token == "" {
			return nil, fmt.Errorf("secret %s missing 'token' key", repo.Spec.SecretRef)
		}
		// For HTTPS URLs, inject token into URL via credential helper
		env = append(env, fmt.Sprintf("GIT_ASKPASS=echo"), fmt.Sprintf("GIT_TOKEN=%s", token))

	case platformv1alpha1.SourceRepoAuthSSH:
		sshKey := secret.Data["ssh-key"]
		if len(sshKey) == 0 {
			return nil, fmt.Errorf("secret %s missing 'ssh-key' key", repo.Spec.SecretRef)
		}
		// Write SSH key to temp file
		keyFile := filepath.Join(sourceRepoBaseDir, ".ssh", repo.Name)
		os.MkdirAll(filepath.Dir(keyFile), 0700)
		if err := os.WriteFile(keyFile, sshKey, 0600); err != nil {
			return nil, fmt.Errorf("writing SSH key: %w", err)
		}
		env = append(env, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=no", keyFile))

	case platformv1alpha1.SourceRepoAuthBasic:
		username := string(secret.Data["username"])
		password := string(secret.Data["password"])
		if username == "" || password == "" {
			return nil, fmt.Errorf("secret %s missing 'username' or 'password' key", repo.Spec.SecretRef)
		}
		env = append(env, fmt.Sprintf("GIT_USERNAME=%s", username), fmt.Sprintf("GIT_PASSWORD=%s", password))
	}

	return env, nil
}

// cloneRepo performs a shallow clone of the repository.
func (r *SourceRepositoryReconciler) cloneRepo(ctx context.Context, repo *platformv1alpha1.SourceRepository, localPath string, authEnv []string) error {
	os.MkdirAll(filepath.Dir(localPath), 0755)

	branch := repo.Spec.Branch
	if branch == "" {
		branch = "main"
	}

	args := []string{"clone", "--depth", "50", "--single-branch", "--branch", branch, repo.Spec.URL, localPath}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), authEnv...)

	// Apply token auth for HTTPS
	if repo.Spec.AuthType == platformv1alpha1.SourceRepoAuthToken {
		// Rewrite URL to include token
		cmd = r.applyTokenAuth(ctx, repo.Spec.URL, authEnv, args)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(output), err)
	}

	return nil
}

func (r *SourceRepositoryReconciler) applyTokenAuth(ctx context.Context, repoURL string, authEnv []string, args []string) *exec.Cmd {
	// Extract token from env
	var token string
	for _, e := range authEnv {
		if strings.HasPrefix(e, "GIT_TOKEN=") {
			token = strings.TrimPrefix(e, "GIT_TOKEN=")
		}
	}

	if token != "" && strings.HasPrefix(repoURL, "https://") {
		// Inject token into URL: https://token@github.com/...
		authedURL := strings.Replace(repoURL, "https://", fmt.Sprintf("https://x-access-token:%s@", token), 1)
		newArgs := make([]string, len(args))
		copy(newArgs, args)
		for i, a := range newArgs {
			if a == repoURL {
				newArgs[i] = authedURL
			}
		}
		cmd := exec.CommandContext(ctx, "git", newArgs...)
		cmd.Env = os.Environ()
		return cmd
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), authEnv...)
	return cmd
}

// pullRepo fetches latest changes.
func (r *SourceRepositoryReconciler) pullRepo(ctx context.Context, repo *platformv1alpha1.SourceRepository, localPath string, authEnv []string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", localPath, "fetch", "--depth", "50", "origin")
	cmd.Env = append(os.Environ(), authEnv...)

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %s: %w", string(output), err)
	}

	branch := repo.Spec.Branch
	if branch == "" {
		branch = "main"
	}

	cmd = exec.CommandContext(ctx, "git", "-C", localPath, "reset", "--hard", fmt.Sprintf("origin/%s", branch))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset: %s: %w", string(output), err)
	}

	return nil
}

// indexRepo indexes the repository contents and populates status fields.
func (r *SourceRepositoryReconciler) indexRepo(ctx context.Context, repo *platformv1alpha1.SourceRepository, localPath string) error {
	// Get recent commits
	commits, err := r.getRecentCommits(ctx, localPath)
	if err != nil {
		return err
	}
	repo.Status.RecentCommits = commits
	if len(commits) > 0 {
		repo.Status.HeadCommit = &commits[0]
	}

	// Detect languages
	repo.Status.DetectedLanguages = detectLanguages(localPath)

	// Find entrypoint files
	repo.Status.EntrypointFiles = findEntrypoints(localPath)

	// Find config files
	repo.Status.ConfigFiles = findConfigFiles(localPath)

	return nil
}

// getRecentCommits retrieves the last N commits with file change info.
func (r *SourceRepositoryReconciler) getRecentCommits(ctx context.Context, localPath string) ([]platformv1alpha1.GitCommitInfo, error) {
	// git log with format: SHA|author|timestamp|message
	cmd := exec.CommandContext(ctx, "git", "-C", localPath, "log",
		fmt.Sprintf("-%d", maxRecentCommits),
		"--format=%H|%an|%aI|%s",
		"--name-only")

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	var commits []platformv1alpha1.GitCommitInfo
	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	var current *platformv1alpha1.GitCommitInfo
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if current != nil {
				commits = append(commits, *current)
				current = nil
			}
			continue
		}

		parts := strings.SplitN(line, "|", 4)
		if len(parts) == 4 && len(parts[0]) == 40 { // SHA is 40 chars
			if current != nil {
				commits = append(commits, *current)
			}
			ts, _ := time.Parse(time.RFC3339, parts[2])
			current = &platformv1alpha1.GitCommitInfo{
				SHA:       parts[0],
				Author:    parts[1],
				Timestamp: metav1.Time{Time: ts},
				Message:   parts[3],
			}
		} else if current != nil && line != "" {
			// This is a changed file name
			if len(current.FilesChanged) < 20 { // limit files per commit
				current.FilesChanged = append(current.FilesChanged, line)
			}
		}
	}
	if current != nil {
		commits = append(commits, *current)
	}

	return commits, nil
}

// detectLanguages identifies programming languages used in the repository.
func detectLanguages(localPath string) []string {
	extensions := map[string]string{
		".go":    "Go",
		".java":  "Java",
		".py":    "Python",
		".js":    "JavaScript",
		".ts":    "TypeScript",
		".rs":    "Rust",
		".rb":    "Ruby",
		".php":   "PHP",
		".cs":    "C#",
		".cpp":   "C++",
		".c":     "C",
		".swift": "Swift",
		".kt":    "Kotlin",
		".scala": "Scala",
	}

	langCount := make(map[string]int)
	filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			// Skip hidden directories and vendor/node_modules
			if info != nil && info.IsDir() {
				base := filepath.Base(path)
				if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := filepath.Ext(path)
		if lang, ok := extensions[ext]; ok {
			langCount[lang]++
		}
		return nil
	})

	type langEntry struct {
		name  string
		count int
	}
	var sorted []langEntry
	for name, count := range langCount {
		sorted = append(sorted, langEntry{name, count})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	var result []string
	for i, e := range sorted {
		if i >= 5 {
			break
		}
		result = append(result, e.name)
	}
	return result
}

// findEntrypoints finds application entrypoint files.
func findEntrypoints(localPath string) []string {
	entrypoints := []string{
		"main.go", "cmd/main.go", "app.py", "manage.py", "wsgi.py",
		"index.js", "server.js", "app.js", "index.ts", "server.ts", "app.ts",
		"Application.java", "App.java", "Main.java",
		"main.rs", "lib.rs",
		"Program.cs", "Startup.cs",
		"main.rb", "app.rb", "config.ru",
	}

	var found []string
	for _, ep := range entrypoints {
		matches, _ := filepath.Glob(filepath.Join(localPath, "**", ep))
		for _, m := range matches {
			rel, _ := filepath.Rel(localPath, m)
			if rel != "" && !strings.Contains(rel, "vendor/") && !strings.Contains(rel, "node_modules/") {
				found = append(found, rel)
			}
		}
		// Also check root
		if _, err := os.Stat(filepath.Join(localPath, ep)); err == nil {
			found = append(found, ep)
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, f := range found {
		if !seen[f] {
			seen[f] = true
			unique = append(unique, f)
		}
	}

	return unique
}

// findConfigFiles finds configuration files relevant for deployment troubleshooting.
func findConfigFiles(localPath string) []string {
	patterns := []string{
		"Dockerfile", "Dockerfile.*", "docker-compose.yml", "docker-compose.yaml",
		"Makefile", "Procfile",
		"k8s/*.yaml", "k8s/*.yml", "kubernetes/*.yaml", "kubernetes/*.yml",
		"deploy/*.yaml", "deploy/*.yml", "manifests/*.yaml", "manifests/*.yml",
		"helm/**/Chart.yaml", "charts/**/Chart.yaml",
		"values.yaml", "values-*.yaml",
		".github/workflows/*.yml", ".github/workflows/*.yaml",
		".gitlab-ci.yml", "Jenkinsfile",
		"go.mod", "package.json", "requirements.txt", "Pipfile", "Cargo.toml",
		"pom.xml", "build.gradle", "build.gradle.kts",
	}

	var found []string
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(localPath, pattern))
		for _, m := range matches {
			rel, _ := filepath.Rel(localPath, m)
			if rel != "" {
				found = append(found, rel)
			}
		}
	}

	if len(found) > 30 {
		found = found[:30]
	}

	return found
}

// SourceCodeAnalyzer provides code-aware context for incident analysis.
type SourceCodeAnalyzer struct {
	client client.Client
}

// NewSourceCodeAnalyzer creates a new SourceCodeAnalyzer.
func NewSourceCodeAnalyzer(c client.Client) *SourceCodeAnalyzer {
	return &SourceCodeAnalyzer{client: c}
}

// SourceCodeContext contains code analysis results for an incident.
type SourceCodeContext struct {
	// RecentChanges are commits near the incident time.
	RecentChanges []platformv1alpha1.GitCommitInfo
	// SuspectedCommit is the most likely commit that caused the incident.
	SuspectedCommit *platformv1alpha1.GitCommitInfo
	// RelevantCode contains code snippets from files referenced in stack traces.
	RelevantCode []CodeSnippet
	// ConfigContext contains deployment-related config file contents.
	ConfigContext []ConfigFileContent
	// Summary is a text summary.
	Summary string
}

// CodeSnippet is a code excerpt around a line referenced in a stack trace.
type CodeSnippet struct {
	FilePath  string
	Language  string
	StartLine int
	EndLine   int
	Content   string
	Reason    string // why this snippet is relevant
}

// ConfigFileContent holds a config file's content.
type ConfigFileContent struct {
	FilePath string
	Content  string
}

// BuildSourceContext correlates the incident with source code.
func (sa *SourceCodeAnalyzer) BuildSourceContext(ctx context.Context, resource platformv1alpha1.ResourceRef, incidentTime time.Time, stackTraces []StackTrace) (*SourceCodeContext, error) {
	// Find SourceRepository for this resource
	var repos platformv1alpha1.SourceRepositoryList
	if err := sa.client.List(ctx, &repos, client.InNamespace(resource.Namespace)); err != nil {
		return nil, err
	}

	var repo *platformv1alpha1.SourceRepository
	for i := range repos.Items {
		r := &repos.Items[i]
		if r.Spec.Resource.Name == resource.Name && r.Spec.Resource.Kind == resource.Kind &&
			r.Status.Ready && r.Status.LocalPath != "" {
			repo = r
			break
		}
	}

	if repo == nil {
		return nil, nil // No source repository configured
	}

	result := &SourceCodeContext{}

	// Find commits near the incident time
	result.RecentChanges = findCommitsNearTime(repo.Status.RecentCommits, incidentTime, 30*time.Minute)

	// Determine suspected commit
	result.SuspectedCommit = findSuspectedCommit(result.RecentChanges, incidentTime)

	// Extract relevant code from stack traces
	result.RelevantCode = extractRelevantCode(repo.Status.LocalPath, repo.Spec.Paths, stackTraces)

	// Read relevant config files
	result.ConfigContext = readConfigFiles(repo.Status.LocalPath, repo.Status.ConfigFiles)

	// Build summary
	result.Summary = buildSourceCodeSummary(result)

	return result, nil
}

// findCommitsNearTime returns commits within the given window before the incident.
func findCommitsNearTime(commits []platformv1alpha1.GitCommitInfo, incidentTime time.Time, window time.Duration) []platformv1alpha1.GitCommitInfo {
	start := incidentTime.Add(-window)
	var near []platformv1alpha1.GitCommitInfo
	for _, c := range commits {
		if c.Timestamp.Time.After(start) && c.Timestamp.Time.Before(incidentTime.Add(5*time.Minute)) {
			near = append(near, c)
		}
	}
	return near
}

// findSuspectedCommit identifies the most likely commit that caused the incident.
func findSuspectedCommit(recentChanges []platformv1alpha1.GitCommitInfo, incidentTime time.Time) *platformv1alpha1.GitCommitInfo {
	if len(recentChanges) == 0 {
		return nil
	}

	// Sort by proximity to incident time (closest before incident wins)
	type scored struct {
		commit platformv1alpha1.GitCommitInfo
		score  float64
	}

	var candidates []scored
	for _, c := range recentChanges {
		diff := incidentTime.Sub(c.Timestamp.Time)
		if diff < 0 {
			continue // committed after incident
		}

		// Score: closer to incident = higher score, more files changed = higher score
		score := 1.0 / (1.0 + diff.Minutes())
		score *= float64(1 + len(c.FilesChanged))
		candidates = append(candidates, scored{commit: c, score: score})
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	return &candidates[0].commit
}

// extractRelevantCode reads code files referenced in stack traces.
func extractRelevantCode(localPath string, relevantPaths []string, stackTraces []StackTrace) []CodeSnippet {
	var snippets []CodeSnippet

	for _, trace := range stackTraces {
		for _, frame := range trace.Frames {
			filePath, lineNum := parseStackFrame(frame, trace.Language)
			if filePath == "" || lineNum == 0 {
				continue
			}

			// Try to find the file in the repo
			fullPath := findFileInRepo(localPath, filePath, relevantPaths)
			if fullPath == "" {
				continue
			}

			// Read the file and extract context around the line
			content, err := readLinesAround(fullPath, lineNum, 5)
			if err != nil {
				continue
			}

			rel, _ := filepath.Rel(localPath, fullPath)
			snippets = append(snippets, CodeSnippet{
				FilePath:  rel,
				Language:  trace.Language,
				StartLine: lineNum - 5,
				EndLine:   lineNum + 5,
				Content:   content,
				Reason:    fmt.Sprintf("Referenced in %s stack trace: %s", trace.Language, trace.ExceptionType),
			})

			if len(snippets) >= 5 { // Limit to 5 snippets
				return snippets
			}
		}
	}

	return snippets
}

// parseStackFrame extracts file path and line number from a stack frame string.
func parseStackFrame(frame, language string) (string, int) {
	frame = strings.TrimSpace(frame)

	switch language {
	case "go":
		// format: package/file.go:123
		if idx := strings.LastIndex(frame, ":"); idx > 0 {
			path := frame[:idx]
			var line int
			fmt.Sscanf(frame[idx+1:], "%d", &line)
			return path, line
		}

	case "java":
		// format: at com.example.MyClass.method(MyClass.java:42)
		frame = strings.TrimPrefix(frame, "at ")
		if start := strings.LastIndex(frame, "("); start > 0 {
			if end := strings.LastIndex(frame, ")"); end > start {
				fileInfo := frame[start+1 : end]
				parts := strings.SplitN(fileInfo, ":", 2)
				if len(parts) == 2 {
					var line int
					fmt.Sscanf(parts[1], "%d", &line)
					return parts[0], line
				}
			}
		}

	case "python":
		// format: File "/path/to/file.py", line 42, in function
		if strings.HasPrefix(frame, "File \"") {
			parts := strings.SplitN(frame, "\"", 3)
			if len(parts) >= 2 {
				path := parts[1]
				var line int
				if lineIdx := strings.Index(frame, "line "); lineIdx > 0 {
					fmt.Sscanf(frame[lineIdx+5:], "%d", &line)
				}
				return path, line
			}
		}

	case "nodejs":
		// format: at functionName (/path/to/file.js:42:10)
		frame = strings.TrimPrefix(frame, "at ")
		if start := strings.Index(frame, "("); start >= 0 {
			frame = frame[start+1:]
		}
		frame = strings.TrimSuffix(frame, ")")
		parts := strings.SplitN(frame, ":", 3)
		if len(parts) >= 2 {
			var line int
			fmt.Sscanf(parts[1], "%d", &line)
			return parts[0], line
		}
	}

	return "", 0
}

// findFileInRepo searches for a file within the cloned repo.
func findFileInRepo(localPath, fileName string, relevantPaths []string) string {
	// Try direct match
	fullPath := filepath.Join(localPath, fileName)
	if _, err := os.Stat(fullPath); err == nil {
		return fullPath
	}

	// Try within relevant paths
	for _, rp := range relevantPaths {
		fullPath = filepath.Join(localPath, rp, fileName)
		if _, err := os.Stat(fullPath); err == nil {
			return fullPath
		}
	}

	// Try finding just the filename
	baseName := filepath.Base(fileName)
	var found string
	filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				base := filepath.Base(path)
				if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if info.Name() == baseName {
			found = path
			return filepath.SkipAll
		}
		return nil
	})

	return found
}

// readLinesAround reads lines around a target line number.
func readLinesAround(filePath string, targetLine, contextLines int) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	startLine := targetLine - contextLines
	if startLine < 1 {
		startLine = 1
	}
	endLine := targetLine + contextLines

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum >= startLine && lineNum <= endLine {
			marker := "  "
			if lineNum == targetLine {
				marker = ">>"
			}
			sb.WriteString(fmt.Sprintf("%s %4d | %s\n", marker, lineNum, scanner.Text()))
		}
		if lineNum > endLine {
			break
		}
	}

	return sb.String(), nil
}

// readConfigFiles reads deployment-related config files.
func readConfigFiles(localPath string, configFiles []string) []ConfigFileContent {
	var configs []ConfigFileContent

	important := []string{"Dockerfile", "values.yaml", "Chart.yaml"}
	for _, cf := range configFiles {
		isImportant := false
		for _, imp := range important {
			if strings.Contains(cf, imp) {
				isImportant = true
				break
			}
		}
		if !isImportant {
			continue
		}

		fullPath := filepath.Join(localPath, cf)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		content := string(data)
		if len(content) > 2000 {
			content = content[:2000] + "\n... (truncated)"
		}

		configs = append(configs, ConfigFileContent{
			FilePath: cf,
			Content:  content,
		})

		if len(configs) >= 3 {
			break
		}
	}

	return configs
}

func buildSourceCodeSummary(ctx *SourceCodeContext) string {
	var parts []string

	if ctx.SuspectedCommit != nil {
		parts = append(parts, fmt.Sprintf("Suspected commit: %s by %s (%s): %s",
			ctx.SuspectedCommit.SHA[:8], ctx.SuspectedCommit.Author,
			ctx.SuspectedCommit.Timestamp.Format("2006-01-02 15:04"),
			ctx.SuspectedCommit.Message))
	}

	if len(ctx.RecentChanges) > 0 {
		parts = append(parts, fmt.Sprintf("%d commits found in the 30-minute window before the incident", len(ctx.RecentChanges)))
	}

	if len(ctx.RelevantCode) > 0 {
		parts = append(parts, fmt.Sprintf("%d code snippets extracted from stack trace references", len(ctx.RelevantCode)))
	}

	if len(parts) == 0 {
		return "No source code correlation found."
	}

	return strings.Join(parts, "; ")
}

// FormatForAI formats the source code context for LLM consumption.
func (ctx *SourceCodeContext) FormatForAI() string {
	if ctx == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Source Code Analysis\n\n")

	// Suspected commit
	if ctx.SuspectedCommit != nil {
		c := ctx.SuspectedCommit
		sb.WriteString("### Suspected Commit\n")
		sb.WriteString(fmt.Sprintf("**%s** by %s at %s\n",
			c.SHA[:8], c.Author, c.Timestamp.Format("2006-01-02 15:04:05")))
		sb.WriteString(fmt.Sprintf("Message: %s\n", c.Message))
		if len(c.FilesChanged) > 0 {
			sb.WriteString("Files changed:\n")
			for _, f := range c.FilesChanged {
				sb.WriteString(fmt.Sprintf("  - %s\n", f))
			}
		}
		sb.WriteString("\n")
	}

	// Recent changes timeline
	if len(ctx.RecentChanges) > 0 {
		sb.WriteString("### Recent Commits (30min before incident)\n")
		for _, c := range ctx.RecentChanges {
			sb.WriteString(fmt.Sprintf("- %s %s: %s (%d files)\n",
				c.SHA[:8], c.Author, c.Message, len(c.FilesChanged)))
		}
		sb.WriteString("\n")
	}

	// Code snippets from stack traces
	if len(ctx.RelevantCode) > 0 {
		sb.WriteString("### Code from Stack Trace References\n")
		for _, s := range ctx.RelevantCode {
			sb.WriteString(fmt.Sprintf("**%s** (%s, lines %d-%d)\n", s.FilePath, s.Reason, s.StartLine, s.EndLine))
			sb.WriteString("```" + s.Language + "\n")
			sb.WriteString(s.Content)
			sb.WriteString("```\n\n")
		}
	}

	// Config files
	if len(ctx.ConfigContext) > 0 {
		sb.WriteString("### Deployment Configuration\n")
		for _, cf := range ctx.ConfigContext {
			sb.WriteString(fmt.Sprintf("**%s**\n```\n%s\n```\n\n", cf.FilePath, cf.Content))
		}
	}

	result := sb.String()
	if len(result) > maxSourceContextChars {
		result = result[:maxSourceContextChars-3] + "..."
	}
	return result
}

// SetupWithManager sets up the controller with the Manager.
func (r *SourceRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.SourceRepository{}).
		Complete(r)
}
