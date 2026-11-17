package subagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

// readCall asks for the file the run reads, which is the boundary the checkpoint
// has to land on.
func readCall(t *testing.T, path string) ai.ToolCall {
	t.Helper()
	args, err := json.Marshal(map[string]string{"file_path": path})
	if err != nil {
		t.Fatal(err)
	}
	return ai.ToolCall{ID: "read-1", Name: "Read", Input: string(args)}
}

// TestProviderFailureLeavesTheTaskPaused pins what a dead run leaves behind: a
// paused task whose checkpoint names the last tool that finished, the model it
// ran on and the child session that holds the transcript — the inputs a restart
// needs to continue instead of redoing the work.
func TestProviderFailureLeavesTheTaskPaused(t *testing.T) {
	store, parent := continuationSessions(t, "parent-paused-checkpoint")
	cwd := t.TempDir()
	path := filepath.Join(cwd, "evidence.txt")
	if err := os.WriteFile(path, []byte("evidence the run already collected"), 0o600); err != nil {
		t.Fatal(err)
	}

	rateLimit := &ai.Error{Driver: "aitest", Kind: ai.KindRateLimit, Status: 429, Message: "too many requests"}
	// One tool turn, then an endpoint that stays down for every retry it sees.
	d := aitest.New(aitest.Asks(readCall(t, path)), aitest.Fails(rateLimit), aitest.Fails(rateLimit), aitest.Fails(rateLimit))
	e := NewExecutor(continuationProvider{d}, cwd, "aitest", nil)
	bindContinuationSessions(t, e, store, parent)
	e.SetDefaultModel("chosen-model")

	run, err := e.RunBackground(tool.AgentExecRequest{Prompt: "Inspect the package", MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !run.WaitForCompletion(5 * time.Second) {
		t.Fatal("execution hung")
	}

	status := run.GetStatus()
	if status.Status != task.StatusPaused {
		t.Fatalf("status = %q, want %q: a retryable failure pauses, it does not fail", status.Status, task.StatusPaused)
	}
	if status.AgentSessionID == "" {
		t.Fatal("the child session must survive the pause")
	}

	cp, ok := run.Checkpoint()
	if !ok {
		t.Fatal("no checkpoint recorded")
	}
	if cp.Steps != 1 || cp.LastToolCall != "Read" {
		t.Fatalf("boundary = %+v, want one completed Read", cp)
	}
	if cp.Model != "chosen-model" {
		t.Fatalf("checkpoint model = %q, want the model that ran", cp.Model)
	}
	if cp.SessionID != status.AgentSessionID || cp.Cwd != cwd {
		t.Fatalf("checkpoint routing = %+v, want the session %q and cwd %q", cp, status.AgentSessionID, cwd)
	}
	if cp.Version != task.CheckpointVersion {
		t.Fatalf("checkpoint version = %d, want %d", cp.Version, task.CheckpointVersion)
	}
}

// TestUnusableModelFailsInsteadOfPausing keeps the two endings apart: a model
// the provider does not have is not a transient failure, so it stays a failure
// and no restart is implied.
func TestUnusableModelFailsInsteadOfPausing(t *testing.T) {
	store, parent := continuationSessions(t, "parent-unusable-model")
	missing := &ai.Error{Driver: "aitest", Kind: ai.KindInvalidRequest, Status: 404, Message: "model_not_found: chosen-model"}
	e := NewExecutor(continuationProvider{aitest.Always(aitest.Fails(missing))}, t.TempDir(), "aitest", nil)
	bindContinuationSessions(t, e, store, parent)
	e.SetDefaultModel("chosen-model")

	run, err := e.RunBackground(tool.AgentExecRequest{Prompt: "Inspect the package", MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !run.WaitForCompletion(5 * time.Second) {
		t.Fatal("execution hung")
	}

	if status := run.GetStatus(); status.Status != task.StatusFailed {
		t.Fatalf("status = %q, want %q", status.Status, task.StatusFailed)
	}
	if _, ok := run.Checkpoint(); ok {
		t.Fatal("a run that never reached a boundary has no checkpoint")
	}
}
