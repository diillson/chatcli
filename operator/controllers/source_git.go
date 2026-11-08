/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/skeema/knownhosts"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Git access for SourceRepository syncs.
//
// Clones, fetches and the commit log are done in-process with go-git: the
// operator image carries no git or ssh binaries, no shell and no package
// manager, so there is nothing in it to patch but the manager itself.
//
// Credentials live in memory for the duration of one sync and never land
// in the clone: HTTPS credentials are sent as basic auth on each request,
// and the SSH private key is parsed from the Secret into a signer. The
// clone's origin URL is the plain spec.url, so .git/config never holds a
// secret (clones made by earlier versions, which embedded the token in the
// URL, are rewritten on their next sync). Only the git server's host keys
// are written to disk, because that is what a known_hosts check reads.

// Keys of the auth Secret.
const (
	gitSecretToken      = "token"
	gitSecretUsername   = "username"
	gitSecretPassword   = "password"
	gitSecretSSHKey     = "ssh-key"
	gitSecretKnownHosts = "known_hosts"
	// gitSecretCABundle holds PEM certificates of a private CA that signed
	// the git server's HTTPS certificate. Added to the system roots.
	gitSecretCABundle = "ca.crt"
)

// tokenAuthUsername is the user name sent with a token. GitHub requires
// this one for installation tokens and accepts it for personal tokens;
// GitLab and Bitbucket ignore the user name when the password is a token.
const tokenAuthUsername = "x-access-token"

// gitAuth is what one sync hands to every git operation it runs.
type gitAuth struct {
	method   transport.AuthMethod // nil for a public repository
	caBundle []byte               // extra PEM roots for HTTPS, or nil
	cleanup  []string             // files removed once the sync is over
}

// authMethod is nil-safe: a sync without credentials passes no auth.
func (a *gitAuth) authMethod() transport.AuthMethod {
	if a == nil {
		return nil
	}
	return a.method
}

// ca is nil-safe: no bundle means the system roots alone.
func (a *gitAuth) ca() []byte {
	if a == nil {
		return nil
	}
	return a.caBundle
}

// done removes the per-sync files.
func (a *gitAuth) done() {
	if a == nil {
		return
	}
	for _, f := range a.cleanup {
		_ = os.Remove(f)
	}
}

// baseDir is where clones and per-repository host key files live.
func (r *SourceRepositoryReconciler) baseDir() string {
	if r.BaseDir != "" {
		return r.BaseDir
	}
	return sourceRepoBaseDir
}

// sshDir holds the per-repository known_hosts files. A namespace cannot
// be named ".ssh" (DNS label), so it never collides with a clone directory.
func (r *SourceRepositoryReconciler) sshDir() string {
	return filepath.Join(r.baseDir(), ".ssh")
}

// sshFileStem is unique per namespace/name ("_" is not valid in either).
func sshFileStem(repo *platformv1alpha1.SourceRepository) string {
	return repo.Namespace + "_" + repo.Name
}

// removeRepoCredentialFiles drops the SSH files of a deleted repository.
// The ".key" suffix is the private key file earlier versions wrote for the
// ssh binary; it is removed in case one was left behind by a pod that
// stopped mid-sync before the upgrade.
func (r *SourceRepositoryReconciler) removeRepoCredentialFiles(namespace, name string) {
	stem := filepath.Join(r.sshDir(), namespace+"_"+name)
	for _, suffix := range []string{".key", ".known_hosts", ".tofu_known_hosts"} {
		_ = os.Remove(stem + suffix)
	}
}

// resolveAuth reads the auth Secret and prepares the credentials for the
// git operations of one sync. The caller runs done() when the sync is over.
func (r *SourceRepositoryReconciler) resolveAuth(ctx context.Context, repo *platformv1alpha1.SourceRepository) (*gitAuth, error) {
	auth := &gitAuth{}
	if repo.Spec.SecretRef == "" {
		return auth, nil
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Name: repo.Spec.SecretRef, Namespace: repo.Namespace,
	}, &secret); err != nil {
		return nil, fmt.Errorf("secret %s not found: %w", repo.Spec.SecretRef, err)
	}
	if ca := secret.Data[gitSecretCABundle]; len(strings.TrimSpace(string(ca))) > 0 {
		auth.caBundle = ca
	}

	switch repo.Spec.AuthType {
	case "", platformv1alpha1.SourceRepoAuthNone:
		// A public repository on a server with a private CA: the Secret
		// carries only ca.crt.

	case platformv1alpha1.SourceRepoAuthToken:
		token := string(secret.Data[gitSecretToken])
		if token == "" {
			return nil, fmt.Errorf("secret %s missing %q key", repo.Spec.SecretRef, gitSecretToken)
		}
		auth.method = &githttp.BasicAuth{Username: tokenAuthUsername, Password: token}

	case platformv1alpha1.SourceRepoAuthBasic:
		username := string(secret.Data[gitSecretUsername])
		password := string(secret.Data[gitSecretPassword])
		if username == "" || password == "" {
			return nil, fmt.Errorf("secret %s missing %q or %q key", repo.Spec.SecretRef, gitSecretUsername, gitSecretPassword)
		}
		auth.method = &githttp.BasicAuth{Username: username, Password: password}

	case platformv1alpha1.SourceRepoAuthSSH:
		if err := r.useSSH(auth, repo, &secret); err != nil {
			auth.done()
			return nil, err
		}
	}
	return auth, nil
}

// useSSH parses the private key into memory and decides how the git
// server's host key is verified: against the Secret's known_hosts, or by
// trust on first use when the SourceRepository opts in.
func (r *SourceRepositoryReconciler) useSSH(auth *gitAuth, repo *platformv1alpha1.SourceRepository, secret *corev1.Secret) error {
	sshKey := secret.Data[gitSecretSSHKey]
	if len(sshKey) == 0 {
		return fmt.Errorf("secret %s missing %q key", repo.Spec.SecretRef, gitSecretSSHKey)
	}
	endpoint, err := transport.NewEndpoint(repo.Spec.URL)
	if err != nil {
		return fmt.Errorf("parsing repository URL: %w", err)
	}
	user := endpoint.User
	if user == "" {
		user = gitssh.DefaultUsername
	}
	// The key never touches the disk: ParsePrivateKey reads the PEM block
	// into a signer. Its errors name the format problem, not the key.
	keys, err := gitssh.NewPublicKeys(user, ensureTrailingNewline(sshKey), "")
	if err != nil {
		return fmt.Errorf("secret %s: %q is not a usable private key: %w", repo.Spec.SecretRef, gitSecretSSHKey, err)
	}

	if err := os.MkdirAll(r.sshDir(), 0o700); err != nil {
		return fmt.Errorf("creating SSH dir: %w", err)
	}
	stem := filepath.Join(r.sshDir(), sshFileStem(repo))
	hostWithPort := sshHostWithPort(endpoint)

	switch {
	case len(strings.TrimSpace(string(secret.Data[gitSecretKnownHosts]))) > 0:
		// Strict: the host key must be one the Secret lists for this host.
		knownHostsFile := stem + ".known_hosts"
		if err := os.WriteFile(knownHostsFile, ensureTrailingNewline(secret.Data[gitSecretKnownHosts]), 0o600); err != nil {
			return fmt.Errorf("writing known_hosts: %w", err)
		}
		auth.cleanup = append(auth.cleanup, knownHostsFile)
		db, err := knownhosts.NewDB(knownHostsFile)
		if err != nil {
			return fmt.Errorf("secret %s: reading %q: %w", repo.Spec.SecretRef, gitSecretKnownHosts, err)
		}
		keys.HostKeyCallback = db.HostKeyCallback()
		keys.HostKeyAlgorithms = db.HostKeyAlgorithms(hostWithPort)

	case repo.Spec.SSHHostKeyPolicy == platformv1alpha1.SSHHostKeyAcceptNew:
		// Kept across syncs: the first key seen is the one enforced later.
		tofuFile := stem + ".tofu_known_hosts"
		if err := touchPrivateFile(tofuFile); err != nil {
			return fmt.Errorf("creating trust-on-first-use known_hosts: %w", err)
		}
		db, err := knownhosts.NewDB(tofuFile)
		if err != nil {
			return fmt.Errorf("reading trust-on-first-use known_hosts: %w", err)
		}
		keys.HostKeyCallback = trustOnFirstUse(db, tofuFile)
		keys.HostKeyAlgorithms = db.HostKeyAlgorithms(hostWithPort)

	default:
		return fmt.Errorf("secret %s has no %q key: add the git server's host keys (ssh-keyscan <host>) or set spec.sshHostKeyPolicy: %s to trust the first key seen",
			repo.Spec.SecretRef, gitSecretKnownHosts, platformv1alpha1.SSHHostKeyAcceptNew)
	}
	auth.method = keys
	return nil
}

// sshHostWithPort is the address the known_hosts lookup is keyed by.
func sshHostWithPort(endpoint *transport.Endpoint) string {
	port := endpoint.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(endpoint.Host, strconv.Itoa(port))
}

// touchPrivateFile creates an empty 0600 file if none exists.
func touchPrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- path is built from the base dir and Kubernetes object names
	if err != nil {
		return err
	}
	return f.Close()
}

// trustOnFirstUse is OpenSSH's StrictHostKeyChecking=accept-new: a host
// with no recorded key has the key it presents appended to file and
// accepted; a host with a recorded key must present that key. Only the
// unknown-host error is turned into an acceptance, so a changed key, a
// malformed record or an unreadable file still refuse the connection.
func trustOnFirstUse(db *knownhosts.HostKeyDB, file string) ssh.HostKeyCallback {
	var mu sync.Mutex
	check := db.HostKeyCallback()
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()
		err := check(hostname, remote, key)
		if err == nil || !knownhosts.IsHostUnknown(err) {
			return err
		}
		f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- path is built from the base dir and Kubernetes object names
		if err != nil {
			return fmt.Errorf("recording host key: %w", err)
		}
		writeErr := knownhosts.WriteKnownHost(f, hostname, remote, key)
		if closeErr := f.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return fmt.Errorf("recording host key: %w", writeErr)
		}
		// Later connections of this sync see the recorded key.
		if reloaded, err := knownhosts.NewDB(file); err == nil {
			check = reloaded.HostKeyCallback()
		}
		return nil
	}
}

func ensureTrailingNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return append(append([]byte{}, b...), '\n')
	}
	return b
}

// scrubOriginURL removes a credential from the clone's origin URL. Clones
// made by earlier versions embedded the token in it; rewriting the URL
// without the user info, and dropping the reflog those versions' git
// binary kept (which may quote the old URL; go-git writes none), leaves
// no credential in the clone.
func scrubOriginURL(repo *git.Repository, localPath string) error {
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("reading clone config: %w", err)
	}
	origin := cfg.Remotes[git.DefaultRemoteName]
	if origin == nil {
		return nil
	}
	changed := false
	for i, raw := range origin.URLs {
		if clean, had := stripURLCredentials(raw); had {
			origin.URLs[i] = clean
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("rewriting origin URL: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(localPath, git.GitDirName, "logs")); err != nil {
		return fmt.Errorf("dropping reflog: %w", err)
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
