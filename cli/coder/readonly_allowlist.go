/*
 * ChatCLI - Read-Only Command Allowlist
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Centralized allowlist of commands that are safe to execute without user
 * approval because they only read data and have no side effects.
 *
 * Inspired by openclaude's readOnlyValidation.ts which maintains a
 * COMMAND_ALLOWLIST with per-flag safe values.
 *
 * Commands in this list are auto-allowed in the policy check flow,
 * bypassing the interactive prompt. This significantly reduces prompt
 * fatigue for common read-only operations.
 *
 * The line is parsed with a real shell parser and every simple command in
 * it must be read-only on its own. Anything the parser cannot prove inert
 * is refused, and a refusal only means the user is asked: command or
 * process substitution, parameter expansion, assignments, background
 * jobs, compound commands, and redirection to anything but /dev/null.
 */
package coder

import (
	"regexp"
	"runtime"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ReadOnlyCommand defines a command that is safe for auto-approval.
type ReadOnlyCommand struct {
	// Name is the base command name (e.g., "git", "ls", "cat")
	Name string

	// SafeSubcommands are subcommands that are read-only.
	// If empty, the command itself is read-only (e.g., "ls").
	// If non-empty, only these subcommands are auto-allowed.
	SafeSubcommands []string

	// UnsafeFlags are flags that make even a safe command unsafe.
	// If the command contains any of these, it's NOT auto-allowed.
	UnsafeFlags []string
}

// shellArg is one argument after shell quote removal. globLead marks an
// unquoted word that starts with a glob or brace character: the shell
// replaces it with file names, and a file named like an option becomes
// one.
type shellArg struct {
	val      string
	globLead bool
}

// readOnlyCheck reports whether the arguments of an allowlisted command
// keep it read-only.
type readOnlyCheck func(args []shellArg) bool

// flagRules is the common shape of a per-command check.
type flagRules struct {
	// short lists option letters that write or execute; they are refused
	// anywhere inside a cluster such as -nri.
	short string
	// long lists long options that write or execute. GNU getopt and git
	// accept any unambiguous prefix, so a prefix of one is refused too.
	long []string
	// words lists single-dash word options, as find and xmllint use.
	words []string
	// maxPositional caps non-option arguments; 0 means no cap.
	maxPositional int
}

func (r flagRules) sensitive() bool {
	return r.short != "" || len(r.long) > 0 || len(r.words) > 0 || r.maxPositional > 0
}

func (r flagRules) check(args []shellArg) bool {
	endOfOptions := false
	positional := 0
	for _, a := range args {
		if !endOfOptions && a.globLead && r.sensitive() {
			return false
		}
		v := a.val
		if endOfOptions || v == "-" || !strings.HasPrefix(v, "-") {
			positional++
			if r.maxPositional > 0 && positional > r.maxPositional {
				return false
			}
			continue
		}
		if v == "--" {
			endOfOptions = true
			continue
		}
		if strings.HasPrefix(v, "--") {
			if matchesLongOption(v, r.long) {
				return false
			}
			continue
		}
		for _, w := range r.words {
			if v == w || strings.HasPrefix(v, w+"=") {
				return false
			}
		}
		if r.short != "" && strings.ContainsAny(v[1:], r.short) {
			return false
		}
	}
	return true
}

// matchesLongOption reports whether arg names one of opts, in full or as
// the abbreviation getopt_long and git's parse-options accept.
func matchesLongOption(arg string, opts []string) bool {
	name, _, _ := strings.Cut(arg, "=")
	if len(name) <= 2 {
		return false
	}
	for _, o := range opts {
		if strings.HasPrefix(o, name) {
			return true
		}
	}
	return false
}

func anyArgs([]shellArg) bool { return true }

// noStartupCommands refuses the "+command" argument of a pager: less runs
// it at startup, and its ! command runs a shell.
func noStartupCommands(args []shellArg) bool {
	for _, a := range args {
		if strings.HasPrefix(a.val, "+") || strings.HasPrefix(a.val, "-+") {
			return false
		}
	}
	return true
}

// readOnlyCommands maps each auto-allowed command to its argument check.
// Not listed, and so always asked: env and printenv (they print secrets
// into the conversation), interpreters with inline code, and toolchain
// front-ends that run a program chosen by a file in the workspace
// (yarn's yarnPath, cargo and rustc through rust-toolchain.toml).
var readOnlyCommands = map[string]readOnlyCheck{
	// Filesystem read operations
	"ls":        anyArgs,
	"ll":        anyArgs,
	"cat":       anyArgs,
	"head":      anyArgs,
	"tail":      flagRules{short: "fF", long: []string{"--follow", "--retry"}}.check,
	"less":      noStartupCommands,
	"more":      noStartupCommands,
	"wc":        anyArgs,
	"file":      flagRules{short: "C", long: []string{"--compile"}}.check,
	"stat":      anyArgs,
	"du":        anyArgs,
	"df":        anyArgs,
	"find":      flagRules{words: []string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls"}}.check,
	"tree":      flagRules{short: "oR", long: []string{"--output"}}.check,
	"realpath":  anyArgs,
	"readlink":  anyArgs,
	"basename":  anyArgs,
	"dirname":   anyArgs,
	"md5sum":    anyArgs,
	"sha256sum": anyArgs,

	// Text processing (read-only)
	"grep":    anyArgs,
	"egrep":   anyArgs,
	"fgrep":   anyArgs,
	"rg":      flagRules{long: []string{"--pre", "--hostname-bin"}}.check,
	"ag":      flagRules{long: []string{"--pager"}}.check,
	"awk":     checkAwk,
	"sed":     checkSed,
	"sort":    flagRules{short: "o", long: []string{"--output", "--compress-program"}}.check,
	"uniq":    flagRules{maxPositional: 1}.check,
	"cut":     anyArgs,
	"tr":      anyArgs,
	"diff":    anyArgs,
	"comm":    anyArgs,
	"jq":      checkJq,
	"yq":      checkYq,
	"xmllint": flagRules{long: []string{"--output", "--shell"}, words: []string{"-o"}}.check,

	// Version control and toolchains
	"git":     checkGit,
	"go":      checkGo,
	"node":    onlyVersionFlag("-v", "--version"),
	"python":  onlyVersionFlag("--version", "-V"),
	"python3": onlyVersionFlag("--version", "-V"),
	"java":    onlyVersionFlag("-version", "--version"),

	// System info
	"uname":    anyArgs,
	"hostname": checkHostname,
	"whoami":   anyArgs,
	"id":       anyArgs,
	"date":     checkDate,
	"uptime":   anyArgs,
	"which":    anyArgs,
	"whereis":  anyArgs,
	"type":     anyArgs,

	// Container/K8s read operations
	"docker":  checkDocker,
	"kubectl": checkKubectl,

	// Package managers (read-only)
	"npm":  checkNpm,
	"pip":  subcommands("list", "show", "freeze", "check"),
	"pip3": subcommands("list", "show", "freeze", "check"),

	// Output only, no side effects
	"echo":   anyArgs,
	"printf": anyArgs,
	"pwd":    anyArgs,
}

// systemBinDirs are the directories an absolute command path may name.
// A relative or workspace path can be a script that shares a name with
// an allowlisted command.
var systemBinDirs = []string{"/bin/", "/usr/bin/", "/usr/local/bin/", "/sbin/", "/usr/sbin/", "/opt/homebrew/bin/"}

// IsReadOnlyCommand checks if a shell command is safe to auto-approve.
// Returns true if the command is in the read-only allowlist and doesn't
// contain any unsafe flags.
func IsReadOnlyCommand(cmdLine string) bool {
	cmdLine = strings.TrimSpace(cmdLine)
	if cmdLine == "" {
		return false
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmdLine), "")
	if err != nil || len(file.Stmts) == 0 {
		return false
	}
	for _, st := range file.Stmts {
		if !readOnlyStmt(st) {
			return false
		}
	}
	return true
}

// readOnlyStmt accepts a simple command, or a list or pipeline whose every
// command is read-only.
func readOnlyStmt(st *syntax.Stmt) bool {
	if st == nil || st.Background || st.Coprocess || st.Disown {
		return false
	}
	for _, r := range st.Redirs {
		if !safeRedirect(r) {
			return false
		}
	}
	switch c := st.Cmd.(type) {
	case *syntax.CallExpr:
		return readOnlyCall(c)
	case *syntax.BinaryCmd:
		switch c.Op {
		case syntax.AndStmt, syntax.OrStmt, syntax.Pipe, syntax.PipeAll:
			return readOnlyStmt(c.X) && readOnlyStmt(c.Y)
		}
	}
	return false
}

// safeRedirect allows discarding output, duplicating descriptors and
// reading a file. Every other redirection can create or truncate a file.
func safeRedirect(r *syntax.Redirect) bool {
	if r.N != nil && !isDigits(r.N.Value) {
		return false
	}
	target, ok := literalWord(r.Word)
	if !ok {
		return false
	}
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll:
		return target.val == "/dev/null"
	case syntax.DplOut, syntax.DplIn:
		return target.val == "-" || isDigits(target.val)
	case syntax.RdrIn, syntax.WordHdoc:
		return true
	}
	return false
}

func readOnlyCall(call *syntax.CallExpr) bool {
	if len(call.Assigns) > 0 || len(call.Args) == 0 {
		return false
	}
	words := make([]shellArg, 0, len(call.Args))
	for _, w := range call.Args {
		a, ok := literalWord(w)
		if !ok {
			return false
		}
		if runtime.GOOS == "windows" && strings.ContainsAny(a.val, "%^!") {
			// cmd.exe expands %VAR% and !VAR! and treats ^ as an escape
			// the POSIX parse above does not see.
			return false
		}
		words = append(words, a)
	}
	name, ok := commandName(words[0])
	if !ok {
		return false
	}
	check, ok := readOnlyCommands[name]
	if !ok {
		return false
	}
	return check(words[1:])
}

// commandName reduces the command word to an allowlist key. A bare name
// resolves through PATH; a path must be absolute and inside a system
// binary directory.
func commandName(w shellArg) (string, bool) {
	if w.globLead || w.val == "" {
		return "", false
	}
	name := w.val
	if strings.ContainsAny(name, `/\`) {
		if !isSystemBinPath(name) {
			return "", false
		}
		name = name[strings.LastIndexAny(name, `/\`)+1:]
	}
	return strings.ToLower(stripCommandPath(name)), true
}

func isSystemBinPath(p string) bool {
	if strings.Contains(p, "..") {
		return false
	}
	dir := p[:strings.LastIndex(p, "/")+1]
	for _, d := range systemBinDirs {
		if dir == d {
			return true
		}
	}
	return false
}

// literalWord returns the value a word has after quote removal when it
// contains nothing the shell expands. Any expansion, substitution or
// ANSI-C quoting makes it non-literal.
func literalWord(w *syntax.Word) (shellArg, bool) {
	if w == nil || len(w.Parts) == 0 {
		return shellArg{}, false
	}
	var b strings.Builder
	var arg shellArg
	for i, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if i == 0 && p.Value != "" && strings.ContainsRune("*?[{", rune(p.Value[0])) {
				arg.globLead = true
			}
			b.WriteString(unescapeUnquoted(p.Value))
		case *syntax.SglQuoted:
			if p.Dollar {
				return shellArg{}, false
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return shellArg{}, false
			}
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return shellArg{}, false
				}
				b.WriteString(unescapeDoubleQuoted(lit.Value))
			}
		default:
			return shellArg{}, false
		}
	}
	arg.val = b.String()
	return arg, true
}

// unescapeUnquoted applies the shell's backslash rule outside quotes: a
// backslash keeps the next character literal, and before a newline it
// joins the lines.
func unescapeUnquoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] != '\n' {
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unescapeDoubleQuoted applies the rule inside double quotes, where a
// backslash only escapes $, `, ", \ and newline.
func unescapeDoubleQuoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
			i++
			if s[i] != '\n' {
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// positionals returns the non-option arguments, respecting "--".
func positionals(args []shellArg) []string {
	var out []string
	end := false
	for _, a := range args {
		if !end && a.val == "--" {
			end = true
			continue
		}
		if end || a.val == "-" || !strings.HasPrefix(a.val, "-") {
			out = append(out, a.val)
		}
	}
	return out
}

func hasArg(args []shellArg, values ...string) bool {
	for _, a := range args {
		for _, v := range values {
			if a.val == v {
				return true
			}
		}
	}
	return false
}

func anyGlobLead(args []shellArg) bool {
	for _, a := range args {
		if a.globLead {
			return true
		}
	}
	return false
}

func onlyVersionFlag(flags ...string) readOnlyCheck {
	return func(args []shellArg) bool {
		if len(args) != 1 {
			return false
		}
		for _, f := range flags {
			if args[0].val == f {
				return true
			}
		}
		return false
	}
}

// subcommands accepts the listed subcommands as the first argument, with
// any further arguments.
func subcommands(names ...string) readOnlyCheck {
	return func(args []shellArg) bool {
		if len(args) == 0 || anyGlobLead(args) {
			return false
		}
		for _, n := range names {
			if strings.EqualFold(args[0].val, n) {
				return true
			}
		}
		return false
	}
}

// ---- git ----

// gitUnsafeLong are options that write a file or run a program on the
// read-only subcommands.
var gitUnsafeLong = []string{"--output", "--ext-diff", "--open-files-in-pager", "--exec", "--upload-pack", "--receive-pack"}

var gitPlainSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "blame": true,
	"shortlog": true, "rev-parse": true, "describe": true, "ls-files": true,
	"ls-tree": true, "cat-file": true, "show-ref": true,
}

// checkGit needs the subcommand right after "git": a global option such as
// -C, -c or --exec-path in front of it is not auto-allowed.
func checkGit(args []shellArg) bool {
	if len(args) == 0 || anyGlobLead(args) {
		return false
	}
	sub := args[0].val
	rest := args[1:]
	if sub == "--version" || sub == "version" {
		return true
	}
	common := flagRules{long: gitUnsafeLong}
	if !common.check(rest) {
		return false
	}
	if gitPlainSubcommands[sub] {
		return true
	}
	switch sub {
	case "branch":
		return gitListingOnly(rest, gitBranchListFlags)
	case "tag":
		return gitListingOnly(rest, gitTagListFlags)
	case "remote":
		return checkGitRemote(rest)
	case "stash":
		pos := positionals(rest)
		return len(pos) > 0 && (pos[0] == "list" || pos[0] == "show") && pos[0] == firstNonOption(rest)
	case "reflog":
		pos := positionals(rest)
		return len(pos) == 0 || (pos[0] != "expire" && pos[0] != "delete" && pos[0] != "drop" && pos[0] != "write")
	case "config":
		return checkGitConfig(rest)
	}
	return false
}

func firstNonOption(args []shellArg) string {
	for _, a := range args {
		if !strings.HasPrefix(a.val, "-") {
			return a.val
		}
	}
	return ""
}

var gitBranchListFlags = []string{
	"-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "-l", "--list",
	"--show-current", "--merged", "--no-merged", "--contains", "--no-contains",
	"--points-at", "--sort", "--format", "--color", "--no-color", "--column",
	"--no-column", "-i", "--ignore-case", "--omit-empty", "--abbrev", "--no-abbrev",
}

var gitTagListFlags = []string{
	"-l", "--list", "-n", "--sort", "--format", "--contains", "--no-contains",
	"--merged", "--no-merged", "--points-at", "--color", "--column", "--no-column",
	"-i", "--ignore-case", "--omit-empty",
}

// gitListingOnly accepts git branch or git tag only in list mode: every
// option from the listing set, and names only as patterns after --list.
func gitListingOnly(args []shellArg, allowed []string) bool {
	listMode := false
	for _, a := range args {
		v := a.val
		if !strings.HasPrefix(v, "-") || v == "--" {
			continue
		}
		name, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(name, "-n") && !strings.HasPrefix(name, "--") {
			name = "-n"
		}
		ok := false
		for _, f := range allowed {
			if name == f {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
		if name == "-l" || name == "--list" {
			listMode = true
		}
	}
	return listMode || len(positionals(args)) == 0
}

func checkGitRemote(args []shellArg) bool {
	pos := positionals(args)
	for _, a := range args {
		if strings.HasPrefix(a.val, "-") && a.val != "-v" && a.val != "--verbose" && a.val != "--push" && a.val != "--all" {
			return false
		}
	}
	return len(pos) == 0 || pos[0] == "get-url"
}

var gitConfigReadActions = map[string]bool{
	"--get": true, "--get-all": true, "--get-regexp": true, "--get-urlmatch": true,
	"--list": true, "-l": true, "--get-color": true, "--get-colorbool": true,
}

var gitConfigWriteActions = []string{
	"--add", "--unset", "--unset-all", "--replace-all", "--rename-section",
	"--remove-section", "--edit",
}

// checkGitConfig accepts the read forms of git config, old and new syntax.
func checkGitConfig(args []shellArg) bool {
	read := false
	for _, a := range args {
		name, _, _ := strings.Cut(a.val, "=")
		if name == "-e" || (strings.HasPrefix(name, "--") && matchesLongOption(name, gitConfigWriteActions)) {
			return false
		}
		if gitConfigReadActions[name] {
			read = true
		}
	}
	if pos := positionals(args); len(pos) > 0 && (pos[0] == "get" || pos[0] == "list") && pos[0] == firstNonOption(args) {
		return true
	}
	return read && len(positionals(args)) <= 2
}

// ---- go ----

// goUnsafeFlags run a program the command line names.
var goUnsafeFlags = map[string]bool{"toolexec": true, "exec": true, "vettool": true}

// goEnvWriteFlags make go env write the user's go.env file.
var goEnvWriteFlags = map[string]bool{"w": true, "u": true}

// checkGo accepts the read-only go subcommands. The flag package takes
// -name and --name alike, so both spellings are checked.
func checkGo(args []shellArg) bool {
	if len(args) == 0 || anyGlobLead(args) {
		return false
	}
	switch args[0].val {
	case "version", "env", "list", "doc", "vet":
	default:
		return false
	}
	for _, a := range args[1:] {
		if a.val == "--" {
			break
		}
		if !strings.HasPrefix(a.val, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a.val, "-"), "=")
		if goUnsafeFlags[name] || (args[0].val == "env" && goEnvWriteFlags[name]) {
			return false
		}
	}
	return true
}

// ---- containers and package managers ----

func checkDocker(args []shellArg) bool {
	if len(args) == 0 || anyGlobLead(args) {
		return false
	}
	switch args[0].val {
	case "ps", "images", "inspect", "logs", "info", "version", "stats", "top":
		return true
	case "network", "volume", "image", "container":
		pos := positionals(args[1:])
		return len(pos) > 0 && (pos[0] == "ls" || pos[0] == "list" || pos[0] == "inspect") && pos[0] == firstNonOption(args[1:])
	}
	return false
}

func checkKubectl(args []shellArg) bool {
	if len(args) == 0 || anyGlobLead(args) {
		return false
	}
	switch args[0].val {
	case "get", "describe", "logs", "top", "version", "cluster-info", "api-resources", "api-versions", "explain":
		return true
	case "config":
		if hasArg(args, "--raw") || hasPrefixArg(args, "--raw=") {
			return false
		}
		pos := positionals(args[1:])
		if len(pos) == 0 || pos[0] != firstNonOption(args[1:]) {
			return false
		}
		switch pos[0] {
		case "view", "get-contexts", "current-context", "get-clusters":
			return true
		}
	}
	return false
}

func hasPrefixArg(args []shellArg, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a.val, prefix) {
			return true
		}
	}
	return false
}

func checkNpm(args []shellArg) bool {
	if len(args) == 0 || anyGlobLead(args) {
		return false
	}
	switch args[0].val {
	case "ls", "list", "outdated", "info", "view", "search":
		return true
	case "config":
		pos := positionals(args[1:])
		return len(pos) > 0 && (pos[0] == "get" || pos[0] == "list" || pos[0] == "ls") && pos[0] == firstNonOption(args[1:])
	}
	return false
}

// ---- system info ----

// checkHostname accepts only queries: a name argument or -F sets the
// host name.
func checkHostname(args []shellArg) bool {
	for _, a := range args {
		if !strings.HasPrefix(a.val, "-") || a.val == "-F" || strings.HasPrefix(a.val, "--file") || a.val == "-b" || a.val == "--boot" {
			return false
		}
	}
	return true
}

// checkDate accepts output formats only: -s, --set or a bare date
// argument sets the clock.
func checkDate(args []shellArg) bool {
	for _, a := range args {
		v := a.val
		if a.globLead {
			return false
		}
		if strings.HasPrefix(v, "-") {
			if v == "-s" || matchesLongOption(v, []string{"--set"}) || (!strings.HasPrefix(v, "--") && strings.Contains(v[1:], "s")) {
				return false
			}
			continue
		}
		if !strings.HasPrefix(v, "+") {
			return false
		}
	}
	return true
}

// ---- jq, yq, awk, sed ----

// envAccess matches the jq and yq builtins that read the environment.
var envAccess = regexp.MustCompile(`\$ENV\b|\benv\b|\bstrenv\b|\$__prog_args\b`)

func checkJq(args []shellArg) bool {
	for _, a := range args {
		if envAccess.MatchString(a.val) {
			return false
		}
	}
	return true
}

func checkYq(args []shellArg) bool {
	for _, a := range args {
		if envAccess.MatchString(a.val) {
			return false
		}
	}
	return flagRules{short: "is", long: []string{"--inplace", "--split-exp", "--front-matter"}}.check(args)
}

// awkUnsafe matches the parts of an awk program that run a command,
// write a file, read one or the environment, or load code.
var awkUnsafe = regexp.MustCompile(`system|getline|ENVIRON|PROCINFO|[|>@]|close|fflush`)

// checkAwk accepts -F and -v options and a program that only reads its
// input and prints.
func checkAwk(args []shellArg) bool {
	program := ""
	seenProgram := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.globLead && !seenProgram {
			return false
		}
		v := a.val
		if !seenProgram && strings.HasPrefix(v, "-") && v != "-" {
			switch {
			case v == "--":
				continue
			case v == "-F" || v == "-v":
				i++
				continue
			case strings.HasPrefix(v, "-F") || strings.HasPrefix(v, "-v"):
				continue
			default:
				return false
			}
		}
		if !seenProgram {
			program = v
			seenProgram = true
		}
	}
	return seenProgram && !awkUnsafe.MatchString(program)
}

// checkSed accepts sed without in-place editing and with scripts made
// only of commands that print, delete, substitute or branch. The w, W,
// r, R and e commands and the w and e substitution flags write files or
// run programs.
func checkSed(args []shellArg) bool {
	var scripts []string
	explicit := false
	endOfOptions := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		v := a.val
		if !endOfOptions && a.globLead {
			return false
		}
		if endOfOptions || v == "-" || !strings.HasPrefix(v, "-") {
			if !explicit && len(scripts) == 0 {
				scripts = append(scripts, v)
			}
			continue
		}
		switch {
		case v == "--":
			endOfOptions = true
		case v == "-e" || v == "--expression":
			if i+1 >= len(args) {
				return false
			}
			i++
			scripts = append(scripts, args[i].val)
			explicit = true
		case strings.HasPrefix(v, "--expression="):
			scripts = append(scripts, strings.TrimPrefix(v, "--expression="))
			explicit = true
		case strings.HasPrefix(v, "--"):
			if matchesLongOption(v, []string{"--in-place", "--file", "--follow-symlinks", "--expression"}) {
				return false
			}
		default:
			cluster := v[1:]
			if strings.ContainsAny(cluster, "iIf") {
				return false
			}
			if idx := strings.IndexByte(cluster, 'e'); idx >= 0 {
				if idx == len(cluster)-1 {
					if i+1 >= len(args) {
						return false
					}
					i++
					scripts = append(scripts, args[i].val)
				} else {
					scripts = append(scripts, cluster[idx+1:])
				}
				explicit = true
			}
		}
	}
	if len(scripts) == 0 {
		return false
	}
	for _, s := range scripts {
		if !sedScriptReadOnly(s) {
			return false
		}
	}
	return true
}

// sedScriptReadOnly walks a sed script command by command.
func sedScriptReadOnly(s string) bool {
	sc := &sedScanner{s: s}
	for {
		sc.skip(" \t\n;")
		if sc.done() {
			return true
		}
		if !sc.addressRange() {
			return false
		}
		sc.skip(" !")
		if sc.done() || !sc.command() {
			return false
		}
	}
}

// sedScanner is a cursor over a sed script.
type sedScanner struct {
	s string
	i int
}

func (sc *sedScanner) done() bool { return sc.i >= len(sc.s) }

func (sc *sedScanner) peek() byte {
	if sc.done() {
		return 0
	}
	return sc.s[sc.i]
}

// skip advances past any of the bytes in set.
func (sc *sedScanner) skip(set string) {
	for !sc.done() && strings.IndexByte(set, sc.s[sc.i]) >= 0 {
		sc.i++
	}
}

// skipUntil advances to the next byte in set.
func (sc *sedScanner) skipUntil(set string) {
	for !sc.done() && strings.IndexByte(set, sc.s[sc.i]) < 0 {
		sc.i++
	}
}

// delimited consumes text up to an unescaped delim, inclusive.
func (sc *sedScanner) delimited(delim byte) bool {
	for !sc.done() {
		switch sc.s[sc.i] {
		case '\\':
			sc.i += 2
			continue
		case delim:
			sc.i++
			return true
		}
		sc.i++
	}
	return false
}

// delimitedPair consumes the two delimited parts of s and y.
func (sc *sedScanner) delimitedPair() bool {
	if sc.done() {
		return false
	}
	delim := sc.s[sc.i]
	if delim == '\\' || delim == '\n' {
		return false
	}
	sc.i++
	if !sc.delimited(delim) {
		return false
	}
	return sc.delimited(delim)
}

func (sc *sedScanner) addressRange() bool {
	if !sc.address() {
		return false
	}
	if sc.peek() != ',' {
		return true
	}
	sc.i++
	sc.skip("+~")
	return sc.address()
}

func (sc *sedScanner) address() bool {
	c := sc.peek()
	switch {
	case c >= '0' && c <= '9':
		sc.skip("0123456789~")
	case c == '$':
		sc.i++
	case c == '/':
		sc.i++
		if !sc.delimited('/') {
			return false
		}
		sc.skip("IM")
	case c == '\\' && sc.i+1 < len(sc.s):
		delim := sc.s[sc.i+1]
		sc.i += 2
		return sc.delimited(delim)
	}
	return true
}

// command accepts the commands that only print, delete, substitute,
// move between buffers or branch.
func (sc *sedScanner) command() bool {
	c := sc.s[sc.i]
	sc.i++
	switch {
	case strings.IndexByte("pPdDnNgGhHx=l{}", c) >= 0:
		return true
	case c == 'q' || c == 'Q':
		sc.skip("0123456789")
		return true
	case c == 'b' || c == 't' || c == 'T' || c == ':':
		sc.skipUntil(";\n")
		return true
	case c == 's':
		if !sc.delimitedPair() {
			return false
		}
		for !sc.done() && strings.IndexByte(";\n} ", sc.s[sc.i]) < 0 {
			if strings.IndexByte("gpiImM0123456789", sc.s[sc.i]) < 0 {
				return false
			}
			sc.i++
		}
		return true
	case c == 'y':
		return sc.delimitedPair()
	}
	return false
}

// stripCommandPath reduces an executable invocation to its bare command name:
// directory prefixes with either separator are removed, along with a Windows
// executable extension, so `C:\Git\git.exe` and `/usr/bin/git` both yield "git".
func stripCommandPath(cmd string) string {
	cmd = cmd[strings.LastIndexAny(cmd, `/\`)+1:]
	for _, ext := range []string{".exe", ".bat", ".cmd"} {
		if len(cmd) > len(ext) && strings.EqualFold(cmd[len(cmd)-len(ext):], ext) {
			return cmd[:len(cmd)-len(ext)]
		}
	}
	return cmd
}
