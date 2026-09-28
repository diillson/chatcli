/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Git credentials for SourceRepository syncs.
//
// Credentials reach git per command and never land in the clone: HTTPS
// credentials go through a GIT_ASKPASS helper that prints them from the
// environment of that one git process, and the SSH key is written to a
// private file for the duration of the sync. The clone's origin URL is the
// plain spec.url, so .git/config never holds a secret (clones made by
// earlier versions, which embedded the token in the URL, are rewritten on
// their next sync).

// Keys of the auth Secret.
const (
	gitSecretToken      = "token"
	gitSecretUsername   = "username"
	gitSecretPassword   = "password"
	gitSecretSSHKey     = "ssh-key"
	gitSecretKnownHosts = "known_hosts"
)

// Environment the askpass helper reads. Scoped to the git process.
const (
	askpassUserEnv = "CHATCLI_GIT_USERNAME"
	askpassPassEnv = "CHATCLI_GIT_PASSWORD"
)

// tokenAuthUsername is the user name sent with a token. GitHub requires
// this one for installation tokens and accepts it for personal tokens;
// GitLab and Bitbucket ignore the user name when the password is a token.
const tokenAuthUsername = "x-access-token"

// askpassScript answers git's two prompts from the environment. It never
// echoes a value it was not given, and it holds no secret itself.
const askpassScript = `#!/bin/sh
case "$1" in
  [Uu]sername*) printf '%s\n' "$` + askpassUserEnv + `" ;;
  *) printf '%s\n' "$` + askpassPassEnv + `" ;;
esac
`

// gitAuth is what one sync hands to every git command it runs.
type gitAuth struct {
	env     []string // extra environment for the git process
	cleanup []string // files removed once the sync is over
}

// done removes the per-sync credential files.
func (a *gitAuth) done() {
	if a == nil {
		return
	}
	for _, f := range a.cleanup {
		_ = os.Remove(f)
	}
}

// baseDir is where clones and per-repository credential files live.
func (r *SourceRepositoryReconciler) baseDir() string {
	if r.BaseDir != "" {
		return r.BaseDir
	}
	return sourceRepoBaseDir
}

// sshDir holds the per-repository SSH key and known_hosts files. A
// namespace cannot be named ".ssh" (DNS label), so it never collides with
// a clone directory.
func (r *SourceRepositoryReconciler) sshDir() string {
	return filepath.Join(r.baseDir(), ".ssh")
}

// sshFileStem is unique per namespace/name ("_" is not valid in either).
func sshFileStem(repo *platformv1alpha1.SourceRepository) string {
	return repo.Namespace + "_" + repo.Name
}

// removeRepoCredentialFiles drops the SSH files of a deleted repository.
func (r *SourceRepositoryReconciler) removeRepoCredentialFiles(namespace, name string) {
	stem := filepath.Join(r.sshDir(), namespace+"_"+name)
	for _, suffix := range []string{".key", ".known_hosts", ".tofu_known_hosts"} {
		_ = os.Remove(stem + suffix)
	}
}

// resolveAuth reads the auth Secret and prepares the credentials for the
// git commands of one sync. The caller runs done() when the sync is over.
func (r *SourceRepositoryReconciler) resolveAuth(ctx context.Context, repo *platformv1alpha1.SourceRepository) (*gitAuth, error) {
	auth := &gitAuth{env: []string{
		// Never block on a terminal prompt; fail instead.
		"GIT_TERMINAL_PROMPT=0",
	}}
	if repo.Spec.AuthType == "" || repo.Spec.AuthType == platformv1alpha1.SourceRepoAuthNone || repo.Spec.SecretRef == "" {
		return auth, nil
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Name: repo.Spec.SecretRef, Namespace: repo.Namespace,
	}, &secret); err != nil {
		return nil, fmt.Errorf("secret %s not found: %w", repo.Spec.SecretRef, err)
	}

	switch repo.Spec.AuthType {
	case platformv1alpha1.SourceRepoAuthToken:
		token := string(secret.Data[gitSecretToken])
		if token == "" {
			return nil, fmt.Errorf("secret %s missing %q key", repo.Spec.SecretRef, gitSecretToken)
		}
		if err := r.useAskpass(auth, tokenAuthUsername, token); err != nil {
			return nil, err
		}

	case platformv1alpha1.SourceRepoAuthBasic:
		username := string(secret.Data[gitSecretUsername])
		password := string(secret.Data[gitSecretPassword])
		if username == "" || password == "" {
			return nil, fmt.Errorf("secret %s missing %q or %q key", repo.Spec.SecretRef, gitSecretUsername, gitSecretPassword)
		}
		if err := r.useAskpass(auth, username, password); err != nil {
			return nil, err
		}

	case platformv1alpha1.SourceRepoAuthSSH:
		if err := r.useSSH(auth, repo, &secret); err != nil {
			auth.done()
			return nil, err
		}
	}
	return auth, nil
}

// useAskpass installs the askpass helper and passes the credential to it
// through the git process environment.
func (r *SourceRepositoryReconciler) useAskpass(auth *gitAuth, username, password string) error {
	helper, err := r.ensureAskpassHelper()
	if err != nil {
		return err
	}
	auth.env = append(auth.env,
		"GIT_ASKPASS="+helper,
		askpassUserEnv+"="+username,
		askpassPassEnv+"="+password,
	)
	return nil
}

// ensureAskpassHelper writes the helper script once per operator pod.
func (r *SourceRepositoryReconciler) ensureAskpassHelper() (string, error) {
	dir := filepath.Join(r.baseDir(), ".bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating askpass dir: %w", err)
	}
	path := filepath.Join(dir, "git-askpass")
	if current, err := os.ReadFile(path); err == nil && string(current) == askpassScript { // #nosec G304 -- fixed file name under the operator's own base dir
		return path, nil
	}
	if err := os.WriteFile(path, []byte(askpassScript), 0o700); err != nil { // #nosec G306 -- the helper must be executable; it holds no secret
		return "", fmt.Errorf("writing askpass helper: %w", err)
	}
	return path, nil
}

// useSSH writes the key and the host keys for this sync and builds the
// ssh command git runs.
func (r *SourceRepositoryReconciler) useSSH(auth *gitAuth, repo *platformv1alpha1.SourceRepository, secret *corev1.Secret) error {
	sshKey := secret.Data[gitSecretSSHKey]
	if len(sshKey) == 0 {
		return fmt.Errorf("secret %s missing %q key", repo.Spec.SecretRef, gitSecretSSHKey)
	}
	if err := os.MkdirAll(r.sshDir(), 0o700); err != nil {
		return fmt.Errorf("creating SSH dir: %w", err)
	}
	stem := filepath.Join(r.sshDir(), sshFileStem(repo))

	keyFile := stem + ".key"
	if err := os.WriteFile(keyFile, ensureTrailingNewline(sshKey), 0o600); err != nil {
		return fmt.Errorf("writing SSH key: %w", err)
	}
	auth.cleanup = append(auth.cleanup, keyFile)

	var knownHostsFile, checking string
	if kh := secret.Data[gitSecretKnownHosts]; len(strings.TrimSpace(string(kh))) > 0 {
		knownHostsFile = stem + ".known_hosts"
		if err := os.WriteFile(knownHostsFile, ensureTrailingNewline(kh), 0o600); err != nil {
			return fmt.Errorf("writing known_hosts: %w", err)
		}
		auth.cleanup = append(auth.cleanup, knownHostsFile)
		checking = "yes"
	} else if repo.Spec.SSHHostKeyPolicy == platformv1alpha1.SSHHostKeyAcceptNew {
		// Kept across syncs: the first key seen is the one enforced later.
		knownHostsFile = stem + ".tofu_known_hosts"
		checking = "accept-new"
	} else {
		return fmt.Errorf("secret %s has no %q key: add the git server's host keys (ssh-keyscan <host>) or set spec.sshHostKeyPolicy: %s to trust the first key seen",
			repo.Spec.SecretRef, gitSecretKnownHosts, platformv1alpha1.SSHHostKeyAcceptNew)
	}

	// GIT_SSH_COMMAND goes through a shell: the paths are made of the base
	// dir and Kubernetes object names only, which hold no shell syntax.
	auth.env = append(auth.env, "GIT_SSH_COMMAND="+strings.Join([]string{
		"ssh", "-F", "/dev/null",
		"-i", keyFile,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "UserKnownHostsFile=" + knownHostsFile,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=" + checking,
	}, " "))
	return nil
}

func ensureTrailingNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return append(append([]byte{}, b...), '\n')
	}
	return b
}

// gitCommand builds a git command carrying the sync's credentials. A
// credential helper configured in the image is switched off so nothing is
// stored on disk.
func gitCommand(ctx context.Context, auth *gitAuth, args ...string) *exec.Cmd {
	full := append([]string{"-c", "credential.helper="}, args...)
	cmd := exec.CommandContext(ctx, "git", full...) // #nosec G204 -- fixed git binary; URL/branch validated by validateGitInputs, paths derived from k8s object names
	cmd.Env = os.Environ()
	if auth != nil {
		cmd.Env = append(cmd.Env, auth.env...)
	}
	return cmd
}

// scrubOriginURL removes a credential from the clone's origin URL. Clones
// made by earlier versions embedded the token in it; rewriting the URL
// without the user info (and dropping the reflog, which may quote the old
// URL) leaves no credential in the clone.
func scrubOriginURL(ctx context.Context, localPath string) error {
	out, err := gitCommand(ctx, nil, "-C", localPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return fmt.Errorf("git remote get-url: %w", err)
	}
	clean, had := stripURLCredentials(strings.TrimSpace(string(out)))
	if !had {
		return nil
	}
	if output, err := gitCommand(ctx, nil, "-C", localPath, "remote", "set-url", "origin", clean).CombinedOutput(); err != nil {
		return fmt.Errorf("git remote set-url: %s: %w", redactURLCredentials(string(output)), err)
	}
	if output, err := gitCommand(ctx, nil, "-C", localPath, "reflog", "expire", "--expire=now", "--all").CombinedOutput(); err != nil {
		return fmt.Errorf("git reflog expire: %s: %w", redactURLCredentials(string(output)), err)
	}
	return nil
}

// stripURLCredentials drops the user info of a URL that carries a password
// or token, and reports whether it did.
func stripURLCredentials(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw, false
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return raw, false
	}
	u.User = nil
	return u.String(), true
}

// urlCredentialsPattern matches the user:password@ part of a URL.
var urlCredentialsPattern = regexp.MustCompile(`://[^/@\s]+:[^/@\s]*@`)

// redactURLCredentials hides user:password@ in git output before it
// reaches the status or the log.
func redactURLCredentials(s string) string {
	return urlCredentialsPattern.ReplaceAllString(s, "://***@")
}
