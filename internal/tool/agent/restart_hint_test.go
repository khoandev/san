package agent

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// A run that stopped on a transient provider failure keeps its work, so the
// result the parent reads has to say so: the task id, the steps already done and
// the exact SendMessage that continues it. Without that line a recoverable stop
// reads like lost work.
func TestRestartHintNamesTaskProgressAndSendMessage(t *testing.T) {
	result := &tool.AgentExecResult{
		Content:     "partial findings",
		TaskID:      "worker-3",
		ToolUses:    2,
		Restartable: true,
		Identity:    toolresult.AgentIdentity{ID: "worker-3", SessionID: "child-session"},
	}

	output := formatForegroundAgentResult(result, "")
	for _, want := range []string{
		"partial findings",
		"Task ID: worker-3",
		"2 completed tool calls",
		"SendMessage",
		`to="worker-3"`,
		"Resume from the checkpoint",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("result is missing %q:\n%s", want, output)
		}
	}
}

// A failure with no checkpoint behind it has nothing to restart: the result must
// not promise a recovery the task cannot make.
func TestRestartHintStaysOutOfUnrecoverableRuns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *tool.AgentExecResult
	}{
		{"finished", &tool.AgentExecResult{Success: true, Content: "done", TaskID: "worker-3", ToolUses: 2}},
		{"not restartable", &tool.AgentExecResult{Error: "model_not_found", TaskID: "worker-3", ToolUses: 2}},
		{"no task", &tool.AgentExecResult{Error: "rate limit", Restartable: true, ToolUses: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if output := formatForegroundAgentResult(tc.result, ""); strings.Contains(output, "Restart available") {
				t.Fatalf("result promises a restart it cannot make:\n%s", output)
			}
		})
	}
}

// A restart names the task once, and it is the id a call continues: the
// checkpoint's work sits behind that address, not behind the session id.
func TestRestartHintFallsBackToTheTaskIdentity(t *testing.T) {
	output := restartLine(&tool.AgentExecResult{
		Error:       "rate limit",
		Restartable: true,
		ToolUses:    1,
		Identity:    toolresult.AgentIdentity{ID: "worker-4"},
	})
	if !strings.Contains(output, `to="worker-4"`) || !strings.Contains(output, "1 completed tool call") {
		t.Fatalf("restart line = %q", output)
	}
}
