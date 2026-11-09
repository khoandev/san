# Auto-tier routing and a four-slot role catalog

Status: active (implementation complete, uncommitted)

## Goal

Routine work reaches a cheap agent and a cheap model without the main model
having to guess; hard work reaches the strong model with a fuller brief and a
narrower assignment. The catalog the model reads is shorter: four agents and
four roles instead of five agents and nine roles.

## Decisions

- Roles are the only routing table: `default` (the session model), `fast`,
  `standard`, `deep`. An unassigned role answers with the session model. The
  classifier itself runs on the first assigned role in `fast` → `standard` →
  session-model order and keeps that role's effort, so routing works on a
  machine that assigned only some roles; one debug line names the role used.
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
- The classifier (`internal/triage`) is one inference with no tools and a 4 s
  bound. It decides the definition first (investigate / implement / small edit /
  review) and the rung second; a verdict that names a usable definition but no
  usable rung keeps that definition's declared rung. Every failure — broken
  JSON, unusable verdict, provider error, timeout — becomes the safe `standard`
  route, never a failed call.
- Every call is offered to the router, because only the host can tell a
  definition from a label: a name the host recognizes is never substituted, and
  a name it does not recognize — like no name at all — is classified, with the
  invented name surviving as the route's display label. One posture read decides
  both the definition and the permission ceiling.
- Read-only sessions route too: a brief review reaches `reviewer`, an
  investigation `explore`, and no verdict can hand an edit charter to a session
  that cannot approve it.
- The route is pinned in the call's parameters at approval time, so what the
  user approves is what executes. A continuation keeps its original config and
  tier and is never classified again.
- `agents.autoTier` (default on) is the off switch. Headless `san agent run`
  uses each definition's declared tier and installs no router.
- The Agent tool description opens with when to delegate rather than when not,
  routes by intent to `explore` / `task` / `quick` / `reviewer`, and still
  teaches the per-rung brief rubric (one sentence for `fast`; goal, paths and
  definition of done for `standard`; exact files, constraints, acceptance
  criteria and risks for `deep`). `behavior.txt` carries the same cross-tool
  policy, so it holds even when the schema is skimmed. Routine work omits both
  `name` and `tier`; san routes it.
- Every routed or named call reports its route: the tool result carries
  `ran as <definition> (<tier>, <model>)` plus the caller's label when it kept
  one, the subtitle names the definition that actually ran, and
  `toolresult.AgentDetails` carries `AgentName`/`Tier` for the UI. A call that
  was neither routed nor named adds no line — there is nothing to correct.

## Files of record

- `internal/llm/roles.go`, `internal/core/modelrole.go`,
  `internal/setting/modelrole.go` — the ladder, the alias table, the fold.
- `internal/subagent/agents/*.md`, `internal/subagent/types.go`,
  `internal/subagent/registry.go` — the four definitions and their tiers.
- `internal/subagent/route.go`, `internal/subagent/executor.go` — the routing
  contract and how a decision reaches the run.
- `internal/tool/agent/schema.go`, `internal/tool/agent/agent.go`,
  `internal/tool/agent/result.go` — the `tier` parameter, approval pinning,
  execution reuse, and the route line reported to the model.
- `internal/core/system/prompts/behavior.txt`, `internal/core/system/catalog.go`
  — the cross-tool delegation policy.
- `internal/triage/` — the classifier and its definition-first rubric.
- `internal/app/auto_tier.go`, `internal/app/agent.go`,
  `internal/app/acp_agents.go`, `internal/setting/{settings,service,merger}.go`
  — wiring, the classifier model fallback and the `agents.autoTier` setting.
- `internal/tool/toolresult/render.go`, `internal/app/conv/tool_render.go` — the
  `AgentDetails` route fields and how the TUI renders them.

## Verification

- `gofmt -l .` reports only the pre-existing `internal/setting/bash_readonly.go`;
  `go build ./...` is clean.
- `go test ./internal/tool/agent/... ./internal/subagent/... ./internal/triage/...
  ./internal/app/... ./internal/core/system/...` all pass. The routing path is
  covered where it matters: an unknown name routes to `explore` while keeping
  its label, a read-only session routes without ever receiving an edit charter,
  a definition survives an unusable rung, the classifier falls back
  `fast` → `standard` → session model, and a routed call reports
  `ran as explore (fast, …)`.
- `make build && mv -f bin/san ~/.config/bin/san && san version`.
- Manual check to repeat on the installed binary: a one-file edit lands on
  `quick`/`fast`, a few-file change on `task`/`standard`, a review or
  architecture question on `reviewer`/`deep`, and the worker's result reports
  `ran as …` for the route it took.
