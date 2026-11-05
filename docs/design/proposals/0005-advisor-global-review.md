# PROP-0005: Global advisor review layer

## Status

Accepted and implemented — Phase 1 shipped 2026-11-05. Phase 2 candidates are
listed at the end; nothing there is committed work.

## Context

San already had one automated review loop: `selflearn` forks a restricted agent
after a turn to create or refine skills. That loop answers "what should the
agent learn?", not "is the work the agent produced correct?". Nothing in the
product independently judged the *result* of a turn:

- A subagent task reported `completed` and the main agent believed it.
- A plan step reported its evidence and the plan advanced.
- A merge command went through the permission rules, which answer "may this
  command run?", not "should this change land?".

The agent that produced the change is the worst judge of it: it wrote the diff,
it wrote the summary, and it wants the step to be done. The gap is a second
opinion with no stake in the answer.

## What Phase 1 ships

One review layer, three gate points, one place to read it.

- **`internal/advisor`** is the domain: the P0–P3 finding model, the block
  policy keyed by action, trigger detection (merge / size / sensitive path),
  a per-target cooldown, the bounded read-only review engine, the merge gate and
  the append-only review store. It imports no app package and no Bubble Tea.
- **Three gates.**
  - *Subagent task:* a finished task is reviewed against its description and
    prompt; a P0 sets `background_needs_review` metadata on the to-do item
    instead of the task counting as clean.
  - *Plan step:* an opt-in review of the step's diff; a P0 records
    `Step.AdvisorReviewID` + `Step.NeedsReview`, keeps the step marked completed
    and pauses the plan.
  - *Merge:* a synchronous gate in the permission path, wrapped around the
    `PermDecisionFunc` / `PermissionFunc` in the agent build, that reviews the
    whole diff against base before a merge-like command runs.
- **UI.** A `/advisor` findings panel with filters, per-finding actions (fix,
  dismiss, defer, follow up, re-run), a status-bar badge, and an Advisor tab in
  `/settings` with user / project / session scope.
- **Persistence.** `advisor.reviewed` and `advisor.finding.updated` transcript
  records project back into a `State`, so a resume or a fork restores the review
  history. Dismissals are recorded decisions.

Every review is bounded — one timeout, a token cap, a finding cap, a step cap,
a cooldown per (target, diff hash) — because it is background work paid for by
someone else's model budget.

## Decisions

**The model's claimed status is discarded.** The reviewer answers with findings
and its own verdict; `Policy.ApplyPolicy` recomputes the status from the
findings. A review that says "passed" while listing a P0 cannot pass, and one
that says "blocked" with nothing in it cannot block. This is the single most
important rule in the package: trusting the reviewer's summary reintroduces the
problem the reviewer exists to solve.

**The policy is keyed by action, not by a global on/off.** The same P1 may hold
back a merge while leaving a finished task usable. Each action carries the least
severe level that blocks it, and an empty threshold means "never blocks", so
there is no second boolean a settings file could contradict. `Recommend`
re-evaluates an existing review against the *merge* policy, so a lenient task
policy cannot launder a P0 into a merge that passes.

**The merge review is synchronous, and only merges pay for it.** A review that
finishes after the merge describes a decision that already happened. The gate
therefore blocks the permission path for one diff read, one review and one
timeout — but only when the call is a shell call whose command line integrates a
branch. Every other tool call returns `MergeNotGated` without reading a diff or
touching a model.

**A review that cannot run fails open with a warning — unless merges block on
P0.** An unavailable reviewer is not evidence of a bad change, so a timeout or a
provider error permits with a notice. Once the user has asked for merges to be
blocked on P0, "we could not check" must not read as "we checked", so that same
error rejects.

**The reviewer is read-only by construction.** The fork gets `Read`, `Grep` and
`Glob` behind an allow-only permission function, with a fixed step budget. A
reviewer that can edit the code it is reviewing, or run its tests, would be a
second agent; the point is an outside reader.

**Findings live in the transcript, not a side file.** The transcript is already
append-only, already resumes and already forks. A re-run adds a review rather
than replacing the verdict it supersedes.

## Alternatives considered

| Alternative | Why not |
| --- | --- |
| Extend `selflearn` instead of a new package | It owns skill writes, a skill-permission model and a per-turn lifecycle. The advisor is read-only, has its own store and is gated by a block policy. Two capabilities would have shared one package's state and one's settings. |
| Reuse `internal/reviewer` (the gray-zone permission judge) | That judge answers "may this call run?" from a rule set; this one reads a diff and classifies what is wrong with it. Only its model-resolution helper is reused. |
| Check merges inside the Bash tool | The tool is one caller of the permission path; a hook, MCP tool or future executor would skip it. Wrapping in the agent build covers every call that goes through permissions. |
| Review merges in the background | The verdict has to exist before the command runs. |
| Block always (fail closed) | Turns a provider outage into an unusable session; the user asked for review, not for a hard dependency. The P0-merge exception is the one case where silence is worse than stopping. |
| One `blockOnP0` boolean for everything | Hides the real product question, which is per action: a task can be "done but flagged" while a merge cannot. |
| Store reviews in a JSON side file beside the session | Duplicates the resume/fork machinery and gives the transcript a second source of truth. |
| Let the reviewer's own status stand | It is the one input the reviewer can be wrong about with no way to tell. |

## Risks

- **Cost.** Reviews spend model calls on every trigger. Mitigated by being
  opt-in, by only `beforeMerge`, `afterSubagentTask` and `sensitivePaths` being
  on by default, and by one-in-flight plus a cooldown per target and diff hash.
- **Merge detection is best-effort.** `IsMergeCommand` is syntactic: `git merge`,
  `git push` to base, `gh pr create|merge`, `glab mr create|merge` with options
  normalized. A merge behind a wrapper script or an alias is not gated. This is
  a coverage limit, not a safety property — the permission rules stay the
  authority. A git-aware hook is a Phase 2 candidate.
- **False confidence.** A read-only reviewer cannot run the tests, so "passed"
  means "no defects visible in the diff and its context", not "verified". The
  panel and the docs say so; the badge shows a verdict, not a guarantee.
- **False findings.** A model reviewer over-reports. The dismissal path is the
  pressure valve, and it is deliberately deliberate: dismissing a P0 or P1
  requires a reason, which is recorded.
- **Reviewer-model quality.** The default inherits the session model. A cheap
  model gives shallow reviews; `model.model` and `review.mode` exist so a user
  can spend more on the merge pass.
- **Concurrency.** Reviews run off the UI goroutine and their verdicts arrive as
  messages with a liveness flag. A verdict that lands after teardown is dropped,
  not written into a dead session; the store and the indicator outlive a
  teardown so the last verdict stays readable.
- **Transcript growth.** Every review is an append-only record with its diff
  hash, findings and timestamps. A long session accumulates them; the alternative
  (overwriting) loses the reason a verdict changed.
- **Settings surface.** Tri-state knobs across three levels are easy to get
  wrong. The file merge is field-level, explicit `false` round-trips, and the
  save notice names the sections another level still supplies.

## Phase 2 — remaining, not committed

- **Git-aware merge detection** instead of string matching: consult the
  repository (a merge in progress, ahead-of-base state) rather than parse the
  command line.
- **Headless entry point:** `san advisor --diff <range>` for CI, sharing the
  engine and the answer contract with the TUI.
- **Richer fix loop:** apply a suggested fix directly from the panel, with the
  follow-up task linked back to the finding it came from.
- **Review history browsing:** the panel shows one review at a time, starting
  from the latest; browsing older reviews and diffing two verdicts by hash is
  not built.
- **Threshold triggers end-to-end:** `onLargeChanges` and the size arms exist in
  the domain, and are off by default; tuning them (per-repo thresholds, ignoring
  generated files) has not been exercised on a real large change.
- **Cost accounting:** review tokens are not surfaced in `/context` or the cost
  view.
- **External publishing:** posting a review as a PR comment, and pruning P3
  noise so a long session's panel stays readable.

## See Also

- Package: [`packages/2-feature/advisor.md`](../../packages/2-feature/advisor.md)
- Config: [`reference/configuration.md#advisor`](../../reference/configuration.md#advisor)
- Command: [`reference/slash-commands.md#advisor`](../../reference/slash-commands.md#advisor)
- Plan: [`notes/active/advisor-global-review.md`](../../../notes/active/advisor-global-review.md)
- Related: [PROP-0002](0002-autonomous-dev-management.md) (personas also benefit
  from an independent reviewer), [`packages/2-feature/selflearn.md`](../../packages/2-feature/selflearn.md)
