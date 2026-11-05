package plan

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrNoPlan reports an action that needs an active plan.
var ErrNoPlan = errors.New("no active plan")

// Store owns one session's Plan Mode state. The UI goroutine drives it from
// the Execution Panel while the agent goroutine reads the mode for submission
// and turn-end parsing, so every exported method takes the lock and returns
// copies.
type Store struct {
	mu    sync.RWMutex
	state State
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{} }

// Snapshot returns a detached copy of the whole state.
func (s *Store) Snapshot() State {
	if s == nil {
		return State{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Clone()
}

// Restore replaces the state, as a session resume does.
func (s *Store) Restore(state State) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state.Clone()
	for i := range s.state.Plans {
		s.state.Plans[i].Normalize()
	}
	if s.state.ActiveID != "" && s.planLocked(s.state.ActiveID) == nil {
		s.state.ActiveID = ""
	}
	if s.state.DraftID != "" && s.planLocked(s.state.DraftID) == nil {
		s.state.DraftID = ""
	}
}

// Reset clears the state for a fresh conversation.
func (s *Store) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = State{}
}

// Mode reports the current mode.
func (s *Store) Mode() Mode {
	if s == nil {
		return ModeChat
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Mode
}

// EnterPlanMode switches to Plan Mode and pauses a plan that was executing.
func (s *Store) EnterPlanMode() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Mode = ModePlan
	if p := s.activeLocked(); p != nil && p.Status == StatusExecuting && !p.Paused {
		p.Paused = true
		p.AddLog("mode", "", "Plan Mode entered; execution paused")
	}
}

// ExitPlanMode returns to ordinary chat. It refuses to silently discard a
// plan still under construction; the caller cancels explicitly.
func (s *Store) ExitPlanMode() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Mode = ModeChat
}

// Active returns a copy of the active plan.
func (s *Store) Active() (Plan, bool) {
	if s == nil {
		return Plan{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.activeLocked()
	if p == nil {
		return Plan{}, false
	}
	return p.Clone(), true
}

// Draft returns the plan awaiting Execute new plan / Keep existing context.
func (s *Store) Draft() (Plan, bool) {
	if s == nil {
		return Plan{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.DraftID == "" {
		return Plan{}, false
	}
	p := s.planLocked(s.state.DraftID)
	if p == nil {
		return Plan{}, false
	}
	return p.Clone(), true
}

// HasConflict reports whether a fresh draft must be resolved against an
// existing, unfinished plan.
func (s *Store) HasConflict() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conflictLocked()
}

func (s *Store) conflictLocked() bool {
	if s.state.DraftID == "" || s.state.ActiveID == "" || s.state.DraftID == s.state.ActiveID {
		return false
	}
	active := s.planLocked(s.state.ActiveID)
	return active != nil && !active.Terminal()
}

// SubmitDraft records a plan the agent just produced. It becomes the active
// plan unless an unfinished plan already holds that place, in which case the
// user must choose between the two in the Execution Panel.
//
// A draft only supersedes an earlier *draft*: replacing a draft never
// deletes or marks an approved plan.
func (s *Store) SubmitDraft(p Plan) Plan {
	if s == nil {
		return p
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	p.Normalize()
	if p.Status == "" || p.Status == StatusDraft {
		p.Status = StatusPendingApproval
	}
	p.AddLog("draft", "", "Plan ready for review")

	// A prior unselected draft is replaced, not kept as history.
	if s.state.DraftID != "" && s.state.DraftID != s.state.ActiveID {
		if old := s.planLocked(s.state.DraftID); old != nil {
			old.Status = StatusCancelled
			old.AddLog("draft", "", "Replaced by a newer draft")
		}
	}

	replacedIndex := -1
	for i := range s.state.Plans {
		if s.state.Plans[i].ID == p.ID {
			replacedIndex = i
			break
		}
	}
	if replacedIndex >= 0 {
		s.state.Plans[replacedIndex] = p
	} else {
		s.state.Plans = append(s.state.Plans, p)
		replacedIndex = len(s.state.Plans) - 1
	}
	s.state.Plans[replacedIndex].Normalize()

	if s.state.ActiveID == "" {
		s.state.ActiveID = p.ID
		s.state.DraftID = ""
	} else if s.state.ActiveID == p.ID {
		s.state.DraftID = ""
	} else if active := s.planLocked(s.state.ActiveID); active == nil || active.Terminal() {
		s.state.ActiveID = p.ID
		s.state.DraftID = ""
	} else {
		s.state.DraftID = p.ID
	}
	if s.state.Mode == ModeChat {
		s.state.Mode = ModePlan
	}
	return p.Clone()
}

// ResolveNewPlan adopts the draft: the previous plan is marked superseded and
// stays in the session. It does not start a step; Approve does.
func (s *Store) ResolveNewPlan() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.conflictLocked() {
		return fmt.Errorf("no new plan is waiting")
	}
	draft := s.planLocked(s.state.DraftID)
	old := s.planLocked(s.state.ActiveID)
	if draft == nil || old == nil {
		return ErrNoPlan
	}
	old.Status = StatusSuperseded
	old.SupersededBy = draft.ID
	old.Paused = true
	old.AddLog("superseded", "", "Replaced by "+draft.Title)

	s.state.ActiveID = draft.ID
	s.state.DraftID = ""
	s.state.Mode = ModePlan
	return nil
}

// KeepExisting discards the unresolved draft and returns to the existing
// plan. A step that was in flight asks the user what to do with it.
func (s *Store) KeepExisting() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.DraftID == "" {
		return fmt.Errorf("no new plan is waiting")
	}
	if draft := s.planLocked(s.state.DraftID); draft != nil && draft.ID != s.state.ActiveID {
		draft.Status = StatusCancelled
		draft.AddLog("draft", "", "Not selected; existing plan kept")
	}
	s.state.DraftID = ""
	active := s.activeLocked()
	if active == nil {
		return ErrNoPlan
	}
	if active.Terminal() {
		s.state.Mode = ModeChat
		return nil
	}
	s.state.Mode = ModeExecute
	active.Status = StatusExecuting
	active.AwaitingStepChoice = false
	if i := active.FirstUnfinished(); i >= 0 {
		active.StepIndex = i
		if active.Steps[i].Status == StepInProgress || active.Steps[i].Status == StepBlocked {
			active.AwaitingStepChoice = true
			active.Paused = true
		}
	}
	active.AddLog("resume", "", "Existing plan kept; continuing from the first unfinished step")
	return nil
}

// Approve moves a pending plan into execution. A conflict must be resolved
// first, so approving never silently drops the plan the user was running.
func (s *Store) Approve() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conflictLocked() {
		return fmt.Errorf("choose Execute new plan or Keep existing context first")
	}
	if s.state.DraftID != "" && s.state.DraftID == s.state.ActiveID {
		s.state.DraftID = ""
	}
	active := s.activeLocked()
	if active == nil {
		return ErrNoPlan
	}
	if active.Terminal() {
		return fmt.Errorf("plan is %s", active.Status)
	}
	if err := active.Validate(); err != nil {
		return err
	}
	active.Status = StatusExecuting
	active.Paused = false
	active.AwaitingStepChoice = false
	active.StepIndex = active.FirstUnfinished()
	if active.StepIndex < 0 {
		active.Status = StatusCompleted
		s.state.Mode = ModeChat
		return nil
	}
	active.AddLog("approve", "", "Plan approved; execution started")
	s.state.Mode = ModeExecute
	return nil
}

// BeginStep marks the focused step in progress. The caller runs the step only
// after this returns, so a restored session never sees a step complete that
// was never marked started.
func (s *Store) BeginStep() (Step, int, error) {
	if s == nil {
		return Step{}, -1, ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeLocked()
	if active == nil {
		return Step{}, -1, ErrNoPlan
	}
	if active.Status != StatusExecuting || active.Paused {
		return Step{}, -1, fmt.Errorf("plan is not runnable (%s)", active.Status)
	}
	if active.AwaitingStepChoice {
		return Step{}, -1, fmt.Errorf("resolve the in-flight step first")
	}
	i := active.FirstUnfinished()
	if i < 0 {
		active.Status = StatusCompleted
		s.state.Mode = ModeChat
		return Step{}, -1, fmt.Errorf("plan has no unfinished step")
	}
	step := &active.Steps[i]
	active.StepIndex = i
	step.Status = StepInProgress
	step.Attempts++
	step.StartedAt = time.Now()
	step.FinishedAt = time.Time{}
	step.BlockReason = ""
	active.AddLog("step_start", step.ID, "Started step "+step.Title)
	return step.Clone(), i, nil
}

// StepResult is what the agent reports at the end of a step turn.
type StepResult struct {
	Status   StepStatus `json:"status"`
	Result   string     `json:"result,omitempty"`
	Files    []string   `json:"files,omitempty"`
	Evidence []string   `json:"evidence,omitempty"`
	Reason   string     `json:"reason,omitempty"`
}

// CompleteStep records the running step's outcome. A step claiming completion
// without a result, or without evidence for its verification criteria, is
// recorded as blocked instead: the executor never advances on an unproven
// step.
func (s *Store) CompleteStep(result StepResult) (bool, error) {
	if s == nil {
		return false, ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeLocked()
	if active == nil {
		return false, ErrNoPlan
	}
	step, ok := active.CurrentStep()
	if !ok || step.Status != StepInProgress {
		return false, fmt.Errorf("no step is in progress")
	}
	sidx := active.StepIndex
	out := &active.Steps[sidx]
	out.Files = cloneStrings(result.Files)
	out.Evidence = cloneStrings(result.Evidence)
	out.Result = strings.TrimSpace(result.Result)
	out.FinishedAt = time.Now()

	switch result.Status {
	case StepCompleted:
		reason := verify(step, result)
		if reason != "" {
			out.Status = StepBlocked
			out.BlockReason = reason
			active.Paused = true
			active.AddLog("step_blocked", step.ID, reason)
			return false, nil
		}
		out.Status = StepCompleted
		out.BlockReason = ""
		active.AddLog("step_done", step.ID, "Completed step "+step.Title)
		return s.advanceLocked(active), nil
	case StepSkipped:
		out.Status = StepSkipped
		active.AddLog("step_skipped", step.ID, "Skipped step "+step.Title)
		return s.advanceLocked(active), nil
	case StepBlocked:
		out.Status = StepBlocked
		out.BlockReason = strings.TrimSpace(result.Reason)
		if out.BlockReason == "" {
			out.BlockReason = "step reported blocked without a reason"
		}
		active.Paused = true
		active.AddLog("step_blocked", step.ID, out.BlockReason)
		return false, nil
	default:
		out.Status = StepBlocked
		out.BlockReason = "step reported no usable status"
		active.Paused = true
		active.AddLog("step_blocked", step.ID, out.BlockReason)
		return false, nil
	}
}

// verify enforces the step's verification criteria and the minimum report.
func verify(step Step, result StepResult) string {
	if strings.TrimSpace(result.Result) == "" {
		return "step reported completion without a result"
	}
	if len(step.Verification) > 0 && len(result.Evidence) == 0 {
		return "step reported completion without verification evidence"
	}
	return ""
}

// advanceLocked moves to the next unfinished step, completing the plan when
// none remains.
func (s *Store) advanceLocked(active *Plan) bool {
	if i := active.FirstUnfinished(); i >= 0 {
		active.StepIndex = i
		return true
	}
	active.Status = StatusCompleted
	active.Paused = false
	active.AwaitingStepChoice = false
	active.StepIndex = max(0, len(active.Steps)-1)
	s.state.Mode = ModeChat
	active.AddLog("complete", "", "Plan completed")
	return false
}

// SkipStep skips the current step. It is allowed while a step is blocked or
// awaiting a choice, and while a step is in progress only after a pause.
func (s *Store) SkipStep() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeLocked()
	if active == nil {
		return ErrNoPlan
	}
	step, ok := active.CurrentStep()
	if !ok {
		return fmt.Errorf("plan has no step to skip")
	}
	if step.Status == StepCompleted || step.Status == StepSkipped {
		return fmt.Errorf("step is already %s", step.Status)
	}
	active.Steps[active.StepIndex].Status = StepSkipped
	active.Steps[active.StepIndex].FinishedAt = time.Now()
	active.AwaitingStepChoice = false
	active.Paused = false
	active.AddLog("step_skipped", step.ID, "Skipped step "+step.Title)
	s.advanceLocked(active)
	return nil
}

// RetryStep re-runs a blocked step.
func (s *Store) RetryStep() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeLocked()
	if active == nil {
		return ErrNoPlan
	}
	step, ok := active.CurrentStep()
	if !ok || step.Status != StepBlocked {
		return fmt.Errorf("only a blocked step can be retried")
	}
	active.Steps[active.StepIndex].Status = StepInProgress
	active.Steps[active.StepIndex].BlockReason = ""
	active.Steps[active.StepIndex].Attempts++
	active.Steps[active.StepIndex].StartedAt = time.Now()
	active.Steps[active.StepIndex].FinishedAt = time.Time{}
	active.AwaitingStepChoice = false
	active.Paused = false
	active.Status = StatusExecuting
	active.AddLog("step_retry", step.ID, "Retrying step "+step.Title)
	s.state.Mode = ModeExecute
	return nil
}

// RestartStep clears a step's outcome and starts it again from the top.
func (s *Store) RestartStep() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeLocked()
	if active == nil {
		return ErrNoPlan
	}
	step, ok := active.CurrentStep()
	if !ok {
		return fmt.Errorf("plan has no step to restart")
	}
	if step.Status == StepCompleted || step.Status == StepSkipped {
		return fmt.Errorf("step is already %s", step.Status)
	}
	reset := &active.Steps[active.StepIndex]
	reset.Status = StepInProgress
	reset.Result = ""
	reset.Files = nil
	reset.Evidence = nil
	reset.BlockReason = ""
	reset.Attempts++
	reset.StartedAt = time.Now()
	reset.FinishedAt = time.Time{}
	active.AwaitingStepChoice = false
	active.Paused = false
	active.Status = StatusExecuting
	active.AddLog("step_restart", step.ID, "Restarted step "+step.Title)
	s.state.Mode = ModeExecute
	return nil
}

// Pause stops the executor between steps.
func (s *Store) Pause() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeLocked()
	if active == nil {
		return ErrNoPlan
	}
	if active.Status != StatusExecuting {
		return fmt.Errorf("plan is not executing")
	}
	active.Paused = true
	active.AddLog("pause", "", "Execution paused")
	return nil
}

// Resume continues a paused plan. A step that was in flight asks the user
// whether to continue, restart, or skip it.
func (s *Store) Resume() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeLocked()
	if active == nil {
		return ErrNoPlan
	}
	if active.Status != StatusExecuting {
		return fmt.Errorf("plan is not executing")
	}
	active.Paused = false
	active.AddLog("resume", "", "Execution resumed")
	step, ok := active.CurrentStep()
	if ok && step.Status == StepInProgress {
		active.AwaitingStepChoice = true
		active.Paused = true
	}
	return nil
}

// ResolveStepChoice applies Continue, Restart, or Skip to a step that was in
// flight when execution resumed. Continue keeps the step in progress; the
// caller re-issues its prompt.
func (s *Store) ResolveStepChoice(action string) error {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "continue":
		if s == nil {
			return ErrNoPlan
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		active := s.activeLocked()
		if active == nil {
			return ErrNoPlan
		}
		active.AwaitingStepChoice = false
		active.Paused = false
		active.AddLog("step_continue", "", "Continued the in-flight step")
		return nil
	case "restart":
		return s.RestartStep()
	case "skip":
		return s.SkipStep()
	default:
		return fmt.Errorf("unknown step choice %q", action)
	}
}

// Cancel cancels the active plan and stops execution.
func (s *Store) Cancel() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeLocked()
	if active == nil {
		return ErrNoPlan
	}
	if active.Status == StatusSuperseded {
		return fmt.Errorf("plan is superseded")
	}
	active.Status = StatusCancelled
	active.Paused = true
	active.AwaitingStepChoice = false
	active.AddLog("cancel", "", "Plan cancelled")
	s.state.Mode = ModeChat
	s.state.DraftID = ""
	return nil
}

// CancelDraft cancels a plan still awaiting the new-plan decision, leaving the
// active plan untouched.
func (s *Store) CancelDraft() error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.DraftID == "" {
		return fmt.Errorf("no new plan is waiting")
	}
	if draft := s.planLocked(s.state.DraftID); draft != nil && draft.ID != s.state.ActiveID {
		draft.Status = StatusCancelled
		draft.AddLog("cancel", "", "Cancelled the new draft")
	}
	s.state.DraftID = ""
	return nil
}

// UpdateDraft replaces an unapproved plan's contents from the Edit plan
// editor. Editing is refused once execution has started.
func (s *Store) UpdateDraft(p Plan) error {
	if s == nil {
		return ErrNoPlan
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		return fmt.Errorf("plan ID is required")
	}
	index := -1
	for i := range s.state.Plans {
		if s.state.Plans[i].ID == p.ID {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrNoPlan
	}
	current := &s.state.Plans[index]
	if current.Status == StatusExecuting || current.Status == StatusCompleted || current.Status == StatusCancelled || current.Status == StatusSuperseded {
		return fmt.Errorf("a %s plan cannot be edited", current.Status)
	}
	p.Normalize()
	p.Status = StatusPendingApproval
	p.SupersededBy = ""
	p.Progress = current.Progress
	p.CreatedAt = current.CreatedAt
	p.AddLog("edit", "", "Plan edited")
	s.state.Plans[index] = p
	if s.state.DraftID == "" && s.state.ActiveID == p.ID {
		s.state.DraftID = ""
	}
	return nil
}

func (s *Store) activeLocked() *Plan {
	if s.state.ActiveID == "" {
		return nil
	}
	return s.planLocked(s.state.ActiveID)
}

func (s *Store) planLocked(id string) *Plan {
	for i := range s.state.Plans {
		if s.state.Plans[i].ID == id {
			return &s.state.Plans[i]
		}
	}
	return nil
}
