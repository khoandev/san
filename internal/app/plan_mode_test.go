package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/san/internal/transcript"
)

// TestParentPostureIsReadOnlyInPlanMode pins what a spawned worker inherits:
// while planning, a worker starts from explore even though the session mode
// still reads normal, so an unnamed Agent call cannot edit its way around the
// plan gate.
func TestParentPostureIsReadOnlyInPlanMode(t *testing.T) {
	perms := setting.NewSessionPermissions()
	if got := parentPosture(perms); got != subagent.PermissionDefault {
		t.Fatalf("normal posture = %q, want default", got)
	}
	perms.SetMode(setting.ModeBypassPermissions)
	if got := parentPosture(perms); got != subagent.PermissionBypass {
		t.Fatalf("bypass posture = %q, want bypass", got)
	}
	perms.SetPlanMode(true)
	if got := parentPosture(perms); got != subagent.PermissionExplore {
		t.Fatalf("plan-mode posture = %q, want explore", got)
	}
	perms.SetPlanMode(false)
	if got := parentPosture(perms); got != subagent.PermissionBypass {
		t.Fatalf("posture after plan mode = %q, want the session mode back", got)
	}
	if got := parentPosture(nil); got != subagent.PermissionDefault {
		t.Fatalf("nil permissions = %q, want default", got)
	}
}

// newPlanModel is a model with the pieces Plan Mode touches: the plan store,
// the permission posture, and the conversation. Approving a plan can also
// replace that conversation (the panel's "fresh" and "compact" contexts), so
// the harness carries the runtime a session swap needs as well.
func newPlanModel(t *testing.T) *model {
	t.Helper()
	m := fixedComposerModel(100, 40)
	m.services.Agent = &agent.Runtime{}
	m.env.SessionPermissions = setting.NewSessionPermissions()
	m.env.ApplyModePermissions(t.TempDir())
	initClearSessionTest(t, m)
	m.services.Reminder = reminder.New()
	return m
}

// testPlan is a normalized plan with pending steps.
func testPlan(id, title string, steps ...string) plan.Plan {
	p := plan.Plan{ID: id, Title: title, Goal: "goal of " + title}
	for _, step := range steps {
		p.Steps = append(p.Steps, plan.Step{Title: step})
	}
	p.Normalize()
	return p
}

func stepStatuses(p plan.Plan) []plan.StepStatus {
	out := make([]plan.StepStatus, len(p.Steps))
	for i := range p.Steps {
		out[i] = p.Steps[i].Status
	}
	return out
}

// Bare /plan switches the session to read-only Plan Mode, and /plan <prompt>
// starts planning that request in the same step.
func TestPlanCommandEntersPlanMode(t *testing.T) {
	m := newPlanModel(t)

	if _, handled := m.runSlashCommandIfMatched("/plan"); !handled {
		t.Fatal("/plan was not handled as a command")
	}
	if got := m.plan.Mode(); got != plan.ModePlan {
		t.Fatalf("mode = %q, want plan", got)
	}
	if !m.env.SessionPermissions.PlanModeActive() {
		t.Fatal("/plan did not turn the read-only permission posture on")
	}

	// A second bare /plan leaves Plan Mode again: it is the only Plan Mode
	// command, so it has to be able to switch back.
	if _, handled := m.runSlashCommandIfMatched("/plan"); !handled {
		t.Fatal("second /plan was not handled")
	}
	if got := m.plan.Mode(); got != plan.ModeChat {
		t.Fatalf("mode after toggling off = %q, want chat", got)
	}
	if m.env.SessionPermissions.PlanModeActive() {
		t.Fatal("leaving Plan Mode must clear the read-only posture")
	}
}

func TestPlanCommandWithPromptStartsPlanning(t *testing.T) {
	m := newPlanModel(t)

	if _, handled := m.runSlashCommandIfMatched("/plan split the auth package"); !handled {
		t.Fatal("/plan <prompt> was not handled")
	}
	if got := m.plan.Mode(); got != plan.ModePlan {
		t.Fatalf("mode = %q, want plan", got)
	}
	// The notice about the missing provider follows it, so find the user row
	// rather than assuming it is last.
	var prompt *core.ChatMessage
	for i := range m.conv.Messages {
		if m.conv.Messages[i].Role == core.ChatUser {
			prompt = &m.conv.Messages[i]
		}
	}
	if prompt == nil || prompt.Content != "split the auth package" {
		t.Fatalf("conversation has no raw prompt row: %+v", m.conv.Messages)
	}
	if m.pendingMessages.Len() == 0 {
		t.Fatal("no message was submitted to the agent")
	}
	sent := m.pendingMessages.First().Text()
	if !strings.Contains(sent, "<plan-mode>") || !strings.Contains(sent, "split the auth package") {
		t.Fatalf("submitted request is not wrapped in the plan protocol: %q", sent)
	}
}

// Ordinary turns in Plan Mode carry the read-only rules; turns outside it do
// not. The wrap is invisible in the conversation.
func TestPlanModeWrapsUserTurns(t *testing.T) {
	m := newPlanModel(t)
	message := core.UserMessage("implement it", nil)

	if got := m.applyPlanModeRules(message).Text(); got != "implement it" {
		t.Fatalf("chat-mode turn was rewritten: %q", got)
	}
	m.enterPlanMode()
	got := m.applyPlanModeRules(message).Text()
	if !strings.HasPrefix(got, "<plan-mode>") || !strings.Contains(got, plan.PlanModeRules) {
		t.Fatalf("plan-mode turn missing the protocol: %q", got)
	}
	if message.Text() != "implement it" {
		t.Fatal("the wrap mutated the caller's message")
	}
}

// The plan is session state: it round-trips through the transcript and comes
// back complete — mode, plans, step statuses, and the cursor.
func TestPlanStateSurvivesSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := store.Create(session.CreateOptions{ID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}

	m := newPlanModel(t)
	m.services.SessionStore = store
	m.services.Session = opened
	m.services.sessionID = "sess-1"
	m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "plan it"}}

	produced := testPlan("plan-1", "Split auth", "read code", "move files", "run tests")
	m.plan.SubmitDraft(produced)
	active, _ := m.plan.Active()
	if err := m.plan.Approve(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.plan.CompleteStep(plan.StepResult{Status: plan.StepCompleted, Result: "read"}); err != nil {
		t.Fatal(err)
	}
	m.persistPlan()

	history, err := store.Read("sess-1", session.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	restored := plan.NewStore()
	restored.Restore(history.Plan)
	if got := restored.Mode(); got != plan.ModeExecute {
		t.Fatalf("restored mode = %q, want execute", got)
	}
	got, ok := restored.Active()
	if !ok {
		t.Fatal("restored state has no active plan")
	}
	if got.Title != active.Title || got.ID != active.ID {
		t.Fatalf("restored plan = %+v, want %q", got, active.Title)
	}
	if statuses := stepStatuses(got); statuses[0] != plan.StepCompleted || statuses[1] != plan.StepPending {
		t.Fatalf("restored step statuses = %v", statuses)
	}
	if got.StepIndex != 1 {
		t.Fatalf("restored cursor = %d, want the first unfinished step (1)", got.StepIndex)
	}

	// A plan never becomes a file of its own.
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), "plan") {
			t.Errorf("plan state leaked into a file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Plan Mode is enforced by the tool gate the app installs, not only by the
// prompt: a write during planning is rejected outright.
func TestPlanModeBlocksWorkspaceWrites(t *testing.T) {
	m := newPlanModel(t)
	m.enterPlanMode()

	settings := &setting.Data{}
	got := settings.HasPermissionToUseTool("Write", map[string]any{"file_path": "a.go", "content": "x"}, m.env.SessionPermissions)
	if got.Behavior != perm.Reject || got.Reason != "plan mode: read-only" {
		t.Fatalf("plan mode allowed a write: %+v", got)
	}
	read := settings.HasPermissionToUseTool("Read", map[string]any{"file_path": "a.go"}, m.env.SessionPermissions)
	if read.Behavior == perm.Reject {
		t.Fatalf("plan mode rejected a read: %+v", read)
	}
}

// The gate runs in the setting package, which cannot read the agent registry,
// so the app installs the verdict with the posture: while planning, delegating
// an investigation is allowed and an edit agent is still refused.
func TestPlanModeAllowsOnlyReadOnlyAgentDelegation(t *testing.T) {
	m := newPlanModel(t)
	registry := subagent.NewRegistry()
	registry.Register(&subagent.AgentConfig{Name: "explore", Description: "Investigates", PermissionMode: subagent.PermissionExplore})
	registry.Register(&subagent.AgentConfig{Name: "task", Description: "Implements", PermissionMode: subagent.PermissionAcceptEdits})
	m.services.Subagent = registry

	m.enterPlanMode()

	settings := &setting.Data{}
	delegate := func(args map[string]any) setting.PermissionDecision {
		return settings.HasPermissionToUseTool("Agent", args, m.env.SessionPermissions)
	}
	for _, args := range []map[string]any{
		{"name": "explore", "prompt": "map the plan gate"},
		{"prompt": "map the plan gate"}, // the posture default runs explore
	} {
		if got := delegate(args); got.Behavior == perm.Reject {
			t.Errorf("Agent(%v) was refused while planning: %+v", args["name"], got)
		}
	}
	if got := delegate(map[string]any{"name": "task", "prompt": "implement it"}); got.Behavior != perm.Reject || got.Reason != "plan mode: read-only" {
		t.Errorf("Agent(task) = %+v, want the plan-mode rejection", got)
	}

	// Execute Mode is not read-only, so the verdict must not outlive the
	// posture that installed it.
	m.leavePlanMode()
	if got := delegate(map[string]any{"name": "task"}); got.Reason == "plan mode: read-only" {
		t.Fatalf("execute mode kept the plan-mode verdict: %+v", got)
	}

	// No registry means no delegation: the gate's own default, not a hole.
	m.services.Subagent = nil
	m.enterPlanMode()
	if got := delegate(map[string]any{"name": "explore", "prompt": "map it"}); got.Behavior != perm.Reject || got.Reason != "plan mode: read-only" {
		t.Fatalf("plan mode delegated without a registry: %+v", got)
	}
}

// A ```plan block opening in the turn's final text is what makes the panel
// appear; nothing else has to be typed.
func TestPlanBlockOpensExecutionPanel(t *testing.T) {
	m := newPlanModel(t)
	m.enterPlanMode()

	block := "```plan\n" + `{
  "title": "Split auth",
  "goal": "one package owns credentials",
  "steps": [
    {"title": "read the code", "objective": "map callers", "verification": ["rg works"]},
    {"title": "move files"}
  ]
}` + "\n```"
	m.onPlanTurn(core.Result{Content: block, StopReason: core.StopEndTurn})

	if !m.userInput.Plan.IsActive() {
		t.Fatal("the Execution Panel did not open after the plan arrived")
	}
	active, ok := m.plan.Active()
	if !ok || active.Status != plan.StatusPendingApproval {
		t.Fatalf("active plan = %+v, want a plan pending approval", active)
	}
	if actions := m.userInput.Plan.Actions(); len(actions) == 0 || actions[0] != "Execute new plan" {
		t.Fatalf("panel actions = %v, want Execute new plan first", actions)
	}
}

// Execute new plan replaces the old plan without deleting it and without
// inheriting its progress, and the conversation stays.
func TestExecuteNewPlanSupersedesOldPlan(t *testing.T) {
	m := newPlanModel(t)
	m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "keep this context"}}

	old := testPlan("plan-old", "Old plan", "a", "b")
	old.Status = plan.StatusExecuting
	old.Steps[0].Status = plan.StepCompleted
	old.Steps[0].Result = "done"
	old.StepIndex = 1
	m.plan.Restore(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{old}, ActiveID: old.ID})

	fresh := testPlan("plan-new", "New plan", "x", "y")
	m.plan.SubmitDraft(fresh)
	if !m.plan.HasConflict() {
		t.Fatal("expected a conflict between the running plan and the new draft")
	}
	m.userInput.Plan.Enter(m.plan.Snapshot(), 100, 40)
	if actions := m.userInput.Plan.Actions(); len(actions) < 2 || actions[1] != "Keep existing context" {
		t.Fatalf("panel actions = %v, want both plan choices", actions)
	}

	m.handlePlanAction(input.PlanActionMsg{Action: input.PlanActionExecuteNew})

	state := m.plan.Snapshot()
	if len(state.Plans) != 2 {
		t.Fatalf("plans in session = %d, want 2 (the old one kept)", len(state.Plans))
	}
	var gotOld plan.Plan
	for _, p := range state.Plans {
		if p.ID == "plan-old" {
			gotOld = p
		}
	}
	if gotOld.Status != plan.StatusSuperseded || gotOld.SupersededBy != "plan-new" {
		t.Fatalf("old plan = %+v, want superseded by plan-new", gotOld)
	}
	// The old progress is history, not a starting point.
	if gotOld.Steps[0].Status != plan.StepCompleted {
		t.Fatal("the superseded plan's progress must be kept, not erased")
	}
	active, ok := m.plan.Active()
	if !ok || active.ID != "plan-new" {
		t.Fatalf("active plan = %+v, want plan-new", active)
	}
	if active.Steps[0].Status == plan.StepCompleted || active.Steps[0].Result != "" {
		t.Fatal("the new plan inherited progress from the old one")
	}
	if got := m.plan.Mode(); got != plan.ModeExecute {
		t.Fatalf("mode = %q, want execute", got)
	}
	if m.env.SessionPermissions.PlanModeActive() {
		t.Fatal("Execute Mode must not stay read-only")
	}
	if len(m.conv.Messages) == 0 || m.conv.Messages[0].Content != "keep this context" {
		t.Fatal("Execute new plan dropped the conversation")
	}
}

// Keep existing context keeps the plan, its decisions and its progress, and
// resumes from the first unfinished step.
func TestKeepExistingContextResumesAtFirstUnfinishedStep(t *testing.T) {
	m := newPlanModel(t)

	kept := testPlan("plan-kept", "Kept plan", "done", "next", "later")
	kept.Status = plan.StatusExecuting
	kept.Steps[0].Status = plan.StepCompleted
	kept.Steps[0].Result = "shipped"
	kept.Decisions = []plan.Decision{{Text: "use the existing helper"}}
	kept.StepIndex = 1
	m.plan.Restore(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{kept}, ActiveID: kept.ID})

	m.plan.SubmitDraft(testPlan("plan-dropped", "Another plan", "one"))
	m.userInput.Plan.Enter(m.plan.Snapshot(), 100, 40)

	m.handlePlanAction(input.PlanActionMsg{Action: input.PlanActionKeepExisting})

	active, ok := m.plan.Active()
	if !ok || active.ID != "plan-kept" {
		t.Fatalf("active plan = %+v, want the kept plan", active)
	}
	if len(active.Decisions) != 1 || active.Decisions[0].Text != "use the existing helper" {
		t.Fatalf("decisions = %+v, want them preserved", active.Decisions)
	}
	if active.Steps[0].Status != plan.StepCompleted || active.Steps[0].Result != "shipped" {
		t.Fatal("progress was not preserved")
	}
	var dropped plan.Plan
	for _, p := range m.plan.Snapshot().Plans {
		if p.ID == "plan-dropped" {
			dropped = p
		}
	}
	if dropped.Status != plan.StatusCancelled {
		t.Fatalf("discarded draft = %+v, want cancelled", dropped)
	}
	if active.StepIndex != 1 {
		t.Fatalf("resume cursor = %d, want step 2 (index 1)", active.StepIndex)
	}
	after, _ := m.plan.Active()
	if after.Steps[1].Status != plan.StepInProgress {
		t.Fatalf("step 2 = %q, want in_progress (the executor resumes there)", after.Steps[1].Status)
	}
}

// A step that reports no verification evidence is blocked, the executor stops,
// and the panel offers the recovery actions rather than advancing.
func TestBlockedStepStopsThePlanAndOffersChoices(t *testing.T) {
	m := newPlanModel(t)

	running := testPlan("plan-run", "Risky plan", "edit", "next")
	running.Status = plan.StatusExecuting
	running.Steps[0].Verification = []string{"go test ./..."}
	running.StepIndex = 0
	m.plan.Restore(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{running}, ActiveID: running.ID})
	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}

	m.onPlanTurn(core.Result{Content: "```plan-step\n" + `{"status":"completed","result":"changed things"}` + "\n```"})

	active, _ := m.plan.Active()
	if active.Steps[0].Status != plan.StepBlocked {
		t.Fatalf("step = %q, want blocked (no evidence for the verification criteria)", active.Steps[0].Status)
	}
	if !active.Paused {
		t.Fatal("a blocked step must pause the executor")
	}
	if active.Steps[1].Status != plan.StepPending {
		t.Fatal("the executor advanced past a blocked step")
	}
	if !m.userInput.Plan.IsActive() {
		t.Fatal("the Execution Panel did not open on a blocked step")
	}
	if actions := m.userInput.Plan.Actions(); !containsAll(actions, "Retry step", "Restart step", "Skip step", "Cancel") {
		t.Fatalf("panel actions = %v, want the blocked-step options", actions)
	}
}

// Esc during a step pauses the plan and asks what to do with the step; an
// interrupted turn is not evidence that the step failed.
func TestCancelledStepTurnPausesAndAsks(t *testing.T) {
	m := newPlanModel(t)

	running := testPlan("plan-run", "Long plan", "edit", "next")
	running.Status = plan.StatusExecuting
	running.Steps[0].Verification = []string{"go test ./..."}
	running.StepIndex = 0
	m.plan.Restore(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{running}, ActiveID: running.ID})
	if _, _, err := m.plan.BeginStep(); err != nil {
		t.Fatal(err)
	}

	m.onPlanTurn(core.Result{Content: "partial work", StopReason: core.StopCanceled})

	active, _ := m.plan.Active()
	if active.Steps[0].Status != plan.StepInProgress {
		t.Fatalf("step = %q, want it still in progress", active.Steps[0].Status)
	}
	if active.Steps[0].BlockReason != "" {
		t.Fatalf("an interrupted step was recorded as blocked: %q", active.Steps[0].BlockReason)
	}
	if !active.Paused || !active.AwaitingStepChoice {
		t.Fatalf("paused=%v awaiting=%v, want the step question", active.Paused, active.AwaitingStepChoice)
	}
	if active.Steps[1].Status != plan.StepPending {
		t.Fatal("the executor advanced past an interrupted step")
	}
	if !m.userInput.Plan.IsActive() {
		t.Fatal("the Execution Panel did not open on the interrupt")
	}
	if actions := m.userInput.Plan.Actions(); !containsAll(actions, "Continue step", "Restart step", "Skip step", "Cancel") {
		t.Fatalf("panel actions = %v, want the continue/restart/skip choice", actions)
	}
	noticed := false
	for _, message := range m.conv.Messages {
		if strings.Contains(message.Content, "Step interrupted") {
			noticed = true
		}
	}
	if !noticed {
		t.Fatal("the interruption was not explained in the transcript")
	}
}

func containsAll(have []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// The permission posture a resumed plan needs is restored with it.
func TestResumedPlanRestoresReadOnlyPosture(t *testing.T) {
	m := newPlanModel(t)
	state := plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{testPlan("plan-1", "Planned", "a")}, ActiveID: "plan-1"}
	m.restorePlanState(state)

	if !m.env.SessionPermissions.PlanModeActive() {
		t.Fatal("a resumed planning session must come back read-only")
	}
	if !m.userInput.Plan.IsActive() {
		t.Fatal("the Execution Panel was not restored with the plan")
	}

	m.resetPlanState()
	if m.env.SessionPermissions.PlanModeActive() {
		t.Fatal("/clear left the session read-only")
	}
}

// The plan state rides on the same record as the rest of the session state.
func TestSessionRecordCarriesPlan(t *testing.T) {
	m := newPlanModel(t)
	m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "plan it"}}
	m.plan.SubmitDraft(testPlan("plan-1", "A plan", "step"))

	record := m.buildSessionRecord()
	if record == nil || record.State == nil || record.State.Plan == nil {
		t.Fatal("the session state record does not carry the plan")
	}
	if record.State.Plan.ActiveID != "plan-1" {
		t.Fatalf("plan in record = %+v", record.State.Plan)
	}
	if record.Type != transcript.SessionStatePatched {
		t.Fatalf("record type = %q", record.Type)
	}
}

// The notice that follows a plan names the open questions, so they are answered
// while planning instead of being discovered mid-execution.
func TestPlanDraftNoticeListsOpenQuestions(t *testing.T) {
	planned := testPlan("plan-1", "Split auth", "read")
	planned.OpenQuestions = []string{"keep the legacy cookie?"}
	notice := planDraftNotice(planned)
	for _, want := range []string{"Split auth", "keep the legacy cookie?", "Execution Panel"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice is missing %q:\n%s", want, notice)
		}
	}
	if got := planDraftNotice(testPlan("plan-2", "Settled", "a")); strings.Contains(got, "Open questions") {
		t.Fatalf("notice invented open questions:\n%s", got)
	}
}

// Execute new plan can start a fresh session: the plan crosses over, the
// planning conversation does not.
func TestExecuteNewPlanFreshContextStartsANewSession(t *testing.T) {
	m := newPlanModel(t)
	m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "planning chatter"}}

	draft := testPlan("plan-fresh", "Fresh start", "a", "b")
	draft.Status = plan.StatusPendingApproval
	m.plan.Restore(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{draft}, ActiveID: draft.ID})
	m.userInput.Plan.Enter(m.plan.Snapshot(), 100, 40)

	before := m.sessionID()
	m.handlePlanAction(input.PlanActionMsg{
		Action:  input.PlanActionExecuteNew,
		Context: input.PlanContextFresh,
	})

	if got := m.sessionID(); got == before || got == "" {
		t.Fatalf("session id = %q, want a fresh session", got)
	}
	for _, msg := range m.conv.Messages {
		if strings.Contains(msg.Content, "planning chatter") {
			t.Fatal("the fresh session carried the planning conversation")
		}
	}
	active, ok := m.plan.Active()
	if !ok || active.ID != "plan-fresh" || active.Status != plan.StatusExecuting {
		t.Fatalf("active plan = %+v, want plan-fresh executing", active)
	}
	if len(m.conv.Messages) == 0 || !strings.Contains(m.conv.Messages[0].Content, "Fresh session") {
		t.Fatalf("the fresh session has no handoff notice: %+v", m.conv.Messages)
	}
}

// Without a provider to summarize with, the compact context falls back to
// running in the conversation rather than leaving the plan unstarted.
func TestExecuteNewPlanCompactFallsBackWithoutAProvider(t *testing.T) {
	m := newPlanModel(t)
	m.conv.Messages = []core.ChatMessage{{Role: core.ChatUser, Content: "planning chatter"}}

	draft := testPlan("plan-compact", "Compact start", "a")
	draft.Status = plan.StatusPendingApproval
	m.plan.Restore(plan.State{Mode: plan.ModePlan, Plans: []plan.Plan{draft}, ActiveID: draft.ID})
	m.userInput.Plan.Enter(m.plan.Snapshot(), 100, 40)

	m.handlePlanAction(input.PlanActionMsg{
		Action:  input.PlanActionExecuteNew,
		Context: input.PlanContextCompact,
	})

	if m.pendingPlanExecID != "" {
		t.Fatalf("pending plan = %q, want none without a compaction", m.pendingPlanExecID)
	}
	active, ok := m.plan.Active()
	if !ok || active.Steps[0].Status != plan.StepInProgress {
		t.Fatalf("active plan = %+v, want the first step running in place", active)
	}
}

// The step a compaction was started for begins from OnCompacted; a marker left
// over from another plan starts nothing.
func TestPlanAfterCompactStartsOnlyItsOwnPlan(t *testing.T) {
	m := newPlanModel(t)
	active := testPlan("plan-c", "Compacted", "one", "two")
	active.Status = plan.StatusExecuting
	m.plan.Restore(plan.State{Mode: plan.ModeExecute, Plans: []plan.Plan{active}, ActiveID: active.ID})

	// The command the step returns is nil here: the harness has no provider to
	// submit to, so what the test owns is the store transition.
	m.pendingPlanExecID = active.ID
	m.planAfterCompact()
	if m.pendingPlanExecID != "" {
		t.Fatal("the pending marker survived the start")
	}
	started, _ := m.plan.Active()
	if started.Steps[0].Status != plan.StepInProgress {
		t.Fatalf("step status = %q, want in progress", started.Steps[0].Status)
	}

	m.pendingPlanExecID = "plan-gone"
	if cmd := m.planAfterCompact(); cmd != nil {
		t.Fatal("a marker from another plan started a step")
	}
	if m.pendingPlanExecID != "" {
		t.Fatal("a stale marker must be cleared")
	}
}
