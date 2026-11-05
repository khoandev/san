# Configuration System

## Overview

Configuration is loaded from multiple files at different scopes. Higher-priority files override lower-priority ones.

**Load priority** (lowest → highest):

| Priority | File |
|----------|------|
| 1 | `~/.claude/settings.json` (Claude user compat) |
| 2 | `~/.san/settings.json` (San user) |
| 3 | `./.claude/settings.json` (Claude project compat) |
| 4 | `./.san/settings.json` (San project) |
| 5 | `./.claude/settings.local.json` |
| 6 | `./.san/settings.local.json` |
| 7 | CLI arguments / environment variables |
| 8 | `managed-settings.json` (read-only system policy) |

**settings.json schema:**

```json
{
  "permissions": {
    "allow": ["Read(**)", "Bash(go test:*)"],
    "deny":  ["Bash(rm -rf*)"],
    "ask":   ["Write(**)"]
  },
  "model": "claude-sonnet-4-6",
  "hooks": { "PreToolUse": [...] },
  "env": { "MY_VAR": "value" },
  "enabledPlugins": { "my-plugin": true },
  "disabledTools": { "WebSearch": true, "Cron": false },
  "theme": "dark",
  "contextBar": true,
  "gitBar": true,
  "skillCommands": true,
  "thinkingDisplay": "collapsed",
  "bashPreviewLines": 3,
  "agentPreviewLines": 3
}
```

### `advisor`

The global advisor — the independent review layer documented in
[`packages/2-feature/advisor.md`](../packages/2-feature/advisor.md). Every knob
is optional; the on/off fields are tri-state, so an explicit `false` survives a
project or session override while an absent field still means "product
default". An absent object means the advisor is off.

```json
{
  "advisor": {
    "enable": true,
    "runInBackground": true,
    "triggers": {
      "afterSubagentTask": true,
      "beforeMerge": true,
      "beforePlanStep": false,
      "onLargeChanges": false,
      "sensitivePaths": true
    },
    "blocks": {
      "mergeOnP0": true,
      "mergeOnP1": false,
      "taskCompletionOnP0": true,
      "planStepCompletionOnP0": true,
      "requireConfirmationOnP1": true,
      "allowProceedWithRisk": false
    },
    "thresholds": {
      "minChangedFiles": 0,
      "minChangedLines": 0,
      "sensitiveFilePatterns": [],
      "sensitiveDirPatterns": [],
      "maxDiffBytes": 262144,
      "cooldownSeconds": 60
    },
    "review": { "mode": "standard", "customPrompt": "" },
    "model": {
      "model": "", "thinkingEffort": "low",
      "maxTokens": 8000, "timeoutSeconds": 180, "maxFindings": 20
    },
    "display": { "statusBar": true, "hideP3": false, "groupBy": "severity" }
  }
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `enable` | off | Runs the advisor at all. Reviews cost model calls, so it is opt-in. |
| `runInBackground` | on | Keeps non-merge reviews off the agent's goroutine. A merge review is always synchronous. |
| `triggers.afterSubagentTask` | on | Review a finished background subagent task. |
| `triggers.beforeMerge` | on | Review the whole diff against base before a merge, PR/MR creation or push to base. |
| `triggers.beforePlanStep` | off | Review a plan step's diff before the step is recorded as done. |
| `triggers.onLargeChanges` | off | Review a change crossing `minChangedFiles` / `minChangedLines`. `0` disables that arm. Resolved by `advisor.TriggerForDiff`; Phase 1 wires no automatic caller, so nothing fires from it yet. |
| `triggers.sensitivePaths` | on | Review a change touching a sensitive pattern, however small. Same caveat as `onLargeChanges`, and it needs at least one pattern to match. |
| `blocks.mergeOnP0` / `mergeOnP1` | on / off | The lowest level that rejects a merge. |
| `blocks.taskCompletionOnP0` | on | A P0 stops a finished task from counting as clean. |
| `blocks.planStepCompletionOnP0` | on | A P0 stops a plan step from being recorded as done and pauses the plan. |
| `blocks.requireConfirmationOnP1` | on | A P1 is put to the human even where it does not block. |
| `blocks.allowProceedWithRisk` | off | Whether the human may continue past a block after confirming. Off makes a block final until the finding is fixed. |
| `thresholds.*Patterns` | empty | Glob-ish patterns matched against changed paths and directories. Empty means the sensitive-path trigger never fires. A project may add to the user list; it cannot remove from it. |
| `thresholds.maxDiffBytes` | 262144 | Diff text handed to one review; a larger change is truncated and flagged as such. Max 4 MiB. |
| `thresholds.cooldownSeconds` | 60 | Minimum gap between two reviews of the same target with the same diff hash. Max 3600. |
| `review.mode` | `standard` | `fast`, `standard` or `deep`. An unknown value is rejected by validation. |
| `review.customPrompt` | empty | Extra review criteria appended to the built-in rubric. |
| `model.model` | session model | `vendor/model` reference for the review, resolved like a reviewer model. |
| `model.thinkingEffort` | empty | Reasoning rung for the review model. |
| `model.maxTokens` / `timeoutSeconds` / `maxFindings` | 8000 / 180 / 20 | Per-review bounds. Caps: 64000 tokens, 1800 s, 100 findings. |
| `display.statusBar` | on | Draw the `advisor · <status>` badge. |
| `display.hideP3` | off | Filter minor findings out of the panel's default list. |
| `display.groupBy` | `severity` | `severity`, `file` or `task`. |

Set it in `/settings` → Advisor, which edits the user, project or this-session
level: user and project saves write the block to that `settings.json` (field-level
merge, so unset fields keep the other level's value), and a session save writes no
file — it pins the config in the transcript for this conversation. When another
level still supplies a value the save did not change, the notice says so.

The advisor is off when `enable` is absent or `false`, and it cannot review when
no model is connected — that is warned at startup and the triggers simply do
nothing. There is no environment kill switch; turning it off means writing the
setting. Merge detection is best-effort: a merge spelled in a way
`IsMergeCommand` does not recognize runs ungated, and the permission rules stay
the authority for it.

### `gitBar`

Optional plain-text Git row beneath the normal status line; defaults to `false`.
Set it in `/settings` → Appearance → STATUS BAR → Git information: Left / Right
previews Off / On; Enter saves to `~/.san/settings.json`. Project/local settings
can override this preference.

```text
  main · +2 ~3 ?1 · ↑1 ↓2 · github.com/owner/repo
```

- `+N`: staged paths; `~N`: unstaged modified paths; `?N`: untracked files;
  `!N`: conflicts. `↑N` / `↓N`: commits ahead / behind locally known tracking refs.
- Zero counts, `clean`, the `git` prefix and extra colors are omitted. Detached
  HEAD shows a short commit; narrow rows prioritize branch/status over repository.
- Repository labels prefer the tracking remote, then `origin`, then another
  remote; without remotes, the root name. Credentials, query/fragment data and
  local parent paths are omitted.

Reads are asynchronous (two-second timeout, three-second delay between polls),
local-only and never fetch. Workspace changes clear the snapshot and reread;
disabling cancels polling. The row hides outside a repository, without Git, or
on read failure.

### `skillCommands`

Whether the active skills are listed as slash commands in the `/` menu and its
autocomplete; defaults to `true`. Set it in `/settings` → Appearance → SLASH
MENU → Skills: Left / Right previews Off / On; Enter saves to
`~/.san/settings.json`. Project/local settings can override this preference.

It is a menu preference only: with it off, typing `/my-skill` still runs the
skill, and `/skills` still lists and toggles it. Use it when a large skill set
crowds the built-in commands out of the first screenful.

### `bashPreviewLines`

How many of a shell command's last output lines its row shows while it runs
(default `3`, at most `20`; `0` shows only the line count), cut at the
terminal width. Once it finishes, a successful command keeps an output that
fits in these lines and folds a longer one to its line count, since the model
reports what it found; a failed one keeps its last 20 lines, wrapped, whatever
this is set to. Ctrl-O shows every full output. Also set in `/settings` → Appearance → Shell output.

### `agentPreviewLines`

How many lines an Agent row previews (default `3`, clamped to `0`–`20`).
Running agents show their latest activity; finished agents preview the tail of
their result, with an omission row above longer output. Elapsed time follows the
Agent title. `0` hides the preview; expanded views still show the full content.
This preference is independent of `bashPreviewLines` and does not change the
compact background tracker. Set it in `/settings` → Appearance → AGENT OUTPUT →
Agent preview; Left / Right adjusts the count and Enter saves to
`~/.san/settings.json`. Changes affect subsequent rendering, not already-committed
native scrollback.

### `thinkingDisplay`

How the model's reasoning is drawn. Display only — it does not change what the
provider is asked to reason about (that is thinking effort, `/think`).

| Value | Live tail | Scrollback |
| --- | --- | --- |
| `full` (default) | the reasoning body | the reasoning body |
| `collapsed` | a `Thinking…` indicator | one `Thought for 3.2 s` line |
| `hidden` | nothing | nothing |

The collapsed duration uses seconds with a space before `s`: short durations
round to tenths (`0.3 s`), then switch to whole seconds at `10 s` (`12 s`).
An unmeasured duration displays just `Thought`.

An unset or unrecognized value resolves to `full`, so the preference is opt-in:
nothing about an existing transcript changes until you ask for it. Set
`collapsed` when a reasoning model's thinking buries the operations and the
answer you actually wanted — that is the whole point of the setting, since
committed scrollback is permanent and the noise cannot be cleaned up later.

Reasoning that is suppressed is never drawn in either surface: the value is read
by both the live view and the scrollback flush, so there is no path that leaks
the body.

Because committed scrollback is immutable ([ADR-0002](../design/decisions/0002-native-scrollback-commit-protocol.md)),
changing this takes effect from that point on: reasoning already on screen stays
as it was drawn. There is no `ctrl+o`-style expansion of a collapsed block for
the same reason.

Set it from `/settings` → Appearance → THINKING, or directly in
`~/.san/settings.json`.

## UI Interactions

- **`/tools`**: shows which tools are disabled via `disabledTools`. Some
  tools ship disabled — `Cron` and `Workflow`, for example; the full set is
  `defaultDisabledTools` in `internal/setting/loader.go`. An explicit
  `"Cron": false` entry — what the panel writes when you enable it —
  overrides the factory default.
- **Env vars**: injected into the Bash tool's environment automatically.
- **Theme**: applied at startup; no restart needed when changed via `/models` or similar commands.

## Automated Tests

```bash
go test ./internal/setting/... -v
```

Covered:

```
# Permission & rule matching
TestMatchRule                               — rule pattern matching (11 sub-tests)
TestBuildRule                               — rule building (5 sub-tests)
TestCheckPermission                         — permission checks (13+ sub-tests)
TestCheckPermissionWithReason               — permission with reason tracking
TestDenialTracking                          — denial fallback mechanism

# Config loading & merging
TestLoaderLoad                              — config file loading
TestConfig_LocalOverridesProject            — local.json overrides project
TestConfig_LocalOverridesProject_MergesNotReplaces — additive merge
TestConfig_UserLevelOverriddenByProject     — project overrides user

# Environment & tools
TestConfig_Env_InjectedIntoBashEnvironment  — env vars available in Bash
TestConfig_DisabledTools_HiddenFromModel    — disabled tools hidden from LLM

# Security
TestIsDestructiveCommand                    — dangerous command detection (13 sub-tests)
TestIsSensitivePath                         — sensitive path detection (13 sub-tests)
TestSensitivePathsRequireConfirmation       — sensitive paths need confirm
TestCheckBashSecurity                       — bash security checks (13 sub-tests)
TestBashSecurityRequiresConfirmation        — bash security needs confirm
TestDenyRulesPriorityOverSession            — deny rules override session
TestDestructiveCommandsRequireConfirmation  — destructive commands need confirm
TestWorkingDirectoryConstraint              — edits outside project root blocked

# Permission modes
TestBypassPermissionsMode                   — bypass permissions mode
TestDontAskMode                             — DontAsk mode
TestDenyRuleBlocksBypass                    — deny rules block bypass
TestSafeToolAllowlist                       — safe tool whitelist
TestPassthroughBehavior                     — passthrough behavior
TestCanApproveByHook                        — hook approval eligibility
TestOperationModeNext                       — operation mode cycling

# Suggestions
TestGenerateSuggestions_Bash                — bash rule suggestions
TestGenerateSuggestions_File                — file rule suggestions
TestGenerateSuggestions_Skill               — skill rule suggestions
TestSuggestBashRules_MaxLimit               — suggestion max limit
TestSuggestBashRules_DangerousFiltered      — dangerous rules filtered
TestSuggestBashRules_Dedup                  — deduplicated suggestions

# Path handling
TestIsInWorkingDirectory                    — working directory check
TestNormalizeMacOSPath                      — macOS path normalization
TestIsSubpath                               — subpath detection

# Bash AST
TestParseBashAST                            — bash command parsing
TestExtractCommandsAST                      — command extraction
TestExtractCommandsAST_Pipe                 — pipe command extraction
TestExtractCommandsAST_Redirect             — redirect extraction
TestExtractCommandsAST_PathStripping        — path stripping
TestCheckASTSecurity                        — AST security checks
TestCheckASTSecurity_ExcessiveCommands      — excessive command blocking
```

Cases to add:

```go
func TestConfig_ManagedSettings_ReadOnly(t *testing.T) {
    // managed-settings.json values must not be overridden by user settings
}

func TestConfig_CLIArgs_OverrideAll(t *testing.T) {
    // CLI arguments must override all file-based settings
}

func TestConfig_AllEightPriorities(t *testing.T) {
    // All 8 priority levels loaded simultaneously with correct precedence
}

func TestConfig_MalformedJSON_Error(t *testing.T) {
    // Malformed JSON in settings must produce a descriptive error
}

func TestConfig_Theme_AppliedAtStartup(t *testing.T) {
    // Theme setting must be applied when the TUI starts
}

func TestConfig_EnabledPlugins_ActivatesPlugin(t *testing.T) {
    // enabledPlugins: true must activate the plugin's components
}

```

## Interactive Tests (tmux)

```bash
mkdir -p /tmp/cfg_test/.san

# User-level env var (backup existing)
cp ~/.san/settings.json ~/.san/settings.json.bak 2>/dev/null || true
cat > ~/.san/settings.json << 'EOF'
{"env": {"SCOPE": "user"}}
EOF

# Project-level overrides it + disables a tool
cat > /tmp/cfg_test/.san/settings.json << 'EOF'
{"env": {"SCOPE": "project"}, "disabledTools": {"WebSearch": true}}
EOF

tmux new-session -d -s t_cfg -x 220 -y 60
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 2

# Test 1: Verify env override (project wins over user)
tmux send-keys -t t_cfg 'run: echo $SCOPE' Enter
sleep 5
tmux capture-pane -t t_cfg -p
# Expected: output is "project" (project config wins)

# Test 2: Verify disabled tool
tmux send-keys -t t_cfg '/tools' Enter
sleep 2
tmux capture-pane -t t_cfg -p
# Expected: WebSearch shown as disabled

# Test 3: Local settings override project
tmux send-keys -t t_cfg C-c
cat > /tmp/cfg_test/.san/settings.local.json << 'EOF'
{"env": {"SCOPE": "local"}}
EOF
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 2
tmux send-keys -t t_cfg 'run: echo $SCOPE' Enter
sleep 5
tmux capture-pane -t t_cfg -p
# Expected: output is "local" (local overrides project)

# Test 4: Allow rule auto-approves
tmux send-keys -t t_cfg C-c
cat > /tmp/cfg_test/.san/settings.json << 'EOF'
{"permissions": {"allow": ["Bash(echo*)"]}}
EOF
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 2
tmux send-keys -t t_cfg 'run: echo auto-approved' Enter
sleep 5
tmux capture-pane -t t_cfg -p
# Expected: Bash runs without permission dialog

# Test 5: Deny rule blocks
tmux send-keys -t t_cfg C-c
cat > /tmp/cfg_test/.san/settings.json << 'EOF'
{"permissions": {"deny": ["Bash(rm*)"]}}
EOF
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 2
tmux send-keys -t t_cfg 'run: rm -f /tmp/test' Enter
sleep 5
tmux capture-pane -t t_cfg -p
# Expected: Bash blocked by deny rule

# Test 6: Hooks from config
tmux send-keys -t t_cfg C-c
cat > /tmp/cfg_test/.san/settings.json << 'EOF'
{
  "hooks": {
    "SessionStart": [{
      "hooks": [{"type": "command", "command": "echo cfg-hook >> /tmp/cfg_hook.txt"}]
    }]
  }
}
EOF
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 3
cat /tmp/cfg_hook.txt
# Expected: "cfg-hook"

tmux send-keys -t t_cfg C-c
tmux kill-session -t t_cfg
mv ~/.san/settings.json.bak ~/.san/settings.json 2>/dev/null || true
rm -rf /tmp/cfg_test /tmp/cfg_hook.txt
```
