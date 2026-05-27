package hub

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	st, err := OpenSQLiteStore(path, nil)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestResolveCreatesAndIsStable(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	first, err := st.Resolve(ctx, "alice")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if first == "" {
		t.Fatal("expected a conversation id")
	}
	again, err := st.Resolve(ctx, "alice")
	if err != nil {
		t.Fatalf("Resolve again: %v", err)
	}
	if again != first {
		t.Fatalf("active conversation changed: %q != %q", again, first)
	}
}

func TestNewConversationRotatesPointer(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	old, _ := st.Resolve(ctx, "alice")
	fresh, err := st.NewConversation(ctx, "alice")
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	if fresh == old {
		t.Fatal("NewConversation returned the same conversation")
	}
	now, _ := st.Resolve(ctx, "alice")
	if now != fresh {
		t.Fatalf("pointer not rotated: got %q want %q", now, fresh)
	}
}

func TestAppendAssignsMonotonicSeqAndReadOrders(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	conv, _ := st.Resolve(ctx, "alice")

	var lastSeq int64
	for i := 0; i < 5; i++ {
		ev, err := st.Append(ctx, models.ConversationEvent{
			ConvID:    conv,
			Principal: "alice",
			Channel:   "telegram",
			Role:      models.ConvRoleUser,
			Content:   fmt.Sprintf("msg %d", i),
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if ev.Seq <= lastSeq {
			t.Fatalf("seq not monotonic: %d after %d", ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
	}

	got, err := st.Read(ctx, conv, 0, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 events, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("Read not ordered by seq at index %d", i)
		}
	}
}

func TestReadSinceSeqResume(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	conv, _ := st.Resolve(ctx, "alice")

	var seqs []int64
	for i := 0; i < 4; i++ {
		ev, _ := st.Append(ctx, models.ConversationEvent{
			ConvID: conv, Principal: "alice", Channel: "local", Role: models.ConvRoleUser, Content: fmt.Sprintf("m%d", i),
		})
		seqs = append(seqs, ev.Seq)
	}

	// A client that last saw seqs[1] resumes and must get only the tail.
	tail, err := st.Read(ctx, conv, seqs[1], 0)
	if err != nil {
		t.Fatalf("Read tail: %v", err)
	}
	if len(tail) != 2 {
		t.Fatalf("expected 2 events after seq %d, got %d", seqs[1], len(tail))
	}
	if tail[0].Seq != seqs[2] {
		t.Fatalf("resume started at wrong seq: %d want %d", tail[0].Seq, seqs[2])
	}
}

func TestAppendDedupeByClientMsgID(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	conv, _ := st.Resolve(ctx, "alice")

	first, err := st.Append(ctx, models.ConversationEvent{
		ConvID: conv, Principal: "alice", Channel: "local", Role: models.ConvRoleUser, Content: "hello", ClientMsgID: "abc",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	// A retry with the same client id must be a no-op returning the same event.
	dup, err := st.Append(ctx, models.ConversationEvent{
		ConvID: conv, Principal: "alice", Channel: "local", Role: models.ConvRoleUser, Content: "hello", ClientMsgID: "abc",
	})
	if err != nil {
		t.Fatalf("Append dup: %v", err)
	}
	if dup.Seq != first.Seq {
		t.Fatalf("dedupe failed: dup seq %d != %d", dup.Seq, first.Seq)
	}
	all, _ := st.Read(ctx, conv, 0, 0)
	if len(all) != 1 {
		t.Fatalf("expected 1 stored event after dedupe, got %d", len(all))
	}
}

func TestConcurrentAppendsNoLoss(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	conv, _ := st.Resolve(ctx, "alice")

	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_, err := st.Append(ctx, models.ConversationEvent{
					ConvID:    conv,
					Principal: "alice",
					Channel:   fmt.Sprintf("chan-%d", w),
					Role:      models.ConvRoleUser,
					Content:   fmt.Sprintf("w%d-i%d", w, i),
				})
				if err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent Append: %v", err)
	}

	all, err := st.Read(ctx, conv, 0, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(all) != writers*perWriter {
		t.Fatalf("lost events: got %d want %d", len(all), writers*perWriter)
	}
	// Seq must be strictly increasing across all writers — proof the writes serialized.
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("seq collision/disorder at %d", i)
		}
	}
}

func TestBindingResolveAndQuarantine(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Unbound identity is quarantined.
	if _, err := st.ResolvePrincipal(ctx, "telegram", "999"); !errors.Is(err, ErrUnboundChannel) {
		t.Fatalf("expected ErrUnboundChannel, got %v", err)
	}

	if err := st.Bind(ctx, "telegram", "999", "alice"); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if err := st.Bind(ctx, "slack", "U1", "alice"); err != nil {
		t.Fatalf("Bind slack: %v", err)
	}
	p, err := st.ResolvePrincipal(ctx, "telegram", "999")
	if err != nil || p != "alice" {
		t.Fatalf("ResolvePrincipal = %q,%v want alice,nil", p, err)
	}

	// Two channels of the same principal resolve to one shared conversation —
	// this is the core cross-channel continuity guarantee.
	convTel, _ := st.Resolve(ctx, p)
	convSlack, _ := st.Resolve(ctx, p)
	if convTel != convSlack {
		t.Fatalf("same principal got different conversations: %q vs %q", convTel, convSlack)
	}

	bindings, err := st.ListBindings(ctx, "alice")
	if err != nil {
		t.Fatalf("ListBindings: %v", err)
	}
	if len(bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(bindings))
	}
}

func TestOwnerOf(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	conv, _ := st.Resolve(ctx, "alice")

	owner, err := st.OwnerOf(ctx, conv)
	if err != nil || owner != "alice" {
		t.Fatalf("OwnerOf = %q,%v want alice,nil", owner, err)
	}
	if _, err := st.OwnerOf(ctx, "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown conversation")
	}
}
