package app

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/setting"
)

// stepReviewReport is a step report claiming the work is done, which is what the
// advisor may then refuse or only annotate.
const stepReviewReport = "```plan-step\n" +
	`{"status":"completed","result":"wired the gate","files":["internal/app/advisor.go"],"evidence":["go test ./internal/app/... ok"]}` +
	"\n```"

// newStepReviewModel is a Plan Mode model with a live advisor over a git
// workspace, so a step turn goes through the real review runner.
func newStepReviewModel(t *testing.T, answer string) *model {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	m := newPlanModel(t)
	m.env.CWD = dir
	m.env.LLMProvider = &advisorStubProvider{answer: answer}
	m.env.CurrentModel = &llm.CurrentModelInfo{ModelID: "stub-model"}
	on := true
	m.env.Advisor = setting.AdvisorSettings{
		Enable:   &on,
		Triggers: setting.AdvisorTriggers{BeforePlanStep: &on},
	}
	m.mainNotices = make(chan mainNotice, 8)
	m.advisorStarts = make(chan struct{}, 8)
	m.services.Advisor = AdvisorServices{Store: advisor.NewStore(), Indicator: NewAdvisorIndicator()}
	m.wireAdvisor()
	t.Cleanup(m.teardownAdvisor)
	return m
}

// startStepPlan restores an approved two-step plan and puts the first step in
// flight, which is the state a review is triggered from.
func startStepPlan(t *testing.T, m *model) plan.Step {
	t.Helper()
	running := testPlan("plan-review", "Reviewed plan", "wire the gate", "next")
	running.Status = plan.StatusExecuting
	running.Steps[0].Verification = []string{"go test ./internal/app/... ok"}
	running.StepIndex = 0
	m.plan.Restore(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{running}, ActiveID: running.ID})
	step, _, err := m.plan.BeginStep()
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// runTurnCmd runs one turn command off the test goroutine, so a review that
// hangs fails the test instead of the run.
func runTurnCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(30 * time.Second):
		t.Fatal("the step turn never finished")
		return nil
	}
}

// advanceRequested reports whether a command would start the next step.
func advanceRequested(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		_, advance := msg.(planAdvanceMsg)
		return advance
	case <-time.After(5 * time.Second):
		t.Fatal("the follow-up command never finished")
		return false
	}
}

// A P0 found on a step that reported completion keeps the completion — the work
// was done — but marks the step, pauses the plan so it cannot auto-advance, opens
// the panel, and explains both in the transcript. The findings stay in the
// session's review history.
func TestPlanStepWithP0PausesAndMarksTheStep(t *testing.T) {
	m := newStepReviewModel(t, blockingAdvisorAnswer)
	step := startStepPlan(t, m)

	cmd := m.onPlanTurn(core.Result{Content: stepReviewReport})
	msg := runTurnCmd(t, cmd)
	reviewed, ok := msg.(planStepReviewedMsg)
	if !ok {
		t.Fatalf("turn command returned %T, want the step's verdict", msg)
	}
	if !reviewed.found {
		t.Fatal("the review produced no verdict")
	}

	if _, followup := m.Update(reviewed); advanceRequested(t, followup) {
		t.Fatal("a refused completion advanced the plan")
	}

	active, ok := m.plan.Active()
	if !ok {
		t.Fatal("the plan is gone")
	}
	if active.Steps[0].Status != plan.StepCompleted {
		t.Fatalf("step status = %q, want the completion kept", active.Steps[0].Status)
	}
	if active.Steps[0].NeedsReview != string(advisor.SeverityP0) {
		t.Fatalf("NeedsReview = %q, want P0", active.Steps[0].NeedsReview)
	}
	reviews := m.services.Advisor.Store.Reviews()
	if len(reviews) != 1 {
		t.Fatalf("history = %d reviews, want 1", len(reviews))
	}
	if active.Steps[0].AdvisorReviewID != reviews[0].ID {
		t.Fatalf("AdvisorReviewID = %q, want %q", active.Steps[0].AdvisorReviewID, reviews[0].ID)
	}
	if !active.Paused {
		t.Fatal("a refused completion must pause the plan")
	}
	if active.Steps[1].Status != plan.StepPending {
		t.Fatal("the executor advanced past a refused step")
	}
	if !m.userInput.Plan.IsActive() {
		t.Fatal("the Execution Panel did not open on the refusal")
	}
	if actions := m.userInput.Plan.Actions(); !containsAll(actions, "Resume", "Cancel", "Close") {
		t.Fatalf("panel actions = %v, want the resume/cancel choice", actions)
	}

	// The review is aimed at this step, is recorded append-only, and its verdict
	// is in the history the panel reads.
	review := reviews[0]
	if review.Trigger != advisor.TriggerPlanStep || review.Target.Kind != advisor.TargetStep || review.Target.ID != step.ID {
		t.Errorf("review = %+v, want it aimed at the step", review)
	}
	if review.Status != advisor.StatusBlocked {
		t.Errorf("status = %q, want the blocked verdict", review.Status)
	}
	if len(review.Findings) != 1 || review.Findings[0].Severity != advisor.SeverityP0 {
		t.Errorf("findings = %+v, want the P0 kept in the session", review.Findings)
	}

	// The transcript explains the stop and points at the panel that holds the
	// detail.
	var noticed bool
	for _, message := range m.conv.Messages {
		if strings.Contains(message.Content, "advisor refused it") && strings.Contains(message.Content, "/advisor") {
			noticed = true
		}
	}
	if !noticed {
		t.Fatalf("conversation has no refusal notice: %+v", m.conv.Messages)
	}
	var logged bool
	for _, entry := range active.Progress {
		if entry.Kind == "step_review" && entry.StepID == step.ID {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("progress = %+v, want the refusal journaled", active.Progress)
	}
}

// Notes only: the step completes and the plan walks to the next step exactly as
// it does with no advisor. The finding stays in the history and changes nothing.
func TestPlanStepWithNotesAdvancesAsBefore(t *testing.T) {
	m := newStepReviewModel(t, notesAdvisorAnswer)
	startStepPlan(t, m)

	cmd := m.onPlanTurn(core.Result{Content: stepReviewReport})
	msg := runTurnCmd(t, cmd)
	reviewed, ok := msg.(planStepReviewedMsg)
	if !ok {
		t.Fatalf("turn command returned %T, want the step's verdict", msg)
	}
	if !reviewed.found {
		t.Fatal("the review produced no verdict")
	}

	if _, followup := m.Update(reviewed); !advanceRequested(t, followup) {
		t.Fatal("a notes-only review stopped the plan from advancing")
	}

	active, _ := m.plan.Active()
	if active.Steps[0].Status != plan.StepCompleted {
		t.Fatalf("step status = %q, want completed", active.Steps[0].Status)
	}
	if active.Steps[0].NeedsReview != "" || active.Steps[0].AdvisorReviewID != "" {
		t.Errorf("step = %+v, want no refusal recorded", active.Steps[0])
	}
	if active.Paused {
		t.Fatal("a notes-only review paused the plan")
	}
	if active.StepIndex != 1 {
		t.Fatalf("cursor = %d, want the next step", active.StepIndex)
	}
	reviews := m.services.Advisor.Store.Reviews()
	if len(reviews) != 1 || reviews[0].Status == advisor.StatusBlocked {
		t.Fatalf("history = %+v, want the P2 note recorded without a block", reviews)
	}
}

// With the trigger off the turn is the turn it always was: no review, no runner
// work, no notice, and the plan advances on its own.
func TestPlanStepReviewSkippedWhenTheTriggerIsOff(t *testing.T) {
	m := newStepReviewModel(t, blockingAdvisorAnswer)
	off := false
	m.env.Advisor.Triggers.BeforePlanStep = &off
	startStepPlan(t, m)

	cmd := m.onPlanTurn(core.Result{Content: stepReviewReport})
	if !advanceRequested(t, cmd) {
		t.Fatal("the turn did not request the next step")
	}
	if got := len(m.services.Advisor.Store.Reviews()); got != 0 {
		t.Fatalf("history = %d reviews, want none while the trigger is off", got)
	}
	select {
	case n := <-m.mainNotices:
		t.Fatalf("a skipped review published a notice: %+v", n)
	default:
	}
	if snap := m.services.Advisor.Indicator.Snapshot(); snap.Running {
		t.Error("a skipped review left the status bar spinning")
	}

	active, _ := m.plan.Active()
	if active.Steps[0].Status != plan.StepCompleted || active.Paused {
		t.Fatalf("plan = %+v, want the untouched advance", active)
	}
	if active.Steps[0].NeedsReview != "" {
		t.Fatalf("step = %+v, want no refusal", active.Steps[0])
	}
	if active.StepIndex != 1 {
		t.Fatalf("cursor = %d, want the next step", active.StepIndex)
	}
}

// A step that reports something other than completion is not reviewed: there is
// nothing to refuse, and the store's own rules still decide what happens.
func TestBlockedStepReportSkipsTheReview(t *testing.T) {
	m := newStepReviewModel(t, blockingAdvisorAnswer)
	startStepPlan(t, m)

	cmd := m.onPlanTurn(core.Result{Content: "```plan-step\n" + `{"status":"blocked","reason":"the vendor API is down"}` + "\n```"})
	if advanceRequested(t, cmd) {
		t.Fatal("a blocked step asked for the next step")
	}
	if got := len(m.services.Advisor.Store.Reviews()); got != 0 {
		t.Fatalf("history = %d reviews, want none for a blocked report", got)
	}
	active, _ := m.plan.Active()
	if active.Steps[0].Status != plan.StepBlocked || !active.Paused {
		t.Fatalf("plan = %+v, want the blocked step paused as before", active)
	}
	if active.Steps[0].NeedsReview != "" {
		t.Fatalf("step = %+v, want no refusal on a blocked step", active.Steps[0])
	}
}
