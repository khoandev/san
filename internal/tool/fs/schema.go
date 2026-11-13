package fs

import (
	"fmt"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/tool"
)

// Schema returns the model-facing tool definition for Read. The limits and
// the truncation marker are formatted from the same constants read.go
// enforces, so the model's instructions can't drift from the behavior.
func (t *ReadTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Read",
		Description: fmt.Sprintf(`Reads a file from the local filesystem.

- Prefer relative paths for files inside the session working directory; absolute for targets outside it
- Reads up to %d lines from the start by default; when you already know which part of a large file you need, read just that part with offset/limit
- Each body line is "N|content" — e.g. "42|total := 0". The "N|" prefix is not part of the file: quote only the content in an Edit payload, and the prefix is stripped for you
- Lines over %d characters end with “%s”; a line cut short was not shown in full, so do not quote it in an Edit payload until you read it in smaller pieces
- Do not re-read a file to verify your own Edit/Write — a failed change errors, and successful results keep your view current
- Image files are recognized but cannot be displayed yet; ask the user to attach the image to a message instead`, maxReadLines, maxLineLen, lineTruncationMarker),
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to read. Relative paths are resolved from the current session working directory.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "The line number to start reading from (1-based). Only provide if the file is too large to read at once.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "The number of lines to read. Only provide if the file is too large to read at once.",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

// Schema returns the model-facing tool definition for Edit: an anchored sloppy
// patch. The format is spelled out because a payload the model cannot write
// from memory costs a failed round-trip per novel mistake, and because the
// anti-patterns below are the ones real payloads hit.
func (t *EditTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Edit",
		Description: `Edits one file with an anchored patch: quote current text under *** SM:FIND, then replace it under *** SM:PUT, insert after it under *** SM:AFTER, or rewrite fragments in place with ⟪old│new⟫. Elide unchanged runs with ….

- *** SM:EDIT relative/path opens the file. One call edits one file; every operation applies atomically, so a payload that fails writes nothing.
- Copy the anchor verbatim from the latest Read output — not from memory, diffs, or summaries. Read rows are "N|content": quote the content, not the "N|" prefix. The anchor must match the file exactly once; on ambiguity add surrounding context, never retry the bare line.
- *** SM:PUT states the complete final text that replaces the whole match; an empty body deletes it. *** SM:AFTER keeps the match and inserts its body after the last matched line. A FIND body carrying ⟪old│new⟫ selections needs no action header: each selection rewrites old to new in place, with exactly one "│" divider. Omitting an action without selections is an error.
- Anchors address the file as you read it, never as this payload rewrites it: an anchor cannot match text an earlier operation in the same payload wrote. Plan every anchor against the current file.
- Headers stand alone; bodies are raw lines until the next header or EOF, with no closing delimiter. Never use diff prefixes (+/-/space) or @@ hunks.
- In FIND, … captures omitted text: a gap with content after it on its line stays on that line; a gap at line end spans lines. In PUT, each … re-emits the next capture in order. A whole-line … with no capture is an error — type those lines out.
- PUT and AFTER indentation is written verbatim. A failure applies nothing: a match failure returns a copy-ready payload to resend, a syntax error returns error[SMxxx] with the payload line and the fix.
- Requires a current view: Read first, unless a successful Write/Edit already observed the file this session. For a new file or a whole-file rewrite use Write.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"payload": map[string]any{
					"type": "string",
					"description": "The anchored patch: *** SM:EDIT <path> / *** SM:FIND <current text> / " +
						"*** SM:PUT <new text> or *** SM:AFTER <inserted text>. Example: " +
						"*** SM:EDIT src/app.go\n*** SM:FIND\nconst timeout = 1000\n*** SM:PUT\nconst timeout = 5000",
				},
			},
			"required": []string{"payload"},
		},
	}
}

// Schema returns the model-facing tool definition for Write.
func (t *WriteTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Write",
		Description: `Writes a file to the local filesystem, overwriting any existing content.

- To overwrite, Read the file first unless successful Write/Edit already observed it this session; re-read after external changes.
- Use Edit for every existing-file change; reserve Write for new files or wholesale regeneration.
- Create documentation only when requested or required by the task or repository instructions.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to write. Relative paths are resolved from the current session working directory.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The content to write to the file",
				},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

var _ tool.ShellDirectoryAwareTool = (*ShellTool)(nil)

// Schema describes the selected interpreter with the host's default cwd behavior.
func (t *ShellTool) Schema() core.ToolSchema {
	return t.SchemaWithDirectoryHelp("")
}

// SchemaWithDirectoryHelp keeps host-specific guidance local to this schema.
func (t *ShellTool) SchemaWithDirectoryHelp(help string) core.ToolSchema {
	if help == "" {
		help = "A successful cd updates the session working directory"
	}
	name := t.Name()
	shell, err := t.resolve()
	if err != nil {
		shell = proc.Shell{Kind: proc.ShellBash}
	}
	description := fmt.Sprintf(unixShellDescription, shell, help)
	if name == tool.ToolPowerShell {
		description = fmt.Sprintf(powerShellDescription, shell, help)
	} else if shell.Kind == proc.ShellZsh {
		description += "\n\nUses zsh -f (no user startup files) and inherited environment. Complex/native expansions require human confirmation. Tool/rule name stays Bash."
	}
	// The second shell is only in play once the user turns it on, so the
	// division of labour is written there and never names a missing tool.
	if t.second {
		description += "\n\n" + secondShellNote(name)
	}
	description += "\n\nRun in the foreground by default. Use background_job only when requested or when you have an independent next step. Use background_service for persistent servers and verify readiness. Services stop when San exits, not on session changes; any self-exit (even code 0) is a failure."
	return core.ToolSchema{
		Name:        name,
		Description: description,
		Definition:  shellParameters(),
	}
}

const unixShellDescription = `Executes a %s command and returns its output.

- Starts in the session cwd; do not prepend cd to it. %s; other shell state does not persist.
- Use for discovery (rg/find/ls); bound large output with head/wc. Use Read/Edit/Write for file contents.
- No TTY or interactive stdin: use non-interactive flags or heredocs. Read-only commands may run without approval.
- Background processes detach; results include the process-group ID and stop commands.`

const powerShellDescription = `Executes a %s command and returns its output.

- Use PowerShell syntax: $env:NAME and backtick escaping. On 5.1, no && or ||; use ";" or "if ($?) { ... }".
- Starts in the session cwd; do not prepend Set-Location to it. %s; other shell state does not persist.
- Use for discovery (Get-ChildItem/Select-String/rg); limit output with Select-Object -First. Use Read/Edit/Write for file contents.
- No interactive console: use non-interactive flags. Simple read-only pipelines may run without approval.`

func shellParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The command to execute",
			},
			"description": map[string]any{
				"type":        []string{"string", "null"},
				"description": "Short action label for approvals/tasks; omit or null to show the command.",
			},
			"timeout_ms": map[string]any{
				"type":        []string{"integer", "null"},
				"minimum":     1,
				"maximum":     maxShellTimeoutMS,
				"description": fmt.Sprintf("Execution deadline in ms (default %d, max %d), not a backgrounding threshold. Omit or null for background_service.", defaultShellTimeoutMS, maxShellTimeoutMS),
			},
			"execution_mode": map[string]any{
				"type":        []string{"string", "null"},
				"enum":        []any{shellForeground, shellBackgroundJob, shellBackgroundService, nil},
				"description": "foreground (default/null) waits for completion; background_job returns a task ID immediately; background_service runs a managed service without a deadline.",
			},
		},
		"required": []string{"command"},
	}
}

// secondShellNote divides work between the two Windows shells.
func secondShellNote(name string) string {
	if name == tool.ToolPowerShell {
		return "Bash (Git for Windows) is also available for POSIX tools/scripts; use PowerShell for Windows-specific work."
	}
	return "PowerShell is also available for Windows-specific work; use Bash for POSIX tools/scripts."
}
