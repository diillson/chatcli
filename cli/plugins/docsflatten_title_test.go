/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"strings"
	"testing"
)

// TestCodeTitle_AcrossLanguages proves the title heuristics are language-
// agnostic: the SAME extractor names declarations across Go, Java, Python,
// Kotlin, Rust, C#, C/C++, TypeScript, Swift and Scala without a per-language
// keyword catalog. These are brace/indent units as splitBraceUnits/
// splitIndentUnits would emit them.
func TestCodeTitle_AcrossLanguages(t *testing.T) {
	cases := []struct {
		lang, block, want string
	}{
		{"go-func", "func Add(a, b int) int {\n\treturn a + b\n}", "Add"},
		{"go-method", "func (s *Server) Start() error {\n\treturn nil\n}", "Start"},
		{"java-method", "public static int compute(int a, int b) {\n\treturn a + b;\n}", "compute"},
		{"java-class", "public final class OrderService extends Base {\n}", "OrderService"},
		{"python-def", "def load_config(path):\n    return open(path)", "load_config"},
		{"python-class", "class Engine(Base):\n    pass", "Engine"},
		{"kotlin-fun", "fun loadConfig(): Config {\n    return cfg\n}", "loadConfig"},
		{"kotlin-typed", "override fun onCreate(state: Bundle?) {\n}", "onCreate"},
		{"rust-fn", "pub fn parse(input: &str) -> Result<Ast> {\n}", "parse"},
		{"rust-impl", "impl Display for Token {\n}", "Display"},
		{"csharp-method", "public async Task<int> DoWorkAsync(CancellationToken ct) {\n}", "DoWorkAsync"},
		{"cpp-func", "int main(int argc, char** argv) {\n\treturn 0;\n}", "main"},
		{"ts-export-func", "export function fetchUser(id: string): Promise<User> {\n}", "fetchUser"},
		{"ts-class", "export class UserRepository implements Repo {\n}", "UserRepository"},
		{"swift-func", "func handle(_ request: Request) throws -> Response {\n}", "handle"},
		{"scala-def", "def compute(x: Int): Int = {\n  x * 2\n}", "compute"},
		{"php-function", "function renderPage($ctx) {\n}", "renderPage"},
	}
	for _, c := range cases {
		if got := codeTitle(c.block); got != c.want {
			t.Errorf("[%s] codeTitle = %q, want %q", c.lang, got, c.want)
		}
	}
}

// TestCodeTitle_NeverBreaksOnUnknown is the robustness contract: for input that
// matches no known declaration shape (an exotic language, a config snippet),
// codeTitle must still return a non-empty, bounded, single-line label drawn
// from the signature — never panic, never a multi-line blob — OR "" for a
// preamble-only unit. Searchability never depends on this.
func TestCodeTitle_NeverBreaksOnUnknown(t *testing.T) {
	cases := map[string]string{
		"elixir":       "defmodule MyApp.Worker do\n  def perform, do: :ok\nend",
		"haskell":      "parseLine :: String -> Maybe Token\nparseLine s = ...",
		"lua":          "local function clamp(x) return x end",
		"sql":          "CREATE TABLE orders (id INT PRIMARY KEY);",
		"preambleOnly": "import os\nimport sys\n",
		"justComment":  "// only a comment here\n# and another",
	}
	for name, block := range cases {
		got := codeTitle(block)
		if strings.Contains(got, "\n") {
			t.Errorf("[%s] title must be single-line, got %q", name, got)
		}
		if len(got) > 80 {
			t.Errorf("[%s] title must be clamped, got %d chars", name, len(got))
		}
		// preamble/comment-only units legitimately yield "".
		if name == "preambleOnly" || name == "justComment" {
			if got != "" {
				t.Errorf("[%s] expected empty title, got %q", name, got)
			}
			continue
		}
		if got == "" {
			t.Errorf("[%s] expected a best-effort title, got empty", name)
		}
	}
}

// TestCodeTitle_StatementBeforeDeclNotShadowed pins the strong/weak ordering: a
// top-level statement (a call) preceding the real declaration must not become
// the title.
func TestCodeTitle_StatementBeforeDeclNotShadowed(t *testing.T) {
	block := "set -euo pipefail\nrun_setup(x)\nfunc RealHandler() {\n\treturn\n}"
	if got := codeTitle(block); got != "RealHandler" {
		t.Errorf("declaration must win over a leading statement, got %q", got)
	}
}
