# Auto-tier routing and a four-slot role catalog

Status: active (implementation complete, uncommitted)

## Goal

Routine work reaches a cheap agent and a cheap model without the main model
having to guess; hard work reaches the strong model with a fuller brief and a
narrower assignment. The catalog the model reads is shorter: four agents and
four roles instead of five agents and nine roles.

## Decisions

- Roles are the only routing table: `default` (the session model), `fast`,
  `standard`, `deep`. There is no implicit fallback chain — an unassigned role
  answers with the session model.
- Retired names still resolve through one alias table in `internal/core`
  (`smol`, `explore`, `librarian` → `fast`; `task`, `designer` → `standard`;
  `plan`, `slow`, `reviewer` → `deep`), mirrored by `llm.LegacyRoleAlias` and
  `setting.CanonicalModelRole` without `setting` importing `llm`. Settings files
  are folded onto the new keys on every load and every write.
- Bundled agents are `quick` (`fast`), `explore` (`fast`, absorbed
  `librarian`), `task` (`standard`, absorbed `designer`) and `reviewer`
  (`deep`). A retired agent name resolves to the agent that absorbed it.
- Routing only narrows permission: it never raises `allow_tools` or the mode,
  and a read-only session may only be routed to a read-only definition.
- The classifier (`internal/triage`) is one inference on the `fast` role with no
  tools and a 4 s bound. It runs only when a call names neither `name` nor
  `tier`; every failure — broken JSON, disabled definition, provider error,
  timeout, unassigned `fast` role — becomes `standard`, never a failed call.
- The route is pinned in the call's parameters at approval time, so what the
  user approves is what executes. A continuation keeps its original config and
  tier and is never classified again.
- `agents.autoTier` (default on) is the off switch. Headless `san agent run`
  uses each definition's declared tier and installs no router.
- The Agent tool description teaches the per-rung brief rubric (one sentence
  for `fast`; goal, paths and definition of done for `standard`; exact files,
  constraints, acceptance criteria and risks for `deep`) and says to omit both
  `name` and `tier` for routine work.

## Files of record

- `internal/llm/roles.go`, `internal/core/modelrole.go`,
  `internal/setting/modelrole.go` — the ladder, the alias table, the fold.
- `internal/subagent/agents/*.md`, `internal/subagent/types.go`,
  `internal/subagent/registry.go` — the four definitions and their tiers.
- `internal/subagent/route.go`, `internal/subagent/executor.go` — the routing
  contract and how a decision reaches the run.
- `internal/tool/agent/schema.go`, `internal/tool/agent/agent.go` — the `tier`
  parameter, approval pinning, execution reuse.
- `internal/triage/` — the classifier.
- `internal/app/auto_tier.go`, `internal/app/agent.go`,
  `internal/app/acp_agents.go`, `internal/setting/{settings,service,merger}.go`
  — wiring and the `agents.autoTier` setting.

## Verification

- `gofmt -l .` (only the pre-existing `internal/setting/bash_readonly.go`),
  `go build ./...`, `go test ./...` (three pre-existing unrelated failures in
  `internal/image` and `internal/workflow`).
- `make build && mv -f bin/san ~/.config/bin/san && san version`.
- Three sample calls on the real binary: a one-file edit lands on
  `quick`/`fast`, a few-file change on `task`/`standard`, a review or
  architecture question on `reviewer`/`deep`.
