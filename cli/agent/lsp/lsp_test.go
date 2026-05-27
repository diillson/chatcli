package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestCodecRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := map[string]interface{}{"jsonrpc": "2.0", "method": "x", "params": map[string]int{"n": 1}}
	if err := writeMessage(&buf, in); err != nil {
		t.Fatal(err)
	}
	// Header must be Content-Length framed.
	if !bytes.Contains(buf.Bytes(), []byte("Content-Length:")) {
		t.Fatalf("missing header: %q", buf.String())
	}
	body, err := readMessage(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out["method"] != "x" {
		t.Errorf("roundtrip wrong: %v", out)
	}
}

func TestServerForFile(t *testing.T) {
	spec, ok := ServerForFile("/path/main.go")
	if !ok || spec.LanguageID != "go" || spec.Command[0] != "gopls" {
		t.Errorf("go spec wrong: %+v ok=%v", spec, ok)
	}
	if _, ok := ServerForFile("/path/file.unknown"); ok {
		t.Error("unknown extension should not match")
	}

	t.Setenv("CHATCLI_LSP_GO_CMD", "gopls -rpc.trace")
	spec, _ = ServerForFile("x.go")
	if len(spec.Command) != 2 || spec.Command[1] != "-rpc.trace" {
		t.Errorf("env override not applied: %+v", spec.Command)
	}
}

// fakeServer speaks just enough LSP to answer initialize and emit diagnostics
// on didOpen.
func fakeServer(t *testing.T, serverIn io.Reader, serverOut io.Writer, diagURI string) {
	t.Helper()
	r := bufio.NewReader(serverIn)
	for {
		body, err := readMessage(r)
		if err != nil {
			return
		}
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return
		}
		switch {
		case m.Method == "initialize":
			_ = writeMessage(serverOut, map[string]interface{}{"jsonrpc": "2.0", "id": m.ID, "result": map[string]interface{}{"capabilities": map[string]interface{}{}}})
		case m.Method == "shutdown":
			_ = writeMessage(serverOut, map[string]interface{}{"jsonrpc": "2.0", "id": m.ID, "result": nil})
		case m.Method == "textDocument/didOpen":
			_ = writeMessage(serverOut, map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "textDocument/publishDiagnostics",
				"params": map[string]interface{}{
					"uri": diagURI,
					"diagnostics": []map[string]interface{}{
						{"severity": 1, "source": "test", "message": "undefined: foo",
							"range": map[string]interface{}{"start": map[string]int{"line": 2, "character": 1}, "end": map[string]int{"line": 2, "character": 4}}},
					},
				},
			})
		}
	}
}

func TestClientDiagnosticsRoundTrip(t *testing.T) {
	c2sR, c2sW := io.Pipe() // client -> server
	s2cR, s2cW := io.Pipe() // server -> client
	uri := "file:///tmp/main.go"

	go fakeServer(t, c2sR, s2cW, uri)

	client := New(c2sW, s2cR, zap.NewNop())
	if err := client.Initialize("file:///tmp"); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := client.DidOpen(uri, "go", "package main\n\nfoo()\n"); err != nil {
		t.Fatalf("didOpen: %v", err)
	}
	diags, ok := client.Diagnostics(uri, 2*time.Second)
	if !ok {
		t.Fatal("expected diagnostics")
	}
	if len(diags) != 1 || diags[0].Message != "undefined: foo" || diags[0].SeverityLabel() != "error" {
		t.Errorf("diagnostics wrong: %+v", diags)
	}
}

func TestSeverityLabel(t *testing.T) {
	cases := map[int]string{1: "error", 2: "warning", 3: "info", 4: "hint", 9: "diagnostic"}
	for sev, want := range cases {
		d := Diagnostic{Severity: sev}
		if d.SeverityLabel() != want {
			t.Errorf("severity %d => %q, want %q", sev, d.SeverityLabel(), want)
		}
	}
}
