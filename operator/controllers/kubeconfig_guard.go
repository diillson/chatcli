/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// AnnotationClusterRegistrationBinding on a kubeconfig Secret lists the
// ClusterRegistrations ("namespace/name", comma-separated) allowed to use
// it. The Secret lives in the operator's namespace, where only cluster
// administrators write; the binding stops a ClusterRegistration in any
// other namespace from borrowing a credential that was not meant for it.
const AnnotationClusterRegistrationBinding = "platform.chatcli.io/cluster-registration"

// restConfigFromUntrustedKubeconfig builds a client configuration from a
// kubeconfig that came from a Secret, refusing everything that would make
// the operator run a command, read a local file or skip TLS verification.
//
// client-go honors users[].user.exec (a command run inside the operator
// pod), auth-provider plugins, tokenFile and file paths for certificates
// and keys (read from the operator's filesystem, which holds its own
// ServiceAccount token). Only inline credentials are accepted: token,
// client-certificate-data, client-key-data and certificate-authority-data.
// The server must be https and TLS verification stays on.
func restConfigFromUntrustedKubeconfig(data []byte) (*rest.Config, error) {
	cfg, err := clientcmd.Load(data)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	if err := checkUntrustedKubeconfig(cfg); err != nil {
		return nil, err
	}
	restConfig, err := clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building client configuration: %w", err)
	}
	// The resolved configuration is checked again: whatever client-go
	// derived must still be inline-only.
	switch {
	case restConfig.ExecProvider != nil, restConfig.AuthProvider != nil:
		return nil, fmt.Errorf("kubeconfig refused: credential plugins are not allowed")
	case restConfig.BearerTokenFile != "", restConfig.CertFile != "", restConfig.KeyFile != "", restConfig.CAFile != "":
		return nil, fmt.Errorf("kubeconfig refused: file paths are not allowed, use the inline *-data fields")
	case restConfig.Insecure:
		return nil, fmt.Errorf("kubeconfig refused: insecure-skip-tls-verify is not allowed")
	}
	return restConfig, nil
}

// checkUntrustedKubeconfig rejects every forbidden form in every cluster
// and user of the file, not only the current context: a context switch
// must not be able to reach a forbidden entry.
func checkUntrustedKubeconfig(cfg *clientcmdapi.Config) error {
	var problems []string
	for _, name := range sortedKeys(cfg.Clusters) {
		c := cfg.Clusters[name]
		if c.InsecureSkipTLSVerify {
			problems = append(problems, fmt.Sprintf("cluster %q sets insecure-skip-tls-verify", name))
		}
		if c.CertificateAuthority != "" {
			problems = append(problems, fmt.Sprintf("cluster %q reads certificate-authority from a file (use certificate-authority-data)", name))
		}
		if c.ProxyURL != "" {
			problems = append(problems, fmt.Sprintf("cluster %q sets proxy-url", name))
		}
		if u, err := url.Parse(c.Server); err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
			problems = append(problems, fmt.Sprintf("cluster %q server %q is not an https URL", name, c.Server))
		}
	}
	for _, name := range sortedKeys(cfg.AuthInfos) {
		a := cfg.AuthInfos[name]
		if a.Exec != nil {
			problems = append(problems, fmt.Sprintf("user %q uses exec (a command the operator would run)", name))
		}
		if a.AuthProvider != nil {
			problems = append(problems, fmt.Sprintf("user %q uses an auth-provider plugin", name))
		}
		if a.TokenFile != "" {
			problems = append(problems, fmt.Sprintf("user %q reads tokenFile from a file (use token)", name))
		}
		if a.ClientCertificate != "" {
			problems = append(problems, fmt.Sprintf("user %q reads client-certificate from a file (use client-certificate-data)", name))
		}
		if a.ClientKey != "" {
			problems = append(problems, fmt.Sprintf("user %q reads client-key from a file (use client-key-data)", name))
		}
		if a.Username != "" || a.Password != "" {
			problems = append(problems, fmt.Sprintf("user %q uses basic authentication (use token or a client certificate)", name))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("kubeconfig refused: %s; only inline token, client-certificate-data, client-key-data and certificate-authority-data are accepted",
			strings.Join(problems, "; "))
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// secretBoundToRegistration reports whether a kubeconfig Secret names the
// registration ("namespace/name") in its binding annotation.
func secretBoundToRegistration(annotations map[string]string, registration string) bool {
	for _, v := range strings.Split(annotations[AnnotationClusterRegistrationBinding], ",") {
		if strings.TrimSpace(v) == registration {
			return true
		}
	}
	return false
}
