/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * rpc_support_resources.go
 *
 * Read-only export of ChatCLI's local state to MCP clients as resources under
 * the chatcli:// scheme: the user's long-term memory, profile and projects,
 * the /context store (including knowledge bases with paged document reads),
 * the skill catalog and the saved sessions. This is what lets a user who
 * built their knowledge in ChatCLI browse the SAME state from any MCP client
 * on the machine. Everything here is read-only by construction; mutations go
 * through the tools surface (@memory, @context, manage_session, …).
 *
 * URIs:
 *   chatcli://memory/index | longterm | profile | projects | stats
 *   chatcli://contexts                       — catalog (JSON)
 *   chatcli://contexts/{name}                — rendered content / index card
 *   chatcli://knowledge/{kb}                 — TOC
 *   chatcli://knowledge/{kb}/{source}?offset=N — paged document read
 *   chatcli://skills                         — catalog with triggers (JSON)
 *   chatcli://skills/{name}                  — skill body (markdown)
 *   chatcli://sessions                       — saved-session names
 *   chatcli://sessions/{name}                — saved session (JSON)
 *
 * Gate: CHATCLI_MCP_RESOURCES=off disables the surface entirely.
 */
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
)

// RPCResourceInfo mirrors rpcserve.ResourceInfo without importing it (the
// dependency points the other way: cmd adapts between the two).
type RPCResourceInfo struct {
	URI         string
	Name        string
	Description string
	MimeType    string
}

// RPCResourceContent mirrors rpcserve.ResourceContent.
type RPCResourceContent struct {
	URI      string
	MimeType string
	Text     string
}

// rpcResourcesEnabled reports whether the resources surface is on
// (CHATCLI_MCP_RESOURCES, default on — everything exported is read-only).
func rpcResourcesEnabled() bool {
	v := strings.TrimSpace(os.Getenv("CHATCLI_MCP_RESOURCES"))
	return !strings.EqualFold(v, "off") && !strings.EqualFold(v, "false") && v != "0"
}

const memoryIndexResourceBudget = 4000

// ListRPCResources enumerates the exported resources. Dynamic entries
// (contexts, knowledge bases, skills, sessions) are listed individually so an
// MCP client can browse them without knowing names in advance.
func (cli *ChatCLI) ListRPCResources() []RPCResourceInfo {
	if !rpcResourcesEnabled() {
		return nil
	}
	var out []RPCResourceInfo

	if cli.memoryStore != nil {
		out = append(out,
			RPCResourceInfo{URI: "chatcli://memory/index", Name: "memory-index", Description: "Compact index of the user's long-term memory (facts, topics, projects)."},
			RPCResourceInfo{URI: "chatcli://memory/longterm", Name: "memory-longterm", Description: "The user's full long-term memory notes.", MimeType: "text/markdown"},
			RPCResourceInfo{URI: "chatcli://memory/profile", Name: "memory-profile", Description: "The user's profile (preferences, environment, identity).", MimeType: "application/json"},
			RPCResourceInfo{URI: "chatcli://memory/projects", Name: "memory-projects", Description: "The user's tracked projects.", MimeType: "application/json"},
			RPCResourceInfo{URI: "chatcli://memory/stats", Name: "memory-stats", Description: "Memory store statistics."},
		)
	}

	if cli.contextHandler != nil {
		out = append(out, RPCResourceInfo{URI: "chatcli://contexts", Name: "contexts", Description: "Catalog of every stored /context (JSON).", MimeType: "application/json"})
		if ctxs, err := cli.contextHandler.GetManager().ListContexts(nil); err == nil {
			for _, fc := range ctxs {
				if fc.Mode == ctxmgr.ModeKnowledge {
					out = append(out, RPCResourceInfo{
						URI:         "chatcli://knowledge/" + url.PathEscape(fc.Name),
						Name:        "knowledge-" + fc.Name,
						Description: "Knowledge base TOC: " + fc.Description + " (read documents via chatcli://knowledge/" + url.PathEscape(fc.Name) + "/{source}?offset=N)",
					})
					continue
				}
				out = append(out, RPCResourceInfo{
					URI:         "chatcli://contexts/" + url.PathEscape(fc.Name),
					Name:        "context-" + fc.Name,
					Description: fc.Description,
				})
			}
		}
	}

	if cli.skillHandler != nil && cli.skillHandler.personaMgr != nil {
		out = append(out, RPCResourceInfo{URI: "chatcli://skills", Name: "skills", Description: "Catalog of installed skills with triggers (JSON).", MimeType: "application/json"})
		if skills, err := cli.skillHandler.personaMgr.ListSkills(); err == nil {
			for _, s := range skills {
				out = append(out, RPCResourceInfo{
					URI:         "chatcli://skills/" + url.PathEscape(s.Name),
					Name:        "skill-" + s.Name,
					Description: s.Description,
					MimeType:    "text/markdown",
				})
			}
		}
	}

	if cli.sessionManager != nil {
		out = append(out, RPCResourceInfo{URI: "chatcli://sessions", Name: "sessions", Description: "Saved-session names (one per line)."})
	}
	return out
}

// ReadRPCResource resolves one chatcli:// URI to its content.
func (cli *ChatCLI) ReadRPCResource(_ context.Context, uri string) (RPCResourceContent, error) {
	if !rpcResourcesEnabled() {
		return RPCResourceContent{}, fmt.Errorf("resources are disabled (CHATCLI_MCP_RESOURCES=off)")
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "chatcli" {
		return RPCResourceContent{}, fmt.Errorf("unsupported resource uri (expected chatcli://…)")
	}
	// url.Parse maps chatcli://memory/index to Host="memory", Path="/index".
	kind := u.Host
	rest := strings.TrimPrefix(u.Path, "/")

	text, mime, err := cli.resolveResource(kind, rest, u.Query())
	if err != nil {
		return RPCResourceContent{}, err
	}
	return RPCResourceContent{URI: uri, MimeType: mime, Text: text}, nil
}

// resolveResource dispatches on the URI family. rest is the path after the
// family segment, already stripped of the leading slash (may be empty).
func (cli *ChatCLI) resolveResource(kind, rest string, query url.Values) (string, string, error) {
	switch kind {
	case "memory":
		return cli.resolveMemoryResource(rest)
	case "contexts":
		return cli.resolveContextResource(rest)
	case "knowledge":
		return cli.resolveKnowledgeResource(rest, query)
	case "skills":
		return cli.resolveSkillResource(rest)
	case "sessions":
		return cli.resolveSessionResource(rest)
	default:
		return "", "", fmt.Errorf("unknown resource family %q", kind)
	}
}

func (cli *ChatCLI) resolveMemoryResource(rest string) (string, string, error) {
	if cli.memoryStore == nil {
		return "", "", fmt.Errorf("memory store unavailable")
	}
	switch rest {
	case "index":
		return cli.memoryStore.GetMemoryIndex(memoryIndexResourceBudget), "text/plain", nil
	case "longterm":
		return cli.memoryStore.ReadLongTerm(), "text/markdown", nil
	case "profile":
		mgr := cli.memoryStore.Manager()
		if mgr == nil || mgr.Profile == nil {
			return "", "", fmt.Errorf("profile store unavailable")
		}
		b, err := json.MarshalIndent(mgr.Profile.Get(), "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(b), "application/json", nil
	case "projects":
		mgr := cli.memoryStore.Manager()
		if mgr == nil || mgr.Projects == nil {
			return "", "", fmt.Errorf("project tracker unavailable")
		}
		b, err := json.MarshalIndent(mgr.Projects.GetAll(), "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(b), "application/json", nil
	case "stats":
		mgr := cli.memoryStore.Manager()
		if mgr == nil {
			return "", "", fmt.Errorf("memory manager unavailable")
		}
		return mgr.FormatStats(), "text/plain", nil
	default:
		return "", "", fmt.Errorf("unknown memory resource %q (index, longterm, profile, projects, stats)", rest)
	}
}

func (cli *ChatCLI) resolveContextResource(rest string) (string, string, error) {
	if cli.contextHandler == nil {
		return "", "", fmt.Errorf("context manager unavailable")
	}
	mgr := cli.contextHandler.GetManager()
	if rest == "" {
		ctxs, err := mgr.ListContexts(nil)
		if err != nil {
			return "", "", err
		}
		type entry struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Mode        string   `json:"mode"`
			Files       int      `json:"files"`
			Tags        []string `json:"tags,omitempty"`
		}
		entries := make([]entry, 0, len(ctxs))
		for _, fc := range ctxs {
			entries = append(entries, entry{
				Name: fc.Name, Description: fc.Description,
				Mode: string(fc.Mode), Files: fc.FileCount, Tags: fc.Tags,
			})
		}
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(b), "application/json", nil
	}
	name, err := url.PathUnescape(rest)
	if err != nil {
		name = rest
	}
	content, err := mgr.RenderContext(name)
	if err != nil {
		return "", "", err
	}
	return content, "text/plain", nil
}

func (cli *ChatCLI) resolveKnowledgeResource(rest string, query url.Values) (string, string, error) {
	if cli.contextHandler == nil {
		return "", "", fmt.Errorf("context manager unavailable")
	}
	if rest == "" {
		return "", "", fmt.Errorf("knowledge base name is required (chatcli://knowledge/{kb})")
	}
	mgr := cli.contextHandler.GetManager()
	kbSeg, sourceSeg, hasSource := strings.Cut(rest, "/")
	kb, err := url.PathUnescape(kbSeg)
	if err != nil {
		kb = kbSeg
	}
	if !hasSource {
		toc, err := mgr.KnowledgeTOCByName(kb, "")
		if err != nil {
			return "", "", err
		}
		return toc, "text/plain", nil
	}
	source, err := url.PathUnescape(sourceSeg)
	if err != nil {
		source = sourceSeg
	}
	offset := 0
	if v := query.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	page, total, next, err := mgr.KnowledgeDocumentByName(kb, source, offset)
	if err != nil {
		return "", "", err
	}
	header := fmt.Sprintf("[%s :: %s — chars %d..%d of %d", kb, source, offset, offset+len(page), total)
	if next > 0 {
		header += fmt.Sprintf("; next offset=%d", next)
	}
	header += "]\n\n"
	return header + page, "text/plain", nil
}

func (cli *ChatCLI) resolveSkillResource(rest string) (string, string, error) {
	if cli.skillHandler == nil || cli.skillHandler.personaMgr == nil {
		return "", "", fmt.Errorf("skill manager unavailable")
	}
	if rest == "" {
		skills, err := cli.skillHandler.personaMgr.ListSkills()
		if err != nil {
			return "", "", err
		}
		type entry struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Triggers    []string `json:"triggers,omitempty"`
			Model       string   `json:"model,omitempty"`
		}
		entries := make([]entry, 0, len(skills))
		for _, s := range skills {
			entries = append(entries, entry{Name: s.Name, Description: s.Description, Triggers: []string(s.Triggers), Model: s.Model})
		}
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(b), "application/json", nil
	}
	name, err := url.PathUnescape(rest)
	if err != nil {
		name = rest
	}
	content, err := cli.SkillContentRPC(name)
	if err != nil {
		return "", "", err
	}
	return content, "text/markdown", nil
}

func (cli *ChatCLI) resolveSessionResource(rest string) (string, string, error) {
	if cli.sessionManager == nil {
		return "", "", fmt.Errorf("%s", "session store unavailable")
	}
	if rest == "" {
		names, err := cli.sessionManager.ListSessions()
		if err != nil {
			return "", "", err
		}
		return strings.Join(names, "\n"), "text/plain", nil
	}
	name, err := url.PathUnescape(rest)
	if err != nil {
		name = rest
	}
	if err := validateSessionName(name); err != nil {
		return "", "", err
	}
	sd, err := cli.sessionManager.LoadSessionV2(name)
	if err != nil {
		return "", "", err
	}
	b, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		return "", "", err
	}
	return string(b), "application/json", nil
}
