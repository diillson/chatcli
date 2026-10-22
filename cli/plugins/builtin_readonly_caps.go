/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_readonly_caps.go — capability parity for the builtins that had
 * none. plugins.IsReadOnly fails closed, so a tool without ReadOnlyAware
 * sends EVERY call through the confirmation gate — a pure listing such as
 * @osv scan, @skill list or @image status prompted the user in coder mode
 * and was denied outright where nobody could answer. Each builtin here
 * now says which of its verbs only observe, which may run in parallel,
 * and what to show in the spinner.
 */
package plugins

import (
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// --- @osv ---------------------------------------------------------------

// IsReadOnly reports true for every @osv invocation: scan reads dependency
// manifests and check queries the OSV API; neither changes anything.
// An envelope that does not parse fails closed.
func (*BuiltinOsvPlugin) IsReadOnly(args []string) bool {
	if len(args) == 0 {
		return true // bare call = scan the current directory
	}
	_, _, err := parseOsvInvocation(args)
	return err == nil
}

// IsConcurrencySafe lets the orchestrator fan @osv out in parallel — it
// only reads files and queries a read-only API.
func (p *BuiltinOsvPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// --- @secrets -----------------------------------------------------------

// IsReadOnly reports true for every @secrets invocation that parses: scan,
// check and rules only read files (or the git index) and never write.
func (*BuiltinSecretsPlugin) IsReadOnly(args []string) bool {
	if len(args) == 0 {
		return true // bare call = scan the current directory
	}
	_, _, err := parseSecretsInvocation(args)
	return err == nil
}

// IsConcurrencySafe mirrors IsReadOnly: scans are independent reads.
func (p *BuiltinSecretsPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// --- @archive -----------------------------------------------------------

// IsReadOnly reports true for list and read; extract and create write to
// the workspace, and an envelope that does not parse fails closed.
func (*BuiltinArchivePlugin) IsReadOnly(args []string) bool {
	cmd, _, err := parseArchiveInvocation(args)
	return err == nil && (cmd == "list" || cmd == "read")
}

// IsConcurrencySafe mirrors IsReadOnly: listing and reading never collide.
func (p *BuiltinArchivePlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// --- @commands ----------------------------------------------------------

// IsReadOnly reports true: list and get only read the command catalog.
func (*BuiltinCommandsPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: catalog reads are independent.
func (*BuiltinCommandsPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall names the catalog operation in the spinner.
func (*BuiltinCommandsPlugin) DescribeCall(args []string) string {
	op, name, _ := parseCommandsArgs(strings.TrimSpace(strings.Join(args, " ")))
	switch op {
	case "get", "show", "expand", "run":
		return i18n.T("plugins.commands.describe.get", describeTrunc(name, 40))
	default:
		return i18n.T("plugins.commands.describe.list")
	}
}

// --- @image -------------------------------------------------------------

// IsReadOnly reports true for status and models (they only report the
// backend and its catalog); gen and edit spend provider credits and write
// files, use changes the session's model.
func (*BuiltinImagePlugin) IsReadOnly(args []string) bool {
	cmd, _, err := parseImageInvocation(args)
	if err != nil {
		return false
	}
	return cmd == "status" || cmd == "models"
}

// IsConcurrencySafe mirrors IsReadOnly.
func (p *BuiltinImagePlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// --- @speak -------------------------------------------------------------

// IsReadOnly reports true for status; say plays audio on the user's
// machine.
func (*BuiltinSpeakPlugin) IsReadOnly(args []string) bool {
	cmd, _, err := parseSpeakInvocation(args)
	return err == nil && cmd == "status"
}

// IsConcurrencySafe reports false: audio playback is serial by nature and
// status is instantaneous.
func (*BuiltinSpeakPlugin) IsConcurrencySafe(_ []string) bool { return false }

// --- @send --------------------------------------------------------------

// IsReadOnly reports true for list (the configured targets); send delivers
// a message to a person.
func (*BuiltinSendPlugin) IsReadOnly(args []string) bool {
	cmd, _, err := parseSendInvocation(args)
	return err == nil && cmd == "list"
}

// IsConcurrencySafe mirrors IsReadOnly.
func (p *BuiltinSendPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// --- @moa ---------------------------------------------------------------

// IsReadOnly reports true for list (the configured panel); ask spends
// tokens on several providers at once.
func (*BuiltinMoaPlugin) IsReadOnly(args []string) bool {
	cmd, _, err := parseMoaInvocation(args)
	return err == nil && cmd == "list"
}

// IsConcurrencySafe mirrors IsReadOnly.
func (p *BuiltinMoaPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// --- @skill -------------------------------------------------------------

// IsReadOnly reports true for list, show and stats; create, update, remove,
// restore, export and import change the skill library or write files.
func (*BuiltinSkillPlugin) IsReadOnly(args []string) bool {
	cmd, _, err := parseSkillInvocation(args)
	if err != nil {
		return false
	}
	switch cmd {
	case "list", "show", "stats":
		return true
	}
	return false
}

// IsConcurrencySafe mirrors IsReadOnly.
func (p *BuiltinSkillPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// --- @voice -------------------------------------------------------------

// IsReadOnly reports true for status; on, off and auto change the
// conversation's voice preference.
func (*BuiltinVoicePlugin) IsReadOnly(args []string) bool {
	cmd, err := parseVoiceCmd(args)
	return err == nil && cmd == "status"
}

// IsConcurrencySafe mirrors IsReadOnly.
func (p *BuiltinVoicePlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// DescribeCall names the voice setting change in the spinner.
func (*BuiltinVoicePlugin) DescribeCall(args []string) string {
	cmd, err := parseVoiceCmd(args)
	if err != nil {
		return i18n.T("plugins.voice.describe.default")
	}
	switch cmd {
	case "status":
		return i18n.T("plugins.voice.describe.status")
	case "on":
		return i18n.T("plugins.voice.describe.on")
	case "off":
		return i18n.T("plugins.voice.describe.off")
	default:
		return i18n.T("plugins.voice.describe.auto")
	}
}

// --- @compress ----------------------------------------------------------

// IsReadOnly reports true: compressing stores a key in the session's
// in-memory CCR store and stats reads it — nothing outside the session
// changes. It is the counterpart of @recall, which is read-only for the
// same reason: a confirmation prompt on it would stall the turn that
// needs it.
func (*BuiltinCompressPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: the CCR store is safe for concurrent use.
func (*BuiltinCompressPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall distinguishes stats from a compression in the spinner.
func (*BuiltinCompressPlugin) DescribeCall(args []string) string {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.EqualFold(payload, "stats") || strings.Contains(payload, `"stats"`) {
		return i18n.T("plugins.compress.describe.stats")
	}
	return i18n.T("plugins.compress.describe.compress")
}

// --- @recall ------------------------------------------------------------

// DescribeCall shows the key being recalled in the spinner.
func (*BuiltinRecallPlugin) DescribeCall(args []string) string {
	for _, cand := range bareKeyCandidates(strings.TrimSpace(strings.Join(args, " "))) {
		if k := strings.TrimSpace(cand); k != "" && !strings.HasPrefix(k, "{") {
			return i18n.T("plugins.recall.describe.key", describeTrunc(k, 40))
		}
	}
	return i18n.T("plugins.recall.describe.default")
}

// --- @memory ------------------------------------------------------------

// DescribeCall names the memory operation in the spinner.
func (*BuiltinMemoryPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseMemoryInvocation(args)
	if err != nil {
		return i18n.T("plugins.memory.describe.default")
	}
	switch cmd {
	case "remember":
		return i18n.T("plugins.memory.describe.remember", describeTrunc(describeStr(inner, "content"), 50))
	case "profile":
		return i18n.T("plugins.memory.describe.profile")
	case "forget":
		return i18n.T("plugins.memory.describe.forget", describeTrunc(describeStr(inner, "query"), 50))
	case "recall":
		return i18n.T("plugins.memory.describe.recall", describeTrunc(describeStr(inner, "query"), 50))
	case "timeline":
		return i18n.T("plugins.memory.describe.timeline")
	case "map":
		return i18n.T("plugins.memory.describe.map")
	case "neighbors":
		return i18n.T("plugins.memory.describe.neighbors", describeTrunc(describeStr(inner, "query"), 50))
	default:
		return i18n.T("plugins.memory.describe.default")
	}
}
