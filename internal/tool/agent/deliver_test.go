package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/broker"
	"github.com/genai-io/san/internal/task"
)

func TestDeliverToRunningUsesTheMainEnvelope(t *testing.T) {
	broker.Reset()
	t.Cleanup(broker.Reset)
	manager := task.NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.CreateAgentTask("w1", "task", "Fix build", ctx, cancel)

	var got broker.Message
	broker.Register("w1", func(msg broker.Message) bool { got = msg; return true })
	t.Cleanup(func() { broker.Unregister("w1") })

	if err := DeliverToRunning(manager, Delivery{
		TaskID: "w1", ParentSession: "session", SourceSession: "native-7", SourceSeq: 7, Message: "be brief",
	}); err != nil {
		t.Fatalf("running worker was refused: %v", err)
	}
	if got.From != broker.Main || got.SessionID != "session" || !strings.Contains(got.Content, "be brief") {
		t.Fatalf("delivery lost its envelope: %+v", got)
	}
	if got.SourceSession != "native-7" || got.SourceSeq != 7 {
		t.Fatalf("delivery lost its native provenance: %+v", got)
	}
	if !strings.Contains(got.Content, `<agent-message from="main"`) {
		t.Fatalf("delivery lost the agent-message tag: %s", got.Content)
	}
}

func TestDeliverToRunningRefusesSettledAndForeignWork(t *testing.T) {
	broker.Reset()
	t.Cleanup(broker.Reset)
	manager := task.NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	worker := manager.CreateAgentTask("w1", "task", "Fix build", ctx, cancel)

	if err := DeliverToRunning(manager, Delivery{TaskID: "w1", ParentSession: "other-session", Message: "hi"}); err == nil {
		t.Fatal("a worker of another conversation accepted a message")
	}
	worker.Complete(nil)
	if err := DeliverToRunning(manager, Delivery{TaskID: "w1", Message: "hi"}); err == nil {
		t.Fatal("a settled worker accepted a message as if it were live")
	}
}
