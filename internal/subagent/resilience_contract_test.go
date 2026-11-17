package subagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

// modelRecordingProvider records the model every client was built for, so a
// test can tell which model a retry or a restart actually asked.
type modelRecordingProvider struct {
	driver *aitest.Driver

	mu     sync.Mutex
	models []string
}

func (p *modelRecordingProvider) Client(model string, _ map[string]string) (*ai.Client, error) {
	p.mu.Lock()
	p.models = append(p.models, model)
	p.mu.Unlock()
	return p.driver.Client(), nil
}

func (p *modelRecordingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func (p *modelRecordingProvider) Name() string { return "aitest" }

func (p *modelRecordingProvider) modelsUsed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

// rateLimited is the transient failure a checkpoint exists for: the provider
// refused this attempt and would accept the next one.
func rateLimited(driver string) error {
	return &ai.Error{Driver: driver, Kind: ai.KindRateLimit, Status: 429, Message: "too many requests"}
}

// TestRestartFromCheckpointResumesWithoutNewPrompt pins the recovery contract a
// provider failure leaves behind: the task keeps its session and checkpoint, and
// the user can restart it without inventing a new brief. A restart that demands
// fresh instructions is the difference between resuming work and redoing it.
func TestRestartFromCheckpointResumesWithoutNewPrompt(t *testing.T) {
	store, parent := continuationSessions(t, "parent-restart-contract")
	cwd := t.TempDir()
	d := aitest.Always(aitest.Fails(rateLimited("aitest")))
	e := NewExecutor(continuationProvider{d}, cwd, "aitest", nil)
	bindContinuationSessions(t, e, store, parent)

	first, err := e.RunBackground(tool.AgentExecRequest{Prompt: "Inspect the package", MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !first.WaitForCompletion(5 * time.Second) {
		t.Fatal("first execution hung")
	}
	failed := first.GetStatus()
	if failed.Status != task.StatusPaused || failed.AgentSessionID == "" {
		t.Fatalf("first execution should stop at a checkpoint: %+v", failed)
	}

	restarted, err := e.RunBackground(tool.AgentExecRequest{ResumeTaskID: first.GetID()})
	if err != nil {
		t.Fatalf("restart from checkpoint refused: %v", err)
	}
	if restarted != first {
		t.Fatal("restart allocated a new task instead of resuming")
	}
	if !restarted.WaitForCompletion(5 * time.Second) {
		t.Fatal("restart hung")
	}
	status := restarted.GetStatus()
	if len(status.Attempts) != 2 || status.AgentSessionID != failed.AgentSessionID {
		t.Fatalf("restart lost the checkpoint: %+v", status)
	}
}

// lastUserText returns the most recent user turn a provider was sent, which is
// where a run's instructions land.
func lastUserText(requests []*ai.Request) string {
	for i := len(requests) - 1; i >= 0; i-- {
		for j := len(requests[i].Messages) - 1; j >= 0; j-- {
			if message := requests[i].Messages[j]; message.Role == ai.RoleUser {
				return message.Content.Text()
			}
		}
	}
	return ""
}

// TestRestartFromCheckpointReusesTheCheckpointsRouting pins what a restart
// continues on: the checkpoint's session, model, effort and tier, not whatever
// this launch would resolve now. A restart is the same work going on, so it must
// not quietly move a paused task onto another binding or an empty brief.
func TestRestartFromCheckpointReusesTheCheckpointsRouting(t *testing.T) {
	store, parent := continuationSessions(t, "parent-restart-routing")
	cwd := t.TempDir()
	path := filepath.Join(cwd, "evidence.txt")
	if err := os.WriteFile(path, []byte("previously discovered evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	d := aitest.New(
		aitest.Asks(readCall(t, path)),      // one completed tool call: the boundary
		aitest.Fails(rateLimited("aitest")), // the provider refuses the next one
		aitest.Says("resumed and finished"), // the restart picks the work up
	)
	recorder := &modelRecordingProvider{driver: d}
	e := NewExecutor(recorder, cwd, "parent-model", nil)
	bindContinuationSessions(t, e, store, parent)
	// The deep rung is the binding the user chose: its model and its effort are
	// what the paused work has to come back on.
	e.SetRoleResolver(func(role string) RoleAssignment {
		if role == string(llm.RoleDeep) {
			return RoleAssignment{Provider: recorder, ModelID: "chosen-model", Effort: "high", Assigned: true}
		}
		return RoleAssignment{}
	})

	run, err := e.RunBackground(tool.AgentExecRequest{Prompt: "Inspect the package", MaxSteps: 3, Tier: "deep"})
	if err != nil {
		t.Fatal(err)
	}
	if !run.WaitForCompletion(5 * time.Second) {
		t.Fatal("first execution hung")
	}
	paused := run.GetStatus()
	if paused.Status != task.StatusPaused {
		t.Fatalf("first execution should pause at its checkpoint: %+v", paused)
	}
	boundary, ok := run.Checkpoint()
	if !ok || boundary.Steps != 1 {
		t.Fatalf("checkpoint = %+v, ok = %v, want one completed step", boundary, ok)
	}
	if boundary.Model != "chosen-model" || boundary.Effort != "high" || boundary.Tier != "deep" {
		t.Fatalf("checkpoint routing = %+v, want the model, effort and tier the work ran with", boundary)
	}

	// The launch would now resolve a different binding. A restart must still run
	// the paused work on the one it was approved with.
	e.SetDefaultModel("other-model")
	e.SetRoleResolver(func(role string) RoleAssignment {
		return RoleAssignment{Provider: recorder, ModelID: "other-model", Effort: "low", Assigned: role == string(llm.RoleDeep)}
	})
	restarted, err := e.RunBackground(tool.AgentExecRequest{ResumeTaskID: run.GetID()})
	if err != nil {
		t.Fatalf("restart from checkpoint refused: %v", err)
	}
	if restarted != run {
		t.Fatal("restart allocated a new task instead of resuming")
	}
	if !restarted.WaitForCompletion(5 * time.Second) {
		t.Fatal("restart hung")
	}
	status := restarted.GetStatus()
	if len(status.Attempts) != 2 || status.AgentSessionID != paused.AgentSessionID {
		t.Fatalf("restart lost the checkpoint: %+v", status)
	}
	if status.Model != "chosen-model" {
		t.Fatalf("task model = %q, want the checkpoint's chosen-model", status.Model)
	}
	for _, model := range recorder.modelsUsed() {
		if model != "chosen-model" {
			t.Fatalf("restart asked for model %q, want the checkpoint's chosen-model", model)
		}
	}
	prompt := lastUserText(d.Sent())
	for _, want := range []string{"Resume the interrupted task from its checkpoint", "1 tool call(s) already completed", "Read"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("restart brief %q does not mention %q", prompt, want)
		}
	}
}

// TestProviderFailureKeepsTheChosenModel pins the model contract: a provider
// failure is reported, never traded for the parent's model behind the user's
// back. A silent switch both hides the broken binding and makes the failure
// unreproducible.
func TestProviderFailureKeepsTheChosenModel(t *testing.T) {
	store, parent := continuationSessions(t, "parent-model-contract")
	cwd := t.TempDir()
	// A missing model is not transient: the failure is reported as a failure,
	// and the model the user chose stays the one the run asked for.
	recorder := &modelRecordingProvider{driver: aitest.Always(aitest.Fails(&ai.Error{
		Driver: "aitest", Kind: ai.KindInvalidRequest, Status: 404, Message: "model_not_found: chosen-model",
	}))}
	e := NewExecutor(recorder, cwd, "parent-model", nil)
	bindContinuationSessions(t, e, store, parent)
	e.SetDefaultModel("chosen-model")

	a, err := e.RunBackground(tool.AgentExecRequest{Prompt: "Inspect the package", MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !a.WaitForCompletion(5 * time.Second) {
		t.Fatal("execution hung")
	}
	if status := a.GetStatus(); status.Status != task.StatusFailed {
		t.Fatalf("execution should fail: %+v", status)
	}
	for _, model := range recorder.modelsUsed() {
		if model != "chosen-model" {
			t.Fatalf("provider failure switched the model to %q", model)
		}
	}
}

// TestProviderFailureSpendsNoRetry pins the replay contract: the first
// transient failure ends the execution, so the retry the provider would have
// accepted is left to the person restarting the paused task. A silent replay
// would both hide the failure and spend tokens nobody approved.
func TestProviderFailureSpendsNoRetry(t *testing.T) {
	store, parent := continuationSessions(t, "parent-no-replay")
	d := aitest.New(aitest.Fails(rateLimited("aitest")), aitest.Says("a replay nobody asked for"))
	e := NewExecutor(continuationProvider{d}, t.TempDir(), "aitest", nil)
	bindContinuationSessions(t, e, store, parent)

	run, err := e.RunBackground(tool.AgentExecRequest{Prompt: "Inspect the package", MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !run.WaitForCompletion(5 * time.Second) {
		t.Fatal("execution hung")
	}
	if calls := d.Calls(); calls != 1 {
		t.Fatalf("inference calls = %d, want 1: a transient failure must not be replayed", calls)
	}
	if status := run.GetStatus(); status.Status != task.StatusPaused {
		t.Fatalf("status = %q, want %q", status.Status, task.StatusPaused)
	}
}

// TestDefaultWorkerInheritsRoleEffort pins the effort contract for the common
// case: an unnamed delegation runs on the worker model, and when that model is
// the standard role's the role's effort has to travel with it. Inheriting the
// model but dropping the effort is the "effort was not respected" bug.
func TestDefaultWorkerInheritsRoleEffort(t *testing.T) {
	provider := stubProvider{}
	executor := &Executor{parentModelID: "parent-model", provider: provider}
	executor.SetDefaultModel("standard-model")
	executor.SetRoleResolver(func(role string) RoleAssignment {
		if role == string(llm.RoleStandard) {
			return RoleAssignment{Provider: provider, ModelID: "standard-model", Effort: "high", Assigned: true}
		}
		return RoleAssignment{}
	})

	run, err := executor.prepareRun(context.Background(), tool.AgentExecRequest{Agent: "", Prompt: "Inspect the package"})
	if err != nil {
		t.Fatal(err)
	}
	if run.cfg.modelID != "standard-model" {
		t.Fatalf("model = %q, want standard-model", run.cfg.modelID)
	}
	if run.cfg.effort != "high" {
		t.Fatalf("effort = %q, want the effort bound to the role that supplied the model", run.cfg.effort)
	}
}
