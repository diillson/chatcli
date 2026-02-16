package coder

import (
	"testing"
)

func TestNormalizeCoderArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		wantSub  string
		wantNorm string
	}{
		// JSON format - standard cases
		{
			name:     "json read with file",
			args:     `{"cmd":"read","args":{"file":"main.go"}}`,
			wantSub:  "read",
			wantNorm: "read --file main.go",
		},
		{
			name:     "json exec with cmd",
			args:     `{"cmd":"exec","args":{"cmd":"ls -la"}}`,
			wantSub:  "exec",
			wantNorm: "exec --cmd ls -la",
		},
		{
			name:     "json write excludes content payload",
			args:     `{"cmd":"write","args":{"file":"out.go","content":"package main","encoding":"base64"}}`,
			wantSub:  "write",
			wantNorm: "write --file out.go",
		},
		{
			name:     "json read with multiple args sorted",
			args:     `{"cmd":"read","args":{"file":"main.go","start":10,"end":20}}`,
			wantSub:  "read",
			wantNorm: "read --end 20 --file main.go --start 10",
		},
		{
			name:     "json search with term and path",
			args:     `{"cmd":"search","args":{"term":"TODO","path":"./src"}}`,
			wantSub:  "search",
			wantNorm: "search --path ./src --term TODO",
		},
		{
			name:     "json tree with path",
			args:     `{"cmd":"tree","args":{"path":"/"}}`,
			wantSub:  "tree",
			wantNorm: "tree --path /",
		},

		// JSON with argv array
		{
			name:     "json argv array",
			args:     `{"argv":["read","--file","main.go"]}`,
			wantSub:  "read",
			wantNorm: "read --file main.go",
		},
		{
			name:     "json cmd with argv",
			args:     `{"cmd":"read","argv":["read","--file","main.go"]}`,
			wantSub:  "read",
			wantNorm: "read --file main.go",
		},

		// JSON array format
		{
			name:     "json array",
			args:     `["read","--file","main.go"]`,
			wantSub:  "read",
			wantNorm: "read --file main.go",
		},

		// CLI format
		{
			name:     "cli read",
			args:     "read --file main.go",
			wantSub:  "read",
			wantNorm: "read --file main.go",
		},
		{
			name:     "cli exec",
			args:     `exec --cmd "ls -la"`,
			wantSub:  "exec",
			wantNorm: `exec --cmd "ls -la"`,
		},
		{
			name:     "cli subcommand only",
			args:     "read",
			wantSub:  "read",
			wantNorm: "read",
		},

		// Edge cases
		{
			name:     "empty args",
			args:     "",
			wantSub:  "",
			wantNorm: "",
		},
		{
			name:     "whitespace only",
			args:     "   ",
			wantSub:  "",
			wantNorm: "",
		},
		{
			name:     "json no cmd field - bypass attempt",
			args:     `{"args":{"file":"main.go"}}`,
			wantSub:  "",
			wantNorm: "",
		},
		{
			name:     "invalid json",
			args:     `{invalid}`,
			wantSub:  "",
			wantNorm: "",
		},
		{
			name:     "html escaped json",
			args:     `{&quot;cmd&quot;:&quot;read&quot;,&quot;args&quot;:{&quot;file&quot;:&quot;main.go&quot;}}`,
			wantSub:  "read",
			wantNorm: "read --file main.go",
		},
		{
			name:     "json with boolean flag",
			args:     `{"cmd":"write","args":{"file":"out.go","append":true}}`,
			wantSub:  "write",
			wantNorm: "write --append --file out.go",
		},
		{
			name:     "json with flags key",
			args:     `{"cmd":"read","flags":{"file":"main.go"}}`,
			wantSub:  "read",
			wantNorm: "read --file main.go",
		},
		{
			name:     "json with command renamed to cmd",
			args:     `{"cmd":"exec","args":{"command":"ls -la"}}`,
			wantSub:  "exec",
			wantNorm: "exec --cmd ls -la",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSub, gotNorm := NormalizeCoderArgs(tt.args)
			if gotSub != tt.wantSub {
				t.Errorf("subcommand = %q, want %q", gotSub, tt.wantSub)
			}
			if gotNorm != tt.wantNorm {
				t.Errorf("normalized = %q, want %q", gotNorm, tt.wantNorm)
			}
		})
	}
}

func TestCheck_FullArgsMatching(t *testing.T) {
	tests := []struct {
		name     string
		rules    []Rule
		toolName string
		args     string
		want     Action
	}{
		{
			name: "deny specific file path blocks read",
			rules: []Rule{
				{Pattern: "@coder read", Action: ActionAllow},
				{Pattern: "@coder read --file /etc", Action: ActionDeny},
			},
			toolName: "@coder",
			args:     `{"cmd":"read","args":{"file":"/etc/passwd"}}`,
			want:     ActionDeny,
		},
		{
			name: "deny specific file path allows other paths",
			rules: []Rule{
				{Pattern: "@coder read", Action: ActionAllow},
				{Pattern: "@coder read --file /etc", Action: ActionDeny},
			},
			toolName: "@coder",
			args:     `{"cmd":"read","args":{"file":"main.go"}}`,
			want:     ActionAllow,
		},
		{
			name: "deny root path without subdirectory",
			rules: []Rule{
				{Pattern: "@coder read", Action: ActionAllow},
				{Pattern: "@coder read --file /", Action: ActionDeny},
			},
			toolName: "@coder",
			args:     `{"cmd":"read","args":{"file":"/etc/passwd"}}`,
			want:     ActionDeny,
		},
		{
			name: "deny exec specific command",
			rules: []Rule{
				{Pattern: "@coder exec", Action: ActionAsk},
				{Pattern: "@coder exec --cmd rm", Action: ActionDeny},
			},
			toolName: "@coder",
			args:     `{"cmd":"exec","args":{"cmd":"rm -rf /"}}`,
			want:     ActionDeny,
		},
		{
			name: "allow exec non-denied command",
			rules: []Rule{
				{Pattern: "@coder exec", Action: ActionAsk},
				{Pattern: "@coder exec --cmd rm", Action: ActionDeny},
			},
			toolName: "@coder",
			args:     `{"cmd":"exec","args":{"cmd":"go test ./..."}}`,
			want:     ActionAsk,
		},
		{
			name: "longest prefix wins - specific deny over broad allow",
			rules: []Rule{
				{Pattern: "@coder read", Action: ActionAllow},
				{Pattern: "@coder read --file /etc/passwd", Action: ActionDeny},
			},
			toolName: "@coder",
			args:     `{"cmd":"read","args":{"file":"/etc/passwd"}}`,
			want:     ActionDeny,
		},
		{
			name: "deny tree at root",
			rules: []Rule{
				{Pattern: "@coder tree", Action: ActionAllow},
				{Pattern: "@coder tree --path /", Action: ActionDeny},
			},
			toolName: "@coder",
			args:     `{"cmd":"tree","args":{"path":"/"}}`,
			want:     ActionDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := &PolicyManager{Rules: tt.rules}
			got := pm.Check(tt.toolName, tt.args)
			if got != tt.want {
				t.Errorf("Check() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheck_BackwardsCompatibility(t *testing.T) {
	defaultRules := []Rule{
		{Pattern: "@coder read", Action: ActionAllow},
		{Pattern: "@coder tree", Action: ActionAllow},
		{Pattern: "@coder search", Action: ActionAllow},
		{Pattern: "@coder git-status", Action: ActionAllow},
		{Pattern: "@coder git-diff", Action: ActionAllow},
		{Pattern: "@coder git-log", Action: ActionAllow},
		{Pattern: "@coder git-changed", Action: ActionAllow},
		{Pattern: "@coder git-branch", Action: ActionAllow},
	}

	tests := []struct {
		name     string
		toolName string
		args     string
		want     Action
	}{
		{"read json allowed", "@coder", `{"cmd":"read","args":{"file":"main.go"}}`, ActionAllow},
		{"read cli allowed", "@coder", "read --file main.go", ActionAllow},
		{"tree allowed", "@coder", `{"cmd":"tree","args":{"path":"./src"}}`, ActionAllow},
		{"search allowed", "@coder", `{"cmd":"search","args":{"term":"TODO"}}`, ActionAllow},
		{"git-status allowed", "@coder", `{"cmd":"git-status"}`, ActionAllow},
		{"git-diff allowed", "@coder", `{"cmd":"git-diff"}`, ActionAllow},
		{"write falls to ask", "@coder", `{"cmd":"write","args":{"file":"x"}}`, ActionAsk},
		{"exec falls to ask", "@coder", `{"cmd":"exec","args":{"cmd":"ls"}}`, ActionAsk},
		{"patch falls to ask", "@coder", `{"cmd":"patch","args":{"file":"x"}}`, ActionAsk},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := &PolicyManager{Rules: defaultRules}
			got := pm.Check(tt.toolName, tt.args)
			if got != tt.want {
				t.Errorf("Check() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheck_BypassPrevention(t *testing.T) {
	rules := []Rule{
		{Pattern: "@coder read", Action: ActionDeny},
		{Pattern: "@coder write", Action: ActionDeny},
		{Pattern: "@coder exec", Action: ActionDeny},
	}

	tests := []struct {
		name     string
		toolName string
		args     string
		want     Action
	}{
		{
			name:     "json without cmd field falls to ask not allow",
			toolName: "@coder",
			args:     `{"args":{"file":"main.go"}}`,
			want:     ActionAsk,
		},
		{
			name:     "empty args falls to ask",
			toolName: "@coder",
			args:     "",
			want:     ActionAsk,
		},
		{
			name:     "invalid json falls to ask",
			toolName: "@coder",
			args:     `{broken json`,
			want:     ActionAsk,
		},
		{
			name:     "standard json read correctly denied",
			toolName: "@coder",
			args:     `{"cmd":"read","args":{"file":"main.go"}}`,
			want:     ActionDeny,
		},
		{
			name:     "cli read correctly denied",
			toolName: "@coder",
			args:     "read --file main.go",
			want:     ActionDeny,
		},
		{
			name:     "html escaped json read correctly denied",
			toolName: "@coder",
			args:     `{&quot;cmd&quot;:&quot;read&quot;,&quot;args&quot;:{&quot;file&quot;:&quot;main.go&quot;}}`,
			want:     ActionDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := &PolicyManager{Rules: rules}
			got := pm.Check(tt.toolName, tt.args)
			if got != tt.want {
				t.Errorf("Check() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetSuggestedPattern(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     string
		want     string
	}{
		{"json read", "@coder", `{"cmd":"read","args":{"file":"x"}}`, "@coder read"},
		{"cli exec returns empty to prevent blanket allow", "@coder", `exec --cmd ls`, ""},
		{"json exec returns empty", "@coder", `{"cmd":"exec","args":{"cmd":"ls -la"}}`, ""},
		{"empty args", "@coder", "", "@coder"},
		{"bypass attempt", "@coder", `{"args":{"file":"x"}}`, "@coder"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetSuggestedPattern(tt.toolName, tt.args)
			if got != tt.want {
				t.Errorf("GetSuggestedPattern() = %q, want %q", got, tt.want)
			}
		})
	}
}
