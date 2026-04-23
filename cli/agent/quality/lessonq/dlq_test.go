package lessonq

import (
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
)

func TestDLQ_PutListRemove(t *testing.T) {
	d, err := NewDLQ(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("NewDLQ: %v", err)
	}
	defer d.Close()

	job := LessonJob{
		ID:         "failed-1",
		Request:    quality.LessonRequest{Task: "x", Trigger: "error", Attempt: "a"},
		EnqueuedAt: time.Now(),
		LastError:  "provider 500",
		Attempts:   5,
	}
	if err := d.Put(job); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if d.Count() != 1 {
		t.Fatalf("Count: want 1; got %d", d.Count())
	}
	list, _ := d.List()
	if len(list) != 1 || list[0].LastError != "provider 500" {
		t.Fatalf("List: %+v", list)
	}
	if err := d.Remove("failed-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if d.Count() != 0 {
		t.Fatalf("Count after remove: want 0; got %d", d.Count())
	}
}

func TestDLQ_Pop(t *testing.T) {
	d, _ := NewDLQ(t.TempDir(), nil, nil)
	defer d.Close()

	orig := LessonJob{
		ID:         "popme",
		Request:    quality.LessonRequest{Task: "t", Trigger: "error", Attempt: "a"},
		EnqueuedAt: time.Now(),
		Attempts:   4,
	}
	_ = d.Put(orig)

	got, ok, err := d.Pop("popme")
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if !ok {
		t.Fatal("Pop: expected ok=true")
	}
	if got.ID != "popme" || got.Attempts != 4 {
		t.Fatalf("Pop: wrong entry %+v", got)
	}
	if d.Count() != 0 {
		t.Fatalf("Pop should have removed the entry; count=%d", d.Count())
	}
}

func TestDLQ_PopMissing(t *testing.T) {
	d, _ := NewDLQ(t.TempDir(), nil, nil)
	defer d.Close()
	_, ok, err := d.Pop("never-existed")
	if err != nil {
		t.Fatalf("Pop of missing should not error; got %v", err)
	}
	if ok {
		t.Fatal("Pop of missing should return ok=false")
	}
}
