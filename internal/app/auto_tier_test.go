package app

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/sdk-go/pkg/acp/protocol"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/aitest"
)

// autoTierProvider answers one canned reply per Client() and records the models
// it was asked for, which is how these tests tell "classified" from "never
// asked" and which rung the classifier ran on.
type autoTierProvider struct {
	reply  string
	calls  int
	models []string
}

func (p *autoTierProvider) Client(model string, _ map[string]string) (*ai.Client, error) {
	p.calls++
	p.models = append(p.models, model)
	return aitest.Always(aitest.Says(p.reply)).Client(), nil
}

func (p *autoTierProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (p *autoTierProvider) Name() string                                        { return "auto-tier-stub" }

// isAutoTierRequest reports whether a request is the brief classifier's, by the
// opening line of its system prompt. Fixture drivers answer it without spending
// a scripted turn on a call that is not the conversation.
func isAutoTierRequest(req *ai.Request) bool {
	return strings.Contains(req.System, "You route one delegation brief")
}

// autoTierRegistry holds the definitions a routed call may name.
func autoTierRegistry() *subagent.Registry {
	registry := subagent.NewRegistry()
	registry.Register(&subagent.AgentConfig{Name: "explore", Model: "@fast", PermissionMode: subagent.PermissionExplore})
	registry.Register(&subagent.AgentConfig{Name: "quick", Model: "@fast", PermissionMode: subagent.PermissionAcceptEdits})
	registry.Register(&subagent.AgentConfig{Name: "task", Model: "@standard", PermissionMode: subagent.PermissionAcceptEdits})
	return registry
}

// autoTierResolver is the role resolver an agent executor receives: only the
// roles listed in bindings answer, everything else is unassigned.
func autoTierResolver(provider llm.Provider, bindings map[llm.ModelRole]string) subagent.RoleResolver {
	return func(role string) subagent.RoleAssignment {
		model := bindings[llm.ModelRole(role)]
		if model == "" {
			return subagent.RoleAssignment{}
		}
		return subagent.RoleAssignment{Provider: provider, ModelID: model, Assigned: true}
	}
}

// autoTierAdapter builds the pair the wiring needs: the adapter under test and
// the session provider installAutoTier falls back to, on session-model.
func autoTierAdapter(t *testing.T, registry *subagent.Registry, resolve subagent.RoleResolver) (*subagent.ExecutorAdapter, llm.Provider) {
	t.Helper()
	parent := &autoTierProvider{reply: "parent"}
	executor := subagent.NewExecutor(parent, t.TempDir(), "session-model", nil)
	executor.ConfigureOwner(nil, registry, nil, nil)
	executor.SetParentPermissionMode(func() subagent.PermissionMode { return subagent.PermissionDefault })
	executor.SetRoleResolver(resolve)
	return subagent.NewExecutorAdapter(executor), parent
}

// TestInstallAutoTier pins the switch and the model fallback chain: the setting
// must be on, and the classifier runs on the fast role, else the standard role,
// else the session's own model. Only a session with nothing to classify with
// leaves the adapter without a router, which is the pre-routing behaviour.
func TestInstallAutoTier(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		bindings   map[llm.ModelRole]string
		wantRouted bool
		wantModel  string
	}{
		{"on and fast is bound", true, map[llm.ModelRole]string{llm.RoleFast: "fast-model"}, true, "fast-model"},
		{"off", false, map[llm.ModelRole]string{llm.RoleFast: "fast-model"}, false, ""},
		{"standard answers when fast is unassigned", true, map[llm.ModelRole]string{llm.RoleStandard: "standard-model"}, true, "standard-model"},
		{"session model answers when no role is bound", true, nil, true, "session-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := autoTierRegistry()
			// One provider serves both as the role target and as the session
			// fallback, so wherever the chain lands the call is recorded here.
			classifier := &autoTierProvider{reply: `{"agent":"quick","tier":"fast","reason":"one rename"}`}
			resolve := autoTierResolver(classifier, tc.bindings)
			adapter, _ := autoTierAdapter(t, registry, resolve)
			installAutoTier(adapter, registry, tc.enabled, resolve, classifier, "session-model")

			decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Prompt: "rename the field"})
			if routed != tc.wantRouted {
				t.Fatalf("routed = %v, want %v (decision %+v)", routed, tc.wantRouted, decision)
			}
			if !routed {
				if classifier.calls != 0 {
					t.Fatalf("classifier called %d times without a router", classifier.calls)
				}
				return
			}
			if decision.Agent != "quick" || decision.Tier != "fast" {
				t.Fatalf("decision = %+v, want quick/fast", decision)
			}
			if classifier.calls != 1 || len(classifier.models) != 1 || classifier.models[0] != tc.wantModel {
				t.Fatalf("classifier ran on %v, want one classification on %s", classifier.models, tc.wantModel)
			}
		})
	}
}

// TestClassifierBindingChain pins the chain itself, including the effort that
// belongs to the rung that answered: a role's effort must travel with its
// model, while the session fallback has no role and keeps the model default.
func TestClassifierBindingChain(t *testing.T) {
	session := &autoTierProvider{reply: "parent"}
	fast := &autoTierProvider{reply: "fast"}
	standard := &autoTierProvider{reply: "standard"}

	for _, tc := range []struct {
		name         string
		resolve      subagent.RoleResolver
		wantProvider llm.Provider
		wantModel    string
		wantEffort   string
		wantRole     string
	}{
		{"fast wins", autoTierResolver(fast, map[llm.ModelRole]string{llm.RoleFast: "fast-model", llm.RoleStandard: "standard-model"}), fast, "fast-model", "", "fast"},
		{"standard is next", autoTierResolver(standard, map[llm.ModelRole]string{llm.RoleStandard: "standard-model"}), standard, "standard-model", "", "standard"},
		{"session is last", autoTierResolver(standard, nil), session, "session-model", "", "session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, model, effort, role := classifierBinding(tc.resolve, session, "session-model")
			if provider != tc.wantProvider {
				t.Fatalf("provider = %v, want the %s rung's provider", provider, tc.wantRole)
			}
			if model != tc.wantModel || effort != tc.wantEffort || role != tc.wantRole {
				t.Fatalf("binding = %s/%q/%s, want %s/%q/%s", model, effort, role, tc.wantModel, tc.wantEffort, tc.wantRole)
			}
		})
	}

	// The role's own effort rides along with its model.
	withEffort := func(role string) subagent.RoleAssignment {
		if role == string(llm.RoleStandard) {
			return subagent.RoleAssignment{Provider: standard, ModelID: "standard-model", Effort: "high", Assigned: true}
		}
		return subagent.RoleAssignment{}
	}
	if _, _, effort, _ := classifierBinding(withEffort, session, "session-model"); effort != "high" {
		t.Fatalf("effort = %q, want the standard role's effort", effort)
	}

	// Nothing to classify with at all: no model, so no judge.
	if provider, model, _, role := classifierBinding(autoTierResolver(nil, nil), nil, ""); provider != nil || model != "" || role != "" {
		t.Fatalf("binding = %v/%q/%s, want none", provider, model, role)
	}
}

// TestAutoTierEnabledFollowsTheSetting pins the TUI switch read: absent
// settings mean on, an explicit false turns it off.
func TestAutoTierEnabledFollowsTheSetting(t *testing.T) {
	if !(&model{}).autoTierEnabled() {
		t.Fatal("a model without settings must follow the default: on")
	}
	off := false
	toggled := &model{services: services{Setting: setting.New(&setting.Data{Agents: setting.AgentsSettings{AutoTier: &off}})}}
	if toggled.autoTierEnabled() {
		t.Fatal("agents.autoTier=false was ignored")
	}
}

// TestInstallAutoTierNeverRoutesAContinuation keeps the promise the Agent tool
// makes to a resumed run: its policy is the one it was approved under.
func TestInstallAutoTierNeverRoutesAContinuation(t *testing.T) {
	registry := autoTierRegistry()
	classifier := &autoTierProvider{reply: `{"agent":"quick","tier":"fast"}`}
	resolve := autoTierResolver(classifier, map[llm.ModelRole]string{llm.RoleFast: "fast-model"})
	adapter, session := autoTierAdapter(t, registry, resolve)
	installAutoTier(adapter, registry, true, resolve, session, "session-model")

	if decision, routed := adapter.RouteAgent(context.Background(), tool.AgentRouteRequest{Prompt: "continue", Resume: true}); routed {
		t.Fatalf("a continuation was routed: %+v", decision)
	}
	if classifier.calls != 0 {
		t.Fatalf("classifier called %d times for a continuation", classifier.calls)
	}
}

// writeAutoTierSettings writes the session settings an ACP fixture reads back
// when it opens a session: the fast role bound to a model, and the switch as
// the case under test. nil leaves the switch unset (default on).
func writeAutoTierSettings(t *testing.T, autoTier *bool) {
	t.Helper()
	settings := map[string]any{
		"modelRoles": map[string]string{"fast": "test-model"},
	}
	if autoTier != nil {
		settings["agents"] = map[string]any{"autoTier": *autoTier}
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(os.Getenv("HOME"), ".san")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestACPSharesTheTierRouterWithTheTUI proves the wiring rather than the
// helper: an ACP session spends one classification inference on an unnamed
// delegation, and spends none when agents.autoTier is false.
func TestACPSharesTheTierRouterWithTheTUI(t *testing.T) {
	off := false
	for _, tc := range []struct {
		name     string
		autoTier *bool
		want     int32
	}{
		{"default on", nil, 1},
		{"explicit off", &off, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var classified atomic.Int32
			driver := aitest.Always(func(ctx context.Context, req *ai.Request) iter.Seq2[ai.Delta, error] {
				switch {
				case isAutoTierRequest(req):
					classified.Add(1)
					return aitest.Says(`{"agent":"quick","tier":"fast","reason":"one line"}`)(ctx, req)
				case workerRequest(req):
					return aitest.Says("worker completed")(ctx, req)
				case hasResult(req):
					return aitest.Says("parent completed")(ctx, req)
				}
				return aitest.Asks(ai.ToolCall{
					ID:    "launch",
					Name:  "Agent",
					Input: `{"prompt":"WORKER_BRIEF","description":"router test"}`,
				})(ctx, req)
			})
			f := newACPFixture(t, driver, func(context.Context, protocol.RequestPermissionRequest) (protocol.RequestPermissionResponse, error) {
				return acpChoice("allow_once"), nil
			})
			writeAutoTierSettings(t, tc.autoTier)
			id := f.session(t, t.TempDir())
			if _, err := f.prompt(id, "delegate"); err != nil {
				t.Fatal(err)
			}
			if got := classified.Load(); got != tc.want {
				t.Fatalf("classification inferences = %d, want %d", got, tc.want)
			}
		})
	}
}
