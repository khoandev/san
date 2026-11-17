package task

import (
	"context"
	"errors"
	"testing"
)

// TestCheckpointRecordsTheLastSafeBoundary pins what a checkpoint carries: the
// boundary a restart continues from, plus the routing that produced it, stamped
// with the attempt so a retired execution cannot move it.
func TestCheckpointRecordsTheLastSafeBoundary(t *testing.T) {
	worker := NewAgentTask("checkpoint", "worker", "work", context.Background(), nil, "")
	worker.SetCheckpoint(Checkpoint{
		SessionID: "child-session", Steps: 3, Model: "worker-model", Effort: "high",
		Tier: "standard", Cwd: "/work", LastToolCall: "Read",
	})

	cp, ok := worker.Checkpoint()
	if !ok {
		t.Fatal("no checkpoint recorded")
	}
	if cp.Version != CheckpointVersion {
		t.Fatalf("version = %d, want %d", cp.Version, CheckpointVersion)
	}
	if cp.Attempt != worker.attempt {
		t.Fatalf("attempt = %d, want the running execution %d", cp.Attempt, worker.attempt)
	}
	if cp.Steps != 3 || cp.LastToolCall != "Read" || cp.SessionID != "child-session" {
		t.Fatalf("boundary lost: %+v", cp)
	}
	if cp.Model != "worker-model" || cp.Effort != "high" || cp.Tier != "standard" || cp.Cwd != "/work" {
		t.Fatalf("routing lost: %+v", cp)
	}
	if cp.UpdatedAt.IsZero() {
		t.Fatal("checkpoint has no timestamp")
	}
}

// TestPausedAttemptKeepsTheCheckpoint pins the difference between a pause and a
// failure: the task stops running, but its attempts and boundary stay, because
// a restart continues from them.
func TestPausedAttemptKeepsTheCheckpoint(t *testing.T) {
	worker := NewAgentTask("paused", "worker", "work", context.Background(), nil, "")
	worker.SetCheckpoint(Checkpoint{SessionID: "child-session", Steps: 2, LastToolCall: "Edit"})

	worker.PauseAttempt(0, errors.New("provider rate_limit (http 429)"))

	if !worker.Paused() {
		t.Fatal("task should report paused")
	}
	if worker.IsRunning() {
		t.Fatal("a paused task is no longer running")
	}
	status := worker.GetStatus()
	if status.Status != StatusPaused {
		t.Fatalf("status = %q, want %q", status.Status, StatusPaused)
	}
	if len(status.Attempts) != 1 || status.Attempts[0].Error == "" {
		t.Fatalf("attempt should be recorded: %+v", status.Attempts)
	}
	cp, ok := worker.Checkpoint()
	if !ok || cp.Steps != 2 || cp.LastToolCall != "Edit" {
		t.Fatalf("pause dropped the checkpoint: %+v", cp)
	}
}

// TestFinishedExecutionCannotAdvanceTheCheckpoint keeps a stale execution from
// overwriting the boundary the live one works from.
func TestFinishedExecutionCannotAdvanceTheCheckpoint(t *testing.T) {
	worker := NewAgentTask("retired", "worker", "work", context.Background(), nil, "")
	worker.SetCheckpoint(Checkpoint{Steps: 1, LastToolCall: "Read"})
	worker.PauseAttempt(0, errors.New("provider rate_limit (http 429)"))

	worker.SetCheckpoint(Checkpoint{Steps: 9, LastToolCall: "Bash"})
	cp, ok := worker.Checkpoint()
	if !ok || cp.Steps != 1 || cp.LastToolCall != "Read" {
		t.Fatalf("a paused task accepted a later checkpoint: %+v", cp)
	}
}

// TestRetiredAttemptCannotPauseTheLiveOne covers the same rule for the status
// transition: an execution that has been replaced cannot speak for the task.
func TestRetiredAttemptCannotPauseTheLiveOne(t *testing.T) {
	worker := NewAgentTask("generation", "worker", "work", context.Background(), nil, "")
	worker.attempt = 2

	worker.PauseAttempt(1, errors.New("provider rate_limit (http 429)"))

	if !worker.IsRunning() {
		t.Fatalf("a retired execution paused the task: %+v", worker.GetStatus())
	}
}
