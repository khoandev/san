package app

// Plan steps are todos. This file projects the active plan onto the session's
// todo tracker so the strip above the composer shows the plan the same way it
// shows any other tracked work: one row per step, the running step accented,
// finished steps struck through. The plan store remains the source of truth —
// every sync re-derives each item from its step — and the join is the step ID
// in the item's metadata, which is what makes repeated syncs and a resumed
// session idempotent.

import (
	"strings"

	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/todo"
)

// syncPlanTodos projects the active plan's steps onto the todo tracker.
//
// Rows outlive only the plan that owns them. A step's row exists while the plan
// executes, and survives its completion — struck through, the record a person
// just watched happen. A cancelled, superseded, or replaced plan's rows are
// dropped: they would describe work nobody is running any more.
func (m *model) syncPlanTodos() {
	tracker := m.services.Tracker
	if tracker == nil {
		return
	}
	active, ok := m.plan.Active()
	if !ok {
		m.dropPlanTodos(func(string) bool { return true })
		return
	}
	// Only the active plan may own rows. This also clears the rows of a plan
	// that a newer one replaced, without the caller having to know it happened.
	m.dropPlanTodos(func(planID string) bool { return planID != active.ID })

	live := active.Status == plan.StatusCompleted ||
		(m.plan.Mode() == plan.ModeExecute && active.Status == plan.StatusExecuting)
	if !live {
		m.dropPlanTodos(func(planID string) bool { return planID == active.ID })
		return
	}
	for _, step := range active.Steps {
		syncPlanStep(tracker, active, step)
	}
}

// syncPlanStep keeps one projected item in step with its plan step. A missing
// item is created — including one the model deleted — so the list can never
// drift from the plan it describes.
func syncPlanStep(tracker *todo.Store, p plan.Plan, step plan.Step) {
	status, blockReason := planStepTodoStatus(step)
	metadata := todo.PlanMetadata(p.ID, step.ID, string(step.Status), blockReason)
	if item := tracker.FindByMetadata(todo.MetadataPlanStepID, step.ID); item != nil {
		_ = tracker.Update(item.ID,
			todo.WithSubject(planStepSubject(step)),
			todo.WithDescription(planStepDescription(step)),
			todo.WithStatus(status),
			todo.WithMetadata(metadata),
		)
		return
	}
	created := tracker.Create(planStepSubject(step), planStepDescription(step), "", metadata)
	_ = tracker.Update(created.ID, todo.WithStatus(status))
}

// planStepTodoStatus maps a step onto the item status the todo list persists.
// Blocked is not a todo status: a blocked step is unfinished work, so it stays
// pending and carries the plan's own status (and reason) in metadata, which is
// what the renderer reads to draw it as blocked.
func planStepTodoStatus(step plan.Step) (status, blockReason string) {
	switch step.Status {
	case plan.StepInProgress:
		return todo.StatusInProgress, ""
	case plan.StepCompleted, plan.StepSkipped:
		return todo.StatusCompleted, ""
	case plan.StepBlocked:
		return todo.StatusPending, strings.TrimSpace(step.BlockReason)
	}
	return todo.StatusPending, ""
}

// planStepSubject is the row text. A plan step always has a title after
// Normalize; the fallback keeps an edited plan from producing a blank row.
func planStepSubject(step plan.Step) string {
	if title := strings.TrimSpace(step.Title); title != "" {
		return title
	}
	return "Untitled step"
}

// planStepDescription is what TaskGet and the item's details show: how the step
// is done, then what proves it is done.
func planStepDescription(step plan.Step) string {
	var parts []string
	if detail := strings.TrimSpace(step.Detail); detail != "" {
		parts = append(parts, detail)
	}
	if objective := strings.TrimSpace(step.Objective); objective != "" {
		parts = append(parts, "Objective: "+objective)
	}
	return strings.Join(parts, "\n")
}

// dropPlanTodos deletes the projected items whose plan ID matches. Items the
// model authored carry no plan ID and are never touched.
func (m *model) dropPlanTodos(match func(planID string) bool) {
	tracker := m.services.Tracker
	if tracker == nil {
		return
	}
	for _, item := range tracker.List() {
		if planID := todo.PlanID(item); planID != "" && match(planID) {
			_ = tracker.Delete(item.ID)
		}
	}
}
