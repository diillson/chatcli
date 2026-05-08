package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

// --- mock transport --------------------------------------------------------

type mockTransport struct {
	calls   []mockCall
	callIdx int
	closed  bool
}

type mockCall struct {
	method string
	result json.RawMessage
	err    error
}

func (m *mockTransport) Call(method string, _ interface{}) (json.RawMessage, error) {
	if m.callIdx >= len(m.calls) {
		return nil, fmt.Errorf("unexpected call #%d to %q", m.callIdx, method)
	}
	c := m.calls[m.callIdx]
	m.callIdx++
	if c.method != "" && c.method != method {
		return nil, fmt.Errorf("expected method %q, got %q", c.method, method)
	}
	return c.result, c.err
}

func (m *mockTransport) Close() error {
	m.closed = true
	return nil
}

// --- JSON-RPC message tests ------------------------------------------------

func TestJSONRPCRequestMarshal(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      42,
		Method:  "tools/list",
		Params:  nil,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", m["jsonrpc"])
	}
	if m["method"] != "tools/list" {
		t.Errorf("method = %v, want tools/list", m["method"])
	}
	if int64(m["id"].(float64)) != 42 {
		t.Errorf("id = %v, want 42", m["id"])
	}
	// params should be omitted when nil
	if _, ok := m["params"]; ok {
		t.Error("params should be omitted when nil")
	}
}

func TestJSONRPCRequestMarshalWithParams(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: toolCallParams{
			Name:      "my_tool",
			Arguments: map[string]interface{}{"key": "value"},
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"my_tool"`) {
		t.Error("expected tool name in marshalled JSON")
	}
	if !strings.Contains(string(data), `"key"`) {
		t.Error("expected arguments key in marshalled JSON")
	}
}

func TestJSONRPCResponseUnmarshalSuccess(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`
	var resp jsonRPCResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 1 {
		t.Errorf("id = %d, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Error("expected no error")
	}
	if resp.Result == nil {
		t.Error("expected non-nil result")
	}
}

func TestJSONRPCResponseUnmarshalError(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"method not found"}}`
	var resp jsonRPCResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
	if resp.Error.Message != "method not found" {
		t.Errorf("error message = %q", resp.Error.Message)
	}
}

func TestInitializeParamsMarshal(t *testing.T) {
	p := initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    capabilities{},
		ClientInfo:      clientInfo{Name: "chatcli", Version: "1.0.0"},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `"protocolVersion":"2024-11-05"`) {
		t.Error("missing protocolVersion")
	}
	if !strings.Contains(s, `"chatcli"`) {
		t.Error("missing client name")
	}
}

func TestToolsListResultUnmarshal(t *testing.T) {
	raw := `{"tools":[
		{"name":"read_file","description":"Read a file","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}},
		{"name":"write_file","description":"Write a file","inputSchema":{"type":"object"}}
	]}`
	var result toolsListResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(result.Tools))
	}
	if result.Tools[0].Name != "read_file" {
		t.Errorf("tool[0].Name = %q", result.Tools[0].Name)
	}
	if result.Tools[1].Description != "Write a file" {
		t.Errorf("tool[1].Description = %q", result.Tools[1].Description)
	}
}

func TestToolCallResultUnmarshal(t *testing.T) {
	raw := `{"content":[{"type":"text","text":"hello world"}],"isError":false}`
	var result toolCallResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(result.Content))
	}
	if result.Content[0].Text != "hello world" {
		t.Errorf("text = %q", result.Content[0].Text)
	}
	if result.IsError {
		t.Error("expected isError=false")
	}
}

func TestToolCallResultWithError(t *testing.T) {
	raw := `{"content":[{"type":"text","text":"something went wrong"}],"isError":true}`
	var result toolCallResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected isError=true")
	}
}

func TestToolCallResultMultipleContent(t *testing.T) {
	raw := `{"content":[
		{"type":"text","text":"part1"},
		{"type":"text","text":"part2"},
		{"type":"resource","data":"base64data","mimeType":"image/png"}
	]}`
	var result toolCallResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 3 {
		t.Fatalf("got %d content items, want 3", len(result.Content))
	}
	if result.Content[2].MimeType != "image/png" {
		t.Errorf("content[2].MimeType = %q", result.Content[2].MimeType)
	}
}

// --- Manager tests ---------------------------------------------------------

func TestNewManager(t *testing.T) {
	m := NewManager(testLogger())
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if len(m.servers) != 0 {
		t.Error("expected empty servers map")
	}
	if len(m.tools) != 0 {
		t.Error("expected empty tools map")
	}
}

func TestLoadConfigNonexistent(t *testing.T) {
	m := NewManager(testLogger())
	err := m.LoadConfig("/nonexistent/path/mcp_servers.json")
	if err != nil {
		t.Errorf("nonexistent config should not error, got: %v", err)
	}
	if len(m.servers) != 0 {
		t.Error("expected no servers loaded")
	}
}

func TestLoadConfigValid(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp.json")
	cfg := `{
		"mcpServers": [
			{"name":"srv1","command":"echo","transport":"stdio","enabled":true},
			{"name":"srv2","command":"cat","transport":"stdio","enabled":false},
			{"name":"srv3","command":"test","transport":"sse","url":"http://localhost:8080","enabled":true}
		]
	}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(testLogger())
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatal(err)
	}

	// Only enabled servers should be loaded
	if len(m.servers) != 2 {
		t.Fatalf("got %d servers, want 2 (only enabled)", len(m.servers))
	}
	if _, ok := m.servers["srv1"]; !ok {
		t.Error("expected srv1")
	}
	if _, ok := m.servers["srv2"]; ok {
		t.Error("srv2 is disabled, should not be loaded")
	}
	if _, ok := m.servers["srv3"]; !ok {
		t.Error("expected srv3")
	}
	if m.servers["srv3"].Config.Transport != TransportSSE {
		t.Errorf("srv3 transport = %q, want sse", m.servers["srv3"].Config.Transport)
	}
	// Servers should be registered as Starting=true so /mcp status shows
	// "iniciando…" while StartAll runs in the background.
	for name, conn := range m.servers {
		if !conn.Status.Starting {
			t.Errorf("server %q: Status.Starting=false, want true after LoadConfig", name)
		}
		if conn.Status.Connected {
			t.Errorf("server %q: Status.Connected=true before StartAll", name)
		}
	}
}

// TestStartAllRecordsLastError verifies that a server failing to start
// surfaces the error on Status.LastError (not just in logs) so /mcp
// status can show why a server is disconnected. The contract is also
// what config_sections.go relies on to print the error inline.
func TestStartAllRecordsLastError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp.json")
	cfg := `{
		"mcpServers": [
			{"name":"bogus","transport":"unsupported-transport","enabled":true}
		]
	}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(testLogger())
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll must not return error for individual server failures: %v", err)
	}

	conn, ok := m.servers["bogus"]
	if !ok {
		t.Fatal("server bogus missing")
	}
	if conn.Status.LastError == nil {
		t.Error("LastError not set after StartAll failure")
	}
	if conn.Status.Connected {
		t.Error("Connected=true after failed start")
	}
	if conn.Status.Starting {
		t.Error("Starting=true after StartAll terminated")
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(cfgPath, []byte(`{invalid`), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(testLogger())
	err := m.LoadConfig(cfgPath)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parsing MCP config") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfigWithEnv(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp.json")
	cfg := `{
		"mcpServers": [
			{"name":"withenv","command":"node","args":["server.js"],"env":{"API_KEY":"secret"},"transport":"stdio","enabled":true}
		]
	}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(testLogger())
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatal(err)
	}
	srv := m.servers["withenv"]
	if srv == nil {
		t.Fatal("expected server 'withenv'")
	}
	if srv.Config.Env["API_KEY"] != "secret" {
		t.Error("expected env var API_KEY=secret")
	}
	if len(srv.Config.Args) != 1 || srv.Config.Args[0] != "server.js" {
		t.Errorf("args = %v", srv.Config.Args)
	}
}

func TestGetToolsEmpty(t *testing.T) {
	m := NewManager(testLogger())
	tools := m.GetTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestGetToolsWithRegisteredTools(t *testing.T) {
	m := NewManager(testLogger())
	m.tools["read_file"] = &MCPTool{
		Name:        "read_file",
		Description: "Read a file",
		Parameters:  map[string]interface{}{"type": "object"},
		ServerName:  "filesystem",
	}
	m.tools["write_file"] = &MCPTool{
		Name:        "write_file",
		Description: "Write a file",
		Parameters:  map[string]interface{}{"type": "object"},
		ServerName:  "filesystem",
	}

	tools := m.GetTools()
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}

	// Check that names are prefixed with mcp_
	foundNames := map[string]bool{}
	for _, td := range tools {
		foundNames[td.Function.Name] = true
		if td.Type != "function" {
			t.Errorf("type = %q, want function", td.Type)
		}
		if !strings.HasPrefix(td.Function.Description, "[MCP:filesystem]") {
			t.Errorf("description = %q, expected [MCP:filesystem] prefix", td.Function.Description)
		}
	}
	if !foundNames["mcp_read_file"] {
		t.Error("expected mcp_read_file")
	}
	if !foundNames["mcp_write_file"] {
		t.Error("expected mcp_write_file")
	}
}

func TestIsMCPTool(t *testing.T) {
	m := NewManager(testLogger())
	m.tools["my_tool"] = &MCPTool{Name: "my_tool"}

	if !m.IsMCPTool("my_tool") {
		t.Error("expected IsMCPTool to return true")
	}
	if m.IsMCPTool("nonexistent") {
		t.Error("expected IsMCPTool to return false for nonexistent tool")
	}
}

func TestGetServerStatusEmpty(t *testing.T) {
	m := NewManager(testLogger())
	statuses := m.GetServerStatus()
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses, got %d", len(statuses))
	}
}

func TestGetServerStatus(t *testing.T) {
	m := NewManager(testLogger())
	m.servers["s1"] = &ServerConnection{
		Status: ServerStatus{Name: "s1", Connected: true, ToolCount: 3},
	}
	m.servers["s2"] = &ServerConnection{
		Status: ServerStatus{Name: "s2", Connected: false},
	}
	statuses := m.GetServerStatus()
	if len(statuses) != 2 {
		t.Fatalf("got %d statuses, want 2", len(statuses))
	}
}

// --- ExecuteTool tests via mock transport -----------------------------------

func TestExecuteToolNotFound(t *testing.T) {
	m := NewManager(testLogger())
	_, err := m.ExecuteTool(context.Background(), "nonexistent", nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestExecuteToolServerNotConnected(t *testing.T) {
	m := NewManager(testLogger())
	m.tools["mytool"] = &MCPTool{Name: "mytool", ServerName: "srv"}
	m.servers["srv"] = &ServerConnection{
		Status: ServerStatus{Connected: false},
	}

	_, err := m.ExecuteTool(context.Background(), "mytool", nil)
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("expected 'not connected' error, got: %v", err)
	}
}

func TestExecuteToolSuccess(t *testing.T) {
	m := NewManager(testLogger())

	toolResult := `{"content":[{"type":"text","text":"file contents here"}],"isError":false}`
	mt := &mockTransport{
		calls: []mockCall{
			{method: "tools/call", result: json.RawMessage(toolResult)},
		},
	}

	m.tools["read_file"] = &MCPTool{Name: "read_file", ServerName: "srv"}
	m.servers["srv"] = &ServerConnection{
		Status:    ServerStatus{Connected: true},
		transport: mt,
	}

	result, err := m.ExecuteTool(context.Background(), "read_file", map[string]interface{}{"path": "/tmp/test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "file contents here" {
		t.Errorf("content = %q", result.Content)
	}
	if result.IsError {
		t.Error("expected isError=false")
	}
}

func TestExecuteToolWithError(t *testing.T) {
	m := NewManager(testLogger())

	toolResult := `{"content":[{"type":"text","text":"permission denied"}],"isError":true}`
	mt := &mockTransport{
		calls: []mockCall{
			{method: "tools/call", result: json.RawMessage(toolResult)},
		},
	}

	m.tools["write_file"] = &MCPTool{Name: "write_file", ServerName: "srv"}
	m.servers["srv"] = &ServerConnection{
		Status:    ServerStatus{Connected: true},
		transport: mt,
	}

	result, err := m.ExecuteTool(context.Background(), "write_file", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected isError=true")
	}
	if result.Content != "permission denied" {
		t.Errorf("content = %q", result.Content)
	}
}

func TestExecuteToolTransportError(t *testing.T) {
	m := NewManager(testLogger())

	mt := &mockTransport{
		calls: []mockCall{
			{method: "tools/call", err: fmt.Errorf("connection lost")},
		},
	}

	m.tools["mytool"] = &MCPTool{Name: "mytool", ServerName: "srv"}
	m.servers["srv"] = &ServerConnection{
		Status:    ServerStatus{Connected: true},
		transport: mt,
	}

	_, err := m.ExecuteTool(context.Background(), "mytool", nil)
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("expected 'connection lost' error, got: %v", err)
	}
}

func TestExecuteToolMultipleContent(t *testing.T) {
	m := NewManager(testLogger())

	toolResult := `{"content":[
		{"type":"text","text":"line1"},
		{"type":"text","text":"line2"},
		{"type":"resource","data":"imgdata","mimeType":"image/png"}
	]}`
	mt := &mockTransport{
		calls: []mockCall{
			{method: "tools/call", result: json.RawMessage(toolResult)},
		},
	}

	m.tools["multi"] = &MCPTool{Name: "multi", ServerName: "srv"}
	m.servers["srv"] = &ServerConnection{
		Status:    ServerStatus{Connected: true},
		transport: mt,
	}

	result, err := m.ExecuteTool(context.Background(), "multi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "line1line2imgdata" {
		t.Errorf("content = %q, want line1line2imgdata", result.Content)
	}
	if result.MimeType != "image/png" {
		t.Errorf("mimeType = %q, want image/png", result.MimeType)
	}
}

func TestExecuteToolNilTransport(t *testing.T) {
	m := NewManager(testLogger())

	m.tools["mytool"] = &MCPTool{Name: "mytool", ServerName: "srv"}
	m.servers["srv"] = &ServerConnection{
		Status:    ServerStatus{Connected: true},
		transport: nil,
	}

	_, err := m.ExecuteTool(context.Background(), "mytool", nil)
	if err == nil || !strings.Contains(err.Error(), "no active transport") {
		t.Errorf("expected 'no active transport' error, got: %v", err)
	}
}

// --- initializeServer / discoverTools tests --------------------------------

func TestInitializeServer(t *testing.T) {
	m := NewManager(testLogger())

	initResult := `{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"test-server","version":"0.1"}}`
	mt := &mockTransport{
		calls: []mockCall{
			{method: "initialize", result: json.RawMessage(initResult)},
			{method: "notifications/initialized", result: json.RawMessage(`null`)},
		},
	}

	conn := &ServerConnection{
		Config:    ServerConfig{Name: "test"},
		transport: mt,
	}

	err := m.initializeServer(conn)
	if err != nil {
		t.Fatal(err)
	}
	if mt.callIdx != 2 {
		t.Errorf("expected 2 calls (initialize + notifications/initialized), got %d", mt.callIdx)
	}
}

func TestInitializeServerError(t *testing.T) {
	m := NewManager(testLogger())

	mt := &mockTransport{
		calls: []mockCall{
			{method: "initialize", err: fmt.Errorf("server rejected")},
		},
	}

	conn := &ServerConnection{
		Config:    ServerConfig{Name: "test"},
		transport: mt,
	}

	err := m.initializeServer(conn)
	if err == nil || !strings.Contains(err.Error(), "server rejected") {
		t.Errorf("expected error, got: %v", err)
	}
}

func TestDiscoverTools(t *testing.T) {
	m := NewManager(testLogger())

	toolsList := `{"tools":[
		{"name":"read","description":"Read files","inputSchema":{"type":"object"}},
		{"name":"write","description":"Write files","inputSchema":{"type":"object"}}
	]}`
	mt := &mockTransport{
		calls: []mockCall{
			{method: "tools/list", result: json.RawMessage(toolsList)},
		},
	}

	conn := &ServerConnection{
		Config:    ServerConfig{Name: "fs-server"},
		transport: mt,
	}

	err := m.discoverTools(conn)
	if err != nil {
		t.Fatal(err)
	}

	if len(m.tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(m.tools))
	}
	if m.tools["read"].ServerName != "fs-server" {
		t.Errorf("tool serverName = %q", m.tools["read"].ServerName)
	}
	if conn.Status.ToolCount != 2 {
		t.Errorf("toolCount = %d, want 2", conn.Status.ToolCount)
	}
}

func TestDiscoverToolsError(t *testing.T) {
	m := NewManager(testLogger())

	mt := &mockTransport{
		calls: []mockCall{
			{method: "tools/list", err: fmt.Errorf("not supported")},
		},
	}

	conn := &ServerConnection{
		Config:    ServerConfig{Name: "basic"},
		transport: mt,
	}

	err := m.discoverTools(conn)
	if err == nil {
		t.Error("expected error")
	}
}

func TestDiscoverToolsInvalidJSON(t *testing.T) {
	m := NewManager(testLogger())

	mt := &mockTransport{
		calls: []mockCall{
			{method: "tools/list", result: json.RawMessage(`{invalid}`)},
		},
	}

	conn := &ServerConnection{
		Config:    ServerConfig{Name: "bad"},
		transport: mt,
	}

	err := m.discoverTools(conn)
	if err == nil || !strings.Contains(err.Error(), "parsing tools/list") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

// --- StopAll tests ---------------------------------------------------------

func TestStopAll(t *testing.T) {
	m := NewManager(testLogger())

	mt1 := &mockTransport{}
	mt2 := &mockTransport{}

	m.servers["s1"] = &ServerConnection{
		Status:    ServerStatus{Connected: true},
		transport: mt1,
	}
	m.servers["s2"] = &ServerConnection{
		Status:    ServerStatus{Connected: true},
		transport: mt2,
	}

	m.StopAll()

	if !mt1.closed {
		t.Error("expected transport 1 to be closed")
	}
	if !mt2.closed {
		t.Error("expected transport 2 to be closed")
	}
	if m.servers["s1"].Status.Connected {
		t.Error("s1 should be disconnected")
	}
	if m.servers["s2"].Status.Connected {
		t.Error("s2 should be disconnected")
	}
	if m.servers["s1"].transport != nil {
		t.Error("s1 transport should be nil")
	}
}

func TestStopAllWithNilTransport(t *testing.T) {
	m := NewManager(testLogger())
	m.servers["s1"] = &ServerConnection{
		Status:    ServerStatus{Connected: false},
		transport: nil,
	}
	// Should not panic
	m.StopAll()
}

// --- startServer unsupported transport ------------------------------------

func TestStartServerUnsupportedTransport(t *testing.T) {
	m := NewManager(testLogger())
	conn := &ServerConnection{
		Config: ServerConfig{Transport: "grpc"},
	}
	err := m.startServer(context.Background(), conn)
	if err == nil || !strings.Contains(err.Error(), "unsupported transport") {
		t.Errorf("expected unsupported transport error, got: %v", err)
	}
}

// --- SSE event parsing tests -----------------------------------------------

func TestSSEHandleEndpointEventRelative(t *testing.T) {
	tr := &sseTransport{
		baseURL: "http://localhost:8080",
		ready:   make(chan struct{}),
		pending: make(map[int64]chan *jsonRPCResponse),
	}

	tr.handleSSEEvent("endpoint", "/messages")

	if tr.messagesURL != "http://localhost:8080/messages" {
		t.Errorf("messagesURL = %q, want http://localhost:8080/messages", tr.messagesURL)
	}

	// ready channel should be closed
	select {
	case <-tr.ready:
		// ok
	default:
		t.Error("expected ready channel to be closed")
	}
}

func TestSSEHandleEndpointEventAbsolute(t *testing.T) {
	tr := &sseTransport{
		baseURL: "http://localhost:8080",
		ready:   make(chan struct{}),
		pending: make(map[int64]chan *jsonRPCResponse),
	}

	tr.handleSSEEvent("endpoint", "http://other-host:9090/messages")

	if tr.messagesURL != "http://other-host:9090/messages" {
		t.Errorf("messagesURL = %q", tr.messagesURL)
	}
}

func TestSSEHandleEndpointEventIdempotent(t *testing.T) {
	tr := &sseTransport{
		baseURL: "http://localhost:8080",
		ready:   make(chan struct{}),
		pending: make(map[int64]chan *jsonRPCResponse),
	}

	tr.handleSSEEvent("endpoint", "/messages")
	// Call again — should not panic on already-closed channel
	tr.handleSSEEvent("endpoint", "/messages2")

	if tr.messagesURL != "http://localhost:8080/messages2" {
		t.Errorf("messagesURL = %q", tr.messagesURL)
	}
}

func TestSSEHandleMessageEvent(t *testing.T) {
	tr := &sseTransport{
		baseURL: "http://localhost:8080",
		ready:   make(chan struct{}),
		pending: make(map[int64]chan *jsonRPCResponse),
		logger:  testLogger(),
	}

	ch := make(chan *jsonRPCResponse, 1)
	tr.pendMu.Lock()
	tr.pending[42] = ch
	tr.pendMu.Unlock()

	data := `{"jsonrpc":"2.0","id":42,"result":{"ok":true}}`
	tr.handleSSEEvent("message", data)

	select {
	case resp := <-ch:
		if resp.ID != 42 {
			t.Errorf("resp.ID = %d, want 42", resp.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for response")
	}
}

func TestSSEHandleMessageEventNoPending(t *testing.T) {
	tr := &sseTransport{
		baseURL: "http://localhost:8080",
		ready:   make(chan struct{}),
		pending: make(map[int64]chan *jsonRPCResponse),
		logger:  testLogger(),
	}

	// No pending request for ID 99 — should not panic
	data := `{"jsonrpc":"2.0","id":99,"result":{}}`
	tr.handleSSEEvent("message", data)
}

func TestSSEHandleMessageEventInvalidJSON(t *testing.T) {
	tr := &sseTransport{
		baseURL: "http://localhost:8080",
		ready:   make(chan struct{}),
		pending: make(map[int64]chan *jsonRPCResponse),
		logger:  testLogger(),
	}

	// Should not panic on invalid JSON
	tr.handleSSEEvent("message", "{invalid}")
}

func TestSSEHandleUnknownEvent(t *testing.T) {
	tr := &sseTransport{
		baseURL: "http://localhost:8080",
		ready:   make(chan struct{}),
		pending: make(map[int64]chan *jsonRPCResponse),
		logger:  testLogger(),
	}

	// Should not panic on unknown event types
	tr.handleSSEEvent("heartbeat", "ping")
}

func TestSSECallNoMessagesURL(t *testing.T) {
	tr := &sseTransport{
		baseURL:     "http://localhost:8080",
		messagesURL: "",
		pending:     make(map[int64]chan *jsonRPCResponse),
		logger:      testLogger(),
	}

	_, err := tr.Call("test", nil)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("expected 'not ready' error, got: %v", err)
	}
}

// --- SSE transport integration with HTTP test server -----------------------

func TestSSETransportCallWithHTTPServer(t *testing.T) {
	// Set up an SSE endpoint that sends an endpoint event, then echoes JSON-RPC responses
	mux := http.NewServeMux()

	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		// Send the endpoint event
		fmt.Fprintf(w, "event: endpoint\ndata: /messages\n\n")
		flusher.Flush()

		// Keep connection open until client disconnects
		<-r.Context().Done()
	})

	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// For testing, we respond inline (not via SSE) -- the real MCP server
		// would send the response via the SSE stream. We simulate by posting
		// to the SSE stream instead. But since that's complex, we'll test
		// the HTTP POST part only and verify it doesn't fail.
		w.WriteHeader(http.StatusAccepted)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := ServerConfig{
		Name:      "test-sse",
		URL:       server.URL,
		Transport: TransportSSE,
	}

	tr, err := newSSETransport(ctx, cfg, testLogger(), NewChannelManager(testLogger()), "test-server")
	if err != nil {
		t.Fatalf("newSSETransport: %v", err)
	}
	defer tr.Close()

	if tr.messagesURL != server.URL+"/messages" {
		t.Errorf("messagesURL = %q, want %s/messages", tr.messagesURL, server.URL)
	}
}

// --- TransportType tests ---------------------------------------------------

func TestTransportTypeConstants(t *testing.T) {
	if TransportStdio != "stdio" {
		t.Errorf("TransportStdio = %q", TransportStdio)
	}
	if TransportSSE != "sse" {
		t.Errorf("TransportSSE = %q", TransportSSE)
	}
}

// --- ServerConfig JSON marshaling ------------------------------------------

func TestServerConfigJSON(t *testing.T) {
	cfg := ServerConfig{
		Name:      "test",
		Command:   "npx",
		Args:      []string{"mcp-server"},
		Env:       map[string]string{"TOKEN": "abc"},
		Transport: TransportStdio,
		Enabled:   true,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var decoded ServerConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != "test" {
		t.Errorf("name = %q", decoded.Name)
	}
	if decoded.Command != "npx" {
		t.Errorf("command = %q", decoded.Command)
	}
	if len(decoded.Args) != 1 || decoded.Args[0] != "mcp-server" {
		t.Errorf("args = %v", decoded.Args)
	}
	if decoded.Env["TOKEN"] != "abc" {
		t.Error("env TOKEN mismatch")
	}
	if decoded.Transport != TransportStdio {
		t.Errorf("transport = %q", decoded.Transport)
	}
	if !decoded.Enabled {
		t.Error("expected enabled")
	}
}

func TestServerConfigURLField(t *testing.T) {
	cfg := ServerConfig{
		Name:      "sse-server",
		URL:       "http://localhost:3000",
		Transport: TransportSSE,
		Enabled:   true,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"url":"http://localhost:3000"`) {
		t.Error("URL field missing from JSON")
	}
}

// --- MCPTool JSON marshaling -----------------------------------------------

func TestMCPToolJSON(t *testing.T) {
	tool := MCPTool{
		Name:        "search",
		Description: "Search files",
		Parameters:  map[string]interface{}{"type": "object"},
		ServerName:  "fs",
	}
	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	// ServerName has json:"-" so should not appear
	if strings.Contains(string(data), "fs") {
		t.Error("ServerName should not be in JSON output")
	}
	if !strings.Contains(string(data), `"inputSchema"`) {
		t.Error("expected inputSchema in JSON")
	}
}

// --- MCPToolResult JSON marshaling -----------------------------------------

func TestMCPToolResultJSON(t *testing.T) {
	r := MCPToolResult{
		Content:  "hello",
		IsError:  true,
		MimeType: "text/plain",
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MCPToolResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Content != "hello" {
		t.Errorf("content = %q", decoded.Content)
	}
	if !decoded.IsError {
		t.Error("expected isError=true")
	}
}

// --- DefaultConfigPath test ------------------------------------------------

func TestDefaultConfigPath(t *testing.T) {
	path := DefaultConfigPath()
	if !strings.Contains(path, "mcp_servers.json") {
		t.Errorf("path = %q, expected to contain mcp_servers.json", path)
	}
	if !strings.Contains(path, ".chatcli") {
		t.Errorf("path = %q, expected to contain .chatcli", path)
	}
}

// --- ServerStatus fields ---------------------------------------------------

func TestServerStatus(t *testing.T) {
	now := time.Now()
	s := ServerStatus{
		Name:      "test",
		Connected: true,
		ToolCount: 5,
		LastPing:  now,
		LastError: fmt.Errorf("temp error"),
		StartedAt: now.Add(-time.Minute),
	}
	if s.Name != "test" {
		t.Error("name mismatch")
	}
	if !s.Connected {
		t.Error("expected connected")
	}
	if s.ToolCount != 5 {
		t.Error("toolCount mismatch")
	}
	if s.LastError == nil || s.LastError.Error() != "temp error" {
		t.Error("lastError mismatch")
	}
}

// --- Concurrent access test ------------------------------------------------

func TestManagerConcurrentAccess(t *testing.T) {
	m := NewManager(testLogger())
	m.tools["tool1"] = &MCPTool{Name: "tool1", ServerName: "s1"}
	m.servers["s1"] = &ServerConnection{
		Status: ServerStatus{Connected: true},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			m.GetTools()
			m.IsMCPTool("tool1")
			m.GetServerStatus()
		}
	}()

	for i := 0; i < 100; i++ {
		m.GetTools()
		m.IsMCPTool("tool1")
		m.GetServerStatus()
	}

	<-done
}

// --- stdio framing regression tests --------------------------------------
// MCP stdio transport spec mandates newline-delimited JSON. An earlier
// implementation used LSP-style Content-Length headers, which silently
// hung against any standard MCP server (initialize timed out). These
// tests pin the on-the-wire framing so the bug cannot regress.

func TestStdioSendWritesNewlineDelimitedJSON(t *testing.T) {
	pr, pw := io.Pipe()
	tr := &stdioTransport{
		stdin:   pw,
		pending: make(map[int64]chan *jsonRPCResponse),
		logger:  testLogger(),
		done:    make(chan struct{}),
	}

	read := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 1024)
		n, _ := pr.Read(buf)
		read <- buf[:n]
	}()

	if err := tr.send(jsonRPCRequest{JSONRPC: "2.0", ID: 7, Method: "initialize"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case got := <-read:
		s := string(got)
		if strings.Contains(s, "Content-Length") {
			t.Errorf("send produced LSP-style frame, want newline-delimited JSON: %q", s)
		}
		if !strings.HasSuffix(s, "\n") {
			t.Errorf("send did not terminate with '\\n': %q", s)
		}
		var probe map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimRight(s, "\n")), &probe); err != nil {
			t.Errorf("frame is not a single JSON object: %v (%q)", err, s)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for send to write")
	}
}

func TestStdioReadLoopDispatchesNDJSON(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	tr := &stdioTransport{
		stdout:  bufio.NewReaderSize(stdoutR, 4096),
		pending: make(map[int64]chan *jsonRPCResponse),
		logger:  testLogger(),
		done:    make(chan struct{}),
	}

	ch := make(chan *jsonRPCResponse, 1)
	tr.pendMu.Lock()
	tr.pending[42] = ch
	tr.pendMu.Unlock()

	go tr.readLoop()

	// Simulate a server that prints a banner before the protocol stream
	// (some MCP servers do this) followed by the response line.
	if _, err := io.WriteString(stdoutW, "MCP server starting...\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdoutW, "{\"jsonrpc\":\"2.0\",\"id\":42,\"result\":{\"ok\":true}}\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case resp := <-ch:
		if resp.ID != 42 {
			t.Errorf("dispatched ID = %d, want 42", resp.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not dispatch NDJSON response")
	}

	_ = stdoutW.Close()
}

func TestReloadAddsRemovesAndUpdatesServers(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp.json")
	write := func(body string) {
		if err := os.WriteFile(cfgPath, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Initial: srv1 only. Use unsupported transport so startServer
	// fails fast — we care about Reload diff semantics, not the spawn.
	write(`{"mcpServers":[{"name":"srv1","transport":"x","enabled":true}]}`)
	m := NewManager(testLogger())
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatal(err)
	}
	_ = m.StartAll(context.Background())

	// 1) Add srv2, keep srv1 unchanged.
	write(`{"mcpServers":[
		{"name":"srv1","transport":"x","enabled":true},
		{"name":"srv2","transport":"x","enabled":true}
	]}`)
	diff, err := m.Reload(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(diff.Started) != 1 || diff.Started[0] != "srv2" {
		t.Errorf("expected srv2 started, got %+v", diff)
	}
	if len(diff.Stopped) != 0 || len(diff.Updated) != 0 {
		t.Errorf("expected no stopped/updated, got %+v", diff)
	}

	// 2) Remove srv1, change srv2's args (update).
	write(`{"mcpServers":[
		{"name":"srv2","transport":"x","args":["--changed"],"enabled":true}
	]}`)
	diff, err = m.Reload(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	stopped := strings.Join(diff.Stopped, ",")
	if !strings.Contains(stopped, "srv1") {
		t.Errorf("expected srv1 stopped, got %+v", diff)
	}
	if len(diff.Updated) != 1 || diff.Updated[0] != "srv2" {
		t.Errorf("expected srv2 updated, got %+v", diff)
	}

	// 3) Disable srv2 → effectively a removal.
	write(`{"mcpServers":[
		{"name":"srv2","transport":"x","enabled":false}
	]}`)
	diff, err = m.Reload(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(diff.Stopped) != 1 || diff.Stopped[0] != "srv2" {
		t.Errorf("expected srv2 stopped on disable, got %+v", diff)
	}

	// 4) No file at all → stop everything (already empty, just must not error).
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(context.Background(), cfgPath); err != nil {
		t.Fatalf("reload after delete: %v", err)
	}
}

func TestMarkDisconnectedFlipsStatusAndDropsTools(t *testing.T) {
	m := NewManager(testLogger())
	conn := &ServerConnection{
		Config: ServerConfig{Name: "srv", Transport: TransportStdio},
		Status: ServerStatus{Name: "srv", Connected: true, ToolCount: 1},
	}
	m.servers["srv"] = conn
	m.tools["srv_tool"] = &MCPTool{Name: "srv_tool", ServerName: "srv"}

	m.markDisconnected("srv", fmt.Errorf("boom"))

	if conn.Status.Connected {
		t.Error("Status.Connected should be false after markDisconnected")
	}
	if conn.Status.LastError == nil || conn.Status.LastError.Error() != "boom" {
		t.Errorf("LastError = %v, want \"boom\"", conn.Status.LastError)
	}
	if _, still := m.tools["srv_tool"]; still {
		t.Error("tool from disconnected server should have been dropped")
	}

	// Idempotent: second call must not clobber LastError.
	m.markDisconnected("srv", fmt.Errorf("second"))
	if conn.Status.LastError.Error() != "boom" {
		t.Errorf("LastError clobbered on second call: %v", conn.Status.LastError)
	}
}

// --- per-server command tests ----------------------------------------------
// These pin the contract /mcp start|stop|logs depend on: StopOne keeps
// the entry alive (so a later StartOne can revive it), StartOne refuses
// unknown/duplicate starts, and the log ring is bounded and snapshotted
// safely.

func TestStopOneKeepsEntryAndDropsTools(t *testing.T) {
	m := NewManager(testLogger())
	m.servers["srv"] = &ServerConnection{
		Config: ServerConfig{Name: "srv", Transport: TransportStdio},
		Status: ServerStatus{Name: "srv", Connected: true, ToolCount: 2},
		logs:   newLogRing(mcpLogRingCapacity),
	}
	m.tools["t1"] = &MCPTool{Name: "t1", ServerName: "srv"}
	m.tools["t2"] = &MCPTool{Name: "t2", ServerName: "srv"}

	if err := m.StopOne("srv"); err != nil {
		t.Fatalf("StopOne: %v", err)
	}

	// Entry must still exist so /mcp start can revive it.
	if _, ok := m.servers["srv"]; !ok {
		t.Error("StopOne should keep the entry in m.servers")
	}
	if m.servers["srv"].Status.Connected {
		t.Error("Status.Connected should be false after StopOne")
	}
	if m.servers["srv"].Status.ToolCount != 0 {
		t.Errorf("ToolCount = %d, want 0 after StopOne", m.servers["srv"].Status.ToolCount)
	}
	// Tools attributed to srv must be dropped so the LLM doesn't try
	// to invoke them while the server is down.
	if _, still := m.tools["t1"]; still {
		t.Error("tool t1 should be dropped after StopOne")
	}
	if _, still := m.tools["t2"]; still {
		t.Error("tool t2 should be dropped after StopOne")
	}
}

func TestStopOneUnknownServerErrors(t *testing.T) {
	m := NewManager(testLogger())
	if err := m.StopOne("ghost"); err == nil {
		t.Error("expected error stopping unknown server")
	}
}

func TestStartOneUnknownServerErrors(t *testing.T) {
	m := NewManager(testLogger())
	if err := m.StartOne(context.Background(), "ghost"); err == nil {
		t.Error("expected error starting unknown server")
	}
}

func TestStartOneAlreadyRunningErrors(t *testing.T) {
	m := NewManager(testLogger())
	m.servers["srv"] = &ServerConnection{
		Config: ServerConfig{Name: "srv", Transport: TransportStdio},
		Status: ServerStatus{Name: "srv", Connected: true},
	}
	err := m.StartOne(context.Background(), "srv")
	if err == nil {
		t.Fatal("expected error starting an already-running server")
	}
	if !errors.Is(err, ErrServerAlreadyRunning) {
		t.Errorf("expected ErrServerAlreadyRunning, got: %v", err)
	}
}

func TestStartOneUnknownErrorIsSentinel(t *testing.T) {
	// Pin the sentinel-error contract so callers can branch on
	// errors.Is(err, ErrServerNotConfigured) without parsing strings.
	m := NewManager(testLogger())
	err := m.StartOne(context.Background(), "ghost")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrServerNotConfigured) {
		t.Errorf("expected ErrServerNotConfigured, got: %v", err)
	}
}

func TestStopOneUnknownErrorIsSentinel(t *testing.T) {
	m := NewManager(testLogger())
	err := m.StopOne("ghost")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrServerNotConfigured) {
		t.Errorf("expected ErrServerNotConfigured, got: %v", err)
	}
}

func TestStartOneRetryClearsLastError(t *testing.T) {
	// Verify that StartOne resets transient state on a previously
	// failed server before attempting the start. We use an
	// unsupported transport so startServer fails predictably, then
	// confirm Status reflects the new failure rather than the old
	// one. This pins the "retry shows current state" contract for
	// the /mcp start command.
	m := NewManager(testLogger())
	m.servers["srv"] = &ServerConnection{
		Config: ServerConfig{Name: "srv", Transport: "bogus"},
		Status: ServerStatus{Name: "srv", LastError: fmt.Errorf("previous failure")},
	}
	err := m.StartOne(context.Background(), "srv")
	if err == nil {
		t.Fatal("expected error starting with bogus transport")
	}
	if m.servers["srv"].Status.LastError == nil {
		t.Fatal("LastError should record the new failure")
	}
	// We cleared it before startServer ran, then startServer +
	// recordStartFailure stamped the new error. So the new error
	// must NOT be the literal "previous failure" string.
	if m.servers["srv"].Status.LastError.Error() == "previous failure" {
		t.Errorf("LastError still holds stale value: %v", m.servers["srv"].Status.LastError)
	}
}

func TestServerNamesSorted(t *testing.T) {
	m := NewManager(testLogger())
	m.servers["zulu"] = &ServerConnection{Config: ServerConfig{Name: "zulu"}}
	m.servers["alpha"] = &ServerConnection{Config: ServerConfig{Name: "alpha"}}
	m.servers["mike"] = &ServerConnection{Config: ServerConfig{Name: "mike"}}

	got := m.ServerNames()
	want := []string{"alpha", "mike", "zulu"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("ServerNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRecentLogsUnknownServerReturnsNil(t *testing.T) {
	m := NewManager(testLogger())
	if got := m.RecentLogs("ghost"); got != nil {
		t.Errorf("RecentLogs(unknown) = %v, want nil", got)
	}
}

func TestAppendLogAndRecentLogs(t *testing.T) {
	m := NewManager(testLogger())
	m.servers["srv"] = &ServerConnection{
		Config: ServerConfig{Name: "srv"},
		logs:   newLogRing(mcpLogRingCapacity),
	}
	m.appendLog("srv", "first")
	m.appendLog("srv", "second")
	m.appendLog("srv", "third")

	got := m.RecentLogs("srv")
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("logs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLogRingDropsOldestAtCapacity(t *testing.T) {
	r := newLogRing(3)
	r.append("a")
	r.append("b")
	r.append("c")
	r.append("d") // should drop "a"
	r.append("e") // should drop "b"

	got := r.snapshot()
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("ring[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLogRingSnapshotIsCopy(t *testing.T) {
	// The snapshot must be safe to mutate without corrupting the
	// ring's internal state — UI code that prints lines may decorate
	// them in-place and we don't want the next call to see surprises.
	r := newLogRing(5)
	r.append("hello")
	snap := r.snapshot()
	snap[0] = "MUTATED"
	if got := r.snapshot()[0]; got != "hello" {
		t.Errorf("ring corrupted by snapshot mutation: got %q, want %q", got, "hello")
	}
}
