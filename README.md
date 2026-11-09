<div align="center">
  <h1>&lt; SAN ✦ /&gt;</h1>
  <p><strong>Minimal overhead. Maximum agent.</strong></p>
  <p>Lean context, native speed, open all the way down.</p>
  <p>
    <a href="https://github.com/genai-io/san/releases"><img src="https://img.shields.io/github/v/release/genai-io/san?style=flat-square" alt="Release"></a>
    <a href="https://genai-io.github.io/san/"><img src="https://img.shields.io/badge/Website-0d9488?style=flat-square" alt="Website"></a>
    <a href="https://genai-io.github.io/san/getting-started.html"><img src="https://img.shields.io/badge/Getting%20Started-0d9488?style=flat-square" alt="Getting Started"></a>
    <a href="docs/index.md"><img src="https://img.shields.io/badge/Docs-0d9488?style=flat-square" alt="Docs"></a>
    <a href="https://www.producthunt.com/products/san?launch=san"><img src="https://img.shields.io/badge/Product%20Hunt-da552f?style=flat-square&logo=producthunt&logoColor=white" alt="Product Hunt"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue?style=flat-square" alt="License"></a>
  </p>
  <p>
    <a href="https://genai-io.github.io/san/intro.html?variant=readme"><picture>
      <source media="(prefers-color-scheme: dark)" srcset="assets/san-intro-dark.gif">
      <img src="assets/san-intro.gif" alt="San — animated intro" width="100%">
    </picture></a>
  </p>
  <sub><a href="https://genai-io.github.io/san/intro.html?variant=readme">Open the full-quality intro ↗</a></sub>
</div>

## Why San

- **Small and fast** — A single native Go binary with minimal context overhead. No Node.js or Python required.
- **A less opinionated harness** — A model-agnostic runtime with a consistent interface for shaping agent behavior through system prompts, personas, skills, and tools.
- **Multi-agent coordination** — Subagent collaboration, cross-session messaging, and dynamic workflows.
- **Control and visibility** — Configure execution permissions and inspect model context and tool calls.

[Features](docs/features.md) · [Architecture](docs/concepts/architecture.md)

<sub>*The name — **San**, written **三** ("three") and drawn **☰**. From the Dao De Jing, 三生万物 — "three begets the ten-thousand things": one runtime that becomes any agent, running a three-step loop (reason → act → observe). The command stays `san`.*</sub>

## Installation

**macOS / Linux** — choose one:

```bash
# Homebrew
brew install genai-io/san/san

# Or use the install script
curl -fsSL https://raw.githubusercontent.com/genai-io/san/main/install.sh | bash
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/genai-io/san/main/install.ps1 | iex
```

<details>
<summary><b>Other installation methods, updates and uninstall</b></summary>

**Uninstall**

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/genai-io/san/main/install.sh | bash -s uninstall
```

```powershell
# Windows (PowerShell)
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/genai-io/san/main/install.ps1))) uninstall
```

**Go Install (requires Go 1.26.9+)**

```bash
go install github.com/genai-io/san/cmd/san@latest
```

**Build from Source**

```bash
git clone https://github.com/genai-io/san.git
cd san
go build -o san ./cmd/san
mkdir -p ~/.local/bin && mv san ~/.local/bin/
```

**Updates**

- **Automatic:** new releases install in the background; the status line prompts you to restart.
- **Manual:** run `san update`, or `brew update && brew upgrade genai-io/san/san` for Homebrew installations. The Homebrew formula syncs daily, so newly published releases may take time to appear.
- **Disable automatic updates:** use `/settings` › General or set `SAN_DISABLE_AUTOUPDATE=1`.

</details>

## Usage

Run `san`, then choose a model and add its API key when prompted.

```bash
san                          # interactive
san "explain this function"  # one-shot
san -p "do something"        # print mode (no TUI), pipe-friendly
san --continue               # resume the latest session
san --resume                 # pick a past session to resume
```

Use `san --help` for CLI options and `/help` for interactive commands.

### Shell output: rtk

If [`rtk`](https://github.com/rtk-ai/rtk) is on your `PATH`, San runs every Bash
command through it — `git status` becomes `rtk git status` — so the model reads
compressed output. There is nothing to configure: rtk is detected per command,
and a command it cannot handle — or a multi-line script — runs unchanged.
Permission rules, the approval preview and the transcript still show the command
as written. Turn it off with `SAN_DISABLE_RTK=1`, or ask for one command's raw
output with `rtk bypass <cmd>`.

## Editing: one anchored patch, applied atomically

`Read` prints each line as `42|content`. `Edit` takes a single payload that
quotes only the lines which identify the change — here, renaming a function
without restating its 40-line body:

```
*** SM:EDIT src/users.go
*** SM:FIND
func load(…) {
	…
	return old(…)
}
*** SM:PUT
func load(…) {
	…
	return fresh(…)
}
```

- `…` elides a run the anchor does not restate and replays it in the
  replacement; `⟪old│new⟫` rewrites a fragment in place, with no action header.
- The anchor is matched tolerantly — indentation, spacing and typographic
  punctuation may drift from the file — but it must be **unique**. A missing or
  ambiguous anchor applies nothing; it never falls back to a whole-file rewrite.
- Failures are written for the model: a syntax error is `error[SM102]` with the
  payload line and the fix, a missed anchor returns the region it nearly matched
  and a copy-ready payload to resend.

Every operation plans against the original text, so either the whole payload
lands or the file is untouched. One call edits one file, because one approval
reviews one diff.

This is the whole Edit surface: san's earlier shapes — line anchors
(`remove_from`/`remove_to`) and exact string replacement
(`old_string`/`new_string`) — were deleted, because both asked the model to
reproduce text it had already read. The payload format is a byte-compatible port
of [pulseaiclub/phi](https://github.com/pulseaiclub/phi)'s sloppy edit engine,
matching ladder and diagnostics included.

### The same change, written three ways

Rename a function and what it returns, keeping its 40-line body. Every shape
below expresses that one edit of a 2,000-line Go file:

| | Sloppy patch | Line anchors (removed) | Exact string (removed) |
|---|---|---|---|
| Call the model writes | **175 B** | 1.1 KB · 6.4x | 2.2 KB · 12.5x |
| What it has to know | the 2 lines that identify the change | a 5-char handle per anchor, from the current Read | every byte between the two sites |
| Anchor retyped from memory | matches anyway, 1.6 ms | stale handle — re-Read | miss — failed call, re-Read |
| One call, target already in context | 1.05 ms · 0.92 MB | 1.08 ms · 1.14 MB | 1.06 ms · 1.03 MB |
| Engine share of that call | 44 µs · 70 KB | — | — |
| Read of the whole file | 68 KB (`42\|content`) | 89 KB (`    42 qwert│content`) | 89 KB |

<sub>Go 1.27.3, darwin/amd64, i5-8257U. `go test ./internal/tool/fs/ -run XXX
-bench 'Edit|Apply'` reproduces the timings, and `TestEditPayloadComparison` pins
the byte counts. The call row restores the file before every iteration, so each
measurement is the same edit — `BenchmarkEditRestored` is that frame. The
removed shapes' rows come from the commit that still had them (`86ea52a2^`), on
the same file and the same edit, because those tools are gone from this tree.</sub>

**Why San adopted it.** What a user waits on is the model writing the call, not
San parsing it, so the number that matters is how many bytes the model must
generate to describe a change: 175, against 1.1 KB and 2.2 KB for the same edit,
and the patch stays 175 B however long the body it keeps. Restating the range is
also where transcription slips come from, and a slip is a failed call plus a
re-read. Tolerance removes the other failure mode — indentation, spacing and
typographic punctuation may drift — while uniqueness and atomicity keep the tool
fail-closed.

**What it costs.** Nothing on the common path. A call whose anchor is copied
from the file runs at the same speed as the shapes it replaced, with less
allocated, and the engine's own share is 44 µs: a substring scan, not the
whole-file fold that used to run per payload on every edit. The one place the
patch is slower is the one where the others fail: an anchor the model retyped
rather than copied forces the tolerant rungs, which fold the file, and costs
~0.5 ms and ~0.7 MB more than an exact anchor. That is the price of matching a
call the other shapes would have rejected.

The rest of the trade is structural: anchoring needs a current view — `Read`
first, and a file changed on disk afterwards says so instead of guessing.

## Benchmark: San vs Claude Code

Compared with [Claude Code](https://claude.ai/code) v2.1.112 on Apple Silicon, same model (`claude-sonnet-4-6`):

| Metric | San | Claude Code | Advantage |
|--------|---------|-------------|-----------|
| Download size | 12 MB | 63 MB (+ Node.js 112 MB) | **5x smaller** |
| Disk footprint | 38 MB | 175 MB | **4.6x smaller** |
| Startup time | ~0.01s | ~0.20s | **20x faster** |
| Startup memory | ~32 MB | ~189 MB | **5.8x less** |
| Simple task | ~2.4s / 39 MB | ~10.4s / 286 MB | **4.3x faster, 7.3x less memory** |
| Tool-use task | ~3.3s / 39 MB | ~26.0s / 285 MB | **7.9x faster, 7.2x less memory** |
| Harness context* | ~2.3k tokens | ~20.9k tokens | **~9x leaner** |

<sub>*Context overhead is system prompt + tool schemas on an empty first turn, measured separately on San v1.22.0 vs Claude Code v2.1.220 — see [method](docs/operations/benchmark.md#7-context-overhead-first-turn). All other rows are from the v1.13.2 / v2.1.112 run.</sub>

Comparable feature sets — the gap is client-side overhead, not capability. [`docs/operations/benchmark.md`](docs/operations/benchmark.md)

## Documentation

- [Get started](docs/index.md#get-started) — setup, configuration, commands, and permissions.
- [Features](docs/features.md) — capabilities and detailed documentation.
- [Customize and extend](docs/index.md#customize-and-extend) — prompts, personas, skills, and integrations.
- [Develop and contribute](docs/index.md#develop-and-contribute) — architecture, development, and design.

## Related Projects

- [Claude Code](https://claude.ai/code) — Anthropic's AI coding assistant
- [Aider](https://github.com/Aider-AI/aider) — AI pair programming in terminal
- [Continue](https://github.com/continuedev/continue) — Open-source AI code assistant

## Community

Two ways in — WeChat for the Chinese community, Slack for everyone else:

- **WeChat** — 关注公众号「极客外传」· 回复 `san` 或 `三` 入群
- **Slack** — [Join our Slack](https://join.slack.com/t/sanaico/shared_invite/zt-3zvfr8v6f-dchFpvpufY7fKA7tG7lhIg)

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache License 2.0 - see [LICENSE](LICENSE) for details.
