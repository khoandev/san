// Per-agent role overrides: the role /settings assigns to a built-in agent
// (agents.roles) instead of the role its definition file declares. The tests
// pin the precedence — the assignment outranks a per-call tier, an explicit
// model in the request still wins — and that a host without settings keeps
// every definition on its own role.
package subagent

import (
	"context"
	"testing"

	"github.com/genai-io/san/internal/tool"
)

// quickConfig is the bundled quick definition's shape as the registry holds it:
// it declares the fast role so a test can tell the declared rung from an
// assigned one.
func quickConfig(t *testing.T) *AgentConfig {
	t.Helper()
	config, ok, _ := routedRegistry(t).LookupAgent("quick")
	if !ok {
		t.Fatal("the fixture registry has no quick definition")
	}
	return config
}

// TestAssignedAgentRoleOutranksTheDefinition pins the assignment's effect: the
// work runs on the role the user picked, not the one the definition declares.
func TestAssignedAgentRoleOutranksTheDefinition(t *testing.T) {
	config := quickConfig(t)

	plain := NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
	if got := plain.routeModelRef(config, tool.AgentExecRequest{}); got != "@fast" {
		t.Fatalf("unaided reference = %q, want the declared @fast", got)
	}

	assigned := NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
	assigned.SetAgentRoleSource(func(name string) string {
		if name != "quick" {
			return ""
		}
		return "deep"
	})
	if got := assigned.routeModelRef(config, tool.AgentExecRequest{}); got != "@deep" {
		t.Fatalf("assigned reference = %q, want @deep", got)
	}

	// A hand-edited file may keep the "@" the definition's model field carries;
	// the executor accepts either spelling.
	prefixed := NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
	prefixed.SetAgentRoleSource(func(string) string { return "@deep" })
	if got := prefixed.routeModelRef(config, tool.AgentExecRequest{}); got != "@deep" {
		t.Fatalf("prefixed assignment = %q, want @deep", got)
	}
}

// TestAssignedAgentRoleOutranksACallTier keeps the assignment meaningful under
// automatic routing: a tier — including the one the brief classifier inferred —
// no longer moves the run off the rung the user picked. Without an assignment
// the tier keeps its old precedence.
func TestAssignedAgentRoleOutranksACallTier(t *testing.T) {
	config := quickConfig(t)
	req := tool.AgentExecRequest{Tier: "standard"}

	plain := NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
	if got := plain.routeModelRef(config, req); got != "@standard" {
		t.Fatalf("tiered reference = %q, want the call's @standard", got)
	}

	assigned := NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
	assigned.SetAgentRoleSource(func(string) string { return "deep" })
	if got := assigned.routeModelRef(config, req); got != "@deep" {
		t.Fatalf("assigned reference = %q, want the assignment to win", got)
	}
}

// TestAssignedAgentRoleSkipsTheDefaultWorker keeps the two knobs apart: the
// worker's model is subagentModel, and the display-only row that describes it
// is never overridden by an agent assignment.
func TestAssignedAgentRoleSkipsTheDefaultWorker(t *testing.T) {
	executor := NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
	executor.SetDefaultModel("worker-model")
	executor.SetAgentRoleSource(func(string) string { return "deep" })

	display := &AgentConfig{displayOnly: true}
	if got := executor.routeModelRef(display, tool.AgentExecRequest{}); got != "worker-model" {
		t.Fatalf("default worker reference = %q, want the configured worker model", got)
	}
	if got := executor.routeModelRef(&AgentConfig{}, tool.AgentExecRequest{}); got != "worker-model" {
		t.Fatalf("nameless definition reference = %q, want the worker model", got)
	}
}

// TestAssignedAgentRoleReachesTheRun proves the reference is not merely spelled
// differently: preparation resolves it through the role resolver, so the run
// talks to the assigned role's provider and model.
func TestAssignedAgentRoleReachesTheRun(t *testing.T) {
	bound := &namedProvider{label: "deep-vendor"}
	executor, _ := roleExecutor(t, func(role string) RoleAssignment {
		if role != "deep" {
			return RoleAssignment{}
		}
		return RoleAssignment{Provider: bound, ModelID: "opus", Effort: "high", Assigned: true}
	})
	executor.SetAgentRoleSource(func(string) string { return "deep" })

	run, err := executor.prepareRun(context.Background(), tool.AgentExecRequest{
		Agent: "quick", Prompt: "Rename the symbol",
		ResolvedAgentConfig: quickConfig(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.cfg.provider != bound || run.cfg.modelID != "opus" || run.cfg.effort != "high" {
		t.Fatalf("run = %v/%q/%q, want the deep role's provider, model and effort",
			run.cfg.provider, run.cfg.modelID, run.cfg.effort)
	}
}

// TestEffectiveModelNameFollowsTheAssignment keeps approval honest: the model
// the user approves is the one the assignment sends the work to, not the one the
// tier would have.
func TestEffectiveModelNameFollowsTheAssignment(t *testing.T) {
	bound := &namedProvider{label: "deep-vendor"}
	executor, _ := roleExecutor(t, func(role string) RoleAssignment {
		if role != "deep" {
			return RoleAssignment{}
		}
		return RoleAssignment{Provider: bound, ModelID: "opus", Assigned: true}
	})
	executor.SetAgentRoleSource(func(string) string { return "deep" })

	if got := executor.effectiveModelNameAtTier(quickConfig(t), "quick", "fast"); got != "opus" {
		t.Fatalf("approved model = %q, want the assigned role's opus", got)
	}
}
