/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/skeema/knownhosts"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// --- readLinesAround ---

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestReadLinesAround_HappyPath(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		sb.WriteString("line ")
		sb.WriteString(string(rune('0' + i%10)))
		sb.WriteString("\n")
	}
	writeTestFile(t, filepath.Join(root, "src", "main.go"), sb.String())

	out, err := readLinesAround(root, filepath.Join(root, "src", "main.go"), 5, 2)
	if err != nil {
		t.Fatalf("readLinesAround: %v", err)
	}
	if !strings.Contains(out, ">>    5 |") {
		t.Errorf("output missing target-line marker, got:\n%s", out)
	}
	if !strings.Contains(out, "   3 |") || !strings.Contains(out, "   7 |") {
		t.Errorf("output missing context lines 3 and 7, got:\n%s", out)
	}
	if strings.Contains(out, "   1 |") || strings.Contains(out, "   9 |") {
		t.Errorf("output includes lines outside the context window, got:\n%s", out)
	}
}

func TestReadLinesAround_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	writeTestFile(t, filepath.Join(base, "secret", "creds.txt"), "top secret\n")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	_, err := readLinesAround(root, filepath.Join(base, "secret", "creds.txt"), 1, 1)
	if err == nil {
		t.Fatal("expected traversal outside the clone root to be rejected")
	}
}

func TestReadLinesAround_RejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	writeTestFile(t, filepath.Join(base, "outside.txt"), "outside\n")
	if err := os.Symlink(filepath.Join(base, "outside.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := readLinesAround(root, filepath.Join(root, "link.txt"), 1, 1)
	if err == nil {
		t.Fatal("expected symlink escaping the clone root to be rejected")
	}
}

func TestReadLinesAround_MissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := readLinesAround(missing, filepath.Join(missing, "f.go"), 1, 1)
	if err == nil {
		t.Fatal("expected error for a missing root directory")
	}
}

func TestReadLinesAround_UnrelatablePaths(t *testing.T) {
	// A relative root and an absolute file path cannot be made relative to
	// each other; readLinesAround must surface that instead of guessing.
	_, err := readLinesAround("relative-root", filepath.Join(t.TempDir(), "f.go"), 1, 1)
	if err == nil {
		t.Fatal("expected error when the file path cannot be made root-relative")
	}
}

// --- parseStackFrame ---

func TestParseStackFrame(t *testing.T) {
	tests := []struct {
		name     string
		frame    string
		language string
		wantPath string
		wantLine int
	}{
		{name: "go frame", frame: "pkg/server/main.go:42", language: "go", wantPath: "pkg/server/main.go", wantLine: 42},
		{name: "go frame with trailing junk", frame: "main.go:42 +0x1b", language: "go", wantPath: "main.go", wantLine: 42},
		{name: "go frame without line", frame: "nocolonhere", language: "go", wantPath: "", wantLine: 0},
		{name: "java frame", frame: "at com.example.MyClass.method(MyClass.java:87)", language: "java", wantPath: "MyClass.java", wantLine: 87},
		{name: "python frame", frame: `File "/app/handlers/views.py", line 7, in handler`, language: "python", wantPath: "/app/handlers/views.py", wantLine: 7},
		{name: "nodejs frame", frame: "at handleRequest (/app/server.js:42:10)", language: "nodejs", wantPath: "/app/server.js", wantLine: 42},
		{name: "unknown language", frame: "main.go:42", language: "rust", wantPath: "", wantLine: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, line := parseStackFrame(tt.frame, tt.language)
			if path != tt.wantPath || line != tt.wantLine {
				t.Errorf("parseStackFrame(%q, %q) = (%q, %d), want (%q, %d)",
					tt.frame, tt.language, path, line, tt.wantPath, tt.wantLine)
			}
		})
	}
}

// --- findFileInRepo ---

func TestFindFileInRepo(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "pkg", "util.go"), "package util\n")
	writeTestFile(t, filepath.Join(root, "src", "app.py"), "print('hi')\n")
	writeTestFile(t, filepath.Join(root, "a", "b", "c", "deep.go"), "package c\n")
	writeTestFile(t, filepath.Join(root, "vendor", "dep", "hidden.go"), "package dep\n")

	t.Run("direct relative match", func(t *testing.T) {
		got := findFileInRepo(root, filepath.Join("pkg", "util.go"), nil)
		if got != filepath.Join(root, "pkg", "util.go") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("match within relevant paths", func(t *testing.T) {
		got := findFileInRepo(root, "app.py", []string{"src"})
		if got != filepath.Join(root, "src", "app.py") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("basename walk fallback", func(t *testing.T) {
		got := findFileInRepo(root, filepath.Join("wrong", "prefix", "deep.go"), nil)
		if got != filepath.Join(root, "a", "b", "c", "deep.go") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("vendor is skipped by the walk", func(t *testing.T) {
		if got := findFileInRepo(root, "hidden.go", nil); got != "" {
			t.Errorf("expected vendor/ to be skipped, got %q", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		if got := findFileInRepo(root, "nope.go", nil); got != "" {
			t.Errorf("expected empty result, got %q", got)
		}
	})
}

// --- readConfigFiles ---

func TestReadConfigFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Dockerfile"), "FROM scratch\n")

	configs := readConfigFiles(root, []string{
		"Dockerfile", // important, readable
		filepath.Join("..", "escape", "Dockerfile"), // important, escapes the root
		filepath.Join("missing", "values.yaml"),     // important, does not exist
		"README.md",                                 // not important
	})

	if len(configs) != 1 {
		t.Fatalf("expected exactly 1 config, got %d: %+v", len(configs), configs)
	}
	if configs[0].FilePath != "Dockerfile" || !strings.Contains(configs[0].Content, "FROM scratch") {
		t.Errorf("unexpected config: %+v", configs[0])
	}
}

// --- detectLanguages ---

func TestDetectLanguages(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeTestFile(t, filepath.Join(root, "util.go"), "package main\n")
	writeTestFile(t, filepath.Join(root, "script.py"), "print('hi')\n")
	writeTestFile(t, filepath.Join(root, "vendor", "dep.go"), "package dep\n")
	writeTestFile(t, filepath.Join(root, ".hidden", "x.go"), "package x\n")

	langs := detectLanguages(root)
	if len(langs) != 2 {
		t.Fatalf("expected 2 languages, got %v", langs)
	}
	// Go has 2 files vs Python's 1 (vendor/ and .hidden/ are skipped),
	// so Go must sort first.
	if langs[0] != "Go" || langs[1] != "Python" {
		t.Errorf("expected [Go Python], got %v", langs)
	}
}

// --- extractRelevantCode ---

func TestExtractRelevantCode(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("package main // filler\n")
	}
	writeTestFile(t, filepath.Join(root, "main.go"), sb.String())

	traces := []StackTrace{
		{
			Language:      "go",
			ExceptionType: "panic",
			Frames:        []string{"main.go:5", "not-a-frame"},
		},
	}

	snippets := extractRelevantCode(root, nil, traces)
	if len(snippets) != 1 {
		t.Fatalf("expected 1 snippet, got %d", len(snippets))
	}
	if snippets[0].FilePath != "main.go" {
		t.Errorf("FilePath = %q, want main.go", snippets[0].FilePath)
	}
	if !strings.Contains(snippets[0].Content, ">>") {
		t.Errorf("snippet content missing target-line marker:\n%s", snippets[0].Content)
	}
}

// --- git-backed helpers ---

// requireGit skips a test that needs a git server. The operator itself
// needs no git binary; the loopback servers below hand the repository to
// git upload-pack / git http-backend, the way a real git host serves it.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{
		"-C", dir,
		"-c", "user.name=test",
		"-c", "user.email=test@example.com",
		"-c", "protocol.file.allow=always",
	}, args...)
	cmd := exec.Command("git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func testSignature() *object.Signature {
	return &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}
}

// newLocalGitRepo creates a repository with one commit on branch main,
// with go-git alone, and returns its path.
func newLocalGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.Main},
	})
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	commitFile(t, repo, dir, "main.go", "package main\n", "feat: initial commit")
	return dir
}

func openRepo(t *testing.T, dir string) *git.Repository {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	return repo
}

// commitFile writes a file and commits it, returning the commit hash.
func commitFile(t *testing.T, repo *git.Repository, dir, name, content, message string) plumbing.Hash {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, name), content)
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(name); err != nil {
		t.Fatalf("git add %s: %v", name, err)
	}
	hash, err := wt.Commit(message, &git.CommitOptions{Author: testSignature(), Committer: testSignature()})
	if err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return hash
}

func newSourceRepo(url, branch string) *platformv1alpha1.SourceRepository {
	return &platformv1alpha1.SourceRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "test-repo", Namespace: "default"},
		Spec: platformv1alpha1.SourceRepositorySpec{
			URL:    url,
			Branch: branch,
		},
	}
}

// newHTTPSGitServer serves a non-bare repository over HTTPS through git
// http-backend. With a user it demands HTTP basic credentials. It returns
// the clone URL and the PEM certificate the client has to trust.
func newHTTPSGitServer(t *testing.T, origin, user, pass string) (string, []byte) {
	t.Helper()
	requireGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("git http-backend through net/http/cgi is not exercised on Windows")
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skipf("git --exec-path: %v", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("git-http-backend not available: %v", err)
	}

	cgiHandler := &cgi.Handler{
		Path: backend,
		Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(origin), "GIT_HTTP_EXPORT_ALL=1", "PATH=" + os.Getenv("PATH")},
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if user != "" {
			if u, p, ok := req.BasicAuth(); !ok || u != user || p != pass {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		cgiHandler.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	// The work tree's .git is the git directory http-backend serves.
	return srv.URL + "/" + filepath.Base(origin) + "/.git", caPEM
}

// newTestKey generates an ed25519 key pair: the signer, and the OpenSSH
// PEM a Secret would carry.
func newTestKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return signer, pem.EncodeToMemory(block)
}

// sshGitServer serves one repository over SSH on the loopback interface
// through git upload-pack, authenticating the client by public key.
type sshGitServer struct {
	url          string
	hostWithPort string
	hostKey      ssh.PublicKey
}

func newSSHGitServer(t *testing.T, repoPath string, clientKey ssh.PublicKey) *sshGitServer {
	t.Helper()
	requireGit(t)
	hostSigner, _ := newTestKey(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &gliderssh.Server{
		Handler: func(s gliderssh.Session) {
			cmd := s.Command()
			if len(cmd) != 2 || cmd[0] != "git-upload-pack" || cmd[1] != "/repo.git" {
				fmt.Fprintf(s.Stderr(), "unsupported command %q\n", s.RawCommand())
				_ = s.Exit(1)
				return
			}
			upload := exec.CommandContext(s.Context(), "git", "upload-pack", repoPath)
			upload.Stdin = s
			upload.Stdout = s
			upload.Stderr = s.Stderr()
			upload.WaitDelay = 5 * time.Second
			if err := upload.Run(); err != nil {
				_ = s.Exit(1)
				return
			}
			_ = s.Exit(0)
		},
		PublicKeyHandler: func(_ gliderssh.Context, key gliderssh.PublicKey) bool {
			return gliderssh.KeysEqual(key, clientKey)
		},
	}
	srv.AddHostKey(hostSigner)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	port := ln.Addr().(*net.TCPAddr).Port
	return &sshGitServer{
		url:          fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", port),
		hostWithPort: fmt.Sprintf("127.0.0.1:%d", port),
		hostKey:      hostSigner.PublicKey(),
	}
}

// knownHostsLine is what ssh-keyscan prints for the server.
func (s *sshGitServer) knownHostsLine(key ssh.PublicKey) []byte {
	return []byte(knownhosts.Line([]string{s.hostWithPort}, key) + "\n")
}

func sshSourceRepo(url string, policy platformv1alpha1.SSHHostKeyPolicy) *platformv1alpha1.SourceRepository {
	repo := newSourceRepo(url, "main")
	repo.Spec.AuthType = platformv1alpha1.SourceRepoAuthSSH
	repo.Spec.SecretRef = "git-creds"
	repo.Spec.SSHHostKeyPolicy = policy
	return repo
}

func sshSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"}, Data: data}
}

func newReconcilerWithSecret(t *testing.T, secret *corev1.Secret) *SourceRepositoryReconciler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(secret).Build()
	return &SourceRepositoryReconciler{Client: c, BaseDir: t.TempDir()}
}

func TestGetRecentCommits(t *testing.T) {
	origin := newLocalGitRepo(t)
	repo := openRepo(t, origin)
	second := commitFile(t, repo, origin, "util.go", "package main\n", "fix: add util\n\nLonger body.")
	r := &SourceRepositoryReconciler{}

	commits, err := r.getRecentCommits(context.Background(), origin)
	if err != nil {
		t.Fatalf("getRecentCommits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	newest, oldest := commits[0], commits[1]
	if newest.SHA != second.String() {
		t.Errorf("newest commit = %s, want %s", newest.SHA, second)
	}
	if newest.Message != "fix: add util" {
		t.Errorf("subject = %q, want the first line only", newest.Message)
	}
	if len(newest.FilesChanged) != 1 || newest.FilesChanged[0] != "util.go" {
		t.Errorf("files changed = %v, want [util.go]", newest.FilesChanged)
	}
	if len(oldest.SHA) != 40 || oldest.Message != "feat: initial commit" || oldest.Author != "test" {
		t.Errorf("unexpected root commit metadata: %+v", oldest)
	}
	if oldest.Timestamp.IsZero() {
		t.Error("commit timestamp was not set")
	}
	if len(oldest.FilesChanged) != 1 || oldest.FilesChanged[0] != "main.go" {
		t.Errorf("root commit files = %v, want [main.go]", oldest.FilesChanged)
	}
}

func TestGetRecentCommits_NotARepository(t *testing.T) {
	r := &SourceRepositoryReconciler{}
	if _, err := r.getRecentCommits(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "git log") {
		t.Fatalf("expected a git log error, got %v", err)
	}
}

func TestPullRepo(t *testing.T) {
	r := &SourceRepositoryReconciler{}
	ctx := context.Background()

	t.Run("rejects invalid url before touching the clone", func(t *testing.T) {
		err := r.pullRepo(ctx, newSourceRepo("file:///etc", ""), t.TempDir(), nil)
		if err == nil || !strings.Contains(err.Error(), "repository URL must use") {
			t.Fatalf("expected URL validation error, got %v", err)
		}
	})

	t.Run("fetch failure on a non-repo directory", func(t *testing.T) {
		err := r.pullRepo(ctx, newSourceRepo("https://example.com/repo.git", "main"), t.TempDir(), nil)
		if err == nil || !strings.Contains(err.Error(), "git fetch") {
			t.Fatalf("expected git fetch error, got %v", err)
		}
	})

	t.Run("fetches and resets from origin", func(t *testing.T) {
		origin := newLocalGitRepo(t)
		url, ca := newHTTPSGitServer(t, origin, "", "")
		work := t.TempDir()
		workRepo, err := git.PlainInit(work, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := workRepo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
			t.Fatal(err)
		}

		// Branch defaulting: empty Spec.Branch must resolve to main.
		if err := r.pullRepo(ctx, newSourceRepo(url, ""), work, &gitAuth{caBundle: ca}); err != nil {
			t.Fatalf("pullRepo: %v", err)
		}
		if _, err := os.Stat(filepath.Join(work, "main.go")); err != nil {
			t.Errorf("expected main.go after reset --hard origin/main: %v", err)
		}
	})

	t.Run("shallow clone follows new commits", func(t *testing.T) {
		origin := newLocalGitRepo(t)
		url, ca := newHTTPSGitServer(t, origin, "", "")
		auth := &gitAuth{caBundle: ca}
		localPath := filepath.Join(t.TempDir(), "clone")
		repo := newSourceRepo(url, "main")
		if err := r.cloneRepo(ctx, repo, localPath, auth); err != nil {
			t.Fatalf("cloneRepo: %v", err)
		}
		// Up to date: a fetch with nothing new is not an error.
		if err := r.pullRepo(ctx, repo, localPath, auth); err != nil {
			t.Fatalf("pullRepo (up to date): %v", err)
		}

		originRepo := openRepo(t, origin)
		var newest plumbing.Hash
		for i := 0; i < 3; i++ {
			newest = commitFile(t, originRepo, origin, fmt.Sprintf("f%d.go", i), "package main\n", fmt.Sprintf("feat: change %d", i))
		}
		if err := r.pullRepo(ctx, repo, localPath, auth); err != nil {
			t.Fatalf("pullRepo (new commits): %v", err)
		}
		if _, err := os.Stat(filepath.Join(localPath, "f2.go")); err != nil {
			t.Errorf("work tree not reset to the new head: %v", err)
		}
		commits, err := r.getRecentCommits(ctx, localPath)
		if err != nil {
			t.Fatalf("getRecentCommits: %v", err)
		}
		if len(commits) != 4 || commits[0].SHA != newest.String() {
			t.Fatalf("expected 4 commits headed by %s, got %+v", newest, commits)
		}
	})
}

func TestCloneRepo(t *testing.T) {
	r := &SourceRepositoryReconciler{}
	ctx := context.Background()

	t.Run("rejects invalid url before cloning", func(t *testing.T) {
		err := r.cloneRepo(ctx, newSourceRepo("-oProxyCommand=evil", "main"), filepath.Join(t.TempDir(), "clone"), nil)
		if err == nil || !strings.Contains(err.Error(), "unsupported repository URL") {
			t.Fatalf("expected URL validation error, got %v", err)
		}
	})

	t.Run("surfaces parent dir creation failure", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		writeTestFile(t, blocker, "not a directory\n")
		err := r.cloneRepo(ctx, newSourceRepo("https://example.com/repo.git", "main"),
			filepath.Join(blocker, "sub", "clone"), nil)
		if err == nil || !strings.Contains(err.Error(), "creating clone parent dir") {
			t.Fatalf("expected mkdir error, got %v", err)
		}
	})

	t.Run("surfaces clone failure and leaves no partial clone", func(t *testing.T) {
		cloneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// Port 1 is reserved and never listening: the clone fails fast
		// with a connection error instead of touching the network.
		localPath := filepath.Join(t.TempDir(), "clone")
		err := r.cloneRepo(cloneCtx, newSourceRepo("https://127.0.0.1:1/repo.git", "main"), localPath, nil)
		if err == nil || !strings.Contains(err.Error(), "git clone failed") {
			t.Fatalf("expected git clone error, got %v", err)
		}
		if _, statErr := os.Stat(localPath); !os.IsNotExist(statErr) {
			t.Errorf("a failed clone must leave no directory behind: %v", statErr)
		}
	})
}

// --- resolveAuth ---

func publicKeysOf(t *testing.T, auth *gitAuth) *gitssh.PublicKeys {
	t.Helper()
	keys, ok := auth.method.(*gitssh.PublicKeys)
	if !ok {
		t.Fatalf("auth method = %T, want *ssh.PublicKeys", auth.method)
	}
	return keys
}

func fakeAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22} }

// With known_hosts in the Secret the host key is checked strictly against
// it, the private key stays in memory, and the host key file does not
// outlive the sync.
func TestResolveAuth_SSHKnownHostsIsStrict(t *testing.T) {
	_, clientPEM := newTestKey(t)
	hostSigner, _ := newTestKey(t)
	otherSigner, _ := newTestKey(t)
	r := newReconcilerWithSecret(t, sshSecret(map[string][]byte{
		"ssh-key":     clientPEM,
		"known_hosts": []byte(knownhosts.Line([]string{"github.com"}, hostSigner.PublicKey())),
	}))

	auth, err := r.resolveAuth(context.Background(), sshSourceRepo("git@github.com:org/repo.git", ""))
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	keys := publicKeysOf(t, auth)
	if keys.User != "git" {
		t.Errorf("ssh user = %q, want git (from the URL)", keys.User)
	}
	if keys.HostKeyCallback == nil {
		t.Fatal("no host key callback: the connection would trust any key")
	}
	if err := keys.HostKeyCallback("github.com:22", fakeAddr(), hostSigner.PublicKey()); err != nil {
		t.Errorf("the listed host key must be accepted: %v", err)
	}
	if err := keys.HostKeyCallback("github.com:22", fakeAddr(), otherSigner.PublicKey()); err == nil {
		t.Error("a host key not in known_hosts must be rejected")
	}
	if err := keys.HostKeyCallback("gitlab.com:22", fakeAddr(), hostSigner.PublicKey()); err == nil {
		t.Error("a host not in known_hosts must be rejected")
	}
	if !slices.Contains(keys.HostKeyAlgorithms, ssh.KeyAlgoED25519) || slices.Contains(keys.HostKeyAlgorithms, ssh.KeyAlgoRSA) {
		t.Errorf("host key algorithms = %v, want only the listed key's type", keys.HostKeyAlgorithms)
	}

	khFile := filepath.Join(r.sshDir(), "default_test-repo.known_hosts")
	if data, err := os.ReadFile(khFile); err != nil || !strings.Contains(string(data), "ssh-ed25519") {
		t.Errorf("known_hosts file: %v / %q", err, data)
	}
	entries, _ := os.ReadDir(r.sshDir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".key") {
			t.Errorf("private key written to disk: %s", e.Name())
		}
	}

	auth.done()
	if _, err := os.Stat(khFile); !os.IsNotExist(err) {
		t.Errorf("%s left behind after the sync: %v", khFile, err)
	}
}

// Without known_hosts the default policy refuses to connect instead of
// trusting whatever key answers.
func TestResolveAuth_SSHWithoutKnownHostsIsRefused(t *testing.T) {
	_, clientPEM := newTestKey(t)
	r := newReconcilerWithSecret(t, sshSecret(map[string][]byte{"ssh-key": clientPEM}))

	for _, policy := range []platformv1alpha1.SSHHostKeyPolicy{"", platformv1alpha1.SSHHostKeyStrict} {
		_, err := r.resolveAuth(context.Background(), sshSourceRepo("git@github.com:org/repo.git", policy))
		if err == nil || !strings.Contains(err.Error(), "known_hosts") || !strings.Contains(err.Error(), "acceptNew") {
			t.Fatalf("policy %q: expected a known_hosts error naming the opt-in, got %v", policy, err)
		}
		if entries, _ := os.ReadDir(r.sshDir()); len(entries) != 0 {
			t.Errorf("policy %q: files left behind after a refused sync: %v", policy, entries)
		}
	}
}

// acceptNew is trust on first use: the first key is recorded in a file
// that outlives the sync so a changed key is rejected later.
func TestResolveAuth_SSHAcceptNewOptIn(t *testing.T) {
	_, clientPEM := newTestKey(t)
	hostSigner, _ := newTestKey(t)
	otherSigner, _ := newTestKey(t)
	r := newReconcilerWithSecret(t, sshSecret(map[string][]byte{"ssh-key": clientPEM}))

	auth, err := r.resolveAuth(context.Background(), sshSourceRepo("git@github.com:org/repo.git", platformv1alpha1.SSHHostKeyAcceptNew))
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	defer auth.done()
	keys := publicKeysOf(t, auth)
	tofu := filepath.Join(r.sshDir(), "default_test-repo.tofu_known_hosts")
	if slices.Contains(auth.cleanup, tofu) {
		t.Fatal("the trust-on-first-use record must survive the sync")
	}
	if err := keys.HostKeyCallback("github.com:22", fakeAddr(), hostSigner.PublicKey()); err != nil {
		t.Fatalf("first contact must be accepted: %v", err)
	}
	// Recorded like OpenSSH does: the host name and the address answered.
	data, err := os.ReadFile(tofu)
	if err != nil || !strings.Contains(string(data), knownhosts.Line([]string{"github.com", "127.0.0.1"}, hostSigner.PublicKey())) {
		t.Fatalf("first key not recorded: %v / %q", err, data)
	}
	if err := keys.HostKeyCallback("github.com:22", fakeAddr(), hostSigner.PublicKey()); err != nil {
		t.Errorf("the recorded key must stay accepted: %v", err)
	}
	if err := keys.HostKeyCallback("github.com:22", fakeAddr(), otherSigner.PublicKey()); err == nil {
		t.Error("a changed host key must be rejected after first use")
	}

	// A later sync reads the record back and now pins the key type.
	later, err := r.resolveAuth(context.Background(), sshSourceRepo("git@github.com:org/repo.git", platformv1alpha1.SSHHostKeyAcceptNew))
	if err != nil {
		t.Fatalf("second resolveAuth: %v", err)
	}
	defer later.done()
	if algos := publicKeysOf(t, later).HostKeyAlgorithms; !slices.Contains(algos, ssh.KeyAlgoED25519) {
		t.Errorf("host key algorithms after first use = %v, want the recorded type", algos)
	}
}

// A Secret whose ssh-key is not a private key fails before any connection,
// and the error does not echo the material.
func TestResolveAuth_SSHRejectsUnusableKey(t *testing.T) {
	material := "fake-key-material"
	r := newReconcilerWithSecret(t, sshSecret(map[string][]byte{
		"ssh-key":     []byte(material),
		"known_hosts": []byte("github.com ssh-ed25519 AAAAfixture"),
	}))
	_, err := r.resolveAuth(context.Background(), sshSourceRepo("git@github.com:org/repo.git", ""))
	if err == nil || !strings.Contains(err.Error(), "not a usable private key") || strings.Contains(err.Error(), material) {
		t.Fatalf("expected a key format error without the material, got %v", err)
	}
}

// Basic and token credentials are sent as HTTP basic auth from memory.
func TestResolveAuth_HTTPSBasicAuth(t *testing.T) {
	password := strings.Join([]string{"pw", "fixture", "value"}, "-")
	r := newReconcilerWithSecret(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("ci-user"), "password": []byte(password), "token": []byte(password)},
	})

	for authType, wantUser := range map[platformv1alpha1.SourceRepositoryAuthType]string{
		platformv1alpha1.SourceRepoAuthBasic: "ci-user",
		platformv1alpha1.SourceRepoAuthToken: tokenAuthUsername,
	} {
		repo := newSourceRepo("https://example.invalid/org/repo.git", "main")
		repo.Spec.AuthType = authType
		repo.Spec.SecretRef = "git-creds"
		auth, err := r.resolveAuth(context.Background(), repo)
		if err != nil {
			t.Fatalf("%s: resolveAuth: %v", authType, err)
		}
		basic, ok := auth.method.(*githttp.BasicAuth)
		if !ok || basic.Username != wantUser || basic.Password != password {
			t.Fatalf("%s: auth = %#v, want basic auth for %s", authType, auth.method, wantUser)
		}
		if len(auth.cleanup) != 0 {
			t.Errorf("%s: HTTPS credentials must not touch the disk: %v", authType, auth.cleanup)
		}
	}
}

// ca.crt in the Secret is honored for every authType, including none.
func TestResolveAuth_CABundleForAnyAuthType(t *testing.T) {
	ca := []byte("-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n")
	r := newReconcilerWithSecret(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"},
		Data:       map[string][]byte{"ca.crt": ca, "token": []byte("t")},
	})
	for _, authType := range []platformv1alpha1.SourceRepositoryAuthType{"", platformv1alpha1.SourceRepoAuthNone, platformv1alpha1.SourceRepoAuthToken} {
		repo := newSourceRepo("https://git.example.invalid/org/repo.git", "main")
		repo.Spec.AuthType = authType
		repo.Spec.SecretRef = "git-creds"
		auth, err := r.resolveAuth(context.Background(), repo)
		if err != nil {
			t.Fatalf("%q: resolveAuth: %v", authType, err)
		}
		if string(auth.ca()) != string(ca) {
			t.Errorf("%q: CA bundle not taken from the Secret", authType)
		}
		if (authType == platformv1alpha1.SourceRepoAuthToken) != (auth.method != nil) {
			t.Errorf("%q: auth method = %v", authType, auth.method)
		}
	}

	// Without a secretRef nothing is read.
	auth, err := r.resolveAuth(context.Background(), newSourceRepo("https://git.example.invalid/org/repo.git", "main"))
	if err != nil || auth.ca() != nil || auth.method != nil {
		t.Fatalf("no secretRef: auth = %+v, err = %v", auth, err)
	}
}

// --- credentials never persist in the clone ---

func TestCloneAndPull_HTTPSCredentialsNeverPersist(t *testing.T) {
	password := strings.Join([]string{"pw", "fixture", "value"}, "-")
	for _, tc := range []struct {
		authType platformv1alpha1.SourceRepositoryAuthType
		user     string
		data     map[string][]byte
	}{
		{platformv1alpha1.SourceRepoAuthBasic, "ci-user", map[string][]byte{"username": []byte("ci-user"), "password": []byte(password)}},
		{platformv1alpha1.SourceRepoAuthToken, tokenAuthUsername, map[string][]byte{"token": []byte(password)}},
	} {
		t.Run(string(tc.authType), func(t *testing.T) {
			repoURL, ca := newHTTPSGitServer(t, newLocalGitRepo(t), tc.user, password)
			tc.data["ca.crt"] = ca
			r := newReconcilerWithSecret(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"}, Data: tc.data})
			repo := newSourceRepo(repoURL, "main")
			repo.Spec.AuthType = tc.authType
			repo.Spec.SecretRef = "git-creds"
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()

			localPath := filepath.Join(r.BaseDir, "default", "test-repo")
			if err := r.cloneRepo(ctx, repo, localPath, &gitAuth{caBundle: ca}); err == nil {
				t.Fatal("clone without credentials must fail against the protected server")
			}

			auth, err := r.resolveAuth(ctx, repo)
			if err != nil {
				t.Fatalf("resolveAuth: %v", err)
			}
			defer auth.done()
			if err := r.cloneRepo(ctx, repo, localPath, auth); err != nil {
				t.Fatalf("cloneRepo: %v", err)
			}
			if err := r.pullRepo(ctx, repo, localPath, auth); err != nil {
				t.Fatalf("pullRepo: %v", err)
			}
			assertNoSecretInGitDir(t, localPath, password)
		})
	}
}

// assertNoSecretInGitDir reads every file of the clone's .git directory.
func assertNoSecretInGitDir(t *testing.T, localPath, secret string) {
	t.Helper()
	err := filepath.Walk(filepath.Join(localPath, ".git"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("credential persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Against a real SSH server: the Secret's known_hosts decides whether the
// clone proceeds, acceptNew records the first key, and a recorded key
// that differs refuses the server.
func TestCloneAndPull_SSHHostKeyPolicy(t *testing.T) {
	clientSigner, clientPEM := newTestKey(t)
	origin := newLocalGitRepo(t)
	server := newSSHGitServer(t, origin, clientSigner.PublicKey())
	otherSigner, _ := newTestKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	t.Run("known_hosts with the server key clones and pulls", func(t *testing.T) {
		r := newReconcilerWithSecret(t, sshSecret(map[string][]byte{
			"ssh-key": clientPEM, "known_hosts": server.knownHostsLine(server.hostKey),
		}))
		repo := sshSourceRepo(server.url, "")
		auth, err := r.resolveAuth(ctx, repo)
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		defer auth.done()
		localPath := filepath.Join(r.BaseDir, "default", repo.Name)
		if err := r.cloneRepo(ctx, repo, localPath, auth); err != nil {
			t.Fatalf("cloneRepo over ssh: %v", err)
		}
		newest := commitFile(t, openRepo(t, origin), origin, "after.go", "package main\n", "feat: after clone")
		if err := r.pullRepo(ctx, repo, localPath, auth); err != nil {
			t.Fatalf("pullRepo over ssh: %v", err)
		}
		commits, err := r.getRecentCommits(ctx, localPath)
		if err != nil || len(commits) == 0 || commits[0].SHA != newest.String() {
			t.Fatalf("expected the pushed commit at the head, got %+v (err %v)", commits, err)
		}
		assertNoSecretInGitDir(t, localPath, "PRIVATE KEY")
	})

	t.Run("known_hosts with another key refuses the server", func(t *testing.T) {
		r := newReconcilerWithSecret(t, sshSecret(map[string][]byte{
			"ssh-key": clientPEM, "known_hosts": server.knownHostsLine(otherSigner.PublicKey()),
		}))
		repo := sshSourceRepo(server.url, "")
		auth, err := r.resolveAuth(ctx, repo)
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		defer auth.done()
		localPath := filepath.Join(r.BaseDir, "default", repo.Name)
		err = r.cloneRepo(ctx, repo, localPath, auth)
		if err == nil || !strings.Contains(err.Error(), "git clone failed") {
			t.Fatalf("a host key mismatch must refuse the clone, got %v", err)
		}
		if _, statErr := os.Stat(localPath); !os.IsNotExist(statErr) {
			t.Errorf("refused clone left a directory: %v", statErr)
		}
	})

	t.Run("acceptNew records the key on first contact", func(t *testing.T) {
		r := newReconcilerWithSecret(t, sshSecret(map[string][]byte{"ssh-key": clientPEM}))
		repo := sshSourceRepo(server.url, platformv1alpha1.SSHHostKeyAcceptNew)
		auth, err := r.resolveAuth(ctx, repo)
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		defer auth.done()
		localPath := filepath.Join(r.BaseDir, "default", repo.Name)
		if err := r.cloneRepo(ctx, repo, localPath, auth); err != nil {
			t.Fatalf("first-contact clone: %v", err)
		}
		tofu := filepath.Join(r.sshDir(), "default_"+repo.Name+".tofu_known_hosts")
		data, err := os.ReadFile(tofu)
		if err != nil || !strings.Contains(string(data), string(server.knownHostsLine(server.hostKey))) {
			t.Fatalf("host key not recorded: %v / %q", err, data)
		}
		if err := r.pullRepo(ctx, repo, localPath, auth); err != nil {
			t.Fatalf("pull after first contact: %v", err)
		}
	})

	t.Run("acceptNew refuses a server whose key changed", func(t *testing.T) {
		r := newReconcilerWithSecret(t, sshSecret(map[string][]byte{"ssh-key": clientPEM}))
		repo := sshSourceRepo(server.url, platformv1alpha1.SSHHostKeyAcceptNew)
		// The record of an earlier sync names a different key for this host.
		writeTestFile(t, filepath.Join(r.sshDir(), "default_"+repo.Name+".tofu_known_hosts"), string(server.knownHostsLine(otherSigner.PublicKey())))
		auth, err := r.resolveAuth(ctx, repo)
		if err != nil {
			t.Fatalf("resolveAuth: %v", err)
		}
		defer auth.done()
		err = r.cloneRepo(ctx, repo, filepath.Join(r.BaseDir, "default", repo.Name), auth)
		if err == nil || !strings.Contains(err.Error(), "git clone failed") {
			t.Fatalf("a changed host key must refuse the clone, got %v", err)
		}
	})
}

// A clone made by an earlier version carries the token in its origin URL
// and a reflog that quoted it; the next sync rewrites the URL and drops
// the reflog.
func TestScrubOriginURL_RemovesEmbeddedCredential(t *testing.T) {
	token := strings.Join([]string{"legacy", "fixture", "value"}, "-")
	work := newLocalGitRepo(t)
	repo := openRepo(t, work)
	withCreds := "https://" + tokenAuthUsername + ":" + token + "@example.invalid/org/repo.git"
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{withCreds}}); err != nil {
		t.Fatal(err)
	}
	reflog := filepath.Join(work, ".git", "logs", "HEAD")
	writeTestFile(t, reflog, "0 1 test <test@example.com> 0 +0000\tfetch: from "+withCreds+"\n")

	if err := scrubOriginURL(repo, work); err != nil {
		t.Fatalf("scrubOriginURL: %v", err)
	}
	remote, err := repo.Remote("origin")
	if err != nil || len(remote.Config().URLs) != 1 || remote.Config().URLs[0] != "https://example.invalid/org/repo.git" {
		t.Fatalf("origin = %+v (err %v)", remote, err)
	}
	cfg, err := os.ReadFile(filepath.Join(work, ".git", "config"))
	if err != nil || strings.Contains(string(cfg), token) {
		t.Fatalf(".git/config still holds the token: %v", err)
	}
	if _, err := os.Stat(reflog); !os.IsNotExist(err) {
		t.Fatal("reflog that quoted the token was kept")
	}

	// A clean URL is left alone.
	if err := scrubOriginURL(repo, work); err != nil {
		t.Fatalf("second scrub: %v", err)
	}
}

func TestRedactURLCredentials(t *testing.T) {
	in := "fatal: unable to access 'https://" + "user:" + "hidden" + "@example.invalid/r.git/'"
	if got := redactURLCredentials(in); strings.Contains(got, "hidden") || !strings.Contains(got, "https://***@example.invalid") {
		t.Fatalf("redactURLCredentials = %q", got)
	}
}

// --- Reconcile cleanup path ---

func TestSourceRepositoryReconcile_NotFoundRemovesClone(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	r := &SourceRepositoryReconciler{Client: c, Scheme: newScheme()}

	ns, name := "srcrepo-test-ns", "deleted-repo"
	localPath := filepath.Join(sourceRepoBaseDir, ns, name)
	writeTestFile(t, filepath.Join(localPath, "main.go"), "package main\n")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(sourceRepoBaseDir, ns)) })

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: ns, Name: name},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("expected no requeue for a deleted CR, got %+v", res)
	}
	if _, statErr := os.Stat(localPath); !os.IsNotExist(statErr) {
		t.Errorf("expected local clone to be removed, stat err: %v", statErr)
	}
}

func TestSourceRepositoryReconcile_NotFoundCleanupFailureIsNonFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not block removal on Windows")
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	r := &SourceRepositoryReconciler{Client: c, Scheme: newScheme()}

	ns, name := "srcrepo-test-ns-ro", "stuck-repo"
	localPath := filepath.Join(sourceRepoBaseDir, ns, name)
	writeTestFile(t, filepath.Join(localPath, "main.go"), "package main\n")
	// Removing the write bit makes RemoveAll fail on the child entry
	// (when not running as root); the reconciler must log and move on.
	if err := os.Chmod(localPath, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(localPath, 0o700)
		_ = os.RemoveAll(filepath.Join(sourceRepoBaseDir, ns))
	})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: ns, Name: name},
	})
	if err != nil {
		t.Fatalf("Reconcile must not fail when clone cleanup fails, got: %v", err)
	}
}
