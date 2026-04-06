/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/diillson/chatcli/i18n"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// =============================================================================
// Remote Resource Discovery RPCs
// =============================================================================

// ListRemotePlugins returns plugins installed on the server.
// Security (L12): Filters by role — admin sees all, user sees non-internal, readonly sees none.
func (h *Handler) ListRemotePlugins(ctx context.Context, req *pb.ListRemotePluginsRequest) (*pb.ListRemotePluginsResponse, error) {
	if h.pluginManager == nil {
		return &pb.ListRemotePluginsResponse{}, nil
	}

	// Access control based on role
	user := UserFromContext(ctx)
	if user != nil && user.Role == RoleReadonly {
		return &pb.ListRemotePluginsResponse{}, nil // readonly sees nothing
	}

	plist := h.pluginManager.GetPlugins()
	var result []*pb.PluginInfo
	for _, p := range plist {
		// Filter internal plugins (prefixed with _) for non-admin users
		if user != nil && user.Role != RoleAdmin && strings.HasPrefix(p.Name(), "_") {
			continue
		}
		result = append(result, &pb.PluginInfo{
			Name:        p.Name(),
			Description: p.Description(),
			Usage:       p.Usage(),
			Version:     p.Version(),
			Schema:      p.Schema(),
		})
	}

	return &pb.ListRemotePluginsResponse{Plugins: result}, nil
}

// ListRemoteAgents returns all agents available on the server.
func (h *Handler) ListRemoteAgents(ctx context.Context, req *pb.ListRemoteAgentsRequest) (*pb.ListRemoteAgentsResponse, error) {
	if h.personaLoader == nil {
		return &pb.ListRemoteAgentsResponse{}, nil
	}

	agents, err := h.personaLoader.ListAgents()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.remote.agents_list_error", err))
	}

	var result []*pb.AgentInfo
	for _, a := range agents {
		result = append(result, &pb.AgentInfo{
			Name:        a.Name,
			Description: a.Description,
			Skills:      []string(a.Skills),
			Plugins:     []string(a.Plugins),
			Model:       a.Model,
			Content:     a.Content,
		})
	}

	return &pb.ListRemoteAgentsResponse{Agents: result}, nil
}

// ListRemoteSkills returns all skills available on the server.
func (h *Handler) ListRemoteSkills(ctx context.Context, req *pb.ListRemoteSkillsRequest) (*pb.ListRemoteSkillsResponse, error) {
	if h.personaLoader == nil {
		return &pb.ListRemoteSkillsResponse{}, nil
	}

	skills, err := h.personaLoader.ListSkills()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.remote.skills_list_error", err))
	}

	var result []*pb.SkillInfo
	for _, s := range skills {
		info := &pb.SkillInfo{
			Name:         s.Name,
			Description:  s.Description,
			Content:      s.Content,
			AllowedTools: []string(s.Tools),
		}

		// Load subskills content
		if len(s.Subskills) > 0 {
			info.Subskills = make(map[string]string, len(s.Subskills))
			for name, path := range s.Subskills {
				if content, err := os.ReadFile(path); err == nil {
					info.Subskills[name] = string(content)
				}
			}
		}

		// Map scripts (paths only — scripts execute server-side)
		if len(s.Scripts) > 0 {
			info.Scripts = s.Scripts
		}

		result = append(result, info)
	}

	return &pb.ListRemoteSkillsResponse{Skills: result}, nil
}

// GetAgentDefinition returns the full definition of a server-side agent.
func (h *Handler) GetAgentDefinition(ctx context.Context, req *pb.GetAgentDefinitionRequest) (*pb.GetAgentDefinitionResponse, error) {
	if h.personaLoader == nil {
		return nil, status.Errorf(codes.Unavailable, "%s", i18n.T("server.remote.persona_unavailable"))
	}
	if req.Name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s", i18n.T("server.remote.agent_name_required"))
	}

	agent, err := h.personaLoader.GetAgent(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%s", i18n.T("server.remote.agent_not_found", err))
	}

	return &pb.GetAgentDefinitionResponse{
		Agent: &pb.AgentInfo{
			Name:        agent.Name,
			Description: agent.Description,
			Skills:      []string(agent.Skills),
			Plugins:     []string(agent.Plugins),
			Model:       agent.Model,
			Content:     agent.Content,
		},
	}, nil
}

// GetSkillContent returns the full content of a server-side skill.
func (h *Handler) GetSkillContent(ctx context.Context, req *pb.GetSkillContentRequest) (*pb.GetSkillContentResponse, error) {
	if h.personaLoader == nil {
		return nil, status.Errorf(codes.Unavailable, "%s", i18n.T("server.remote.persona_unavailable"))
	}
	if req.Name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s", i18n.T("server.remote.skill_name_required"))
	}

	skill, err := h.personaLoader.GetSkill(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%s", i18n.T("server.remote.skill_not_found", err))
	}

	info := &pb.SkillInfo{
		Name:         skill.Name,
		Description:  skill.Description,
		Content:      skill.Content,
		AllowedTools: []string(skill.Tools),
	}

	if len(skill.Subskills) > 0 {
		info.Subskills = make(map[string]string, len(skill.Subskills))
		for name, path := range skill.Subskills {
			if content, err := os.ReadFile(path); err == nil {
				info.Subskills[name] = string(content)
			}
		}
	}
	if len(skill.Scripts) > 0 {
		info.Scripts = skill.Scripts
	}

	return &pb.GetSkillContentResponse{Skill: info}, nil
}

// ExecuteRemotePlugin executes a plugin on the server and returns the output.
// Security (C1): Requires at least "user" role — readonly users cannot execute plugins.
func (h *Handler) ExecuteRemotePlugin(ctx context.Context, req *pb.ExecuteRemotePluginRequest) (*pb.ExecuteRemotePluginResponse, error) {
	// Access control: readonly users cannot execute plugins
	if user := UserFromContext(ctx); user != nil && !user.HasRole(RoleUser) {
		return nil, status.Errorf(codes.PermissionDenied, "insufficient permissions: role %q cannot execute plugins", user.Role)
	}

	if h.pluginManager == nil {
		return nil, status.Errorf(codes.Unavailable, "%s", i18n.T("server.remote.plugin_unavailable"))
	}
	if req.PluginName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s", i18n.T("server.remote.plugin_name_required"))
	}

	plugin, ok := h.pluginManager.GetPlugin(req.PluginName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "%s", i18n.T("server.remote.plugin_not_found", req.PluginName))
	}

	h.logger.Info(i18n.T("server.remote.plugin_executing"),
		zap.String("plugin", req.PluginName),
		zap.Strings("args", req.Args),
	)

	output, err := plugin.Execute(ctx, req.Args)
	resp := &pb.ExecuteRemotePluginResponse{
		Output: output,
		Done:   true,
	}
	if err != nil {
		resp.Error = err.Error()
	}

	return resp, nil
}

// DownloadPlugin streams the plugin binary to the client.
func (h *Handler) DownloadPlugin(req *pb.DownloadPluginRequest, stream pb.ChatCLIService_DownloadPluginServer) error {
	if h.pluginManager == nil {
		return status.Errorf(codes.Unavailable, "%s", i18n.T("server.remote.plugin_unavailable"))
	}
	if req.PluginName == "" {
		return status.Errorf(codes.InvalidArgument, "%s", i18n.T("server.remote.plugin_name_required"))
	}

	plugin, ok := h.pluginManager.GetPlugin(req.PluginName)
	if !ok {
		return status.Errorf(codes.NotFound, "%s", i18n.T("server.remote.plugin_not_found", req.PluginName))
	}

	pluginPath := plugin.Path()
	info, err := os.Stat(pluginPath)
	if err != nil {
		return status.Errorf(codes.Internal, "%s", i18n.T("server.remote.plugin_stat_error", err))
	}

	f, err := os.Open(pluginPath)
	if err != nil {
		return status.Errorf(codes.Internal, "%s", i18n.T("server.remote.plugin_open_error", err))
	}
	defer f.Close()

	const chunkSize = 64 * 1024 // 64KB chunks
	buf := make([]byte, chunkSize)
	filename := info.Name()
	totalSize := info.Size()
	first := true

	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			resp := &pb.DownloadPluginResponse{
				Chunk: buf[:n],
				Done:  false,
			}
			if first {
				resp.Filename = filename
				resp.TotalSize = totalSize
				first = false
			}
			if err := stream.Send(resp); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return status.Errorf(codes.Internal, "%s", i18n.T("server.remote.plugin_read_error", readErr))
		}
	}

	// Send final empty chunk with done=true
	return stream.Send(&pb.DownloadPluginResponse{Done: true})
}
