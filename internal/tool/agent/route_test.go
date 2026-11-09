package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool"
)

// routingExecutor is a host that both selects a definition and classifies a
// call: the two halves the Agent tool must keep in step. Each RouteAgent call
// consumes one queued decision, so a test can change the answer between
// approval and execution and prove the pin wins.
type routingExecutor struct {
	recordingExecutor
	decisions []tool.AgentRoute
	requests  []tool.AgentRouteRequest
}

func (e *routingExecutor) RouteAgent(_ context.Context, req tool.AgentRouteRequest) (tool.AgentRoute, bool) {
	e.requests = append(e.requests, req)
	if len(e.decisions) == 0 {
		return tool.AgentRoute{}, false
	}
	decision := e.decisions[0]
	e.decisions = e.decisions[1:]
	return decision, true
}

func routedExecutor(decisions ...tool.AgentRoute) *routingExecutor {
	return &routingExecutor{recordingExecutor: recordingExecutor{configOK: true}, decisions: decisions}
}

// TestApprovalPinsTheRoutedDecision pins the promise of routing: the user
// approves a definition and a model, and that exact pair runs even if the
// router would now answer differently or the registry changed while the
// approval was pending.
func TestApprovalPinsTheRoutedDecision(t *testing.T) {
	executor := routedExecutor(
		tool.AgentRoute{
			Tier:  "deep",
			Agent: "task",
			Info:  tool.AgentConfigInfo{Name: "task", Model: "deep-model", PermissionMode: "default"},
			Raw:   "pinned-task-config",
		},
		// A second, different answer: execution must not consume it.
		tool.AgentRoute{Tier: "fast", Agent: "quick", Info: tool.AgentConfigInfo{Name: "quick"}, Raw: "other"},
	)
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)
	params := map[string]any{"prompt": "rename the field"}

	permission, err := agentTool.PreparePermission(context.Background(), params, ".")
	if err != nil {
		t.Fatal(err)
	}
	if permission.AgentMeta.AgentName != "task" || permission.AgentMeta.Model != "deep-model" {
		t.Fatalf("approval shows %+v, want the routed task on deep-model", permission.AgentMeta)
	}
	pinned, ok := pinnedRoute(params)
	if !ok || pinned.Tier != "deep" || pinned.Agent != "task" {
		t.Fatalf("pinned route = %+v, pinned=%v; want deep/task", pinned, ok)
	}
	if params["_resolvedAgentConfig"] != "pinned-task-config" {
		t.Fatalf("pinned config = %v, want the approved configuration", params["_resolvedAgentConfig"])
	}

	if result := agentTool.ExecuteApproved(context.Background(), params, "."); !result.Success {
		t.Fatalf("execution failed: %+v", result)
	}
	if got := executor.runReq; got.Tier != "deep" || got.Route.Agent != "task" || got.ResolvedAgentConfig != "pinned-task-config" {
		t.Fatalf("execution ran %+v, want the approved deep/task route", got)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("router consulted %d times, want once at approval", len(executor.requests))
	}
}

// TestBypassExecutionRoutesWithoutApproval covers the path that never prepares
// a permission request: the call is routed once at execution instead of
// running the session model because nothing asked.
func TestBypassExecutionRoutesWithoutApproval(t *testing.T) {
	executor := routedExecutor(tool.AgentRoute{
		Tier:  "fast",
		Agent: "quick",
		Info:  tool.AgentConfigInfo{Name: "quick", Model: "fast-model"},
		Raw:   "quick-config",
	})
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	result := agentTool.Execute(context.Background(), map[string]any{"prompt": "one small edit"}, ".")
	if !result.Success {
		t.Fatalf("execution failed: %+v", result)
	}
	if got := executor.runReq; got.Tier != "fast" || got.Route.Agent != "quick" {
		t.Fatalf("bypass run = %+v, want the fast/quick route", got)
	}
	if len(executor.requests) != 1 || executor.requests[0].Name != "" || executor.requests[0].Tier != "" {
		t.Fatalf("router requests = %+v, want one unnamed, tierless call", executor.requests)
	}
}

// TestCallerDecisionSkipsTheRouter keeps classification for calls that left it
// open: a named definition or an explicit depth has already decided, and
// spending a model call to re-decide would only be able to disagree.
func TestCallerDecisionSkipsTheRouter(t *testing.T) {
	executor := routedExecutor(tool.AgentRoute{Tier: "deep", Agent: "task"})
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	named := map[string]any{"name": "explore", "prompt": "map the store"}
	if _, err := agentTool.PreparePermission(context.Background(), named, "."); err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 0 {
		t.Fatalf("a named call asked for routing: %+v", executor.requests)
	}
	tierExecutor := routedExecutor()
	agentTool.SetExecutor(tierExecutor)
	tiered := map[string]any{"prompt": "one small edit", "tier": "fast"}
	if _, err := agentTool.PreparePermission(context.Background(), tiered, "."); err != nil {
		t.Fatal(err)
	}
	// A tier still names the rung, so the host may be asked to map it to a
	// definition — but the request carries the tier, which is what tells a
	// classifier there is nothing left to classify.
	if len(tierExecutor.requests) != 1 || tierExecutor.requests[0].Tier != "fast" || tierExecutor.requests[0].Name != "" {
		t.Fatalf("tiered call requests = %+v, want one tier-carrying request", tierExecutor.requests)
	}
	pinned, _ := pinnedRoute(tiered)
	if pinned.Tier != "fast" || pinned.Agent != "" {
		t.Fatalf("tiered route = %+v, want the caller's fast rung and no substitution", pinned)
	}
	if pinned, _ := pinnedRoute(named); pinned.Tier != "" || pinned.Agent != "" {
		t.Fatalf("named route = %+v, want no routing", pinned)
	}
	agentTool.ExecuteApproved(context.Background(), tiered, ".")
	if tierExecutor.runReq.Tier != "fast" {
		t.Fatalf("explicit tier did not reach execution: %+v", tierExecutor.runReq)
	}
}

func TestUnknownTierIsRejectedBeforeAnythingRuns(t *testing.T) {
	executor := routedExecutor()
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)
	params := map[string]any{"prompt": "do it", "tier": "enormous"}

	if _, err := agentTool.PreparePermission(context.Background(), params, "."); err == nil || !strings.Contains(err.Error(), "unknown tier") {
		t.Fatalf("approval accepted an unknown tier: %v", err)
	}
	result := agentTool.Execute(context.Background(), params, ".")
	if result.Success || !strings.Contains(result.Error, "unknown tier") {
		t.Fatalf("execution accepted an unknown tier: %+v", result)
	}
	if len(executor.requests) != 0 || executor.runReq.Prompt != "" {
		t.Fatalf("an invalid tier reached the executor: %+v", executor.runReq)
	}
}

// TestRoutedCallKeepsTheCallerNameForPosture pins the identity the executor
// uses to decide posture: a routed definition must not look like a call the
// model named, or an unnamed session call would silently adopt the definition's
// own edit policy.
func TestRoutedCallKeepsTheCallerNameForPosture(t *testing.T) {
	executor := routedExecutor(tool.AgentRoute{
		Tier:  "fast",
		Agent: "quick",
		Info:  tool.AgentConfigInfo{Name: "quick", Model: "fast-model", PermissionMode: "default"},
		Raw:   "quick-config",
	})
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)
	params := map[string]any{"prompt": "one small edit"}
	_, _ = agentTool.PreparePermission(context.Background(), params, ".")
	agentTool.ExecuteApproved(context.Background(), params, ".")

	if executor.runReq.Agent != "" {
		t.Fatalf("routed call reaches the executor as %q; it must stay unnamed", executor.runReq.Agent)
	}
	if executor.runReq.Route.Agent != "quick" {
		t.Fatalf("route lost its definition: %+v", executor.runReq.Route)
	}
}
