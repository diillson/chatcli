/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestInlineCodeRiskAnalyzer_Python documents the contract for Python:
// stdlib pretty-printing and arithmetic stay safe, anything that imports
// os/subprocess/socket or calls eval/exec/file-write is high risk.
//
// This is the fix for the original false-positive: `python -c "print(1)"`
// used to be flagged as dangerous by a blanket `\bpython[23]?\s+-c\b`
// regex. The classifier now lets it through while still catching the
// real attack patterns documented in CommandValidator's legacy tests.
func TestInlineCodeRiskAnalyzer_Python(t *testing.T) {
	a := NewInlineCodeRiskAnalyzer()
	cases := []struct {
		name   string
		source string
		want   InlineCodeRisk
	}{
		{"simple print", `print(1)`, RiskSafe},
		{"sys.version read", `import sys; print(sys.version)`, RiskSafe},
		{"math computation", `print(sum([1,2,3,4]))`, RiskSafe},
		{"json dump of dict", `import json; print(json.dumps({"a":1}))`, RiskSafe},
		{"empty", ``, RiskSafe},
		{"whitespace only", "   \n   ", RiskSafe},
		{"import os direct", `import os`, RiskHigh},
		{"os.system call", `import os; os.system("id")`, RiskHigh},
		{"subprocess", `import subprocess; subprocess.run(["ls"])`, RiskHigh},
		{"socket import", `import socket; s=socket.socket()`, RiskHigh},
		{"eval call", `eval("1+1")`, RiskHigh},
		{"exec call", `exec("print(1)")`, RiskHigh},
		{"__import__", `__import__("os").system("id")`, RiskHigh},
		{"open with write", `open("foo","w").write("x")`, RiskHigh},
		{"requests import", `import requests; requests.get("https://x")`, RiskHigh},
		{"from os import", `from os import path`, RiskHigh},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := a.Analyze("python", c.source)
			assert.Equal(t, c.want, got, "source=%q", c.source)
		})
	}
}

// TestInlineCodeRiskAnalyzer_JavaScript covers Node.js -e classification.
// The set of dangerous primitives in JS is bigger than Python because the
// module system is dynamic — child_process, fs, net are all banned, plus
// the perennial favorites `eval` and `new Function()`.
func TestInlineCodeRiskAnalyzer_JavaScript(t *testing.T) {
	a := NewInlineCodeRiskAnalyzer()
	cases := []struct {
		name   string
		source string
		want   InlineCodeRisk
	}{
		{"console log", `console.log(1)`, RiskSafe},
		{"math", `console.log([1,2,3].reduce((a,b)=>a+b))`, RiskSafe},
		{"empty", ``, RiskSafe},
		{"require child_process", `require("child_process").exec("ls")`, RiskHigh},
		{"require fs", `require("fs").writeFileSync("x","y")`, RiskHigh},
		{"require net", `require("net").createServer()`, RiskHigh},
		{"eval", `eval("1+1")`, RiskHigh},
		{"new Function", `new Function("return 1")()`, RiskHigh},
		{"process.kill", `process.kill(1)`, RiskHigh},
		{"fetch", `fetch("https://x")`, RiskHigh},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := a.Analyze("node", c.source)
			assert.Equal(t, c.want, got, "source=%q", c.source)
		})
	}
}

// TestInlineCodeRiskAnalyzer_Perl_Ruby_PHP_Lua spot-checks the other
// interpreters we support. Each language has a different idiom for
// "execute a shell command" — backticks, qx{}, system(), %x{}, popen —
// and each is enumerated in dangerousInlineMarkers.
func TestInlineCodeRiskAnalyzer_OtherLanguages(t *testing.T) {
	a := NewInlineCodeRiskAnalyzer()
	cases := []struct {
		name   string
		lang   string
		source string
		want   InlineCodeRisk
	}{
		{"perl print", "perl", `print "hello"`, RiskSafe},
		{"perl system", "perl", `system("id")`, RiskHigh},
		{"perl backtick", "perl", "`id`", RiskHigh},

		{"ruby puts", "ruby", `puts "hello"`, RiskSafe},
		{"ruby system", "ruby", `system("id")`, RiskHigh},
		{"ruby percent-x", "ruby", `%x{id}`, RiskHigh},
		{"ruby File.open w", "ruby", `File.open("x","w") { |f| f.puts "y" }`, RiskHigh},

		{"php print", "php", `print 1;`, RiskSafe},
		{"php system", "php", `system("id");`, RiskHigh},
		{"php shell_exec", "php", `shell_exec("id");`, RiskHigh},
		{"php backtick", "php", "`id`", RiskHigh},

		{"lua print", "lua", `print(1)`, RiskSafe},
		{"lua os.execute", "lua", `os.execute("id")`, RiskHigh},
		{"lua io.popen", "lua", `io.popen("id")`, RiskHigh},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := a.Analyze(c.lang, c.source)
			assert.Equal(t, c.want, got, "lang=%s source=%q", c.lang, c.source)
		})
	}
}

// TestInlineCodeRiskAnalyzer_UnknownLanguageIsUnknown documents what
// happens when the validator passes a language we don't have rules for.
// Default (non-strict) returns Unknown — caller decides; strict mode
// elevates to High.
func TestInlineCodeRiskAnalyzer_UnknownLanguageIsUnknown(t *testing.T) {
	a := NewInlineCodeRiskAnalyzer()
	assert.Equal(t, RiskUnknown, a.Analyze("haskell", `putStrLn "x"`))
	assert.False(t, a.IsHighRisk("haskell", `putStrLn "x"`),
		"non-strict mode lets unknown through")
}

// TestCommandValidator_PythonInlineSafe is the headline regression test
// for Fase 1.3: a Python invocation with safe inline source must not
// trigger the dangerous-command confirmation. Previously this fired
// because of the blanket `\bpython[23]?\s+-c\b` regex.
func TestCommandValidator_PythonInlineSafe(t *testing.T) {
	v := NewCommandValidator(zap.NewNop())
	assert.False(t, v.IsDangerous(`python -c "print(1)"`),
		"safe Python one-liner must pass")
	assert.False(t, v.IsDangerous(`python3 -c "import sys; print(sys.version)"`),
		"reading sys.version is safe")
	assert.False(t, v.IsDangerous(`python -c 'print(sum([1,2,3]))'`),
		"pure arithmetic is safe")
}

// TestCommandValidator_PythonInlineDangerous keeps the existing
// behavior for malicious inline scripts. This is the case we cannot
// regress — `python -c "import os; os.system('rm -rf /')"` must remain
// flagged.
func TestCommandValidator_PythonInlineDangerous(t *testing.T) {
	v := NewCommandValidator(zap.NewNop())
	assert.True(t, v.IsDangerous(`python -c "import os; os.system('id')"`),
		"os.system from inline must stay dangerous")
	assert.True(t, v.IsDangerous(`python3 -c "import subprocess; subprocess.run(['rm','-rf','/'])"`),
		"subprocess.run from inline must stay dangerous")
	assert.True(t, v.IsDangerous(`python -c "open('/etc/passwd','w').write('')"`),
		"open with write mode from inline must stay dangerous")
}

// TestCommandValidator_NodeInline covers the same matrix for Node.
func TestCommandValidator_NodeInline(t *testing.T) {
	v := NewCommandValidator(zap.NewNop())
	assert.False(t, v.IsDangerous(`node -e "console.log(2+2)"`))
	assert.True(t, v.IsDangerous(`node -e "require('child_process').exec('id')"`))
}

// TestCommandValidator_PipelineSafeStaysSafe asserts that benign
// pipelines like `ls | jq` and `cat file | grep pattern` do not get
// elevated to dangerous. The previous regex layer did not flag them
// either; this test pins the contract so future refactors don't drift.
func TestCommandValidator_PipelineSafeStaysSafe(t *testing.T) {
	v := NewCommandValidator(zap.NewNop())
	cases := []string{
		`ls | jq .`,
		`cat file.txt | grep TODO`,
		`docker ps | awk '{print $1}'`,
		`find . -name "*.go" | head -5`,
		`echo hello | tr a-z A-Z`,
		`ps aux | sort -k 3 -r | head`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			assert.False(t, v.IsDangerous(c))
		})
	}
}

// TestInlineCodeStrictMode confirms the env-var-driven strict mode
// elevates Unknown to dangerous. We don't toggle the env in-test because
// NewInlineCodeRiskAnalyzer reads it once at construction; we set the
// field directly via an internal constructor for isolation.
func TestInlineCodeStrictMode(t *testing.T) {
	a := &InlineCodeRiskAnalyzer{strict: true}
	assert.True(t, a.IsHighRisk("haskell", `putStrLn "x"`),
		"strict mode treats unknown language as dangerous")
}
