package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/mcp"
)

func TestBuildMCPToolChangeNotice(t *testing.T) {
	changes := []mcp.ToolListChange{
		{Server: "http-toolkit", Added: []string{"send_request", "list_intercepted"}},
		{Server: "other", Removed: []string{"stale_tool"}},
	}
	out := buildMCPToolChangeNotice(changes)

	for _, want := range []string{
		"[MCP TOOL CATALOG UPDATED]",
		`Server "http-toolkit": +2 new tool(s): mcp_send_request, mcp_list_intercepted.`,
		`Server "other": 1 removed (do NOT call these anymore): mcp_stale_tool.`,
		"@tools describe",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("notice missing %q in:\n%s", want, out)
		}
	}
	if buildMCPToolChangeNotice(nil) != "" {
		t.Error("empty batch must render nothing")
	}
}

func TestBuildMCPToolChangeNotice_CapsNameList(t *testing.T) {
	names := make([]string, mcpNoticeMaxNames+7)
	for i := range names {
		names[i] = fmt.Sprintf("tool_%02d", i)
	}
	out := buildMCPToolChangeNotice([]mcp.ToolListChange{{Server: "big", Added: names}})
	if !strings.Contains(out, "(+7 more)") {
		t.Errorf("overflow suffix missing in:\n%s", out)
	}
	if strings.Count(out, "mcp_tool_") != mcpNoticeMaxNames {
		t.Errorf("listed names must cap at %d", mcpNoticeMaxNames)
	}
}

func TestSummarizeMCPToolChanges(t *testing.T) {
	got := summarizeMCPToolChanges([]mcp.ToolListChange{
		{Server: "http-toolkit", Added: []string{"a", "b"}, Removed: []string{"c"}},
		{Server: "srv2", Added: []string{"x"}},
	})
	if got != "http-toolkit: +2/-1, srv2: +1/-0" {
		t.Errorf("summary = %q", got)
	}
}
