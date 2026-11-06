package app

import (
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// roleModel builds the smallest model the role helpers read: a session
// provider/model and a settings service holding modelRoles. No provider store
// is connected, so a vendor-qualified binding cannot resolve — which is exactly
// the stale-configuration case the fallback has to survive.
func roleModel(roles map[string]string, subagentModel string) (*model, *switchProviderStub) {
	provider := &switchProviderStub{name: "anthropic"}
	return &model{
		env: env{
			LLMProvider:  provider,
			CurrentModel: &llm.CurrentModelInfo{ModelID: "session-model", Provider: llm.Anthropic},
		},
		services: services{
			LLM: &llm.Conn{},
			Setting: setting.New(&setting.Data{
				ModelRoles:    roles,
				SubagentModel: subagentModel,
			}),
		},
	}, provider
}

// TestRoleModelPrecedence pins the one precedence rule every role consumer
// follows: an explicit setting for that consumer wins, then the role's
// modelRoles binding, then the session model. It covers the three roles the plan
// routes work through — smol (judges, hooks, the self-learning review), slow
// (the advisor), task (the default worker).
func TestRoleModelPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		roles  map[string]string
		worker string // subagentModel
		smol   string // autoPilot.model
		slow   string // advisor.model
		want   map[string]string
	}{
		{
			name: "unassigned roles follow the session model",
			want: map[string]string{"smol": "session-model", "slow": "session-model", "task": ""},
		},
		{
			name:  "role bindings answer when no override is set",
			roles: map[string]string{"smol": "haiku", "slow": "opus", "task": "worker"},
			want:  map[string]string{"smol": "haiku", "slow": "opus", "task": "worker"},
		},
		{
			name:   "an explicit setting wins over the role",
			roles:  map[string]string{"smol": "haiku", "slow": "opus", "task": "worker"},
			worker: "explicit-worker",
			smol:   "explicit-judge",
			slow:   "explicit-review",
			want:   map[string]string{"smol": "explicit-judge", "slow": "explicit-review", "task": "explicit-worker"},
		},
		{
			name:   "an explicit inherit still wins over the task role",
			roles:  map[string]string{"task": "worker"},
			worker: "inherit",
			want:   map[string]string{"smol": "session-model", "slow": "session-model", "task": "inherit"},
		},
		{
			name:  "an unconnected vendor falls back to the session model",
			roles: map[string]string{"smol": "openai/gpt-5", "slow": "openai/gpt-5", "task": "openai/gpt-5"},
			// task is a reference handed to the executor, which resolves it
			// later; the other two resolve now.
			want: map[string]string{"smol": "session-model", "slow": "session-model", "task": "openai/gpt-5"},
		},
		{
			name:  "an unconnected vendor override falls back too",
			roles: map[string]string{"smol": "haiku", "slow": "opus"},
			smol:  "openai/gpt-5",
			slow:  "openai/gpt-5",
			want:  map[string]string{"smol": "session-model", "slow": "session-model", "task": ""},
		},
		{
			name:  "an at-alias chain resolves through the other role",
			roles: map[string]string{"smol": "@slow", "slow": "opus"},
			want:  map[string]string{"smol": "opus", "slow": "opus", "task": ""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, provider := roleModel(tc.roles, tc.worker)

			smolProvider, smolModel := m.resolveReviewerModel(tc.smol)
			if smolModel != tc.want["smol"] {
				t.Errorf("smol model = %q, want %q", smolModel, tc.want["smol"])
			}
			if smolProvider != provider {
				t.Errorf("smol provider = %v, want the session provider", smolProvider)
			}

			slowProvider, slowModel := m.resolveAdvisorModel(setting.AdvisorSettings{Model: tc.slow})
			if slowModel != tc.want["slow"] {
				t.Errorf("slow model = %q, want %q", slowModel, tc.want["slow"])
			}
			if slowProvider != provider {
				t.Errorf("slow provider = %v, want the session provider", slowProvider)
			}

			if got := m.workerModel(); got != tc.want["task"] {
				t.Errorf("task role = %q, want %q", got, tc.want["task"])
			}
		})
	}
}

// TestRoleModelWithoutSettings keeps the helpers safe before settings are wired:
// a bare test model (or a first-build model) must resolve to the session model
// rather than panic on a nil service.
func TestRoleModelWithoutSettings(t *testing.T) {
	provider := &switchProviderStub{name: "anthropic"}
	m := &model{
		env: env{
			LLMProvider:  provider,
			CurrentModel: &llm.CurrentModelInfo{ModelID: "session-model"},
		},
	}
	if p, id := m.resolveReviewerModel(""); p != provider || id != "session-model" {
		t.Fatalf("resolveReviewerModel = %v, %q; want the session model", p, id)
	}
	if got := m.workerModel(); got != "" {
		t.Fatalf("workerModel = %q, want empty (inherit)", got)
	}
}

// TestSubagentRoleResolver covers the resolver an agent executor receives: a
// definition holding "@slow" must run on whatever modelRoles assigns to slow,
// read live so a settings edit reaches an executor that is already built, and
// fall back to the session model for anything unassigned or unreachable.
func TestSubagentRoleResolver(t *testing.T) {
	roles := map[string]string{"slow": "opus", "smol": "@slow", "librarian": "openai/gpt-5"}
	m, provider := roleModel(roles, "")
	resolve := roleResolverFor(m.modelRoleBindings, m.llmStore(), m.env.LLMProvider, m.env.GetModelID())

	if p, id, ok := resolve("slow"); !ok || id != "opus" || p != provider {
		t.Fatalf("slow = %v/%q/%v, want the session provider and opus", p, id, ok)
	}
	// A role bound to another role follows the chain instead of stopping.
	if _, id, ok := resolve("smol"); !ok || id != "opus" {
		t.Fatalf("smol chain = %q/%v, want opus", id, ok)
	}
	// Nothing assigned, and a vendor this session has not connected, both leave
	// the session model to answer rather than failing the worker.
	for _, role := range []string{"designer", "librarian"} {
		if _, id, ok := resolve(role); ok {
			t.Fatalf("%s resolved to %q, want the session fallback", role, id)
		}
	}
	// The bindings are read per call, so a settings change is visible.
	roles["designer"] = "haiku"
	if _, id, ok := resolve("designer"); !ok || id != "haiku" {
		t.Fatalf("designer = %q/%v, want the newly assigned haiku", id, ok)
	}
}

// TestRoleResolverWithoutBindings keeps the resolver safe before settings are
// wired: no bindings means every role follows the session model.
func TestRoleResolverWithoutBindings(t *testing.T) {
	provider := &switchProviderStub{name: "anthropic"}
	resolve := roleResolverFor(nil, nil, provider, "session-model")
	if p, id, ok := resolve("slow"); ok || p != provider || id != "session-model" {
		t.Fatalf("slow = %v/%q/%v, want the session model", p, id, ok)
	}
}

// TestAgentModelForRef covers what /agents reads: the model an agent definition
// will actually run on. A role is resolved through the same bindings the
// executor reads, and anything unassigned says the session model answered, so
// the list never advertises a model the run will not use.
func TestAgentModelForRef(t *testing.T) {
	m, _ := roleModel(map[string]string{"slow": "opus"}, "")

	for _, tc := range []struct {
		ref, model string
		inherited  bool
	}{
		{"@slow", "opus", false},
		{"@smol", "session-model", true},
		{"@default", "session-model", true},
		{"haiku", "haiku", false},
		{"inherit", "session-model", true},
		{"  ", "session-model", true},
		// An alias that names no role is a typo, not a model id: the session
		// model answers it rather than the provider receiving "@" as a model.
		{"@", "session-model", true},
	} {
		model, inherited := m.agentModelForRef(tc.ref)
		if model != tc.model || inherited != tc.inherited {
			t.Errorf("agentModelForRef(%q) = %q/%v, want %q/%v", tc.ref, model, inherited, tc.model, tc.inherited)
		}
	}
}

// TestEffectiveWorkerModel covers the shared precedence rule the headless
// runner uses, which holds a *setting.Data rather than the TUI model.
func TestEffectiveWorkerModel(t *testing.T) {
	for _, tc := range []struct {
		subagentModel, taskRole, want string
	}{
		{"", "", ""},
		{"", "worker", "worker"},
		{"explicit", "worker", "explicit"},
		{"inherit", "worker", "inherit"},
		{"  explicit  ", "", "explicit"},
		{"", "  worker  ", "worker"},
	} {
		if got := effectiveWorkerModel(tc.subagentModel, tc.taskRole); got != tc.want {
			t.Errorf("effectiveWorkerModel(%q, %q) = %q, want %q", tc.subagentModel, tc.taskRole, got, tc.want)
		}
	}
}
