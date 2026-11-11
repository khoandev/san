package app

import (
	"testing"

	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/todo"
)

// planTodoRows returns the tracker's plan-projected rows in list order.
func planTodoRows(m *model) []*todo.Item {
	var rows []*todo.Item
	for _, item := range m.services.Tracker.List() {
		if todo.PlanID(item) != "" {
			rows = append(rows, item)
		}
	}
	return rows
}

// approvedPlanModel is a model with an approved, not yet started plan.
func approvedPlanModel(t *testing.T, steps ...string) *model {
	t.Helper()
	m := newPlanModel(t)
	m.plan.SubmitDraft(testPlan("plan-todos", "Split auth", steps...))
	if err := m.plan.Approve(); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()
	return m
}

// One approval writes one todo per step, in plan order, joined to the step it
// mirrors.
func TestPlanApprovalCreatesOneTodoPerStep(t *testing.T) {
	m := approvedPlanModel(t, "read code", "move files", "add tests")
	rows := planTodoRows(m)
	if len(rows) != 3 {
		t.Fatalf("plan rows = %d, want 3:\n%+v", len(rows), rows)
	}
	for i, want := range []string{"read code", "move files", "add tests"} {
		if rows[i].Subject != want {
			t.Fatalf("row %d subject = %q, want %q", i, rows[i].Subject, want)
		}
		if rows[i].Status != todo.StatusPending {
			t.Fatalf("row %d status = %q, want pending", i, rows[i].Status)
		}
		if todo.PlanID(rows[i]) != "plan-todos" || todo.PlanStepID(rows[i]) == "" {
			t.Fatalf("row %d is not joined to its plan step: %+v", i, rows[i].Metadata)
		}
	}
}

// The running step is the one in_progress row; completing it closes the row and
// the next step takes the status.
func TestPlanTodoFollowsStepLifecycle(t *testing.T) {
	m := approvedPlanModel(t, "read code", "move files")

	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()
	rows := planTodoRows(m)
	if rows[0].Status != todo.StatusInProgress || rows[1].Status != todo.StatusPending {
		t.Fatalf("after begin: %q, %q", rows[0].Status, rows[1].Status)
	}

	if _, err := m.plan.CompleteStep(plan.StepResult{Status: plan.StepCompleted, Result: "done"}); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()
	rows = planTodoRows(m)
	if rows[0].Status != todo.StatusCompleted || todo.PlanStepStatus(rows[0]) != string(plan.StepCompleted) {
		t.Fatalf("completed step row = %+v", rows[0])
	}
	if rows[1].Status != todo.StatusPending {
		t.Fatalf("second step = %q, want pending", rows[1].Status)
	}

	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()
	rows = planTodoRows(m)
	if rows[1].Status != todo.StatusInProgress {
		t.Fatalf("second step = %q, want in_progress", rows[1].Status)
	}
}

// A blocked step is not finished work: the todo stays pending and carries the
// plan's status and reason for the renderer.
func TestPlanTodoCarriesBlockedStep(t *testing.T) {
	m := approvedPlanModel(t, "read code")
	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.plan.CompleteStep(plan.StepResult{Status: plan.StepBlocked, Reason: "the API moved"}); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()

	rows := planTodoRows(m)
	if len(rows) != 1 {
		t.Fatalf("plan rows = %d, want 1", len(rows))
	}
	if rows[0].Status != todo.StatusPending {
		t.Fatalf("blocked row status = %q, want pending", rows[0].Status)
	}
	if got := todo.PlanStepStatus(rows[0]); got != string(plan.StepBlocked) {
		t.Fatalf("blocked row step status = %q", got)
	}
	if got := todo.PlanStepBlockReason(rows[0]); got != "the API moved" {
		t.Fatalf("blocked row reason = %q", got)
	}
}

// A skipped step still closes its row, and the plan moves on.
func TestPlanTodoMarksSkippedStepDone(t *testing.T) {
	m := approvedPlanModel(t, "read code", "move files")
	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}
	if err := m.plan.SkipStep(); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()

	rows := planTodoRows(m)
	if rows[0].Status != todo.StatusCompleted || todo.PlanStepStatus(rows[0]) != string(plan.StepSkipped) {
		t.Fatalf("skipped row = %+v", rows[0])
	}
}

// Repeating the sync must not grow the list: the step ID in metadata is the
// join, and every call rewrites the same row.
func TestPlanTodoSyncIsIdempotent(t *testing.T) {
	m := approvedPlanModel(t, "read code", "move files")
	before := planTodoRows(m)

	m.syncPlanTodos()
	m.syncPlanTodos()

	after := planTodoRows(m)
	if len(after) != len(before) {
		t.Fatalf("sync created rows: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID {
			t.Fatalf("row %d was recreated: %s -> %s", i, before[i].ID, after[i].ID)
		}
	}
}

// A resumed plan rebuilds the rows it lost, keeping the step in progress and
// the steps after it pending.
func TestResumedPlanRebuildsMissingTodos(t *testing.T) {
	m := approvedPlanModel(t, "read code", "move files")
	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()
	rows := planTodoRows(m)
	if err := m.services.Tracker.Delete(rows[0].ID); err != nil {
		t.Fatal(err)
	}

	resumed := newPlanModel(t)
	resumed.restorePlanState(m.plan.Snapshot())

	rebuilt := planTodoRows(resumed)
	if len(rebuilt) != 2 {
		t.Fatalf("resumed plan rows = %d, want 2", len(rebuilt))
	}
	if rebuilt[0].Status != todo.StatusInProgress {
		t.Fatalf("resumed first row = %q, want in_progress", rebuilt[0].Status)
	}
	if rebuilt[1].Status != todo.StatusPending {
		t.Fatalf("resumed second row = %q, want pending", rebuilt[1].Status)
	}
}

// A completed plan leaves its rows struck through: the list is the record of
// what just ran.
func TestCompletedPlanKeepsTodos(t *testing.T) {
	m := approvedPlanModel(t, "read code")
	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.plan.CompleteStep(plan.StepResult{Status: plan.StepCompleted, Result: "done"}); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()

	active, ok := m.plan.Active()
	if !ok || active.Status != plan.StatusCompleted {
		t.Fatalf("plan did not complete: %+v", active)
	}
	rows := planTodoRows(m)
	if len(rows) != 1 || rows[0].Status != todo.StatusCompleted {
		t.Fatalf("completed plan rows = %+v", rows)
	}
}

// Cancelling a plan removes the work it described, and nothing else.
func TestCancelledPlanDropsOnlyPlanTodos(t *testing.T) {
	m := approvedPlanModel(t, "read code", "move files")
	kept := m.services.Tracker.Create("Audit handlers", "read the handlers", "", nil)

	if err := m.plan.Cancel(); err != nil {
		t.Fatal(err)
	}
	m.syncPlanTodos()

	if rows := planTodoRows(m); len(rows) != 0 {
		t.Fatalf("cancelled plan left rows: %+v", rows)
	}
	if _, ok := m.services.Tracker.Get(kept.ID); !ok {
		t.Fatal("an ordinary todo was dropped with the plan")
	}
}

// Leaving Execute Mode takes the plan's rows with it; the plan itself stays in
// the session and comes back when execution resumes.
func TestPlanTodosDropWhenLeavingExecuteMode(t *testing.T) {
	m := approvedPlanModel(t, "read code")
	if rows := planTodoRows(m); len(rows) == 0 {
		t.Fatal("no rows to drop")
	}
	m.leavePlanMode()
	if rows := planTodoRows(m); len(rows) != 0 {
		t.Fatalf("rows survived leaving Execute Mode: %+v", rows)
	}
	if _, ok := m.plan.Active(); !ok {
		t.Fatal("leaving Plan Mode must not discard the plan")
	}
}

// A model without a tracker (early startup, tests) syncs nothing and panics on
// nothing.
func TestPlanTodoSyncWithoutTracker(t *testing.T) {
	m := approvedPlanModel(t, "read code")
	m.services.Tracker = nil
	m.syncPlanTodos()
	m.dropPlanTodos(func(string) bool { return true })
}
