# San Agent Guide

This file is the short navigation map for agents and contributors. Keep durable
knowledge in `docs/`; keep this file focused on where to look and what rules to
follow before changing code.

San follows the [AGENTS.md](https://agents.md) standard, so this file is both
the navigation aid for whoever opens the repository and the project
instructions loaded into the running agent's context at session start. Keep it
short: pointers and rules only. Uncommitted personal notes belong in
`AGENTS.local.md` (git-ignored); a subdirectory with its own conventions can
carry its own `AGENTS.md`, and the file nearest the working directory wins.

## Start Here

- Product overview: `README.md`
- Documentation index: `docs/index.md`
- Detailed architecture: `docs/concepts/architecture.md`
- Package map and ownership: `docs/reference/package-map.md`
- Dependency rules: `docs/reference/dependency-rules.md`
- Feature notes: `docs/packages/index.md`
- Development workflow: `docs/operations/development.md`

## Repository Shape

- `cmd/san`: CLI entrypoint and command wiring.
- `internal/app`: Bubble Tea TUI shell, model composition, event routing.
- `internal/core`: stable agent, message, tool, and system-prompt contracts.
- `internal/agent`: agent construction and session-facing runtime setup.
- `internal/llm`: model provider registry, clients, cost and logging helpers.
- `internal/tool`: built-in tool registry, schemas, adapters, and executors.
- `internal/session`: transcript persistence, projection, metadata, resume.
- `internal/task`, `internal/subagent`, `internal/cron`: background work and orchestration.
- `internal/command`, `internal/skill`, `internal/plugin`, `internal/mcp`, `internal/hook`: extension surfaces.
- `internal/setting`, `internal/persona`: configuration and persona overlays.
- `internal/log`, `internal/secret`, `internal/filecache`, `internal/markdown`, `internal/confdir`: infrastructure helpers.
- `internal/selflearn`: background skill review loop (auto memory is the main agent's own).
- `docs`: durable explanations, design decisions, operations, and references.

## Rules

Before editing internal packages, read:

- `docs/reference/dependency-rules.md` — allowed import directions and the
  rule for each layer.
- `docs/design/principles.md` — coding principles for package structure,
  interfaces, tests, and context handling.

Update those files when the rules change. Do not duplicate them here.

san is one application built on [sdk-go](https://github.com/genai-io/sdk-go),
which holds the generic agent building blocks. Do not reimplement what sdk-go
provides; when san grows a generic piece, move it to sdk-go and drop san's
copy. san keeps its own policy and UI. See sdk-go's CONTRIBUTING.md, "What
belongs here".

## Editing

`Read` prints `42|content`; `Edit` takes one `payload` in the sloppy anchored
format ported from [pulseaiclub/phi](https://github.com/pulseaiclub/phi):
`*** SM:EDIT path`, then `*** SM:FIND` + `*** SM:PUT` | `*** SM:AFTER` | `⟪old│new⟫`.
It is the only Edit shape — do not reintroduce string or line-range arguments
without deleting this one in the same change. A payload must fail closed: an
absent or ambiguous anchor applies nothing, and no path may fall back to a
whole-file rewrite.

- `internal/tool/fs/sloppy/` — payload lexer/parser and diagnostics (`error[SMxxx]`).
- `internal/tool/fs/sloppyedit/` — apply engine: tolerant matching ladder,
  ambiguity checks, atomic planning.
- `internal/tool/fs/edit_sloppy.go` — san's wiring: one file per call, current
  view required, BOM/CRLF preserved.
- `internal/tool/fs/edit_bench_test.go` — apply cost and payload-size test;
  `README.md`'s numbers come from here.
- `internal/tool/perm/call.go` — `CallFile` resolves the file a call targets.
  The payload header names it, so permission rules, path checks and previews
  must ask there rather than read a `file_path` argument Edit never sends.

Keep the two ported packages byte-compatible with upstream semantics; a local
dialect would cost a failed round-trip per novel payload mistake.

## Common Commands

See `docs/operations/development.md` for build / test / lint / format
and the sandbox-friendly `GOCACHE` workaround. Update that file when
commands change. Do not duplicate them here.

## Manual Test Build

To test the binary by hand, install it into `~/.config/bin/san` (already on
`PATH`). Build first, then move the fresh binary into place; `mv -f` replaces
the directory entry so macOS sees a new inode instead of a rewritten, stale
code signature:

```bash
make build && mv -f bin/san ~/.config/bin/san
san --version
```

Do not `cp` over an existing binary in place on macOS — it reuses the inode and
AMFI may kill the process with "code signature invalid".

## Documentation Rules

- Add or update docs in the same change as architecture or workflow changes.
- Each feature document should list purpose, entrypoints, core packages, flow,
  configuration, tests, and common pitfalls.
- Architecture decision records live in `docs/design/decisions/`.
- File naming rules live in `docs/reference/file-naming.md`.
- Active plans live in `notes/active/`; completed plans move to
  `notes/completed/`.
