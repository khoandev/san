// The advisor reviews on its own model role, not on a hardcoded rung and not on
// a model setting of its own: modelRoles.advisor (bound in /models) decides,
// and an unassigned role reviews on the session model.
package app

import (
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// advisorRoleModel builds the smallest model the advisor helpers read: a
// session provider/model plus live settings carrying modelRoles. No provider
// store is connected, so bare ids stay on the session provider the way an
// unqualified binding does.
func advisorRoleModel(t *testing.T, roles map[string]string) *model {
	t.Helper()
	provider := &switchProviderStub{name: "anthropic"}
	data := &setting.Data{ModelRoles: roles}
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

// The advisor role's binding moves the review, with no per-advisor model setting
// involved.
func TestAdvisorReviewsOnItsRole(t *testing.T) {
	bindings := map[string]string{"fast": "haiku", "deep": "opus", setting.AgentAdvisor: "opus"}

	m := advisorRoleModel(t, bindings)
	if _, modelID := m.resolveAdvisorModel(); modelID != "opus" {
		t.Fatalf("advisor model = %q, want the advisor role's opus", modelID)
	}

	// Moving the role moves the review; the other rungs have nothing to do with
	// it.
	m = advisorRoleModel(t, map[string]string{"fast": "haiku", "deep": "opus", setting.AgentAdvisor: "haiku"})
	if _, modelID := m.resolveAdvisorModel(); modelID != "haiku" {
		t.Fatalf("advisor model = %q, want the role's haiku", modelID)
	}

	// A binding written under another spelling of the key still answers.
	m = advisorRoleModel(t, map[string]string{"Advisor": "sonnet"})
	if _, modelID := m.resolveAdvisorModel(); modelID != "sonnet" {
		t.Fatalf("advisor model = %q, want the bound sonnet", modelID)
	}
}

// An unassigned advisor role reviews on the session model, the same fallback
// every unassigned role takes.
func TestAdvisorRoleFallsBackToTheSessionModel(t *testing.T) {
	m := advisorRoleModel(t, map[string]string{"deep": "opus"})
	if _, modelID := m.resolveAdvisorModel(); modelID != "session-model" {
		t.Fatalf("unassigned advisor model = %q, want the session model", modelID)
	}

	// A role bound to a vendor this session has not connected falls back too,
	// rather than failing the review.
	m = advisorRoleModel(t, map[string]string{setting.AgentAdvisor: "openai/gpt-5-mini"})
	if _, modelID := m.resolveAdvisorModel(); modelID != "session-model" {
		t.Fatalf("unresolvable role = %q, want the session model", modelID)
	}
}

// A role change has to reach a running session: the signature names the model it
// resolved, so syncAdvisor rewires instead of keeping the old one.
func TestAdvisorSignatureFollowsTheRole(t *testing.T) {
	on := true
	cfg := setting.AdvisorSettings{Enable: &on}

	opus := advisorRoleModel(t, map[string]string{setting.AgentAdvisor: "opus"})
	opus.env.Advisor = cfg
	opusSig := opus.advisorSignature()

	haiku := advisorRoleModel(t, map[string]string{setting.AgentAdvisor: "haiku"})
	haiku.env.Advisor = cfg
	haikuSig := haiku.advisorSignature()

	if opusSig == haikuSig {
		t.Fatalf("role change did not move the signature: %q", opusSig)
	}
	if opusSig == "" || haikuSig == "" {
		t.Fatal("an enabled advisor needs a signature")
	}

	// A disabled advisor has no review to describe.
	off := advisorRoleModel(t, nil)
	if got := off.advisorSignature(); got != "" {
		t.Fatalf("disabled advisor signature = %q, want empty", got)
	}
}
