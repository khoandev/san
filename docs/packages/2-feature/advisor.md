---
package: github.com/genai-io/san/internal/advisor
layer: feature
---

# advisor

The global advisor: an independent, read-only reviewer of what the agent and
its subagents produced. Every review classifies findings P0–P3, and the block
policy decides what a finding means for a merge, a finished subagent task or a
plan step.

## Purpose

`advisor` owns the review domain — the finding model, the severity rubric, the
block policy, the trigger detection, the bounded review engine and the
append-only review store. The app owns everything around it: settings
resolution, the model wiring, the UI, and the gate that sits in the permission
path.

The package is deliberately outside the Bubble Tea model: it imports no app
package, never prompts the TUI, and never touches the main agent's
conversation. Each review is a fresh read-only fork with its own system prompt,
so a review can neither be influenced by the work it is judging nor leave
state behind for the next one.

## Triggers and flow

| Trigger | Default | What it reviews |
| --- | --- | --- |
| `subagent_task` | on | A finished background subagent task, against what it was asked to do. |
| `before_merge` | on | The whole diff against the base branch, before the merge command runs. |
| `plan_step` | off | A plan step's diff, before the step is recorded as done. |
| `large_change` (size arms) | off | A change crossing `minChangedFiles` / `minChangedLines`. |
| sensitive path | on | A change matching a configured sensitive pattern, however small; it reports as `large_change` with that reason. |
| `user_request` | — | Whatever `/advisor diff \| commit \| task \| step \| session \| file` names. |

The size and sensitive-path arms are resolved by `TriggerForDiff` and covered by
tests, but Phase 1 wires no automatic caller for them: only the three gates
above and `/advisor` start a review today. Treating an unreviewed large change
as normal work is the current gap, listed as Phase 2 in
[PROP-0005](../../design/proposals/0005-advisor-global-review.md#phase-2--remaining-not-committed).

End-to-end for an automatic review:

1. The trigger fires — a task completes, a plan step reports done, `/advisor`
   is asked for a review, or a merge-like command reaches the permission path.
2. The app builds an `advisor.ReviewInput` from what it knows (goal, acceptance
   criteria, checks, decisions, constraints) and reads the diff. `BuildRequest`
   caps the diff at `thresholds.maxDiffBytes`, derives the changed paths and
   carries the mode and finding cap.
3. `Engine.Run` forks a reviewer that may use only `Read`, `Grep` and `Glob`
   through an allow-only permission function, gives it its own system prompt and
   a 12-step budget inside the review's timeout, and parses the JSON answer. The
   model's own claimed status is discarded; `Policy` resolves the real one from
   the findings.
4. The review lands in the store and the transcript append-only, and the
   trigger's consumer reads the verdict:
   - a plan step gets `AdvisorReviewID` + `NeedsReview` and the plan pauses;
   - a subagent task gets `background_needs_review` metadata instead of
     counting as clean;
   - a merge is permitted, confirmed or rejected before it runs.
5. The status bar badge and the `/advisor` panel read the same store; nothing
   is recomputed for display.

Merge detection is **best-effort**: `IsMergeCommand` recognises the shapes the
reviewer knows — `git merge`, `git push` to the base branch, `gh pr
create|merge`, `glab mr create|merge` — with global options and flags
normalized away. A merge spelled some other way (a wrapper script, an alias, a
custom deploy script) is not detected and runs ungated. That is a coverage
limit of a syntactic check, not a safety property: the gate catches the common
paths, and the permission rules remain the authority for everything else.

## Contract

Concrete types and functions; there is no producer-side service interface.

```go
package advisor

// Findings and verdicts.
type Severity string   // "P0" | "P1" | "P2" | "P3"
type ReviewStatus string // passed | passed_with_notes | changes_required | blocked | error
type Trigger string    // subagent_task | before_merge | plan_step | large_change | user_request
type TargetKind string // diff | task | step | commit | session | file | merge
type FindingStatus string // open | addressed | dismissed | deferred

func ParseSeverity(s string) (Severity, bool)
func (s Severity) Valid() bool
func (s Severity) Rank() int
func (s Severity) Less(other Severity) bool

type Target struct {
    Kind   TargetKind
    ID     string
    Label  string
    Base   string
    Head   string
    Files  []string
}

func (t Target) Clone() Target
func (t Target) Empty() bool

type Finding struct {
    ID       string
    Severity Severity
    Title    string
    Detail   string
    File     string
    Line     int
    Evidence string
    Impact   string
    Suggestion string
    Status   FindingStatus
    Note     string
    UpdatedAt time.Time
}

func (f Finding) Unresolved() bool
func (f *Finding) Resolve(status FindingStatus, note string) error

type Review struct {
    ID      string
    Trigger Trigger
    Target  Target
    Status  ReviewStatus
    Severity Severity
    Summary  string
    Findings []Finding
    DiffHash string
    Model    string
    Error    string
    StartedAt  time.Time
    FinishedAt time.Time
}

func (r Review) Clone() Review
func (r Review) Highest() Severity
func (r Review) Counts() Counts
func (r Review) UnresolvedCounts() Counts
func (r Review) OpenFindings() []Finding
func (r Review) Finding(id string) (Finding, bool)
func (r Review) Validate() error

type Counts struct{ P0, P1, P2, P3 int }

func (c Counts) Total() int
func (c Counts) Blocking(from Severity) int
func NewID(prefix string) string

// Block policy: the lowest level that blocks each gated action.
type Action string // merge | task_completion | plan_step_completion

type Policy struct {
    BlockMerge              Severity
    BlockTaskCompletion     Severity
    BlockPlanStepCompletion Severity
    RequireConfirmation     Severity
    AllowProceedWithRisk    bool
}

func (p Policy) Threshold(action Action) Severity
func (p Policy) Blocks(action Action, severity Severity) bool
func (p Policy) NeedsConfirmation(severity Severity) bool
func ActionForTrigger(t Trigger) (Action, bool)
func Evaluate(trigger Trigger, findings []Finding, policy Policy) Verdict
func Recommend(review Review, policy Policy) Recommendation
func (r *Review) ApplyPolicy(policy Policy)

type Verdict struct {
    Status   ReviewStatus
    Severity Severity
    Counts   Counts
}

type Recommendation string // safe_to_merge | review_required | blocked

// Engine: one bounded, read-only review.
type EngineConfig struct {
    Client         func(msgs []core.Message) (*ai.Client, error)
    CallOptions    func() []ai.Option
    ShapeInference func(system string, tools []core.ToolSchema) (string, []core.ToolSchema)
    Tools          *core.Tools
    Policy         Policy
    Model          string
    Mode           string
    MaxTokens      int
    Timeout        time.Duration
    MaxFindings    int
    CWD            string
    OnEvent        func(core.Event)
}

type Engine struct{ /* unexported */ }

func NewEngine(cfg EngineConfig) *Engine
func (e *Engine) Run(ctx context.Context, req Request) (Review, error)

type Request struct {
    Trigger       Trigger
    Target        Target
    Diff          string
    DiffHash      string
    Files         []string
    DiffTruncated bool
    Goal          string
    Acceptance    []string
    Checks        []string
    Decisions     []string
    Constraints   []string
    CustomPrompt  string
    Mode          string
    MaxFindings   int
}

var ReadOnlyToolNames = []string{"Read", "Grep", "Glob"}

func ReadOnlyTools(cwd string) *core.Tools

// Triggers: detection, request building, cooldown.
type ReviewInput struct {
    Trigger       Trigger
    Target        Target
    Diff          string
    DiffHash      string
    Files         []string
    DiffTruncated bool
    Goal          string
    Acceptance    []string
    Checks        []string
    Decisions     []string
    Constraints   []string
    CustomPrompt  string
}

func BuildRequest(cfg setting.AdvisorSettings, in ReviewInput) Request
func TriggerForDiff(cfg setting.AdvisorSettings, diff proc.GitDiff) (Trigger, string)
func SensitivePaths(cfg setting.AdvisorSettings, files []string) []string
func MergeCommand(cmd, baseBranch string) string
func IsMergeCommand(cmd string) bool

type Cooldown struct{ /* unexported */ }

func NewCooldown() *Cooldown
func (c *Cooldown) Due(target Target, diffHash string, window time.Duration) bool
func (c *Cooldown) Mark(target Target, diffHash string)
func (c *Cooldown) Remaining(target Target, diffHash string, window time.Duration) time.Duration
func (c *Cooldown) Reset()

// Merge gate: the pre-merge check inside the permission path.
type MergeVerdict int // MergeNotGated | MergePermit | MergeConfirm | MergeReject

type MergeOutcome struct {
    Verdict MergeVerdict
    Reason  string
    Review  Review
}

type MergeGateConfig struct {
    Enabled    func() bool
    Policy     func() Policy
    BaseBranch string
    Timeout    time.Duration
    ReadDiff   func(ctx context.Context) (proc.GitDiff, error)
    Review     func(ctx context.Context, in ReviewInput) (Review, error)
    Reuse      func(target Target, diffHash string) (Review, bool)
    Record     func(Review)
}

type MergeGate struct{ /* unexported */ }

func NewMergeGate(cfg MergeGateConfig) *MergeGate
func (g *MergeGate) Check(ctx context.Context, name string, input map[string]any) MergeOutcome

// Store: one session's append-only review history.
type State struct {
    Reviews []Review
}

func (s State) Empty() bool
func (s State) Clone() State
func (s State) Latest() (Review, bool)
func (s State) LatestFor(target Target) (Review, bool)
func (s State) ReviewFor(target Target, diffHash string) (Review, bool)
func (s State) Append(review Review) (State, Review, error)
func (s State) UpdateFinding(reviewID, findingID string, status FindingStatus, note string, at time.Time) (State, error)

type Store struct{ /* unexported */ }

func NewStore() *Store
func (s *Store) Snapshot() State
func (s *Store) Restore(state State)
func (s *Store) Reset()
func (s *Store) Append(review Review) (Review, error)
func (s *Store) UpdateFinding(reviewID, findingID string, status FindingStatus, note string) error
func (s *Store) Reviews() []Review
func (s *Store) Latest() (Review, bool)
func (s *Store) LatestFor(target Target) (Review, bool)
func (s *Store) ReviewFor(target Target, diffHash string) (Review, bool)
func (s *Store) OpenFindings() []FindingRef
func (s *Store) Counts() Counts
```

## Configuration

`setting.AdvisorSettings` is the configuration surface; every on/off knob is a
`*bool` so an explicit `false` round-trips through a layered merge, and the
resolvers (`Enabled`, `BeforeMerge`, `MergeThreshold`, `ResolvedTimeout`, …)
are the only read path. The app resolves the block policy with `advisorPolicy`
and the bounds with `BuildRequest`, so this package never reads settings on
its own except through the `cfg setting.AdvisorSettings` argument of
`BuildRequest` and `TriggerForDiff`.

Defaults worth knowing: the advisor itself is opt-in (`enable` unset means
off), `before_merge` / `afterSubagentTask` / `sensitivePaths` are on,
`beforePlanStep` / `onLargeChanges` are off, a P0 blocks and a P1 asks the
human, and `allowProceedWithRisk` is off — a block is final until the finding
is fixed. See [`reference/configuration.md`](../../reference/configuration.md#advisor)
for the field list.

`/settings` → Advisor exposes three of them — `enable`, the reviewer model
(`model.model`, picked from the connected `vendor/model` refs) and the review
depth `review.mode` (fast / standard / deep) — because that is the whole
decision a person makes before trusting the layer. Triggers, block policy,
bounds and display options keep their documented defaults and are edited in
`settings.json`. A panel with forty rows reads as a system that must be
configured before it can be switched on, which is the opposite of what an
opt-in review layer should cost.

The panel edits the session's live config, not the file it was seeded from, and
it saves whole: the block is replaced at the chosen level, so a knob turned back
to its default actually persists there. The picked model ref is resolved by
`resolveReviewer` on the next turn — `ensureAgentRuntime` rebuilds when
`advisorSignature` changes, so a save or a model switch takes effect without a
restart.

## Internals

- `Engine` holds no state. Each `Run` builds a fresh fork from `EngineConfig`,
  applies the mode's guidance, and parses one JSON object; the fork's tool
  registry is `ReadOnlyTools(cwd)` filtered by `ReadOnlyToolNames`, gated by an
  allow-only `perm.PermissionFunc`, so a review cannot write even if the
  session runs with bypass permissions.
- `Policy.Blocks` is keyed by action, not by a single on/off, because the same
  P1 may hold back a merge while leaving a finished task usable. An empty
  threshold means "never blocks", so there is no second boolean to contradict.
- `Evaluate` ignores the status the model claimed; `Recommend` re-evaluates an
  existing review against the *merge* policy, so a lenient task policy cannot
  launder a P0 into a merge.
- `MergeGate` is synchronous and stateless: one diff read, one review, one
  timeout, and only for a shell call whose command line integrates a branch.
  Every other tool call returns `MergeNotGated` without a diff read or a model
  call. A review that cannot run fails open with a warning, unless the merge
  policy blocks on P0 — "we could not check" must not read as "we checked".
- `Store` takes a lock on every exported method and returns copies; `Append`
  never replaces a review, and a missing id is assigned rather than erroring.
  The transcript is the durability layer: `advisor.reviewed` and
  `advisor.finding.updated` records project back into a fresh `State`.
- `Cooldown` keys on target identity plus diff hash, so re-running a review of
  unchanged code is suppressed while new code is reviewed again.

## Lifecycle

- Construction: the app calls `wireAdvisor` after the agent runtime starts and
  builds one `Engine`, one `Cooldown` and one merge gate per session. A
  disabled or invalid configuration builds nothing — no goroutine, no extra
  model calls.
- Run: automatic reviews go through one run slot (at most one in flight) and
  the cooldown; a merge review claims the slot but not the cooldown, because
  the diff hash already decided whether it is new work.
- Shutdown: `teardownAdvisor` cancels the session context and flips a liveness
  flag, so a late verdict is dropped rather than written into a torn-down
  session. The store and indicator outlive a teardown so the last verdict
  stays readable.
- Concurrency: `Store` and `Cooldown` are mutex-guarded; `MergeGate` is
  stateless and safe from the agent goroutine; nothing here prompts the TUI.

## Tests

```
internal/advisor/types_test.go    — severity ranks, target identity, review
                                    validation, counts.
internal/advisor/policy_test.go   — per-action thresholds, evaluate/recommend,
                                    dismissal rules.
internal/advisor/engine_test.go   — read-only tool set, answer parsing, bounds.
internal/advisor/trigger_test.go  — merge-command detection, size and sensitive
                                    triggers, cooldown, request building.
internal/advisor/merge_test.go    — verdict table (P0/P1 × block on/off),
                                    hash reuse, fail-open vs fail-closed.
internal/advisor/store_test.go    — append-only history, finding lifecycle.
internal/app/advisor_test.go      — runner wiring, transcript persistence.
internal/app/advisor_plan_test.go — plan step gating and pause.
internal/app/advisor_task_test.go — task metadata and notices.
internal/app/advisor_panel_test.go — /advisor targets and panel actions.
internal/app/input/advisor_panel_test.go — panel rendering, filters, actions.
internal/app/input/config_advisor_test.go — the /settings tab: live seed, scope
                                    cycle, save targets, model picker.
internal/app/conv/status_advisor_test.go — badge states and hiding rules.
```

## Pitfalls

- **Merge detection is syntactic and best-effort.** A merge hidden behind a
  script or an alias is not gated. Do not treat `IsMergeCommand` as a security
  boundary; the permission rules are.
- **Never trust the model's claimed status.** `Review.Status` must come from
  `ApplyPolicy`; a review that says "passed" while listing a P0 has to be
  resolved, not believed.
- **Do not let the review goroutine touch the UI.** Verdicts travel as a
  channel message handled on the UI goroutine, and the `live` flag decides
  whether they are still wanted. A review that completes after teardown must
  be discarded, not applied to a stale session.
- **Keep reviews bounded.** A review without a timeout, a step cap, a token
  cap, a finding cap or a cooldown is a runaway model call on someone's
  dime — the defaults exist for that reason.
- **Dismissal is a decision.** Dismissing a P0/P1 without a reason is rejected
  by `Finding.Resolve`; do not add a path that bypasses it.
- **The diff hash is identity.** Reuse and cooldown both key on it, so a
  caller that invents a hash (or forgets to pass the diff reader's) silently
  duplicates reviews.

## See Also

- Code: `internal/advisor/`
- App wiring: `internal/app/advisor.go`, `internal/app/advisor_panel.go`,
  `internal/app/merge_gate.go`, `internal/app/config_advisor.go`
- Related packages: [`plan`](plan.md), [`task`](task.md), [`setting`](setting.md),
  [`transcript`](transcript.md), [`agent`](agent.md)
- Concepts: [`concepts/permission-model.md`](../../concepts/permission-model.md)
- Reference: [`reference/configuration.md`](../../reference/configuration.md#advisor),
  [`reference/slash-commands.md`](../../reference/slash-commands.md)
- Layer: `feature` (see [`reference/dependency-rules.md`](../../reference/dependency-rules.md))
- Design: [PROP-0005](../../design/proposals/0005-advisor-global-review.md)
