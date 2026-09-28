/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeDownloadStream is the server side of DownloadPlugin without a
// transport: it carries the caller context and records what was sent.
type fakeDownloadStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*pb.DownloadPluginResponse
}

func (s *fakeDownloadStream) Context() context.Context { return s.ctx }

func (s *fakeDownloadStream) Send(r *pb.DownloadPluginResponse) error {
	s.sent = append(s.sent, r)
	return nil
}

// filePlugin is a plugin whose binary is a plain file on disk.
type filePlugin struct{ name, path string }

func (p filePlugin) Name() string        { return p.name }
func (p filePlugin) Description() string { return "" }
func (p filePlugin) Usage() string       { return "" }
func (p filePlugin) Version() string     { return "1.0.0" }
func (p filePlugin) Path() string        { return p.path }
func (p filePlugin) Schema() string      { return "" }
func (p filePlugin) Execute(context.Context, []string) (string, error) {
	return "", nil
}
func (p filePlugin) ExecuteWithStream(context.Context, []string, func(string)) (string, error) {
	return "", nil
}

func newDownloadTestHandler(t *testing.T) *Handler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	pm, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("plugin manager: %v", err)
	}
	t.Cleanup(pm.Close)
	dir := t.TempDir()
	for _, name := range []string{"@lister", "_internal"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("binary-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		pm.RegisterRemotePlugin(filePlugin{name: name, path: path})
	}
	h := &Handler{logger: zap.NewNop()}
	h.SetPluginManager(pm)
	return h
}

func downloadAs(h *Handler, role UserRole, name string) (*fakeDownloadStream, error) {
	ctx := ContextWithUser(context.Background(), &UserInfo{Subject: "caller", Role: role})
	stream := &fakeDownloadStream{ctx: ctx}
	return stream, h.DownloadPlugin(&pb.DownloadPluginRequest{PluginName: name}, stream)
}

// DownloadPlugin applies the visibility ListRemotePlugins applies: a
// readonly caller lists nothing and downloads nothing, a user cannot fetch
// an internal plugin it cannot list, an admin gets both.
func TestDownloadPlugin_RoleMatchesListVisibility(t *testing.T) {
	h := newDownloadTestHandler(t)

	if _, err := downloadAs(h, RoleReadonly, "@lister"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("readonly download: got %v, want PermissionDenied", err)
	}
	if _, err := downloadAs(h, RoleUser, "_internal"); status.Code(err) != codes.NotFound {
		t.Fatalf("user download of internal plugin: got %v, want NotFound", err)
	}

	stream, err := downloadAs(h, RoleUser, "@lister")
	if err != nil {
		t.Fatalf("user download: %v", err)
	}
	if len(stream.sent) < 2 || string(stream.sent[0].Chunk) != "binary-bytes" || !stream.sent[len(stream.sent)-1].Done {
		t.Fatalf("user download stream: %+v", stream.sent)
	}

	if _, err := downloadAs(h, RoleAdmin, "_internal"); err != nil {
		t.Fatalf("admin download of internal plugin: %v", err)
	}
}
