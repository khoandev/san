---
package: github.com/genai-io/san/internal/plan
layer: feature
---

# plan

Owns Plan Mode's data model and rules: the plan document, its step machine,
and the session-scoped store that the Execution Panel drives. A plan is
session state — it is persisted inside the transcript, never as a file of its
own.

## Purpose

Plan Mode is a read-only posture in which the agent investigates and produces a
plan, followed by Execute Mode in which the agent runs the approved steps one at
a time. This package holds everything both modes need to agree on:

- the `Plan` value (goal, scope, constraints, risks, open questions, steps,
  decisions, progress journal) and its lifecycle status;
- the `Store` that is the only writer of that value, enforcing which
  transitions are legal so the panel can never offer an impossible action;
- the wire protocol the model speaks: ` ```plan `, ` ```plan-update `, and
  ` ```plan-step ` blocks in the turn's final text;
- the plain-text format the panel's **Edit plan** editor round-trips.

The app owns the UI and the agent; this package never imports either, so it
stays pure and testable.

## Contract

The store is a concrete `*Store`; callers take a `State` snapshot for the UI
and persistence, and mutate only through methods that return errors.

```go
package plan

var ErrNoPlan = errors.New("no active plan")

type Mode string    // ModeChat | ModePlan | ModeExecute
type Status string  // StatusDraft | StatusPendingApproval | StatusExecuting |
                    // StatusCompleted | StatusCancelled | StatusSuperseded
type StepStatus string // StepPending | StepInProgress | StepCompleted |
                       // StepBlocked | StepSkipped

type State struct {
    Mode     Mode
    Plans    []Plan  // every plan this session produced, oldest first
    ActiveID string  // the plan being worked on
    DraftID  string  // a new draft awaiting Execute new plan / Keep existing
}

type Store struct{ /* unexported */ }

func NewStore() *Store
func (s *Store) Snapshot() State
func (s *Store) Restore(state State)
func (s *Store) Reset()

// Mode
func (s *Store) Mode() Mode
func (s *Store) EnterPlanMode()
func (s *Store) ExitPlanMode()

// Drafting
func (s *Store) SubmitDraft(p Plan) Plan   // returns the stored plan (IDs assigned)
func (s *Store) UpdateDraft(p Plan) error
func (s *Store) Approve() error            // pending_approval -> executing
func (s *Store) ResolveNewPlan() error     // supersede the old plan, activate the draft
func (s *Store) KeepExisting() error       // keep the running plan, cancel the draft

// Step machine
func (s *Store) BeginStep() (Step, int, error)
func (s *Store) CompleteStep(result StepResult) (advanced bool, err error)
func (s *Store) SkipStep() error
func (s *Store) RetryStep() error
func (s *Store) RestartStep() error
func (s *Store) Pause() error
func (s *Store) Resume() error
func (s *Store) Interrupt(reason string) error         // cancelled turn: pause, ask, do not block
func (s *Store) ResolveStepChoice(action string) error // continue | restart | skip
func (s *Store) Cancel() error
func (s *Store) CancelDraft() error

// Protocol
func ParsePlanBlock(text string) (Plan, bool, error)
func ParseStepBlock(text string) (StepResult, bool, error)
func ParseUpdateBlock(text string) (Update, bool, error)
func Merge(p *Plan, u Update)
func PlanRequest(request string) string
func StepPrompt(p Plan, step Step, index int) string

// Editor format
func FormatText(p Plan) string
func ParseText(text string) (Plan, error)
func MergeEdit(base, edited Plan) Plan
```

The `State` and `Plan` values are JSON-tagged and deep-cloned at the seam
(`State.Clone`, `Plan.Clone`) so the transcript writer and the UI never alias
live state.

## Internals

- **One mutex, one writer.** Every transition goes through `Store`; an illegal
  one returns an error instead of a partial change. `Snapshot`/`Restore` are
  the only wide doors, used for persistence and resume.
- **Evidence gates advancement.** `CompleteStep` checks the step's
  `Verification` criteria against the reported `Evidence`. A step that claims
  `completed` with no evidence is recorded as `blocked` with a reason, and a
  blocked step pauses the plan — the executor never walks past it.
- **Interrupted is not blocked.** A turn the person cancelled mid-step goes
  through `Interrupt`: the step stays `in_progress`, the plan pauses and asks
  continue / restart / skip. Only a turn that ends without a report on its own
  is a blocked step.
- **Sequential only.** `BeginStep` starts the first unfinished step and marks
  it `in_progress`; `advanceLocked` moves the cursor only after a completed or
  skipped step. `Paused` and `AwaitingStepChoice` hold the cursor where it is.
- **Replaced, never deleted.** `ResolveNewPlan` marks the previous plan
  `superseded` with `SupersededBy` and keeps its steps, decisions and journal;
  the new plan starts with pending steps.
- **Blocks are the protocol.** `ParsePlanBlock` reads the last ` ```plan `
  fence in the turn's text, `ParseStepBlock` the last ` ```plan-step `, and
  `ParseUpdateBlock` the last ` ```plan-update `. Unknown fields and malformed
  JSON are errors, not silent drops.
- **Identity survives an edit.** `MergeEdit` folds editor text onto the plan
  it replaces: same `ID`, existing step IDs kept by position, new steps get
  IDs, and the progress journal stays while step statuses restart as pending.

## Lifecycle

- The app builds one `Store` per session (`model_plan.go`) and restores it from
  `transcript.StatePatch.Plan` on resume and fork.
- Every mutation is followed by `appendSessionState`, so the plan travels with
  the conversation; no plan file is written.
- Entering Plan Mode flips the session permission posture to read-only; leaving
  it restores ordinary permissions and pauses a running plan.
- The gate (`setting.PlanModeAllows`) refuses only what provably writes: the
  read tools, read-only shell commands (pipelines and `echo` banners included,
  expansions and redirections excepted), and mounted MCP tools whose leading
  name verb is a read all stay usable. A mounted tool San built nothing for is
  judged by that name alone — `write_file` is refused, `codegraph_explore` is
  not.
- A restored plan with a step left `in_progress` sets `AwaitingStepChoice` so
  the panel can ask continue / restart / skip instead of silently resuming.

## Tests

```
internal/plan/plan_test.go  — statuses, evidence gating, supersede/keep, step
                              recovery, pause/resume, validation.
internal/plan/text_test.go  — editor round-trip, refused input, identity kept
                              across an edit.
internal/app/plan_mode_test.go        — /plan, panel wiring, session round-trip,
                                        supersede vs keep, blocked step.
internal/app/input/plan_panel_test.go — which buttons each state offers, the
                                        editor save path.
internal/setting/plan_mode_test.go    — what the read-only gate refuses and
                                        what it lets through.
```

## See Also

- Code: `internal/plan/`
- App wiring: [`packages/1-app/app.md`](../1-app/app.md), `internal/app/model_plan.go`
- Commands: [`reference/slash-commands.md`](../../reference/slash-commands.md) (`/plan`)
- Permissions: [`packages/2-feature/setting.md`](setting.md)
- Layer: `feature`
