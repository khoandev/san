package plan

import (
	"strings"
	"testing"
	"time"
)

func draftPlan(t *testing.T) Plan {
	t.Helper()
	p, found, err := ParsePlanBlock("```plan\n" + `{
	  "title": "Add plan mode",
	  "goal": "Ship it cleanly",
	  "inScope": ["app", "session"],
	  "outOfScope": ["cloud"],
	  "constraints": ["no extra commands"],
	  "risks": ["stale plans"],
	  "openQuestions": ["panel placement?"],
	  "decisions": ["state in session"],
	  "steps": [
	    {"title": "Domain model", "objective": "store plans", "verification": ["unit tests pass"]},
	    {"title": "Wire /plan", "objective": "enter mode", "verification": ["command test"]}
	  ]
	}` + "\n```")
	if err != nil || !found {
		t.Fatalf("parse plan: found=%v err=%v", found, err)
	}
	return p
}

func TestParsePlanBlock(t *testing.T) {
	p := draftPlan(t)
	if p.Title != "Add plan mode" || p.Goal != "Ship it cleanly" {
		t.Fatalf("title/goal = %q/%q", p.Title, p.Goal)
	}
	if len(p.Steps) != 2 || len(p.Steps[0].Verification) != 1 {
		t.Fatalf("steps = %+v", p.Steps)
	}
	if p.ID == "" || p.Steps[0].ID == "" || p.Steps[0].Status != StepPending {
		t.Fatalf("normalization failed: %+v", p)
	}
	if p.Status != StatusPendingApproval {
		t.Fatalf("status = %q", p.Status)
	}
}

func TestParsePlanBlockLastWins(t *testing.T) {
	text := strings.Join([]string{
		"```plan", `{"title":"first","steps":[{"title":"a"}]}`, "```",
		"some prose",
		"```plan", `{"title":"second","steps":[{"title":"b"}]}`, "```",
	}, "\n")
	p, found, err := ParsePlanBlock(text)
	if err != nil || !found {
		t.Fatalf("parse: %v %v", found, err)
	}
	if p.Title != "second" {
		t.Fatalf("last plan block must win, got %q", p.Title)
	}
}

func TestParsePlanBlockRejectsMalformedJSON(t *testing.T) {
	_, found, err := ParsePlanBlock("```plan\n{not json}\n```")
	if !found || err == nil {
		t.Fatalf("malformed block must report an error, found=%v err=%v", found, err)
	}
}

func TestParseUpdateBlockMerges(t *testing.T) {
	p := draftPlan(t)
	u, found, err := ParseUpdateBlock("```plan-update\n" + `{
	  "risks": ["new risk"],
	  "openQuestions": ["who owns the panel?"],
	  "decisions": ["panel is fullscreen"]
	}` + "\n```")
	if err != nil || !found {
		t.Fatalf("parse update: %v %v", found, err)
	}
	Merge(&p, u)
	if len(p.Risks) != 2 || p.Risks[1] != "new risk" {
		t.Fatalf("risks = %v", p.Risks)
	}
	if len(p.OpenQuestions) != 2 {
		t.Fatalf("questions = %v", p.OpenQuestions)
	}
	if len(p.Decisions) != 2 || p.Decisions[1].Text != "panel is fullscreen" {
		t.Fatalf("decisions = %+v", p.Decisions)
	}
}

func TestParseStepBlockNormalizesStatus(t *testing.T) {
	r, found, err := ParseStepBlock("```plan-step\n" + `{
	  "status": "success",
	  "result": "done",
	  "files": ["a.go"],
	  "evidence": ["go test ./..."]
	}` + "\n```")
	if err != nil || !found {
		t.Fatalf("parse step: %v %v", found, err)
	}
	if r.Status != StepCompleted || len(r.Evidence) != 1 {
		t.Fatalf("result = %+v", r)
	}

	r, _, err = ParseStepBlock("```plan-step\n{\"status\":\"exploded\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StepBlocked || r.Reason == "" {
		t.Fatalf("unknown status must become a reasoned block: %+v", r)
	}
}

func TestSubmitDraftStartsInPlanMode(t *testing.T) {
	s := NewStore()
	p := s.SubmitDraft(draftPlan(t))
	if s.Mode() != ModePlan {
		t.Fatalf("mode = %q", s.Mode())
	}
	active, ok := s.Active()
	if !ok || active.ID != p.ID {
		t.Fatalf("active = %+v ok=%v", active, ok)
	}
	if s.HasConflict() {
		t.Fatal("a first plan must not conflict")
	}
}

func TestExecuteNewPlanKeepsAndSupersedesOldPlan(t *testing.T) {
	s := NewStore()
	old := s.SubmitDraft(draftPlan(t))
	if err := s.Approve(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginStep(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteStep(StepResult{Status: StepCompleted, Result: "old work", Evidence: []string{"proof"}}); err != nil {
		t.Fatal(err)
	}

	next := draftPlan(t)
	next.Title = "Replacement"
	next.Steps = next.Steps[:1]
	s.SubmitDraft(next)
	if !s.HasConflict() {
		t.Fatal("a draft over an unfinished plan must conflict")
	}
	if err := s.ResolveNewPlan(); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if active.Title != "Replacement" {
		t.Fatalf("active = %q", active.Title)
	}
	snap := s.Snapshot()
	if len(snap.Plans) != 2 {
		t.Fatalf("plans kept = %d, want 2", len(snap.Plans))
	}
	if snap.Plans[0].Status != StatusSuperseded || snap.Plans[0].SupersededBy != active.ID {
		t.Fatalf("old plan = %+v", snap.Plans[0])
	}
	if snap.Plans[0].ID != old.ID || len(snap.Plans[0].Progress) == 0 {
		t.Fatal("superseding must keep identity and progress")
	}
	if snap.Plans[0].Steps[0].Status != StepCompleted {
		t.Fatal("superseding must not inherit or clear the old step progress")
	}
	if len(active.Steps[0].Evidence) != 0 || active.Steps[0].Status != StepPending {
		t.Fatal("the new plan must not inherit step progress")
	}
}

func TestKeepExistingContextResumesFirstUnfinished(t *testing.T) {
	s := NewStore()
	s.SubmitDraft(draftPlan(t))
	if err := s.Approve(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginStep(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteStep(StepResult{Status: StepCompleted, Result: "step one", Evidence: []string{"ok"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginStep(); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause(); err != nil {
		t.Fatal(err)
	}

	next := draftPlan(t)
	next.Title = "Distraction"
	s.SubmitDraft(next)
	if err := s.KeepExisting(); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if active.Title == "Distraction" {
		t.Fatal("Keep existing must keep the old plan")
	}
	if active.StepIndex != 1 {
		t.Fatalf("resume index = %d, want the first unfinished step 1", active.StepIndex)
	}
	if active.Steps[0].Status != StepCompleted {
		t.Fatal("resume must keep completed progress")
	}
	if !active.AwaitingStepChoice {
		t.Fatal("an in-flight step must ask continue/restart/skip")
	}
	if s.Snapshot().Plans[len(s.Snapshot().Plans)-1].Status != StatusCancelled {
		t.Fatal("the rejected draft is cancelled, not deleted")
	}
	if _, _, err := s.BeginStep(); err == nil {
		t.Fatal("must not begin while the in-flight step is unresolved")
	}
	for _, action := range []string{"continue", "restart", "skip"} {
		t.Run(action, func(t *testing.T) {
			s := NewStore()
			s.SubmitDraft(draftPlan(t))
			_ = s.Approve()
			_, _, _ = s.BeginStep()
			_ = s.Pause()
			s.SubmitDraft(draftPlan(t))
			_ = s.KeepExisting()
			if err := s.ResolveStepChoice(action); err != nil {
				t.Fatal(err)
			}
			active, _ := s.Active()
			if active.AwaitingStepChoice {
				t.Fatal("choice must clear the question")
			}
		})
	}
}

func TestStepLifecycleEnforcesVerification(t *testing.T) {
	s := NewStore()
	s.SubmitDraft(draftPlan(t))
	if err := s.Approve(); err != nil {
		t.Fatal(err)
	}
	step, index, err := s.BeginStep()
	if err != nil {
		t.Fatal(err)
	}
	if index != 0 || step.Status != StepInProgress || step.Attempts != 1 {
		t.Fatalf("begin = %+v index=%d", step, index)
	}
	if s.Mode() != ModeExecute {
		t.Fatalf("mode = %q", s.Mode())
	}

	if _, err := s.CompleteStep(StepResult{Status: StepCompleted, Result: "done"}); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if active.Steps[0].Status != StepBlocked {
		t.Fatalf("missing evidence must block, got %q", active.Steps[0].Status)
	}
	if !strings.Contains(active.Steps[0].BlockReason, "verification") {
		t.Fatalf("reason = %q", active.Steps[0].BlockReason)
	}
	if active.StepIndex != 0 {
		t.Fatal("a blocked step must not advance")
	}
	if s.Mode() != ModeExecute {
		t.Fatal("a blocked step stays in Execute Mode for the user's decision")
	}

	if err := s.RetryStep(); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	if active.Steps[0].Status != StepInProgress || active.Steps[0].Attempts != 2 {
		t.Fatalf("retry = %+v", active.Steps[0])
	}
	if _, err := s.CompleteStep(StepResult{Status: StepCompleted, Result: "done", Evidence: []string{"go test passed"}}); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	if active.StepIndex != 1 || active.Steps[1].Status != StepPending {
		t.Fatalf("advance = %+v", active)
	}

	if err := s.SkipStep(); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	if active.Status != StatusCompleted || active.Steps[1].Status != StepSkipped {
		t.Fatalf("skip to completion = %+v", active)
	}
	if s.Mode() != ModeChat {
		t.Fatalf("completed plan must leave Execute Mode, got %q", s.Mode())
	}
}

func TestBlockAndRestartFromTheTop(t *testing.T) {
	s := NewStore()
	s.SubmitDraft(draftPlan(t))
	_ = s.Approve()
	_, _, _ = s.BeginStep()
	if _, err := s.CompleteStep(StepResult{Status: StepBlocked, Reason: "workspace drifted"}); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if active.Steps[0].Status != StepBlocked || active.Steps[0].BlockReason != "workspace drifted" {
		t.Fatalf("blocked = %+v", active.Steps[0])
	}
	if err := s.RestartStep(); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	if active.Steps[0].Status != StepInProgress || active.Steps[0].BlockReason != "" {
		t.Fatalf("restart = %+v", active.Steps[0])
	}
	if active.Steps[0].Attempts != 2 {
		t.Fatalf("attempts = %d", active.Steps[0].Attempts)
	}
}

func TestPauseAndResume(t *testing.T) {
	s := NewStore()
	s.SubmitDraft(draftPlan(t))
	_ = s.Approve()
	_, _, _ = s.BeginStep()
	if err := s.Pause(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginStep(); err == nil {
		t.Fatal("a paused plan must not begin a step")
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if !active.Paused || !active.AwaitingStepChoice {
		t.Fatalf("resume with an in-flight step must pause and ask the user: %+v", active)
	}
	if err := s.ResolveStepChoice("continue"); err != nil {
		t.Fatal(err)
	}
	active, _ = s.Active()
	if active.Paused || active.AwaitingStepChoice {
		t.Fatalf("continue must clear the question: %+v", active)
	}
}

func TestSnapshotSurvivesRoundTrip(t *testing.T) {
	s := NewStore()
	s.SubmitDraft(draftPlan(t))
	_ = s.Approve()
	_, _, _ = s.BeginStep()
	_, _ = s.CompleteStep(StepResult{Status: StepCompleted, Result: "one", Evidence: []string{"test"}})

	snap := s.Snapshot()
	restored := NewStore()
	restored.Restore(snap)
	if restored.Mode() != ModeExecute {
		t.Fatalf("mode = %q", restored.Mode())
	}
	active, ok := restored.Active()
	if !ok || active.StepIndex != 1 || active.Steps[0].Status != StepCompleted {
		t.Fatalf("restored = %+v", active)
	}
	step, _, err := restored.BeginStep()
	if err != nil {
		t.Fatal(err)
	}
	if step.Title != "Wire /plan" {
		t.Fatalf("continued at %q", step.Title)
	}

	// Mutating a snapshot must not reach back into the store.
	snap.Plans[0].Steps[0].Result = "mutated"
	snap.Plans[0].InScope[0] = "mutated"
	current, _ := s.Active()
	if current.Steps[0].Result == "mutated" || current.InScope[0] == "mutated" {
		t.Fatal("snapshot aliases live state")
	}
}

func TestApproveRefusesUnvalidatedPlan(t *testing.T) {
	s := NewStore()
	empty := Plan{Title: "No steps"}
	s.SubmitDraft(empty)
	if err := s.Approve(); err == nil {
		t.Fatal("approve must reject a plan with no steps")
	}
}

func TestCancelStopsExecution(t *testing.T) {
	s := NewStore()
	s.SubmitDraft(draftPlan(t))
	_ = s.Approve()
	_, _, _ = s.BeginStep()
	if err := s.Cancel(); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if active.Status != StatusCancelled || s.Mode() != ModeChat {
		t.Fatalf("cancel = %+v mode=%q", active, s.Mode())
	}
	if active.Steps[0].Status != StepInProgress {
		t.Fatal("cancel must leave the interrupted step's state visible")
	}
}

// An interrupted turn is not a failed step: it pauses and asks the person,
// which is what Keep existing context does with a step left mid-flight.
func TestInterruptAsksForTheStepChoice(t *testing.T) {
	s := NewStore()
	s.SubmitDraft(draftPlan(t))
	if err := s.Approve(); err != nil {
		t.Fatal(err)
	}
	step, _, err := s.BeginStep()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Interrupt("interrupted before the step reported"); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if active.Steps[0].Status != StepInProgress || !active.Paused || !active.AwaitingStepChoice {
		t.Fatalf("after an interrupt: %+v paused=%v awaiting=%v", active.Steps[0], active.Paused, active.AwaitingStepChoice)
	}
	if active.Steps[0].BlockReason != "" {
		t.Fatalf("an interrupted step was recorded as blocked: %q", active.Steps[0].BlockReason)
	}
	if len(active.Progress) == 0 || active.Progress[len(active.Progress)-1].StepID != step.ID {
		t.Fatalf("the interrupt is not in the journal: %+v", active.Progress)
	}
	if err := s.ResolveStepChoice("continue"); err != nil {
		t.Fatal(err)
	}
	if active, _ := s.Active(); active.Paused || active.AwaitingStepChoice {
		t.Fatalf("continue did not release the choice: %+v", active)
	}
	// A plan that is no longer executing has nothing to interrupt.
	if err := s.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := s.Interrupt(""); err == nil {
		t.Fatal("interrupt must refuse a plan that is not executing")
	}
}

func TestUpdateDraftOnlyBeforeExecution(t *testing.T) {
	s := NewStore()
	p := s.SubmitDraft(draftPlan(t))
	p.Goal = "edited goal"
	if err := s.UpdateDraft(p); err != nil {
		t.Fatal(err)
	}
	active, _ := s.Active()
	if active.Goal != "edited goal" {
		t.Fatalf("goal = %q", active.Goal)
	}
	if err := s.Approve(); err != nil {
		t.Fatal(err)
	}
	p.Goal = "late edit"
	if err := s.UpdateDraft(p); err == nil {
		t.Fatal("an executing plan must not be editable")
	}
}

func TestPlanNormalizeKeepsCursorOnUnfinishedStep(t *testing.T) {
	p := draftPlan(t)
	p.Steps[0].Status = StepCompleted
	p.StepIndex = 0
	p.Normalize()
	if p.StepIndex != 1 {
		t.Fatalf("cursor = %d, want 1", p.StepIndex)
	}
	p.Steps[1].Status = StepSkipped
	p.Normalize()
	if p.StepIndex != 1 {
		t.Fatalf("finished plan cursor = %d", p.StepIndex)
	}
}

func TestPlanModeRulesDocumentThePlanBlock(t *testing.T) {
	if !strings.Contains(PlanModeRules, "PLAN MODE") {
		t.Fatal("the contract must name the mode")
	}
	if !strings.Contains(PlanModeRules, "```plan") {
		t.Fatal("the contract must document the plan block")
	}
}

func TestStepPromptNamesOnlyTheCurrentStep(t *testing.T) {
	p := draftPlan(t)
	got := StepPrompt(p, p.Steps[1], 1)
	if !strings.Contains(got, "Wire /plan") {
		t.Fatal("step objective missing")
	}
	if !strings.Contains(got, `step="2/2"`) || !strings.Contains(got, "plan-step") {
		t.Fatalf("prompt = %q", got)
	}
	if !strings.Contains(got, "Execute ONLY the current step") {
		t.Fatal("one-step rule missing")
	}
}

func TestOpenQuestionsBlock(t *testing.T) {
	if got := OpenQuestionsBlock(nil); got != "" {
		t.Fatalf("empty block = %q", got)
	}
	if got := OpenQuestionsBlock([]string{"a?", "b?"}); !strings.Contains(got, "- a?") {
		t.Fatalf("block = %q", got)
	}
}

func TestSummaryCountsHandledSteps(t *testing.T) {
	p := draftPlan(t)
	p.Steps[0].Status = StepCompleted
	p.Steps[1].Status = StepSkipped
	got := Summary(p)
	if !strings.Contains(got, "1/2 completed") || !strings.Contains(got, "2 handled") {
		t.Fatalf("summary = %q", got)
	}
}

func TestRestoreDropsDanglingIDs(t *testing.T) {
	s := NewStore()
	s.Restore(State{Mode: ModeExecute, ActiveID: "missing", DraftID: "gone"})
	if s.Mode() != ModeExecute {
		t.Fatalf("mode = %q", s.Mode())
	}
	if s.HasConflict() {
		t.Fatal("dangling IDs must not conflict")
	}
	if _, ok := s.Active(); ok {
		t.Fatal("dangling active ID resolved")
	}
}

func TestStampsUpdateOnWrite(t *testing.T) {
	s := NewStore()
	p := s.SubmitDraft(draftPlan(t))
	p.UpdatedAt = time.Time{}
	snap := s.Snapshot()
	if snap.Plans[0].UpdatedAt.IsZero() {
		t.Fatal("submit must stamp UpdatedAt")
	}
}

// Plan Mode is read-only but not idle: the rules name the one definition a
// delegated investigation may use — explore, covering both this repository and
// upstream docs — and say plainly that an edit agent is refused, so the model
// does not burn turns retrying one.
func TestPlanModeRulesNameTheReadOnlyDelegates(t *testing.T) {
	for _, want := range []string{
		"Agent tool with a read-only definition",
		"explore covers this codebase",
		"upstream docs and APIs",
		"Any definition that can",
		"write (task, quick",
		"rather than retrying with another",
	} {
		if !strings.Contains(PlanModeRules, want) {
			t.Errorf("PlanModeRules should contain %q", want)
		}
	}
}

// An approved plan's steps are drawn as the user's todo list while the plan
// executes, so the rules say what a step has to be: one verifiable outcome,
// small enough to finish in a turn, and a question whenever an instruction is
// still unclear.
func TestPlanModeRulesDescribeVerifiableSteps(t *testing.T) {
	for _, want := range []string{
		"Every step is one verifiable unit",
		"small enough to finish in one turn",
		"user's todo list",
		"write them as tasks a person can check off",
		"ask before writing the plan",
	} {
		if !strings.Contains(PlanModeRules, want) {
			t.Errorf("PlanModeRules should contain %q", want)
		}
	}
}
