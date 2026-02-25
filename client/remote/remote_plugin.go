/*
 * ChatCLI - Remote Plugin Implementation
 * client/remote/remote_plugin.go
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package remote

import (
	"context"
	"fmt"
	"os"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// RemotePlugin implements the plugins.Plugin interface by delegating execution
// to a remote ChatCLI server via gRPC.
type RemotePlugin struct {
	name        string
	description string
	usage       string
	version     string
	schema      string
	client      *Client // gRPC client for remote execution
}

// NewRemotePlugin creates a RemotePlugin from server-provided metadata.
func NewRemotePlugin(info *pb.PluginInfo, client *Client) *RemotePlugin {
	return &RemotePlugin{
		name:        info.Name,
		description: info.Description,
		usage:       info.Usage,
		version:     info.Version,
		schema:      info.Schema,
		client:      client,
	}
}

// NewRemotePluginFromInfo creates a RemotePlugin from a RemotePluginInfo struct.
func NewRemotePluginFromInfo(info RemotePluginInfo, client *Client) *RemotePlugin {
	return &RemotePlugin{
		name:        info.Name,
		description: info.Description,
		usage:       info.Usage,
		version:     info.Version,
		schema:      info.Schema,
		client:      client,
	}
}

func (p *RemotePlugin) Name() string        { return p.name }
func (p *RemotePlugin) Description() string { return p.description }
func (p *RemotePlugin) Usage() string       { return p.usage }
func (p *RemotePlugin) Version() string     { return p.version }
func (p *RemotePlugin) Path() string        { return "[remote]" }
func (p *RemotePlugin) Schema() string      { return p.schema }

// Execute runs the plugin on the remote server.
func (p *RemotePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs the plugin on the remote server with optional output callback.
func (p *RemotePlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	output, err := p.client.ExecuteRemotePlugin(ctx, p.name, args)
	if onOutput != nil && output != "" {
		onOutput(output)
	}
	return output, err
}

// IsRemote returns true to indicate this is a remote plugin.
func (p *RemotePlugin) IsRemote() bool { return true }

// --- Client methods for remote resource operations ---

// ExecuteRemotePlugin executes a plugin on the remote server.
func (c *Client) ExecuteRemotePlugin(ctx context.Context, pluginName string, args []string) (string, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.ExecuteRemotePlugin(ctx, &pb.ExecuteRemotePluginRequest{
		PluginName: pluginName,
		Args:       args,
	})
	if err != nil {
		return "", fmt.Errorf("remote ExecuteRemotePlugin failed: %w", err)
	}
	if resp.Error != "" {
		return resp.Output, fmt.Errorf("remote plugin error: %s", resp.Error)
	}
	return resp.Output, nil
}

// RemotePluginInfo holds plugin metadata from the server.
type RemotePluginInfo struct {
	Name        string
	Description string
	Usage       string
	Version     string
	Schema      string
}

// ListRemotePlugins lists all plugins available on the server.
func (c *Client) ListRemotePlugins(ctx context.Context) ([]RemotePluginInfo, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.ListRemotePlugins(ctx, &pb.ListRemotePluginsRequest{})
	if err != nil {
		return nil, fmt.Errorf("remote ListRemotePlugins failed: %w", err)
	}

	var result []RemotePluginInfo
	for _, p := range resp.Plugins {
		result = append(result, RemotePluginInfo{
			Name:        p.Name,
			Description: p.Description,
			Usage:       p.Usage,
			Version:     p.Version,
			Schema:      p.Schema,
		})
	}
	return result, nil
}

// RemoteAgentInfo holds agent metadata from the server.
type RemoteAgentInfo struct {
	Name        string
	Description string
	Skills      []string
	Plugins     []string
	Model       string
	Content     string
}

// ListRemoteAgents lists all agents available on the server.
func (c *Client) ListRemoteAgents(ctx context.Context) ([]RemoteAgentInfo, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.ListRemoteAgents(ctx, &pb.ListRemoteAgentsRequest{})
	if err != nil {
		return nil, fmt.Errorf("remote ListRemoteAgents failed: %w", err)
	}

	var result []RemoteAgentInfo
	for _, a := range resp.Agents {
		result = append(result, RemoteAgentInfo{
			Name:        a.Name,
			Description: a.Description,
			Skills:      a.Skills,
			Plugins:     a.Plugins,
			Model:       a.Model,
			Content:     a.Content,
		})
	}
	return result, nil
}

// RemoteSkillInfo holds skill metadata from the server.
type RemoteSkillInfo struct {
	Name         string
	Description  string
	Content      string
	AllowedTools []string
	Subskills    map[string]string
	Scripts      map[string]string
}

// ListRemoteSkills lists all skills available on the server.
func (c *Client) ListRemoteSkills(ctx context.Context) ([]RemoteSkillInfo, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.ListRemoteSkills(ctx, &pb.ListRemoteSkillsRequest{})
	if err != nil {
		return nil, fmt.Errorf("remote ListRemoteSkills failed: %w", err)
	}

	var result []RemoteSkillInfo
	for _, s := range resp.Skills {
		result = append(result, RemoteSkillInfo{
			Name:         s.Name,
			Description:  s.Description,
			Content:      s.Content,
			AllowedTools: s.AllowedTools,
			Subskills:    s.Subskills,
			Scripts:      s.Scripts,
		})
	}
	return result, nil
}

// GetAgentDefinition fetches the full definition of a remote agent.
func (c *Client) GetAgentDefinition(ctx context.Context, name string) (*RemoteAgentInfo, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.GetAgentDefinition(ctx, &pb.GetAgentDefinitionRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("remote GetAgentDefinition failed: %w", err)
	}
	a := resp.Agent
	return &RemoteAgentInfo{
		Name:        a.Name,
		Description: a.Description,
		Skills:      a.Skills,
		Plugins:     a.Plugins,
		Model:       a.Model,
		Content:     a.Content,
	}, nil
}

// GetSkillContent fetches the full content of a remote skill.
func (c *Client) GetSkillContent(ctx context.Context, name string) (*RemoteSkillInfo, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.GetSkillContent(ctx, &pb.GetSkillContentRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("remote GetSkillContent failed: %w", err)
	}
	s := resp.Skill
	return &RemoteSkillInfo{
		Name:         s.Name,
		Description:  s.Description,
		Content:      s.Content,
		AllowedTools: s.AllowedTools,
		Subskills:    s.Subskills,
		Scripts:      s.Scripts,
	}, nil
}

// DownloadPlugin downloads a plugin binary from the server to the specified directory.
func (c *Client) DownloadPlugin(ctx context.Context, pluginName, targetDir string) (string, error) {
	ctx = c.withAuth(ctx)

	stream, err := c.grpcClient.DownloadPlugin(ctx, &pb.DownloadPluginRequest{
		PluginName: pluginName,
	})
	if err != nil {
		return "", fmt.Errorf("remote DownloadPlugin failed: %w", err)
	}

	var (
		filename string
		data     []byte
	)

	for {
		resp, err := stream.Recv()
		if err != nil {
			return "", fmt.Errorf("download stream error: %w", err)
		}
		if filename == "" && resp.Filename != "" {
			filename = resp.Filename
		}
		data = append(data, resp.Chunk...)
		if resp.Done {
			break
		}
	}

	if filename == "" {
		filename = pluginName
	}

	targetPath := targetDir + "/" + filename
	if err := writePluginFile(targetPath, data); err != nil {
		return "", fmt.Errorf("failed to write plugin file: %w", err)
	}

	return targetPath, nil
}

func writePluginFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o755)
}
