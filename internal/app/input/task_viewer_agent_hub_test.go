package input

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/genai-io/san/internal/task"
)

func TestTaskViewerRosterShowsStageUsageAndModel(t *testing.T) {
	var v TaskViewer
	start := time.Now().Add(-2*time.Minute - 5*time.Second)
	items := []TaskViewItem{
		{ID: "live", AgentName: "task", Subject: "Fix build", Status: task.StatusRunning,
			StartedAt: start, Steps: 12, Tokens: 15400, Model: "claude-sonnet-4-5"},
		{ID: "done", AgentName: "reviewer", Subject: "Review diff", Status: task.StatusCompleted,
			CanContinue: true, Steps: 3, Tokens: 900},
	}
	v.Show(items, 100, 24)
	v.HandleKeypress(taskKey("h")) // settled work folds into history by default
	plain := ansi.Strip(v.Render())
	for _, want := range []string{"running", "2m05s", "15.4k tok", "12 steps", "claude-sonnet-4-5", "idle"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("roster hid %q:\n%s", want, plain)
		}
	}
	// A narrow terminal drops the measurement cells before it drops the stage or
	// squeezes the subject to nothing.
	v.Resize(40, 24)
	plain = ansi.Strip(v.Render())
	for _, want := range []string{"running", "Fix"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("narrow roster lost %q:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"claude-sonnet-4-5", "steps"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("narrow roster kept %q:\n%s", unwanted, plain)
		}
	}
}

func TestTaskViewerInspectorNamesModelModeAndUsage(t *testing.T) {
	var v TaskViewer
	start := time.Now().Add(-90 * time.Second)
	v.Show([]TaskViewItem{{
		ID: "w", AgentName: "task", Subject: "Fix build", Status: task.StatusRunning,
		Model: "claude-sonnet-4-5", Mode: "edit", Steps: 12, Tokens: 15400,
		StartedAt: start, Activity: "ran go test ./...",
	}}, 100, 30)
	v.HandleKeypress(taskKey("enter"))
	plain := ansi.Strip(v.Render())
	for _, want := range []string{
		"Model: claude-sonnet-4-5", "Mode: edit", "Usage: 12 steps", "15.4k tokens",
		"Elapsed: 1m30s", "Activity: ran go test ./...",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("inspector hid %q:\n%s", want, plain)
		}
	}
}

func TestTaskViewerOpensWorkerTranscript(t *testing.T) {
	var v TaskViewer
	item := TaskViewItem{ID: "w1", AgentName: "task", Subject: "Fix build",
		Status: task.StatusRunning, SessionID: "child-session"}
	v.Show([]TaskViewItem{item}, 80, 24)
	cmd := v.HandleKeypress(taskKey("t"))
	if cmd == nil {
		t.Fatal("t did not ask for the worker transcript")
	}
	msg := cmd().(TaskTranscriptMsg)
	if msg.TaskID != "w1" || msg.SessionID != "child-session" || msg.ViewID != v.ViewID() {
		t.Fatalf("transcript request lost identity: %+v", msg)
	}

	v.Show([]TaskViewItem{{ID: "saved", Status: task.StatusCompleted}}, 80, 24)
	if cmd := v.HandleKeypress(taskKey("t")); cmd != nil {
		t.Fatal("a worker without a session offered a transcript")
	}
}

func TestTaskViewerSteersRunningWorker(t *testing.T) {
	var v TaskViewer
	items := []TaskViewItem{{ID: "w1", AgentName: "task", Subject: "Fix build", Status: task.StatusRunning}}
	v.Show(items, 80, 24)
	if cmd := v.HandleKeypress(taskKey("m")); cmd != nil || !v.steer {
		t.Fatal("m did not open the steer input")
	}
	v.HandlePaste("be brief")
	if got := v.steerInput.Value(); got != "be brief" {
		t.Fatalf("paste landed as %q", got)
	}
	if cmd := v.HandleKeypress(taskKey("esc")); cmd != nil || v.steer {
		t.Fatal("Esc did not cancel the steer input")
	}
}

func TestTaskViewerSteerSendsOneLine(t *testing.T) {
	var v TaskViewer
	items := []TaskViewItem{{ID: "w1", AgentName: "task", Subject: "Fix build", Status: task.StatusRunning, Attempt: 2}}
	v.Show(items, 80, 24)
	v.HandleKeypress(taskKey("m"))
	v.HandlePaste("only the client tests")
	cmd := v.HandleKeypress(taskKey("enter"))
	if cmd == nil {
		t.Fatal("Enter did not send the steer line")
	}
	msg := cmd().(TaskSteerMsg)
	if msg.TaskID != "w1" || msg.Message != "only the client tests" || msg.Attempt != 2 || msg.ViewID == 0 {
		t.Fatalf("steer lost identity or text: %+v", msg)
	}
	if v.IsActive() || !v.ActionPending() {
		t.Fatal("a sent steer must suspend the viewer and wait for the host")
	}

	var esc TaskViewer
	esc.Show(items, 80, 24)
	esc.HandleKeypress(taskKey("m"))
	if cmd := esc.HandleKeypress(taskKey("esc")); cmd != nil || esc.steer {
		t.Fatal("Esc did not cancel the steer input")
	}
}

func TestTaskViewerOffersNoSteerForSettledWork(t *testing.T) {
	var v TaskViewer
	settled := []TaskViewItem{
		{ID: "done", Status: task.StatusCompleted, CanContinue: true},
		{ID: "saved", Status: task.StatusCompleted, Recorded: true},
		{ID: "stopped", Status: task.StatusStopped},
	}
	for _, item := range settled {
		v.Show([]TaskViewItem{item}, 80, 24)
		if cmd := v.HandleKeypress(taskKey("m")); cmd != nil || v.steer {
			t.Fatalf("%s offered a steer input", item.Status)
		}
	}
}
