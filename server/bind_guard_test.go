package server

import (
	"strings"
	"testing"
)

// Loopback is the local CLI's own transport: the boundary is the machine,
// so it may run without a credential. Anything reachable may not.
func TestLoopbackMayRunWithoutAuth(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "")
	for _, addr := range []string{"127.0.0.1", "::1", "localhost", "127.0.0.1:8080", "[::1]:8080"} {
		if err := requireAuthOnReachableBind(addr, ""); err != nil {
			t.Errorf("%s must be allowed without a credential: %v", addr, err)
		}
	}
}

func TestReachableBindRequiresACredential(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "")
	for _, addr := range []string{"0.0.0.0", "::", "10.0.0.5", "", "chatcli.internal"} {
		err := requireAuthOnReachableBind(addr, "")
		if err == nil {
			t.Errorf("%q must refuse to serve unauthenticated", addr)
			continue
		}
		// The message has to say what to do, not just what went wrong.
		for _, want := range []string{"CHATCLI_SERVER_TOKEN", "CHATCLI_JWT_SECRET", "CHATCLI_BIND_ADDRESS"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error for %q should name %s: %v", addr, want, err)
			}
		}
	}
}

func TestReachableBindAcceptsEitherCredential(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "")
	if err := requireAuthOnReachableBind("0.0.0.0", "shared-token"); err != nil {
		t.Errorf("a shared token must be enough: %v", err)
	}
	t.Setenv("CHATCLI_JWT_SECRET", "a-jwt-secret")
	if err := requireAuthOnReachableBind("0.0.0.0", ""); err != nil {
		t.Errorf("a JWT secret must be enough: %v", err)
	}
}

// An address that does not parse is treated as reachable: the safe answer
// to "unknown" is to require a credential.
func TestUnparseableBindIsTreatedAsReachable(t *testing.T) {
	if isLoopbackBind("not-an-address") {
		t.Error("an unresolvable host must not be assumed loopback")
	}
	if isLoopbackBind("") {
		t.Error("an empty bind means every interface, not loopback")
	}
}
