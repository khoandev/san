package agent

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/tool"
)

// The parent needs findings and inspection pointers; usage and activity travel
// separately in Details, so the UI never has to parse the worker's prose. The
// route line sits between them: the model reads where its work actually went,
// while the pointers stay at the end.
func formatForegroundAgentResult(result *tool.AgentExecResult, routeLine string) string {
	output := result.Content
	if routeLine != "" {
		output += "\n\n" + routeLine
	}
	if result.Identity.ID != "" {
		output += fmt.Sprintf("\n\nTask ID: %s", result.Identity.ID)
	}
	if result.AgentID != "" {
		output += fmt.Sprintf("\n\nAgentID: %s", result.AgentID)
	}
	if result.OutputFile != "" {
		output += "\n\nTranscript: " + result.OutputFile
	}
	if line := restartLine(result); line != "" {
		output += "\n\n" + line
	}
	return output
}

// restartLine tells the caller how to go on from a failure that kept its work.
// A transient provider failure stops the run, not the task: the session and the
// steps behind the checkpoint are still there, so the parent restarts that task
// instead of redoing what it already did.
func restartLine(result *tool.AgentExecResult) string {
	if result.Success || !result.Restartable {
		return ""
	}
	taskID := strings.TrimSpace(result.TaskID)
	if taskID == "" {
		taskID = strings.TrimSpace(result.Identity.ID)
	}
	if taskID == "" {
		return ""
	}
	progress := "no completed tool call"
	switch {
	case result.ToolUses == 1:
		progress = "1 completed tool call"
	case result.ToolUses > 1:
		progress = fmt.Sprintf("%d completed tool calls", result.ToolUses)
	}
	return fmt.Sprintf("Restart available: the run stopped at its checkpoint after %s, and its session keeps that work. Continue it with SendMessage — to=%q, message=\"Resume from the checkpoint and finish the remaining work.\" Do not redo the completed steps and do not spawn a replacement worker.", progress, taskID)
}

// ranAgentName names the definition a call actually ran as. The executor's own
// report wins because it is frozen at launch; the pinned route answers next,
// and the name the caller wrote is the last resort — for a call routed under an
// invented label, that name is only the label.
func ranAgentName(resultName string, route tool.AgentRoute, callerName string) string {
	for _, name := range []string{resultName, route.Agent, route.Info.Name, callerName} {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return ""
}

// routeLine reports where a delegated call went, so the parent model can see
// the route it got and choose the next one itself. A call that was neither
// routed nor named runs exactly the default worker it asked for, and reporting
// that says nothing the caller does not already know, so it gets no line.
func routeLine(route tool.AgentRoute, callerName, ranName, model string) string {
	unnamed := strings.TrimSpace(callerName) == ""
	unrouted := strings.TrimSpace(route.Agent) == "" && strings.TrimSpace(route.Tier) == ""
	if ranName == "" || (unnamed && unrouted) {
		return ""
	}
	where := make([]string, 0, 2)
	if tier := strings.TrimSpace(route.Tier); tier != "" {
		where = append(where, tier)
	}
	if model = strings.TrimSpace(model); model != "" {
		where = append(where, model)
	}
	line := "ran as " + ranName
	if len(where) > 0 {
		line += " (" + strings.Join(where, ", ") + ")"
	}
	if label := strings.TrimSpace(route.Label); label != "" {
		line += fmt.Sprintf(" · %q", label)
	}
	return line
}
