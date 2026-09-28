/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config web — the speech engines the web page uses.
 */
package cli

import (
	"fmt"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/transcription"
	"github.com/diillson/chatcli/llm/tts"
)

// webVoiceSourceKeys map a selection source to its label.
var webVoiceSourceKeys = map[string]string{
	transcription.SourceEnv:      "cfg.web.voice_src_env",
	transcription.SourceEmbedded: "cfg.web.voice_src_embedded",
	transcription.SourceAuto:     "cfg.web.voice_src_auto",
	transcription.SourceNone:     "cfg.web.voice_src_none",
}

// showConfigWebVoice prints, per direction, the engine the page uses and
// the state of the embedded engine it prefers (the same resolution the web
// server runs at start: explicit env first, then embedded, then fallback).
func (cli *ChatCLI) showConfigWebVoice(p string) {
	in := transcription.SelectPreferEmbedded(cli.logger)
	out := tts.SelectPreferEmbedded(cli.logger)
	inName := ""
	if !transcription.IsNull(in.Active) {
		inName = in.Active.Name()
	}
	outName := ""
	if !tts.IsNull(out.Active) {
		outName = out.Active.Name()
	}
	fmt.Println(p)
	kv(p, i18n.T("cfg.web.voice_in"), webVoiceActive(inName, in.Source))
	kv(p, i18n.T("cfg.web.voice_offline"), webVoiceEmbedded(in.Embedded.Name, in.Embedded.Supported, in.Embedded.Installed, in.Embedded.DownloadBytes))
	kv(p, i18n.T("cfg.web.voice_out"), webVoiceActive(outName, out.Source))
	kv(p, i18n.T("cfg.web.voice_offline"), webVoiceEmbedded(out.Embedded.Name, out.Embedded.Supported, out.Embedded.Installed, out.Embedded.DownloadBytes))
	fmt.Println(p + colorize(i18n.T("cfg.web.voice_about"), ColorGray))
}

func webVoiceActive(name, source string) string {
	if name == "" {
		name = i18n.T("cfg.web.voice_none")
	}
	key, ok := webVoiceSourceKeys[source]
	if !ok {
		key = "cfg.web.voice_src_env"
	}
	return i18n.T("cfg.web.voice_active", name, i18n.T(key))
}

func webVoiceEmbedded(name string, supported, installed bool, downloadBytes int64) string {
	switch {
	case !supported:
		return i18n.T("cfg.web.voice_unsupported")
	case installed:
		return i18n.T("cfg.web.voice_installed", name)
	}
	return i18n.T("cfg.web.voice_missing", name, (downloadBytes+500_000)/1_000_000)
}
