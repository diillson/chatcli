/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Unified retention. Every durable store had its own lifecycle rule and
 * two of them (park snapshots, cost snapshots) only expired when a user
 * remembered to run a manual sweep or happened to trigger a save. The boot
 * pass below applies the machine-session TTL to those stores too, and
 * /config retention shows every policy in one place.
 */
package cli

import (
	"context"
	"fmt"
	"github.com/diillson/chatcli/pkg/auditchain"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/taskgraph"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// retentionReport is what one boot pass removed.
type retentionReport struct {
	Transcripts int // tenant transcripts removed (the shared set prunes on open)
	Sessions    int // tenant sessions past the window (the shared set's sessions are the user's)
	Pending     int // queued memory segments older than the window
	AuditFiles  int // rotated audit trail files past the retention window
	Parks       int
	Costs       int
}

// runRetentionPass expires park snapshots and cost snapshots older than the
// session TTL (CHATCLI_SESSION_TTL, 90 days by default; 0 keeps everything).
// Best-effort and quiet: a failure to sweep must never affect the session.
// Sessions, transcripts, CCR, task graphs and memory keep their own passes.
func (cli *ChatCLI) runRetentionPass() retentionReport {
	var rep retentionReport
	ttl := sessionTTLDuration()
	if ttl <= 0 {
		return rep
	}
	cutoff := time.Now().Add(-ttl)
	removed, errs := SweepStaleParks(cutoff)
	rep.Parks = removed
	if cli != nil && cli.logger != nil {
		for _, err := range errs {
			cli.logger.Debug("retention: park sweep error", zap.Error(err))
		}
	}
	if dir := costStoreDir(); dir != "" {
		rep.Costs = pruneCostSnapshotsOlderThan(dir, cutoff)
	}
	// Queued memory segments are a buffer, not an archive: past the
	// window they are stale conversation nobody will distill.
	rep.Pending += pruneFilesOlderThan(defaultPendingDir(), cutoff, ".json")
	// Tenant store sets (gateway isolation) follow the same policy, and
	// their parks and sessions expire too: a gateway principal's state is
	// transient by contract, unlike the user's own named sessions.
	if cli != nil && cli.stateRoot != "" {
		for _, root := range tenantRoots(cli.stateRoot) {
			rep.Costs += pruneCostSnapshotsOlderThan(filepath.Join(root, "costs"), cutoff)
			rep.Transcripts += pruneTranscripts(filepath.Join(root, transcriptDirName), ttl)
			rep.Parks += pruneFilesOlderThan(filepath.Join(root, "parked"), cutoff, "")
			rep.Sessions += pruneFilesOlderThan(filepath.Join(root, "sessions"), cutoff, ".json")
			rep.Pending += pruneFilesOlderThan(filepath.Join(root, "memory", "pending"), cutoff, ".json")
		}
	}
	// Rotated audit trails (the live file is never touched) follow the
	// same window; the operator keeps them elsewhere for longer retention.
	if path := os.Getenv(AuditLogPathEnv); path != "" && filepath.IsAbs(filepath.Clean(path)) {
		rep.AuditFiles = auditchain.PruneRotated(filepath.Clean(path), cutoff)
	}
	if cli != nil && cli.logger != nil && (rep.Parks > 0 || rep.Costs > 0 || rep.AuditFiles > 0 || rep.Sessions > 0 || rep.Pending > 0) {
		cli.logger.Info("retention pass", zap.Int("parks_removed", rep.Parks), zap.Int("cost_snapshots_removed", rep.Costs), zap.Int("audit_files_removed", rep.AuditFiles), zap.Int("sessions_removed", rep.Sessions), zap.Int("pending_removed", rep.Pending))
	}
	return rep
}

// pruneFilesOlderThan removes the regular files of dir (with the given
// extension, any when "") modified before cutoff; missing dir = 0.
func pruneFilesOlderThan(dir string, cutoff time.Time, ext string) int {
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || (ext != "" && filepath.Ext(e.Name()) != ext) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}

// retentionInterval is how often a long-lived process (the gateway
// daemon) re-runs the pass; the REPL runs it once at boot.
const retentionInterval = 6 * time.Hour

// runRetentionLoop repeats the retention pass until ctx ends.
func (cli *ChatCLI) runRetentionLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = retentionInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cli.runRetentionPass()
		}
	}
}

// pruneCostSnapshotsOlderThan removes cost snapshots modified before cutoff.
// Stray .tmp files (a crash between write and rename) go after an hour.
func pruneCostSnapshotsOlderThan(dir string, cutoff time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	tmpCutoff := time.Now().Add(-time.Hour)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		isJSON := len(name) > 5 && name[len(name)-5:] == ".json"
		isTmp := len(name) > 4 && name[len(name)-4:] == ".tmp"
		if !isJSON && !isTmp {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if (isJSON && info.ModTime().Before(cutoff)) || (isTmp && info.ModTime().Before(tmpCutoff)) {
			if os.Remove(dir+string(os.PathSeparator)+name) == nil {
				removed++
			}
		}
	}
	return removed
}

// showConfigRetention renders every store's lifecycle policy.
func (cli *ChatCLI) showConfigRetention() {
	sectionHeader("🗄", "cfg.section.retention.title", ColorCyan)
	p := uiPrefix(ColorCyan)

	ttl := sessionTTLDuration()
	ttlVal := i18n.T("cfg.val.retention.never")
	if ttl > 0 {
		ttlVal = i18n.T("cfg.val.retention.days", int(ttl.Hours()/24))
	}
	if os.Getenv("CHATCLI_SESSION_TTL") == "" {
		ttlVal = defaultMarker + ttlVal
	}

	subheader(p, "cfg.sub.retention.conversation")
	kv(p, i18n.T("cfg.kv.retention.sessions"), ttlVal+"  "+i18n.T("cfg.kv.retention.sessions_note", sessionAutosaveKeep()))
	kv(p, i18n.T("cfg.kv.retention.transcripts"), ttlVal)
	kv(p, i18n.T("cfg.kv.retention.parks"), ttlVal)
	kv(p, i18n.T("cfg.kv.retention.costs"), ttlVal)

	fmt.Println(p)
	subheader(p, "cfg.sub.retention.archives")
	ccrTTL := i18n.T("cfg.val.retention.hours", int(compress.DefaultCCRTTL.Hours()))
	if v := os.Getenv("CHATCLI_COMPRESSION_CCR_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			if d <= 0 {
				ccrTTL = i18n.T("cfg.val.retention.never")
			} else {
				ccrTTL = i18n.T("cfg.val.retention.hours", int(d.Hours()))
			}
		}
	} else {
		ccrTTL = defaultMarker + ccrTTL
	}
	kv(p, i18n.T("cfg.kv.retention.ccr"), ccrTTL+"  "+i18n.T("cfg.kv.retention.ccr_note", envOr("CHATCLI_COMPRESSION_CCR_MAX_MB")))
	kv(p, i18n.T("cfg.kv.retention.taskgraph"), defaultMarker+i18n.T("cfg.val.retention.days", int(taskgraph.DefaultRetention.Hours()/24)))

	fmt.Println(p)
	subheader(p, "cfg.sub.retention.memory")
	kv(p, i18n.T("cfg.kv.retention.daily_notes"), envOr("CHATCLI_MEMORY_RETENTION_DAYS")+" "+i18n.T("cfg.val.retention.days_unit"))
	kv(p, i18n.T("cfg.kv.retention.facts"), envOr("CHATCLI_MEMORY_MAX_FACTS")+" "+i18n.T("cfg.val.retention.cap_unit"))
	kv(p, i18n.T("cfg.kv.retention.hub"), envOr("CHATCLI_HUB_TTL_HOURS")+" "+i18n.T("cfg.val.retention.hours_unit"))

	fmt.Println(p)
	fmt.Println(colorize("  "+i18n.T("cfg.retention.note"), ColorGray))
	fmt.Println()
}

// sessionTTLDuration is the machine-session policy as a duration:
// CHATCLI_SESSION_TTL in days (90 by default), 0 disables expiry.
func sessionTTLDuration() time.Duration {
	days := 90
	if v := os.Getenv("CHATCLI_SESSION_TTL"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil {
			if n == 0 {
				return 0
			}
			if n > 0 {
				days = n
			}
		}
	}
	return time.Duration(days) * 24 * time.Hour
}
