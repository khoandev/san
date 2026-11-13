package input

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/kit"
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

// The caller decides what the first Execute new plan carries: fast verify opens
// the panel on compact, while an explicit choice made with "c" survives a
// reopen instead of being reset by the caller's default.
func TestPlanPanelDefaultsTheExecuteContext(t *testing.T) {
	ready := panelPlan("plan-1", "Split auth", "read", "move")
	ready.Status = plan.StatusPendingApproval
	state := plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{ready}, ActiveID: ready.ID}

	p := NewPlanPanel()
	p.Enter(state, 100, 40, PlanContextCompact)
	if got := p.Context(); got != PlanContextCompact {
		t.Fatalf("first Execute new plan carries %q, want compact", got)
	}

	p.HandleKeypress(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if got := p.Context(); got != PlanContextFresh {
		t.Fatalf("c should step compact to fresh, got %q", got)
	}
	p.Enter(state, 100, 40, PlanContextCompact)
	if got := p.Context(); got != PlanContextFresh {
		t.Fatalf("reopening reset the person's choice to %q", got)
	}

	// No default, or an empty one, keeps the legacy opening: the planning
	// conversation carries over.
	plain := NewPlanPanel()
	plain.Enter(state, 100, 40)
	if got := plain.Context(); got != PlanContextKeep {
		t.Fatalf("without a default the panel opened on %q, want keep", got)
	}
	plain.Enter(state, 100, 40, "")
	if got := plain.Context(); got != PlanContextKeep {
		t.Fatalf("an empty default moved the panel to %q", got)
	}
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

// dockBudgetCheck is the card height the panel promises at a terminal size:
// three fifths of it, never past the rows above the dock separator.
func dockBudgetCheck(height int) int {
	return max(min(12, height-2), min(height*3/5, height-4))
}

// assertDockedCard checks the invariants every docked render must hold: it is
// a card, not a screen — inside its row and column budget — and it still ends
// on the hint row with the buttons above it.
func assertDockedCard(t *testing.T, view string, width, height int) {
	t.Helper()
	if got, budget := lipgloss.Height(view), dockBudgetCheck(height); got > budget {
		t.Fatalf("docked card is %d rows, budget is %d:\n%s", got, budget, view)
	}
	if got, limit := lipgloss.Width(view), min(width-4, 96); got > limit {
		t.Fatalf("docked card is %d columns, want at most %d:\n%s", got, limit, view)
	}
	// The hint is the last row by contract; it may drop its words on a narrow
	// card but never the keys themselves.
	if !strings.Contains(view, "Esc") {
		t.Fatalf("docked card lost its hint row:\n%s", view)
	}
}

// A docked card spends its rows on the plan, not on the terminal: however long
// the plan is, the card stops at its budget, says how many steps it hid, and
// keeps the buttons and the hint.
func TestPlanPanelDockedCardStaysWithinItsBudget(t *testing.T) {
	const width, height = 100, 40

	steps := make([]string, 14)
	for i := range steps {
		steps[i] = fmt.Sprintf("step %d", i+1)
	}
	ready := panelPlan("plan-long", "A long plan", steps...)
	ready.Status = plan.StatusPendingApproval
	ready.Goal = strings.Repeat("why this plan exists ", 8)
	ready.Risks = []string{strings.Repeat("flaky tests ", 10), strings.Repeat("a wide refactor ", 10)}
	ready.OpenQuestions = []string{strings.Repeat("keep the legacy cookie? ", 6)}
	ready.Decisions = []plan.Decision{{Text: strings.Repeat("reuse the helper ", 10)}}

	p := NewPlanPanel()
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{ready}, ActiveID: ready.ID}, width, height)
	view := p.Render()

	assertDockedCard(t, view, width, height)
	for _, want := range []string{"Execution Panel", "A long plan", "pending_approval", "Execute new plan"} {
		if !strings.Contains(view, want) {
			t.Fatalf("docked card is missing %q:\n%s", want, view)
		}
	}
	// The step list is bounded by the budget and says what it hid.
	if !strings.Contains(view, "… ") || !strings.Contains(view, "more") {
		t.Fatalf("a clipped step list must say how much it hid:\n%s", view)
	}
	if strings.Contains(view, "14. step 14") {
		t.Fatalf("the last step fits only if the card is not docked:\n%s", view)
	}

	if !strings.Contains(view, "confirm") {
		t.Fatalf("a wide card keeps the hint's words:\n%s", view)
	}

	// A narrower terminal narrows the card; the frame keeps its margins.
	p.Resize(60, height)
	assertDockedCard(t, p.Render(), 60, height)
}

// The conflict warning is the reason the panel opened in that state, so it
// outranks the plan's detail and survives the docked budget.
func TestPlanPanelDockedCardKeepsTheConflictWarning(t *testing.T) {
	const width, height = 100, 40

	running := panelPlan("plan-old", "Running", "a", "b")
	running.Status = plan.StatusExecuting
	running.Steps[0].Status = plan.StepInProgress
	fresh := panelPlan("plan-new", "Fresh", "x", "y")
	fresh.Status = plan.StatusPendingApproval

	p := NewPlanPanel()
	p.Enter(plan.State{
		Mode:     plan.ModeExecute,
		Plans:    []plan.Plan{running, fresh},
		ActiveID: running.ID,
		DraftID:  fresh.ID,
	}, width, height)

	view := p.Render()
	assertDockedCard(t, view, width, height)
	if !strings.Contains(view, "still unfinished") {
		t.Fatalf("the docked card dropped the conflict warning:\n%s", view)
	}
	if !strings.Contains(view, "Keep existing context") {
		t.Fatalf("the docked card dropped the conflict buttons:\n%s", view)
	}
}

// Reviewing a plan docks it above the composer; editing the plan's text is the
// one state that needs the whole frame, so it leaves the dock.
func TestPlanPanelDocksExceptWhileEditing(t *testing.T) {
	p := NewPlanPanel()
	ready := panelPlan("plan-1", "Split auth", "read", "move")
	ready.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{ready}, ActiveID: ready.ID}, 100, 40)

	if !p.Docked() {
		t.Fatal("a plan under review must dock above the composer")
	}
	p.HandleKeypress(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if !p.editing {
		t.Fatal("e did not open the editor")
	}
	if p.Docked() {
		t.Fatal("the editor must take the full screen, not the dock")
	}
	p.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !p.Docked() {
		t.Fatal("discarding the editor must return the panel to the dock")
	}
}

// sgrOpening returns the escape sequence a style opens with, so a test can name
// a theme color without hard-coding its RGB frames.
func sgrOpening(style lipgloss.Style) string {
	rendered := style.Render("x")
	if i := strings.IndexByte(rendered, 'm'); i >= 0 {
		return rendered[:i+1]
	}
	return ""
}

// panelRow returns the rendered row whose visible text contains label.
func panelRow(t *testing.T, view, label string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(line), label) {
			return line
		}
	}
	t.Fatalf("no row for %q:\n%s", label, ansi.Strip(view))
	return ""
}

// SGR strike-on and strike-off, as conv's checklist emits them.
const (
	testStrikeOn  = "\x1b[9m"
	testStrikeOff = "\x1b[29m"
)

// The step list and the strip above the composer are one system: the same
// states, the same glyphs, the same colors. A finished step keeps its row and
// is struck through, the current one wears the accent, skipped work reads as
// closed, and a blocked step says why.
func TestPlanPanelStepsUseTheSharedChecklist(t *testing.T) {
	p := NewPlanPanel()
	pl := panelPlan("plan-mixed", "Mixed progress",
		"read code", "move files", "rewrite tests", "wire the API", "ship it")
	pl.Status = plan.StatusExecuting
	pl.Steps[0].Status = plan.StepCompleted
	pl.Steps[1].Status = plan.StepInProgress
	pl.Steps[2].Status = plan.StepSkipped
	pl.Steps[3].Status = plan.StepBlocked
	pl.Steps[3].BlockReason = "tests failed"
	pl.StepIndex = 1
	p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{pl}, ActiveID: pl.ID}, 100, 40)
	view := p.Render()

	done := panelRow(t, view, "read code")
	if !strings.Contains(done, testStrikeOn) || !strings.Contains(done, testStrikeOff) {
		t.Fatalf("a finished step is not struck through:\n%q", done)
	}
	if !strings.Contains(ansi.Strip(done), kit.CurrentSymbols.Success) {
		t.Fatalf("a finished step lost its check glyph:\n%s", ansi.Strip(done))
	}

	current := panelRow(t, view, "move files")
	if accent := sgrOpening(lipgloss.NewStyle().Foreground(kit.CurrentTheme.Accent).Bold(true)); !strings.Contains(current, accent) {
		t.Fatalf("the current step does not use the accent %q:\n%q", accent, current)
	}
	if strings.Contains(current, testStrikeOn) {
		t.Fatalf("the current step is struck through:\n%q", current)
	}
	if !strings.Contains(ansi.Strip(current), kit.CurrentSymbols.LiveDot) {
		t.Fatalf("the current step lost the live dot:\n%s", ansi.Strip(current))
	}

	skipped := panelRow(t, view, "rewrite tests")
	if !strings.Contains(skipped, testStrikeOn) {
		t.Fatalf("a skipped step is not struck through:\n%q", skipped)
	}
	if !strings.Contains(ansi.Strip(skipped), "- 3. rewrite tests") {
		t.Fatalf("a skipped step lost its dash:\n%s", ansi.Strip(skipped))
	}

	blocked := panelRow(t, view, "wire the API")
	if warning := sgrOpening(lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)); !strings.Contains(blocked, warning) {
		t.Fatalf("a blocked step does not use the warning color %q:\n%q", warning, blocked)
	}
	if !strings.Contains(ansi.Strip(blocked), "tests failed") {
		t.Fatalf("a blocked step dropped its reason:\n%s", ansi.Strip(blocked))
	}

	pending := panelRow(t, view, "ship it")
	if !strings.Contains(ansi.Strip(pending), kit.CurrentSymbols.RadioOff) {
		t.Fatalf("a pending step lost the open circle:\n%s", ansi.Strip(pending))
	}
	if strings.Contains(pending, testStrikeOn) {
		t.Fatalf("a pending step is struck through:\n%q", pending)
	}

	// The ordinal is the step's place in the plan, not its id.
	for _, want := range []string{"1. read code", "2. move files", "5. ship it"} {
		if !strings.Contains(ansi.Strip(view), want) {
			t.Fatalf("the step list lost its ordinal (%q):\n%s", want, ansi.Strip(view))
		}
	}
}

// The STEPS heading carries the same reads as the strip: the exact counts, the
// five-cell bar, and the flags. A card too narrow for the bar keeps the counts.
func TestPlanPanelStepsHeadingMirrorsTheStrip(t *testing.T) {
	p := NewPlanPanel()
	pl := panelPlan("plan-counts", "Split auth", "read", "move", "audit", "ship")
	pl.Steps[0].Status = plan.StepCompleted
	pl.Steps[1].Status = plan.StepSkipped
	pl.Steps[2].Status = plan.StepBlocked
	pl.Steps[2].BlockReason = "tests failed"
	pl.Status = plan.StatusPendingApproval
	p.Enter(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{pl}, ActiveID: pl.ID}, 100, 40)

	view := ansi.Strip(p.Render())
	for _, want := range []string{"STEPS  1/4 done", "▰▱▱▱▱", "2 handled", "1 blocked"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the steps heading is missing %q:\n%s", want, view)
		}
	}

	// At the narrowest card the bar is the first thing to go; the counts and
	// the list itself stay.
	p.Resize(30, 40)
	narrow := ansi.Strip(p.Render())
	if strings.Contains(narrow, "▰") || strings.Contains(narrow, "▱") {
		t.Fatalf("the narrow card kept a bar it cannot fit:\n%s", narrow)
	}
	if !strings.Contains(narrow, "1/4 done") || !strings.Contains(narrow, "read") {
		t.Fatalf("the narrow card lost the counts or the list:\n%s", narrow)
	}
}

// The current step's dot is solid: the panel says which step is running with
// color and weight, and nothing on the card blinks while a person reads it.
func TestPlanPanelCurrentStepHoldsStill(t *testing.T) {
	p := NewPlanPanel()
	running := runningStep()
	p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{running}, ActiveID: running.ID}, 100, 40)

	first := p.Render()
	if !strings.Contains(ansi.Strip(first), kit.CurrentSymbols.LiveDot) {
		t.Fatalf("the current step lost its live dot:\n%s", ansi.Strip(first))
	}
	for range 4 {
		if again := p.Render(); again != first {
			t.Fatal("the panel changed between renders without a state change")
		}
	}
}

// However long the plan, the executing card stays inside its budget and keeps
// the buttons and the hint: the list is the one thing that yields.
func TestPlanPanelExecutingCardKeepsBudgetAndButtons(t *testing.T) {
	const width, height = 100, 30

	steps := make([]string, 14)
	for i := range steps {
		steps[i] = fmt.Sprintf("step %d", i+1)
	}
	running := panelPlan("plan-long", "A long run", steps...)
	running.Status = plan.StatusExecuting
	running.Steps[0].Status = plan.StepCompleted
	running.Steps[1].Status = plan.StepInProgress
	running.StepIndex = 1

	p := NewPlanPanel()
	p.Enter(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{running}, ActiveID: running.ID}, width, height)
	view := p.Render()

	assertDockedCard(t, view, width, height)
	for _, want := range []string{"Pause", "Skip step", "Cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the executing card dropped the %q button:\n%s", want, ansi.Strip(view))
		}
	}
	// The list is what yields: past the budget the card shows a prefix of the
	// steps rather than growing a row per step.
	if strings.Contains(ansi.Strip(view), "step 14") {
		t.Fatalf("the card grew past its budget to fit every step:\n%s", ansi.Strip(view))
	}
}
