package subagent

import (
	"context"
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/tool"
)

// namedProvider is a stubProvider with an identity, so a test can tell the
// parent's provider from the one a role resolved to.
type namedProvider struct {
	stubProvider
	label string
}

func (p *namedProvider) Name() string { return p.label }

// roleExecutor builds an executor whose parent model is parent-model on a
// provider of its own, so a test can tell the session's provider from the one a
// role binding resolves to.
func roleExecutor(t *testing.T, resolve RoleResolver) (*Executor, llm.Provider) {
	t.Helper()
	parent := &namedProvider{label: "parent"}
	executor := NewExecutor(parent, t.TempDir(), "parent-model", nil)
	executor.SetRoleResolver(resolve)
	return executor, parent
}

// TestAgentModelRoleResolvesToAssignedModel covers the promise of "@role" in an
// agent definition: the definition names the kind of work, and the user's
// modelRoles binding decides which model — and which vendor — answers it.
func TestAgentModelRoleResolvesToAssignedModel(t *testing.T) {
	var bound *namedProvider
	executor, _ := roleExecutor(t, func(role string) RoleAssignment {
		if role != string(llm.RoleSlow) {
			return RoleAssignment{}
		}
		return RoleAssignment{Provider: bound, ModelID: "opus", Assigned: true}
	})
	bound = &namedProvider{label: "bound"}

	run, err := executor.prepareRun(context.Background(), tool.AgentExecRequest{
		Agent: "review", Prompt: "Review the diff",
		ResolvedAgentConfig: &AgentConfig{Name: "review", Model: "@slow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.cfg.modelID != "opus" || run.cfg.provider != bound {
		t.Fatalf("role model = %v/%q, want the bound provider and opus", run.cfg.provider, run.cfg.modelID)
	}
}

// TestAgentModelRoleFallsBackToSessionModel keeps the failure mode safe: a role
// nobody assigned, a name that is not a role at all, and a resolver that says
// nothing all run on the parent model instead of failing the session.
func TestAgentModelRoleFallsBackToSessionModel(t *testing.T) {
	cases := []struct {
		name  string
		model string
		set   bool
	}{
		{"unassigned role", "@slow", true},
		{"unknown role", "@nobody", true},
		{"default role", "@default", true},
		{"garbage alias with a space", "@Slow Role", true},
		{"bare alias prefix", "@", true},
		{"no resolver wired", "@slow", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var executor *Executor
			if tc.set {
				executor, _ = roleExecutor(t, func(string) RoleAssignment {
					return RoleAssignment{}
				})
			} else {
				executor = NewExecutor(&namedProvider{label: "parent"}, t.TempDir(), "parent-model", nil)
			}
			parent := executor.provider

			run, err := executor.prepareRun(context.Background(), tool.AgentExecRequest{
				Agent: "review", Prompt: "Review the diff",
				ResolvedAgentConfig: &AgentConfig{Name: "review", Model: tc.model},
			})
			if err != nil {
				t.Fatal(err)
			}
			if run.cfg.provider != parent || run.cfg.modelID != "parent-model" {
				t.Fatalf("%s model = %v/%q, want the parent model", tc.model, run.cfg.provider, run.cfg.modelID)
			}
		})
	}
}

// TestAgentModelRoleLosesToRequestButWinsOverDefault pins the precedence around
// a role: the per-call override still wins, and a named definition's role beats
// the default worker model — a role is a configuration, not a fallback.
func TestAgentModelRoleLosesToRequestButWinsOverDefault(t *testing.T) {
	var bound *namedProvider
	executor, _ := roleExecutor(t, func(role string) RoleAssignment {
		if role == string(llm.RoleTask) {
			return RoleAssignment{Provider: bound, ModelID: "task-model", Assigned: true}
		}
		return RoleAssignment{}
	})
	bound = &namedProvider{label: "bound"}
	executor.SetDefaultModel("default-worker")

	run, err := executor.prepareRun(context.Background(), tool.AgentExecRequest{
		Agent: "worker", Prompt: "Implement the change",
		ResolvedAgentConfig: &AgentConfig{Name: "worker", Model: "@task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.cfg.modelID != "task-model" || run.cfg.provider != bound {
		t.Fatalf("task role = %v/%q, want the bound provider and task-model", run.cfg.provider, run.cfg.modelID)
	}

	run, err = executor.prepareRun(context.Background(), tool.AgentExecRequest{
		Agent: "worker", Prompt: "Implement the change", Model: "explicit-model",
		ResolvedAgentConfig: &AgentConfig{Name: "worker", Model: "@task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.cfg.modelID != "explicit-model" {
		t.Fatalf("request override = %q, want explicit-model", run.cfg.modelID)
	}
}

// TestAgentModelRoleCarriesEffort pins the second half of a role binding: the
// depth the user bound to the role travels with the model the role supplied, so
// an "@explore" definition thinks exactly as hard as the explore role says.
func TestAgentModelRoleCarriesEffort(t *testing.T) {
	bound := &namedProvider{label: "bound"}
	executor, _ := roleExecutor(t, func(role string) RoleAssignment {
		if role == string(llm.RoleExplore) {
			return RoleAssignment{Provider: bound, ModelID: "haiku", Effort: "xhigh", Assigned: true}
		}
		return RoleAssignment{}
	})

	run, err := executor.prepareRun(context.Background(), tool.AgentExecRequest{
		Agent: "scout", Prompt: "Find the parser entry point",
		ResolvedAgentConfig: &AgentConfig{Name: "scout", Model: "@explore"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.cfg.effort != "xhigh" {
		t.Fatalf("run effort = %q, want the explore role's xhigh", run.cfg.effort)
	}
	if client := newRunClient(run.cfg); client.ThinkingEffort() != "xhigh" {
		t.Fatalf("client effort = %q, want the role effort to reach the request", client.ThinkingEffort())
	}

	// A definition with no role — or a role with no effort bound — sends no
	// effort at all: the model's own default is the honest answer.
	plain, _ := roleExecutor(t, func(role string) RoleAssignment {
		return RoleAssignment{Provider: bound, ModelID: "haiku", Assigned: true}
	})
	run, err = plain.prepareRun(context.Background(), tool.AgentExecRequest{
		Agent: "scout", Prompt: "Find the parser entry point",
		ResolvedAgentConfig: &AgentConfig{Name: "scout", Model: "@slow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.cfg.effort != "" {
		t.Fatalf("unbound effort = %q, want none", run.cfg.effort)
	}

	// The per-call override names its own model, so the role's depth does not
	// apply to it either.
	run, err = executor.prepareRun(context.Background(), tool.AgentExecRequest{
		Agent: "scout", Prompt: "Find the parser entry point", Model: "explicit-model",
		ResolvedAgentConfig: &AgentConfig{Name: "scout", Model: "@explore"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.cfg.modelID != "explicit-model" || run.cfg.effort != "" {
		t.Fatalf("override = %q/%q, want explicit-model with no role effort", run.cfg.modelID, run.cfg.effort)
	}
}

// TestApprovalShowsResolvedRoleModel covers what the user approves: the prompt
// names the model that will actually answer, not the slot the definition holds.
func TestApprovalShowsResolvedRoleModel(t *testing.T) {
	var bound *namedProvider
	executor, _ := roleExecutor(t, func(role string) RoleAssignment {
		if role == string(llm.RoleSlow) {
			return RoleAssignment{Provider: bound, ModelID: "opus", Assigned: true}
		}
		return RoleAssignment{}
	})
	bound = &namedProvider{label: "bound"}
	executor.registry = NewRegistry()
	executor.registry.Register(&AgentConfig{Name: "review", Model: "@slow"})
	executor.registry.Register(&AgentConfig{Name: "other", Model: "@slow"})
	executor.registry.Register(&AgentConfig{Name: "concrete", Model: "named-worker"})

	adapter := NewExecutorAdapter(executor)
	if info, _, ok := adapter.ResolveAgentSelection("review"); !ok || info.Model != "opus" {
		t.Fatalf("assigned role shown as %q, want opus", info.Model)
	}
	if info, _, ok := adapter.ResolveAgentSelection("concrete"); !ok || info.Model != "named-worker" {
		t.Fatalf("concrete model shown as %q, want named-worker", info.Model)
	}

	// The same definition with nothing assigned shows the session model: the
	// run will use it, so approval must not imply "@slow".
	unassigned, _ := roleExecutor(t, func(string) RoleAssignment { return RoleAssignment{} })
	unassigned.registry = NewRegistry()
	unassigned.registry.Register(&AgentConfig{Name: "review", Model: "@slow"})
	if info, _, ok := NewExecutorAdapter(unassigned).ResolveAgentSelection("review"); !ok || info.Model != "parent-model" {
		t.Fatalf("unassigned role shown as %q, want the parent model", info.Model)
	}
}
