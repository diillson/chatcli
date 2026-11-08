package controllers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"github.com/diillson/chatcli/pkg/textfold"
)

const (
	sourceRepoBaseDir     = "/tmp/chatcli-repos"
	maxRecentCommits      = 20
	maxFilesPerCommit     = 20
	maxSourceContextChars = 5000
	// gitCloneDepth bounds the history a clone and its fetches carry:
	// enough for the recent-commit window, not the whole repository.
	gitCloneDepth = 50
)

// SourceRepositoryReconciler reconciles SourceRepository objects.
// It clones/syncs git repositories and indexes them for code-aware incident analysis.
type SourceRepositoryReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// BaseDir overrides where clones and credential files live
	// (default /tmp/chatcli-repos, an emptyDir in the operator pod).
	BaseDir string
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=sourcerepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=sourcerepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *SourceRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var repo platformv1alpha1.SourceRepository
	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		if apierrors.IsNotFound(err) {
			// Clean up local clone if the CR was deleted
			localPath := filepath.Join(r.baseDir(), req.Namespace, req.Name)
			if rmErr := os.RemoveAll(localPath); rmErr != nil {
				logger.Error(rmErr, "Failed to remove local clone of deleted repository", "path", localPath)
			}
			r.removeRepoCredentialFiles(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	localPath := filepath.Join(r.baseDir(), repo.Namespace, repo.Name)

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
	auth, err := r.resolveAuth(ctx, &repo)
	if err != nil {
		repo.Status.Ready = false
		repo.Status.Error = fmt.Sprintf("auth error: %v", err)
		_ = r.Status().Update(ctx, &repo)
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
	defer auth.done()

	// Clone or pull
	if _, err := os.Stat(filepath.Join(localPath, ".git")); os.IsNotExist(err) {
		if err := r.cloneRepo(ctx, &repo, localPath, auth); err != nil {
			repo.Status.Ready = false
			repo.Status.Error = fmt.Sprintf("clone failed: %v", err)
			_ = r.Status().Update(ctx, &repo)
			return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
		}
	} else {
		if err := r.pullRepo(ctx, &repo, localPath, auth); err != nil {
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

// gitBranchPattern allows the conservative charset git branch names use in
// practice, and rejects a leading "-" and anything a ref name cannot hold.
var gitBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// validateGitInputs is the policy on what a SourceRepository may point at:
// a remote over HTTPS or SSH, on a plainly named branch. SourceRepository
// objects are namespace-scoped and may be authored by tenants, so the URL
// and branch are treated as untrusted input; local paths, file:// and
// git's ext:: helper are refused.
func validateGitInputs(repoURL, branch string) error {
	if strings.HasPrefix(repoURL, "-") || strings.Contains(repoURL, "ext::") {
		return fmt.Errorf("unsupported repository URL %q", repoURL)
	}
	if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "ssh://") && !strings.HasPrefix(repoURL, "git@") {
		return fmt.Errorf("repository URL must use https://, ssh:// or git@ form, got %q", repoURL)
	}
	if branch != "" && !gitBranchPattern.MatchString(branch) {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	return nil
}

// trackedBranch is the branch a SourceRepository follows.
func trackedBranch(repo *platformv1alpha1.SourceRepository) string {
	if repo.Spec.Branch == "" {
		return "main"
	}
	return repo.Spec.Branch
}

// cloneRepo performs a shallow, single-branch clone of the repository.
// The origin URL is the plain spec URL: credentials come from auth, per
// connection, and are never written to the clone.
func (r *SourceRepositoryReconciler) cloneRepo(ctx context.Context, repo *platformv1alpha1.SourceRepository, localPath string, auth *gitAuth) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0750); err != nil {
		return fmt.Errorf("creating clone parent dir: %w", err)
	}

	branch := trackedBranch(repo)
	if err := validateGitInputs(repo.Spec.URL, branch); err != nil {
		return err
	}

	_, err := git.PlainCloneContext(ctx, localPath, false, &git.CloneOptions{
		URL:           repo.Spec.URL,
		Auth:          auth.authMethod(),
		CABundle:      auth.ca(),
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         gitCloneDepth,
		Tags:          git.NoTags,
	})
	if err != nil {
		// A failed clone leaves no half-initialized .git behind, so the
		// next sync clones again instead of fetching into it.
		_ = os.RemoveAll(localPath)
		return fmt.Errorf("git clone failed: %s", redactURLCredentials(err.Error()))
	}
	return nil
}

// pullRepo fetches the tracked branch and resets the work tree to it, the
// equivalent of fetch --depth followed by reset --hard origin/<branch>.
func (r *SourceRepositoryReconciler) pullRepo(ctx context.Context, repo *platformv1alpha1.SourceRepository, localPath string, auth *gitAuth) error {
	branch := trackedBranch(repo)
	if err := validateGitInputs(repo.Spec.URL, branch); err != nil {
		return err
	}

	clone, err := git.PlainOpen(localPath)
	if err != nil {
		return fmt.Errorf("git fetch: opening clone: %w", err)
	}
	if err := scrubOriginURL(clone, localPath); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}

	remoteRef := plumbing.NewRemoteReferenceName(git.DefaultRemoteName, branch)
	err = clone.FetchContext(ctx, &git.FetchOptions{
		RemoteName: git.DefaultRemoteName,
		Auth:       auth.authMethod(),
		CABundle:   auth.ca(),
		RefSpecs: []config.RefSpec{
			config.RefSpec("+" + plumbing.NewBranchReferenceName(branch).String() + ":" + remoteRef.String()),
		},
		Depth: gitCloneDepth,
		Tags:  git.NoTags,
		Force: true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("git fetch: %s", redactURLCredentials(err.Error()))
	}

	ref, err := clone.Reference(remoteRef, true)
	if err != nil {
		return fmt.Errorf("git reset: resolving %s: %w", remoteRef.Short(), err)
	}
	if err := bringHEADToLife(clone, ref.Hash()); err != nil {
		return fmt.Errorf("git reset: %w", err)
	}
	worktree, err := clone.Worktree()
	if err != nil {
		return fmt.Errorf("git reset: %w", err)
	}
	if err := worktree.Reset(&git.ResetOptions{Commit: ref.Hash(), Mode: git.HardReset}); err != nil {
		return fmt.Errorf("git reset: %w", err)
	}
	return nil
}

// bringHEADToLife points an unborn branch (HEAD of a repository that was
// initialized but never committed to) at commit, which is what git reset
// --hard does there; go-git's Reset needs the branch to exist first.
func bringHEADToLife(clone *git.Repository, commit plumbing.Hash) error {
	head, err := clone.Reference(plumbing.HEAD, false)
	if err != nil {
		return err
	}
	if head.Type() != plumbing.SymbolicReference {
		return nil
	}
	if _, err := clone.Reference(head.Target(), false); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return err
	}
	return clone.Storer.SetReference(plumbing.NewHashReference(head.Target(), commit))
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

// getRecentCommits retrieves the last N commits with file change info,
// newest first, the way git log --name-only reports them.
func (r *SourceRepositoryReconciler) getRecentCommits(ctx context.Context, localPath string) ([]platformv1alpha1.GitCommitInfo, error) {
	clone, err := git.PlainOpen(localPath)
	if err != nil {
		return nil, fmt.Errorf("git log: opening clone: %w", err)
	}
	head, err := clone.Head()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	iter, err := clone.Log(&git.LogOptions{From: head.Hash(), Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	defer iter.Close()

	var commits []platformv1alpha1.GitCommitInfo
	for len(commits) < maxRecentCommits {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("git log: %w", err)
		}
		commit, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("git log: %w", err)
		}
		commits = append(commits, platformv1alpha1.GitCommitInfo{
			SHA:          commit.Hash.String(),
			Author:       commit.Author.Name,
			Timestamp:    metav1.NewTime(commit.Author.When),
			Message:      commitSubject(commit.Message),
			FilesChanged: changedFiles(ctx, commit),
		})
	}
	return commits, nil
}

// commitSubject is git's %s: the first line of the message.
func commitSubject(message string) string {
	subject, _, _ := strings.Cut(message, "\n")
	return strings.TrimSpace(subject)
}

// changedFiles lists the paths a commit touched against its first parent,
// capped at maxFilesPerCommit. A root commit, or the boundary commit of a
// shallow clone whose parent was not fetched, is compared with the empty
// tree, which is what git log shows for it.
func changedFiles(ctx context.Context, commit *object.Commit) []string {
	tree, err := commit.Tree()
	if err != nil {
		return nil
	}
	var parentTree *object.Tree
	if commit.NumParents() > 0 {
		if parent, err := commit.Parent(0); err == nil {
			parentTree, _ = parent.Tree()
		}
	}
	changes, err := object.DiffTreeWithOptions(ctx, parentTree, tree, object.DefaultDiffTreeOptions)
	if err != nil {
		return nil
	}
	var files []string
	for _, change := range changes {
		if len(files) == maxFilesPerCommit {
			break
		}
		name := change.To.Name
		if name == "" {
			name = change.From.Name
		}
		files = append(files, name)
	}
	return files
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
	// Walk errors mean a partially indexed tree — acceptable for language
	// detection, which is advisory.
	_ = filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
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
	sorted := make([]langEntry, 0, len(langCount))
	for name, count := range langCount {
		sorted = append(sorted, langEntry{name, count})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	result := make([]string, 0, 5)
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
		if c.Timestamp.After(start) && c.Timestamp.Time.Before(incidentTime.Add(5*time.Minute)) {
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

	candidates := make([]scored, 0, len(recentChanges))
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
			content, err := readLinesAround(localPath, fullPath, lineNum, 5)
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
			return path, leadingInt(frame[idx+1:])
		}

	case "java":
		// format: at com.example.MyClass.method(MyClass.java:42)
		frame = strings.TrimPrefix(frame, "at ")
		if start := strings.LastIndex(frame, "("); start > 0 {
			if end := strings.LastIndex(frame, ")"); end > start {
				fileInfo := frame[start+1 : end]
				parts := strings.SplitN(fileInfo, ":", 2)
				if len(parts) == 2 {
					return parts[0], leadingInt(parts[1])
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
					line = leadingInt(frame[lineIdx+5:])
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
			return parts[0], leadingInt(parts[1])
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
	// Walk errors mean some subtrees were unreadable; an empty result is
	// the documented "not found" answer in that case.
	_ = filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
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

// readLinesAround reads lines around a target line number. filePath is
// resolved against rootDir through os.Root, so a stack-frame path crafted
// to traverse or symlink outside the repository clone cannot escape it —
// stack traces arrive on Issue objects and are untrusted input.
func readLinesAround(rootDir, filePath string, targetLine, contextLines int) (string, error) {
	rel, err := filepath.Rel(rootDir, filePath)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

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
			fmt.Fprintf(&sb, "%s %4d | %s\n", marker, lineNum, scanner.Text())
		}
		if lineNum > endLine {
			break
		}
	}

	return sb.String(), nil
}

// readConfigFiles reads deployment-related config files.
func readConfigFiles(localPath string, configFiles []string) []ConfigFileContent {
	configs := make([]ConfigFileContent, 0, len(configFiles))

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
		// Confinement: config paths come from our own index, but re-check
		// that the join did not escape the clone root before reading.
		if !strings.HasPrefix(filepath.Clean(fullPath)+string(os.PathSeparator), filepath.Clean(localPath)+string(os.PathSeparator)) {
			continue
		}
		data, err := os.ReadFile(fullPath) // #nosec G304 -- path confined to the repo clone root above
		if err != nil {
			continue
		}

		content := string(data)
		if len(content) > 2000 {
			content = textfold.Cut(content, 2000) + "\n... (truncated)"
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
		fmt.Fprintf(&sb, "**%s** by %s at %s\n",
			c.SHA[:8], c.Author, c.Timestamp.Format("2006-01-02 15:04:05"))
		fmt.Fprintf(&sb, "Message: %s\n", c.Message)
		if len(c.FilesChanged) > 0 {
			sb.WriteString("Files changed:\n")
			for _, f := range c.FilesChanged {
				fmt.Fprintf(&sb, "  - %s\n", f)
			}
		}
		sb.WriteString("\n")
	}

	// Recent changes timeline
	if len(ctx.RecentChanges) > 0 {
		sb.WriteString("### Recent Commits (30min before incident)\n")
		for _, c := range ctx.RecentChanges {
			fmt.Fprintf(&sb, "- %s %s: %s (%d files)\n",
				c.SHA[:8], c.Author, c.Message, len(c.FilesChanged))
		}
		sb.WriteString("\n")
	}

	// Code snippets from stack traces
	if len(ctx.RelevantCode) > 0 {
		sb.WriteString("### Code from Stack Trace References\n")
		for _, s := range ctx.RelevantCode {
			fmt.Fprintf(&sb, "**%s** (%s, lines %d-%d)\n", s.FilePath, s.Reason, s.StartLine, s.EndLine)
			sb.WriteString("```" + s.Language + "\n")
			sb.WriteString(s.Content)
			sb.WriteString("```\n\n")
		}
	}

	// Config files
	if len(ctx.ConfigContext) > 0 {
		sb.WriteString("### Deployment Configuration\n")
		for _, cf := range ctx.ConfigContext {
			fmt.Fprintf(&sb, "**%s**\n```\n%s\n```\n\n", cf.FilePath, cf.Content)
		}
	}

	result := sb.String()
	if len(result) > maxSourceContextChars {
		result = textfold.Cut(result, maxSourceContextChars-3) + "..."
	}
	return result
}

// SetupWithManager sets up the controller with the Manager.
func (r *SourceRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.SourceRepository{}).
		Complete(r)
}
