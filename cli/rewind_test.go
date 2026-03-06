package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestSaveCheckpoint(t *testing.T) {
	cli := &ChatCLI{
		history: []models.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi there"},
		},
	}

	cli.saveCheckpoint()

	if len(cli.checkpoints) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cli.checkpoints))
	}

	cp := cli.checkpoints[0]
	if cp.MsgCount != 3 {
		t.Errorf("expected MsgCount=3, got %d", cp.MsgCount)
	}
	if cp.Label != "hello" {
		t.Errorf("expected label='hello', got %q", cp.Label)
	}

	// Verify deep copy: modifying original should not affect checkpoint
	cli.history = append(cli.history, models.Message{Role: "user", Content: "new msg"})
	if len(cp.History) != 3 {
		t.Error("checkpoint history should not be affected by original mutation")
	}
}

func TestSaveCheckpointMaxLimit(t *testing.T) {
	cli := &ChatCLI{
		history: []models.Message{{Role: "user", Content: "test"}},
	}

	for i := 0; i < maxCheckpoints+5; i++ {
		cli.saveCheckpoint()
	}

	if len(cli.checkpoints) != maxCheckpoints {
		t.Errorf("expected max %d checkpoints, got %d", maxCheckpoints, len(cli.checkpoints))
	}
}

func TestSaveCheckpointLabelTruncation(t *testing.T) {
	longMsg := ""
	for i := 0; i < 100; i++ {
		longMsg += "word "
	}

	cli := &ChatCLI{
		history: []models.Message{{Role: "user", Content: longMsg}},
	}

	cli.saveCheckpoint()

	if len(cli.checkpoints[0].Label) > 63 { // 57 + "..."
		t.Errorf("label should be truncated, got length %d", len(cli.checkpoints[0].Label))
	}
}

func TestCheckpointEmptyHistory(t *testing.T) {
	cli := &ChatCLI{
		history: []models.Message{},
	}

	cli.saveCheckpoint()

	if len(cli.checkpoints) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cli.checkpoints))
	}
	if cli.checkpoints[0].Label != "(start)" {
		t.Errorf("expected label='(start)', got %q", cli.checkpoints[0].Label)
	}
}
