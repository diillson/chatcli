/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

// newLocalGitRepo creates a repo with one commit on branch main and
// returns its path.
func newLocalGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "-c", "init.defaultBranch=main", "init")
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feat: initial commit")
	return dir
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

func TestGetRecentCommits(t *testing.T) {
	origin := newLocalGitRepo(t)
	r := &SourceRepositoryReconciler{}

	commits, err := r.getRecentCommits(context.Background(), origin)
	if err != nil {
		t.Fatalf("getRecentCommits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	c := commits[0]
	if len(c.SHA) != 40 {
		t.Errorf("SHA length = %d, want 40", len(c.SHA))
	}
	if c.Message != "feat: initial commit" || c.Author != "test" {
		t.Errorf("unexpected commit metadata: %+v", c)
	}
	if c.Timestamp.IsZero() {
		t.Error("commit timestamp was not parsed")
	}
}

func TestPullRepo(t *testing.T) {
	r := &SourceRepositoryReconciler{}
	ctx := context.Background()

	t.Run("rejects invalid url before touching git", func(t *testing.T) {
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
		work := t.TempDir()
		runGit(t, work, "-c", "init.defaultBranch=main", "init")
		// file:// (instead of a bare path) forces the smart transport,
		// which supports the shallow fetch pullRepo performs.
		runGit(t, work, "remote", "add", "origin", "file://"+origin)

		// Branch defaulting: empty Spec.Branch must resolve to main.
		repo := newSourceRepo("https://example.com/repo.git", "")
		if err := r.pullRepo(ctx, repo, work, nil); err != nil {
			t.Fatalf("pullRepo: %v", err)
		}
		if _, err := os.Stat(filepath.Join(work, "main.go")); err != nil {
			t.Errorf("expected main.go after reset --hard origin/main: %v", err)
		}
	})
}

func TestCloneRepo(t *testing.T) {
	r := &SourceRepositoryReconciler{}
	ctx := context.Background()

	t.Run("rejects invalid url before running git", func(t *testing.T) {
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

	t.Run("surfaces git clone failure", func(t *testing.T) {
		cloneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// Port 1 is reserved and never listening: the clone fails fast
		// with a connection error instead of touching the network.
		err := r.cloneRepo(cloneCtx, newSourceRepo("https://127.0.0.1:1/repo.git", "main"),
			filepath.Join(t.TempDir(), "clone"), nil)
		if err == nil || !strings.Contains(err.Error(), "git clone failed") {
			t.Fatalf("expected git clone error, got %v", err)
		}
	})
}

// --- resolveAuth ---

func sshSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"}, Data: data}
}

func sshRepo(policy platformv1alpha1.SSHHostKeyPolicy) *platformv1alpha1.SourceRepository {
	repo := newSourceRepo("git@github.com:org/repo.git", "main")
	repo.Name = "ssh-auth-test-repo"
	repo.Spec.AuthType = platformv1alpha1.SourceRepoAuthSSH
	repo.Spec.SecretRef = "git-creds"
	repo.Spec.SSHHostKeyPolicy = policy
	return repo
}

func envValue(env []string, key string) string {
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return strings.TrimPrefix(e, key+"=")
		}
	}
	return ""
}

// With known_hosts in the Secret the host key is checked strictly against
// it, and the key material does not outlive the sync.
func TestResolveAuth_SSHKnownHostsIsStrict(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sshSecret(map[string][]byte{
		"ssh-key":     []byte("fake-key-material"),
		"known_hosts": []byte("github.com ssh-ed25519 AAAAfixture"),
	})).Build()
	r := &SourceRepositoryReconciler{Client: c, BaseDir: t.TempDir()}

	auth, err := r.resolveAuth(context.Background(), sshRepo(""))
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	sshCmd := envValue(auth.env, "GIT_SSH_COMMAND")
	keyFile := filepath.Join(r.sshDir(), "default_ssh-auth-test-repo.key")
	khFile := filepath.Join(r.sshDir(), "default_ssh-auth-test-repo.known_hosts")
	for _, want := range []string{"-i " + keyFile, "StrictHostKeyChecking=yes", "UserKnownHostsFile=" + khFile, "BatchMode=yes"} {
		if !strings.Contains(sshCmd, want) {
			t.Errorf("GIT_SSH_COMMAND %q lacks %q", sshCmd, want)
		}
	}
	if strings.Contains(sshCmd, "StrictHostKeyChecking=no") || strings.Contains(sshCmd, "accept-new") {
		t.Errorf("host key checking weakened: %q", sshCmd)
	}
	if data, err := os.ReadFile(keyFile); err != nil || string(data) != "fake-key-material\n" {
		t.Errorf("key file: %v / %q", err, data)
	}
	if data, err := os.ReadFile(khFile); err != nil || !strings.Contains(string(data), "AAAAfixture") {
		t.Errorf("known_hosts file: %v / %q", err, data)
	}

	auth.done()
	for _, f := range []string{keyFile, khFile} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s left behind after the sync: %v", f, err)
		}
	}
}

// Without known_hosts the default policy refuses to connect instead of
// trusting whatever key answers.
func TestResolveAuth_SSHWithoutKnownHostsIsRefused(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sshSecret(map[string][]byte{
		"ssh-key": []byte("fake-key-material"),
	})).Build()
	r := &SourceRepositoryReconciler{Client: c, BaseDir: t.TempDir()}

	for _, policy := range []platformv1alpha1.SSHHostKeyPolicy{"", platformv1alpha1.SSHHostKeyStrict} {
		_, err := r.resolveAuth(context.Background(), sshRepo(policy))
		if err == nil || !strings.Contains(err.Error(), "known_hosts") || !strings.Contains(err.Error(), "acceptNew") {
			t.Fatalf("policy %q: expected a known_hosts error naming the opt-in, got %v", policy, err)
		}
		if _, statErr := os.Stat(filepath.Join(r.sshDir(), "default_ssh-auth-test-repo.key")); !os.IsNotExist(statErr) {
			t.Errorf("policy %q: key file left behind after a refused sync", policy)
		}
	}
}

// acceptNew is trust on first use: the first key is recorded in a file
// that outlives the sync so a changed key is rejected later.
func TestResolveAuth_SSHAcceptNewOptIn(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(sshSecret(map[string][]byte{
		"ssh-key": []byte("fake-key-material"),
	})).Build()
	r := &SourceRepositoryReconciler{Client: c, BaseDir: t.TempDir()}

	auth, err := r.resolveAuth(context.Background(), sshRepo(platformv1alpha1.SSHHostKeyAcceptNew))
	if err != nil {
		t.Fatalf("resolveAuth: %v", err)
	}
	defer auth.done()
	sshCmd := envValue(auth.env, "GIT_SSH_COMMAND")
	tofu := filepath.Join(r.sshDir(), "default_ssh-auth-test-repo.tofu_known_hosts")
	if !strings.Contains(sshCmd, "StrictHostKeyChecking=accept-new") || !strings.Contains(sshCmd, "UserKnownHostsFile="+tofu) {
		t.Fatalf("GIT_SSH_COMMAND = %q", sshCmd)
	}
	for _, f := range auth.cleanup {
		if f == tofu {
			t.Fatal("the trust-on-first-use record must survive the sync")
		}
	}
}

// Basic and token credentials go to git through the askpass helper, from
// the environment of the git process only.
func TestResolveAuth_HTTPSUsesAskpass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the askpass helper is a POSIX shell script")
	}
	password := strings.Join([]string{"pw", "fixture", "value"}, "-")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("ci-user"), "password": []byte(password), "token": []byte(password)},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(secret).Build()
	r := &SourceRepositoryReconciler{Client: c, BaseDir: t.TempDir()}

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
		helper := envValue(auth.env, "GIT_ASKPASS")
		if helper == "" || envValue(auth.env, "GIT_TERMINAL_PROMPT") != "0" {
			t.Fatalf("%s: env = %v", authType, auth.env)
		}
		script, err := os.ReadFile(helper)
		if err != nil || strings.Contains(string(script), password) {
			t.Fatalf("%s: helper must hold no secret: %v", authType, err)
		}
		for prompt, want := range map[string]string{
			"Username for 'https://example.invalid': ":         wantUser,
			"Password for 'https://ci-user@example.invalid': ": password,
		} {
			cmd := exec.Command(helper, prompt)
			cmd.Env = append(os.Environ(), auth.env...)
			out, err := cmd.Output()
			if err != nil || strings.TrimSpace(string(out)) != want {
				t.Errorf("%s: helper answered %q to %q (err %v)", authType, out, prompt, err)
			}
		}
	}
}

// --- credentials never persist in the clone ---

// newAuthGitServer serves a repository over HTTPS through git
// http-backend and demands HTTP basic credentials.
func newAuthGitServer(t *testing.T, user, pass string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the askpass helper is a POSIX shell script")
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skipf("git --exec-path: %v", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("git-http-backend not available: %v", err)
	}
	projectRoot := t.TempDir()
	runGit(t, projectRoot, "clone", "--bare", newLocalGitRepo(t), "repo.git")

	cgiHandler := &cgi.Handler{
		Path: backend,
		Env:  []string{"GIT_PROJECT_ROOT=" + projectRoot, "GIT_HTTP_EXPORT_ALL=1", "PATH=" + os.Getenv("PATH")},
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if u, p, ok := req.BasicAuth(); !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cgiHandler.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", caFile)
	return srv.URL + "/repo.git"
}

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
			repoURL := newAuthGitServer(t, tc.user, password)
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"}, Data: tc.data}
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(secret).Build()
			r := &SourceRepositoryReconciler{Client: c, BaseDir: t.TempDir()}
			repo := newSourceRepo(repoURL, "main")
			repo.Spec.AuthType = tc.authType
			repo.Spec.SecretRef = "git-creds"
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()

			localPath := filepath.Join(r.BaseDir, "default", "test-repo")
			if err := r.cloneRepo(ctx, repo, localPath, &gitAuth{env: []string{"GIT_TERMINAL_PROMPT=0"}}); err == nil {
				t.Fatal("clone without credentials must fail against the protected server")
			}
			_ = os.RemoveAll(localPath)

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

// A clone made by an earlier version carries the token in its origin URL;
// the next sync rewrites it and drops the reflog that quoted it.
func TestScrubOriginURL_RemovesEmbeddedCredential(t *testing.T) {
	token := strings.Join([]string{"legacy", "fixture", "value"}, "-")
	work := newLocalGitRepo(t)
	withCreds := "https://" + tokenAuthUsername + ":" + token + "@example.invalid/org/repo.git"
	runGit(t, work, "remote", "add", "origin", withCreds)
	runGit(t, work, "commit", "--allow-empty", "-m", "fetched from "+withCreds)

	if err := scrubOriginURL(context.Background(), work); err != nil {
		t.Fatalf("scrubOriginURL: %v", err)
	}
	out, err := exec.Command("git", "-C", work, "remote", "get-url", "origin").Output()
	if err != nil || strings.TrimSpace(string(out)) != "https://example.invalid/org/repo.git" {
		t.Fatalf("origin = %q (err %v)", out, err)
	}
	cfg, err := os.ReadFile(filepath.Join(work, ".git", "config"))
	if err != nil || strings.Contains(string(cfg), token) {
		t.Fatalf(".git/config still holds the token: %v", err)
	}
	if logs, err := os.ReadFile(filepath.Join(work, ".git", "logs", "HEAD")); err == nil && strings.Contains(string(logs), token) {
		t.Fatal("reflog still quotes the token")
	}

	// A clean URL is left alone.
	if err := scrubOriginURL(context.Background(), work); err != nil {
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
