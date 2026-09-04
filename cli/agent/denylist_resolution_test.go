package agent

import (
	"runtime"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// The denylist is compared against a path already resolved through
// EvalSymlinks. On macOS /etc resolves to /private/etc, so the literal
// entries matched nothing and the list was inert on that platform.
func TestSensitivePathsAreCheckedInBothSpellings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix denylist")
	}
	pv := NewPathValidator("/tmp/workspace", zap.NewNop())
	for _, cmd := range []string{
		"cat /etc/passwd",
		"cat /etc/ssh/ssh_host_rsa_key",
	} {
		flagged, reason := pv.DetectPathTraversal(cmd)
		if !flagged {
			t.Errorf("%q must be flagged as touching a sensitive path", cmd)
			continue
		}
		if !strings.Contains(reason, "sensitive") {
			t.Errorf("%q flagged for the wrong reason: %s", cmd, reason)
		}
	}
}

func TestOrdinaryPathsAreNotFlagged(t *testing.T) {
	pv := NewPathValidator("/tmp/workspace", zap.NewNop())
	if flagged, reason := pv.DetectPathTraversal("go build ./cmd/server"); flagged {
		t.Errorf("an ordinary command must pass: %s", reason)
	}
}

func TestResolveDenyListKeepsTheLiteralEntry(t *testing.T) {
	got := resolveDenyList([]string{"/etc/ssl/"})
	if len(got) == 0 || got[0] != "/etc/ssl/" {
		t.Fatalf("the literal entry must survive: %+v", got)
	}
	for _, e := range got {
		if !strings.HasSuffix(e, "/") {
			t.Errorf("a directory entry keeps its separator: %q", e)
		}
	}
}

// The macOS shadow file lives under the resolved spelling, which is what
// a read check receives.
func TestMasterPasswdIsBlockedInBothSpellings(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific path mapping")
	}
	s := NewSensitiveReadPaths()
	for _, p := range []string{"/etc/master.passwd", "/private/etc/master.passwd"} {
		if blocked, _ := s.isSensitivePath(p); !blocked {
			t.Errorf("%s must be blocked", p)
		}
	}
}
