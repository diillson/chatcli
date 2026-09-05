/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Prefixing a benign command used to be a passphrase for the rest of the
// line: only the first word was checked, so anything after && or | rode in.
func TestAllowlist_ChecksEveryCommandOnTheLine(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_SECURITY_MODE", "strict")
	al := NewCommandAllowlist()

	bypasses := []string{
		"ls && nmap -sS 10.0.0.0/8",
		"echo hi; nmap localhost",
		"ls | nmap -",
		"ls || nmap -",
		"ls && ls && nmap -",
		"(cd /tmp && nmap -)",
		"ls & nmap -",
	}
	for _, cmd := range bypasses {
		if ok, _, _ := al.IsAllowed(cmd); ok {
			t.Errorf("bypass still open: %q passed the strict allowlist", cmd)
		}
	}
}

// Everything legitimate must keep working — a chain of allowed commands is
// the normal case, not an attack.
func TestAllowlist_AllowsChainsOfAllowedCommands(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_SECURITY_MODE", "strict")
	al := NewCommandAllowlist()

	fine := []string{
		"ls -la",
		"go build ./... && go test ./...",
		"cat go.mod | grep module",
		"git status; git diff",
		"grep -r foo . | sort | uniq -c | head",
		"FOO=bar go test ./...",
		"cd /tmp && ls",
	}
	for _, cmd := range fine {
		if ok, _, reason := al.IsAllowed(cmd); !ok {
			t.Errorf("legitimate command refused: %q (%s)", cmd, reason)
		}
	}
}

// Quoting must not be mistaken for a chain: the shell parser is the point.
func TestAllowlist_QuotedOperatorsAreNotSegments(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_SECURITY_MODE", "strict")
	al := NewCommandAllowlist()

	for _, cmd := range []string{
		`echo "a && b"`,
		`grep "foo | bar" file.txt`,
		`echo 'nmap is a word here'`,
	} {
		if ok, _, reason := al.IsAllowed(cmd); !ok {
			t.Errorf("quoted operator treated as a chain: %q (%s)", cmd, reason)
		}
	}
}

// The sudo prefix keeps being handled by the denylist, not by demanding
// that "sudo" itself be an allowlisted command.
func TestAllowlist_SudoPrefixStillResolvesToTheRealCommand(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_SECURITY_MODE", "strict")
	al := NewCommandAllowlist()

	if ok, _, reason := al.IsAllowed("sudo ls -la"); !ok {
		t.Errorf("sudo prefix changed the allowlist verdict: %s", reason)
	}
	if ok, _, _ := al.IsAllowed("sudo nmap -"); ok {
		t.Error("sudo hid a command that is not on the allowlist")
	}
}

// Every command the documentation lists as allowed must actually be allowed.
func TestAllowlist_CoversTheDocumentedCommands(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_SECURITY_MODE", "strict")
	al := NewCommandAllowlist()

	documented := []string{
		"ag", "argocd", "base64", "cal", "clear", "cmp", "csvtool", "flux",
		"istioctl", "kotlinc", "look", "npx", "openssl", "poetry", "reset",
		"stty", "tput", "xmllint", "zig",
	}
	for _, cmd := range documented {
		if ok, _, _ := al.IsAllowed(cmd + " --version"); !ok {
			t.Errorf("%q is documented as allowed and is not", cmd)
		}
	}
}

// The value shape the documentation gave has to work, or the setting looks
// applied and is not.
func TestAllowlist_CustomListAcceptsBothSeparators(t *testing.T) {
	for _, value := range []string{
		"mycli;internal-tool;company-deploy",
		"mycli,internal-tool,company-deploy",
		"mycli; internal-tool ,company-deploy",
	} {
		t.Setenv("CHATCLI_AGENT_ALLOWLIST", value)
		al := NewCommandAllowlist()
		for _, cmd := range []string{"mycli", "internal-tool", "company-deploy"} {
			if ok, _, _ := al.IsAllowed(cmd + " run"); !ok {
				t.Errorf("CHATCLI_AGENT_ALLOWLIST=%q did not register %q", value, cmd)
			}
		}
	}
}

func TestExtraReadPaths_AcceptsBothSeparators(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("':' is a drive-letter separator on Windows")
	}
	for _, value := range []string{
		"/etc/hosts;/usr/local/share/config",
		"/etc/hosts:/usr/local/share/config",
	} {
		t.Setenv("CHATCLI_AGENT_EXTRA_READ_PATHS", value)
		s := NewSensitiveReadPaths()
		if len(s.extraReadPaths) != 2 {
			t.Errorf("CHATCLI_AGENT_EXTRA_READ_PATHS=%q parsed to %v", value, s.extraReadPaths)
			continue
		}
		if s.extraReadPaths[0] != "/etc/hosts" || s.extraReadPaths[1] != "/usr/local/share/config" {
			t.Errorf("CHATCLI_AGENT_EXTRA_READ_PATHS=%q parsed to %v", value, s.extraReadPaths)
		}
	}
}

// A path list that genuinely needs a semicolon in a name still resolves via
// the native separator, so nothing that worked stops working.
func TestExtraReadPaths_NativeSeparatorStillWorks(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	t.Setenv("CHATCLI_AGENT_EXTRA_READ_PATHS", a+string(os.PathListSeparator)+b)
	s := NewSensitiveReadPaths()
	if len(s.extraReadPaths) != 2 {
		t.Fatalf("native separator parsed to %v", s.extraReadPaths)
	}
}
