package input

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A paused subagent gets the provider dialog too, and its answer has to name the
// task: Restart continues that checkpoint, it does not re-run the session's turn.
func TestRestartDialogCarriesThePausedTask(t *testing.T) {
	m := NewRateLimit()
	m.Show(RateLimitRequest{
		Title:  "Subagent paused at a checkpoint",
		Detail: "rate_limit (http 429): slow down",
		TaskID: "task-7",
	}, 80, 24)

	frame := ansi.Strip(m.Render())
	for _, want := range []string{"Subagent paused at a checkpoint", "rate_limit (http 429): slow down", "Restart task"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("dialog is missing %q:\n%s", want, frame)
		}
	}
	got := answer(t, &m, "r")
	if got == nil || !got.Restart || got.TaskID != "task-7" {
		t.Fatalf("answer = %+v, want a restart of task-7", got)
	}
}

// The main turn's dialog keeps answering for the turn: no task id travels back.
func TestRestartDialogOfTheTurnCarriesNoTask(t *testing.T) {
	m := rateLimitDialog()
	got := answer(t, m, "r")
	if got == nil || !got.Restart || got.TaskID != "" {
		t.Fatalf("answer = %+v, want a restart of the turn", got)
	}
}
