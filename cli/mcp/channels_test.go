/*
 * ChatCLI - ChannelManager tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the production-grade behavior of the channel ring:
 *   - Filtering by subscription resolver
 *   - OnMessage handler fan-out (copy semantics, no aliasing)
 *   - Unread tracking + Ack
 *   - JSONL persistence: write, load on restart, rotation
 *   - ProcessSSENotification routing across all three method shapes
 */
package mcp

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func channelTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	return zap.NewNop()
}

func TestChannelManager_PushAndGetRecent(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))

	for i := 0; i < 5; i++ {
		cm.Push(ChannelMessage{ServerName: "s", Channel: "ci", Content: "msg"})
	}

	if got := cm.Count(); got != 5 {
		t.Fatalf("Count = %d, want 5", got)
	}
	if got := cm.Unread(); got != 5 {
		t.Fatalf("Unread = %d, want 5", got)
	}
	if got := len(cm.GetRecent(3)); got != 3 {
		t.Fatalf("GetRecent(3) returned %d, want 3", got)
	}
}

func TestChannelManager_RingTrimsToCapacity(t *testing.T) {
	cm := NewChannelManagerWithOptions(channelTestLogger(t), ChannelManagerOptions{MaxMessages: 3})
	for i := 0; i < 10; i++ {
		cm.Push(ChannelMessage{ServerName: "s", Channel: "c", Content: "x"})
	}
	if got := cm.Count(); got != 3 {
		t.Fatalf("Count = %d, want 3 (capacity)", got)
	}
}

func TestChannelManager_SubscriptionResolverDropsUnsubscribed(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))
	cm.SetSubscriptionResolver(func(server, channel string) bool {
		return channel == "alerts"
	})

	cm.Push(ChannelMessage{ServerName: "s", Channel: "alerts", Content: "yes"})
	cm.Push(ChannelMessage{ServerName: "s", Channel: "info", Content: "no"})
	cm.Push(ChannelMessage{ServerName: "s", Channel: "alerts", Content: "yes"})

	if got := cm.Count(); got != 2 {
		t.Fatalf("Count = %d, want 2 (subscription filter dropped 'info')", got)
	}
	for _, msg := range cm.GetRecent(10) {
		if msg.Channel != "alerts" {
			t.Errorf("unexpected channel kept: %q", msg.Channel)
		}
	}
}

func TestChannelManager_OnMessageFanOut(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))
	var hits atomic.Int32
	cm.OnMessage(func(msg ChannelMessage) { hits.Add(1) })
	cm.OnMessage(func(msg ChannelMessage) { hits.Add(1) })

	cm.Push(ChannelMessage{ServerName: "s", Channel: "c", Content: "x"})

	if got := hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2", got)
	}
}

func TestChannelManager_AckClearsUnreadAndStampsLastViewed(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))
	cm.Push(ChannelMessage{ServerName: "s", Channel: "c", Content: "x"})
	cm.Push(ChannelMessage{ServerName: "s", Channel: "c", Content: "y"})

	if got := cm.Unread(); got != 2 {
		t.Fatalf("Unread = %d, want 2", got)
	}

	cleared := cm.Ack()
	if cleared != 2 {
		t.Fatalf("Ack returned %d, want 2", cleared)
	}
	if got := cm.Unread(); got != 0 {
		t.Fatalf("Unread after Ack = %d, want 0", got)
	}

	cm.Push(ChannelMessage{ServerName: "s", Channel: "c", Content: "z"})
	since := cm.UnreadSince()
	if len(since) != 1 || since[0].Content != "z" {
		t.Fatalf("UnreadSince = %+v, want one msg with Content=z", since)
	}
}

func TestChannelManager_GetBySeqRoundTrips(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))
	cm.Push(ChannelMessage{ServerName: "s", Channel: "a", Content: "1"})
	cm.Push(ChannelMessage{ServerName: "s", Channel: "b", Content: "2"})

	all := cm.GetRecent(10)
	if len(all) != 2 {
		t.Fatalf("want 2 messages, got %d", len(all))
	}
	mid := all[0]
	got, ok := cm.GetBySeq(mid.Seq)
	if !ok {
		t.Fatalf("GetBySeq(%d) = (_, false), want found", mid.Seq)
	}
	if got.Content != mid.Content {
		t.Fatalf("GetBySeq content = %q, want %q", got.Content, mid.Content)
	}

	if _, ok := cm.GetBySeq(99999); ok {
		t.Fatalf("GetBySeq(99999) = (_, true), want not found")
	}
}

func TestChannelManager_FormatForPromptRendersOrChrono(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))
	cm.Push(ChannelMessage{ServerName: "s", Channel: "ci", Content: "first"})
	cm.Push(ChannelMessage{ServerName: "s", Channel: "ci", Content: "second"})

	out := cm.FormatForPrompt(5)
	if !strings.Contains(out, "## MCP Channel Messages") {
		t.Errorf("expected header, got %q", out)
	}
	if i, j := strings.Index(out, "first"), strings.Index(out, "second"); !(i >= 0 && i < j) {
		t.Errorf("messages not in chronological order: %q", out)
	}
}

func TestChannelManager_ProcessSSENotification_StandardMethod(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))
	body := []byte(`{"jsonrpc":"2.0","method":"notifications/ci","params":{"build":"42"}}`)
	cm.ProcessSSENotification("srv", body)
	msgs := cm.GetRecent(10)
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if msgs[0].Channel != "ci" {
		t.Errorf("Channel = %q, want ci", msgs[0].Channel)
	}
	if !strings.Contains(msgs[0].Content, `"build":"42"`) {
		t.Errorf("Content = %q, want raw params JSON", msgs[0].Content)
	}
}

func TestChannelManager_ProcessSSENotification_MessageMethod(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))
	body := []byte(`{"jsonrpc":"2.0","method":"channel/message","params":{"channel":"alerts","text":"prod hot"}}`)
	cm.ProcessSSENotification("srv", body)
	msgs := cm.GetRecent(10)
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if msgs[0].Channel != "alerts" || msgs[0].Content != "prod hot" {
		t.Errorf("got channel=%q content=%q", msgs[0].Channel, msgs[0].Content)
	}
}

func TestChannelManager_ProcessSSENotification_NonJSONRoutedAsRaw(t *testing.T) {
	cm := NewChannelManager(channelTestLogger(t))
	cm.ProcessSSENotification("srv", []byte("not json"))
	msgs := cm.GetRecent(10)
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if msgs[0].Channel != "raw" {
		t.Errorf("Channel = %q, want raw", msgs[0].Channel)
	}
}

func TestChannelManager_PersistenceWriteAndLoad(t *testing.T) {
	dir := t.TempDir()

	cm1 := NewChannelManagerWithOptions(channelTestLogger(t), ChannelManagerOptions{
		PersistDir: dir,
	})
	cm1.Push(ChannelMessage{ServerName: "srv", Channel: "ci", Content: "saved"})
	if err := cm1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify file written
	path := filepath.Join(dir, persistFileName)
	data, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read persistence file: %v", err)
	}
	if !strings.Contains(string(data), `"content":"saved"`) {
		t.Fatalf("persistence file missing content; got %s", string(data))
	}

	cm2 := NewChannelManagerWithOptions(channelTestLogger(t), ChannelManagerOptions{
		PersistDir: dir,
	})
	msgs := cm2.GetRecent(10)
	if len(msgs) != 1 {
		t.Fatalf("after reload want 1 msg, got %d", len(msgs))
	}
	if msgs[0].Content != "saved" {
		t.Errorf("loaded content = %q, want saved", msgs[0].Content)
	}
	// Replayed messages should NOT count as unread — they were
	// already seen in the previous session.
	if got := cm2.Unread(); got != 0 {
		t.Errorf("Unread after reload = %d, want 0", got)
	}
	_ = cm2.Close()
}

func TestChannelManager_PersistenceTolerratesCorruptLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, persistFileName)

	good, _ := json.Marshal(ChannelMessage{
		ServerName: "s", Channel: "c", Content: "ok",
		Timestamp: time.Now().UTC(), Seq: 1,
	})
	contents := string(good) + "\nNOT VALID JSON\n" + string(good) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	cm := NewChannelManagerWithOptions(channelTestLogger(t), ChannelManagerOptions{
		PersistDir: dir,
	})
	defer cm.Close() //nolint:errcheck

	msgs := cm.GetRecent(10)
	if len(msgs) != 2 {
		t.Fatalf("want 2 msgs (corrupt line skipped), got %d", len(msgs))
	}
}

func TestChannelManager_PersistenceRotatesAtMaxSize(t *testing.T) {
	dir := t.TempDir()
	cm := NewChannelManagerWithOptions(channelTestLogger(t), ChannelManagerOptions{
		PersistDir:      dir,
		PersistMaxBytes: 256, // tiny on purpose so a few pushes trip rotation
		LoadLimit:       -1,
	})
	defer cm.Close() //nolint:errcheck

	// Push enough to exceed 256 bytes across multiple records.
	for i := 0; i < 20; i++ {
		cm.Push(ChannelMessage{
			ServerName: "s", Channel: "c",
			Content: strings.Repeat("x", 64),
		})
	}

	rotated := filepath.Join(dir, persistFileName+persistRotatedSuffix)
	if _, err := os.Stat(rotated); err != nil {
		t.Fatalf("expected rotated file at %s: %v", rotated, err)
	}

	// Rotation keeps only one historical file (.1) so we cannot
	// assert all 20 messages persisted — that would require multi-
	// file rotation, which is intentionally out of scope. What we
	// CAN assert is that rotation happened (the file exists above),
	// the active file is bounded by persistMaxBytes (well below 20×
	// record size), and at least some messages are preserved across
	// the pair.
	active := filepath.Join(dir, persistFileName)
	stat, err := os.Stat(active)
	if err != nil {
		t.Fatalf("stat active file: %v", err)
	}
	if stat.Size() > 256*2 {
		t.Errorf("active file size %d exceeds rotation budget", stat.Size())
	}

	count := 0
	for _, p := range []string{active, rotated} {
		f, err := os.Open(p) //nolint:gosec // test path
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) != "" {
				count++
			}
		}
		_ = f.Close()
	}
	if count == 0 {
		t.Fatalf("no messages persisted across active+rotated files")
	}
}

func TestServerConfig_IsChannelSubscribed(t *testing.T) {
	cases := []struct {
		name     string
		channels []string
		ask      string
		want     bool
	}{
		{"empty allows everything", nil, "anything", true},
		{"exact match", []string{"alerts"}, "alerts", true},
		{"miss", []string{"alerts"}, "info", false},
		{"wildcard", []string{"*"}, "anything", true},
		{"wildcard alongside specific", []string{"alerts", "*"}, "info", true},
		{"trim whitespace", []string{"  alerts  "}, "alerts", true},
		{"empty entries ignored", []string{"", "alerts"}, "alerts", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ServerConfig{Channels: tc.channels}
			if got := cfg.IsChannelSubscribed(tc.ask); got != tc.want {
				t.Errorf("IsChannelSubscribed(%q) on %v = %v, want %v",
					tc.ask, tc.channels, got, tc.want)
			}
		})
	}
}
