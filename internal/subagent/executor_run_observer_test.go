package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

// The run observer is what the host counts requests with: it must fire once
// per finished run, for foreground and background runs alike, naming the agent
// that actually ran and reporting the steps it spent.
func TestRunObserverReportsFinishedRuns(t *testing.T) {
	type report struct {
		name  string
		steps int
	}
	var got []report
	executor := NewExecutor(continuationProvider{aitest.New(aitest.Says("answer"))}, t.TempDir(), "aitest", nil)
	executor.SetRunObserver(func(name string, steps int) {
		got = append(got, report{name: name, steps: steps})
	})

	result, err := executor.Run(context.Background(), tool.AgentExecRequest{Prompt: "work", Agent: "quick"})
	if err != nil || result == nil {
		t.Fatalf("foreground run: %+v %v", result, err)
	}
	worker, ok := task.Default().Get(result.Identity.ID)
	if ok {
		t.Cleanup(func() { task.Default().Remove(worker.GetID()) })
	}
	if len(got) != 1 {
		t.Fatalf("observer fired %d times for one run: %+v", len(got), got)
	}
	if got[0].name != result.AgentName || got[0].steps != result.StepCount {
		t.Fatalf("report = %+v, want name %q steps %d", got[0], result.AgentName, result.StepCount)
	}

	got = nil
	worker2, err := executor.RunBackground(tool.AgentExecRequest{Prompt: "work", Agent: "explore"})
	if err != nil {
		t.Fatalf("background run: %v", err)
	}
	t.Cleanup(func() { task.Default().Remove(worker2.GetID()) })
	if !worker2.WaitForCompletion(5 * time.Second) {
		t.Fatal("background run hung")
	}
	if len(got) != 1 {
		t.Fatalf("observer fired %d times for one background run: %+v", len(got), got)
	}
	if got[0].name != "explore" || got[0].steps <= 0 {
		t.Fatalf("background report = %+v, want explore with a positive step count", got[0])
	}
}

// Clearing the observer stops reporting without disturbing a run already in
// flight.
func TestRunObserverCanBeCleared(t *testing.T) {
	fired := false
	executor := NewExecutor(continuationProvider{aitest.New(aitest.Says("answer"))}, t.TempDir(), "aitest", nil)
	executor.SetRunObserver(func(string, int) { fired = true })
	executor.SetRunObserver(nil)

	result, err := executor.Run(context.Background(), tool.AgentExecRequest{Prompt: "work", Agent: "quick"})
	if err != nil || result == nil {
		t.Fatalf("run: %+v %v", result, err)
	}
	if worker, ok := task.Default().Get(result.Identity.ID); ok {
		t.Cleanup(func() { task.Default().Remove(worker.GetID()) })
	}
	if fired {
		t.Fatal("a cleared observer still fired")
	}
}
