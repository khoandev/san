package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

// fixtureRouter answers with one fixed decision and counts how often it was
// asked, so a test can tell "routed" from "asked and declined".
type fixtureRouter struct {
	decision RouteDecision
	ok       bool
	calls    int
}

func (r *fixtureRouter) Route(context.Context, RouteRequest) (RouteDecision, bool) {
	r.calls++
	return r.decision, r.ok
}

// routedRegistry holds the four bundled definitions by name, with the modes the
// real files declare, so routing can be checked against the boundary it must
// not cross.
func routedRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	if err := registry.InitStores(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	registry.Register(&AgentConfig{Name: "explore", Model: "@fast", PermissionMode: PermissionExplore})
	registry.Register(&AgentConfig{Name: "quick", Model: "@fast", PermissionMode: PermissionAcceptEdits})
	registry.Register(&AgentConfig{Name: "task", Model: "@standard", PermissionMode: PermissionAcceptEdits})
	registry.Register(&AgentConfig{Name: "reviewer", Model: "@deep", PermissionMode: PermissionExplore})
	return registry
}

func routedExecutor(t *testing.T, parent PermissionMode, router Router) *ExecutorAdapter {
	t.Helper()
	executor := NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
	executor.ConfigureOwner(nil, routedRegistry(t), nil, nil)
	executor.SetParentPermissionMode(func() PermissionMode { return parent })
	executor.SetRoleResolver(func(role string) RoleAssignment {
		return RoleAssignment{Provider: executor.provider, ModelID: role + "-model", Assigned: true}
	})
	adapter := NewExecutorAdapter(executor)
	adapter.SetRouter(router)
	return adapter
}

// TestRouteAgentNeverSendsAnEditDefinitionToAReadOnlySession is the boundary
// routing must not cross: a read-only posture delegates investigation, so a
// classifier that suggests an edit worker must not be able to substitute one.
func TestRouteAgentNeverSendsAnEditDefinitionToAReadOnlySession(t *testing.T) {
	router := &fixtureRouter{decision: RouteDecision{Tier: "fast", Agent: "quick"}, ok: true}
	adapter := routedExecutor(t, PermissionExplore, router)

	if decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Prompt: "rename the field"}); routed {
		t.Fatalf("read-only session routed an unnamed call to %+v", decision)
	}
	if router.calls != 0 {
		t.Fatalf("read-only session classified a brief %d times; classification could only offer an edit definition", router.calls)
	}

	// An explicit tier still reaches the run, but only as a rung: the posture
	// keeps its read-only definition.
	decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Tier: "deep", Prompt: "map the plan store"})
	if !routed || decision.Tier != "deep" || decision.Agent != "" {
		t.Fatalf("tiered read-only decision = %+v, routed=%v; want the deep rung and no substitution", decision, routed)
	}
	config, ok := adapter.Executor.resolveAgentConfigIn("", PermissionExplore)
	if !ok || config.Name != "explore" {
		t.Fatalf("read-only unnamed call would run %+v, want explore", config)
	}
	// A router that names an edit definition directly is refused as well.
	if got := adapter.Executor.routableDefinition("quick", PermissionExplore); got != "" {
		t.Fatalf("routableDefinition(quick) in a read-only session = %q, want no substitution", got)
	}
	if got := adapter.Executor.routableDefinition("reviewer", PermissionExplore); got != "reviewer" {
		t.Fatalf("routableDefinition(reviewer) = %q, want the read-only definition", got)
	}
}

// TestRouteAgentMapsTheRungToAWorker pins what routing buys: a rung becomes a
// definition and a model, and the model approval shows is the model that runs.
func TestRouteAgentMapsTheRungToAWorker(t *testing.T) {
	for _, tc := range []struct {
		tier, agent, model string
	}{
		{"fast", "quick", "fast-model"},
		{"standard", "task", "standard-model"},
		{"deep", "task", "deep-model"},
	} {
		t.Run(tc.tier, func(t *testing.T) {
			router := &fixtureRouter{decision: RouteDecision{Tier: tc.tier}, ok: true}
			adapter := routedExecutor(t, PermissionDefault, router)

			decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Prompt: "brief"})
			if !routed {
				t.Fatal("the call was not routed")
			}
			if decision.Tier != tc.tier || decision.Agent != tc.agent || decision.Info.Model != tc.model {
				t.Fatalf("decision = %+v, want %s/%s on %s", decision, tc.tier, tc.agent, tc.model)
			}
			// An unnamed call keeps the posture it would have inherited.
			if decision.Info.PermissionMode != string(PermissionDefault) {
				t.Fatalf("approval mode = %q, want the inherited default posture", decision.Info.PermissionMode)
			}

			// Execution follows the same rung, from the pinned decision alone.
			run, err := adapter.Executor.prepareRun(context.Background(), tool.AgentExecRequest{
				Prompt: "brief", Route: decision, Tier: decision.Tier,
			})
			if err != nil {
				t.Fatal(err)
			}
			if run.cfg.modelID != tc.model {
				t.Fatalf("run model = %q, want %s", run.cfg.modelID, tc.model)
			}
			if run.cfg.config.Name != tc.agent {
				t.Fatalf("run definition = %q, want %s", run.cfg.config.Name, tc.agent)
			}
			if run.cfg.permMode != PermissionDefault {
				t.Fatalf("run mode = %q, want the inherited default posture", run.cfg.permMode)
			}
		})
	}
}

// TestRouterVerdictFallsBackToStandard keeps classification an optimization: a
// verdict nobody can act on routes at the safe rung instead of returning the
// call to the session model.
func TestRouterVerdictFallsBackToStandard(t *testing.T) {
	router := &fixtureRouter{decision: RouteDecision{Tier: "enormous"}, ok: true}
	adapter := routedExecutor(t, PermissionDefault, router)

	decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Prompt: "brief"})
	if !routed || decision.Tier != string(TierStandard) || decision.Agent != "task" {
		t.Fatalf("unusable verdict = %+v, routed=%v; want the standard rung", decision, routed)
	}
}

// TestRouterDecliningLeavesTheCallAlone pins the opt-out: a host with no
// opinion must not move the call, and a named call never asks at all.
func TestRouterDecliningLeavesTheCallAlone(t *testing.T) {
	router := &fixtureRouter{ok: false}
	adapter := routedExecutor(t, PermissionDefault, router)

	if decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Prompt: "brief"}); routed {
		t.Fatalf("a declining router produced a decision: %+v", decision)
	}
	if decision, _ := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Name: "explore", Prompt: "brief"}); decision.Tier != "" || decision.Agent != "" {
		t.Fatalf("a named call was routed: %+v", decision)
	}
	if router.calls != 1 {
		t.Fatalf("router consulted %d times; only the unnamed call may be classified", router.calls)
	}
}

// TestExplicitTierBeatsTheDefinitionsOwnRung covers the precedence the schema
// promises: a caller that asks for a depth gets that depth, while the definition
// still supplies the charter, tools and posture.
func TestExplicitTierBeatsTheDefinitionsOwnRung(t *testing.T) {
	executor := NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
	executor.SetRoleResolver(func(role string) RoleAssignment {
		return RoleAssignment{Provider: executor.provider, ModelID: role + "-model", Assigned: true}
	})
	config := &AgentConfig{Name: "task", Model: "@standard", PermissionMode: PermissionAcceptEdits}

	run, err := executor.prepareRunConfig(context.Background(), tool.AgentExecRequest{
		Agent: "task", Prompt: "brief", Tier: "fast", ResolvedAgentConfig: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.modelID != "fast-model" {
		t.Fatalf("tiered run model = %q, want the fast rung", run.modelID)
	}
	if run.permMode != PermissionAcceptEdits {
		t.Fatalf("tier changed the posture: %q", run.permMode)
	}

	plain, err := executor.prepareRunConfig(context.Background(), tool.AgentExecRequest{
		Agent: "task", Prompt: "brief", ResolvedAgentConfig: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.modelID != "standard-model" {
		t.Fatalf("untiered run model = %q, want the definition's own rung", plain.modelID)
	}
}

// TestContinuationKeepsItsApprovedRung pins continuation: a follow-up message
// continues the run it was started with, so a new tier on the request cannot
// move the model underneath it.
func TestContinuationKeepsItsApprovedRung(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	router := &fixtureRouter{decision: RouteDecision{Tier: "deep"}, ok: true}
	executor := NewExecutor(continuationProvider{aitest.New(aitest.Says("done"))}, t.TempDir(), "aitest", nil)
	executor.SetSessionStore(store, "parent-tier-test")
	executor.SetParentPermissionMode(func() PermissionMode { return PermissionDefault })
	executor.SetRoleResolver(func(role string) RoleAssignment {
		return RoleAssignment{Provider: executor.provider, ModelID: role + "-model", Assigned: true}
	})
	NewExecutorAdapter(executor).SetRouter(router)

	first, err := executor.RunBackground(tool.AgentExecRequest{Prompt: "first pass", Tier: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.WaitForCompletion(5 * time.Second) {
		t.Fatal("first execution hung")
	}
	state, ok := first.LaunchState().(*continuationState)
	if !ok || state.cfg == nil || state.cfg.modelID != "fast-model" {
		t.Fatalf("first run state = %#v, want the fast rung", state)
	}

	second, err := executor.RunBackground(tool.AgentExecRequest{
		ResumeTaskID: first.GetID(), Prompt: "second pass", Tier: "deep",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.WaitForCompletion(5 * time.Second) {
		t.Fatal("continuation hung")
	}
	continued := second.LaunchState().(*continuationState)
	if continued.cfg.modelID != "fast-model" || continued.cfg.config.Name != state.cfg.config.Name {
		t.Fatalf("continuation ran %q on %q, want the approved %q on %q",
			continued.cfg.config.Name, continued.cfg.modelID, state.cfg.config.Name, state.cfg.modelID)
	}
	if router.calls != 0 {
		t.Fatalf("continuation classified its brief again (%d calls)", router.calls)
	}
}
