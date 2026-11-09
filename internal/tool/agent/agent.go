package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/san/internal/tool/toolresult"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
)

// AgentTool spawns subagents to handle complex tasks.
// It implements PermissionAwareTool to require user confirmation.
type AgentTool struct {
	executor tool.AgentExecutor
}

// NewAgentTool creates a new AgentTool
func NewAgentTool() *AgentTool {
	return &AgentTool{}
}

func (t *AgentTool) Name() string { return "Agent" }
func (t *AgentTool) Description() string {
	return "Delegate an independently scoped task to a separate conversation"
}
func (t *AgentTool) Icon() string { return tool.IconAgent }

// RequiresPermission returns true - Agent always requires permission
func (t *AgentTool) RequiresPermission() bool {
	return true
}

// SetExecutor sets the agent executor
func (t *AgentTool) SetExecutor(executor tool.AgentExecutor) {
	t.executor = executor
}

// PreparePermission prepares a permission request with agent metadata
func (t *AgentTool) PreparePermission(ctx context.Context, params map[string]any, cwd string) (*perm.PermissionRequest, error) {
	agentName := tool.GetString(params, "name")
	tier, err := parseTier(tool.GetString(params, "tier"))
	if err != nil {
		return nil, err
	}

	prompt, err := tool.RequireString(params, "prompt")
	if err != nil {
		return nil, err
	}

	description := tool.GetString(params, "description")
	if description == "" {
		description = "Run agent task"
	}

	runBackground := tool.GetBool(params, "run_in_background")
	if err := validateAgentParams(params); err != nil {
		return nil, err
	}

	// Check if executor is configured
	if t.executor == nil {
		return nil, fmt.Errorf("agent executor not configured")
	}

	resume, _ := params["_continuation"].(continuationApproval)
	if resume.TaskID != "" {
		runBackground = true
	}

	// Resolve the agent definition and carry the exact config into execution so a
	// registry reload while approval is pending cannot change what was approved.
	// A continuation is pinned to the original worker policy by the executor and
	// is never routed again: the depth it was approved with is the depth it runs.
	var config tool.AgentConfigInfo
	var route tool.AgentRoute
	if resolver, supported := t.executor.(interface {
		ContinuationConfig(string) (tool.AgentConfigInfo, error)
	}); resume.TaskID != "" && supported {
		config, err = resolver.ContinuationConfig(resume.TaskID)
		if err != nil {
			return nil, err
		}
	} else {
		route = t.routeAgent(ctx, agentName, tier, prompt)
		resolvedConfig := route.Raw
		if route.Info.Name != "" {
			config = route.Info
		} else {
			selected, pinned, ok := t.executor.ResolveAgentSelection(agentName)
			if !ok {
				return nil, fmt.Errorf("unknown or disabled agent: %s", agentName)
			}
			config, resolvedConfig = selected, pinned
		}
		if resume.TaskID == "" {
			params["_resolvedAgentConfig"] = resolvedConfig
		} else {
			config.PermissionMode = resume.PermissionMode
		}
		// Pin what execution reads, even when it is "no route": a call that was
		// reviewed without routing must not be classified again at execution.
		params["_route"] = tool.AgentRoute{Tier: route.Tier, Agent: route.Agent}
	}

	// Determine effective model for permission display.
	effectiveModel := config.Model
	if effectiveModel == "inherit" {
		effectiveModel = ""
	}
	if effectiveModel == "" {
		effectiveModel = t.executor.ParentModelID()
	}
	if effectiveModel == "" {
		effectiveModel = "inherit"
	}

	// Build description
	displayName := displayAgentLabel(config.Name)
	desc := fmt.Sprintf("Spawn %s agent: %s", displayName, description)
	if resume.TaskID != "" {
		desc = fmt.Sprintf("Continue %s agent task %s in its original session: %s", displayName, resume.TaskID, description)
	}
	if runBackground {
		desc += " (background)"
	}

	return &perm.PermissionRequest{
		ID:          tool.GenerateRequestID(),
		ToolName:    t.Name(),
		Description: desc,
		AgentMeta: &perm.AgentMetadata{
			AgentName:      displayName,
			Description:    config.Description,
			Model:          effectiveModel,
			PermissionMode: config.PermissionMode,
			Tools:          config.Tools,
			Prompt:         prompt,
			Background:     runBackground,
		},
	}, nil
}

// routeAgent asks the host to route an Agent call. A host without a router, or
// a call that already decided (a named definition or an explicit tier), keeps
// the plain selection path: the returned route carries only the tier the caller
// asked for, so an explicit depth still reaches execution.
func (t *AgentTool) routeAgent(ctx context.Context, agentName, tier, prompt string) tool.AgentRoute {
	route := tool.AgentRoute{Tier: tier}
	// A named definition has decided already: it supplies its own charter, and
	// only an unnamed call can have a definition substituted for it.
	if strings.TrimSpace(agentName) != "" {
		return route
	}
	router, ok := t.executor.(tool.AgentRouter)
	if !ok {
		return route
	}
	decision, routed := router.RouteAgent(ctx, tool.AgentRouteRequest{Name: agentName, Tier: tier, Prompt: prompt})
	if !routed {
		return route
	}
	// The caller's rung is a decision, not a suggestion: a host may map it to a
	// definition, but it cannot quietly answer at a different depth.
	if tier != "" {
		decision.Tier = tier
	}
	return decision
}

// pinnedRoute returns the routing decision a call was approved with, and
// whether approval pinned one at all.
func pinnedRoute(params map[string]any) (tool.AgentRoute, bool) {
	value, ok := params["_route"]
	if !ok {
		return tool.AgentRoute{}, false
	}
	route, _ := value.(tool.AgentRoute)
	return route, true
}

// Reject removed selectors rather than silently widening an old read-only request.
func validateAgentParams(params map[string]any) error {
	if _, exists := params["resume_task_id"]; exists {
		return fmt.Errorf("Agent creates new sessions; use SendMessage with the existing task ID for follow-up work")
	}
	for _, field := range []string{"mode", "model"} {
		if _, exists := params[field]; exists {
			return fmt.Errorf("Agent does not accept %q; worker configuration comes from session settings or its definition", field)
		}
	}
	return nil
}

// ExecuteApproved executes the agent after user approval
func (t *AgentTool) ExecuteApproved(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	return t.execute(ctx, params, cwd)
}

// Execute implements the Tool interface
func (t *AgentTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	return t.execute(ctx, params, cwd)
}

// execute is the internal implementation. Subagents cannot reach this: the
// Agent tool is parent-only (excluded from every subagent's tool set), which
// is what keeps the model flat — main spawns workers, workers do not.
func (t *AgentTool) execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()

	agentName := tool.GetString(params, "name")
	tier, err := parseTier(tool.GetString(params, "tier"))
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	prompt := tool.GetString(params, "prompt")
	if prompt == "" {
		return toolresult.NewErrorResult(t.Name(), "prompt is required")
	}

	description := tool.GetString(params, "description")
	runBackground := tool.GetBool(params, "run_in_background")
	if err := validateAgentParams(params); err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	var onActivity tool.ActivityFunc
	if cb, ok := params["_onActivity"].(tool.ActivityFunc); ok {
		onActivity = cb
	}
	var onQuestion tool.AskQuestionFunc
	if cb, ok := params["_onQuestion"].(tool.AskQuestionFunc); ok {
		onQuestion = cb
	}

	maxSteps := tool.GetInt(params, "max_steps", 0)

	// Check executor
	if t.executor == nil {
		return toolresult.NewErrorResult(t.Name(), "agent executor not configured")
	}

	resolvedAgentConfig, _ := params["_resolvedAgentConfig"]
	// A call approved with a route runs that route verbatim. Bypass mode never
	// prepares a permission request, so a call without a pin is routed here,
	// once, by the same rule approval would have used.
	route, pinned := pinnedRoute(params)
	if !pinned {
		route = t.routeAgent(ctx, agentName, tier, prompt)
	}
	// New work receives a self-contained brief.
	req := tool.AgentExecRequest{
		ParentToolCallID:    core.ToolCallIDFromContext(ctx),
		Agent:               agentName,
		ResolvedAgentConfig: resolvedAgentConfig,
		Route:               route,
		Tier:                route.Tier,
		Prompt:              prompt,
		Description:         description,
		Background:          runBackground,
		MaxSteps:            maxSteps,
		OnIdentity: func(identity toolresult.AgentIdentity) {
			sdkagent.Report(ctx, sdkagent.Result{Details: toolresult.AgentDetails{Identity: identity}})
		},
		OnActivity: onActivity,
		OnQuestion: onQuestion,
	}

	// Handle background execution
	if runBackground {
		taskInfo, err := t.executor.RunBackground(req)
		if err != nil {
			return toolresult.NewErrorResult(t.Name(), fmt.Sprintf("failed to start background agent: %v", err))
		}

		output := fmt.Sprintf("Agent started in background.\nTask ID: %s\nDescription: %s", taskInfo.TaskID, description)
		duration := time.Since(start)
		return toolresult.ToolResult{
			Success: true,
			Details: toolresult.AgentDetails{TaskID: taskInfo.TaskID, Identity: taskInfo.Identity},
			Output:  output + tool.BackgroundLaunchSuffix,
			HookResponse: map[string]any{
				"backgroundTask": map[string]any{
					"taskId":      taskInfo.TaskID,
					"agentName":   agentName,
					"description": description,
					"outputFile":  taskInfo.OutputFile,
					"toolName":    t.Name(),
				},
			},
			Metadata: toolresult.ResultMetadata{
				Title:    t.Name(),
				Icon:     t.Icon(),
				Subtitle: fmt.Sprintf("[background] %s: %s", displayAgentLabel(agentName), taskInfo.TaskID),
				Duration: duration,
			},
		}
	}

	// Foreground execution
	result, err := t.executor.Run(ctx, req)
	if err != nil && result == nil {
		return toolresult.NewErrorResult(t.Name(), fmt.Sprintf("agent execution failed: %v", err))
	}
	if result == nil {
		return toolresult.NewErrorResult(t.Name(), "agent returned no result")
	}
	if err != nil {
		result.Success = false
		if result.Error == "" {
			result.Error = err.Error()
		}
	}

	duration := time.Since(start)

	displayName := displayAgentLabel(agentName)
	agentDuration := result.Duration
	if agentDuration == 0 {
		agentDuration = duration
	}
	status := "finished"
	if !result.Success {
		status = "failed"
	}
	return toolresult.ToolResult{
		Success:      result.Success,
		Output:       formatForegroundAgentResult(result),
		Error:        result.Error,
		HookResponse: buildAgentHookResponse(result, agentName, prompt),
		Details: toolresult.AgentDetails{
			Response: &result.Content,
			Identity: result.Identity,
			Model:    result.Model, ToolUses: result.ToolUses,
			InputTokens: result.TotalInputTokens, OutputTokens: result.TotalOutputTokens,
			Duration: agentDuration, Activity: result.Activity, Error: result.Error,
			Stopped: errors.Is(err, context.Canceled),
		},
		Metadata: toolresult.ResultMetadata{
			Title:    t.Name(),
			Icon:     t.Icon(),
			Subtitle: fmt.Sprintf("%s: %s (%d steps)", displayName, status, result.StepCount),
			Duration: duration,
		},
	}
}

func displayAgentLabel(name string) string {
	if name != "" {
		return name
	}
	return "Agent"
}

// buildAgentHookResponse creates a structured response for PostToolUse hooks.
func buildAgentHookResponse(result *tool.AgentExecResult, agentName, prompt string) map[string]any {
	status := "completed"
	if !result.Success {
		status = "error"
	}

	return map[string]any{
		"agentId":           result.AgentID,
		"agentName":         agentName,
		"outputFile":        result.OutputFile,
		"content":           result.Content,
		"status":            status,
		"prompt":            prompt,
		"totalDurationMs":   result.Duration.Milliseconds(),
		"totalToolUseCount": result.ToolUses,
		"usage": map[string]any{
			"total_input_tokens":  result.TotalInputTokens,
			"total_output_tokens": result.TotalOutputTokens,
		},
	}
}

func init() {
	tool.Register(NewAgentTool())
}
