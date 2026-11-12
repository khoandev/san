package app

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/broker"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/transcript"
)

// childSession writes a finished worker conversation the way the subagent
// executor does, and returns its store and id.
func childSession(t *testing.T, line string) (*session.LegacyStore, string) {
	t.Helper()
	store, err := session.NewLegacyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	open, err := store.Create(session.LegacyCreateOptions{
		AgentID: "subagent", Model: "test-model", Cwd: t.TempDir(), ParentSessionID: "parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	message := core.UserMessage(line, nil)
	message.ID = core.NewMessageID()
	record := transcript.Record{Type: transcript.ConversationReplaced, Messages: []core.Message{message}}
	if err := open.Append(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := open.Close(); err != nil {
		t.Fatal(err)
	}
	return store, open.ID()
}

func TestTaskTranscriptOpensWorkerConversation(t *testing.T) {
	m := interactionModel()
	store, id := childSession(t, "worker found the leak")
	m.services.SessionStore = store
	m.openTasks()

	cmd := m.openTaskTranscript(input.TaskTranscriptMsg{TaskID: "w1", SessionID: id, ViewID: m.userInput.Tasks.ViewID()})
	if cmd == nil {
		t.Fatal("no transcript load was started")
	}
	m.taskTranscriptLoaded(cmd().(taskTranscriptLoadedMsg))

	if !m.userInput.Transcript.IsActive() {
		t.Fatal("worker transcript did not open")
	}
	view := ansi.Strip(m.userInput.Transcript.Render())
	if !strings.Contains(view, "Task w1") || !strings.Contains(view, "worker found the leak") {
		t.Fatalf("transcript is missing its title or content:\n%s", view)
	}
	if !m.userInput.Tasks.IsActive() {
		t.Fatal("the roster should stay behind the transcript")
	}
}

func TestTaskTranscriptReportsAnUnreadableSession(t *testing.T) {
	m := interactionModel()
	store, _ := childSession(t, "irrelevant")
	m.services.SessionStore = store
	m.openTasks()

	cmd := m.openTaskTranscript(input.TaskTranscriptMsg{TaskID: "w1", SessionID: "missing-session"})
	m.taskTranscriptLoaded(cmd().(taskTranscriptLoadedMsg))
	view := ansi.Strip(m.userInput.Transcript.Render())
	if !strings.Contains(view, "Unable to read") {
		t.Fatalf("unreadable session was not reported:\n%s", view)
	}
}

func TestSteerViewedTaskQueuesOneLine(t *testing.T) {
	broker.Reset()
	t.Cleanup(broker.Reset)
	m := interactionModel()
	m.services.sessionID = "current"
	m.openTasks()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	worker := m.services.Task.CreateAgentTask("w1", "task", "Fix build", ctx, cancel)

	var got broker.Message
	broker.Register("w1", func(msg broker.Message) bool { got = msg; return true })
	t.Cleanup(func() { broker.Unregister("w1") })

	m.steerViewedTask(input.TaskSteerMsg{
		TaskID: "w1", Message: "only the client tests", ViewID: m.userInput.Tasks.ViewID(), Attempt: 1,
	})

	if !strings.Contains(got.Content, "only the client tests") || got.From != broker.Main {
		t.Fatalf("steer was not queued as main mail: %+v", got)
	}
	if !m.userInput.Tasks.IsActive() {
		t.Fatal("steering closed the roster")
	}

	worker.Complete(nil)
	broker.Reset()
	if m.steerViewedTask(input.TaskSteerMsg{
		TaskID: "w1", Message: "again", ViewID: m.userInput.Tasks.ViewID(), Attempt: 1,
	}); m.userInput.Tasks.ActionPending() {
		t.Fatal("a settled worker still accepted a steer")
	}
}
