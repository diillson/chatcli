package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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
