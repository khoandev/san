package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/task"
)

// pausedTask registers one agent task that stopped at a checkpoint after a
// provider failure, which is the state both restart paths act on.
func pausedTask(t *testing.T, manager *task.Manager) *task.AgentTask {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	worker := manager.CreateAgentTask("restart-7", "Explore", "Audit dependencies", ctx, cancel)
	worker.SetIdentity("Explore", "child-session")
	worker.SetCheckpoint(task.Checkpoint{
		SessionID: "child-session", Steps: 3, Model: "chosen-model", Effort: "high",
		Tier: "deep", Cwd: t.TempDir(), LastToolCall: "Read",
	})
	worker.PauseAttempt(1, errors.New("rate_limit (http 429): slow down"))
	return worker
}

// A paused subagent has to tell the parent that the work survived, how far it
// got, and the exact command that continues it. Silence would strand the task:
// nothing else in the parent's context says a restart is possible.
func TestResumePausedTaskNoticeNamesCheckpointAndCommand(t *testing.T) {
	manager := task.NewManager()
	worker := pausedTask(t, manager)

	message, ok := taskCompletionMessage(worker.GetStatus())
	if !ok {
		t.Fatal("a paused task must notify the parent instead of ending silently")
	}
	for _, want := range []string{
		`status="paused"`,
		"restart-7",
		"The task paused at a checkpoint after 3 completed step(s)",
		`"Read"`,
		"SendMessage",
		`to set to "restart-7"`,
		"Resume from the checkpoint",
	} {
		if !strings.Contains(message.Content, want) {
			t.Fatalf("notification is missing %q:\n%s", want, message.Content)
		}
	}
}

// Restarting a paused task asks for confirmation through the same dialog a
// failed turn uses, and the answer carries the task that has to continue.
func TestRestartPausedTaskUsesTheProviderDialog(t *testing.T) {
	manager := task.NewManager()
	pausedTask(t, manager)
	m := &model{services: services{Task: manager}}
	m.env.Width, m.env.Height = 100, 40
	m.env.SessionPermissions = setting.NewSessionPermissions()

	if !m.openTaskRestartDialog("restart-7") {
		t.Fatal("a paused task did not raise the restart dialog")
	}
	if !m.userInput.RateLimit.IsActive() {
		t.Fatal("the restart dialog is not showing")
	}
	frame := ansi.Strip(m.userInput.RateLimit.Render())
	for _, want := range []string{"Subagent paused at a checkpoint", "Restart task", "rate_limit"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("dialog is missing %q:\n%s", want, frame)
		}
	}

	answer := m.userInput.RateLimit.HandleKeypress(tea.KeyPressMsg{Code: 'r', Text: "r"})()
	choice, ok := answer.(input.RateLimitChoiceMsg)
	if !ok || !choice.Restart || choice.TaskID != "restart-7" {
		t.Fatalf("dialog answered %#v, want a restart of restart-7", answer)
	}
}

// A task that is not paused has nothing to restart, and a restart of one task
// never answers for another.
func TestRestartDialogIgnoresOtherTasks(t *testing.T) {
	manager := task.NewManager()
	m := &model{services: services{Task: manager}}
	m.env.Width, m.env.Height = 100, 40
	m.env.SessionPermissions = setting.NewSessionPermissions()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := manager.CreateAgentTask("finished-1", "Explore", "Audit dependencies", ctx, cancel)
	done.CompleteAttempt(1, nil)

	if m.openTaskRestartDialog("finished-1") {
		t.Fatal("a completed task raised the restart dialog")
	}
	if m.openTaskRestartDialog("missing") {
		t.Fatal("an unknown task raised the restart dialog")
	}
	if m.userInput.RateLimit.IsActive() {
		t.Fatal("the dialog is showing without a paused task")
	}
}
