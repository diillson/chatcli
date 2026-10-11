/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"errors"
	"testing"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/plugins"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// recordingPlugin records whether it ran; registered under "@coder" it
// stands in for the builtin coder so no command really executes.
type recordingPlugin struct {
	name string
	ran  bool
}

func (p *recordingPlugin) Name() string        { return p.name }
func (p *recordingPlugin) Description() string { return "" }
func (p *recordingPlugin) Usage() string       { return "" }
func (p *recordingPlugin) Version() string     { return "test" }
func (p *recordingPlugin) Path() string        { return "[builtin]" }
func (p *recordingPlugin) Schema() string      { return "" }
func (p *recordingPlugin) Execute(context.Context, []string) (string, error) {
	p.ran = true
	return "ok", nil
}
func (p *recordingPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	return p.Execute(ctx, args)
}

func newRemoteCoderHandler(t *testing.T) (*Handler, *recordingPlugin, *recordingPlugin) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv(coder.PolicyAutoModeEnv, "")
	pm, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("plugin manager: %v", err)
	}
	t.Cleanup(pm.Close)
	cod := &recordingPlugin{name: "@coder"}
	other := &recordingPlugin{name: "@lister"}
	pm.RegisterBuiltinPlugin(cod)
	pm.RegisterBuiltinPlugin(other)
	h := &Handler{logger: zap.NewNop()}
	h.SetPluginManager(pm)
	return h, cod, other
}

func execAs(h *Handler, role UserRole, plugin string, args ...string) error {
	ctx := ContextWithUser(context.Background(), &UserInfo{Subject: "caller", Role: role})
	_, err := h.ExecuteRemotePlugin(ctx, &pb.ExecuteRemotePluginRequest{PluginName: plugin, Args: args})
	return err
}

func wantDenied(t *testing.T, err error, what string) {
	t.Helper()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("%s: want PermissionDenied, got %v", what, err)
	}
}

// TestExecuteRemotePlugin_CoderRequiresAdmin: the default role of a JWT
// without a role claim and of mTLS is "user"; it must not reach a shell.
func TestExecuteRemotePlugin_CoderRequiresAdmin(t *testing.T) {
	h, cod, _ := newRemoteCoderHandler(t)
	wantDenied(t, execAs(h, RoleUser, "@coder", "exec", "--cmd", "id", "--allow-unsafe"), "user role")
	wantDenied(t, execAs(h, RoleUser, "@coder", "read", "--file", "x"), "user role read")
	if cod.ran {
		t.Fatal("@coder ran for a non-admin caller")
	}
}

// TestExecuteRemotePlugin_AdminSubjectToPolicy: an admin still goes through
// the coder policy with nobody to approve — exec asks, so it is refused
// unless the operator opted into automode; reads the policy allows run.
func TestExecuteRemotePlugin_AdminSubjectToPolicy(t *testing.T) {
	h, cod, _ := newRemoteCoderHandler(t)

	wantDenied(t, execAs(h, RoleAdmin, "@coder", "exec", "--cmd", "touch built.txt"), "admin exec without opt-in")
	if cod.ran {
		t.Fatal("an ask-gated exec ran with no one to approve it")
	}

	if err := execAs(h, RoleAdmin, "@coder", "read", "--file", "main.go"); err != nil {
		t.Fatalf("a policy-allowed read must run for admin: %v", err)
	}
	if !cod.ran {
		t.Fatal("the allowed read did not reach the plugin")
	}
}

func TestExecuteRemotePlugin_AdminAutoModeStillHonorsDenyAndImmune(t *testing.T) {
	h, cod, _ := newRemoteCoderHandler(t)
	t.Setenv(coder.PolicyAutoModeEnv, "true")

	if err := execAs(h, RoleAdmin, "@coder", "exec", "--cmd", "touch built.txt"); err != nil {
		t.Fatalf("automode must approve a plain ask for admin: %v", err)
	}
	cod.ran = false

	wantDenied(t, execAs(h, RoleAdmin, "@coder", "exec", "--cmd", "git push --force origin main"), "safety-immune under automode")

	pm, err := coder.NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.AddRule("@coder exec --cmd touch", coder.ActionDeny); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, execAs(h, RoleAdmin, "@coder", "exec", "--cmd", "touch built.txt"), "deny rule under automode")
	if cod.ran {
		t.Fatal("a denied or immune call reached the plugin")
	}
}

func TestExecuteRemotePlugin_CoderPolicyFailureDenies(t *testing.T) {
	h, cod, _ := newRemoteCoderHandler(t)
	prev := remoteCoderPolicy
	remoteCoderPolicy = func(*zap.Logger) (*coder.PolicyManager, error) { return nil, errors.New("no home") }
	t.Cleanup(func() { remoteCoderPolicy = prev })
	t.Setenv(coder.PolicyAutoModeEnv, "true")

	wantDenied(t, execAs(h, RoleAdmin, "@coder", "read", "--file", "x"), "policy unavailable")
	if cod.ran {
		t.Fatal("@coder ran without a policy")
	}
}

// TestExecuteRemotePlugin_OtherPluginsKeepUserRole: the admin requirement is
// specific to @coder.
func TestExecuteRemotePlugin_OtherPluginsKeepUserRole(t *testing.T) {
	h, _, other := newRemoteCoderHandler(t)
	if err := execAs(h, RoleUser, "@lister"); err != nil {
		t.Fatalf("a user must still run other plugins: %v", err)
	}
	if !other.ran {
		t.Fatal("the plugin did not run")
	}
	wantDenied(t, execAs(h, RoleReadonly, "@lister"), "readonly")
}
