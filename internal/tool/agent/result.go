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
	return output
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
