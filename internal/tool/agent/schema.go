package agent

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
)

// tierNames is the depth vocabulary in one place: the schema advertises exactly
// these, and the tool validates a call against the same list, so a call cannot
// ask for a depth the model never saw.
var tierNames = []string{"fast", "standard", "deep"}

// parseTier normalizes the tier a call asked for. An unknown label is an error
// rather than a silent fallback: a typo would otherwise spend the session model
// on work the caller meant to route, with nothing reporting the mistake.
func parseTier(raw string) (string, error) {
	tier := strings.ToLower(strings.TrimSpace(raw))
	if tier == "" {
		return "", nil
	}
	for _, name := range tierNames {
		if tier == name {
			return tier, nil
		}
	}
	return "", fmt.Errorf("unknown tier %q: must be fast, standard, or deep", raw)
}

var _ tool.AgentCatalogAwareTool = (*AgentTool)(nil)

// Schema returns the single model-facing Agent tool definition.
func (t *AgentTool) Schema() core.ToolSchema {
	return agentSchema("")
}

// SchemaWithAgentCatalog adds predefined-agent discovery metadata.
func (t *AgentTool) SchemaWithAgentCatalog(agentCatalog string) core.ToolSchema {
	return agentSchema(agentCatalog)
}

// ReadOnlyDelegation reports whether the Agent call described by args would run
// a definition that cannot write, which is what Plan Mode is willing to
// delegate. An omitted name is the default worker: a read-only session resolves
// that to the bundled explore definition, so it counts as read-only. A named
// call is read-only only when the registry vouches for it — and so is a call
// approval already routed, which is judged by the definition it will run rather
// than the name it omitted.
func ReadOnlyDelegation(args map[string]any, isReadOnly func(name string) bool) bool {
	name := strings.TrimSpace(tool.GetString(args, "name"))
	if name == "" {
		if route, pinned := pinnedRoute(args); pinned {
			name = strings.TrimSpace(route.Agent)
		}
	}
	if name == "" {
		return true
	}
	return isReadOnly != nil && isReadOnly(name)
}

func agentSchema(agentCatalog string) core.ToolSchema {
	var sb strings.Builder
	sb.WriteString("Delegate independently scoped, self-contained work: investigate what you do not know, hand off a bounded change, ask for a check on what exists. Do it yourself when it is one step or needs the conversation's context.\n\n")
	if catalog := strings.TrimSpace(agentCatalog); catalog != "" {
		sb.WriteString(catalog)
		sb.WriteString("\n\nRoute by intent:\n")
		sb.WriteString("- a question, or where code lives → explore\n")
		sb.WriteString("- a fix with a definition of done → task\n")
		sb.WriteString("- one small edit already spelled out → quick\n")
		sb.WriteString("- checking an existing diff or design → reviewer\n")
		sb.WriteString("Name an entry; other names are labels. Findings never go to task; a read-only definition never edits. Omit name and tier for routine work; san routes it.\n\n")
	}
	sb.WriteString("Brief by tier:\n")
	sb.WriteString("- fast: one sentence of goal and the command that proves it.\n")
	sb.WriteString("- standard: the goal, the paths involved, and the definition of done.\n")
	sb.WriteString("- deep: the goal, exact files, constraints, acceptance criteria, risks.\n\n")
	sb.WriteString("Brief with only task-local context: goal, paths, constraints, known findings, the validation plan. Report a failed tool and its reason instead of retrying a denied command.\n\n")
	sb.WriteString("Launch independent tasks concurrently: foreground when you need the result to proceed, background when you can continue useful work. Do not duplicate delegated work, and verify the result before reporting it. SendMessage to the returned Task ID continues the same session, model and tier. Check uncertain side effects before retrying failed work.")

	return core.ToolSchema{
		Name:        "Agent",
		Description: sb.String(),
		Definition:  agentToolParameters,
	}
}

var agentToolParameters = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"prompt": map[string]any{
			"type":        "string",
			"description": "A self-contained task brief with the goal, relevant paths, constraints, known findings, and validation plan. Its depth should match the tier.",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "A short (3-5 word) description of the task",
		},
		"name": map[string]any{
			"type":        "string",
			"description": "Optional. A name the catalog lists selects that definition. Any other name only labels the task, and san still routes the call on its brief.",
		},
		"tier": map[string]any{
			"type":        "string",
			"enum":        tierNames,
			"description": "Optional depth: fast is one small change or lookup, deep is work where extra latency pays. Omit it to let san route the call; a named definition supplies its own tier.",
		},
		"run_in_background": map[string]any{
			"type":        "boolean",
			"description": "Defaults to false for new work: foreground calls in the same batch run concurrently, and the batch waits for all results before your next inference. Set true when you can continue useful independent work without this result; background completion will be notified separately.",
		},
		"max_steps": map[string]any{
			"type":        "number",
			"description": "Maximum number of LLM inference steps. A positive value overrides the execution budget. New work defaults to the selected agent's configured budget (500 unless configured otherwise).",
		},
	},
	"required": []string{"description", "prompt"},
}

// Schema returns the model-facing tool definition for AgentStop.
func (t *AgentStopTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: t.Name(),
		Description: `Stop a running or idle agent. Later messages cannot restart it.

Only use the exact task_id returned when that agent was started. This tool cannot stop background Bash commands; use the process-group command reported by Bash instead.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The exact Task ID of the agent to stop.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional concise reason for stopping the agent.",
				},
			},
			"required": []string{"task_id"},
		},
	}
}

// Schema returns the model-facing tool definition for SendMessage.
func (t *SendMessageTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "SendMessage",
		Description: `Send a message to an agent's exact Task ID, "main", or a group member's name.
- Running agents queue messages between steps. Only the owning main conversation can continue an idle or recoverable failed agent, after permission checks. Continuation runs in the background with the original history, tools, model, permissions, and step budget.
- Stopped/killed agents, exhausted recovery budgets, missing history, and unknown IDs return errors; no replacement is created. Task IDs remain usable until process exit.
- Acceptance is not acknowledgment or completion. Verify the result before relying on it.
- Background Bash processes have no agent inbox; inspect their output and use shell tools to manage them.
- Group members appear in the <group> reminder. The result explains whether delivery waits for a step, user input, or session resume.
- Keep messages brief and self-contained. They are attributed to you, not the recipient's user. Report results or blockers when asked; skip greetings and bare acknowledgments.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type":        "string",
					"description": "An existing agent's exact task ID, \"main\", or a group member's name without \"@\".",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "Optional: one line, under ten words, on what the message is about. The recipient's user sees it; without it they see only who wrote.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "The message body: just what you have to say.",
				},
			},
			"required": []string{"to", "message"},
		},
	}
}
