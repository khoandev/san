package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/plan"
)

// startExecutingPlan puts m into Execute Mode with one step in flight.
func startExecutingPlan(t *testing.T, m *model, steps ...string) plan.Plan {
	t.Helper()
	m.plan.SubmitDraft(testPlan("plan-row", "Split auth", steps...))
	if err := m.plan.Approve(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}
	active, ok := m.plan.Active()
	if !ok {
		t.Fatal("no active plan after approval")
	}
	return active
}

// The strip above the composer answers "which step is the executor on"; that
// row is plan state, so it is drawn without any task being tracked.
func TestTrackerStripShowsPlanProgressWhileExecuting(t *testing.T) {
	m := newPlanModel(t)
	startExecutingPlan(t, m, "read code", "move files")
	m.conv.ShowTasks = false

	view := ansi.Strip(m.renderTrackerList())
	if !strings.Contains(view, "Plan step 1/2") || !strings.Contains(view, "running") {
		t.Fatalf("the plan row is missing from the strip:\n%s", view)
	}
	if strings.Contains(view, "Tasks") {
		t.Fatalf("the task list is toggled off but still rendered:\n%s", view)
	}

	// The row has to reach the frame above the composer, not just this helper.
	frame, _ := m.viewString()
	if !strings.Contains(ansi.Strip(frame), "Plan step 1/2") {
		t.Fatalf("the plan row never reached the rendered frame:\n%s", ansi.Strip(frame))
	}
}

// Outside execution there is no step to announce: planning has none in flight,
// and an approved plan waiting for its first step is the panel's business.
func TestTrackerStripHidesPlanRowOutsideExecution(t *testing.T) {
	m := newPlanModel(t)
	m.conv.ShowTasks = false

	if view := ansi.Strip(m.renderTrackerList()); strings.Contains(view, "Plan step") {
		t.Fatalf("ordinary chat rendered a plan row:\n%s", view)
	}

	m.plan.SubmitDraft(testPlan("plan-draft", "Split auth", "read code"))
	view := ansi.Strip(m.renderTrackerList())
	if strings.Contains(view, "Plan step") {
		t.Fatalf("a plan awaiting approval rendered a progress row:\n%s", view)
	}
}

// Both sections keep their own rows, plan first, with nothing between them.
func TestTrackerStripStacksPlanRowAboveTasks(t *testing.T) {
	m := newPlanModel(t)
	startExecutingPlan(t, m, "read code", "move files")
	m.conv.ShowTasks = true
	m.services.Tracker.Create("Audit handlers", "read the handlers", "", nil)

	view := ansi.Strip(m.renderTrackerList())
	planAt := strings.Index(view, "Plan step 1/2")
	tasksAt := strings.Index(view, "Tasks")
	if planAt < 0 || tasksAt < 0 {
		t.Fatalf("strip is missing a section:\n%s", view)
	}
	if planAt > tasksAt {
		t.Fatalf("the plan row should lead the task list:\n%s", view)
	}
	if !strings.Contains(view, "Audit handlers") {
		t.Fatalf("tracked task dropped from the strip:\n%s", view)
	}
	if strings.Contains(view, "\n\n") {
		t.Fatalf("the sections are separated by a blank row:\n%q", view)
	}
}
