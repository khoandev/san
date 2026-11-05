// Plan Mode orchestration: the /plan command, the turn-end protocol that turns
// ```plan / ```plan-step blocks into session state, the step-by-step executor,
// and the Execution Panel's action handlers.
//
// All of this runs on the UI goroutine. The plan store itself lives in
// m.plan, and every change is written into the session transcript through the
// ordinary appendSessionState path (StatePatch.Plan) — there is no plan file on
// disk, and a resume restores the plan from the same record as the conversation.
package app

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// planCommand backs /plan, the single Plan Mode entry point.
//
// Bare /plan toggles: it enters read-only Plan Mode, and leaves it when the
// session is already planning or executing. /plan <prompt> always enters Plan
// Mode and starts planning that request immediately.
func (m *model) planCommand(prompt string) (string, tea.Cmd) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		if m.plan.Mode() == plan.ModeChat {
			m.enterPlanMode()
			return "Plan Mode on — read-only. Describe what to plan, then review it in the Execution Panel. /plan again leaves Plan Mode.", nil
		}
		m.leavePlanMode()
		return "Plan Mode off. The plan stays in this session.", nil
	}

	m.enterPlanMode()
	chat := m.conv.Append(core.ChatMessage{Role: core.ChatUser, Content: prompt, DisplayContent: prompt})
	message, _ := chat.ToMessage()
	return "", tea.Batch(append(m.CommitMessages(), m.SubmitToAgent(planRequest(message)))...)
}

// enterPlanMode switches the session to Plan Mode: the store records the mode,
// the permission gate turns read-only, and the state is persisted so a resume
// comes back read-only too.
func (m *model) enterPlanMode() {
	m.plan.EnterPlanMode()
	m.syncPlanPermissions()
	m.persistPlan()
}

// leavePlanMode returns to ordinary chat. An executing plan is paused rather
// than left claiming to run.
func (m *model) leavePlanMode() {
	if active, ok := m.plan.Active(); ok && active.Status == plan.StatusExecuting && !active.Paused {
		_ = m.plan.Pause()
	}
	m.plan.ExitPlanMode()
	m.syncPlanPermissions()
	m.persistPlan()
}

// syncPlanPermissions keeps the tool gate in step with the plan mode. Plan Mode
// is read-only; Execute Mode is not.
func (m *model) syncPlanPermissions() {
	if m.env.SessionPermissions == nil {
		return
	}
	m.env.SessionPermissions.SetPlanMode(m.plan.Mode() == plan.ModePlan)
}

// persistPlan writes the plan state into the session transcript. Failures are
// logged rather than surfaced per action: the next state write retries.
func (m *model) persistPlan() {
	if err := m.appendSessionState(); err != nil {
		log.Logger().Warn("plan state append failed", zap.Error(err))
	}
}

// applyPlanModeRules wraps ordinary user turns while Plan Mode is on, so every
// request carries the read-only rules and the ```plan protocol.
func (m *model) applyPlanModeRules(message core.Message) core.Message {
	if m.plan.Mode() != plan.ModePlan {
		return message
	}
	return planRequest(message)
}

// planRequest wraps a user turn in the Plan Mode protocol. The rules ride on
// the message, not the system prompt, so they survive a persona override and a
// compaction, and the host's tool gate backstops them either way.
func planRequest(message core.Message) core.Message {
	content := slices.Clone(message.Content)
	for i := range content {
		if content[i].Type != ai.BlockText {
			continue
		}
		content[i].Text = plan.PlanRequest(content[i].Text)
	}
	message.Content = content
	return message
}

// onPlanTurn interprets the turn that just ended. It returns a command that
// continues the plan, if any; it never submits from inside OnTurnEnd, so the
// turn's finalization is acknowledged before the next step starts.
func (m *model) onPlanTurn(result core.Result) tea.Cmd {
	switch m.plan.Mode() {
	case plan.ModePlan:
		return m.onPlanModeTurn(result)
	case plan.ModeExecute:
		return m.onExecuteModeTurn(result)
	}
	return nil
}

// onPlanModeTurn folds a finished planning turn into the store: a complete
// ```plan block becomes the draft the Execution Panel shows, and a
// ```plan-update block refines the draft already under construction.
func (m *model) onPlanModeTurn(result core.Result) tea.Cmd {
	if produced, found, err := plan.ParsePlanBlock(result.Content); err != nil {
		m.conv.AddNotice("Plan rejected: " + err.Error())
		return tea.Batch(m.CommitMessages()...)
	} else if found {
		m.plan.SubmitDraft(produced)
		m.syncPlanPermissions()
		// EndTurn's RecordState (which runs after this) writes the new state;
		// persisting here would race the turn's own finalization.
		m.showPlanPanel()
		m.conv.AddNotice(plan.Summary(produced) + "\n\nReview and run it in the Execution Panel.")
		return tea.Batch(m.CommitMessages()...)
	}

	update, found, err := plan.ParseUpdateBlock(result.Content)
	if err != nil || !found {
		return nil
	}
	active, ok := m.plan.Active()
	if !ok || active.Status == plan.StatusExecuting || active.Terminal() {
		return nil
	}
	plan.Merge(&active, update)
	if err := m.plan.UpdateDraft(active); err != nil {
		return nil
	}
	m.refreshPlanPanel()
	return nil
}

// onExecuteModeTurn records how the running step ended. Advancing is decided by
// the store: a step that claims completion without evidence is recorded as
// blocked, and a blocked step stops the executor.
func (m *model) onExecuteModeTurn(result core.Result) tea.Cmd {
	active, ok := m.plan.Active()
	if !ok {
		return nil
	}
	step, has := active.CurrentStep()
	if !has || step.Status != plan.StepInProgress {
		return nil // the person typed something; no step was running
	}

	outcome, found, err := plan.ParseStepBlock(result.Content)
	switch {
	case err != nil:
		outcome = plan.StepResult{Status: plan.StepBlocked, Reason: "the step report was invalid: " + err.Error()}
	case !found:
		outcome = plan.StepResult{Status: plan.StepBlocked, Reason: "the step ended without a plan-step report"}
	}

	advanced, completeErr := m.plan.CompleteStep(outcome)
	m.syncPlanPermissions()
	if completeErr != nil {
		return nil
	}
	if !advanced {
		m.showPlanPanel()
		m.conv.AddNotice(stepOutcomeNotice(outcome))
		return tea.Batch(m.CommitMessages()...)
	}
	return func() tea.Msg { return planAdvanceMsg{} }
}

// planAdvanceMsg starts the next approved step. It is a message rather than a
// direct call so it runs after the turn's finalization, exactly like any other
// queued input.
type planAdvanceMsg struct{}

// planNextStep marks the next unfinished step in progress and sends it to the
// agent. Only one step is ever in flight: the next one starts after the
// previous one reported success.
func (m *model) planNextStep() tea.Cmd {
	step, index, err := m.plan.BeginStep()
	if err != nil {
		m.syncPlanPermissions()
		m.showPlanPanel()
		return tea.Batch(m.CommitMessages()...)
	}
	active, _ := m.plan.Active()
	m.syncPlanPermissions()
	m.persistPlan()

	chat := m.conv.Append(core.ChatMessage{
		Role:           core.ChatUser,
		Content:        plan.StepPrompt(active, step, index),
		DisplayContent: plan.StepDisplay(active, step, index),
	})
	message, _ := chat.ToMessage()
	return tea.Batch(append(m.CommitMessages(), m.SubmitToAgent(message))...)
}

// handlePlanAction applies one Execution Panel button.
func (m *model) handlePlanAction(msg input.PlanActionMsg) tea.Cmd {
	switch msg.Action {
	case input.PlanActionClose:
		m.userInput.Plan.Close()
		return nil

	case input.PlanActionExecuteNew:
		if m.plan.HasConflict() {
			if err := m.plan.ResolveNewPlan(); err != nil {
				return m.planActionFailed(err)
			}
			// The superseded plan stays in the session on purpose: nothing the
			// session produced is deleted, it is only marked replaced.
			m.conv.AddNotice("Previous plan marked superseded — kept in this session.")
		}
		if err := m.plan.Approve(); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		m.userInput.Plan.Close()
		return tea.Batch(append(m.CommitMessages(), m.planNextStep())...)

	case input.PlanActionKeepExisting:
		if err := m.plan.KeepExisting(); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		if active, ok := m.plan.Active(); ok && active.AwaitingStepChoice {
			// A step was in flight when the plan stopped; the person decides
			// whether it continues, restarts, or is skipped.
			m.refreshPlanPanel()
			return tea.Batch(m.CommitMessages()...)
		}
		m.userInput.Plan.Close()
		return tea.Batch(append(m.CommitMessages(), m.planNextStep())...)

	case input.PlanActionCancel:
		if err := m.cancelPlan(); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		m.refreshPlanPanel()
		return tea.Batch(m.CommitMessages()...)

	case input.PlanActionPause:
		if err := m.plan.Pause(); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		m.refreshPlanPanel()
		return nil

	case input.PlanActionResume:
		if err := m.plan.Resume(); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		if active, ok := m.plan.Active(); ok && active.AwaitingStepChoice {
			m.refreshPlanPanel()
			return nil
		}
		m.userInput.Plan.Close()
		return m.planNextStep()

	case input.PlanActionContinue:
		if err := m.plan.ResolveStepChoice("continue"); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		m.userInput.Plan.Close()
		return m.planNextStep()

	case input.PlanActionRestartStep:
		if err := m.plan.RestartStep(); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		m.userInput.Plan.Close()
		return m.planNextStep()

	case input.PlanActionRetryStep:
		if err := m.plan.RetryStep(); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		m.userInput.Plan.Close()
		return m.planNextStep()

	case input.PlanActionSkipStep:
		if err := m.plan.SkipStep(); err != nil {
			return m.planActionFailed(err)
		}
		m.afterPlanChange()
		if active, ok := m.plan.Active(); !ok || active.Terminal() {
			m.refreshPlanPanel()
			return tea.Batch(m.CommitMessages()...)
		}
		m.userInput.Plan.Close()
		return m.planNextStep()
	}
	return nil
}

// cancelPlan cancels the pending draft when one is waiting, and the running
// plan otherwise, so Cancel never destroys a plan the person still wants.
func (m *model) cancelPlan() error {
	if m.plan.HasConflict() {
		return m.plan.CancelDraft()
	}
	return m.plan.Cancel()
}

// applyPlanEdit stores a plan revised in the panel's editor.
func (m *model) applyPlanEdit(edited plan.Plan) tea.Cmd {
	if err := m.plan.UpdateDraft(edited); err != nil {
		return m.planActionFailed(err)
	}
	m.afterPlanChange()
	m.refreshPlanPanel()
	return tea.Batch(m.CommitMessages()...)
}

func (m *model) planActionFailed(err error) tea.Cmd {
	m.conv.AddNotice("Plan: " + err.Error())
	m.refreshPlanPanel()
	return tea.Batch(m.CommitMessages()...)
}

// afterPlanChange re-derives the permission posture and persists the state
// after any store mutation.
func (m *model) afterPlanChange() {
	m.syncPlanPermissions()
	m.persistPlan()
}

func (m *model) showPlanPanel() {
	if _, ok := m.plan.Active(); !ok {
		if _, ok := m.plan.Draft(); !ok {
			return
		}
	}
	m.userInput.Plan.Enter(m.plan.Snapshot(), m.env.Width, m.env.Height)
}

func (m *model) refreshPlanPanel() {
	m.userInput.Plan.Refresh(m.plan.Snapshot())
}

// restorePlanState adopts a session's saved plan state on load or fork, and
// reopens the panel where the plan left off.
func (m *model) restorePlanState(state plan.State) {
	m.plan.Restore(state)
	m.syncPlanPermissions()
	if m.plan.Mode() == plan.ModeChat && state.ActiveID == "" {
		m.userInput.Plan.Close()
		return
	}
	m.showPlanPanel()
}

// resetPlanState drops the plan when the conversation it belongs to is gone.
func (m *model) resetPlanState() {
	m.plan.Reset()
	m.syncPlanPermissions()
	m.userInput.Plan.Close()
}

// planStatusLabel is the status bar's Plan Mode badge: "plan" and "execute"
// are postures the operation-mode indicator cannot express.
func (m *model) planStatusLabel() string {
	return string(m.plan.Mode())
}

func stepOutcomeNotice(outcome plan.StepResult) string {
	switch outcome.Status {
	case plan.StepBlocked:
		reason := strings.TrimSpace(outcome.Reason)
		if reason == "" {
			reason = "the step did not meet its verification criteria"
		}
		return "Step blocked: " + reason + "\nThe plan is paused; retry, restart, skip, or cancel it in the Execution Panel."
	case plan.StepSkipped:
		return "Step skipped."
	}
	return ""
}
