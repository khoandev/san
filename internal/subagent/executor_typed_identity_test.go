package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

func TestTypedContinuationRejectsReplacementParentHandle(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	store, err := session.OpenStore(ctx, cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	parent, err := store.Create(ctx, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := aitest.New(aitest.Says("first"), aitest.Says("must not run"))
	e := NewExecutor(continuationProvider{d}, cwd, "aitest", nil)
	if err := e.SetSessions(store, parent); err != nil {
		t.Fatal(err)
	}
	a, err := e.RunBackground(tool.AgentExecRequest{Prompt: "work", ParentToolCallID: "launch"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.WaitForCompletion(5 * time.Second) {
		t.Fatal("hung")
	}
	id := parent.ID()
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := e.SetSessions(store, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunBackground(tool.AgentExecRequest{ResumeTaskID: a.GetID(), Prompt: "continue", ParentToolCallID: "later"}); err == nil {
		t.Fatal("replacement parent handle accepted")
	}
	if d.Calls() != 1 {
		t.Fatalf("replacement dispatched %d calls", d.Calls())
	}
}
