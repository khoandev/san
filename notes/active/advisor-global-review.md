# Advisor — global review layer

Design: [PROP-0005](../../docs/design/proposals/0005-advisor-global-review.md).
Status: Phase 1 complete (2026-11-05). Phase 2 not started.

Goal: the advisor runs as an independent review layer behind the agent, in
every workflow — after a subagent task, before a plan step is recorded as done,
and before a merge — with P0–P3 findings, an append-only review history in the
transcript, a panel and a status badge, and configuration at global, project
and session level.

## Phase 1 — shipped

- [x] **Settings.** `setting.AdvisorSettings` with tri-state `*bool` knobs and
  resolvers (`Enabled`, `BeforeMerge`, `MergeThreshold`, `ResolvedTimeout`, …),
  field-level merge in `setting/merger.go`, user / project / session scopes.
- [x] **Finding model and policy.** `Severity` P0–P3, `ReviewStatus`, `Target`,
  `Finding`, `Review`, `Counts`; `Policy` keyed by action; `Evaluate` ignores
  the model's claimed status; dismissal of a P0/P1 requires a reason.
- [x] **Review store.** `advisor.State` / `Store`, append-only, lock-guarded,
  with per-target and per-hash lookup for cooldown and reuse.
- [x] **Engine.** Bounded read-only fork (`Read` / `Grep` / `Glob` behind an
  allow-only gate), JSON answer contract, mode guidance, step / token / time /
  finding caps.
- [x] **Triggers.** `TriggerForDiff` (size and sensitive path), merge-command
  detection, `BuildRequest`, `Cooldown`.
- [x] **Transcript.** `advisor.reviewed` and `advisor.finding.updated` records,
  projector support, session state patch for the config pin.
- [x] **App wiring.** `wireAdvisor` / `teardownAdvisor` / `advisorSignature`,
  the run slot, notices, the live indicator and the review runner. Commit
  `6536166a`.
- [x] **Task trigger.** A finished subagent task is reviewed; a P0 sets
  `background_needs_review` on the to-do item instead of the task counting as
  clean. Commits `d4b75b51`, `7eb73f4c`, `bf9104cf`.
- [x] **Plan-step trigger.** `Step.AdvisorReviewID` + `Step.NeedsReview`, plan
  paused on a blocking P0, notice pointing at `/advisor`. Commit `05e9af71`.
- [x] **Merge gate.** `agent.MergeGateFunc` wrapping the permission path,
  synchronous whole-diff review, hash reuse, fail-open with a warning except
  when merges block on P0. Commit `9325357b`.
- [x] **Command and panel.** `/advisor` with target subcommands; the findings
  panel with filters and the fix / dismiss / defer / follow-up / re-run actions.
  Commit `9cf718ff`.
- [x] **Settings tab and badge.** Advisor tab in `/settings` with user /
  project / session scope, override notice, session pin persisted in the
  transcript; `advisor · <status>` status-bar badge. Commit `c57c5d4e`.
- [x] **Docs and CI.** Package page, configuration and slash-command reference,
  proposal, this note; `make lint` (including `lint-layers`) and `make test`
  green.

## Phase 2 — remaining

- [ ] Git-aware merge detection instead of command-line string matching.
- [ ] Headless `san advisor --diff <range>` for CI, sharing the engine and the
  answer contract.
- [ ] Apply a suggested fix directly from the panel; link the follow-up task to
  the finding.
- [ ] Browse older reviews in the panel and compare two verdicts by diff hash.
- [ ] Exercise and tune the size / sensitive-path triggers on a real large
  change (per-repo thresholds, generated-file exclusions).
- [ ] Surface review token cost in `/context` and the cost view.
- [ ] Publish a review as a PR comment; prune P3 noise so a long session's
  panel stays readable.
