package subagent

import (
	"context"
	"testing"

	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

type namedHookProvider struct {
	continuationProvider
	name string
}

func (p namedHookProvider) Name() string { return p.name }

// TestChildHooksFollowSelectedProvider pins the child handler to the model the
// request asked for: the factory runs once, start and stop see that provider
// and model, and the handler is never rebound afterwards.
func TestChildHooksFollowSelectedProvider(t *testing.T) {
	store, parent := continuationSessions(t, "owner")
	factoryCalls, cleanups, starts, stops := 0, 0, 0, 0
	child := namedHookProvider{continuationProvider{aitest.New(aitest.Says("child answer"))}, "child-provider"}
	parentProvider := namedHookProvider{continuationProvider{aitest.New(aitest.Says("parent answer"))}, "parent-provider"}
	e := NewExecutor(parentProvider, t.TempDir(), "parent-model", nil)
	e.SetResolver(&stubResolver{provider: child})
	if err := e.SetSessions(store, parent); err != nil {
		t.Fatal(err)
	}
	e.SetChildHooks(func(owner *session.Session, provider llm.Provider, model string) (hook.Handler, func()) {
		factoryCalls++
		if owner == parent || provider.Name() != "child-provider" || model != "selected-model" {
			t.Errorf("factory received the parent instead of the selected child: owner=%s provider=%s model=%s", owner.ID(), provider.Name(), model)
		}
		return childStartHooks{dispatch: func(event hook.EventType) {
			switch event {
			case hook.SubagentStart:
				starts++
			case hook.SubagentStop:
				stops++
			}
		}}, func() {
			cleanups++
			if stops != 1 {
				t.Error("cleanup preceded child stop")
			}
		}
	})
	result, err := e.Run(context.Background(), tool.AgentExecRequest{Prompt: "work", Model: "openai/selected-model", ParentToolCallID: "launch"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "child answer" || factoryCalls != 1 || cleanups != 1 || starts != 1 || stops != 1 {
		t.Fatalf("handler lifetime changed: result=%+v factory=%d cleanup=%d starts=%d stops=%d", result, factoryCalls, cleanups, starts, stops)
	}
}
