/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * identity.go — how the launched browser presents itself, where its
 * downloads land, and the network answers the model can read back.
 *
 * A headless Chrome announces itself as "HeadlessChrome" in its user agent
 * and client hints, which many sites answer with a block page before any
 * content; the session presents the same browser the way its headed
 * counterpart would, with the user's language. Downloads the page starts go
 * to the session's artifact directory (never the user's Downloads folder)
 * and are listed on request; response bodies are fetched on demand, bounded.
 */
package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/pkg/textfold"
	"github.com/diillson/chatcli/utils"
)

// ArtifactDir is where screenshots, PDFs and downloads of the session go:
// the agent's scratch directory (CHATCLI_AGENT_TMPDIR) or, without one, an
// owner-only directory under the user's state root — never a fixed name in
// the shared temp directory that another local user could pre-create.
func ArtifactDir() (string, error) {
	if scratch := strings.TrimSpace(os.Getenv("CHATCLI_AGENT_TMPDIR")); scratch != "" {
		return scratch, nil
	}
	return utils.PrivateStateDir("browser")
}

// headlessUAMarker is what a headless Chrome puts in its user agent.
const headlessUAMarker = "HeadlessChrome/"

// applyIdentity makes a headless page session present itself like the
// headed browser it is: the user agent without the headless marker, client
// hint brands to match, the platform of this machine and the user's
// language. A headed or attached session already looks like itself.
// Failures are ignored: the page still works, it just looks headless.
func (s *Session) applyIdentity(ctx context.Context, sessionID string) {
	if !s.headless || s.attached {
		return
	}
	res, err := s.conn.call(ctx, "", "Browser.getVersion", nil)
	if err != nil {
		return
	}
	var v struct {
		UserAgent string `json:"userAgent"`
		Product   string `json:"product"` // e.g. HeadlessChrome/131.0.6778.85
	}
	if json.Unmarshal(res, &v) != nil || v.UserAgent == "" {
		return
	}
	ua := strings.ReplaceAll(v.UserAgent, headlessUAMarker, "Chrome/")
	full := strings.TrimPrefix(strings.TrimPrefix(v.Product, "HeadlessChrome/"), "Chrome/")
	major := full
	if i := strings.IndexByte(major, '.'); i > 0 {
		major = major[:i]
	}
	params := map[string]interface{}{
		"userAgent":      ua,
		"acceptLanguage": acceptLanguage(),
		"platform":       navigatorPlatform(),
	}
	if major != "" {
		params["userAgentMetadata"] = map[string]interface{}{
			"brands": []map[string]string{
				{"brand": "Chromium", "version": major},
				{"brand": "Google Chrome", "version": major},
				{"brand": "Not_A Brand", "version": "24"},
			},
			"fullVersion":     full,
			"platform":        hintPlatform(),
			"platformVersion": "",
			"architecture":    hintArchitecture(),
			"model":           "",
			"mobile":          false,
		}
	}
	_, _ = s.conn.call(ctx, sessionID, "Network.setUserAgentOverride", params)
}

// navigatorPlatform is navigator.platform for this machine.
func navigatorPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacIntel"
	case "windows":
		return "Win32"
	default:
		return "Linux x86_64"
	}
}

// hintPlatform is the Sec-CH-UA-Platform value for this machine.
func hintPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	default:
		return "Linux"
	}
}

// hintArchitecture is the Sec-CH-UA-Arch value for this machine.
func hintArchitecture() string {
	if strings.HasPrefix(runtime.GOARCH, "arm") {
		return "arm"
	}
	return "x86"
}

// enableDownloads sends the page's downloads to ArtifactDir and subscribes
// to their progress. Only for a browser the session launched: an attached
// browser belongs to the user, whose download folder stays theirs.
func (s *Session) enableDownloads(ctx context.Context) {
	if s.attached {
		return
	}
	dir, err := ArtifactDir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_, _ = s.conn.call(ctx, "", "Browser.setDownloadBehavior", map[string]interface{}{
		"behavior": "allow", "downloadPath": dir, "eventsEnabled": true,
	})
}

// handleDownloadEvent records Browser.downloadWillBegin / downloadProgress.
func (s *Session) handleDownloadEvent(method string, params json.RawMessage) {
	var ev struct {
		GUID              string `json:"guid"`
		URL               string `json:"url"`
		SuggestedFilename string `json:"suggestedFilename"`
		FilePath          string `json:"filePath"`
		State             string `json:"state"`
		TotalBytes        int64  `json:"totalBytes"`
		ReceivedBytes     int64  `json:"receivedBytes"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.GUID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.downloadGUIDs == nil {
		s.downloadGUIDs = make(map[string]int)
	}
	idx, known := s.downloadGUIDs[ev.GUID]
	if method == "Browser.downloadWillBegin" {
		if known {
			return
		}
		file := ""
		if dir, err := ArtifactDir(); err == nil && ev.SuggestedFilename != "" {
			file = filepath.Join(dir, filepath.Base(ev.SuggestedFilename))
		}
		s.downloads = append(s.downloads, DownloadInfo{URL: ev.URL, File: file, State: "inProgress"})
		if len(s.downloads) > maxDownloads {
			s.downloads = s.downloads[1:]
			for g, i := range s.downloadGUIDs {
				if i == 0 {
					delete(s.downloadGUIDs, g)
				} else {
					s.downloadGUIDs[g] = i - 1
				}
			}
		}
		s.downloadGUIDs[ev.GUID] = len(s.downloads) - 1
		return
	}
	if !known || idx >= len(s.downloads) {
		return
	}
	d := &s.downloads[idx]
	if ev.State != "" {
		d.State = ev.State
	}
	if ev.FilePath != "" {
		d.File = ev.FilePath
	}
	d.Received, d.Total = ev.ReceivedBytes, ev.TotalBytes
}

// Downloads lists the files the page downloaded during this session,
// oldest first.
func (s *Session) Downloads() []DownloadInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DownloadInfo, len(s.downloads))
	copy(out, s.downloads)
	return out
}

// responseBodyCap bounds a response body handed to the model.
const responseBodyCap = 8000

// ResponseBody returns the most recent captured response whose URL contains
// filter and its body (text, or a note for binary), capped at maxBytes
// (0 = default). Bodies are only available while the page that received
// them is still loaded.
func (s *Session) ResponseBody(ctx context.Context, filter string, maxBytes int) (NetworkEntry, string, error) {
	span := s.beginAction(ctx, "network-body")
	entry, body, err := s.doResponseBody(ctx, filter, maxBytes)
	span.EndErr(err)
	return entry, body, err
}

// doResponseBody is ResponseBody under its dashboard node.
func (s *Session) doResponseBody(ctx context.Context, filter string, maxBytes int) (NetworkEntry, string, error) {
	if maxBytes <= 0 {
		maxBytes = responseBodyCap
	}
	f := strings.ToLower(strings.TrimSpace(filter))
	s.mu.Lock()
	var entry NetworkEntry
	var requestID string
	for i := len(s.network) - 1; i >= 0; i-- {
		if s.networkIDs[i] == "" {
			continue
		}
		if f == "" || strings.Contains(strings.ToLower(s.network[i].URL), f) {
			entry, requestID = s.network[i], s.networkIDs[i]
			break
		}
	}
	s.mu.Unlock()
	if requestID == "" {
		return NetworkEntry{}, "", fmt.Errorf("no captured response matches %q — `network` lists what the page received", filter)
	}
	res, err := s.call(ctx, "Network.getResponseBody", map[string]interface{}{"requestId": requestID})
	if err != nil {
		return entry, "", fmt.Errorf("the body of %s is no longer available (the page moved on or the browser dropped it): %w", entry.URL, err)
	}
	var out struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return entry, "", err
	}
	body := out.Body
	if out.Base64Encoded {
		raw, err := base64.StdEncoding.DecodeString(out.Body)
		if err != nil {
			return entry, "", fmt.Errorf("decode body: %w", err)
		}
		if !isMostlyText(raw) {
			return entry, fmt.Sprintf("(binary body, %d bytes — not shown)", len(raw)), nil
		}
		body = string(raw)
	}
	if len(body) > maxBytes {
		body = textfold.Cut(body, maxBytes) + fmt.Sprintf("\n… (body truncated at %d of %d bytes)", maxBytes, len(out.Body))
	}
	return entry, body, nil
}

// isMostlyText reports whether raw reads as text (no NUL, few control
// bytes in its head).
func isMostlyText(raw []byte) bool {
	head := raw
	if len(head) > 512 {
		head = head[:512]
	}
	control := 0
	for _, b := range head {
		if b == 0 {
			return false
		}
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			control++
		}
	}
	return control*10 < len(head)+1
}
