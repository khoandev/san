// Package plan owns Plan Mode: the plan document, its step state machine, and
// the session state that carries both across a resume.
//
// A plan never lives in a file of its own. The whole State — mode, every plan
// the session has produced, and the active plan's progress — is serialized
// inside the session transcript (see transcript.StatePatch). Execute-new keeps
// the superseded plan in that same State, so the history stays readable.
//
// The package is pure: no Bubble Tea, no agent, no disk. The app layer drives
// it and renders it; the transcript package persists it.
package plan

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Mode is whether the session is planning, executing an approved plan, or in
// ordinary chat.
type Mode string

const (
	ModeChat    Mode = ""
	ModePlan    Mode = "plan"
	ModeExecute Mode = "execute"
)

// Status is the plan's overall lifecycle.
type Status string

const (
	// StatusDraft is a plan still being assembled while the agent asks
	// clarifying questions.
	StatusDraft Status = "draft"
	// StatusPendingApproval is a finished plan waiting for the user in the
	// Execution Panel.
	StatusPendingApproval Status = "pending_approval"
	// StatusExecuting is an approved plan with a step in flight or waiting.
	StatusExecuting Status = "executing"
	StatusCompleted Status = "completed"
	StatusCancelled Status = "cancelled"
	// StatusSuperseded marks a plan replaced by Execute new plan. It stays in
	// the session; nothing is deleted.
	StatusSuperseded Status = "superseded"
)

// StepStatus is one step's state.
type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepInProgress StepStatus = "in_progress"
	StepCompleted  StepStatus = "completed"
	StepBlocked    StepStatus = "blocked"
	StepSkipped    StepStatus = "skipped"
)

// Decision is a choice settled while planning, kept so execution does not
// reopen it.
type Decision struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// LogEntry is the plan's progress journal: what ran, what it proved, and what
// the user decided.
type LogEntry struct {
	At      time.Time `json:"at"`
	StepID  string    `json:"stepId,omitempty"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// Step is one sequential unit of the plan.
type Step struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Detail       string     `json:"detail,omitempty"`
	Objective    string     `json:"objective,omitempty"`
	Verification []string   `json:"verification,omitempty"`
	Status       StepStatus `json:"status"`
	Result       string     `json:"result,omitempty"`
	Files        []string   `json:"files,omitempty"`
	Evidence     []string   `json:"evidence,omitempty"`
	BlockReason  string     `json:"blockedReason,omitempty"`
	Attempts     int        `json:"attempts,omitempty"`
	StartedAt    time.Time  `json:"startedAt,omitempty"`
	FinishedAt   time.Time  `json:"finishedAt,omitempty"`
	// AdvisorReviewID names the independent review that inspected this step's
	// result, and NeedsReview carries the severity it refused. A step can be
	// StepCompleted — the work was done and reported — and still carry both:
	// the review changes how the completion reads, not what happened. Empty for
	// steps no review refused.
	AdvisorReviewID string `json:"advisorReviewId,omitempty"`
	NeedsReview     string `json:"needsReview,omitempty"`
}

// Done reports whether the step needs no more work.
func (s Step) Done() bool {
	return s.Status == StepCompleted || s.Status == StepSkipped
}

// Clone returns a deep copy of the step.
func (s Step) Clone() Step {
	out := s
	out.Verification = cloneStrings(s.Verification)
	out.Files = cloneStrings(s.Files)
	out.Evidence = cloneStrings(s.Evidence)
	return out
}

// Plan is one plan document.
type Plan struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Goal  string `json:"goal,omitempty"`

	InScope       []string `json:"inScope,omitempty"`
	OutOfScope    []string `json:"outOfScope,omitempty"`
	Constraints   []string `json:"constraints,omitempty"`
	Risks         []string `json:"risks,omitempty"`
	OpenQuestions []string `json:"openQuestions,omitempty"`

	Steps     []Step     `json:"steps,omitempty"`
	Decisions []Decision `json:"decisions,omitempty"`
	Progress  []LogEntry `json:"progress,omitempty"`

	Status Status `json:"status"`
	// Paused stops the executor between steps without leaving Execute Mode.
	Paused bool `json:"paused,omitempty"`
	// AwaitingStepChoice asks the user to continue, restart, or skip a step
	// that was in flight when the session was restored.
	AwaitingStepChoice bool `json:"awaitingStepChoice,omitempty"`
	// StepIndex is the step the panel focuses and the executor is running.
	// -1 means no step is selected.
	StepIndex int `json:"stepIndex"`

	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	SupersededBy string    `json:"supersededBy,omitempty"`
}

// State is the session-scoped Plan Mode state persisted in the transcript.
type State struct {
	Mode Mode `json:"mode,omitempty"`
	// Plans holds every plan this session produced, oldest first. Superseded
	// plans stay.
	Plans []Plan `json:"plans,omitempty"`
	// ActiveID is the plan the session is working on. It may be executing or
	// pending approval while a newer draft exists.
	ActiveID string `json:"activePlanId,omitempty"`
	// DraftID is a newly drafted plan awaiting Execute new plan / Keep
	// existing context. Empty when no decision is pending.
	DraftID string `json:"draftPlanId,omitempty"`
}

// Empty reports whether the state carries nothing worth persisting.
func (s State) Empty() bool {
	return s.Mode == ModeChat && len(s.Plans) == 0 && s.ActiveID == "" && s.DraftID == ""
}

// Clone returns a deep copy, so a snapshot can be handed to the UI or a
// transcript writer without either side aliasing the live state.
func (s State) Clone() State {
	out := State{Mode: s.Mode, ActiveID: s.ActiveID, DraftID: s.DraftID}
	out.Plans = make([]Plan, len(s.Plans))
	for i := range s.Plans {
		out.Plans[i] = s.Plans[i].Clone()
	}
	return out
}

// Clone returns a deep copy of the plan.
func (p Plan) Clone() Plan {
	out := p
	out.InScope = cloneStrings(p.InScope)
	out.OutOfScope = cloneStrings(p.OutOfScope)
	out.Constraints = cloneStrings(p.Constraints)
	out.Risks = cloneStrings(p.Risks)
	out.OpenQuestions = cloneStrings(p.OpenQuestions)
	out.Decisions = append([]Decision(nil), p.Decisions...)
	out.Progress = append([]LogEntry(nil), p.Progress...)
	out.Steps = make([]Step, len(p.Steps))
	for i := range p.Steps {
		step := p.Steps[i]
		step.Verification = cloneStrings(step.Verification)
		step.Files = cloneStrings(step.Files)
		step.Evidence = cloneStrings(step.Evidence)
		out.Steps[i] = step
	}
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

// Normalize fills the invariants a plan needs to be usable: an ID, step IDs,
// pending statuses, and a valid cursor. It is idempotent, so parsing and
// editing can both call it.
func (p *Plan) Normalize() {
	if p.ID == "" {
		p.ID = newID("plan")
	}
	p.Title = strings.TrimSpace(p.Title)
	p.Goal = strings.TrimSpace(p.Goal)
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}
	for i := range p.Steps {
		p.Steps[i].Normalize()
	}
	p.repairCursor()
}

// Normalize fills a step's identity and default status.
func (s *Step) Normalize() {
	if s.ID == "" {
		s.ID = newID("step")
	}
	s.Title = strings.TrimSpace(s.Title)
	if s.Status == "" {
		s.Status = StepPending
	}
	s.AdvisorReviewID = strings.TrimSpace(s.AdvisorReviewID)
	s.NeedsReview = strings.TrimSpace(s.NeedsReview)
}

// repairCursor keeps StepIndex on an unfinished step when the plan is still
// executable, and on the last step for a finished plan.
func (p *Plan) repairCursor() {
	if len(p.Steps) == 0 {
		p.StepIndex = -1
		return
	}
	if p.StepIndex >= 0 && p.StepIndex < len(p.Steps) && !p.Steps[p.StepIndex].Done() {
		return
	}
	if i := p.FirstUnfinished(); i >= 0 {
		p.StepIndex = i
		return
	}
	p.StepIndex = len(p.Steps) - 1
}

// FirstUnfinished returns the index of the first step that is not completed
// or skipped, or -1 when every step is done.
func (p Plan) FirstUnfinished() int {
	for i := range p.Steps {
		if !p.Steps[i].Done() {
			return i
		}
	}
	return -1
}

// CurrentStep returns the step at StepIndex, if any.
func (p Plan) CurrentStep() (Step, bool) {
	if p.StepIndex < 0 || p.StepIndex >= len(p.Steps) {
		return Step{}, false
	}
	return p.Steps[p.StepIndex], true
}

// Counts returns how many steps are completed, handled (completed or
// skipped), blocked, and the total.
func (p Plan) Counts() (completed, handled, blocked, total int) {
	total = len(p.Steps)
	for _, s := range p.Steps {
		if s.Status == StepCompleted {
			completed++
		}
		if s.Done() {
			handled++
		}
		if s.Status == StepBlocked {
			blocked++
		}
	}
	return completed, handled, blocked, total
}

// Validate reports whether the plan is complete enough to execute.
func (p Plan) Validate() error {
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("plan title is required")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	for i, s := range p.Steps {
		if strings.TrimSpace(s.Title) == "" {
			return fmt.Errorf("step %d has no title", i+1)
		}
	}
	return nil
}

// AddLog appends a progress entry and updates the modification time.
func (p *Plan) AddLog(kind, stepID, message string) {
	p.Progress = append(p.Progress, LogEntry{At: time.Now(), StepID: stepID, Kind: kind, Message: message})
	p.UpdatedAt = time.Now()
}

// AddDecision records a settled choice.
func (p *Plan) AddDecision(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	p.Decisions = append(p.Decisions, Decision{Text: text, At: time.Now()})
	p.UpdatedAt = time.Now()
}

func newID(prefix string) string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// Terminal reports whether no further work can happen on the plan.
func (p Plan) Terminal() bool {
	switch p.Status {
	case StatusCompleted, StatusCancelled, StatusSuperseded:
		return true
	}
	return false
}
