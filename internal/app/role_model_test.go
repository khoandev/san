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
// modelRoles binding, then the session model. It covers the three roles this
// work routes consumer calls through — fast (judges, hooks, the self-learning
// review), advisor (the review, which owns a role and therefore has no explicit
// setting to outrank it), standard (the default worker).
func TestRoleModelPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		roles  map[string]string
		worker string // subagentModel
		judge  string // autoPilot.model
		want   map[string]string
	}{
		{
			name: "unassigned roles follow the session model",
			want: map[string]string{"fast": "session-model", "advisor": "session-model", "standard": ""},
		},
		{
			name:  "role bindings answer when no override is set",
			roles: map[string]string{"fast": "haiku", "advisor": "opus", "standard": "worker"},
			want:  map[string]string{"fast": "haiku", "advisor": "opus", "standard": "worker"},
		},
		{
			name:   "an explicit setting wins over the role",
			roles:  map[string]string{"fast": "haiku", "advisor": "opus", "standard": "worker"},
			worker: "explicit-worker",
			judge:  "explicit-judge",
			// The advisor has no setting of its own: its role's binding is the
			// only source, so the judge's and worker's overrides cannot move it.
			want: map[string]string{"fast": "explicit-judge", "advisor": "opus", "standard": "explicit-worker"},
		},
		{
			name:   "an explicit inherit still wins over the standard role",
			roles:  map[string]string{"standard": "worker"},
			worker: "inherit",
			want:   map[string]string{"fast": "session-model", "advisor": "session-model", "standard": "inherit"},
		},
		{
			name:  "an unconnected vendor falls back to the session model",
			roles: map[string]string{"fast": "openai/gpt-5", "advisor": "openai/gpt-5", "standard": "openai/gpt-5"},
			// standard is a reference handed to the executor, which resolves it
			// later; the other two resolve now.
			want: map[string]string{"fast": "session-model", "advisor": "session-model", "standard": "openai/gpt-5"},
		},
		{
			name:  "an unconnected vendor override falls back too",
			roles: map[string]string{"fast": "haiku", "advisor": "opus"},
			judge: "openai/gpt-5",
			want:  map[string]string{"fast": "session-model", "advisor": "opus", "standard": ""},
		},
		{
			name:  "an at-alias chain resolves through the other role",
			roles: map[string]string{"fast": "@deep", "deep": "opus"},
			want:  map[string]string{"fast": "opus", "advisor": "session-model", "standard": ""},
		},
		{
			name:  "a retired key still answers its rung",
			roles: map[string]string{"smol": "haiku", "slow": "opus"},
			want:  map[string]string{"fast": "haiku", "advisor": "session-model", "standard": ""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, provider := roleModel(tc.roles, tc.worker)

			judgeProvider, judgeModel := m.resolveJudgeModel(tc.judge)
			if judgeModel != tc.want["fast"] {
				t.Errorf("judge model = %q, want %q", judgeModel, tc.want["fast"])
			}
			if judgeProvider != provider {
				t.Errorf("judge provider = %v, want the session provider", judgeProvider)
			}

			adviceProvider, adviceModel := m.resolveAdvisorModel()
			if adviceModel != tc.want["advisor"] {
				t.Errorf("advisor model = %q, want %q", adviceModel, tc.want["advisor"])
			}
			if adviceProvider != provider {
				t.Errorf("advisor provider = %v, want the session provider", adviceProvider)
			}

			if got := m.workerModel(); got != tc.want["standard"] {
				t.Errorf("standard role = %q, want %q", got, tc.want["standard"])
			}
		})
	}
}

// roleModelWithEfforts is roleModel with modelRoleEfforts bound and a provider
// that advertises a reasoning ladder, so effort resolution is checked against a
// live model catalog rather than a stub that agrees with itself.
func roleModelWithEfforts(roles, efforts map[string]string, ladder []string, def string) (*model, *testThinkingProvider) {
	provider := &testThinkingProvider{efforts: ladder, def: def}
	return &model{
		env: env{
			LLMProvider:  provider,
			CurrentModel: &llm.CurrentModelInfo{ModelID: "session-model", Provider: llm.Anthropic},
		},
		services: services{
			LLM: &llm.Conn{},
			Setting: setting.New(&setting.Data{
				ModelRoles:       roles,
				ModelRoleEfforts: efforts,
			}),
		},
	}, provider
}

// TestResolveRoleEffortValidatesAgainstTheModel keeps a stored effort a hint,
// not a value: only a rung the model that will answer actually offers is sent,
// and a label it does not offer falls back to that model's default instead of
// spending a request the vendor rejects.
func TestResolveRoleEffortValidatesAgainstTheModel(t *testing.T) {
	m, _ := roleModelWithEfforts(
		map[string]string{"explore": "haiku", "reviewer": "@slow"},
		map[string]string{"explore": "high", "reviewer": "ultra", "plan": "low"},
		[]string{"low", "medium", "high"}, "medium",
	)

	if got := m.resolveRoleEffort(m.resolveRole(llm.RoleExplore)); got != "high" {
		t.Errorf("explore effort = %q, want its bound high", got)
	}
	// reviewer is unassigned, so the slow role supplies its model — and the
	// named role's own effort still wins, rescaled to the model's ladder.
	if got := m.resolveRoleEffort(m.resolveRole(llm.RoleReviewer)); got != "medium" {
		t.Errorf("reviewer effort = %q, want the model default for a rung it lacks", got)
	}
	// A role nobody bound to anything sends no effort at all.
	if got := m.resolveRoleEffort(m.resolveRole(llm.RoleTask)); got != "" {
		t.Errorf("task effort = %q, want none", got)
	}

	// A model with no ladder to climb sends no effort, whatever is stored.
	plain, _ := roleModelWithEfforts(map[string]string{"explore": "haiku"}, map[string]string{"explore": "high"}, nil, "")
	if got := plain.resolveRoleEffort(plain.resolveRole(llm.RoleExplore)); got != "" {
		t.Errorf("non-reasoning model effort = %q, want none", got)
	}
}

// TestAutopilotJudgeEffortPrecedence pins the judge's order: the rung saved on
// the judge itself wins, then the effort bound to the role behind its model.
func TestAutopilotJudgeEffortPrecedence(t *testing.T) {
	provider := &testThinkingProvider{efforts: []string{"low", "medium", "high"}, def: "medium"}
	info := &llm.CurrentModelInfo{ModelID: "judge-model", Provider: llm.Anthropic}

	if got := autopilotJudgeEffort("low", provider, nil, info, "high"); got != "low" {
		t.Fatalf("saved rung = %q, want the judge's own low", got)
	}
	if got := autopilotJudgeEffort("", provider, nil, info, "high"); got != "high" {
		t.Fatalf("role rung = %q, want the role's high", got)
	}
	if got := autopilotJudgeEffort("", provider, nil, info, ""); got != "" {
		t.Fatalf("no rung = %q, want none", got)
	}
	// A saved rung the judge model does not offer is validated away rather than
	// sent: the model's default answers.
	if got := autopilotJudgeEffort("ultra", provider, nil, info, "high"); got != "medium" {
		t.Fatalf("invalid saved rung = %q, want the model default", got)
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
	if p, id := m.resolveJudgeModel(""); p != provider || id != "session-model" {
		t.Fatalf("resolveJudgeModel = %v, %q; want the session model", p, id)
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

	if got := resolve("slow"); !got.Assigned || got.ModelID != "opus" || got.Provider != provider {
		t.Fatalf("slow = %+v, want the session provider and opus", got)
	}
	// A role bound to another role follows the chain instead of stopping.
	if got := resolve("smol"); !got.Assigned || got.ModelID != "opus" {
		t.Fatalf("smol chain = %+v, want opus", got)
	}
	// Nothing assigned, and a vendor this session has not connected, both leave
	// the session model to answer rather than failing the worker.
	for _, role := range []string{"designer", "librarian"} {
		if got := resolve(role); got.Assigned {
			t.Fatalf("%s resolved to %+v, want the session fallback", role, got)
		}
	}
	// The bindings are read per call, so a settings change is visible.
	roles["designer"] = "haiku"
	if got := resolve("designer"); !got.Assigned || got.ModelID != "haiku" {
		t.Fatalf("designer = %+v, want the newly assigned haiku", got)
	}
}

// TestSubagentRoleResolverCarriesEffort covers what the executor receives: the
// depth bound to a role travels with the model it supplied, validated against
// that model; a role bound to an effort alone still gets its depth on the
// session model, and a role bound to nothing hands the run back untouched.
func TestSubagentRoleResolverCarriesEffort(t *testing.T) {
	m, provider := roleModelWithEfforts(
		map[string]string{"explore": "haiku", "reviewer": "opus"},
		map[string]string{"explore": "high", "reviewer": "ultra", "budget": "low"},
		[]string{"low", "medium", "high"}, "medium",
	)
	resolve := roleResolverFor(m.modelRoleBindings, m.llmStore(), m.env.LLMProvider, m.env.GetModelID())

	if got := resolve("explore"); !got.Assigned || got.ModelID != "haiku" || got.Effort != "high" || got.Provider != provider {
		t.Fatalf("explore = %+v, want haiku with the role's high on the session provider", got)
	}
	// "ultra" is not on this model's ladder: the model's default answers.
	if got := resolve("reviewer"); got.Effort != "medium" {
		t.Fatalf("reviewer = %+v, want the model default for a rung it lacks", got)
	}
	// An effort with no model of its own still reaches the run, on the session
	// model the role inherits. "budget" is a custom role: a name the catalog
	// does not describe also occupies no row, so nothing can supply its model.
	if got := resolve("budget"); !got.Assigned || got.ModelID != "session-model" || got.Effort != "low" {
		t.Fatalf("budget = %+v, want the session model with the role's low", got)
	}
	// Nothing bound anywhere: the executor keeps its own parent model.
	if got := resolve("task"); got.Assigned || got.Effort != "" {
		t.Fatalf("task = %+v, want an unassigned result", got)
	}
}

// TestRoleResolverWithoutBindings keeps the resolver safe before settings are
// wired: no bindings means every role follows the session model.
func TestRoleResolverWithoutBindings(t *testing.T) {
	provider := &switchProviderStub{name: "anthropic"}
	resolve := roleResolverFor(nil, nil, provider, "session-model")
	if got := resolve("slow"); got.Assigned || got.Provider != nil || got.ModelID != "" || got.Effort != "" {
		t.Fatalf("slow = %+v, want an unassigned result the executor turns into the session model", got)
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
		model, effort, inherited := m.agentModelForRef(tc.ref)
		if model != tc.model || inherited != tc.inherited {
			t.Errorf("agentModelForRef(%q) = %q/%v, want %q/%v", tc.ref, model, inherited, tc.model, tc.inherited)
		}
		if effort != "" {
			t.Errorf("agentModelForRef(%q) reported effort %q with none bound", tc.ref, effort)
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
