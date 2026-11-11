package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/plan"
)

// The strip is the only place plan progress is drawn: the plan's steps are todo
// rows, so these tests hold the checklist itself — heading, rows, colors and
// budget — rather than a separate progress line.
//
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

// The strip is the plan's todo list: the heading names the plan and counts its
// steps, and every step keeps its own row. That is plan state, so it is drawn
// without any task being tracked, and with the task toggle off.
func TestTrackerStripShowsPlanStepsWhileExecuting(t *testing.T) {
	m := newPlanModel(t)
	startExecutingPlan(t, m, "read code", "move files")
	m.syncPlanTodos()
	m.conv.ShowTasks = false

	view := ansi.Strip(m.renderTrackerList())
	for _, want := range []string{"Plan · Split auth", "0/2 done", "read code", "move files"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the plan checklist lost %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "  Todos") {
		t.Fatalf("the task list is toggled off but an ordinary group is rendered:\n%s", view)
	}

	// The strip has to reach the frame above the composer, not just this helper.
	frame, _ := m.viewString()
	if !strings.Contains(ansi.Strip(frame), "Plan · Split auth") {
		t.Fatalf("the plan heading never reached the rendered frame:\n%s", ansi.Strip(frame))
	}
}

// Outside execution there is no plan list: planning has no rows, and an
// approved plan waiting for its first step is the panel's business.
func TestTrackerStripHidesPlanRowOutsideExecution(t *testing.T) {
	m := newPlanModel(t)
	m.conv.ShowTasks = false

	if view := ansi.Strip(m.renderTrackerList()); view != "" {
		t.Fatalf("ordinary chat rendered a plan checklist:\n%s", view)
	}

	m.plan.SubmitDraft(testPlan("plan-draft", "Split auth", "read code"))
	if view := ansi.Strip(m.renderTrackerList()); strings.Contains(view, "Plan ·") {
		t.Fatalf("a plan awaiting approval rendered a checklist:\n%s", view)
	}
}

// Both groups keep their own rows, plan first, with nothing between them.
func TestTrackerStripStacksPlanRowAboveTasks(t *testing.T) {
	m := newPlanModel(t)
	startExecutingPlan(t, m, "read code", "move files")
	m.syncPlanTodos()
	m.conv.ShowTasks = true
	m.services.Tracker.Create("Audit handlers", "read the handlers", "", nil)

	view := ansi.Strip(m.renderTrackerList())
	planAt := strings.Index(view, "Plan · Split auth")
	todosAt := strings.Index(view, "Todos")
	auditAt := strings.Index(view, "Audit handlers")
	if planAt < 0 || todosAt < 0 || auditAt < 0 {
		t.Fatalf("strip is missing a section:\n%s", view)
	}
	if !(planAt < todosAt && todosAt < auditAt) {
		t.Fatalf("plan rows should lead, then the session's own todos:\n%s", view)
	}
	if strings.Contains(view, "\n\n") {
		t.Fatalf("the sections are separated by a blank row:\n%q", view)
	}
}

// Alt+T hides tracked work, never the plan steps the person asked the executor
// to work through.
func TestTrackerStripKeepsPlanRowsWhenTasksHidden(t *testing.T) {
	m := newPlanModel(t)
	startExecutingPlan(t, m, "read code", "move files")
	m.syncPlanTodos()
	m.services.Tracker.Create("Audit handlers", "read the handlers", "", nil)

	m.conv.ShowTasks = false
	view := ansi.Strip(m.renderTrackerList())
	for _, want := range []string{"Plan · Split auth", "read code", "move files"} {
		if !strings.Contains(view, want) {
			t.Fatalf("hidden task list dropped the plan row %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Audit handlers") {
		t.Fatalf("toggled-off task list still rendered its todos:\n%s", view)
	}

	m.conv.ShowTasks = true
	view = ansi.Strip(m.renderTrackerList())
	if !strings.Contains(view, "Audit handlers") || !strings.Contains(view, "  Todos") {
		t.Fatalf("re-enabled task list lost its todos:\n%s", view)
	}
}
