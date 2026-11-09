package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
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
//
// The tool still offers every call to the host, because only the host knows
// which names are definitions; a name it recognizes must come back unrouted.
func TestCallerDecisionSkipsTheRouter(t *testing.T) {
	executor := routedExecutor()
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	named := map[string]any{"name": "explore", "prompt": "map the store"}
	if _, err := agentTool.PreparePermission(context.Background(), named, "."); err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 || executor.requests[0].Name != "explore" || executor.requests[0].Tier != "" {
		t.Fatalf("router requests = %+v, want one request carrying the name", executor.requests)
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
	if pinned, _ := pinnedRoute(named); pinned.Tier != "" || pinned.Agent != "" || pinned.Label != "" {
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

// TestUnrecognizedNameIsRoutedAsALabel pins what an invented name buys: the
// host classifies the call like an unnamed one, the definition it chose is what
// runs, and the name survives only as the label the work is reported under.
func TestUnrecognizedNameIsRoutedAsALabel(t *testing.T) {
	executor := routedExecutor(tool.AgentRoute{
		Tier:  "fast",
		Agent: "explore",
		Label: "fix-parser",
		Info:  tool.AgentConfigInfo{Name: "explore", Model: "fast-model", PermissionMode: "explore"},
		Raw:   "explore-config",
	})
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)
	params := map[string]any{"name": "fix-parser", "prompt": "find where the parser lives"}

	permission, err := agentTool.PreparePermission(context.Background(), params, ".")
	if err != nil {
		t.Fatal(err)
	}
	if got := permission.AgentMeta.AgentName; got != "explore" {
		t.Fatalf("approval shows %q, want the routed definition", got)
	}
	if len(executor.requests) != 1 || executor.requests[0].Name != "fix-parser" {
		t.Fatalf("router requests = %+v, want the label offered for classification", executor.requests)
	}
	pinned, ok := pinnedRoute(params)
	if !ok || pinned.Agent != "explore" || pinned.Label != "fix-parser" {
		t.Fatalf("pinned route = %+v, pinned=%v; want explore under the label", pinned, ok)
	}

	if result := agentTool.ExecuteApproved(context.Background(), params, "."); !result.Success {
		t.Fatalf("execution failed: %+v", result)
	}
	// The label keeps naming the work; the route keeps naming the definition.
	if got := executor.runReq; got.Agent != "fix-parser" || got.Route.Agent != "explore" || got.Route.Label != "fix-parser" {
		t.Fatalf("execution ran %+v, want explore under label fix-parser", got)
	}
	if executor.runReq.ResolvedAgentConfig != "explore-config" {
		t.Fatalf("execution lost the approved configuration: %+v", executor.runReq.ResolvedAgentConfig)
	}
}

// TestRoutedCallKeepsTheCallerNameForPosture pins the identity the executor
// uses to decide posture: a call the model left unnamed stays unnamed, so it
// inherits the session posture instead of adopting the definition's own edit
// policy. A labelled call keeps its label instead, and the route decides what
// runs.
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

// TestRoutedCallReportsWhereItRan pins what the parent model reads back: a call
// it left unnamed names the definition san chose and the rung it ran at, so the
// next delegation needs no classifier.
func TestRoutedCallReportsWhereItRan(t *testing.T) {
	executor := routedExecutor(tool.AgentRoute{
		Tier:  "fast",
		Agent: "explore",
		Info:  tool.AgentConfigInfo{Name: "explore", Model: "fast-model", PermissionMode: "explore"},
		Raw:   "explore-config",
	})
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	result := agentTool.Execute(context.Background(), map[string]any{"prompt": "map the store"}, ".")
	if !result.Success {
		t.Fatalf("execution failed: %+v", result)
	}
	if !strings.Contains(result.Output, "ran as explore (fast") {
		t.Fatalf("model cannot see the route it got: %q", result.Output)
	}
	if !strings.Contains(result.Metadata.Subtitle, "explore") {
		t.Fatalf("subtitle lost the definition that ran: %q", result.Metadata.Subtitle)
	}
	details, ok := result.Details.(toolresult.AgentDetails)
	if !ok || details.AgentName != "explore" || details.Tier != "fast" {
		t.Fatalf("details = %+v, want the ran definition and its rung", result.Details)
	}
}

// TestRoutedCallReportsTheLabelItKept pins that an invented name stays visible
// as the label of the work while the line still names the definition that ran.
func TestRoutedCallReportsTheLabelItKept(t *testing.T) {
	executor := routedExecutor(tool.AgentRoute{
		Tier:  "fast",
		Agent: "explore",
		Label: "fix-parser",
		Info:  tool.AgentConfigInfo{Name: "explore", Model: "fast-model", PermissionMode: "explore"},
		Raw:   "explore-config",
	})
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	result := agentTool.Execute(context.Background(), map[string]any{"name": "fix-parser", "prompt": "find the parser"}, ".")
	if !result.Success {
		t.Fatalf("execution failed: %+v", result)
	}
	if !strings.Contains(result.Output, `ran as explore (fast, fast-model) · "fix-parser"`) {
		t.Fatalf("route line = %q, want the definition under the caller's label", result.Output)
	}
}

// TestBackgroundLaunchReportsWhereItRan covers the launch path: the model reads
// the same route line even though the work has not produced content yet.
func TestBackgroundLaunchReportsWhereItRan(t *testing.T) {
	executor := routedExecutor(tool.AgentRoute{
		Tier:  "standard",
		Agent: "task",
		Info:  tool.AgentConfigInfo{Name: "task", Model: "standard-model", PermissionMode: "default"},
		Raw:   "task-config",
	})
	agentTool := NewAgentTool()
	agentTool.SetExecutor(executor)

	result := agentTool.Execute(context.Background(), map[string]any{"prompt": "fix the parser", "run_in_background": true}, ".")
	if !result.Success {
		t.Fatalf("launch failed: %+v", result)
	}
	if !strings.Contains(result.Output, "ran as task (standard, standard-model)") {
		t.Fatalf("background launch hid the route: %q", result.Output)
	}
	details, ok := result.Details.(toolresult.AgentDetails)
	if !ok || details.AgentName != "task" || details.Tier != "standard" {
		t.Fatalf("details = %+v, want the ran definition and its rung", result.Details)
	}
}
