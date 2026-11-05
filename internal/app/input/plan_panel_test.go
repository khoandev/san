package input

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/plan"
)

func panelPlan(id, title string, steps ...string) plan.Plan {
	p := plan.Plan{ID: id, Title: title, Goal: "goal"}
	for _, step := range steps {
		p.Steps = append(p.Steps, plan.Step{Title: step})
	}
	p.Normalize()
	return p
}

func actionsOf(t *testing.T, p PlanPanel) []string {
	t.Helper()
	return p.Actions()
}

func hasAction(actions []string, want string) bool {
	for _, action := range actions {
		if action == want {
			return true
		}
	}
	return false
}

// A ready plan offers Execute new plan without any further command, and the
// panel renders the plan, its steps and the buttons.
func TestPlanPanelShowsReadyPlan(t *testing.T) {
	p := NewPlanPanel()
	ready := panelPlan("plan-1", "Split auth", "read", "move")
	ready.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{ready}, ActiveID: ready.ID}, 100, 40)

	if !p.IsActive() {
		t.Fatal("Enter did not activate the panel")
	}
	if got := p.Actions(); len(got) == 0 || got[0] != "Execute new plan" {
		t.Fatalf("actions = %v, want Execute new plan first", got)
	}
	view := p.Render()
	for _, want := range []string{"Execution Panel", "Split auth", "read", "move", "Execute new plan", "Cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel is missing %q:\n%s", want, view)
		}
	}
}

// Two plans at once is the one state that needs both core choices.
func TestPlanPanelOffersBothFlowsOnConflict(t *testing.T) {
	p := NewPlanPanel()
	running := panelPlan("plan-old", "Running", "a")
	running.Status = plan.StatusExecuting
	running.StepIndex = 0
	draft := panelPlan("plan-new", "Fresh", "x")
	draft.Status = plan.StatusPendingApproval
	p.Enter(plan.State{
		Mode:     plan.ModeExecute,
		Plans:    []plan.Plan{running, draft},
		ActiveID: running.ID,
		DraftID:  draft.ID,
	}, 100, 40)

	actions := actionsOf(t, p)
	for _, want := range []string{"Execute new plan", "Keep existing context", "Edit plan", "Cancel"} {
		if !hasAction(actions, want) {
			t.Fatalf("actions = %v, missing %q", actions, want)
		}
	}
	if !strings.Contains(p.Render(), "unfinished") {
		t.Fatal("the conflict is not explained in the panel")
	}
}

// Each execution state offers exactly the actions that make sense in it.
func TestPlanPanelActionsFollowState(t *testing.T) {
	cases := []struct {
		name  string
		plan  plan.Plan
		wants []string
		nots  []string
	}{
		{
			name:  "running",
			plan:  runningStep(),
			wants: []string{"Pause", "Skip step", "Cancel"},
			nots:  []string{"Resume", "Retry step"},
		},
		{
			name:  "paused",
			plan:  pausedPlan(),
			wants: []string{"Resume", "Skip step", "Cancel"},
			nots:  []string{"Pause"},
		},
		{
			name:  "blocked",
			plan:  blockedPlan(),
			wants: []string{"Retry step", "Restart step", "Skip step", "Cancel"},
		},
		{
			name:  "awaiting choice",
			plan:  awaitingPlan(),
			wants: []string{"Continue step", "Restart step", "Skip step", "Cancel"},
		},
		{
			name:  "finished",
			plan:  finishedPlan(),
			wants: []string{"Close"},
			nots:  []string{"Pause", "Cancel", "Execute new plan"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPlanPanel()
			p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{tc.plan}, ActiveID: tc.plan.ID}, 100, 40)
			actions := actionsOf(t, p)
			for _, want := range tc.wants {
				if !hasAction(actions, want) {
					t.Fatalf("actions = %v, missing %q", actions, want)
				}
			}
			for _, not := range tc.nots {
				if hasAction(actions, not) {
					t.Fatalf("actions = %v, must not offer %q", actions, not)
				}
			}
		})
	}
}

func runningStep() plan.Plan {
	p := panelPlan("plan-run", "Running", "one", "two")
	p.Status = plan.StatusExecuting
	p.Steps[0].Status = plan.StepInProgress
	p.StepIndex = 0
	return p
}

func pausedPlan() plan.Plan {
	p := runningStep()
	p.Paused = true
	return p
}

func blockedPlan() plan.Plan {
	p := runningStep()
	p.Steps[0].Status = plan.StepBlocked
	p.Steps[0].BlockReason = "tests failed"
	p.Paused = true
	return p
}

func awaitingPlan() plan.Plan {
	p := runningStep()
	p.AwaitingStepChoice = true
	p.Paused = true
	return p
}

func finishedPlan() plan.Plan {
	p := panelPlan("plan-done", "Finished", "one")
	p.Status = plan.StatusCompleted
	p.Steps[0].Status = plan.StepCompleted
	p.StepIndex = 0
	return p
}

// A step that stopped keeps its evidence on screen: what it changed, what was
// observed, and why it is blocked.
func TestPlanPanelShowsStepEvidence(t *testing.T) {
	p := NewPlanPanel()
	stopped := blockedPlan()
	stopped.Steps[0].Files = []string{"internal/auth/auth.go"}
	stopped.Steps[0].Evidence = []string{"go test ./internal/auth/... — 1 failing"}
	p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{stopped}, ActiveID: stopped.ID}, 120, 40)

	view := p.Render()
	for _, want := range []string{"internal/auth/auth.go", "go test ./internal/auth/", "tests failed", "Retry step"} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel is missing %q:\n%s", want, view)
		}
	}
}

// Choosing a button produces the action message and nothing else — the store
// is the app's, so the panel never mutates the plan.
func TestPlanPanelEmitsActionOnEnter(t *testing.T) {
	p := NewPlanPanel()
	draft := panelPlan("plan-1", "Draft", "one")
	draft.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{draft}, ActiveID: draft.ID}, 100, 40)

	cmd := p.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter produced no command")
	}
	msg, ok := cmd().(PlanActionMsg)
	if !ok || msg.Action != PlanActionExecuteNew {
		t.Fatalf("message = %#v, want the Execute new plan action", msg)
	}
}

// Edit plan opens an in-memory editor over the plan text: the edit keeps the
// plan's identity and the session never grows a plan file.
func TestPlanPanelEditEmitsMergedPlan(t *testing.T) {
	p := NewPlanPanel()
	draft := panelPlan("plan-1", "Draft", "one")
	draft.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{draft}, ActiveID: draft.ID}, 120, 40)

	p.HandleKeypress(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if !p.editing {
		t.Fatal("e did not open the plan editor")
	}
	if got := p.editor.Value(); !strings.Contains(got, "title: Draft") {
		t.Fatalf("editor seeded with:\n%s", got)
	}
	p.editor.SetValue("title: Renamed\ngoal: new goal\n\nin-scope:\n- internal/auth\n\nsteps:\n- first | do it | test it\n- second\n")

	cmd := p.HandleKeypress(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+s produced no command")
	}
	msg, ok := cmd().(PlanEditSavedMsg)
	if !ok {
		t.Fatalf("message = %#v, want PlanEditSavedMsg", msg)
	}
	if msg.Plan.ID != "plan-1" {
		t.Fatalf("edited plan lost its identity: %q", msg.Plan.ID)
	}
	if msg.Plan.Title != "Renamed" || len(msg.Plan.Steps) != 2 {
		t.Fatalf("edited plan = %+v", msg.Plan)
	}
	if msg.Plan.Steps[0].Objective != "do it" || len(msg.Plan.Steps[0].Verification) != 1 {
		t.Fatalf("step 1 = %+v", msg.Plan.Steps[0])
	}
	if p.editing {
		t.Fatal("the editor stayed open after a successful save")
	}
}

// A malformed edit is refused in place, with the reason under the editor.
func TestPlanPanelEditRefusesInvalidText(t *testing.T) {
	p := NewPlanPanel()
	draft := panelPlan("plan-1", "Draft", "one")
	draft.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{draft}, ActiveID: draft.ID}, 120, 40)

	p.HandleKeypress(tea.KeyPressMsg{Code: 'e', Text: "e"})
	p.editor.SetValue("title: No steps\n")
	if cmd := p.HandleKeypress(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}); cmd != nil {
		t.Fatal("an invalid plan was accepted")
	}
	if !p.editing || !strings.Contains(p.status, "Not saved") {
		t.Fatalf("status = %q, editing = %v", p.status, p.editing)
	}
}

// Esc dismisses the panel without touching the plan: the state is the app's.
func TestPlanPanelEscClosesWithoutAction(t *testing.T) {
	p := NewPlanPanel()
	draft := panelPlan("plan-1", "Draft", "one")
	draft.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{draft}, ActiveID: draft.ID}, 100, 40)

	if cmd := p.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
		t.Fatalf("esc produced a command: %#v", cmd)
	}
	if p.IsActive() {
		t.Fatal("esc did not close the panel")
	}
	if p.Render() != "" {
		t.Fatal("a closed panel still renders")
	}
}

// The memoized render can never go stale: every change the body reads —
// a refreshed snapshot, the cursor, the status line, the editor — marks it
// dirty, so the panel only ever shows what the store holds.
func TestPlanPanelRenderFollowsEveryChange(t *testing.T) {
	p := NewPlanPanel()
	running := runningStep()
	p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{running}, ActiveID: running.ID}, 100, 40)
	first := p.Render()

	next := running.Clone()
	next.Steps[0].Status = plan.StepCompleted
	next.Steps[0].Result = "shipped the helper"
	next.StepIndex = 1
	p.Refresh(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{next}, ActiveID: next.ID})
	if got := p.Render(); got == first {
		t.Fatal("Refresh left the stale render in place")
	}

	p.setStatus("a fresh status line")
	if got := p.Render(); !strings.Contains(got, "a fresh status line") {
		t.Fatalf("status change did not reach the render:\n%s", got)
	}

	p.Resize(80, 30)
	if !strings.Contains(p.Render(), "a fresh status line") {
		t.Fatalf("resize dropped the status line:\n%s", p.Render())
	}

	// The editor is never served from the body cache.
	draft := panelPlan("plan-2", "New draft", "one")
	draft.Status = plan.StatusPendingApproval
	p.Refresh(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{next, draft}, ActiveID: next.ID, DraftID: draft.ID})
	p.HandleKeypress(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if !p.editing {
		t.Fatal("e did not open the editor on a pending draft")
	}
	if got := p.Render(); !strings.Contains(got, "title: New draft") {
		t.Fatalf("the editor did not render:\n%s", got)
	}
	p.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := p.Render(); strings.Contains(got, "title: New draft") {
		t.Fatalf("discarding the editor left it on screen:\n%s", got)
	}
}

// The panel re-renders on every spinner tick, so the render is memoized per
// snapshot: a steady-state frame must not redo the layout work.
func BenchmarkPlanPanelRender(b *testing.B) {
	for _, cached := range []bool{false, true} {
		name := "rebuild"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			long := panelPlan("plan-long", "A long plan", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l")
			long.Status = plan.StatusExecuting
			long.Risks = []string{"flaky tests", "a wide refactor"}
			long.OpenQuestions = []string{"keep the legacy cookie?"}
			long.Steps[0].Status = plan.StepInProgress
			long.Steps[0].Objective = "map every caller of the old helper"
			long.Steps[0].Verification = []string{"rg oldHelper", "go build ./..."}
			p := NewPlanPanel()
			p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{long}, ActiveID: long.ID}, 120, 40)
			p.Render()
			b.ReportAllocs()
			for b.Loop() {
				if !cached {
					p.invalidate()
				}
				p.Render()
			}
		})
	}
}

// A completion a review refused must not read as a clean one: the step keeps its
// completion, but the panel marks the row, names the severity and the review, and
// points at the panel that holds the findings.
func TestPlanPanelShowsRefusedCompletion(t *testing.T) {
	p := NewPlanPanel()
	refused := panelPlan("plan-refused", "Reviewed", "wire the gate", "next")
	refused.Status = plan.StatusExecuting
	refused.Steps[0].Status = plan.StepCompleted
	refused.Steps[0].Result = "wired it"
	refused.Steps[0].AdvisorReviewID = "review-7"
	refused.Steps[0].NeedsReview = "P0"
	refused.StepIndex = 1
	refused.Paused = true
	p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{refused}, ActiveID: refused.ID}, 120, 40)

	view := p.Render()
	for _, want := range []string{"NEEDS REVIEW", "P0", "review-7", "/advisor", "1 needs review", "Resume"} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "✓ 1. wire the gate") {
		t.Fatalf("a refused completion still reads as a clean one:\n%s", view)
	}

	// A clean plan says nothing about reviews, so the section never takes room
	// from an ordinary plan.
	clean := NewPlanPanel()
	clean.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{runningStep()}, ActiveID: "plan-run"}, 120, 40)
	if strings.Contains(clean.Render(), "NEEDS REVIEW") {
		t.Fatalf("a clean plan rendered the refusal section:\n%s", clean.Render())
	}
}

// Execute new plan carries a context choice: keep the planning conversation,
// summarize it first, or start a new session. The panel owns the choice and
// sends it with the action; c cycles it while a plan is ready to run.
func TestPlanPanelCyclesExecuteContext(t *testing.T) {
	p := NewPlanPanel()
	ready := panelPlan("plan-1", "Split auth", "read", "move")
	ready.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{ready}, ActiveID: ready.ID}, 100, 40)

	if got := p.Context(); got != PlanContextKeep {
		t.Fatalf("default context = %q, want keep", got)
	}
	for _, want := range []string{"Context", "keep", "planning history"} {
		if !strings.Contains(p.Render(), want) {
			t.Fatalf("panel is missing the context row (%q):\n%s", want, p.Render())
		}
	}

	steps := []PlanContext{PlanContextCompact, PlanContextFresh, PlanContextKeep}
	for _, want := range steps {
		p.HandleKeypress(tea.KeyPressMsg{Code: 'c', Text: "c"})
		if got := p.Context(); got != want {
			t.Fatalf("context = %q, want %q", got, want)
		}
	}

	// The choice rides on the action, so the app never has to read the panel.
	p.HandleKeypress(tea.KeyPressMsg{Code: 'c', Text: "c"}) // → compact
	cmd := p.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on Execute new plan emitted nothing")
	}
	msg, ok := cmd().(PlanActionMsg)
	if !ok || msg.Action != PlanActionExecuteNew || msg.Context != PlanContextCompact {
		t.Fatalf("message = %+v, want execute-new with the compact context", msg)
	}
}

// A narrow card keeps the context control and drops the sentence explaining it:
// the choice is what a person acts on, and a description broken across two rows
// under it reads as noise.
func TestPlanPanelContextRowFitsANarrowCard(t *testing.T) {
	p := NewPlanPanel()
	ready := panelPlan("plan-1", "Split auth", "read", "move")
	ready.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{ready}, ActiveID: ready.ID}, 56, 40)

	body := p.Render()
	if !strings.Contains(body, "keep") {
		t.Fatalf("the narrow card lost the context control:\n%s", body)
	}
	if strings.Contains(body, "planning history carries over") {
		t.Fatalf("the narrow card kept a description it cannot fit:\n%s", body)
	}
}

// The context row is only a decision while a plan is about to run; an executing
// plan has no context to choose.
func TestPlanPanelHidesContextWhileExecuting(t *testing.T) {
	p := NewPlanPanel()
	running := runningStep()
	p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{running}, ActiveID: running.ID}, 100, 40)

	if p.offersExecute() {
		t.Fatal("a running plan should not offer Execute new plan")
	}
	if strings.Contains(p.Render(), "Context") {
		t.Fatalf("a running plan rendered the context row:\n%s", p.Render())
	}
	p.HandleKeypress(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if got := p.Context(); got != PlanContextKeep {
		t.Fatalf("c changed the context with nothing to execute: %q", got)
	}
}
