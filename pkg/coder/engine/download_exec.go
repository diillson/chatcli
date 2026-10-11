/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package engine

import "regexp"

// downloadExecPatterns match a shell line that fetches or decodes
// content and hands it straight to an interpreter: the payload is never
// on disk for anyone to read before it runs. The patterns work on raw
// text, so they also match a command embedded in tool-call arguments.
//
// The interpreter must follow the pipe directly, optionally behind a
// launcher such as sudo or env and a directory prefix; a consumer such
// as shellcheck or sha256sum does not match. A shell runs its stdin
// whatever flags it gets; another interpreter only when it is given no
// program of its own, so "python3 -m json.tool" is a formatter and not
// a match.
var downloadExecPatterns = func() []*regexp.Regexp {
	const (
		after    = `(?:\s|$|[;|&)"'` + "`" + `])`
		shellRun = `(?:sh|bash|zsh|dash|ksh|mksh|ash|fish|iex|invoke-expression)`
		langRun  = `(?:python[0-9.]*|perl|ruby|node|nodejs|php|pwsh|powershell)(?:\s+-)?\s*(?:$|[;|&)])`
		interp   = `(?:` + shellRun + after + `|` + langRun + `)`
		dirPart  = `(?:[\w.~-]*[/\\])*`
		launcher = `(?:` + dirPart + `(?:sudo|doas|env|command|exec|nohup|nice|time|xargs)(?:\s+-{1,2}[\w-]+(?:=\S*)?)*\s+)*`
		source   = `\b(?:curl|wget|fetch|iwr|irm|invoke-webrequest|invoke-restmethod|base64|openssl\s+(?:base64|enc))\b`
		shells   = `(?:sh|bash|zsh|dash|ksh|mksh|ash|source)`
		fetcher  = `(?:curl|wget)\b`
	)
	raw := []string{
		// curl URL | sh, wget -qO- URL | sudo bash, base64 -d blob | /bin/sh
		`(?i)` + source + `(?:[^;&\n]|>&)*\|\s*` + launcher + dirPart + interp,
		// bash <(curl URL), source <(wget -qO- URL)
		`(?i)\b` + shells + `\b[^|;&\n]*<\(\s*` + fetcher,
		// sh -c "$(curl URL)", bash -c ` + "`wget -O- URL`" + `
		`(?i)\b` + shells + `\s+(?:-\w+\s+)*-c\s+["']?(?:\$\(|` + "`" + `)\s*` + fetcher,
		// eval "$(curl URL)"
		`(?i)\beval\b[^;&\n]*(?:\$\(|` + "`" + `)\s*` + fetcher,
	}
	out := make([]*regexp.Regexp, 0, len(raw))
	for _, p := range raw {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}()

// IsDownloadAndExecute reports whether cmd pipes downloaded or decoded
// content into an interpreter, or runs a download through a shell by
// process or command substitution. Such a line must always be confirmed
// by the user, whatever the policy says.
func IsDownloadAndExecute(cmd string) bool {
	for _, re := range downloadExecPatterns {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}
