package app

import (
	"testing"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/mcp"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/setting"
)

// planModeModel is a model wired well enough to build agent params and to hold
// a runtime: the resumed harness (session writer, permissions, reminder) plus
// the plan store and a session provider/model.
func planModeModel(t *testing.T, roles map[string]string) *model {
	t.Helper()
	m, _ := resumedModel(t, nil)
	m.plan = plan.NewStore()
	m.services.MCP = mcp.NewManagerForTest(nil)
	m.env.LLMProvider = &switchProviderStub{name: "anthropic"}
	m.env.CurrentModel = &llm.CurrentModelInfo{ModelID: "m", Provider: llm.Anthropic}
	m.services.Setting = setting.New(&setting.Data{ModelRoles: roles})
	return m
}

// startPlanRuntime starts a live runtime the way a session has one, so the test
// can tell a stopped runtime from a reused one. Nothing is sent, so the runtime
// stays idle while its run is active.
func startPlanRuntime(t *testing.T, m *model) {
	t.Helper()
	provider := &restartStubProvider{requests: make(chan []core.Message, 1)}
	if err := m.services.Agent.Start(agent.BuildParams{Provider: provider, ModelID: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	if !m.services.Agent.Active() {
		t.Fatal("runtime did not start")
	}
}

// Plan Mode builds with the plan role's model when one is assigned, and with the
// session model when the role is not — never moving the session's own choice.
func TestBuildAgentParamsUsesPlanRole(t *testing.T) {
	cases := []struct {
		name  string
		roles map[string]string
		want  string
	}{
		{name: "assigned plan role answers the turn", roles: map[string]string{"plan": "plan-model"}, want: "plan-model"},
		{name: "unassigned plan role keeps the session model", want: "m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := planModeModel(t, tc.roles)
			m.enterPlanMode()

			params, err := m.buildAgentParams()
			if err != nil {
				t.Fatal(err)
			}
			if params.ModelID != tc.want {
				t.Fatalf("plan-mode model = %q, want %q", params.ModelID, tc.want)
			}
			// A bare model id stays on the session's provider.
			if params.Provider != m.env.LLMProvider {
				t.Fatal("plan-mode build switched provider for a bare model id")
			}
			if got := m.env.GetModelID(); got != "m" {
				t.Fatalf("session model changed to %q", got)
			}

			wantLabel := "m"
			if tc.want != "m" {
				wantLabel = tc.want + " · plan"
			}
			if got := m.env.GetModelDisplayName(); got != wantLabel {
				t.Fatalf("status-line model = %q, want %q", got, wantLabel)
			}

			// Leaving Plan Mode restores the session's label without touching
			// the stored model.
			m.leavePlanMode()
			if got := m.env.GetModelDisplayName(); got != "m" {
				t.Fatalf("label after leaving plan mode = %q, want the session model", got)
			}
			if got := m.env.GetModelID(); got != "m" {
				t.Fatalf("session model after leaving plan mode = %q", got)
			}
		})
	}
}

// Plan Mode sends the plan role's own reasoning effort. The session's selection
// belongs to the session model, so it is not carried to a different model the
// plan role routes the turn to; without a role effort it is rescaled to that
// model's ladder instead of being dropped.
func TestBuildAgentParamsUsesPlanRoleEffort(t *testing.T) {
	ladder := &testThinkingProvider{efforts: []string{"low", "medium", "high"}, def: "medium"}
	build := func(t *testing.T, efforts map[string]string) agent.BuildParams {
		t.Helper()
		m := planModeModel(t, map[string]string{"plan": "plan-model"})
		m.services.Setting = setting.New(&setting.Data{
			ModelRoles:       map[string]string{"plan": "plan-model"},
			ModelRoleEfforts: efforts,
		})
		m.env.LLMProvider = ladder
		m.env.ThinkingEffort = "low"
		m.enterPlanMode()
		params, err := m.buildAgentParams()
		if err != nil {
			t.Fatal(err)
		}
		if params.ModelID != "plan-model" {
			t.Fatalf("plan-mode model = %q, want plan-model", params.ModelID)
		}
		return params
	}

	if got := build(t, map[string]string{"plan": "high"}).ThinkingEffort; got != "high" {
		t.Fatalf("plan effort = %q, want the role's high over the session's low", got)
	}
	if got := build(t, nil).ThinkingEffort; got != "low" {
		t.Fatalf("plan effort without a role binding = %q, want the session's low on the plan model", got)
	}
}

// Toggling Plan Mode restarts the runtime only when the plan role moves the turn
// to a different model and nothing is in flight.
func TestPlanModeToggleRestartsIdleRuntimeOnly(t *testing.T) {
	const planModel = "plan-model"

	t.Run("idle runtime restarts for the plan model", func(t *testing.T) {
		m := planModeModel(t, map[string]string{"plan": planModel})
		startPlanRuntime(t, m)
		m.enterPlanMode()
		if m.services.Agent.Active() {
			t.Fatal("the idle runtime was kept for a different plan model")
		}
		if got := m.env.GetModelID(); got != "m" {
			t.Fatalf("session model changed to %q", got)
		}
	})

	t.Run("busy runtime keeps the turn it started", func(t *testing.T) {
		m := planModeModel(t, map[string]string{"plan": planModel})
		startPlanRuntime(t, m)
		m.conv.Stream.Active = true
		m.enterPlanMode()
		if !m.services.Agent.Active() {
			t.Fatal("a runtime in the middle of a turn was stopped")
		}
	})

	t.Run("unassigned role leaves the runtime alone", func(t *testing.T) {
		m := planModeModel(t, nil)
		startPlanRuntime(t, m)
		m.enterPlanMode()
		if !m.services.Agent.Active() {
			t.Fatal("Plan Mode restarted a runtime nothing asked to move")
		}
	})

	t.Run("leaving plan mode returns to the session model", func(t *testing.T) {
		m := planModeModel(t, map[string]string{"plan": planModel})
		m.enterPlanMode()
		startPlanRuntime(t, m)
		// The runtime was built under Plan Mode, on the plan model.
		m.agentPlanModel = m.planModelSignature()
		m.leavePlanMode()
		if m.services.Agent.Active() {
			t.Fatal("leaving Plan Mode kept the plan model's runtime")
		}
	})
}

// A session resumed while planning builds on the plan role's model, and its
// stored model is still the session's own.
func TestResumedPlanModeBuildsPlanModel(t *testing.T) {
	m := planModeModel(t, map[string]string{"plan": "plan-model"})
	m.plan.EnterPlanMode()
	state := m.plan.Snapshot()

	resumed := planModeModel(t, map[string]string{"plan": "plan-model"})
	resumed.restorePlanState(state)
	if resumed.plan.Mode() != plan.ModePlan {
		t.Fatalf("restored mode = %q, want plan", resumed.plan.Mode())
	}

	params, err := resumed.buildAgentParams()
	if err != nil {
		t.Fatal(err)
	}
	if params.ModelID != "plan-model" {
		t.Fatalf("resumed plan-mode model = %q, want the plan role's model", params.ModelID)
	}
	if got := resumed.env.GetModelID(); got != "m" {
		t.Fatalf("resume moved the session model to %q", got)
	}
	if got := resumed.env.GetModelDisplayName(); got != "plan-model · plan" {
		t.Fatalf("resumed status-line model = %q", got)
	}
}
