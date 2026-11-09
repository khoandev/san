// The advisor reviews on a model role, not on a hardcoded rung: agents.roles
// decides which one, deep answers when the agents tab assigned nothing, and a
// legacy advisor.model still means what it used to.
package app

import (
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// advisorRoleModel builds the smallest model the advisor helpers read: a
// session provider/model plus live settings carrying modelRoles and the agent
// role assignments. No provider store is connected, so bare ids stay on the
// session provider the way an unqualified binding does.
func advisorRoleModel(t *testing.T, roles, agentRoles map[string]string) *model {
	t.Helper()
	provider := &switchProviderStub{name: "anthropic"}
	data := &setting.Data{ModelRoles: roles}
	if agentRoles != nil {
		data.Agents.Roles = agentRoles
	}
	return &model{
		env: env{
			LLMProvider:  provider,
			CurrentModel: &llm.CurrentModelInfo{ModelID: "session-model", Provider: llm.Anthropic},
		},
		services: services{
			LLM:     &llm.Conn{},
			Setting: setting.New(data),
		},
	}
}

// The assignment wins over the deep default: moving the advisor row to another
// role moves the review, with no per-model setting involved.
func TestAdvisorReviewsOnTheAssignedRole(t *testing.T) {
	bindings := map[string]string{"fast": "haiku", "standard": "worker", "deep": "opus"}

	m := advisorRoleModel(t, bindings, nil)
	if _, modelID := m.resolveAdvisorModel(setting.AdvisorSettings{}); modelID != "opus" {
		t.Fatalf("unassigned advisor model = %q, want the deep role's opus", modelID)
	}

	m = advisorRoleModel(t, bindings, map[string]string{setting.AgentAdvisor: "fast"})
	if _, modelID := m.resolveAdvisorModel(setting.AdvisorSettings{}); modelID != "haiku" {
		t.Fatalf("assigned advisor model = %q, want the fast role's haiku", modelID)
	}

	// The lookup folds case, the way the agents.roles fold writes it.
	m = advisorRoleModel(t, bindings, map[string]string{"Advisor": "standard"})
	if _, modelID := m.resolveAdvisorModel(setting.AdvisorSettings{}); modelID != "worker" {
		t.Fatalf("assigned advisor model = %q, want the standard role's worker", modelID)
	}
}

// A legacy advisor.model keeps its old meanings: "@role" names a routing slot,
// a concrete reference pins the model — until an agents-tab role assignment
// supersedes the pin, the precedence UpdateAgentsAt writes to the file.
func TestAdvisorLegacyModelStillApplies(t *testing.T) {
	bindings := map[string]string{"fast": "haiku", "deep": "opus"}

	m := advisorRoleModel(t, bindings, nil)
	if _, modelID := m.resolveAdvisorModel(setting.AdvisorSettings{Model: "@fast"}); modelID != "haiku" {
		t.Fatalf("legacy @fast model = %q, want haiku", modelID)
	}
	// A bare id stays on the session provider, so the pin is visible without a
	// connected provider store.
	if _, modelID := m.resolveAdvisorModel(setting.AdvisorSettings{Model: "claude-sonnet-4-5"}); modelID != "claude-sonnet-4-5" {
		t.Fatalf("legacy pin = %q, want the pinned model", modelID)
	}

	assigned := advisorRoleModel(t, bindings, map[string]string{setting.AgentAdvisor: "deep"})
	if _, modelID := assigned.resolveAdvisorModel(setting.AdvisorSettings{Model: "claude-sonnet-4-5"}); modelID != "opus" {
		t.Fatalf("assigned role = %q, want it to supersede the pin", modelID)
	}
}

// A role change has to reach a running session: the signature names the role it
// resolved, so syncAdvisor rewires instead of keeping the old model.
func TestAdvisorSignatureFollowsTheRole(t *testing.T) {
	on := true
	cfg := setting.AdvisorSettings{Enable: &on}
	bindings := map[string]string{"fast": "haiku", "deep": "opus"}

	deep := advisorRoleModel(t, bindings, map[string]string{setting.AgentAdvisor: "deep"})
	deep.env.Advisor = cfg
	deepSig := deep.advisorSignature()

	fast := advisorRoleModel(t, bindings, map[string]string{setting.AgentAdvisor: "fast"})
	fast.env.Advisor = cfg
	fastSig := fast.advisorSignature()

	if deepSig == fastSig {
		t.Fatalf("role change did not move the signature: %q", deepSig)
	}
	if deepSig == "" || fastSig == "" {
		t.Fatal("an enabled advisor needs a signature")
	}

	// A different model behind the same role moves it too: a modelRoles edit
	// must rewire as well.
	moved := advisorRoleModel(t, map[string]string{"fast": "sonnet", "deep": "opus"}, map[string]string{setting.AgentAdvisor: "fast"})
	moved.env.Advisor = cfg
	if moved.advisorSignature() == fastSig {
		t.Fatalf("model change did not move the signature: %q", fastSig)
	}

	// A disabled advisor has no review to describe.
	off := advisorRoleModel(t, bindings, nil)
	if got := off.advisorSignature(); got != "" {
		t.Fatalf("disabled advisor signature = %q, want empty", got)
	}
}

// An unconnected vendor in the role's binding still leaves the advisor on the
// session model rather than failing the review.
func TestAdvisorRoleFallsBackToTheSessionModel(t *testing.T) {
	// A custom role is legal, and its binding names a vendor this session has
	// not connected.
	m := advisorRoleModel(t, map[string]string{"custom-role": "openai/gpt-5-mini"}, map[string]string{setting.AgentAdvisor: "custom-role"})
	_, modelID := m.resolveAdvisorModel(setting.AdvisorSettings{})
	if modelID != "session-model" {
		t.Fatalf("unresolvable role = %q, want the session model", modelID)
	}
}
