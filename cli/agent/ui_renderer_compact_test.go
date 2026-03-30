package agent

import "testing"

func TestCompactToolLabel(t *testing.T) {
	tests := []struct {
		subcmd  string
		rawArgs string
		want    string
	}{
		{"read", `{"cmd":"read","args":{"file":"main.go"}}`, "Read(main.go)"},
		{"write", `{"cmd":"write","args":{"file":"pkg/handler.go","content":"..."}}`, "Write(pkg/handler.go)"},
		{"exec", `{"cmd":"exec","args":{"cmd":"go test ./..."}}`, "Exec(go test ./...)"},
		{"search", `{"cmd":"search","args":{"term":"TODO","dir":"./src"}}`, "Search(TODO)"},
		{"tree", `{"cmd":"tree","args":{"dir":"."}}`, "Tree(.)"},
		{"tree", `{"cmd":"tree","args":{}}`, "Tree(.)"},
		{"read_file", `{"file":"src/lib.rs"}`, "Read(src/lib.rs)"},
		{"run_command", `{"cmd":"npm test"}`, "Exec(npm test)"},
		{"git-status", `{"dir":"."}`, "GitStatus(.)"},
		// CLI-style args
		{"read", "read --file main.go --start 1", "Read(main.go)"},
		{"exec", "exec --cmd 'ls -la'", "Exec('ls)"},
		// Empty/unknown
		{"unknown", `{}`, "unknown"},
	}

	for _, tt := range tests {
		got := CompactToolLabel(tt.subcmd, tt.rawArgs)
		if got != tt.want {
			t.Errorf("CompactToolLabel(%q, %q) = %q, want %q", tt.subcmd, tt.rawArgs, got, tt.want)
		}
	}
}
