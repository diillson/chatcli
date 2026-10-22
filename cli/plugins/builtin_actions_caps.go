/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * DescribeCall implementations for the action/multimodal builtins. These give
 * the agent progress UI a concise, contextual one-liner (e.g. "🎨 Generating
 * image: a watercolor fox") instead of falling back to the long static
 * Description(), which rendered as an oversized box. Labels are i18n-resolved.
 */
package plugins

import (
	"encoding/json"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// describeStr extracts a string field from an inner-args JSON object.
func describeStr(inner, key string) string {
	var m map[string]interface{}
	if json.Unmarshal([]byte(inner), &m) != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// describeTrunc shortens a label argument for single-line display.
func describeTrunc(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

// --- @image ---

func (*BuiltinImagePlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseImageInvocation(args)
	if err != nil {
		return i18n.T("plugins.image.describe.default")
	}
	switch cmd {
	case "status":
		return i18n.T("plugins.image.describe.status")
	case "models":
		return i18n.T("plugins.image.describe.models")
	case "use":
		target := describeStr(inner, "model")
		if target == "" {
			target = describeStr(inner, "provider")
		}
		return i18n.T("plugins.image.describe.use", describeTrunc(target, 50))
	case "edit":
		return i18n.T("plugins.image.describe.edit", describeTrunc(describeStr(inner, "prompt"), 50))
	}
	return i18n.T("plugins.image.describe.gen", describeTrunc(describeStr(inner, "prompt"), 50))
}

// --- @speak ---

func (*BuiltinSpeakPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseSpeakInvocation(args)
	if err != nil {
		return i18n.T("plugins.speak.describe.default")
	}
	if cmd == "status" {
		return i18n.T("plugins.speak.describe.status")
	}
	return i18n.T("plugins.speak.describe.say", describeTrunc(describeStr(inner, "text"), 50))
}

// --- @session ---

func (*BuiltinSessionPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseSessionInvocation(args)
	if err != nil {
		return i18n.T("plugins.session.describe.default")
	}
	switch cmd {
	case "list":
		return i18n.T("plugins.session.describe.list")
	case "get":
		return i18n.T("plugins.session.describe.get", describeTrunc(describeStr(inner, "id"), 40))
	case "save":
		return i18n.T("plugins.session.describe.save")
	case "fork":
		return i18n.T("plugins.session.describe.fork")
	case "attach":
		return i18n.T("plugins.session.describe.attach", describeTrunc(describeStr(inner, "id"), 40))
	case "detach":
		return i18n.T("plugins.session.describe.detach")
	}
	return i18n.T("plugins.session.describe.search", describeTrunc(describeStr(inner, "query"), 50))
}

// --- @osv ---

func (*BuiltinOsvPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseOsvInvocation(args)
	if err != nil {
		return i18n.T("plugins.osv.describe.default")
	}
	if cmd == "check" {
		pkg := describeStr(inner, "package")
		if ver := describeStr(inner, "version"); ver != "" {
			pkg += "@" + ver
		}
		return i18n.T("plugins.osv.describe.check", pkg)
	}
	path := describeStr(inner, "path")
	if path == "" {
		path = "."
	}
	return i18n.T("plugins.osv.describe.scan", path)
}

// --- @skill ---

func (*BuiltinSkillPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseSkillInvocation(args)
	if err != nil {
		return i18n.T("plugins.skill.describe.default")
	}
	name := describeStr(inner, "name")
	switch cmd {
	case "create":
		return i18n.T("plugins.skill.describe.create", name)
	case "update":
		return i18n.T("plugins.skill.describe.update", name)
	case "show":
		return i18n.T("plugins.skill.describe.show", name)
	case "remove":
		return i18n.T("plugins.skill.describe.remove", name)
	case "restore":
		return i18n.T("plugins.skill.describe.restore", name)
	case "stats":
		return i18n.T("plugins.skill.describe.stats")
	case "export":
		return i18n.T("plugins.skill.describe.export")
	case "import":
		return i18n.T("plugins.skill.describe.import")
	default:
		return i18n.T("plugins.skill.describe.list")
	}
}

// --- @send ---

func (*BuiltinSendPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseSendInvocation(args)
	if err != nil {
		return i18n.T("plugins.send.describe.default")
	}
	if cmd == "list" {
		return i18n.T("plugins.send.describe.list")
	}
	return i18n.T("plugins.send.describe.send", describeStr(inner, "to"))
}

// --- @moa ---

func (*BuiltinMoaPlugin) DescribeCall(args []string) string {
	cmd, _, err := parseMoaInvocation(args)
	if err != nil {
		return i18n.T("plugins.moa.describe.default")
	}
	if cmd == "list" {
		return i18n.T("plugins.moa.describe.list")
	}
	return i18n.T("plugins.moa.describe.ask")
}

// --- @secrets ---

func (*BuiltinSecretsPlugin) DescribeCall(args []string) string {
	cmd, in, err := parseSecretsInvocation(args)
	if err != nil {
		return i18n.T("plugins.secrets.describe.default")
	}
	switch cmd {
	case "check":
		return i18n.T("plugins.secrets.describe.check")
	case "rules":
		return i18n.T("plugins.secrets.describe.rules")
	}
	target := jsonString(in, "path", "dir", "file")
	if jsonBool(in, "staged") {
		target = "git index"
	} else if target == "" {
		target = "."
	}
	return i18n.T("plugins.secrets.describe.scan", describeTrunc(target, 50))
}

// --- @archive ---

func (*BuiltinArchivePlugin) DescribeCall(args []string) string {
	cmd, in, err := parseArchiveInvocation(args)
	if err != nil {
		return i18n.T("plugins.archive.describe.default")
	}
	file := describeTrunc(jsonString(in, "file", "path", "archive"), 50)
	switch cmd {
	case "read":
		return i18n.T("plugins.archive.describe.read", describeTrunc(jsonString(in, "entry", "name", "member"), 40), file)
	case "extract":
		return i18n.T("plugins.archive.describe.extract", file, describeTrunc(jsonString(in, "to", "dest", "dir"), 40))
	case "create":
		return i18n.T("plugins.archive.describe.create", file)
	}
	return i18n.T("plugins.archive.describe.list", file)
}
