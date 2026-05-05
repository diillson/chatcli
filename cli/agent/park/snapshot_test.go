package park

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

// withTempDir redirects the snapshot directory to a temp dir for the
// life of a test. Returns the directory path so the test can inspect.
func withTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(envOverride, dir)
	return dir
}

func TestSnapshot_RoundTrip(t *testing.T) {
	dir := withTempDir(t)

	want := &Snapshot{
		Token:   NewToken(),
		History: []models.Message{{Role: "user", Content: "hello"}},
		Park: Request{
			Mode:  ModeDelay,
			Delay: 5 * time.Minute,
			Note:  "ci wait",
		},
		IsCoderMode:    true,
		ToolCallsExecd: 7,
	}
	if err := want.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, want.Token+".json")); err != nil {
		t.Fatalf("snapshot file not present: %v", err)
	}

	got, err := Load(want.Token)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Token != want.Token || len(got.History) != 1 || got.History[0].Content != "hello" {
		t.Fatalf("loaded snapshot does not match: %+v", got)
	}
	if got.Park.Mode != ModeDelay || got.Park.Delay != 5*time.Minute || got.Park.Note != "ci wait" {
		t.Fatalf("park request mismatch: %+v", got.Park)
	}
	if !got.IsCoderMode || got.ToolCallsExecd != 7 {
		t.Fatalf("flags/counters mismatch: %+v", got)
	}
}

func TestSnapshot_AtomicRename(t *testing.T) {
	dir := withTempDir(t)

	s := &Snapshot{Token: NewToken(), Park: Request{Mode: ModeDelay, Delay: time.Minute}}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// No tmp file should remain after a successful save.
	tmp := filepath.Join(dir, s.Token+".json.tmp")
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tmp file leaked: %v", err)
	}
}

func TestSnapshot_NotFound(t *testing.T) {
	withTempDir(t)
	_, err := Load("absentbutvalidtoken1234")
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("expected ErrSnapshotNotFound, got %v", err)
	}
}

func TestSnapshot_InvalidToken(t *testing.T) {
	withTempDir(t)
	_, err := Load("../etc/passwd")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	short := &Snapshot{Token: "a", Park: Request{Mode: ModeDelay, Delay: time.Minute}}
	if err := short.Save(); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken on short save, got %v", err)
	}
}

func TestSnapshot_SchemaMismatch(t *testing.T) {
	dir := withTempDir(t)
	tok := NewToken()
	path := filepath.Join(dir, tok+".json")
	// Forge a file with an unsupported version.
	if err := os.WriteFile(path, []byte(`{"version":99,"token":"`+tok+`"}`), 0o600); err != nil {
		t.Fatalf("write forged file: %v", err)
	}
	_, err := Load(tok)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("expected ErrSchemaMismatch, got %v", err)
	}
}

func TestSnapshot_DeleteIdempotent(t *testing.T) {
	withTempDir(t)
	tok := NewToken()
	if err := Delete(tok); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	s := &Snapshot{Token: tok, Park: Request{Mode: ModeDelay, Delay: time.Minute}}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Delete(tok); err != nil {
		t.Fatalf("delete present: %v", err)
	}
	if _, err := Load(tok); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("expected NotFound after delete, got %v", err)
	}
}

func TestSnapshot_ListSorted(t *testing.T) {
	withTempDir(t)
	for i := 0; i < 3; i++ {
		s := &Snapshot{
			Token:     NewToken(),
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour),
			Park:      Request{Mode: ModeDelay, Delay: time.Minute},
		}
		if err := s.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	got, errs := List()
	if len(errs) > 0 {
		t.Fatalf("List errs: %v", errs)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].CreatedAt.Before(got[i].CreatedAt) {
			t.Fatalf("not sorted desc by CreatedAt: %v < %v", got[i-1].CreatedAt, got[i].CreatedAt)
		}
	}
}

func TestRequest_Validate(t *testing.T) {
	cases := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{"delay ok", Request{Mode: ModeDelay, Delay: time.Minute}, false},
		{"delay zero", Request{Mode: ModeDelay, Delay: 0}, true},
		{"delay too long", Request{Mode: ModeDelay, Delay: MaxParkDuration + time.Hour}, true},
		{"until past", Request{Mode: ModeUntil, Until: time.Now().Add(-time.Minute)}, true},
		{"until future", Request{Mode: ModeUntil, Until: time.Now().Add(time.Minute)}, false},
		{"for_url no url", Request{Mode: ModeForURL, Interval: 30 * time.Second, Deadline: time.Now().Add(time.Minute)}, true},
		{"for_url bad scheme", Request{Mode: ModeForURL, URL: "ftp://x", Interval: 30 * time.Second, Deadline: time.Now().Add(time.Minute)}, true},
		{"for_url interval too small", Request{Mode: ModeForURL, URL: "http://x", Interval: time.Second, Deadline: time.Now().Add(time.Minute)}, true},
		{"for_url ok", Request{Mode: ModeForURL, URL: "https://x", Interval: 30 * time.Second, Deadline: time.Now().Add(time.Minute)}, false},
		{"for_cmd no cmd", Request{Mode: ModeForCmd, Interval: 30 * time.Second, Deadline: time.Now().Add(time.Minute)}, true},
		{"unknown mode", Request{Mode: "bogus"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.req.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func TestAsParkError(t *testing.T) {
	r := Request{Mode: ModeDelay, Delay: time.Minute}
	got, ok := AsParkError(NewParkError(r))
	if !ok {
		t.Fatalf("AsParkError did not unwrap")
	}
	if got.Mode != ModeDelay {
		t.Fatalf("unwrapped wrong request: %+v", got)
	}
	_, ok = AsParkError(errors.New("boring"))
	if ok {
		t.Fatalf("AsParkError matched non-park error")
	}
}
