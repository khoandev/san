---
package: github.com/genai-io/san/internal/tool
layer: feature
---

# tool

Registry of built-in tools the agent can call, with their JSON schemas,
permission gate, side-effect plumbing, and per-call dispatch.

## Purpose

Every built-in tool (Bash, Read, Edit, Write, WebFetch, …)
registers into this package's singleton at init time. The shell tool is
named for the shell `proc.DefaultShell` picks — `PowerShell` on Windows,
`Bash` elsewhere (`SAN_SHELL` overrides) — so the model writes that
shell's syntax. A Windows with both also registers the other, disabled by
default (`/tools` turns it on), whose description says how to split the work; `IsShellTool` is how the rest of San asks "is this the shell
tool" without naming one. The agent loop calls
`Execute(name, params, cwd)` to dispatch; the registry resolves the tool,
runs the permission check (`internal/setting`), invokes the tool, and
returns a `toolresult.ToolResult` with stdout, error, side-effect handle,
and audit metadata.

## Contract

Built-in tool registry: tools register at init time; consumers fetch by name and dispatch through the permission gate. The package exposes `*Registry` directly — no Service interface.

```go
package tool

// Registry is the opaque handle. Type exported; fields unexported.
type Registry struct { /* internal fields */ }

func (r *Registry) Register(t Tool)
func (r *Registry) Get(name string) (Tool, bool)
func (r *Registry) List() []string
func (r *Registry) Execute(ctx context.Context, name string, params map[string]any, cwd string) toolresult.ToolResult
func (r *Registry) PopSideEffect(toolCallID string) any

// Package-level access
func Initialize(opts Options)
func Default() *Registry
func SetDefaultRegistry(r *Registry)  // test-only
func ResetDefaultRegistry()           // test-only
```

## Agent delegation

Delegate only for a clear benefit and with little need for prior conversation
context; otherwise work directly.

## Shell arguments

The model-facing `Bash` interface requires `command` (string) and accepts:

- `description`: optional short string describing the work.
- `execution_mode`: optional string, one of `foreground` (default),
  `background_job`, or `background_service`.
- `timeout_ms`: optional positive integer in milliseconds for finite foreground
  commands and background jobs (default 120000, maximum 600000). Omit it or use
  `null` for services. It limits execution, not how long to wait before returning.

JSON `null` for an optional argument is treated as omitted. Prefer leaving
`execution_mode` out for ordinary commands: foreground execution returns the
result before the next step. Use background jobs only for genuinely independent
work, and services only for long-running processes with an explicit readiness
check and cleanup plan. Starting a service does not prove it is healthy.

The old `timeout`, `run_in_background`, and `lifetime` fields are runtime-only
compatibility inputs, not model-schema properties. Explicit conflicts between
canonical and legacy arguments are errors, not mode overrides. Validation errors
are rejected before human approval and checked again before process launch; they
must not be replaced with a generic approval prompt. This cleanup does
not change `Agent.run_in_background` or task lifecycle metadata. See
[task](task.md#shell-jobs-and-services) for service and session ownership rules.

## Internals

- `Registry` (`registry.go`) — name → `Tool` map plus alias map. The
  package-level `defaultRegistry` is the singleton; subpackages call
  `tool.Register(...)` from `init()`.
- Schemas (`schema_base.go`, `schema_agent.go`, `schema_task.go`) — JSON
  schema fragments shared by the built-in tools.
- Permission gate (`perm/`) — wraps tool execution with
  `setting.HasPermissionToUseTool`. Returns deny / ask / allow.
- Side-effect store — `Execute` may return a token; the consumer calls
  `PopSideEffect(token)` to retrieve a queued action (e.g. a pending
  file write).
- Subpackages own their tools: `fs/` for filesystem, `web/` for fetch,
  `agent/` for the Agent tool, `task/` for task tools, `skill/` for skill
  invocation, etc.
- Editing (`fs/edit.go`, `fs/edit_sloppy.go`, `fs/sloppy/`, `fs/sloppyedit/`) —
  `Read` prints rows as `42|content`, and `Edit` takes one `payload` in the
  sloppy anchored format ported from pulseaiclub/phi: `*** SM:EDIT path`,
  `*** SM:FIND` anchor, then `*** SM:PUT`, `*** SM:AFTER`, or `⟪old│new⟫`
  selections. The parser (`fs/sloppy`) produces the operation IR and
  `error[SMxxx]` diagnostics; the engine (`fs/sloppyedit`) locates each anchor
  through a tolerant matching ladder and plans all splices against the original
  text, so a payload applies atomically or not at all. One call edits one file
  (`[E_MULTI_FILE]` otherwise) because a permission review covers one diff, and
  the file must have a current view (`fs.ResetFileViews()` on `/clear` clears
  it). Matching is fail-closed: an absent or ambiguous anchor never falls back
  to a whole-file rewrite. The ladder starts on the raw file, and the fold into
  the matching space (whitespace dropped, punctuation folded) is built on first
  use: an anchor copied from the file never pays for it, and a retyped one pays
  once. Folding a whole file is the engine's largest per-edit allocation, so
  keep it behind that lazy boundary.
- The payload header, not an argument, names the file, so anything outside the
  tool that scopes or previews a call reads the target through
  `perm.CallFile(toolName, args)` (`internal/tool/perm/call.go`): permission
  rules, the working-directory and sensitive-path checks, the auto-memory
  exemption, the subagent activity line, and the conversation's Edit preview.
  Reading `args["file_path"]` directly works only for `Read` and `Write`; for
  `Edit` it silently yields an empty path, which turns a narrow rule into one
  that matches every call.

## Lifecycle

- Registration: tools register from `init()` in their subpackages.
- `Initialize(Options{})` flips the singleton to `defaultRegistry` —
  before that, `Default()` already returns `defaultRegistry`, so
  `init()`-time registrations are not lost.
- Per-call: `Execute` is goroutine-safe; the registry uses an RWMutex.
- `Write` and `Edit` replace file contents through `atomicfile.WriteFile` so
  failed writes leave the original intact. Existing permissions and symlink
  targets are retained; new files honor the umask. Replacement changes the
  inode, so hard-link aliases retain their old contents; devices, pipes and
  other non-regular targets are rejected.
- Foreground shell capture is bounded while the full stream is drained and
  counted. Model output and hook stdout/stderr each retain at most 30,000 bytes
  plus a truncation notice. Interactive capture and unfinished prompt text are
  bounded too. Background output streams directly to the task log, with the
  existing 512 KiB in-memory preview; stdout/stderr share their arrival order.
- Shell progress and final display details use the same exact interleaved row
  count, including an unfinished last row. SDK `Report` carries `BashDetails`
  through `ToolUpdate` for the UI, not the model conversation. `TailTruncated`
  distinguishes a bounded byte suffix from complete output; omission counts
  must not be inferred from rounded counter text or retained newlines alone.

## Tests

```
internal/tool/execute_test.go         — dispatch and not-found behavior.
internal/tool/schema_registry_test.go — schema generation, including the Agent tool.
internal/tool/pretool_hook_test.go    — PreToolUse hook outcomes and the permission gate.
internal/tool/fs/write_failure_test.go — failed writes preserve original contents.
internal/tool/fs/shell_options_test.go — mode/default/null/legacy parsing and runtime execution.
internal/tool/fs/shell_schema_test.go — canonical nullable schemas sent through both OpenAI protocols.
internal/tool/fs/shell_output_test.go — bounded capture and complete background logs.
internal/tool/fs/edit_test.go          — Edit payloads: apply, atomicity, view gate, staleness, limits.
internal/tool/fs/edit_bench_test.go    — apply cost and request-payload size.
internal/tool/fs/sloppy/               — payload parsing, pattern tokens, diagnostics.
internal/tool/fs/sloppyedit/           — apply engine, matching ladder, parity fixtures, fuzzing.
internal/tool/perm/call_test.go       — the file a call targets, from file_path or the payload header.
```

## See Also

- Code: `internal/tool/`
- Primitive: [`packages/core.md`](core.md) (`Tool` and `Tools` interfaces)
- Permission gate: [`packages/setting.md`](setting.md), [`concepts/permission-model.md`](../concepts/permission-model.md)
- MCP-registered tools: [`packages/mcp.md`](mcp.md)
- Layer: `feature`
