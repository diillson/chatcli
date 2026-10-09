/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/evals"
	"go.uber.org/zap"
)

func runEvalCmd(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := RunEval(context.Background(), args, nil, zap.NewNop(), &out, &errb)
	return code, out.String(), errb.String()
}

func writeEvalSuite(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const evalOKSuite = "name: s\ncases:\n  - {id: a, tags: [x], prompt: p, checks: [{contains: y}]}\n  - {id: b, prompt: p, checks: [{judge: {rubric: r}}]}\n"

func TestEvalNeedsLLM(t *testing.T) {
	if !EvalNeedsLLM([]string{"run", "x"}) || EvalNeedsLLM([]string{"list"}) || EvalNeedsLLM(nil) {
		t.Error("only `run` boots an LLM")
	}
}

func TestRunEvalUsageAndUnknown(t *testing.T) {
	if code, _, errb := runEvalCmd(); code != EvalExitUsage || errb == "" {
		t.Errorf("no args: %d", code)
	}
	if code, _, _ := runEvalCmd("nope"); code != EvalExitUsage {
		t.Errorf("unknown: %d", code)
	}
	if code, out, _ := runEvalCmd("help"); code != EvalExitOK || out == "" {
		t.Errorf("help: %d", code)
	}
}

func TestRunEvalListAndValidate(t *testing.T) {
	p := writeEvalSuite(t, evalOKSuite)
	code, out, _ := runEvalCmd("list", p, "--filter", "tag:x")
	if code != EvalExitOK || !strings.Contains(out, "a") || strings.Contains(out, "  b ") {
		t.Errorf("list: %d %q", code, out)
	}
	if code, _, _ := runEvalCmd("validate", p); code != EvalExitOK {
		t.Errorf("validate: %d", code)
	}
	if code, _, errb := runEvalCmd("validate", p, "--filter", "zzz"); code != EvalExitUsage || errb == "" {
		t.Errorf("no match: %d", code)
	}
	bad := writeEvalSuite(t, "cases:\n  - {id: a, prompt: p}\n")
	if code, _, _ := runEvalCmd("validate", bad); code != EvalExitUsage {
		t.Errorf("invalid suite: %d", code)
	}
	if code, _, _ := runEvalCmd("validate", p, p); code != EvalExitUsage {
		t.Errorf("two paths: %d", code)
	}
}

func TestRunEvalRunRefusesBadFlagsAndMissingJudge(t *testing.T) {
	p := writeEvalSuite(t, evalOKSuite)
	for _, args := range [][]string{
		{"run", p, "--concurrency", "0"},
		{"run", p, "--min-pass-rate", "2"},
		{"run", p, "--trials", "500"},
		{"run", p, "--tolerance", "-1"},
		{"run", p, "--baseline", filepath.Join(t.TempDir(), "missing.json")},
	} {
		if code, _, _ := runEvalCmd(args...); code != EvalExitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	// The suite has a judge check and no LLM manager is available.
	if code, _, errb := runEvalCmd("run", p, "--bin", "/bin/true"); code != EvalExitUsage || errb == "" {
		t.Errorf("judge without a manager: %d", code)
	}
}

func TestRunEvalCompareGate(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, statuses ...string) string {
		r := &evals.Report{Schema: evals.ReportSchema}
		for i, st := range statuses {
			r.Cases = append(r.Cases, evals.CaseResult{Suite: "s", ID: string(rune('a' + i)), Status: st})
		}
		p := filepath.Join(dir, name)
		if err := evals.WriteReport(p, r); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("base.json", evals.StatusPass, evals.StatusPass)
	same := write("same.json", evals.StatusPass, evals.StatusPass)
	worse := write("worse.json", evals.StatusPass, evals.StatusFail)

	if code, _, _ := runEvalCmd("compare", base, same); code != EvalExitOK {
		t.Errorf("same: %d", code)
	}
	if code, out, _ := runEvalCmd("compare", base, worse); code != EvalExitRegression || !strings.Contains(out, "s/b") {
		t.Errorf("worse: %d %q", code, out)
	}
	if code, _, _ := runEvalCmd("compare", "--max-regressions", "1", "--tolerance", "0.5", base, worse); code != EvalExitOK {
		t.Errorf("tolerated: %d", code)
	}
	if code, out, _ := runEvalCmd("compare", base, worse, "--json"); code != EvalExitRegression || !strings.Contains(out, `"regressions"`) {
		t.Errorf("json: %d %q", code, out)
	}
	if code, _, _ := runEvalCmd("compare", base); code != EvalExitUsage {
		t.Errorf("one report: %d", code)
	}
}

func TestParseInterleaved(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	n := fs.Int("n", 0, "")
	q := fs.Bool("q", false, "")
	pos, err := parseInterleaved(fs, []string{"-n", "3", "a", "-q", "b"})
	if err != nil || *n != 3 || !*q || strings.Join(pos, ",") != "a,b" {
		t.Errorf("got %v %d %v %v", pos, *n, *q, err)
	}
	if _, err := parseInterleaved(fs, []string{"--bogus"}); err == nil {
		t.Error("unknown flag must fail")
	}
}

func TestSameTargetAndSuiteJudge(t *testing.T) {
	if !sameTarget(evals.JudgeTarget{Provider: "OPENAI", Model: "GPT-X"}, evals.JudgeTarget{Provider: "openai", Model: "gpt-x"}) {
		t.Error("case-insensitive match")
	}
	if sameTarget(evals.JudgeTarget{Model: "a"}, evals.JudgeTarget{Model: "b"}) || sameTarget(evals.JudgeTarget{}, evals.JudgeTarget{Model: "b"}) {
		t.Error("different or unknown models are not self-judging")
	}
	p, m := suiteJudge([]*evals.Suite{{}, {Judge: evals.JudgeTarget{Provider: "X", Model: "y"}}})
	if p != "X" || m != "y" {
		t.Errorf("suite judge: %s %s", p, m)
	}
}
